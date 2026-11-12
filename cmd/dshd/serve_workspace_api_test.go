package main

// 官方 workspace / workspaceFiles / directoryPicker 的**宿主侧**测试：证明这些
// 方法读的是真东西（真 registry 文件、真 v4 会话、真文件系统），不是夹具。
//
// 分两层（两半都由本任务交付，Lead 接线只是把两半接起来）：
//
//	internal/transport/handlers_workspace_official_test.go  官方 zod 契约与四字段错误
//	本文件                                                  真实持久化 / 真实边界 / A/B
//
// 为什么不 POST /api/workspace.create：那 16 个 case 在 server.go 里，本轮写作用域
// 不允许改它。这里直接打 `transport.ServeOfficialWorkspace`——它就是 Lead 接线后
// /api 路由会调用的同一个函数，所以参数校验、官方字段、错误信封都走真实代码路径。

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/session"
	"dshgo/internal/transport"
	"dshgo/internal/worktree"
)

// officialWorkspaceHarness 是一套"真 home + 真 host + 真 transport 分派"的夹具。
type officialWorkspaceHarness struct {
	home   string
	host   *officialWorkspaceHost
	api    transport.OfficialWorkspaceAPI
	picker transport.OfficialDirectoryPickerAPI
}

func newOfficialWorkspaceHarness(t *testing.T) *officialWorkspaceHarness {
	t.Helper()
	home := t.TempDir()
	return newOfficialWorkspaceHarnessAt(t, home)
}

// newOfficialWorkspaceHarnessAt 复用同一个 home（模拟"进程重启后再读一遍磁盘"）。
func newOfficialWorkspaceHarnessAt(t *testing.T, home string) *officialWorkspaceHarness {
	t.Helper()
	host := newOfficialWorkspaceHost(home, nil, nil)
	if host == nil {
		t.Fatal("newOfficialWorkspaceHost 返回 nil")
	}
	return &officialWorkspaceHarness{home: home, host: host, api: host.workspaceAPI(), picker: host.directoryPickerAPI()}
}

// call 打一次方法，返回 HTTP 状态与信封。
func (h *officialWorkspaceHarness) call(t *testing.T, method string, body any) (int, map[string]any) {
	t.Helper()
	var raw []byte
	switch v := body.(type) {
	case nil:
		raw = nil
	case string:
		raw = []byte(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("序列化请求体: %v", err)
		}
		raw = encoded
	}
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(string(raw)))
	rec := httptest.NewRecorder()
	transport.ServeOfficialWorkspace(rec, req, method, h.api, h.picker)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 的响应不是 JSON 对象: %q", method, rec.Body.String())
		}
	}
	return rec.Code, out
}

// ok 打一次方法并要求 200，返回 result 对象。
func (h *officialWorkspaceHarness) ok(t *testing.T, method string, body any) map[string]any {
	t.Helper()
	code, out := h.call(t, method, body)
	if code != http.StatusOK {
		t.Fatalf("%s 应 200，实际 %d（%v）", method, code, out)
	}
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s 的 result 不是对象：%v", method, out)
	}
	return res
}

// fail 打一次方法并要求指定状态码，返回错误 message（官方码写在 message 里）。
func (h *officialWorkspaceHarness) fail(t *testing.T, method string, body any, wantStatus int) string {
	t.Helper()
	code, out := h.call(t, method, body)
	if code != wantStatus {
		t.Fatalf("%s 应 %d，实际 %d（%v）", method, wantStatus, code, out)
	}
	obj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("%s 的错误信封缺 error 对象：%v", method, out)
	}
	for _, key := range []string{"code", "message", "hint", "retryable"} {
		if _, has := obj[key]; !has {
			t.Fatalf("%s 的错误信封缺 %s：%v", method, key, obj)
		}
	}
	msg, _ := obj["message"].(string)
	return msg
}

// officialWorkspaceView 取 result.workspace。
func officialWorkspaceView(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	ws, ok := res["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("结果缺 workspace 对象：%v", res)
	}
	return ws
}

// officialWorkspaceStrings 取一个 JSON 字符串数组。
func officialWorkspaceStrings(t *testing.T, res map[string]any, key string) []string {
	t.Helper()
	raw, ok := res[key]
	if !ok {
		t.Fatalf("结果缺 %s：%v", key, res)
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s 不是数组（可能是 null）：%v", key, raw)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// newOfficialWorkspaceDir 建一个真实目录并返回它的规范路径。
func newOfficialWorkspaceDir(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录 %s: %v", dir, err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("取绝对路径 %s: %v", dir, err)
	}
	return filepath.Clean(abs)
}

// newRealSession 造一份真实 v4 会话（cwd 就是它的工作区根）。
func newRealSession(t *testing.T, home, cwd, id string) {
	t.Helper()
	lg, err := session.CreateSession(home, cwd, id, "standard")
	if err != nil {
		t.Fatalf("建真实会话 %s: %v", id, err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关闭会话 %s: %v", id, err)
	}
}

// TestOfficialWorkspaceRegistryPersistsAcrossReload 9 个 workspace 方法的真实语义：
// 真 registry 文件、幂等 create、真排序、真重命名/删除、真 pin/archive 集合。
func TestOfficialWorkspaceRegistryPersistsAcrossReload(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	base := t.TempDir()
	dirA := newOfficialWorkspaceDir(t, base, "alpha")
	dirB := newOfficialWorkspaceDir(t, base, "beta")

	// create A：created=true，title 取目录名，path 是规范绝对路径。
	res := h.ok(t, "workspace.create", map[string]any{"path": dirA})
	if created, _ := res["created"].(bool); !created {
		t.Fatalf("第一次 create 应 created=true：%v", res)
	}
	wsA := officialWorkspaceView(t, res)
	idA, _ := wsA["workspaceId"].(string)
	if idA == "" {
		t.Fatalf("workspaceId 不能为空：%v", wsA)
	}
	if got, _ := wsA["path"].(string); got != dirA {
		t.Fatalf("path 不是规范绝对路径：%q（期望 %q）", got, dirA)
	}
	if got, _ := wsA["title"].(string); got != filepath.Base(dirA) {
		t.Fatalf("title 应取目录名：%q", got)
	}
	for _, key := range []string{"createdAt", "updatedAt"} {
		ts, _ := wsA[key].(string)
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Fatalf("%s 不是真实 ISO-8601 时间：%q（%v）", key, ts, err)
		}
	}

	// registry 必须真的落盘（不是内存里的一张表）。
	registryPath := filepath.Join(h.home, "state", "workspaces.json")
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("registry 文件没落盘（%s）: %v", registryPath, err)
	}
	if !strings.Contains(string(raw), idA) {
		t.Fatalf("registry 文件里没有 %s：%s", idA, raw)
	}

	// 幂等：同一个目录再 create → created=false 且 id 不变。
	res = h.ok(t, "workspace.create", map[string]any{"path": dirA})
	if created, _ := res["created"].(bool); created {
		t.Fatalf("第二次 create 应 created=false：%v", res)
	}
	if got, _ := officialWorkspaceView(t, res)["workspaceId"].(string); got != idA {
		t.Fatalf("幂等 create 换了 id：%q → %q", idA, got)
	}

	// 不存在的目录 → 400 workspace/invalid-path（官方码在 message 里）。
	msg := h.fail(t, "workspace.create", map[string]any{"path": filepath.Join(base, "missing")}, http.StatusBadRequest)
	if !strings.Contains(msg, "workspace/invalid-path") {
		t.Fatalf("不存在的目录应报 workspace/invalid-path：%q", msg)
	}
	if _, err := os.Stat(filepath.Join(base, "missing")); !os.IsNotExist(err) {
		t.Fatalf("create 失败却建出了目录：%v", err)
	}

	// create B，然后 insertBefore(B, A) → 顺序 [B, A]。
	res = h.ok(t, "workspace.create", map[string]any{"path": dirB})
	idB, _ := officialWorkspaceView(t, res)["workspaceId"].(string)
	res = h.ok(t, "workspace.insertBefore", map[string]any{"workspaceId": idB, "beforeWorkspaceId": idA})
	if order := officialWorkspaceStrings(t, res, "workspaceIds"); !equalStrings(order, []string{idB, idA}) {
		t.Fatalf("insertBefore 顺序不对：%v", order)
	}
	// 省略 anchor = 移到末尾。
	res = h.ok(t, "workspace.insertBefore", map[string]any{"workspaceId": idB})
	if order := officialWorkspaceStrings(t, res, "workspaceIds"); !equalStrings(order, []string{idA, idB}) {
		t.Fatalf("insertBefore(省略 anchor) 顺序不对：%v", order)
	}
	// 未知 anchor → 404 workspace/not-found。
	msg = h.fail(t, "workspace.insertBefore", map[string]any{"workspaceId": idA, "beforeWorkspaceId": "ws-nope"}, http.StatusNotFound)
	if !strings.Contains(msg, "workspace/not-found") {
		t.Fatalf("未知 anchor 应报 workspace/not-found：%q", msg)
	}

	// rename：空白 title 400；重名 400 workspace/name-conflict；正常改名落盘。
	_ = h.fail(t, "workspace.rename", map[string]any{"workspaceId": idA, "title": "   "}, http.StatusBadRequest)
	msg = h.fail(t, "workspace.rename", map[string]any{"workspaceId": idA, "title": filepath.Base(dirB)}, http.StatusBadRequest)
	if !strings.Contains(msg, "workspace/name-conflict") {
		t.Fatalf("重名应报 workspace/name-conflict：%q", msg)
	}
	res = h.ok(t, "workspace.rename", map[string]any{"workspaceId": idA, "title": "  改名后  "})
	wsA = officialWorkspaceView(t, res)
	if got, _ := wsA["title"].(string); got != "改名后" {
		t.Fatalf("rename 应 trim 后落盘：%q", got)
	}
	if created, _ := wsA["createdAt"].(string); created == "" {
		t.Fatalf("rename 丢了 createdAt：%v", wsA)
	}
	if updated, _ := wsA["updatedAt"].(string); updated == wsA["createdAt"] {
		t.Fatalf("rename 后 updatedAt 必须变：%v", wsA)
	}
	msg = h.fail(t, "workspace.rename", map[string]any{"workspaceId": "ws-nope", "title": "x"}, http.StatusNotFound)
	if !strings.Contains(msg, "workspace/not-found") {
		t.Fatalf("未知 workspace 应报 workspace/not-found：%q", msg)
	}

	// pin / unpin：最新在前；未知会话 404 session/not-found；归档后不可 pin。
	realSession := "11111111-1111-1111-1111-111111111111"
	newRealSession(t, h.home, dirA, realSession)
	res = h.ok(t, "workspace.pinSession", map[string]any{"sessionId": realSession})
	if pinned := officialWorkspaceStrings(t, res, "pinnedSessionIds"); !equalStrings(pinned, []string{realSession}) {
		t.Fatalf("pinSession 集合不对：%v", pinned)
	}
	msg = h.fail(t, "workspace.pinSession", map[string]any{"sessionId": "00000000-0000-0000-0000-000000000000"}, http.StatusNotFound)
	if !strings.Contains(msg, "session/not-found") {
		t.Fatalf("未知会话应报 session/not-found：%q", msg)
	}

	// archive / unarchive：归档集最新在前；unarchive 幂等。
	res = h.ok(t, "workspace.archiveSession", map[string]any{"sessionId": realSession})
	if set := officialWorkspaceStrings(t, res, "archivedSessionIds"); !equalStrings(set, []string{realSession}) {
		t.Fatalf("archiveSession 集合不对：%v", set)
	}
	msg = h.fail(t, "workspace.pinSession", map[string]any{"sessionId": realSession}, http.StatusBadRequest)
	if !strings.Contains(msg, "gateway/bad-request") {
		t.Fatalf("已归档会话不可 pin，应报 gateway/bad-request：%q", msg)
	}
	res = h.ok(t, "workspace.unarchiveSession", map[string]any{"sessionId": realSession})
	if set := officialWorkspaceStrings(t, res, "archivedSessionIds"); len(set) != 0 {
		t.Fatalf("unarchiveSession 后集合应为空：%v", set)
	}
	res = h.ok(t, "workspace.unarchiveSession", map[string]any{"sessionId": realSession})
	if set := officialWorkspaceStrings(t, res, "archivedSessionIds"); len(set) != 0 {
		t.Fatalf("unarchive 必须幂等：%v", set)
	}
	res = h.ok(t, "workspace.unpinSession", map[string]any{"sessionId": realSession})
	if set := officialWorkspaceStrings(t, res, "pinnedSessionIds"); len(set) != 0 {
		t.Fatalf("unpin 后集合应为空：%v", set)
	}
	_ = h.ok(t, "workspace.unpinSession", map[string]any{"sessionId": realSession})

	// 进程重启（同一个 home 新 host）：顺序、标题、pin/archive 全部还在。
	h2 := newOfficialWorkspaceHarnessAt(t, h.home)
	res = h2.ok(t, "workspace.create", map[string]any{"path": dirA})
	if created, _ := res["created"].(bool); created {
		t.Fatalf("重启后 create 同一目录不应再 create：%v", res)
	}
	if got, _ := officialWorkspaceView(t, res)["workspaceId"].(string); got != idA {
		t.Fatalf("重启后 workspaceId 变了：%q → %q", idA, got)
	}
	if got, _ := officialWorkspaceView(t, res)["title"].(string); got != "改名后" {
		t.Fatalf("重启后 title 丢了：%q", got)
	}
	res = h2.ok(t, "workspace.insertBefore", map[string]any{"workspaceId": idB})
	if order := officialWorkspaceStrings(t, res, "workspaceIds"); !equalStrings(order, []string{idA, idB}) {
		t.Fatalf("重启后顺序丢了：%v", order)
	}

	// delete：只摘注册（目录与会话保留）。
	res = h2.ok(t, "workspace.delete", map[string]any{"workspaceId": idB})
	if deleted, _ := res["deleted"].(bool); !deleted {
		t.Fatalf("delete 结果应是 {deleted:true}：%v", res)
	}
	if _, err := os.Stat(dirB); err != nil {
		t.Fatalf("delete 不该删目录：%v", err)
	}
	if s, err := os.Stat(filepath.Join(h.home, "sessions")); err != nil || !s.IsDir() {
		t.Fatalf("delete 不该删会话：%v", err)
	}
	msg = h2.fail(t, "workspace.delete", map[string]any{"workspaceId": idB}, http.StatusNotFound)
	if !strings.Contains(msg, "workspace/not-found") {
		t.Fatalf("重复 delete 应报 workspace/not-found：%q", msg)
	}

	// 再重启一次：delete 也落盘了。
	h3 := newOfficialWorkspaceHarnessAt(t, h.home)
	res = h3.ok(t, "workspace.create", map[string]any{"path": dirB})
	if created, _ := res["created"].(bool); !created {
		t.Fatalf("delete 之后同目录应可重新 create：%v", res)
	}
}

// TestOfficialWorkspaceSessionsComeFromRealStore 会话归属必须来自真实 v4 会话存储，
// 手动排序落盘，归档的会话不参与分组，A/B 工作区不串。
func TestOfficialWorkspaceSessionsComeFromRealStore(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	base := t.TempDir()
	dirA := newOfficialWorkspaceDir(t, base, "wsA")
	dirB := newOfficialWorkspaceDir(t, base, "wsB")

	// 真会话：A 下 3 条，B 下 1 条。
	idsA := []string{
		"aaaaaaaa-0000-0000-0000-000000000001",
		"aaaaaaaa-0000-0000-0000-000000000002",
		"aaaaaaaa-0000-0000-0000-000000000003",
	}
	for _, id := range idsA {
		newRealSession(t, h.home, dirA, id)
		time.Sleep(10 * time.Millisecond) // UpdatedAt 的先后决定默认顺序
	}
	idB := "bbbbbbbb-0000-0000-0000-000000000001"
	newRealSession(t, h.home, dirB, idB)

	res := h.ok(t, "workspace.create", map[string]any{"path": dirA})
	wsA := officialWorkspaceView(t, res)
	idWA, _ := wsA["workspaceId"].(string)
	sessions := officialWorkspaceStrings(t, wsA, "sessionIds")
	if len(sessions) != 3 {
		t.Fatalf("A 工作区应有 3 条真实会话，实际 %v", sessions)
	}
	// 默认顺序：最近更新的在前。
	if !equalStrings(sessions, []string{idsA[2], idsA[1], idsA[0]}) {
		t.Fatalf("默认会话顺序应按最近更新倒序：%v", sessions)
	}

	res = h.ok(t, "workspace.create", map[string]any{"path": dirB})
	if got := officialWorkspaceStrings(t, officialWorkspaceView(t, res), "sessionIds"); !equalStrings(got, []string{idB}) {
		t.Fatalf("B 工作区会话串了：%v", got)
	}

	// 手动排序：把最早那条移到最前。
	res = h.ok(t, "workspace.insertSessionBefore", map[string]any{
		"workspaceId": idWA, "sessionId": idsA[0], "beforeSessionId": idsA[2],
	})
	if got := officialWorkspaceStrings(t, officialWorkspaceView(t, res), "sessionIds"); !equalStrings(got, []string{idsA[0], idsA[2], idsA[1]}) {
		t.Fatalf("insertSessionBefore 顺序不对：%v", got)
	}
	// 省略 anchor = 移到末尾。
	res = h.ok(t, "workspace.insertSessionBefore", map[string]any{"workspaceId": idWA, "sessionId": idsA[0]})
	if got := officialWorkspaceStrings(t, officialWorkspaceView(t, res), "sessionIds"); !equalStrings(got, []string{idsA[2], idsA[1], idsA[0]}) {
		t.Fatalf("insertSessionBefore(省略 anchor) 顺序不对：%v", got)
	}
	// 不属于本工作区的会话 → 400 workspace/move-invalid（A/B 不串）。
	msg := h.fail(t, "workspace.insertSessionBefore", map[string]any{
		"workspaceId": idWA, "sessionId": idB,
	}, http.StatusBadRequest)
	if !strings.Contains(msg, "workspace/move-invalid") {
		t.Fatalf("跨工作区会话应报 workspace/move-invalid：%q", msg)
	}

	// 重启后手动顺序还在。
	h2 := newOfficialWorkspaceHarnessAt(t, h.home)
	res = h2.ok(t, "workspace.create", map[string]any{"path": dirA})
	if got := officialWorkspaceStrings(t, officialWorkspaceView(t, res), "sessionIds"); !equalStrings(got, []string{idsA[2], idsA[1], idsA[0]}) {
		t.Fatalf("重启后手动顺序丢了：%v", got)
	}

	// 归档：会话从分组里消失，归档集里有它。
	res = h2.ok(t, "workspace.archiveSession", map[string]any{"sessionId": idsA[1]})
	if got := officialWorkspaceStrings(t, res, "archivedSessionIds"); !equalStrings(got, []string{idsA[1]}) {
		t.Fatalf("归档集不对：%v", got)
	}
	res = h2.ok(t, "workspace.create", map[string]any{"path": dirA})
	if got := officialWorkspaceStrings(t, officialWorkspaceView(t, res), "sessionIds"); !equalStrings(got, []string{idsA[2], idsA[0]}) {
		t.Fatalf("归档的会话不该出现在分组里：%v", got)
	}
	// 未知会话归档 → 404 session/not-found。
	msg = h2.fail(t, "workspace.archiveSession", map[string]any{"sessionId": "ffffffff-0000-0000-0000-000000000000"}, http.StatusNotFound)
	if !strings.Contains(msg, "session/not-found") {
		t.Fatalf("未知会话归档应报 session/not-found：%q", msg)
	}
}

// TestOfficialWorkspaceArchiveActiveRequiresStopActivity 活动会话必须先停：
// 真轮次在跑时（active 回调为真），无 stopActivity 拒绝且集合不变；给了就真请求停止。
func TestOfficialWorkspaceArchiveActiveRequiresStopActivity(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	dir := newOfficialWorkspaceDir(t, t.TempDir(), "active")
	id := "cccccccc-0000-0000-0000-000000000001"
	newRealSession(t, h.home, dir, id)

	// 把"这一条正在跑"注入宿主（生产上来自 promptService）。
	var stops []string
	h.host.active = func(sessionID string) bool { return sessionID == id }
	h.host.stop = func(sessionID string) error { stops = append(stops, sessionID); return nil }
	h.api = h.host.workspaceAPI()

	msg := h.fail(t, "workspace.archiveSession", map[string]any{"sessionId": id}, http.StatusConflict)
	if !strings.Contains(msg, "workspace/session-active") {
		t.Fatalf("活动会话应报 workspace/session-active：%q", msg)
	}
	if len(stops) != 0 {
		t.Fatalf("被拒绝时不得请求停止：%v", stops)
	}
	// 归档集必须没变。
	h.host.mu.Lock()
	archived := append([]string(nil), h.host.doc.ArchivedSessionIDs...)
	h.host.mu.Unlock()
	if len(archived) != 0 {
		t.Fatalf("被拒绝时归档集不得变化：%v", archived)
	}

	res := h.ok(t, "workspace.archiveSession", map[string]any{"sessionId": id, "stopActivity": true})
	if got := officialWorkspaceStrings(t, res, "archivedSessionIds"); !equalStrings(got, []string{id}) {
		t.Fatalf("带 stopActivity 应完成归档：%v", got)
	}
	if len(stops) != 1 || stops[0] != id {
		t.Fatalf("stopActivity=true 必须先请求停止：%v", stops)
	}
}

// TestOfficialWorkspaceFilesRealFSBoundary workspaceFiles 走真实文件系统且受工作区根约束。
func TestOfficialWorkspaceFilesRealFSBoundary(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	other := newOfficialWorkspaceDir(t, t.TempDir(), "other")
	id := "dddddddd-0000-0000-0000-000000000001"
	newRealSession(t, h.home, root, id)

	textPath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(textPath, []byte("l1\nl2\nl3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(binPath, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(other, "secret.txt")
	if err := os.WriteFile(outsidePath, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	// list：根目录 = 空 path，child type 只取 file/directory/other。
	res := h.ok(t, "workspaceFiles.list", map[string]any{"workspaceFileScopeId": id, "path": ""})
	if res["path"] != "" {
		t.Fatalf("根目录的 path 应是空串：%v", res)
	}
	if truncated, _ := res["truncated"].(bool); truncated {
		t.Fatalf("小目录不该 truncated：%v", res)
	}
	entries, _ := res["entries"].([]any)
	names := make([]string, 0, len(entries))
	types := map[string]string{}
	sizes := map[string]any{}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		names = append(names, name)
		types[name], _ = entry["type"].(string)
		if size, has := entry["size"]; has {
			sizes[name] = size
		}
	}
	if !equalStrings(names, []string{"blob.bin", "notes.txt", "sub"}) {
		t.Fatalf("list 应按名字排序且只列直接子项：%v", names)
	}
	if types["blob.bin"] != "file" || types["sub"] != "directory" {
		t.Fatalf("entry type 不对：%v", types)
	}
	if sizes["blob.bin"] != float64(6) {
		t.Fatalf("文件项应有真实 size=6：%v", sizes)
	}
	if _, has := sizes["sub"]; has {
		t.Fatalf("目录项不该有 size：%v", sizes)
	}

	// 子目录的 path 是工作区相对路径（用 '/' 连接）。
	res = h.ok(t, "workspaceFiles.list", map[string]any{"workspaceFileScopeId": id, "path": "sub"})
	if res["path"] != "sub" {
		t.Fatalf("子目录 path 应是工作区相对路径：%v", res)
	}

	// read：分页语义（offset 从 1 开始，末行 eof）。
	res = h.ok(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "notes.txt", "range": map[string]any{"offset": 1, "limit": 2},
	})
	if res["offset"] != float64(1) || res["text"] != "l1\nl2" || res["lines"] != float64(2) || res["eof"] != false {
		t.Fatalf("read 首页不对：%v", res)
	}
	// 官方 absolutePath 是"符号链接解析后"的绝对路径（Windows 上 8.3 短名也会被展开），
	// 所以期望值独立地用 EvalSymlinks 算一遍。
	if got, _ := res["absolutePath"].(string); filepath.Clean(got) != resolvedOrSelf(textPath) {
		t.Fatalf("absolutePath 应是符号链接解析后的真实绝对路径：%q（期望 %q）", got, resolvedOrSelf(textPath))
	}
	if version, _ := res["version"].(string); version == "" {
		t.Fatalf("read 必须给 version：%v", res)
	}
	if res["bytes"] != float64(len("l1\nl2\nl3\n")) {
		t.Fatalf("read 的 bytes 应是完整文件大小：%v", res)
	}
	res = h.ok(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "notes.txt", "range": map[string]any{"offset": 3, "limit": 5},
	})
	if res["text"] != "l3" || res["lines"] != float64(1) || res["eof"] != true {
		t.Fatalf("read 末页不对：%v", res)
	}
	// 越过末行的页：lines=0、text 空。
	res = h.ok(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "notes.txt", "range": map[string]any{"offset": 99},
	})
	if res["text"] != "" || res["lines"] != float64(0) {
		t.Fatalf("越过末行的页应是空页：%v", res)
	}
	// limit 超过上限 → 400（官方：may not exceed maxLines）。
	h.fail(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "notes.txt", "range": map[string]any{"limit": 100000},
	}, http.StatusBadRequest)
	// 目录不是文件 → 400。
	h.fail(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "sub", "range": map[string]any{},
	}, http.StatusBadRequest)

	// stat：version 随内容变化。
	res = h.ok(t, "workspaceFiles.stat", map[string]any{"workspaceFileScopeId": id, "path": "notes.txt"})
	versionBefore, _ := res["version"].(string)
	if res["bytes"] != float64(len("l1\nl2\nl3\n")) {
		t.Fatalf("stat 的 bytes 不对：%v", res)
	}
	if err := os.WriteFile(textPath, []byte("l1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = h.ok(t, "workspaceFiles.stat", map[string]any{"workspaceFileScopeId": id, "path": "notes.txt"})
	if versionAfter, _ := res["version"].(string); versionAfter == versionBefore {
		t.Fatalf("内容变了 version 却没变：%q", versionAfter)
	}
	// 目录不能 stat 成文件。
	h.fail(t, "workspaceFiles.stat", map[string]any{"workspaceFileScopeId": id, "path": "sub"}, http.StatusBadRequest)

	// readBytes：真实字节的 base64 + 窗口 + eof。
	res = h.ok(t, "workspaceFiles.readBytes", map[string]any{
		"workspaceFileScopeId": id, "path": "blob.bin",
		"options": map[string]any{"range": map[string]any{"offset": 2, "length": 3}},
	})
	data, _ := res["data"].(string)
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatalf("readBytes 的 data 不是合法 base64：%q（%v）", data, err)
	}
	if string(decoded) != "cde" {
		t.Fatalf("readBytes 窗口内容不对：%q", decoded)
	}
	if res["offset"] != float64(2) || res["eof"] != false || res["encoding"] != "base64" {
		t.Fatalf("readBytes 字段不对：%v", res)
	}
	res = h.ok(t, "workspaceFiles.readBytes", map[string]any{
		"workspaceFileScopeId": id, "path": "blob.bin",
		"options": map[string]any{"range": map[string]any{"offset": 3, "length": 10}},
	})
	data, _ = res["data"].(string)
	if decoded, err = base64.StdEncoding.DecodeString(data); err != nil || string(decoded) != "def" {
		t.Fatalf("readBytes 越界窗口应截到 EOF：%q（%v）", data, err)
	}
	if res["eof"] != true {
		t.Fatalf("读到末尾时 eof 应为 true：%v", res)
	}
	// 越过 EOF 的窗口：空 data、eof=true。
	res = h.ok(t, "workspaceFiles.readBytes", map[string]any{
		"workspaceFileScopeId": id, "path": "blob.bin",
		"options": map[string]any{"range": map[string]any{"offset": 99}},
	})
	if res["data"] != "" || res["eof"] != true {
		t.Fatalf("越过 EOF 应给空 data + eof=true：%v", res)
	}
	// baseFile：相对路径从 baseFile 所在目录解析。
	res = h.ok(t, "workspaceFiles.readBytes", map[string]any{
		"workspaceFileScopeId": id, "path": "blob.bin",
		"options": map[string]any{"baseFile": "notes.txt"},
	})
	if res["eof"] != true {
		t.Fatalf("baseFile 解析后应读到整个 blob.bin：%v", res)
	}

	// 边界：`..` 逃逸、绝对路径越界、另一个工作区的文件，全部 400 且不泄漏内容。
	for _, path := range []string{
		"../secret.txt",
		"../other/secret.txt",
		filepath.ToSlash(outsidePath),
		outsidePath,
	} {
		msg := h.fail(t, "workspaceFiles.read", map[string]any{
			"workspaceFileScopeId": id, "path": path, "range": map[string]any{},
		}, http.StatusBadRequest)
		if !strings.Contains(msg, "workspaceFiles/outside-workspace") {
			t.Fatalf("越界读取 %q 应报 workspaceFiles/outside-workspace：%q", path, msg)
		}
	}
	// stat / list / readBytes 同样受边界约束。
	h.fail(t, "workspaceFiles.stat", map[string]any{"workspaceFileScopeId": id, "path": "../secret.txt"}, http.StatusBadRequest)
	h.fail(t, "workspaceFiles.list", map[string]any{"workspaceFileScopeId": id, "path": ".."}, http.StatusBadRequest)
	h.fail(t, "workspaceFiles.readBytes", map[string]any{
		"workspaceFileScopeId": id, "path": "../secret.txt", "options": map[string]any{},
	}, http.StatusBadRequest)

	// 不存在的文件 → 404 workspace-file/not-found。
	msg := h.fail(t, "workspaceFiles.stat", map[string]any{"workspaceFileScopeId": id, "path": "missing.txt"}, http.StatusNotFound)
	if !strings.Contains(msg, "workspace-file/not-found") {
		t.Fatalf("不存在的文件应报 workspace-file/not-found：%q", msg)
	}
	// 未知会话（scope 不成立）→ 404 session/not-found，不拿进程默认目录顶上。
	msg = h.fail(t, "workspaceFiles.list", map[string]any{
		"workspaceFileScopeId": "eeeeeeee-0000-0000-0000-000000000000", "path": "",
	}, http.StatusNotFound)
	if !strings.Contains(msg, "session/not-found") {
		t.Fatalf("未知会话应报 session/not-found：%q", msg)
	}

	// 符号链接逃逸：能建就必测（Windows 无权限时跳过）。
	linkPath := filepath.Join(root, "escape-link")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Logf("跳过符号链接逃逸断言（本机不允许建符号链接）：%v", err)
	} else {
		msg = h.fail(t, "workspaceFiles.read", map[string]any{
			"workspaceFileScopeId": id, "path": "escape-link", "range": map[string]any{},
		}, http.StatusBadRequest)
		if !strings.Contains(msg, "workspaceFiles/") {
			t.Fatalf("符号链接逃逸应被拒：%q", msg)
		}
	}
}

// TestOfficialDirectoryPickerRealListAndCreate directoryPicker 走真实文件系统：
// 真 home、真 crumbs、按名字排序、hidden 标记、entry 上限与 truncation、真建目录。
func TestOfficialDirectoryPickerRealListAndCreate(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"a-dir", "b-dir", ".hidden-dir"} {
		if err := os.MkdirAll(filepath.Join(home, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	absHome = filepath.Clean(absHome)

	host := newOfficialWorkspaceHost(t.TempDir(), nil, nil)
	host.pickerHome = absHome // 生产上默认是 os.UserHomeDir()
	picker := host.directoryPickerAPI()

	call := func(t *testing.T, method string, body any) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body.(string)))
		rec := httptest.NewRecorder()
		transport.ServeOfficialWorkspace(rec, req, method, host.workspaceAPI(), picker)
		var out map[string]any
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("%s 的响应不是 JSON：%q", method, rec.Body.String())
			}
		}
		return rec.Code, out
	}

	code, out := call(t, "directoryPicker.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("list 应 200，实际 %d（%v）", code, out)
	}
	res, _ := out["result"].(map[string]any)
	if got, _ := res["path"].(string); filepath.Clean(got) != absHome {
		t.Fatalf("省略 path 应列 home：%q（期望 %q）", got, absHome)
	}
	if got, _ := res["home"].(string); filepath.Clean(got) != absHome {
		t.Fatalf("home 字段不对：%q", got)
	}
	crumbs, _ := res["crumbs"].([]any)
	if len(crumbs) == 0 {
		t.Fatalf("crumbs 不能为空：%v", res)
	}
	last, _ := crumbs[len(crumbs)-1].(map[string]any)
	if got, _ := last["path"].(string); filepath.Clean(got) != absHome {
		t.Fatalf("最后一个 crumb 必须是所列目录：%v", last)
	}
	first, _ := crumbs[0].(map[string]any)
	rootPath, _ := first["path"].(string)
	if got, _ := first["name"].(string); got != rootPath {
		t.Fatalf("根 crumb 的 name 应是它的完整路径：%v", first)
	}
	if hidden, _ := first["hidden"].(bool); hidden {
		t.Fatalf("crumb 的 hidden 永远是 false：%v", first)
	}

	entries, _ := res["entries"].([]any)
	var names []string
	hiddenOf := map[string]bool{}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		names = append(names, name)
		hiddenOf[name], _ = entry["hidden"].(bool)
	}
	if !equalStrings(names, []string{".hidden-dir", "a-dir", "b-dir"}) {
		t.Fatalf("entries 应只含目录且按名字排序：%v", names)
	}
	if !hiddenOf[".hidden-dir"] || hiddenOf["a-dir"] {
		t.Fatalf("hidden 标记不对：%v", hiddenOf)
	}
	if truncated, _ := res["truncated"].(bool); truncated {
		t.Fatalf("3 个目录不该 truncated：%v", res)
	}

	// entry 上限生效时 truncated=true，且缺的是名字排序的尾部。
	host.dirCap = 2
	code, out = call(t, "directoryPicker.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("list 应 200，实际 %d（%v）", code, out)
	}
	res, _ = out["result"].(map[string]any)
	entries, _ = res["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("上限 2 时应只给 2 项：%v", res)
	}
	if truncated, _ := res["truncated"].(bool); !truncated {
		t.Fatalf("被截断时 truncated 必须为 true：%v", res)
	}
	host.dirCap = 0

	// createDirectory：真建目录，返回真实绝对路径。
	code, out = call(t, "directoryPicker.createDirectory", `{"path":`+jsonQuote(absHome)+`,"name":"new-dir"}`)
	if code != http.StatusOK {
		t.Fatalf("createDirectory 应 200，实际 %d（%v）", code, out)
	}
	created, _ := out["result"].(string)
	if filepath.Clean(created) != filepath.Join(absHome, "new-dir") {
		t.Fatalf("createDirectory 返回路径不对：%q", created)
	}
	if st, err := os.Stat(created); err != nil || !st.IsDir() {
		t.Fatalf("createDirectory 没真的建出目录：%v", err)
	}
	// 已存在 → 400 directory-picker/exists。
	code, out = call(t, "directoryPicker.createDirectory", `{"path":`+jsonQuote(absHome)+`,"name":"new-dir"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("已存在应 400，实际 %d（%v）", code, out)
	}
	obj, _ := out["error"].(map[string]any)
	if msg, _ := obj["message"].(string); !strings.Contains(msg, "directory-picker/exists") {
		t.Fatalf("已存在应报 directory-picker/exists：%q", msg)
	}
	// 父目录不存在 → 400 directory-picker/create-failed 或 unreadable（不静默成功）。
	code, out = call(t, "directoryPicker.createDirectory", `{"path":`+jsonQuote(filepath.Join(absHome, "missing"))+`,"name":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("父目录不存在应 400，实际 %d（%v）", code, out)
	}
	// 相对 path → 400（官方要求绝对存在的父目录）。
	code, out = call(t, "directoryPicker.createDirectory", `{"path":"relative","name":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("相对 path 应 400，实际 %d（%v）", code, out)
	}
	// 列一个不存在的目录 → 400 directory-picker/unreadable。
	code, out = call(t, "directoryPicker.list", `{"path":`+jsonQuote(filepath.Join(absHome, "nope"))+`}`)
	if code != http.StatusBadRequest {
		t.Fatalf("列不存在的目录应 400，实际 %d（%v）", code, out)
	}
	obj, _ = out["error"].(map[string]any)
	if msg, _ := obj["message"].(string); !strings.Contains(msg, "directory-picker/unreadable") {
		t.Fatalf("不可读目录应报 directory-picker/unreadable：%q", msg)
	}
}

// resolvedOrSelf 是测试里独立算"符号链接解析后的绝对路径"（不复用被测实现）。
func resolvedOrSelf(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// jsonQuote 把一个路径编成 JSON 字符串字面量（测试里手拼 body 用）。
func jsonQuote(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// ---------------------------------------------------------------------------
// workspaceFiles.changes：目录目标
// ---------------------------------------------------------------------------

// nextChangesFrame 从一条 changes 流上等下一帧（超时即判失败）。
func nextChangesFrame(t *testing.T, stream transport.OfficialFrameStream) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, done, fault := stream.Next(ctx)
	if fault != nil {
		t.Fatalf("changes 流故障：%v（%s）", fault.Code, fault.Message)
	}
	if done {
		t.Fatalf("changes 流提前结束（等帧超时）")
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("changes 帧不是 JSON 对象：%q（%v）", string(raw), err)
	}
	return frame
}

// changesChangeOf 取一帧 change 的 change 对象，并钉住 kind。
func changesChangeOf(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	if frame["kind"] != "change" {
		t.Fatalf("帧不是 change：%v", frame)
	}
	change, ok := frame["change"].(map[string]any)
	if !ok {
		t.Fatalf("change 帧缺 change 对象：%v", frame)
	}
	return change
}

// TestOfficialWorkspaceChangesDirectoryTargetRealFS
//
// 目录目标必须真的产出 change 帧（官方帧：{absolutePath,version} /
// {absolutePath,absent:true}），而不是 501 workspace-file/watch-unsupported：
//
//   - absolutePath 是**符号链接解析后的绝对路径**，不是工作区相对路径；
//   - 目录直接子项增/删 → 目录版本变化 → 一帧带新版本的 change；
//   - 目录消失 → absent；目录回来 → 带新版本的 change；
//   - 文件目标仍走同一条流（尺寸/内容变化 → change），目录支持不改变文件语义。
func TestOfficialWorkspaceChangesDirectoryTargetRealFS(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	id := "dddddddd-0000-0000-0000-0000000000d1"
	newRealSession(t, h.home, root, id)

	watched := filepath.Join(root, "watched")
	if err := os.MkdirAll(watched, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(watched, "nested"), 0o755); err != nil {
		t.Fatalf("建子目录: %v", err)
	}

	journals := transport.NewJournals(nil)
	srv, err := transport.NewServer(transport.Options{Source: newSessionSource(h.home, ""), Journals: journals})
	if err != nil {
		t.Fatalf("transport.NewServer: %v", err)
	}
	defer srv.Close()
	srv.AttachOfficialWorkspaceAPI(h.api)
	defer srv.DetachOfficialWorkspaceAPI()

	open := func(path string) (transport.OfficialFrameStream, *transport.OfficialStreamFault) {
		return srv.OpenOfficialFrameStream(context.Background(), "workspaceFiles.changes",
			[]byte(`{"workspaceFileScopeId":`+jsonQuote(id)+`,"path":`+jsonQuote(path)+`}`))
	}

	stream, fault := open("watched")
	if fault != nil {
		t.Fatalf("目录目标必须能打开 changes 流，当前 fault=%d %s：%s",
			fault.Status, fault.Code, fault.Message)
	}
	defer stream.Close()

	head := nextChangesFrame(t, stream)
	if head["kind"] != "ready" {
		t.Fatalf("首帧必须是 ready（watch 已生效）：%v", head)
	}

	wantAbs := resolvedOrSelf(watched)
	if !filepath.IsAbs(wantAbs) {
		t.Fatalf("测试夹具的目录必须能算成绝对路径：%q", wantAbs)
	}
	// 目录的"绝对路径身份"：不是请求里那个相对路径。
	if wantAbs == "watched" || wantAbs == filepath.ToSlash("watched") {
		t.Fatalf("目录目标的绝对路径不能等于工作区相对路径：%q", wantAbs)
	}

	// 第 1 个直接子项 → 一帧 change（绝对路径 + 版本令牌）。
	if err := os.WriteFile(filepath.Join(watched, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatalf("写子文件: %v", err)
	}
	first := changesChangeOf(t, nextChangesFrame(t, stream))
	if abs, _ := first["absolutePath"].(string); abs != wantAbs {
		t.Fatalf("目录帧的 absolutePath 必须是解析后的绝对路径 %q，实际 %q", wantAbs, abs)
	}
	if abs, _ := first["absolutePath"].(string); !filepath.IsAbs(abs) {
		t.Fatalf("目录帧的 absolutePath 不是绝对路径：%q（不拿相对路径冒充）", abs)
	}
	version1, _ := first["version"].(string)
	if version1 == "" {
		t.Fatalf("目录帧必须带非空 version：%v", first)
	}
	if _, has := first["absent"]; has {
		t.Fatalf("目录存在时不许带 absent：%v", first)
	}

	// 第 2 个直接子项 → 目录版本必须再变。
	if err := os.WriteFile(filepath.Join(watched, "b.txt"), []byte("2"), 0o644); err != nil {
		t.Fatalf("写第二个子文件: %v", err)
	}
	second := changesChangeOf(t, nextChangesFrame(t, stream))
	version2, _ := second["version"].(string)
	if version2 == "" || version2 == version1 {
		t.Fatalf("目录直接子项增加后 version 必须变化：%q → %q", version1, version2)
	}

	// 删掉一个直接子项 → 目录版本再变。
	if err := os.Remove(filepath.Join(watched, "a.txt")); err != nil {
		t.Fatalf("删子文件: %v", err)
	}
	third := changesChangeOf(t, nextChangesFrame(t, stream))
	version3, _ := third["version"].(string)
	if version3 == "" || version3 == version2 {
		t.Fatalf("目录直接子项删除后 version 必须变化：%q → %q", version2, version3)
	}

	// 目录消失 → absent 帧，路径身份不变。
	if err := os.RemoveAll(watched); err != nil {
		t.Fatalf("删目录: %v", err)
	}
	gone := changesChangeOf(t, nextChangesFrame(t, stream))
	if gone["absent"] != true {
		t.Fatalf("目录消失必须发 absent 帧：%v", gone)
	}
	if abs, _ := gone["absolutePath"].(string); abs != wantAbs {
		t.Fatalf("absent 帧仍要带原来的绝对路径 %q，实际 %q", wantAbs, abs)
	}
	if _, has := gone["version"]; has {
		t.Fatalf("absent 帧不许带 version：%v", gone)
	}

	// 目录回来 → 带新版本的 change（不是再发一次 absent）。
	if err := os.MkdirAll(watched, 0o755); err != nil {
		t.Fatalf("重建目录: %v", err)
	}
	back := changesChangeOf(t, nextChangesFrame(t, stream))
	version4, _ := back["version"].(string)
	if version4 == "" {
		t.Fatalf("目录回来后必须发带 version 的 change：%v", back)
	}
	if _, has := back["absent"]; has {
		t.Fatalf("目录回来后的 change 不许带 absent：%v", back)
	}

	// 文件目标回归：目录支持不改变"文件 watch"的语义。
	fileTarget := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(fileTarget, []byte("x"), 0o644); err != nil {
		t.Fatalf("写文件目标: %v", err)
	}
	fileStream, fault := open("plain.txt")
	if fault != nil {
		t.Fatalf("文件目标必须仍能打开 changes 流：%v", fault)
	}
	defer fileStream.Close()
	if head := nextChangesFrame(t, fileStream); head["kind"] != "ready" {
		t.Fatalf("文件目标首帧必须是 ready：%v", head)
	}
	if err := os.WriteFile(fileTarget, []byte("xy"), 0o644); err != nil {
		t.Fatalf("改文件目标: %v", err)
	}
	fileChange := changesChangeOf(t, nextChangesFrame(t, fileStream))
	if abs, _ := fileChange["absolutePath"].(string); abs != resolvedOrSelf(fileTarget) {
		t.Fatalf("文件帧的 absolutePath 不对：%v", fileChange)
	}

	// 空 path = 工作区根，也是目录目标：changes 能 watch 根，而 unary stat
	// 仍按官方契约拒绝（根是目录，不是普通文件）。
	h.fail(t, "workspaceFiles.stat", map[string]any{"workspaceFileScopeId": id, "path": ""}, http.StatusBadRequest)
	rootStream, fault := open("")
	if fault != nil {
		t.Fatalf("工作区根（空 path）必须能打开 changes 流：%v", fault)
	}
	defer rootStream.Close()
	if head := nextChangesFrame(t, rootStream); head["kind"] != "ready" {
		t.Fatalf("根目录首帧必须是 ready：%v", head)
	}
	if err := os.WriteFile(filepath.Join(root, "root-new.txt"), []byte("r"), 0o644); err != nil {
		t.Fatalf("在根下建文件: %v", err)
	}
	rootChange := changesChangeOf(t, nextChangesFrame(t, rootStream))
	if abs, _ := rootChange["absolutePath"].(string); abs != resolvedOrSelf(root) {
		t.Fatalf("根目录帧的 absolutePath 必须是工作区根的绝对路径：%v", rootChange)
	}
	if version, _ := rootChange["version"].(string); version == "" {
		t.Fatalf("根目录帧必须带 version：%v", rootChange)
	}
}

// TestOfficialWorkspaceChangesDirectoryBoundary
//
// 目录目标不能成为越界/符号链接的口子：
//   - `..`、工作区外的绝对目录 → 400 workspaceFiles/outside-workspace；
//   - 指向工作区外的目录符号链接 → 400 outside-workspace；
//   - 指向工作区内的目录符号链接 → 400 symlink-refused（watch 不借链接改路径身份）；
//   - 拒绝都发生在开流之前（没有 200 + 假 ready）。
func TestOfficialWorkspaceChangesDirectoryBoundary(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	other := newOfficialWorkspaceDir(t, t.TempDir(), "other")
	id := "dddddddd-0000-0000-0000-0000000000d2"
	newRealSession(t, h.home, root, id)
	if err := os.MkdirAll(filepath.Join(other, "sub"), 0o755); err != nil {
		t.Fatalf("建工作区外目录: %v", err)
	}
	inside := filepath.Join(root, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("建工作区内目录: %v", err)
	}

	journals := transport.NewJournals(nil)
	srv, err := transport.NewServer(transport.Options{Source: newSessionSource(h.home, ""), Journals: journals})
	if err != nil {
		t.Fatalf("transport.NewServer: %v", err)
	}
	defer srv.Close()
	srv.AttachOfficialWorkspaceAPI(h.api)
	defer srv.DetachOfficialWorkspaceAPI()

	open := func(path string) (transport.OfficialFrameStream, *transport.OfficialStreamFault) {
		return srv.OpenOfficialFrameStream(context.Background(), "workspaceFiles.changes",
			[]byte(`{"workspaceFileScopeId":`+jsonQuote(id)+`,"path":`+jsonQuote(path)+`}`))
	}
	mustRefused := func(path, wantCode string) {
		t.Helper()
		stream, fault := open(path)
		if fault == nil {
			stream.Close()
			t.Fatalf("越界/链接目录 %q 必须被拒，实际打开了流", path)
		}
		if fault.Status != http.StatusBadRequest {
			t.Fatalf("%q 应 400，实际 %d（%s）", path, fault.Status, fault.Code)
		}
		if !strings.Contains(fault.Message, wantCode) {
			t.Fatalf("%q 应报 %s，实际：%s", path, wantCode, fault.Message)
		}
	}

	mustRefused("..", "workspaceFiles/outside-workspace")
	mustRefused(filepath.ToSlash(other), "workspaceFiles/outside-workspace")
	mustRefused(filepath.ToSlash(filepath.Join(other, "sub")), "workspaceFiles/outside-workspace")

	// 符号链接：能建就必测（Windows 无权限时跳过，与既有边界测试同口径）。
	if err := os.Symlink(other, filepath.Join(root, "link-out")); err != nil {
		t.Logf("跳过目录符号链接断言（本机不允许建符号链接）：%v", err)
	} else {
		mustRefused("link-out", "workspaceFiles/outside-workspace")
		if err := os.Symlink(inside, filepath.Join(root, "link-in")); err != nil {
			t.Logf("跳过工作区内目录符号链接断言：%v", err)
		} else {
			mustRefused("link-in", "workspace-file/not-regular-file")
		}
	}
}

// ---------------------------------------------------------------------------
// 官方 rc.2 归档事件：`workspace/session-archived` / `workspace/session-unarchiving`
// （已知缺口 #3）
// ---------------------------------------------------------------------------
//
// 官方依据（`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-workspace/src/index.ts`）：
//
//	:98-100  SessionActivityRequest = { sessionId }（两个事件共用这一个载荷）
//	:156     `workspace/session-archived` 声明
//	:403     归档**落盘之后**广播（订阅者失败只 warn，不回滚归档）
//	:163     `workspace/session-unarchiving` 声明
//	:426     把 id 移出归档集**之前**广播；监听者拒绝 → 保留归档态
//
// 客户端可见性走本仓既有两条通道（不新开协议面）：
//   - 每会话 transport journal：`session.history` 与官方 `session/events` 尾部流读同一份；
//   - `/ws` 的 `session:<id>` topic（Hub）。

// officialWorkspaceEventFixture 是"真 host + 真 Transport（带 journal）"的夹具。
type officialWorkspaceEventFixture struct {
	home     string
	source   *sessionSource
	journals *transport.Journals
	srv      *transport.Server
	host     *officialWorkspaceHost
	sessions int
}

func newOfficialWorkspaceEventFixture(t *testing.T) *officialWorkspaceEventFixture {
	t.Helper()
	home := t.TempDir()
	source := newSessionSource(home, "")
	journals := transport.NewJournals(nil)
	srv, err := transport.NewServer(transport.Options{Source: source, Journals: journals})
	if err != nil {
		t.Fatalf("transport.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	host := attachOfficialWorkspace(srv, home, source, nil)
	if host == nil {
		t.Fatal("attachOfficialWorkspace 返回 nil")
	}
	t.Cleanup(srv.DetachOfficialWorkspaceAPI)
	return &officialWorkspaceEventFixture{home: home, source: source, journals: journals, srv: srv, host: host}
}

// harness 给 fixture 套上"走 transport 分派"的调用口（与 officialWorkspaceHarness 同一条路径）。
func (f *officialWorkspaceEventFixture) harness() *officialWorkspaceHarness {
	return &officialWorkspaceHarness{
		home: f.home, host: f.host, api: f.host.workspaceAPI(), picker: f.host.directoryPickerAPI(),
	}
}

// eventSession 建一条真实会话并返回它的 id（测试内唯一）。
func (f *officialWorkspaceEventFixture) eventSession(t *testing.T, name string) string {
	t.Helper()
	dir := newOfficialWorkspaceDir(t, t.TempDir(), name)
	f.sessions++
	id := fmt.Sprintf("22222222-2222-2222-2222-%012d", f.sessions)
	newRealSession(t, f.home, dir, id)
	return id
}

// journalFrames 取某会话 journal 里的**全部**帧（SinceNothing 是排他下界 -1）。
func journalFrames(journals *transport.Journals, sessionID string) []transport.ServerFrame {
	frames, _, _ := journals.Get(sessionID).Since(transport.SinceNothing, 0)
	return frames
}

func journalFrameTypes(frames []transport.ServerFrame) []string {
	out := make([]string, 0, len(frames))
	for _, frame := range frames {
		out = append(out, frame.Type)
	}
	return out
}

// requireJournalEvent 断言 journal 里恰有一条该事件，且载荷是官方的 `{sessionId}`。
func requireJournalEvent(t *testing.T, journals *transport.Journals, sessionID, event string) transport.ServerFrame {
	t.Helper()
	frames := journalFrames(journals, sessionID)
	var found []transport.ServerFrame
	for _, frame := range frames {
		if frame.Type == event {
			found = append(found, frame)
		}
	}
	if len(found) != 1 {
		t.Fatalf("journal 里应恰有 1 条 %s，实际 %d 条（全部帧：%v）",
			event, len(found), journalFrameTypes(frames))
	}
	frame := found[0]
	var payload map[string]any
	if len(frame.Data) == 0 {
		t.Fatalf("%s 帧没有 data（官方载荷是 {sessionId}）", event)
	}
	if err := json.Unmarshal(frame.Data, &payload); err != nil {
		t.Fatalf("%s 的 data 不是 JSON 对象：%q", event, frame.Data)
	}
	if len(payload) != 1 {
		t.Fatalf("%s 的载荷必须**只有** sessionId（官方 SessionActivityRequest）：%v", event, payload)
	}
	if got, _ := payload["sessionId"].(string); got != sessionID {
		t.Fatalf("%s 的 sessionId 不对：%q（期望 %q）", event, got, sessionID)
	}
	if frame.Seq <= 0 {
		t.Fatalf("%s 帧必须带 journal seq（客户端要靠它补洞）：%+v", event, frame)
	}
	if frame.Time <= 0 {
		t.Fatalf("%s 帧必须带 time（毫秒墙钟）：%+v", event, frame)
	}
	return frame
}

// requireNoJournalEvent 断言 journal 里没有该事件。
func requireNoJournalEvent(t *testing.T, journals *transport.Journals, sessionID, event string) {
	t.Helper()
	for _, frame := range journalFrames(journals, sessionID) {
		if frame.Type == event {
			t.Fatalf("%s 不该出现在 journal 里（官方只在真的变更时广播）：%v",
				event, journalFrameTypes(journalFrames(journals, sessionID)))
		}
	}
}

// TestOfficialWorkspaceArchiveEventsLandInJournalAndStream 是缺口 #3 的正面判据：
//
//  1. 归档 → journal 里出现 `workspace/session-archived`，载荷恰为 `{sessionId}`；
//  2. 反归档 → journal 里出现 `workspace/session-unarchiving`，同一载荷；
//  3. 幂等路径**不**广播（官方：已经在集合里 / 本来就不在集合里都直接解析）；
//  4. 官方 stream（mux `session/events` 尾部流）读同一份 journal，客户端从流里也看得到，
//     且顺序是 archived 在前、unarchiving 在后。
func TestOfficialWorkspaceArchiveEventsLandInJournalAndStream(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "alpha")
	api := f.host.workspaceAPI()

	// 归档：事件在**落盘之后**（官方 :403 在 setState 之后）。
	if _, err := api.ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}
	requireJournalEvent(t, f.journals, sessionID, "workspace/session-archived")

	// 再归档一次：官方 "An already archived id resolves without writing, asking, or stopping."
	// → 不广播第二条。
	if _, err := api.ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("重复 archiveSession: %v", err)
	}
	requireJournalEvent(t, f.journals, sessionID, "workspace/session-archived")

	// 反归档：事件在**落盘之前**（官方 :426 在 setState 之前）。
	if _, err := api.UnarchiveSession(sessionID); err != nil {
		t.Fatalf("unarchiveSession: %v", err)
	}
	requireJournalEvent(t, f.journals, sessionID, "workspace/session-unarchiving")

	// 再反归档一次（本来就不在集合里）：官方直接解析，不广播。
	if _, err := api.UnarchiveSession(sessionID); err != nil {
		t.Fatalf("重复 unarchiveSession: %v", err)
	}
	requireJournalEvent(t, f.journals, sessionID, "workspace/session-unarchiving")

	// 官方 stream 尾部流：同一份 journal，顺序与落盘顺序一致。
	stream, fault := f.srv.OpenOfficialJournalTail(sessionID, transport.SinceNothing, 16)
	if fault != nil {
		t.Fatalf("开 session/events 尾部流失败：%d %s：%s", fault.Status, fault.Code, fault.Message)
	}
	defer stream.Close()
	var seen []string
	for i := 0; i < 2; i++ {
		raw, done, streamFault := stream.Next(context.Background())
		if streamFault != nil {
			t.Fatalf("读 session/events 流失败：%s", streamFault.Message)
		}
		if done {
			break
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("流帧不是 JSON：%v（%s）", err, raw)
		}
		if typ, _ := frame["type"].(string); typ != "" {
			seen = append(seen, typ)
		}
	}
	if len(seen) != 2 || seen[0] != "workspace/session-archived" || seen[1] != "workspace/session-unarchiving" {
		t.Fatalf("官方 stream 里的顺序应是 [session-archived, session-unarchiving]，实际 %v", seen)
	}
}

// TestOfficialWorkspaceUnarchiveEventFiresBeforeArchiveSetChanges 钉住官方**顺序**：
//
// `workspace/session-unarchiving` 必须在把 id 移出归档集**之前**广播
// （`dsh-workspace/src/index.ts:426` → `:427` 的 setState）。否则监听者（官方是 worktree
// 归档清理服务，`dsh-experimental-worktree/src/archive.ts:87-93`）就无法判断"这条会话
// 现在是不是还在归档集里"，取消/结算清理也就无从谈起。
func TestOfficialWorkspaceUnarchiveEventFiresBeforeArchiveSetChanges(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "order")
	api := f.host.workspaceAPI()

	if _, err := api.ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}

	// 投影口是宿主侧唯一的"广播点"：在它被调用时读一次归档集，就是监听者看到的现场。
	type snapshot struct {
		event       string
		stillInSet  bool
		archivedSet []string
	}
	var seen []snapshot
	original := f.host.projectEvent
	if original == nil {
		t.Fatal("attachOfficialWorkspace 没给宿主接上事件投影口（projectEvent 为 nil）")
	}
	f.host.projectEvent = func(sessionID, event string) error {
		f.host.mu.Lock()
		defer f.host.mu.Unlock()
		if err := f.host.ensureLoadedLocked(); err != nil {
			t.Fatalf("事件回调里读 registry 失败：%v", err)
		}
		seen = append(seen, snapshot{
			event:       event,
			stillInSet:  officialContains(f.host.doc.ArchivedSessionIDs, sessionID),
			archivedSet: append([]string(nil), f.host.doc.ArchivedSessionIDs...),
		})
		return original(sessionID, event)
	}
	defer func() { f.host.projectEvent = original }()

	if _, err := api.UnarchiveSession(sessionID); err != nil {
		t.Fatalf("unarchiveSession: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("反归档应广播**一条**事件，实际 %d 条：%+v", len(seen), seen)
	}
	if seen[0].event != "workspace/session-unarchiving" {
		t.Fatalf("反归档广播的事件名不对：%q", seen[0].event)
	}
	if !seen[0].stillInSet {
		t.Fatalf("workspace/session-unarchiving 必须在移出归档集**之前**广播，"+
			"实际广播时归档集已经是 %v（会话已不在里面）", seen[0].archivedSet)
	}
	if got := f.host.doc.ArchivedSessionIDs; len(got) != 0 {
		t.Fatalf("反归档落盘后归档集应为空：%v", got)
	}
}

// TestOfficialWorkspaceUnarchiveEventRejectionRetainsArchive 钉住官方另一句原话：
// "A rejection retains the archived state."（`docs/subsystems/workspace.md` / index.ts:163）
//
// 事件投递失败 = 监听者拒绝 → 归档态必须保留，且方法要**响亮**失败（不能静默成功）。
func TestOfficialWorkspaceUnarchiveEventRejectionRetainsArchive(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "reject")
	api := f.host.workspaceAPI()

	if _, err := api.ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}
	original := f.host.projectEvent
	if original == nil {
		t.Fatal("attachOfficialWorkspace 没给宿主接上事件投影口（projectEvent 为 nil）")
	}
	f.host.projectEvent = func(string, string) error {
		return fmt.Errorf("worktree 清理服务拒绝了这次反归档")
	}
	defer func() { f.host.projectEvent = original }()

	if _, err := api.UnarchiveSession(sessionID); err == nil {
		t.Fatal("事件投递失败时反归档必须报错，不能静默成功")
	}
	// 归档态保留：下面用 registry 面读回来（不是看函数返回值）。
	f.host.mu.Lock()
	if err := f.host.ensureLoadedLocked(); err != nil {
		f.host.mu.Unlock()
		t.Fatalf("读 registry: %v", err)
	}
	still := officialContains(f.host.doc.ArchivedSessionIDs, sessionID)
	f.host.mu.Unlock()
	if !still {
		t.Fatal("监听者拒绝后归档态必须保留（官方 \"A rejection retains the archived state.\"），实际已被移出")
	}
}

// TestOfficialWorkspaceArchiveEventReachesWS 证明事件真的能到 `/ws`：
// 走的是既有 Hub 的 `session:<id>` topic，不是"只在进程内"的假事件。
func TestOfficialWorkspaceArchiveEventReachesWS(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "ws")
	mounted := httptest.NewServer(f.srv)
	defer mounted.Close()

	client := newOfficialWireTestClient(t)
	connect, err := client.Get(mounted.URL + "/connect?token=" + f.srv.Token())
	if err != nil {
		t.Fatalf("GET /connect: %v", err)
	}
	connect.Body.Close()
	if connect.StatusCode != http.StatusSeeOther {
		t.Fatalf("/connect 应 303，实际 %d", connect.StatusCode)
	}
	cookieRequest, err := http.NewRequest(http.MethodGet, mounted.URL+"/", nil)
	if err != nil {
		t.Fatalf("构造 cookie 查询: %v", err)
	}
	cookies := client.Jar.Cookies(cookieRequest.URL)
	if len(cookies) == 0 {
		t.Fatal("/connect 没给 cookie")
	}

	// 先归档再订阅：`session/events` 的既有订阅回放会把 journal 里的帧补上（这正是
	// 客户端断线重连的路径），所以这里不必抢时序。
	if resp, err := client.Post(mounted.URL+"/api/workspace.archiveSession", "application/json",
		strings.NewReader(`{"sessionId":"`+sessionID+`"}`)); err != nil {
		t.Fatalf("POST workspace.archiveSession: %v", err)
	} else {
		body := make([]byte, 0)
		buf := make([]byte, 512)
		for {
			n, readErr := resp.Body.Read(buf)
			body = append(body, buf[:n]...)
			if readErr != nil {
				break
			}
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("archiveSession 应 200，实际 %d：%s", resp.StatusCode, body)
		}
	}

	ws, handshake := dialOfficialWS(t, mounted.URL, "/ws", cookies)
	defer ws.Close()
	if handshake.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("/ws 升级应 101，实际 %d", handshake.StatusCode)
	}
	ws.writeText(t, `{"t":"sub","topic":"session:`+sessionID+`","sinceSeq":-1}`)
	frame := ws.readJSON(t, 5*time.Second)
	if typ, _ := frame["type"].(string); typ != "workspace/session-archived" {
		t.Fatalf("WS 上第一条应是 workspace/session-archived，实际 %q（%v）", typ, frame)
	}
	data, ok := frame["data"].(map[string]any)
	if !ok {
		t.Fatalf("WS 帧的 data 不是对象：%v", frame)
	}
	if got, _ := data["sessionId"].(string); got != sessionID {
		t.Fatalf("WS 帧的 sessionId 不对：%q（期望 %q）", got, sessionID)
	}
}

// dialOfficialWS 用真 cookie 打通 `/ws`（返回未关闭的连接；调用方负责 Close）。
//
// 与 `dialWireMux` 同一套裸 WS 客户端（复用 `wireWS` 的读写），只换了路径：
// `/ws` 是 L1 的订阅面，`/api/remote.mux` 是 L2 的。
func dialOfficialWS(t *testing.T, base, path string, cookies []*http.Cookie) (*wireWS, *http.Response) {
	t.Helper()
	address := strings.TrimPrefix(base, "http://")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("连接 %s: %v", address, err)
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		t.Fatalf("生成 key: %v", err)
	}
	var request strings.Builder
	fmt.Fprintf(&request, "GET %s HTTP/1.1\r\nHost: %s\r\n", path, address)
	request.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n")
	fmt.Fprintf(&request, "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n",
		base64.StdEncoding.EncodeToString(keyBytes))
	for _, cookie := range cookies {
		fmt.Fprintf(&request, "Cookie: %s=%s\r\n", cookie.Name, cookie.Value)
	}
	request.WriteString("\r\n")
	if _, err := conn.Write([]byte(request.String())); err != nil {
		conn.Close()
		t.Fatalf("写握手: %v", err)
	}
	br := bufio.NewReader(conn)
	response, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		t.Fatalf("读握手响应: %v", err)
	}
	return &wireWS{conn: conn, br: br}, response
}

// ---------------------------------------------------------------------------
// 反归档与 rc.2「归档时清理工作树」状态机的协同（缺口 #3 第 2 条）
// ---------------------------------------------------------------------------
//
// 官方指南（`docs/upgrade-guide/v0.2.2-rc.1/archive-worktrees/guide.md` §Migration 2）：
// "To resume an archived Session whose checkout was removed, unarchive it and use
// **Restore worktree**. The retained branch is checked out at the recorded path."
//
// 也就是说 checkout 被归档清理移除之后，恢复走 `forgePanel.restoreWorktree` 背后的
// `worktree.Service.RestoreWorktree`（internal/worktree/worktree.go:636）。反归档
// 必须在这条路径上如实报告结果：走了就说走了，失败了就**响亮**失败，不许静默成功。

// fakeArchiveWorktree 是反归档协同用的假 worktree 能力（真实现是 *worktree.Service）。
type fakeArchiveWorktree struct {
	state      worktree.State
	stateErr   error
	restoreErr error
	restored   []string

	// 取消点（官方 archive.ts:87-93）：默认"没有在途尝试"（零值 = no-op）。
	cancelErr    error
	cancellation worktree.CleanupCancellation
	cancelled    []string
}

func (f *fakeArchiveWorktree) State(string) (worktree.State, error) {
	if f.stateErr != nil {
		return worktree.State{}, f.stateErr
	}
	return f.state, nil
}

func (f *fakeArchiveWorktree) CancelArchiveCleanup(sessionID string) (worktree.CleanupCancellation, error) {
	f.cancelled = append(f.cancelled, sessionID)
	return f.cancellation, f.cancelErr
}

func (f *fakeArchiveWorktree) RestoreWorktree(sessionID string) error {
	f.restored = append(f.restored, sessionID)
	return f.restoreErr
}

// TestOfficialWorkspaceUnarchiveRestoresWorktreeRemovedByArchiveCleanup
//
// 反归档时 checkout 已经被归档清理移除 → 必须走官方恢复路径（RestoreWorktree），
// 而且反归档本身仍然成功（官方语义：恢复是独立动作）。
func TestOfficialWorkspaceUnarchiveRestoresWorktreeRemovedByArchiveCleanup(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "restore")
	api := f.host.workspaceAPI()
	if _, err := api.ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}

	removed := filepath.Join(t.TempDir(), "checkout-removed-by-cleanup")
	fake := &fakeArchiveWorktree{state: worktree.State{
		Status:    worktree.StatusCreated,
		Directory: removed,
		Branch:    "dsh/worktree-x",
	}}
	f.host.worktreeOverride = fake

	set, err := api.UnarchiveSession(sessionID)
	if err != nil {
		t.Fatalf("checkout 被清理后反归档仍应成功（恢复是独立动作）：%v", err)
	}
	if len(set) != 0 {
		t.Fatalf("反归档后归档集应为空：%v", set)
	}
	if len(fake.restored) != 1 || fake.restored[0] != sessionID {
		t.Fatalf("checkout 已不在时必须调官方恢复路径 RestoreWorktree(%q)，实际 %v", sessionID, fake.restored)
	}
}

// TestOfficialWorkspaceUnarchiveReportsWorktreeRestoreFailure 是"不能假装恢复"的判据：
//
// 恢复失败 → 反归档整体**响亮**失败（四字段 500 internal），错误里带真实原因与
// 真实目录，并明说归档集已经变了；不许返回 200 让客户端以为工作树回来了。
func TestOfficialWorkspaceUnarchiveReportsWorktreeRestoreFailure(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "restorefail")
	if _, err := f.host.workspaceAPI().ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}

	removed := filepath.Join(t.TempDir(), "checkout-gone")
	fake := &fakeArchiveWorktree{
		state:      worktree.State{Status: worktree.StatusCreated, Directory: removed},
		restoreErr: fmt.Errorf("git worktree add 失败：目标分支已被占用"),
	}
	f.host.worktreeOverride = fake

	// wire 层：四字段 500 internal（不是 200 + 空 result），message 带真实原因。
	code, out := f.harness().call(t, "workspace.unarchiveSession", map[string]any{"sessionId": sessionID})
	if code != http.StatusInternalServerError {
		t.Fatalf("恢复失败应报 500 internal，实际 %d（%v）", code, out)
	}
	errObj, _ := out["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("四字段 code 应是 internal：%v", out)
	}
	message, _ := errObj["message"].(string)
	if !strings.Contains(message, removed) {
		t.Fatalf("错误里必须点名被清理的 checkout 目录 %q：%s", removed, message)
	}
	if !strings.Contains(message, "目标分支已被占用") {
		t.Fatalf("错误里必须带官方恢复路径的真实失败原因：%s", message)
	}
	if !strings.Contains(message, "已移出归档集") {
		t.Fatalf("错误里必须如实说明归档态已经改变（否则客户端会以为状态没动）：%s", message)
	}
	if len(fake.restored) != 1 || fake.restored[0] != sessionID {
		t.Fatalf("恢复必须真的走官方路径一次：%v", fake.restored)
	}

	// 归档集已经落盘为空（这是真实状态，不因为恢复失败而回滚）。
	f.host.mu.Lock()
	if err := f.host.ensureLoadedLocked(); err != nil {
		f.host.mu.Unlock()
		t.Fatalf("读 registry: %v", err)
	}
	stillArchived := officialContains(f.host.doc.ArchivedSessionIDs, sessionID)
	f.host.mu.Unlock()
	if stillArchived {
		t.Fatal("官方语义下 unarchive 的归档集变更在恢复之前就已落盘，不该回滚")
	}
}

// TestOfficialWorkspaceUnarchiveSkipsWorktreeRestoreWhenNothingToRestore
//
// 两条"无恢复可做"的分支都不能乱动工作树：
//   - checkout 还在（清理被跳过/保留策略生效）；
//   - 这条会话压根没创建过 checkout。
func TestOfficialWorkspaceUnarchiveSkipsWorktreeRestoreWhenNothingToRestore(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)

	// ① checkout 仍在。
	sessionID := f.eventSession(t, "present")
	present := t.TempDir()
	if _, err := f.host.workspaceAPI().ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}
	presentFake := &fakeArchiveWorktree{state: worktree.State{
		Status: worktree.StatusCreated, Directory: present,
	}}
	f.host.worktreeOverride = presentFake
	if _, err := f.host.workspaceAPI().UnarchiveSession(sessionID); err != nil {
		t.Fatalf("checkout 还在时反归档应成功：%v", err)
	}
	if len(presentFake.restored) != 0 {
		t.Fatalf("checkout 还在时不许调 RestoreWorktree（官方在目录存在时也是 409）：%v", presentFake.restored)
	}

	// ② 没有创建过 checkout。
	noWorktree := f.eventSession(t, "none")
	if _, err := f.host.workspaceAPI().ArchiveSession(noWorktree, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}
	noneFake := &fakeArchiveWorktree{state: worktree.State{Status: worktree.StatusNone}}
	f.host.worktreeOverride = noneFake
	if _, err := f.host.workspaceAPI().UnarchiveSession(noWorktree); err != nil {
		t.Fatalf("没有 checkout 时反归档应成功：%v", err)
	}
	if len(noneFake.restored) != 0 {
		t.Fatalf("没有创建过 checkout 时不许调 RestoreWorktree：%v", noneFake.restored)
	}
}

// TestOfficialWorkspaceUnarchiveWithoutWorktreeServiceIsStillHonest
//
// 宿主没有 worktree 服务（不带 serve 循环的入口）时：
// 反归档本身照官方语义成功，但不许声称"检查/恢复过"——只记一行 stderr。
func TestOfficialWorkspaceUnarchiveWithoutWorktreeServiceIsStillHonest(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "nowt")
	api := f.host.workspaceAPI()
	if _, err := api.ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}
	// 把服务能力拿掉：override 与 prompts 都为空（fixture 的 prompts 本来就是 nil）。
	f.host.worktreeOverride = nil
	if _, err := api.UnarchiveSession(sessionID); err != nil {
		t.Fatalf("没有 worktree 服务时反归档仍应成功（恢复检查会记 stderr）：%v", err)
	}
	// 事件照发（归档/反归档各一条），只是"恢复检查"这件事做不了——它只进 stderr，
	// 不伪造任何"已恢复"的帧。
	requireJournalEvent(t, f.journals, sessionID, "workspace/session-archived")
	requireJournalEvent(t, f.journals, sessionID, "workspace/session-unarchiving")
}

// TestOfficialWorkspaceConcurrentUnarchiveBroadcastsOnce
//
// 并发反归档（UI 连点、或 restore 与 unarchive 撞在一起）只能广播**一条**
// `workspace/session-unarchiving`、只做**一次**工作树恢复：
//
// 官方 registry 用写队列把 check-then-write 串起来（`enqueueOperation`），所以
// 第二个调用看到的是"已经不在归档集里"→ 直接解析，不再广播。这里用
// `archiveOps` 达到同样效果。
func TestOfficialWorkspaceConcurrentUnarchiveBroadcastsOnce(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "concurrent")
	api := f.host.workspaceAPI()
	if _, err := api.ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}

	// checkout 已被清理：并发里只该有一次恢复（第二次不该再调）。
	removed := filepath.Join(t.TempDir(), "concurrent-gone")
	fake := &fakeArchiveWorktree{state: worktree.State{
		Status: worktree.StatusCreated, Directory: removed,
	}}
	f.host.worktreeOverride = fake

	const callers = 4
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, errs[slot] = api.UnarchiveSession(sessionID)
		}(i)
	}
	wg.Wait()
	for slot, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个并发反归档失败：%v", slot, err)
		}
	}

	// 事件只一条：`requireJournalEvent` 断言"恰有 1 条"。
	requireJournalEvent(t, f.journals, sessionID, "workspace/session-unarchiving")
	if len(fake.restored) != 1 {
		t.Fatalf("并发反归档只该恢复一次工作树，实际 %d 次：%v", len(fake.restored), fake.restored)
	}
	// 取消点同样只准入一次：并发里第二个调用看到的是"已经不在归档集"，直接解析
	// （官方 index.ts:426 的提前 return），不会再去取消/恢复第二遍。
	if len(fake.cancelled) != 1 {
		t.Fatalf("并发反归档只该调一次 CancelArchiveCleanup，实际 %d 次：%v",
			len(fake.cancelled), fake.cancelled)
	}
}
