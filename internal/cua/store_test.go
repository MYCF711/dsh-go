package cua

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// demoGrant 用一条**不存在的**路径造身份：本文件测的是状态机与持久化，
// 不依赖磁盘上真有那个 exe（真实图标提取在 platform_windows_test.go 里测）。
func demoGrant(name string) Grant {
	return ApplicationIdentity(filepath.Join(`C:\Apps`, name, name+".exe"))
}

func TestApplicationIdentityIsStableAndCaseInsensitive(t *testing.T) {
	upper := ApplicationIdentity(`C:\Apps\Demo\app.exe`)
	lower := ApplicationIdentity(`c:\apps\demo\APP.EXE`)
	if upper.ID != lower.ID {
		t.Fatalf("同一个 exe 的大小写变体算出了两个身份：%s vs %s（Windows 路径大小写不敏感，不归一化会让 revoke 只撤掉其中一个）",
			upper.ID, lower.ID)
	}
	if !ApplicationIDPattern.MatchString(upper.ID) {
		t.Fatalf("身份摘要不是官方 CuaApplicationId 形状：%q", upper.ID)
	}
	if upper.Name != "app.exe" {
		t.Fatalf("名称应取可执行文件名，得到 %q", upper.Name)
	}
	if upper.ExecutablePath == "" || strings.ToLower(upper.ExecutablePath) != `c:\apps\demo\app.exe` {
		t.Fatalf("规范路径不对：%q", upper.ExecutablePath)
	}
	if blank := ApplicationIdentity("   "); blank.ID != "" {
		t.Fatalf("空路径不该造出身份，得到 %+v", blank)
	}
}

func TestStoreStatusProjectsAlwaysAndDenials(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	always := demoGrant("always")
	denied := demoGrant("denied")
	allowedOnce := demoGrant("allowed-once")

	if err := store.GrantAlways(always); err != nil {
		t.Fatalf("GrantAlways: %v", err)
	}
	if err := store.Decide("s1", denied, DecisionDeny); err != nil {
		t.Fatalf("Decide deny: %v", err)
	}
	if err := store.Decide("s1", allowedOnce, DecisionAllow); err != nil {
		t.Fatalf("Decide allow: %v", err)
	}

	state, err := store.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !state.PersistentAvailable {
		t.Fatal("有状态目录时 persistentAvailable 应为 true")
	}
	if len(state.AllowedApps) != 1 || state.AllowedApps[0].ID != always.ID {
		t.Fatalf("allowedApps 只应含持久许可，得到 %+v", state.AllowedApps)
	}
	if len(state.BlockedApps) != 1 || state.BlockedApps[0].ID != denied.ID {
		t.Fatalf("blockedApps 应含被拒绝的那个应用，得到 %+v", state.BlockedApps)
	}
	// 会话级 allow 不是持久许可，也不进 blockedApps。
	for _, grant := range append(append([]Grant{}, state.AllowedApps...), state.BlockedApps...) {
		if grant.ID == allowedOnce.ID {
			t.Fatalf("会话级 allow 不该出现在任何集合里：%+v", grant)
		}
	}

	// 内存态组合：persistentAvailable 必须是 false（真话，不是能力声明）。
	memoryState, err := NewStore("").Status()
	if err != nil {
		t.Fatalf("内存态 Status: %v", err)
	}
	if memoryState.PersistentAvailable {
		t.Fatal("没有状态目录时 persistentAvailable 应为 false")
	}
	if len(memoryState.AllowedApps) != 0 || len(memoryState.BlockedApps) != 0 {
		t.Fatalf("新内存态应为空，得到 %+v", memoryState)
	}
}

func TestStoreAlwaysGrantWinsOverSessionDeny(t *testing.T) {
	store := NewStore(t.TempDir())
	grant := demoGrant("both")
	if err := store.Decide("s1", grant, DecisionDeny); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if err := store.GrantAlways(grant); err != nil {
		t.Fatalf("GrantAlways: %v", err)
	}
	state, err := store.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	// 官方 application-access.ts:71-78：有 always 就进 allowedApps，不进 blockedApps。
	if len(state.AllowedApps) != 1 || len(state.BlockedApps) != 0 {
		t.Fatalf("持久许可应压过会话拒绝：allowed=%+v blocked=%+v", state.AllowedApps, state.BlockedApps)
	}
}

func TestStoreRevokeChangesStateAndPersists(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	grant := demoGrant("revoked")
	if err := store.Decide("s1", grant, DecisionAllow); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if err := store.Revoke(grant.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	app := snapshot.Applications[grant.ID]
	if app.Revoked != 1 {
		t.Fatalf("revoked 应为 1，得到 %d", app.Revoked)
	}
	if app.Sessions["s1"].Decision != DecisionDeny {
		t.Fatalf("撤销后会话决策应为 deny，得到 %q", app.Sessions["s1"].Decision)
	}
	if app.Sessions["s1"].Revision != 2 {
		t.Fatalf("撤销应把 revision 推到 2（决策一次 + 撤销一次），得到 %d", app.Sessions["s1"].Revision)
	}

	// 换一个 Store 实例读同一目录 = 模拟重启：撤销必须留下。
	reopened, err := NewStore(dir).Status()
	if err != nil {
		t.Fatalf("重开 Status: %v", err)
	}
	if len(reopened.BlockedApps) != 1 || reopened.BlockedApps[0].ID != grant.ID {
		t.Fatalf("撤销没有持久化：blockedApps=%+v", reopened.BlockedApps)
	}

	// 再撤销一次：代数与 revision 继续前进（每次撤销都让旧快照失效）。
	if err := store.Revoke(grant.ID); err != nil {
		t.Fatalf("第二次 Revoke: %v", err)
	}
	snapshot, err = store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snapshot.Applications[grant.ID].Revoked; got != 2 {
		t.Fatalf("第二次撤销后 revoked 应为 2，得到 %d", got)
	}
}

func TestStoreRetryApprovalFlipsDenyToAskWithoutGranting(t *testing.T) {
	store := NewStore(t.TempDir())
	grant := demoGrant("retry")
	if err := store.Decide("s1", grant, DecisionDeny); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if err := store.RetryApproval(grant.ID); err != nil {
		t.Fatalf("RetryApproval: %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	app := snapshot.Applications[grant.ID]
	if app.Sessions["s1"].Decision != DecisionAsk {
		t.Fatalf("重试后应回到 ask（不是 allow），得到 %q", app.Sessions["s1"].Decision)
	}
	if app.Always != nil {
		t.Fatal("重试不许顺手授予持久许可")
	}
	if app.Revoked != 0 {
		t.Fatalf("重试不改变撤销代数，得到 %d", app.Revoked)
	}

	state, err := store.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(state.BlockedApps) != 0 {
		t.Fatalf("回到 ask 后不该再算 blocked，得到 %+v", state.BlockedApps)
	}

	// 再点一次：没有 deny 可清，必须是 no-op（revision 不动）。
	before := app.Sessions["s1"].Revision
	if err := store.RetryApproval(grant.ID); err != nil {
		t.Fatalf("第二次 RetryApproval: %v", err)
	}
	snapshot, err = store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snapshot.Applications[grant.ID].Sessions["s1"].Revision; got != before {
		t.Fatalf("没有拒绝可清时不该推 revision：%d → %d", before, got)
	}

	// 未知 id 也是 no-op（官方 `if (!app …) return`）。
	if err := store.RetryApproval(demoGrant("never-seen").ID); err != nil {
		t.Fatalf("未知 id 的 RetryApproval 应为 no-op，得到 %v", err)
	}
}

func TestStoreRejectsInvalidApplicationIDs(t *testing.T) {
	store := NewStore("")
	for _, id := range []string{"", "xyz", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		if err := store.Revoke(id); err == nil {
			t.Fatalf("Revoke(%q) 应被拒", id)
		} else if code := errorCode(err); code != "bad_request" {
			t.Fatalf("Revoke(%q) 的错误码应为 bad_request，得到 %q", id, code)
		}
		if err := store.RetryApproval(id); err == nil {
			t.Fatalf("RetryApproval(%q) 应被拒", id)
		}
	}
}

func TestStoreMemoryCompositionRefusesPersistentGrant(t *testing.T) {
	store := NewStore("")
	err := store.GrantAlways(demoGrant("nolink"))
	if err == nil {
		t.Fatal("内存态组合不该能保存持久许可")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Status != 409 {
		t.Fatalf("应为 409 结构化错误，得到 %v", err)
	}
}

func TestStoreCorruptFileFailsLoud(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StateFileName)
	if err := os.WriteFile(path, []byte(`{"applications":{"not-a-digest":{"revoked":0,"sessions":{}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir)
	if _, err := store.Status(); err == nil {
		t.Fatal("坏状态文件必须报错，不许当空库（当空库 = 悄悄清掉用户的拒绝记录）")
	}
	// 报错也不许把它改写成"干净的空库"。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "not-a-digest") {
		t.Fatalf("坏文件被静默覆盖了：%s", raw)
	}

	// 无法解析的 JSON 同理。
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Status(); err == nil {
		t.Fatal("无法解析的状态文件必须报错")
	}
}

func TestStoreConcurrentRevokesSerialise(t *testing.T) {
	store := NewStore(t.TempDir())
	grant := demoGrant("concurrent")
	if err := store.Decide("s1", grant, DecisionAllow); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.Revoke(grant.ID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发撤销出错: %v", err)
		}
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snapshot.Applications[grant.ID].Revoked; got != workers {
		t.Fatalf("并发撤销丢了更新：revoked=%d，期望 %d（说明读改写的锁没生效）", got, workers)
	}
}

func TestStoreFileShapeMatchesOfficialDomain(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	grant := demoGrant("shape")
	if err := store.GrantAlways(grant); err != nil {
		t.Fatalf("GrantAlways: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("落盘文件不是 JSON 对象: %v", err)
	}
	// 官方 domain 状态只有 migrated + applications 两个键（permission-store.ts:23-24）。
	if len(decoded) != 2 {
		t.Fatalf("落盘文件应为官方同形的两个键，得到 %v", keysOf(decoded))
	}
	for _, key := range []string{"migrated", "applications"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("落盘文件缺官方键 %q（现有 %v）", key, keysOf(decoded))
		}
	}
	if _, err := os.Stat(filepath.Join(dir, LockFileName)); err != nil {
		t.Fatalf("锁文件应存在（跨进程排它锁）：%v", err)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// errorCode 把 *Error 的码取出来；非结构化错误返回 "error"。
func errorCode(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return "error"
}
