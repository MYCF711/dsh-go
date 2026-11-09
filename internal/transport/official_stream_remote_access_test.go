package transport

// remoteAccess.follow 的协议层测试：owner 单帧 + 非 owner baseline/upsert/remove +
// 真实 HTTP NDJSON + peer 身份来自 context。

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

// fakeRemoteAccessSource 是一台可变的真实 grant 源（测试里模拟策略表重算）。
type fakeRemoteAccessSource struct {
	mu      sync.Mutex
	snap    OfficialRemoteAccessSnapshot
	err     error
	seenPID []string
}

func (f *fakeRemoteAccessSource) api() OfficialRemoteAccessAPI {
	return OfficialRemoteAccessAPI{Snapshot: func(peerID string) (OfficialRemoteAccessSnapshot, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.seenPID = append(f.seenPID, peerID)
		if f.err != nil {
			return OfficialRemoteAccessSnapshot{}, f.err
		}
		return f.snap, nil
	}}
}

func (f *fakeRemoteAccessSource) set(snapshot OfficialRemoteAccessSnapshot) {
	f.mu.Lock()
	f.snap = snapshot
	f.mu.Unlock()
}

func readRemoteFrame(t *testing.T, stream OfficialFrameStream, timeout time.Duration) map[string]any {
	t.Helper()
	ch := make(chan struct {
		frame json.RawMessage
		done  bool
		fault *OfficialStreamFault
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	go func() {
		frame, done, fault := stream.Next(ctx)
		ch <- struct {
			frame json.RawMessage
			done  bool
			fault *OfficialStreamFault
		}{frame, done, fault}
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

func TestOfficialRemoteAccessFollowOwnerIsOneFrameThenWaits(t *testing.T) {
	oldInterval := officialStreamPollInterval
	officialStreamPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { officialStreamPollInterval = oldInterval })

	source := &fakeRemoteAccessSource{}
	source.set(OfficialRemoteAccessSnapshot{Owner: true})
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialRemoteAccessAPI(source.api())
	t.Cleanup(srv.DetachOfficialRemoteAccessAPI)

	stream, fault := srv.OpenOfficialRemoteAccessFollowStream(context.Background())
	if fault != nil {
		t.Fatalf("Open: %v", fault)
	}
	defer stream.Close()
	frame := readRemoteFrame(t, stream, time.Second)
	if frame["type"] != "baseline" || frame["owner"] != true {
		t.Fatalf("owner baseline = %#v", frame)
	}
	if _, has := frame["grants"]; has {
		t.Fatalf("owner 帧不能带 grants: %#v", frame)
	}
	// owner 之后没有增量：用短 ctx 验证流"挂住"而不是发假帧/结束。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, done, fault := stream.Next(ctx); !done || fault != nil {
		t.Fatalf("owner 流应在取消时 done=true, fault=nil：done=%v fault=%v", done, fault)
	}
}

func TestOfficialRemoteAccessFollowNonOwnerBaselineUpsertRemove(t *testing.T) {
	oldInterval := officialStreamPollInterval
	officialStreamPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { officialStreamPollInterval = oldInterval })

	source := &fakeRemoteAccessSource{}
	source.set(OfficialRemoteAccessSnapshot{Owner: false, Grants: []OfficialRemoteAccessGrant{
		{Resource: OfficialRemoteAccessResource{Kind: "session", ID: "s1"},
			Actions: []string{"session/prompt"}},
	}})
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialRemoteAccessAPI(source.api())
	t.Cleanup(srv.DetachOfficialRemoteAccessAPI)

	ctx := WithOfficialRemoteAccessPeer(context.Background(), "peer-42")
	stream, fault := srv.OpenOfficialRemoteAccessFollowStream(ctx)
	if fault != nil {
		t.Fatalf("Open: %v", fault)
	}
	defer stream.Close()
	baseline := readRemoteFrame(t, stream, time.Second)
	if baseline["type"] != "baseline" || baseline["owner"] != false {
		t.Fatalf("非 owner baseline = %#v", baseline)
	}
	grants := baseline["grants"].([]any)
	if len(grants) != 1 {
		t.Fatalf("grants = %#v", grants)
	}
	grant := grants[0].(map[string]any)
	if grant["resource"].(map[string]any)["id"] != "s1" ||
		grant["actions"].([]any)[0] != "session/prompt" {
		t.Fatalf("grant 形状 = %#v", grant)
	}

	// actions 变化 → upsert（actions 排序去重）。
	source.set(OfficialRemoteAccessSnapshot{Owner: false, Grants: []OfficialRemoteAccessGrant{
		{Resource: OfficialRemoteAccessResource{Kind: "session", ID: "s1"},
			Actions: []string{"session/abort", "session/prompt"}},
	}})
	upsert := readRemoteFrame(t, stream, time.Second)
	if upsert["type"] != "upsert" {
		t.Fatalf("actions 变化应 upsert: %#v", upsert)
	}
	actions := upsert["grant"].(map[string]any)["actions"].([]any)
	if len(actions) != 2 || actions[0] != "session/abort" {
		t.Fatalf("actions 必须排序: %#v", actions)
	}

	// 新增一个 workspace grant + 删掉 session grant → upsert(workspace) + remove(session)。
	source.set(OfficialRemoteAccessSnapshot{Owner: false, Grants: []OfficialRemoteAccessGrant{
		{Resource: OfficialRemoteAccessResource{Kind: "workspace", ID: "w1"},
			Actions: []string{"workspace.read"}},
	}})
	first := readRemoteFrame(t, stream, time.Second)
	second := readRemoteFrame(t, stream, time.Second)
	if first["type"] != "upsert" || second["type"] != "remove" {
		t.Fatalf("增删应产生 upsert+remove，实际 %v / %v", first["type"], second["type"])
	}
	if resource := second["resource"].(map[string]any); resource["kind"] != "session" || resource["id"] != "s1" {
		t.Fatalf("remove.resource = %#v", resource)
	}

	// peer 身份确实来自 ctx。
	source.mu.Lock()
	seen := append([]string{}, source.seenPID...)
	source.mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("Snapshot 没被调用")
	}
	for _, id := range seen {
		if id != "peer-42" {
			t.Fatalf("Snapshot 收到的 peerID = %q，want peer-42", id)
		}
	}
}

func TestOfficialRemoteAccessFollowRejectsBadSnapshots(t *testing.T) {
	oldInterval := officialStreamPollInterval
	officialStreamPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { officialStreamPollInterval = oldInterval })

	cases := []struct {
		name string
		snap OfficialRemoteAccessSnapshot
	}{
		{"owner 带 grants", OfficialRemoteAccessSnapshot{Owner: true, Grants: []OfficialRemoteAccessGrant{
			{Resource: OfficialRemoteAccessResource{Kind: "global"}},
		}}},
		{"未知 resource kind", OfficialRemoteAccessSnapshot{Grants: []OfficialRemoteAccessGrant{
			{Resource: OfficialRemoteAccessResource{Kind: "team", ID: "t"}},
		}}},
		{"session 缺 id", OfficialRemoteAccessSnapshot{Grants: []OfficialRemoteAccessGrant{
			{Resource: OfficialRemoteAccessResource{Kind: "session"}},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := &fakeRemoteAccessSource{}
			source.set(tc.snap)
			srv, err := NewServer(Options{})
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			srv.AttachOfficialRemoteAccessAPI(source.api())
			defer srv.DetachOfficialRemoteAccessAPI()
			_, fault := srv.OpenOfficialRemoteAccessFollowStream(context.Background())
			if fault == nil || fault.Status != http.StatusInternalServerError {
				t.Fatalf("坏快照必须响亮失败: %#v", fault)
			}
		})
	}
}

func TestOfficialRemoteAccessFollowHTTPOwnerAndParams(t *testing.T) {
	oldInterval := officialStreamPollInterval
	officialStreamPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { officialStreamPollInterval = oldInterval })

	source := &fakeRemoteAccessSource{}
	source.set(OfficialRemoteAccessSnapshot{Owner: true})
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialRemoteAccessAPI(source.api())
	t.Cleanup(srv.DetachOfficialRemoteAccessAPI)
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeOfficialRemoteAccessFollowHTTP))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{"peerId":"forged"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bad params: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("参数错状态 = %d", resp.StatusCode)
	}

	resp, err = http.Post(ts.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("follow: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 = %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	if !sc.Scan() {
		t.Fatal("没有 baseline 行")
	}
	var frame map[string]any
	if err := json.Unmarshal(sc.Bytes(), &frame); err != nil || frame["owner"] != true {
		t.Fatalf("baseline = %q", sc.Text())
	}
	// 关闭连接 = 客户端取消：服务端不得崩、不得继续发帧。
	_ = resp.Body.Close()
	time.Sleep(20 * time.Millisecond)
}

func TestOfficialRemoteAccessFollowWithoutAPIIs501(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	_, fault := srv.OpenOfficialRemoteAccessFollowStream(context.Background())
	if fault == nil || fault.Status != http.StatusNotImplemented {
		t.Fatalf("没有 API 时必须 501: %#v", fault)
	}
	if !strings.Contains(fault.Message, "remoteAccess.follow") {
		t.Fatalf("501 要点名方法: %q", fault.Message)
	}
}
