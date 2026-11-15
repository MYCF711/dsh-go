package llm

// 官方 0.2.2-rc.2 `masked` 的**取值口**：明文不出包。
//
// 设计约束（Lead 裁定 A1）：`internal/llm` 只暴露"给 mask 函数、回掩码串"的方法，
// 明文既不进返回值、也不进错误字符串。这组用例就是那条约束的证据。

import (
	"strings"
	"testing"
)

// fakeMask 把明文换成固定预览，同时记录它见过哪些明文（只在测试里）。
func fakeMask(seen *[]string) func(string) string {
	return func(value string) string {
		*seen = append(*seen, value)
		return "<masked:" + strings.Repeat("x", len([]rune(value))) + ">"
	}
}

// TestDescribeCredentialMasksOnlyStoredLayer 钉住：只有本地存储层有掩码，
// env 层与未配置的引用都不出现（对应官方 describe 只在 source:'file' 填 masked）。
func TestDescribeCredentialMasksOnlyStoredLayer(t *testing.T) {
	home := t.TempDir()
	store := OpenModelCatalog(home)
	if _, err := store.SetCredential("DSH_STORED", "sk-stored-ABCDEFGHIJKLMNOP", nil); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	var seen []string
	masks, err := store.DescribeCredentialMasks([]string{"DSH_STORED", "DSH_ENV_ONLY", "DSH_NEVER_SET"}, fakeMask(&seen))
	if err != nil {
		t.Fatalf("DescribeCredentialMasks: %v", err)
	}
	if _, ok := masks["DSH_ENV_ONLY"]; ok {
		t.Fatalf("env 层不许有掩码：%v", masks)
	}
	if _, ok := masks["DSH_NEVER_SET"]; ok {
		t.Fatalf("未配置的引用不许有掩码：%v", masks)
	}
	got, ok := masks["DSH_STORED"]
	if !ok {
		t.Fatalf("stored 层必须有掩码：%v", masks)
	}
	if got != "<masked:"+strings.Repeat("x", len("sk-stored-ABCDEFGHIJKLMNOP"))+">" {
		t.Fatalf("掩码串不对：%q", got)
	}
	// mask 函数只该被调用一次（只对 stored 值）。
	if len(seen) != 1 || seen[0] != "sk-stored-ABCDEFGHIJKLMNOP" {
		t.Fatalf("mask 函数的调用次数/入参不对：%v", seen)
	}
}

// TestDescribeCredentialMasksNeverReturnsPlaintext 钉住：返回值里没有明文，
// 错误串里也没有明文（非法 ref 只报位置与长度）。
func TestDescribeCredentialMasksNeverReturnsPlaintext(t *testing.T) {
	home := t.TempDir()
	store := OpenModelCatalog(home)
	const secret = "sk-never-leak-0123456789ABCDEF"
	if _, err := store.SetCredential("DSH_LEAK_KEY", secret, nil); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	masks, err := store.DescribeCredentialMasks([]string{"DSH_LEAK_KEY"}, func(string) string { return "••••" })
	if err != nil {
		t.Fatalf("DescribeCredentialMasks: %v", err)
	}
	for ref, masked := range masks {
		if strings.Contains(masked, secret) || strings.Contains(masked, "0123456789") {
			t.Fatalf("掩码 %s 里出现了明文片段：%q", ref, masked)
		}
	}

	// 非法 ref：整批拒绝，错误串不回显名字（名字是调用方可控字段）。
	_, err = store.DescribeCredentialMasks([]string{"BAD-NAME"}, func(string) string { return "••••" })
	if err == nil {
		t.Fatal("非法 ref 必须整批拒绝")
	}
	if strings.Contains(err.Error(), "BAD-NAME") {
		t.Fatalf("错误串回显了 ref 名字：%v", err)
	}
}

// TestDescribeCredentialMasksNilMaskIsNoop 钉住 nil mask 不炸：没有掩码函数时
// 返回空表（调用方按"这个后端给不出 masked"处理），绝不退化成回明文。
func TestDescribeCredentialMasksNilMaskIsNoop(t *testing.T) {
	home := t.TempDir()
	store := OpenModelCatalog(home)
	if _, err := store.SetCredential("DSH_NIL_MASK", "sk-nil-mask-0123456789", nil); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	masks, err := store.DescribeCredentialMasks([]string{"DSH_NIL_MASK"}, nil)
	if err != nil {
		t.Fatalf("DescribeCredentialMasks(nil): %v", err)
	}
	if len(masks) != 0 {
		t.Fatalf("nil mask 必须返回空表：%v", masks)
	}
}

// TestDescribeCredentialMasksAfterUnset 钉住 unset 之后掩码消失（契约：set/unset 不变）。
func TestDescribeCredentialMasksAfterUnset(t *testing.T) {
	home := t.TempDir()
	store := OpenModelCatalog(home)
	if _, err := store.SetCredential("DSH_UNSET_MASK", "sk-unset-mask-0123456789", nil); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	if _, err := store.DeleteCredential("DSH_UNSET_MASK", nil); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	masks, err := store.DescribeCredentialMasks([]string{"DSH_UNSET_MASK"}, func(string) string { return "••••" })
	if err != nil {
		t.Fatalf("DescribeCredentialMasks: %v", err)
	}
	if _, ok := masks["DSH_UNSET_MASK"]; ok {
		t.Fatalf("unset 之后不许还有掩码：%v", masks)
	}
}
