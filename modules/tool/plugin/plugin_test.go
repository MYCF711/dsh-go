package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
)

const sample = `[profile]
name = "test"

[[module]]
id      = "tool-a"
backend = "static"

[[module]]
id      = "tool-b"
backend = "static"

[[module]]
id      = "tool-c"
backend = "static"
enabled = false
`

func newTool(t *testing.T) (*Tool, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "dsh.toml")
	if err := os.WriteFile(p, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Tool{manifest: p}, p
}

func exec(t *testing.T, tl *Tool, raw string) (string, error) {
	t.Helper()
	res, err := tl.Exec(context.Background(), json.RawMessage(raw))
	return res.Summary + "|" + res.Text, err
}

// list 要如实报出每个模块的开合状态。
func TestListShowsState(t *testing.T) {
	tl, _ := newTool(t)
	out, err := exec(t, tl, `{"action":"list"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tool-a") || !strings.Contains(out, "tool-c") {
		t.Fatalf("没列出模块：%s", out)
	}
	if !strings.Contains(out, "off tool-c") {
		t.Fatalf("tool-c 是关着的，要标出来：%s", out)
	}
}

// disable 之后文件里必须真的多了 enabled = false，而且还能被 kernel 解析。
func TestDisableWritesAndStaysParseable(t *testing.T) {
	tl, p := newTool(t)
	if _, err := exec(t, tl, `{"action":"disable","id":"tool-a"}`); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if !strings.Contains(string(body), "enabled = false") {
		t.Fatalf("没写进 enabled=false：%s", body)
	}
	if _, err := kernel.LoadManifest(p); err != nil {
		t.Fatalf("改完的 manifest 解析不过：%v", err)
	}
	// 再列一次：tool-a 应当是关的
	out, _ := exec(t, tl, `{"action":"list"}`)
	if !strings.Contains(out, "off tool-a") {
		t.Fatalf("disable 之后 tool-a 应当是 off：%s", out)
	}
}

// enable 把 enabled 行删掉（回到默认开），并留下 .bak。
func TestEnableRemovesFlagAndBacksUp(t *testing.T) {
	tl, p := newTool(t)
	if _, err := exec(t, tl, `{"action":"enable","id":"tool-c"}`); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if strings.Contains(string(body), "enabled = false") {
		t.Fatalf("enable 之后不该还有 enabled=false：%s", body)
	}
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Fatal("没留备份：出事就没法回滚")
	}
}

// 不存在的 id 必须拒绝，并列出可选的——否则模型下一步还是瞎猜。
func TestUnknownIDIsRefused(t *testing.T) {
	tl, p := newTool(t)
	_, err := exec(t, tl, `{"action":"disable","id":"tool-zzz"}`)
	if err == nil {
		t.Fatal("不存在的 id 必须报错")
	}
	body, _ := os.ReadFile(p)
	if string(body) != sample {
		t.Fatal("失败的调用不该改动文件")
	}
}

// 重复操作要幂等：已经关着的再关一次，不许重复插入 enabled 行。
func TestDisableIsIdempotent(t *testing.T) {
	tl, p := newTool(t)
	if _, err := exec(t, tl, `{"action":"disable","id":"tool-a"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := exec(t, tl, `{"action":"disable","id":"tool-a"}`); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if n := strings.Count(string(body), "enabled = false"); n != 2 {
		// tool-c 本来就有一条，tool-a 只该多一条
		t.Fatalf("enabled=false 出现 %d 次，说明重复插入了：%s", n, body)
	}
}

// 结果里必须写明"下次启动生效"——本仓没有热重载，说已经生效是骗人。
func TestSaysRestartRequired(t *testing.T) {
	tl, _ := newTool(t)
	res, err := tl.Exec(context.Background(), json.RawMessage(`{"action":"disable","id":"tool-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "重启") {
		t.Fatalf("要说明下次启动生效：%s", res.Text)
	}
	m, _ := res.Data.(map[string]any)
	if m["restartRequired"] != true {
		t.Fatalf("Data.restartRequired 应当为 true：%v", m)
	}
}

// Caps：会改 dsh.toml，必须按写报（审批要能看到）。
func TestCapsDeclareWrites(t *testing.T) {
	if !(&Tool{}).Caps().Writes {
		t.Fatal("plugin 会改 manifest，Caps.Writes 必须为真")
	}
}

// enabled 行要紧挨着本块的内容，不能落在空行后面——
// 那样读起来像下一块的配置，人改文件时会看错。
func TestDisablePutsFlagInsideBlock(t *testing.T) {
	tl, p := newTool(t)
	if _, err := exec(t, tl, `{"action":"disable","id":"tool-a"}`); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	lines := strings.Split(string(body), "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "enabled = false" && i > 0 && strings.TrimSpace(lines[i-1]) == "backend = \"static\"" {
			return // 紧跟在 backend 后面，正确
		}
	}
	t.Fatalf("enabled=false 没紧跟在块内容后面：\n%s", body)
}

// 没有 manifest 不是错误：宿主没有白名单时就是"全开"，这是合法配置。
//
// 真机上（tool-live 探针用一个空 home）就是这种情况：
// 以前它返回 ERR，看起来像"插件系统坏了"，其实只是这台机器没建过这个文件。
func TestMissingManifestIsNotAnError(t *testing.T) {
	tl := &Tool{manifest: filepath.Join(t.TempDir(), "no-such.toml")}
	res, err := tl.Exec(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatalf("没有 manifest 不该报错：%v", err)
	}
	if !strings.Contains(res.Text, "默认全开") && !strings.Contains(res.Summary, "全开") {
		t.Fatalf("要说清这是'默认全开'：%s / %s", res.Summary, res.Text)
	}
}

// 但 enable/disable 没有 manifest 就没法改，得报错并指路。
func TestToggleWithoutManifestFails(t *testing.T) {
	tl := &Tool{manifest: filepath.Join(t.TempDir(), "no-such.toml")}
	if _, err := tl.Exec(context.Background(), json.RawMessage(`{"action":"disable","id":"tool-a"}`)); err == nil {
		t.Fatal("没有 manifest 时改不了，应当报错")
	}
}

// ---------------------------------------------------------------------------
// 回滚诚实性：「回滚失败却报告成功」是**没有症状**的缺陷（用户以为配置干净，
// 实际留下一个下次启动直接解析失败的 manifest），所以这里钉三件事：
//   1. 回滚写回失败必须能被观察到（错误 + writeDirty 状态 + .bak 位置）；
//   2. 回滚成功必须有**回读校验**，不许凭"写没报错"就说已回滚；
//   3. 写失败但文件其实没变时，不许谎报一次"已回滚"。
// ---------------------------------------------------------------------------

// 回滚写回失败：状态必须是 writeDirty，错误要能被 errors.Is 识别、
// 要指出原文备份位置，而且**绝不能**出现"已回滚"。
func TestRollbackWriteFailureIsReported(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dsh.toml")
	original := []byte("[profile]\nname = \"test\"\n")
	if err := os.WriteFile(p, original, 0o644); err != nil {
		t.Fatal(err)
	}
	manifestWrites := 0
	tl := &Tool{manifest: p, writeFile: func(name string, data []byte, perm os.FileMode) error {
		if name == p {
			manifestWrites++
			if manifestWrites > 1 { // 第 1 次是改动本体，第 2 次是回滚写回
				return errors.New("injected rollback write failure")
			}
		}
		return os.WriteFile(name, data, perm)
	}}
	// 直接喂一段解析不过的内容，走"解析失败 → 回滚"这条路。
	state, err := tl.writeGuarded([]string{"[[module]", `id = "x"`}, original)
	if err == nil {
		t.Fatal("解析不过必须报错")
	}
	if state != writeDirty {
		t.Fatalf("回滚写回失败时状态必须是 writeDirty，拿到 %v", state)
	}
	if !errors.Is(err, errWriteNotRestored) {
		t.Fatalf("错误要能被 errors.Is(errWriteNotRestored) 识别：%v", err)
	}
	if strings.Contains(err.Error(), "已回滚") {
		t.Fatalf("回滚没成功却说已回滚：%v", err)
	}
	if !strings.Contains(err.Error(), ".bak") {
		t.Fatalf("要指出原文备份在哪，人才能捞回来：%v", err)
	}
	body, readErr := os.ReadFile(p)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) == string(original) {
		t.Fatalf("夹具没生效：文件竟然已经回到原文（%q）", body)
	}
}

// 解析失败但回滚成功：状态是 writeRestored，错误里要写"回读校验一致"，
// 磁盘上必须逐字节回到原文。
func TestParseFailureRestoresAndVerifies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dsh.toml")
	original := []byte("[profile]\nname = \"test\"\n")
	if err := os.WriteFile(p, original, 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := (&Tool{manifest: p}).writeGuarded([]string{"[[module]", `id = "x"`}, original)
	if err == nil {
		t.Fatal("解析不过必须报错")
	}
	if state != writeRestored {
		t.Fatalf("回滚成功时状态必须是 writeRestored，拿到 %v", state)
	}
	if !strings.Contains(err.Error(), "回读校验一致") {
		t.Fatalf("回滚成功要说明是回读校验过的：%v", err)
	}
	if errors.Is(err, errWriteNotRestored) {
		t.Fatalf("回滚成功了就不该带 errWriteNotRestored：%v", err)
	}
	body, readErr := os.ReadFile(p)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != string(original) {
		t.Fatalf("回滚后应与原文逐字节一致，实际：%q", body)
	}
}

// 写一半就失败（os.WriteFile 先截断）：回读发现文件已变，就必须回滚，
// 并如实说"改动落过盘、已还原"——不许当没事发生。
func TestPartialWriteFailureRestoresAndSaysSo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dsh.toml")
	original := []byte("[profile]\nname = \"test\"\n")
	if err := os.WriteFile(p, original, 0o644); err != nil {
		t.Fatal(err)
	}
	injected := false
	tl := &Tool{manifest: p, writeFile: func(name string, data []byte, perm os.FileMode) error {
		if name == p && !injected {
			injected = true
			if werr := os.WriteFile(name, []byte("[[module]"), perm); werr != nil {
				return werr
			}
			return errors.New("injected partial write failure")
		}
		return os.WriteFile(name, data, perm)
	}}
	state, err := tl.writeGuarded([]string{"[profile]", `name = "test"`, "", "[[module]]", `id = "tool-a"`}, original)
	if err == nil {
		t.Fatal("写失败必须报错")
	}
	if state != writeRestored {
		t.Fatalf("写失败但文件被改动过、随后还原成功，状态应是 writeRestored，拿到 %v", state)
	}
	if !strings.Contains(err.Error(), "写 manifest 失败") || !strings.Contains(err.Error(), "回读校验一致") {
		t.Fatalf("错误要说明先写失败、再已还原：%v", err)
	}
	body, readErr := os.ReadFile(p)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != string(original) {
		t.Fatalf("还原后应与原文逐字节一致，实际：%q", body)
	}
}

// 文件只读导致写失败、但文件一个字节都没变：提示必须说"原样未改"，
// 不许谎报"已回滚"（回滚动作根本没发生过）。
func TestReadOnlyManifestIsReportedAsUntouched(t *testing.T) {
	tl, p := newTool(t)
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	res, err := tl.Exec(context.Background(), json.RawMessage(`{"action":"disable","id":"tool-a"}`))
	if err == nil {
		t.Fatal("只读文件写不进去，必须报错")
	}
	if strings.Contains(res.Hint, "已回滚") {
		t.Fatalf("文件没变却说已回滚：%s", res.Hint)
	}
	if !strings.Contains(res.Hint, "原样未改") {
		t.Fatalf("要如实说文件没变：%s", res.Hint)
	}
	body, readErr := os.ReadFile(p)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != sample {
		t.Fatalf("失败的调用不该改动文件：%q", body)
	}
}
