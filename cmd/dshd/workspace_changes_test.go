package main

// 缺口 ③ 的端到端：真的改文件 → 真的拍快照 → 真的从 `/api/changes.*` 取 diff。
//
// 这条链要证明的是"GUI deliverables 卡片拿得到真实数据"：
//   ① `dshd run` 的一轮（这里直接驱动同一个记录器）在真实 git 仓库里落了一条
//      `workspace/changes` 事件 + 一份记录；
//   ② `transport` 的真 server 上，`GET /api/changes.summary` 给出真实的
//      total/added/deleted 与文件清单（不是空壳）；
//   ③ `GET /api/changes.diff` 给出真实的统一 diff hunk（内容逐行对得上）；
//   ④ `POST /api/changes.open` 把真实绝对路径交给桌面通道（fake 记下来）。
//
// 没有 git 的环境整条 skip：这不是"模拟真实"，而是这条链的真实前提。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

func runWorkspaceChangesGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func initWorkspaceChangesRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("本机没有 git：workspace/changes 记录器没有快照来源")
	}
	repo := t.TempDir()
	runWorkspaceChangesGit(t, repo, "init", "-q")
	runWorkspaceChangesGit(t, repo, "config", "user.email", "changes@example.invalid")
	runWorkspaceChangesGit(t, repo, "config", "user.name", "changes")
	writeFile(t, filepath.Join(repo, "a.txt"), "one\ntwo\n")
	writeFile(t, filepath.Join(repo, "gone.txt"), "gone\n")
	runWorkspaceChangesGit(t, repo, "add", "-A")
	runWorkspaceChangesGit(t, repo, "commit", "-qm", "init")
	return repo
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startChangesSession 建一个真实 v4 会话并开出第 1 轮。
func startChangesSession(t *testing.T, home, repo, sid string) *session.Log {
	t.Helper()
	logw, err := session.CreateSession(home, repo, sid, modes.DefaultID)
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	if err := logw.Append("turn/start", session.TurnStartData(1)); err != nil {
		t.Fatalf("写 turn/start: %v", err)
	}
	return logw
}

// loadOnlyChangesRecord 读回唯一的记录（记录器一轮只该写一份）。
func loadOnlyChangesRecord(t *testing.T, home, sid string) workspaceChangesRecord {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(home, "state", "workspace-changes", sid, "records", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("记录数 = %d（%v），期望恰好 1 份", len(paths), paths)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var record workspaceChangesRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("解析记录 %s: %v", paths[0], err)
	}
	return record
}

func TestWorkspaceChangesRoutesServeRealGitDiff(t *testing.T) {
	home := t.TempDir()
	repo := initWorkspaceChangesRepo(t)
	const sid = "session-changes-e2e"
	logw := startChangesSession(t, home, repo, sid)

	recorder := startWorkspaceChangesTurn(home, logw, repo, 1)
	if recorder.repo == nil {
		t.Fatal("记录器没定位到仓库：真实 git 仓库里必须能拍快照")
	}
	// 真实改动：改一个、删一个、加一个。
	writeFile(t, filepath.Join(repo, "a.txt"), "one\nTWO\nthree\n")
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "new.txt"), "brand new\n")
	recorder.finish()
	if err := logw.Close(); err != nil {
		t.Fatal(err)
	}

	record := loadOnlyChangesRecord(t, home, sid)
	if record.Turn != 1 || record.Total != 3 {
		t.Fatalf("记录 turn=%d total=%d files=%+v", record.Turn, record.Total, record.Files)
	}
	if record.Added != 3 || record.Deleted != 2 { // a.txt +2/-1，gone.txt -1，new.txt +1
		t.Fatalf("记录行数 added=%d deleted=%d，期望 3/2", record.Added, record.Deleted)
	}
	if len(record.Files) != 3 {
		t.Fatalf("记录文件数 = %d：%+v", len(record.Files), record.Files)
	}
	// 事件真的落了盘，而且 seq 与记录一致。
	parsed, err := v4.ReadFile(logw.Path)
	if err != nil {
		t.Fatalf("读会话: %v", err)
	}
	foundEvent := false
	for _, event := range parsed.Events {
		if event.Type == "workspace/changes" && event.Seq == record.Seq {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("seq=%d 没有 workspace/changes 事件：%+v", record.Seq, parsed.Events)
	}

	// 真 server + 真 cookie，走官方三条路由。
	store := newWorkspaceChangesStore(home)
	native := &fakeNativeService{canOpen: true}
	srv, err := transport.NewServer(transport.Options{
		Changes: transport.ChangesAPI{
			Summary: store.Summary, Diff: store.Diff, Open: changesOpenOnDesktop(store, native),
		},
	})
	if err != nil {
		t.Fatalf("建 server: %v", err)
	}
	base, err := srv.Start()
	if err != nil {
		_ = srv.Close()
		t.Fatalf("起 server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	resp.Body.Close()

	baseQuery := "/api/changes.summary?sessionId=" + sid + "&seq=" + itoa(int(record.Seq))
	summary := getChangesJSON(t, client, base+baseQuery)
	if numField(summary, "turn") != 1 || numField(summary, "total") != 3 {
		t.Fatalf("summary = %v", summary)
	}
	if numField(summary, "added") != 3 || numField(summary, "deleted") != 2 {
		t.Fatalf("summary 行数 = %v", summary)
	}
	files, _ := summary["files"].([]any)
	if len(files) != 3 {
		t.Fatalf("summary.files = %v", summary["files"])
	}
	indexOf := func(name string) int {
		for i, item := range files {
			file, _ := item.(map[string]any)
			if file["path"] == name {
				return i
			}
		}
		t.Fatalf("summary.files 里没有 %s：%v", name, files)
		return -1
	}

	// a.txt：两侧都存在，hunk 里有 -two / +TWO / +three。
	diffA := getChangesJSON(t, client, base+"/api/changes.diff?sessionId="+sid+"&seq="+itoa(int(record.Seq))+"&index="+itoa(indexOf("a.txt")))
	if diffA["kind"] != "text" || diffA["before"] != true || diffA["after"] != true || diffA["coarse"] != false {
		t.Fatalf("a.txt diff 头 = %v", diffA)
	}
	lines := hunkLines(diffA)
	for _, want := range []string{"-two", "+TWO", "+three", " one"} {
		if !containsLine(lines, want) {
			t.Fatalf("a.txt diff 缺 %q：%v", want, lines)
		}
	}

	// new.txt：before=false, after=true。
	diffNew := getChangesJSON(t, client, base+"/api/changes.diff?sessionId="+sid+"&seq="+itoa(int(record.Seq))+"&index="+itoa(indexOf("new.txt")))
	if diffNew["before"] != false || diffNew["after"] != true || !containsLine(hunkLines(diffNew), "+brand new") {
		t.Fatalf("new.txt diff = %v", diffNew)
	}

	// gone.txt：before=true, after=false，删除行在里面。
	diffGone := getChangesJSON(t, client, base+"/api/changes.diff?sessionId="+sid+"&seq="+itoa(int(record.Seq))+"&index="+itoa(indexOf("gone.txt")))
	if diffGone["before"] != true || diffGone["after"] != false || !containsLine(hunkLines(diffGone), "-gone") {
		t.Fatalf("gone.txt diff = %v", diffGone)
	}

	// open：真实绝对路径交给桌面通道；reveal 走同一份服务。
	openURL := base + "/api/changes.open?sessionId=" + sid + "&seq=" + itoa(int(record.Seq)) + "&index=" + itoa(indexOf("a.txt"))
	postChanges(t, client, openURL, http.StatusOK)
	wantOpened := workspaceChangesCanonical(filepath.Join(repo, "a.txt"))
	if len(native.opened) != 1 || workspaceChangesCanonical(native.opened[0]) != wantOpened {
		t.Fatalf("open 拿到的路径 = %q，期望 %q", native.opened, wantOpened)
	}
	postChanges(t, client, openURL+"&action=reveal", http.StatusOK)
	if len(native.revealed) != 1 || workspaceChangesCanonical(native.revealed[0]) != wantOpened {
		t.Fatalf("reveal 拿到的路径 = %q，期望 %q", native.revealed, wantOpened)
	}

	// 不存在的 seq / 越界 index → 404；缺参数 → 400。
	getChangesStatus(t, client, base+"/api/changes.summary?sessionId="+sid+"&seq=999", http.StatusNotFound)
	getChangesStatus(t, client, base+"/api/changes.diff?sessionId="+sid+"&seq="+itoa(int(record.Seq))+"&index=99", http.StatusNotFound)
	getChangesStatus(t, client, base+"/api/changes.summary?seq=1", http.StatusBadRequest)
	getChangesStatus(t, client, base+"/api/changes.diff?sessionId="+sid+"&seq="+itoa(int(record.Seq)), http.StatusBadRequest)
}

// TestWorkspaceChangesSkipsTurnWithoutChanges：没改任何文件的一轮不写事件、不写记录
// （GUI 不该因此画一张空卡片）。
func TestWorkspaceChangesSkipsTurnWithoutChanges(t *testing.T) {
	home := t.TempDir()
	repo := initWorkspaceChangesRepo(t)
	const sid = "session-changes-noop"
	logw := startChangesSession(t, home, repo, sid)
	recorder := startWorkspaceChangesTurn(home, logw, repo, 1)
	recorder.finish()
	if err := logw.Close(); err != nil {
		t.Fatal(err)
	}
	if paths, _ := filepath.Glob(filepath.Join(home, "state", "workspace-changes", sid, "records", "*.json")); len(paths) != 0 {
		t.Fatalf("没改文件却写了记录：%v", paths)
	}
	parsed, err := v4.ReadFile(logw.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range parsed.Events {
		if event.Type == "workspace/changes" {
			t.Fatalf("没改文件却写了 workspace/changes 事件：%+v", event)
		}
	}
}

// TestWorkspaceChangesSkipsSubagentSessions：子代理会话不进变更摘要（官方 eligible()）。
func TestWorkspaceChangesSkipsSubagentSessions(t *testing.T) {
	home := t.TempDir()
	repo := initWorkspaceChangesRepo(t)
	const sid = "child-changes-e2e"
	path := filepath.Join(home, "sessions", "child", sid, "session.v4.jsonl.zstd")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writer, err := v4.Create(path, v4.Header{
		Type: "session", Version: 4, ID: sid, CreatedAt: 1, Cwd: repo, DelegationDepth: 1,
	})
	if err != nil {
		t.Fatalf("造子会话: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatalf("打开子会话: %v", err)
	}
	recorder := startWorkspaceChangesTurn(home, logw, repo, 1)
	if recorder.repo != nil {
		t.Fatal("子代理会话竟然开了变更记录器")
	}
	recorder.finish()
	if err := logw.Close(); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func getChangesJSON(t *testing.T, client *http.Client, url string) map[string]any {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d", url, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s 响应不是 JSON: %v", url, err)
	}
	return out
}

func getChangesStatus(t *testing.T, client *http.Client, url string, want int) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("GET %s status=%d want=%d", url, resp.StatusCode, want)
	}
}

func postChanges(t *testing.T, client *http.Client, url string, want int) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("POST %s status=%d want=%d", url, resp.StatusCode, want)
	}
}

// numField 读一个 JSON 数字字段；缺字段/类型不对返回 -1（断言失败时打印整个对象）。
func numField(object map[string]any, key string) int {
	value, ok := object[key].(float64)
	if !ok {
		return -1
	}
	return int(value)
}

func hunkLines(diff map[string]any) []string {
	var out []string
	hunks, _ := diff["hunks"].([]any)
	for _, item := range hunks {
		hunk, _ := item.(map[string]any)
		lines, _ := hunk["lines"].([]any)
		for _, line := range lines {
			if text, ok := line.(string); ok {
				out = append(out, text)
			}
		}
	}
	return out
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}
