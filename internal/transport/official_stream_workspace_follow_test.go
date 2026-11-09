package transport

// workspace.follow 的协议层测试：真实帧机 + 真实 HTTP NDJSON + 取消。
//
// 数据源是测试里的一台"可变 registry"（与宿主同一份快照片机），验证
// baseline→upsert/order/archived/pinned/remove 的分类与帧形状。

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeWorkspaceFollowSource struct {
	mu   sync.Mutex
	snap OfficialWorkspaceFollowSnapshot
	err  error
}

func (f *fakeWorkspaceFollowSource) WorkspaceFollowSnapshot() (OfficialWorkspaceFollowSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return OfficialWorkspaceFollowSnapshot{}, f.err
	}
	return f.snap, nil
}

func (f *fakeWorkspaceFollowSource) set(snapshot OfficialWorkspaceFollowSnapshot) {
	f.mu.Lock()
	f.snap = snapshot
	f.mu.Unlock()
}

func (f *fakeWorkspaceFollowSource) fail(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func workspaceView(id, title string) OfficialWorkspace {
	return OfficialWorkspace{
		WorkspaceID: id, Path: "C:/work/" + id, Title: title,
		SessionIDs: []string{"s-" + id}, CreatedAt: "2026-11-08T12:00:00Z",
		UpdatedAt: "2026-11-08T12:00:00Z",
	}
}

// readFollowFrame 从帧机读一帧（带超时，避免测试挂死）。
func readFollowFrame(t *testing.T, stream OfficialFrameStream, timeout time.Duration) map[string]any {
	t.Helper()
	type result struct {
		frame json.RawMessage
		done  bool
		fault *OfficialStreamFault
	}
	ch := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	go func() {
		frame, done, fault := stream.Next(ctx)
		ch <- result{frame: frame, done: done, fault: fault}
	}()
	select {
	case got := <-ch:
		if got.fault != nil {
			t.Fatalf("流故障: %v", got.fault)
		}
		if got.done {
			t.Fatalf("流意外结束")
		}
		var out map[string]any
		if err := json.Unmarshal(got.frame, &out); err != nil {
			t.Fatalf("帧不是 JSON: %q", got.frame)
		}
		return out
	case <-time.After(timeout + time.Second):
		t.Fatalf("等帧超时（%v）", timeout)
		return nil
	}
}

func TestOfficialWorkspaceFollowBaselineAndIncrements(t *testing.T) {
	oldInterval := officialStreamPollInterval
	officialStreamPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { officialStreamPollInterval = oldInterval })

	source := &fakeWorkspaceFollowSource{}
	source.set(OfficialWorkspaceFollowSnapshot{})
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialWorkspaceFollowSource(source)
	t.Cleanup(srv.DetachOfficialWorkspaceFollowSource)

	stream, fault := srv.OpenOfficialWorkspaceFollowStream()
	if fault != nil {
		t.Fatalf("Open: %v", fault)
	}
	defer stream.Close()

	baseline := readFollowFrame(t, stream, time.Second)
	if baseline["type"] != "baseline" {
		t.Fatalf("首帧必须 baseline: %#v", baseline)
	}
	value := baseline["value"].(map[string]any)
	if value["items"] == nil || value["archivedSessionIds"] == nil || value["pinnedSessionIds"] == nil {
		t.Fatalf("baseline 数组不能是 null: %#v", value)
	}
	if len(value["items"].([]any)) != 0 {
		t.Fatalf("空 registry 的 baseline items = %#v", value["items"])
	}

	// 新增一个 workspace：upsert + order（order 从空变成一条）。
	source.set(OfficialWorkspaceFollowSnapshot{Items: []OfficialWorkspace{workspaceView("w1", "一")}})
	first := readFollowFrame(t, stream, time.Second)
	second := readFollowFrame(t, stream, time.Second)
	if first["type"] != "upsert" || second["type"] != "order" {
		t.Fatalf("新增应产生 upsert+order，实际 %v / %v", first["type"], second["type"])
	}
	ws := first["workspace"].(map[string]any)
	if ws["workspaceId"] != "w1" || ws["path"] != "C:/work/w1" || ws["title"] != "一" {
		t.Fatalf("upsert.workspace 形状错: %#v", ws)
	}
	if ids := second["workspaceIds"].([]any); len(ids) != 1 || ids[0] != "w1" {
		t.Fatalf("order = %#v", second["workspaceIds"])
	}

	// 改名：只 upsert，不 order。
	renamed := workspaceView("w1", "二")
	renamed.UpdatedAt = "2026-11-08T12:01:00Z"
	source.set(OfficialWorkspaceFollowSnapshot{Items: []OfficialWorkspace{renamed}})
	upsert := readFollowFrame(t, stream, time.Second)
	if upsert["type"] != "upsert" || upsert["workspace"].(map[string]any)["title"] != "二" {
		t.Fatalf("改名应只 upsert: %#v", upsert)
	}

	// 顺序变化：新增 w2 放到前面 → upsert(w2) + order；w1 只是换位置，不 upsert。
	w2 := workspaceView("w2", "三")
	source.set(OfficialWorkspaceFollowSnapshot{Items: []OfficialWorkspace{w2, renamed}})
	upsertW2 := readFollowFrame(t, stream, time.Second)
	orderFrame := readFollowFrame(t, stream, time.Second)
	if upsertW2["type"] != "upsert" || orderFrame["type"] != "order" {
		t.Fatalf("新增+换序应产生 upsert(w2)+order，实际 %v / %v", upsertW2["type"], orderFrame["type"])
	}
	if ids := orderFrame["workspaceIds"].([]any); len(ids) != 2 || ids[0] != "w2" || ids[1] != "w1" {
		t.Fatalf("order = %#v", ids)
	}

	// archived / pinned。
	source.set(OfficialWorkspaceFollowSnapshot{
		Items:              []OfficialWorkspace{w2, renamed},
		ArchivedSessionIDs: []string{"s-a"},
		PinnedSessionIDs:   []string{"s-p"},
	})
	archived := readFollowFrame(t, stream, time.Second)
	pinned := readFollowFrame(t, stream, time.Second)
	if archived["type"] != "archived" || len(archived["archivedSessionIds"].([]any)) != 1 {
		t.Fatalf("archived 帧 = %#v", archived)
	}
	if pinned["type"] != "pinned" || len(pinned["pinnedSessionIds"].([]any)) != 1 {
		t.Fatalf("pinned 帧 = %#v", pinned)
	}

	// 删除：remove + order。
	source.set(OfficialWorkspaceFollowSnapshot{Items: []OfficialWorkspace{w2}})
	remove := readFollowFrame(t, stream, time.Second)
	order := readFollowFrame(t, stream, time.Second)
	if remove["type"] != "remove" || remove["workspaceId"] != "w1" {
		t.Fatalf("remove 帧 = %#v", remove)
	}
	if order["type"] != "order" || len(order["workspaceIds"].([]any)) != 1 {
		t.Fatalf("删除后的 order = %#v", order)
	}
}

func TestOfficialWorkspaceFollowHTTPNDJSONAndParams(t *testing.T) {
	oldInterval := officialStreamPollInterval
	officialStreamPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { officialStreamPollInterval = oldInterval })

	source := &fakeWorkspaceFollowSource{}
	source.set(OfficialWorkspaceFollowSnapshot{Items: []OfficialWorkspace{workspaceView("w1", "一")}})
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialWorkspaceFollowSource(source)
	t.Cleanup(srv.DetachOfficialWorkspaceFollowSource)
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeOfficialWorkspaceFollowHTTP))
	defer ts.Close()

	// 参数错：写响应头之前就是 400（不是 200 之后才报错）。
	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{"extra":1}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bad params: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("带字段状态 = %d，want 400", resp.StatusCode)
	}

	// 正常：200 + ndjson，一行 baseline。
	resp, err = http.Post(ts.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("follow: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "x-ndjson") {
		t.Fatalf("状态/类型 = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	select {
	case line := <-lines:
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil || frame["type"] != "baseline" {
			t.Fatalf("第一行不是 baseline: %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NDJSON 没出 baseline")
	}
	// 变更后同一连接继续出 upsert。
	source.set(OfficialWorkspaceFollowSnapshot{
		Items: []OfficialWorkspace{workspaceView("w1", "一"), workspaceView("w2", "二")},
	})
	select {
	case line := <-lines:
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil || frame["type"] != "upsert" {
			t.Fatalf("变更行不是 upsert: %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NDJSON 没出变更帧")
	}
}

func TestOfficialWorkspaceFollowWithoutSourceIs501(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	_, fault := srv.OpenOfficialWorkspaceFollowStream()
	if fault == nil || fault.Status != http.StatusNotImplemented {
		t.Fatalf("没有来源时必须 501: %#v", fault)
	}
	if !strings.Contains(fault.Message, "workspace.follow") || !strings.Contains(fault.Message, "OfficialWorkspaceFollowSource") {
		t.Fatalf("501 要点名缺什么: %q", fault.Message)
	}
}
