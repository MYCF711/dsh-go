package transport

// 官方 0.2.2-rc.2 `credentials.describe` 的 `masked` 字段（R3-G08）契约测试。
//
// 官方原件：
//
//	@deepseek-ai/dsh-credentials/src/types.ts:62-75      CredentialInfo.masked?: string
//	@deepseek-ai/dsh-credentials-local/src/index.ts:522-530  maskCredential()
//	@deepseek-ai/dsh-credentials-local/src/index.ts:648-657  describe() 只在 source:'file' 填 masked
//
// 四类必测用例（任务书要求）：普通长密钥 / 短密钥整串 / 边界 hidden==8 / 非 file 来源。
// 另外补两类**安全**断言：多字节码点按码点算；describe 的整份 JSON 里不出现完整明文。

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"dshgo/internal/llm"
)

// ---------------------------------------------------------------------------
// 算法：逐字符对齐官方 maskCredential()
// ---------------------------------------------------------------------------

// TestOfficialMaskedPreviewLongKey 钉住普通长密钥：provider 标识 + 前 5 个字符保留，
// 中间按 hidden 个 '•'，末 4 个字符保留。
//
// 手算（"sk-proj-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"，44 个码点）：
//
//	providerEnd = 第一个 '-' 的下标 2 + 1 = 3
//	hidden      = 44 - 3 - 5 - 4 = 32
//	前缀         = chars[:3+5] = "sk-proj-"
//	后缀         = 末 4 个 = "7890"
func TestOfficialMaskedPreviewLongKey(t *testing.T) {
	const secret = "sk-proj-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
	want := "sk-proj-" + strings.Repeat("•", 32) + "7890"
	if got := officialMaskedPreview(secret); got != want {
		t.Fatalf("掩码 = %q，期望 %q", got, want)
	}
	// 长度守恒：掩码的长度必须等于明文码点数（官方 '•'.repeat(chars.length) 与
	// 前缀+hidden+后缀两条分支都满足）。长度变了就说明有人按字节算。
	if len([]rune(officialMaskedPreview(secret))) != len([]rune(secret)) {
		t.Fatalf("掩码长度 %d ≠ 明文码点数 %d", len([]rune(officialMaskedPreview(secret))), len([]rune(secret)))
	}
}

// TestOfficialMaskedPreviewShortKeyAllMasked 钉住"隐藏不到 8 个码点 → 整串掩码"。
func TestOfficialMaskedPreviewShortKeyAllMasked(t *testing.T) {
	const secret = "sk-abc" // providerEnd=3，hidden = 6-3-9 = -6 < 8
	if got, want := officialMaskedPreview(secret), "••••••"; got != want {
		t.Fatalf("短密钥必须整串掩码：得到 %q，期望 %q", got, want)
	}
}

// TestOfficialMaskedPreviewHiddenBoundary 钉住边界：官方判据是 `hidden < 8`，
// 所以 hidden==8 必须**保留**前后缀，hidden==7 必须整串掩码。差一就在这根线上。
func TestOfficialMaskedPreviewHiddenBoundary(t *testing.T) {
	// providerEnd=3（"sk-"），len = 3+9+8 = 20 → hidden=8：保留前后缀。
	const atBoundary = "sk-12345678901234567"
	want := "sk-12345" + strings.Repeat("•", 8) + "4567"
	if got := officialMaskedPreview(atBoundary); got != want {
		t.Fatalf("hidden==8 应当保留前后缀：得到 %q，期望 %q", got, want)
	}
	// len = 3+9+7 = 19 → hidden=7：整串掩码。
	const belowBoundary = "sk-1234567890123456"
	if got, want := officialMaskedPreview(belowBoundary), strings.Repeat("•", 19); got != want {
		t.Fatalf("hidden==7 必须整串掩码：得到 %q，期望 %q", got, want)
	}
}

// TestOfficialMaskedPreviewNoProviderSeparator 钉住"没有 '-'/'_' 时 providerEnd=0"
// （官方 findIndex 返回 -1，+1 之后就是 0）。
func TestOfficialMaskedPreviewNoProviderSeparator(t *testing.T) {
	const secret = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn" // 40 码点，无分隔符
	want := "ABCDE" + strings.Repeat("•", 31) + "klmn"
	if got := officialMaskedPreview(secret); got != want {
		t.Fatalf("无 provider 分隔符：得到 %q，期望 %q", got, want)
	}
	// 下划线也是分隔符（ghp_ 形式的 token）。
	const underscore = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" // 40 码点，providerEnd=4
	wantUnderscore := "ghp_ABCDE" + strings.Repeat("•", 27) + "6789"
	if got := officialMaskedPreview(underscore); got != wantUnderscore {
		t.Fatalf("下划线分隔：得到 %q，期望 %q", got, wantUnderscore)
	}
}

// TestOfficialMaskedPreviewCodePoints 钉住按**码点**而不是字节/UTF-16 码元计。
// 官方是 `Array.from(value)`；按字节算会得到不同的 '•' 个数。
func TestOfficialMaskedPreviewCodePoints(t *testing.T) {
	const secret = "sk-密钥-ABCDEFGHIJ" // 16 个码点，providerEnd=3，hidden=4 < 8
	got := officialMaskedPreview(secret)
	if want := strings.Repeat("•", 16); got != want {
		t.Fatalf("多字节明文必须按码点数掩码：得到 %q（%d 个字符），期望 %q", got, len([]rune(got)), want)
	}
}

// ---------------------------------------------------------------------------
// 投影：只有 source=='file'（catalog）层才有 masked
// ---------------------------------------------------------------------------

// TestMergeOfficialCredentialMasksSkipsEnvLayer 钉住"env/dotenv 来源不返回 masked"。
func TestMergeOfficialCredentialMasksSkipsEnvLayer(t *testing.T) {
	out := map[string]OfficialCredentialInfo{
		"DSH_STORED_KEY": {Configured: true, Source: llm.ModelCredentialSourceCatalog, Writable: true},
		"DSH_ENV_KEY":    {Configured: true, Source: llm.ModelCredentialSourceEnv, Writable: true},
	}
	mergeOfficialCredentialMasks(out, map[string]string{
		"DSH_STORED_KEY": "sk-proj" + strings.Repeat("•", 20) + "7890",
		"DSH_ENV_KEY":    "sk-bogus" + strings.Repeat("•", 10) + "0000", // 后端多给的必须丢掉
	})
	if out["DSH_STORED_KEY"].Masked == "" {
		t.Fatalf("catalog 层应当带 masked：%+v", out["DSH_STORED_KEY"])
	}
	if out["DSH_ENV_KEY"].Masked != "" {
		t.Fatalf("env 来源不许带 masked（官方 describe 只在 source:'file' 填）：%+v", out["DSH_ENV_KEY"])
	}
	// 未配置的引用也绝不能凭空长出一个 masked。
	out["DSH_NEVER_SET"] = OfficialCredentialInfo{Writable: true}
	mergeOfficialCredentialMasks(out, map[string]string{"DSH_NEVER_SET": strings.Repeat("•", 10)})
	if out["DSH_NEVER_SET"].Masked != "" {
		t.Fatalf("未配置的引用不许出现 masked：%+v", out["DSH_NEVER_SET"])
	}
}

// ---------------------------------------------------------------------------
// 端到端：真 catalog → describe → masked，且全 JSON 不含明文
// ---------------------------------------------------------------------------

func TestOfficialCredentialsDescribeReturnsMasked(t *testing.T) {
	home := t.TempDir()
	srv, store := newCatalogServer(t, home)
	const ref = "DSH_MASK_E2E_KEY"
	const secret = "sk-rc2-DEADBEEFCAFEBABE0123456789" // 33 码点，providerEnd=3，hidden=21
	if _, err := store.SetCredential(ref, secret, nil); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}

	code, out := callOfficialSettings(t, srv, "credentials.describe", map[string]any{"refs": []string{ref}})
	if code != http.StatusOK {
		t.Fatalf("credentials.describe 状态 %d：%v", code, out)
	}
	views := officialResult(t, out)
	entry, _ := views[ref].(map[string]any)
	if entry == nil {
		t.Fatalf("结果缺 %s：%v", ref, views)
	}
	if entry["configured"] != true {
		t.Fatalf("写过的凭据必须 configured=true：%v", entry)
	}
	if entry["source"] != llm.ModelCredentialSourceCatalog {
		t.Fatalf("source 应当是本地存储层 %q：%v", llm.ModelCredentialSourceCatalog, entry)
	}
	want := "sk-rc2-D" + strings.Repeat("•", 21) + "6789"
	if entry["masked"] != want {
		t.Fatalf("masked = %v，期望 %q", entry["masked"], want)
	}

	// 负向扫描：整份响应（含任何错误/附加字段）里都不许出现完整明文，
	// 也不许出现明文里超过 5 个连续字符的片段。
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("序列化响应: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("describe 响应里出现了完整明文：%s", string(encoded))
	}
	for _, probe := range []string{"DEADBEEF", "CAFEBABE", "0123456789"} {
		if strings.Contains(string(encoded), probe) {
			t.Fatalf("describe 响应里出现了明文片段 %q：%s", probe, string(encoded))
		}
	}
}

// TestOfficialCredentialsDescribeEnvLayerHasNoMask 钉住进程环境变量供货时没有 masked。
func TestOfficialCredentialsDescribeEnvLayerHasNoMask(t *testing.T) {
	const ref = "DSH_MASK_ENV_ONLY_KEY"
	t.Setenv(ref, "sk-env-only-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123")
	home := t.TempDir()
	srv, _ := newCatalogServer(t, home)

	code, out := callOfficialSettings(t, srv, "credentials.describe", map[string]any{"refs": []string{ref}})
	if code != http.StatusOK {
		t.Fatalf("credentials.describe 状态 %d：%v", code, out)
	}
	entry, _ := officialResult(t, out)[ref].(map[string]any)
	if entry == nil {
		t.Fatalf("结果缺 %s", ref)
	}
	if entry["source"] != llm.ModelCredentialSourceEnv {
		t.Fatalf("source 应当是 %q：%v", llm.ModelCredentialSourceEnv, entry)
	}
	if _, has := entry["masked"]; has {
		t.Fatalf("env 来源不许返回 masked：%v", entry)
	}
}

// TestOfficialCredentialsUnsetClearsMasked 钉住 set/unset 契约不变：
// unset 之后 masked 消失、configured 回落到 env 层（这里没有 env → false）。
func TestOfficialCredentialsUnsetClearsMasked(t *testing.T) {
	home := t.TempDir()
	srv, store := newCatalogServer(t, home)
	const ref = "DSH_MASK_UNSET_KEY"
	if _, err := store.SetCredential(ref, "sk-unset-me-ABCDEFGHIJKLMNOPQRSTUVWX", nil); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}

	code, out := callOfficialSettings(t, srv, "credentials.describe", map[string]any{"refs": []string{ref}})
	if code != http.StatusOK {
		t.Fatalf("set 之后 describe 状态 %d：%v", code, out)
	}
	if entry, _ := officialResult(t, out)[ref].(map[string]any); entry == nil || entry["masked"] == nil {
		t.Fatalf("set 之后必须有 masked：%v", out)
	}

	code, out = callOfficialSettings(t, srv, "credentials.unset", map[string]any{"ref": ref})
	if code != http.StatusOK {
		t.Fatalf("credentials.unset 状态 %d：%v", code, out)
	}
	code, out = callOfficialSettings(t, srv, "credentials.describe", map[string]any{"refs": []string{ref}})
	if code != http.StatusOK {
		t.Fatalf("unset 之后 describe 状态 %d：%v", code, out)
	}
	entry, _ := officialResult(t, out)[ref].(map[string]any)
	if entry == nil {
		t.Fatalf("unset 之后结果缺 %s", ref)
	}
	if _, has := entry["masked"]; has {
		t.Fatalf("unset 之后 masked 必须消失：%v", entry)
	}
	if entry["configured"] != false {
		t.Fatalf("unset 且无 env 回落时 configured 必须 false：%v", entry)
	}
}

// TestOfficialCredentialMaskNeverTouchesProcessEnv 钉住掩码只覆盖本地存储层：
// 同一个 ref 同时有 env 与 stored 时，source 是 catalog（stored 覆盖 env），
// masked 描述的是 stored 值，不是环境变量值。
func TestOfficialCredentialMaskNeverTouchesProcessEnv(t *testing.T) {
	const ref = "DSH_MASK_BOTH_LAYERS_KEY"
	t.Setenv(ref, "sk-from-env-ENVVALUEENVVALUEENVVALUE")
	home := t.TempDir()
	srv, store := newCatalogServer(t, home)
	if _, err := store.SetCredential(ref, "sk-from-file-FILEVALUEFILEVALUEFILE", nil); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}

	code, out := callOfficialSettings(t, srv, "credentials.describe", map[string]any{"refs": []string{ref}})
	if code != http.StatusOK {
		t.Fatalf("describe 状态 %d：%v", code, out)
	}
	entry, _ := officialResult(t, out)[ref].(map[string]any)
	if entry == nil {
		t.Fatalf("结果缺 %s", ref)
	}
	if entry["source"] != llm.ModelCredentialSourceCatalog {
		t.Fatalf("stored 必须覆盖 env：%v", entry)
	}
	masked, _ := entry["masked"].(string)
	if strings.Contains(masked, "ENVVALUE") || strings.Contains(masked, "FILEVALUE") {
		t.Fatalf("masked 不能含任一层明文的连续片段：%q", masked)
	}
	if os.Getenv(ref) == "" {
		t.Fatalf("测试前提被破坏：环境变量没设上")
	}
}
