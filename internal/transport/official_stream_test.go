package transport

// 官方 6 个流式方法（`mode:'stream'`）的真实现测试。
//
// 每条断言都对着官方 zod 契约里的**帧形状**（typert.host.js 的 jsonSchema），
// 不是"有个字段就行"：
//
//	session.follow              snapshot → event（cursor 单调、hasMore 正确、不重放）
//	session.observeEvent        ready → event（从订阅时刻起，不重放）
//	session.observeProjections  ready → projection{seq,changes}
//	session.observeState        完整状态替换（变化才发）
//	workspaceFiles.changes      {kind:"ready"} → {kind:"change"}
//	toolResults.watch           {available:false} | {available:true,value}
//
// # 为什么用 NDJSON 捕获器而不是 httptest.ResponseRecorder
//
// 流是**长连接**：handler 在别的 goroutine 里写，测试要在另一端等帧。
// httptest.ResponseRecorder 的 Body 是裸 bytes.Buffer，跨 goroutine 读写就是
// data race（-race 下必炸）。所以这里自带一把锁的捕获器。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

// streamRecorder 是并发安全的 NDJSON 捕获器。
type streamRecorder struct {
	mu     sync.Mutex
	header http.Header
	code   int
	buf    []byte
	done   chan struct{}
	once   sync.Once
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{header: http.Header{}, done: make(chan struct{})}
}

func (r *streamRecorder) Header() http.Header { return r.header }

func (r *streamRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = code
	}
}

func (r *streamRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	return len(p), nil
}

func (r *streamRecorder) Flush() {}

// close 标记 handler 返回。
func (r *streamRecorder) close() { r.once.Do(func() { close(r.done) }) }

// status 返回 handler 写下的状态码；**0 = 还没写**（流仍在打开中）。
func (r *streamRecorder) status() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}

// waitStatus 等到 handler 写下状态码（或超时），返回它。
func (r *streamRecorder) waitStatus(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if code := r.status(); code != 0 {
			return code
		}
		if time.Now().After(deadline) {
			t.Fatalf("等状态码超时（handler 没有结束）body=%q", rawBody(r))
		}
		time.Sleep(time.Millisecond)
	}
}

// lines 返回当前所有**完整**行（最后一行没有换行就不返回）。
func (r *streamRecorder) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw := string(r.buf)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	} else {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// frames 解码所有完整行。
func (r *streamRecorder) frames() []map[string]any {
	out := []map[string]any{}
	for _, line := range r.lines() {
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			continue
		}
		out = append(out, frame)
	}
	return out
}

// waitFrames 等到至少 n 帧（或超时），返回实际拿到的帧。
func (r *streamRecorder) waitFrames(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		frames := r.frames()
		if len(frames) >= n {
			return frames
		}
		if time.Now().After(deadline) {
			t.Fatalf("等 %d 帧超时，只拿到 %d 帧：%v", n, len(frames), frames)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// mutableOfficialSource 是**可变**的假宿主：测试中途追加事件，模拟真实写入。
type mutableOfficialSource struct {
	mu     sync.Mutex
	events []DurableEvent
	live   OfficialLiveState
	header *OfficialSessionHeader
	// tail 为 true 时实现 OfficialSessionTailSource（走增量读路径）。
	tail bool
	// wake 在追加时关闭并换新（模拟 journal 广播）。
	wake chan struct{}
}

func newMutableOfficialSource(events []DurableEvent) *mutableOfficialSource {
	src := &mutableOfficialSource{events: append([]DurableEvent(nil), events...),
		wake: make(chan struct{})}
	header := OfficialSessionHeader{Version: 4, ID: "s1", CreatedAt: 1000}
	src.header = &header
	return src
}

func (m *mutableOfficialSource) Messages(string) ([]Message, error) { return nil, nil }

func (m *mutableOfficialSource) logEndLocked() int64 {
	if len(m.events) == 0 {
		return -1
	}
	return m.events[len(m.events)-1].Seq
}

func (m *mutableOfficialSource) DurableEvents(string) ([]DurableEvent, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]DurableEvent(nil), m.events...), m.logEndLocked(), nil
}

// DurableEventsSince 只在 tail 为 true 时**通过接口暴露**（见 tailSource 包装）。
func (m *mutableOfficialSource) DurableEventsSince(sessionID string, afterSeq int64) ([]DurableEvent, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return officialEventsAfter(append([]DurableEvent(nil), m.events...), afterSeq), m.logEndLocked(), nil
}

func (m *mutableOfficialSource) LiveState(string) (OfficialLiveState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.live, nil
}

func (m *mutableOfficialSource) Search(string, int) ([]OfficialSearchItem, bool, error) {
	return nil, false, nil
}

func (m *mutableOfficialSource) RemoveQueueItem(string, string) error { return nil }

func (m *mutableOfficialSource) SessionHeader(string) (OfficialSessionHeader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.header == nil {
		return OfficialSessionHeader{}, nil
	}
	return *m.header, nil
}

// append 追加一条事件并唤醒等待者。
func (m *mutableOfficialSource) append(ev DurableEvent) {
	m.mu.Lock()
	m.events = append(m.events, ev)
	old := m.wake
	m.wake = make(chan struct{})
	m.mu.Unlock()
	close(old)
}

func (m *mutableOfficialSource) setLive(live OfficialLiveState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.live = live
}

// runStream 在后台跑一个流 handler，返回捕获器与取消函数。
func runStream(t *testing.T, handler officialHandler, body string) (*streamRecorder, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/official", strings.NewReader(body)).WithContext(ctx)
	rec := newStreamRecorder()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer rec.close()
		handler(rec, req)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Errorf("流 handler 在取消后没有退出（goroutine 泄漏）")
		}
	})
	return rec, cancel
}

// officialStreamTestServer 起一个挂了可变宿主的 Server。
func officialStreamTestServer(t *testing.T, src *mutableOfficialSource) *Server {
	t.Helper()
	return newTestServer(t, Options{Source: src, Journals: NewJournals(nil)})
}

func rawJSON(t *testing.T, value any) DurableEvent {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化事件: %v", err)
	}
	return DurableEvent{Data: raw}
}

func eventAt(seq int64, typ, data string) DurableEvent {
	return DurableEvent{Type: typ, Seq: seq, Time: 1000 + seq, Data: json.RawMessage(data)}
}

// ---------------------------------------------------------------------------
// session.follow
// ---------------------------------------------------------------------------

// TestOfficialStreamFollowSnapshotThenLiveEvents 钉住官方 follow 的
// snapshot → event 形状，以及 cursor 单调。
func TestOfficialStreamFollowSnapshotThenLiveEvents(t *testing.T) {
	src := newMutableOfficialSource(officialFixtureEvents())
	s := officialStreamTestServer(t, src)

	rec, _ := runStream(t, s.handleSessionFollow, `{"address":{"kind":"session","sessionId":"s1"}}`)
	frames := rec.waitFrames(t, 1)

	if rec.status() != http.StatusOK {
		t.Fatalf("流方法必须 200（不是 501）：%d", rec.status())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("Content-Type 应为 NDJSON 长连接：%q", got)
	}
	snapshot := frames[0]
	if snapshot["type"] != "snapshot" {
		t.Fatalf("首帧必须是 snapshot：%v", snapshot)
	}
	if snapshot["cursor"] != float64(6) {
		t.Fatalf("cursor 必须是日志末端 v4 seq=6：%v", snapshot["cursor"])
	}
	if snapshot["hasMore"] != false {
		t.Fatalf("整段在一页里，hasMore 必须 false：%v", snapshot["hasMore"])
	}
	records, _ := snapshot["records"].([]any)
	if len(records) != 7 {
		t.Fatalf("records 必须是 7 条 durable 事件：%v", snapshot["records"])
	}
	// header 是官方必填三键 + 本仓可证的真值。
	header, ok := snapshot["header"].(map[string]any)
	if !ok || header["id"] != "s1" || header["version"] != float64(4) ||
		header["createdAt"] != float64(1000) || header["isSeeded"] != false {
		t.Fatalf("snapshot.header 形状不对：%v", snapshot["header"])
	}
	// projections 与 session.projections 同形。
	projections, ok := snapshot["projections"].(map[string]any)
	if !ok || projections["asOfSeq"] != float64(6) {
		t.Fatalf("snapshot.projections 形状不对：%v", snapshot["projections"])
	}

	// 真跟随：追加一条，必须推下来（且 seq 单调）。
	src.append(eventAt(7, "assistant/message", `{"content":[{"type":"text","text":"live"}]}`))
	frames = rec.waitFrames(t, 2)
	event, ok := frames[1]["event"].(map[string]any)
	if !ok || frames[1]["type"] != "event" {
		t.Fatalf("第二帧必须是 event 帧：%v", frames[1])
	}
	if event["seq"] != float64(7) {
		t.Fatalf("实时帧 seq 应为 7：%v", event["seq"])
	}
	if event["type"] != "assistant/message" {
		t.Fatalf("实时帧 type 应原样来自 durable 事件：%v", event["type"])
	}
}

// TestOfficialStreamFollowResumeIncludesSeqZero 是那个已知坑的回归：
// `sinceSeq = -1` 必须**包含 seq 0**（写 0 表示"第 0 帧我有了"）。
func TestOfficialStreamFollowResumeIncludesSeqZero(t *testing.T) {
	src := newMutableOfficialSource(officialFixtureEvents())
	s := officialStreamTestServer(t, src)

	rec, _ := runStream(t, s.handleSessionFollow,
		`{"address":{"kind":"session","sessionId":"s1"},"sinceSeq":-1}`)
	frames := rec.waitFrames(t, 2)

	snapshot := frames[0]
	if snapshot["type"] != "snapshot" {
		t.Fatalf("首帧必须是 snapshot：%v", snapshot)
	}
	if snapshot["cursor"] != float64(-1) {
		t.Fatalf("sinceSeq=-1（我什么都没拿）时 cursor 必须是 -1：%v", snapshot["cursor"])
	}
	if records, _ := snapshot["records"].([]any); len(records) != 0 {
		t.Fatalf("续传不许重放历史，records 必须为空：%v", snapshot["records"])
	}
	// 关键：第一条实时帧必须是 **seq 0**。
	event, _ := frames[1]["event"].(map[string]any)
	if event["seq"] != float64(0) {
		t.Fatalf("sinceSeq=-1 必须包含 seq 0（这就是「seq 0 永远取不到」那个坑）：%v", frames[1])
	}

	// 对照：写 0 表示"我已有第 0 帧"，所以第一条是 seq 1。
	rec2, _ := runStream(t, s.handleSessionFollow,
		`{"address":{"kind":"session","sessionId":"s1"},"sinceSeq":0}`)
	frames2 := rec2.waitFrames(t, 2)
	event2, _ := frames2[1]["event"].(map[string]any)
	if event2["seq"] != float64(1) {
		t.Fatalf("sinceSeq=0 是排他下界，第一条应是 seq 1：%v", frames2[1])
	}
}

// TestOfficialStreamFollowResumeSkipsHistory 钉住"断线重连不重放历史"：
// sinceSeq=N 之后只交付 seq > N。
func TestOfficialStreamFollowResumeSkipsHistory(t *testing.T) {
	src := newMutableOfficialSource(officialFixtureEvents())
	s := officialStreamTestServer(t, src)

	rec, _ := runStream(t, s.handleSessionFollow,
		`{"address":{"kind":"session","sessionId":"s1"},"sinceSeq":4}`)
	frames := rec.waitFrames(t, 2)
	if frames[0]["cursor"] != float64(4) {
		t.Fatalf("cursor 必须是调用方钉住的切点 4：%v", frames[0]["cursor"])
	}
	if records, _ := frames[0]["records"].([]any); len(records) != 0 {
		t.Fatalf("续传必须 records=[]（不重放）：%v", frames[0]["records"])
	}
	event, _ := frames[1]["event"].(map[string]any)
	if event["seq"] != float64(5) {
		t.Fatalf("sinceSeq=4 之后第一条应是 seq 5：%v", frames[1])
	}
}

// TestOfficialStreamFollowRejectsInconsistentCursor 钉住自相矛盾的续传参数不被猜。
func TestOfficialStreamFollowRejectsInconsistentCursor(t *testing.T) {
	src := newMutableOfficialSource(officialFixtureEvents())
	s := officialStreamTestServer(t, src)

	cases := []struct{ name, body string }{
		{"sinceSeq 与 throughSeq 矛盾",
			`{"address":{"kind":"session","sessionId":"s1"},"sinceSeq":3,"throughSeq":5}`},
		{"sinceSeq 与 beforeSeq 同时给",
			`{"address":{"kind":"session","sessionId":"s1"},"sinceSeq":3,"beforeSeq":2}`},
		{"sinceSeq 小于 -1",
			`{"address":{"kind":"session","sessionId":"s1"},"sinceSeq":-2}`},
		{"throughSeq 超过日志末端",
			`{"address":{"kind":"session","sessionId":"s1"},"throughSeq":99}`},
		{"未知字段",
			`{"address":{"kind":"session","sessionId":"s1"},"sinceeq":1}`},
		{"subagent 地址",
			`{"address":{"kind":"subagent","parentSessionId":"p","childSessionId":"c","mode":"one-shot"}}`},
	}
	for _, tc := range cases {
		rec, _ := runStream(t, s.handleSessionFollow, tc.body)
		code := rec.waitStatus(t)
		if code != http.StatusBadRequest && code != http.StatusNotImplemented {
			t.Errorf("%s：状态应为 400/501，实际 %d body=%q", tc.name, code, rawBody(rec))
			continue
		}
		// 参数错误必须在**写帧之前**（普通四字段 JSON），不是 NDJSON 里的一行。
		if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "application/x-ndjson") {
			t.Errorf("%s：参数错误不该开长连接：%q", tc.name, got)
		}
	}
}

func rawBody(rec *streamRecorder) string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return string(rec.buf)
}

// ---------------------------------------------------------------------------
// session.observeEvent / observeProjections / observeState
// ---------------------------------------------------------------------------

func TestOfficialStreamObserveEventStartsAtReadyWithoutReplay(t *testing.T) {
	src := newMutableOfficialSource(officialFixtureEvents())
	s := officialStreamTestServer(t, src)

	rec, _ := runStream(t, s.handleSessionObserveEvent, `{"sessionId":"s1"}`)
	frames := rec.waitFrames(t, 1)
	if frames[0]["type"] != "ready" {
		t.Fatalf("首帧必须是 ready：%v", frames[0])
	}
	if len(frames) != 1 {
		t.Fatalf("ready 之后不许重放历史（observeEvent 不是 follow）：%v", frames[1:])
	}

	src.append(eventAt(7, "user/message", `{"content":[{"type":"text","text":"hi"}],"source":{"kind":"user"}}`))
	frames = rec.waitFrames(t, 2)
	event, _ := frames[1]["event"].(map[string]any)
	if frames[1]["type"] != "event" || event["seq"] != float64(7) {
		t.Fatalf("ready 之后只发新事件：%v", frames[1])
	}
}

func TestOfficialStreamObserveProjectionsEmitsChangedKeys(t *testing.T) {
	src := newMutableOfficialSource(officialFixtureEvents())
	s := officialStreamTestServer(t, src)

	rec, _ := runStream(t, s.handleSessionObserveProjections, `{"sessionId":"s1"}`)
	frames := rec.waitFrames(t, 1)
	if frames[0]["type"] != "ready" {
		t.Fatalf("首帧必须是 ready：%v", frames[0])
	}

	// 改标题 → changes 只有 title。
	src.append(eventAt(7, "session/title", `{"title":"新标题"}`))
	frames = rec.waitFrames(t, 2)
	if frames[1]["type"] != "projection" || frames[1]["seq"] != float64(7) {
		t.Fatalf("必须是 projection 帧且带 committed seq：%v", frames[1])
	}
	changes, _ := frames[1]["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("改了一个键就只列一个：%v", frames[1]["changes"])
	}
	change, _ := changes[0].(map[string]any)
	if change["key"] != "title" || change["val"] != "新标题" {
		t.Fatalf("改动集不对：%v", change)
	}

	// 不改任何客户端可见键的事件 → changes 是**空数组**（不是缺字段、不是"没有能力"）。
	src.append(eventAt(8, "tool/result", `{"callId":"c1","text":"out"}`))
	frames = rec.waitFrames(t, 3)
	if frames[2]["type"] != "projection" || frames[2]["seq"] != float64(8) {
		t.Fatalf("每条 committed 事件一帧：%v", frames[2])
	}
	changes, ok := frames[2]["changes"].([]any)
	if !ok || len(changes) != 0 {
		t.Fatalf("没改任何键时 changes 必须是空数组：%v", frames[2]["changes"])
	}
}

func TestOfficialStreamObserveStateEmitsReplacements(t *testing.T) {
	src := newMutableOfficialSource(officialFixtureEvents())
	s := officialStreamTestServer(t, src)

	rec, _ := runStream(t, s.handleSessionObserveState, `{"sessionId":"s1"}`)
	frames := rec.waitFrames(t, 1)
	// 官方 observeState 的 item 就是 SessionState 本身（没有 type 判别式）。
	if frames[0]["cursor"] != float64(6) {
		t.Fatalf("首帧必须是当前状态（cursor=日志末端）：%v", frames[0])
	}
	if frames[0]["running"] != false || frames[0]["active"] != false {
		t.Fatalf("冷会话状态应为 false：%v", frames[0])
	}

	// 状态没变 → 不许刷替换帧（这是"完整替换"不是心跳）。
	time.Sleep(150 * time.Millisecond)
	if got := len(rec.frames()); got != 1 {
		t.Fatalf("状态没变时不该有第二帧，实际 %d 帧：%v", got, rec.frames())
	}

	// running 翻转 → 一帧完整状态。
	src.setLive(OfficialLiveState{Running: true, Active: true})
	frames = rec.waitFrames(t, 2)
	if frames[1]["running"] != true || frames[1]["active"] != true {
		t.Fatalf("状态变化必须发完整替换：%v", frames[1])
	}
}

// ---------------------------------------------------------------------------
// workspaceFiles.changes
// ---------------------------------------------------------------------------

// fakeWatchFiles 是可控的 FilesStat（版本令牌 + 消失）。
type fakeWatchFiles struct {
	mu      sync.Mutex
	version string
	absent  bool
	calls   int
}

func (f *fakeWatchFiles) stat(string, string) (OfficialWorkspaceFileStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.absent {
		return OfficialWorkspaceFileStat{}, OfficialWorkspaceError("workspace-file/not-found",
			"文件不存在：a.txt")
	}
	return OfficialWorkspaceFileStat{AbsolutePath: "/work/a.txt", Version: f.version}, nil
}

func (f *fakeWatchFiles) set(version string, absent bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version, f.absent = version, absent
}

func TestOfficialStreamWorkspaceFileChanges(t *testing.T) {
	files := &fakeWatchFiles{version: "v1"}
	srv := officialWorkspaceTestServer(t, OfficialWorkspaceAPI{FilesStat: files.stat},
		OfficialDirectoryPickerAPI{})

	rec, _ := runStream(t, func(w http.ResponseWriter, r *http.Request) {
		srv.serveOfficialWorkspaceAPI(w, r, "workspaceFiles.changes")
	}, `{"workspaceFileScopeId":"s1","path":"a.txt"}`)

	frames := rec.waitFrames(t, 1)
	if frames[0]["kind"] != "ready" {
		t.Fatalf("首帧必须是 ready（watch 已生效）：%v", frames[0])
	}

	// 版本变了 → 一帧 change（带绝对路径 + 新版本令牌）。
	files.set("v2", false)
	frames = rec.waitFrames(t, 2)
	if frames[1]["kind"] != "change" {
		t.Fatalf("版本变化必须发 change：%v", frames[1])
	}
	change, _ := frames[1]["change"].(map[string]any)
	if change["absolutePath"] != "/work/a.txt" || change["version"] != "v2" {
		t.Fatalf("change 帧形状不对：%v", change)
	}
	if _, has := change["absent"]; has {
		t.Fatalf("存在时不许带 absent：%v", change)
	}

	// 目标消失 → absent 帧（路径身份不变）。
	files.set("v2", true)
	frames = rec.waitFrames(t, 3)
	change, _ = frames[2]["change"].(map[string]any)
	if change["absent"] != true || change["absolutePath"] != "/work/a.txt" {
		t.Fatalf("消失必须发 absent 帧：%v", frames[2])
	}
}

// TestOfficialStreamWorkspaceDirectoryIsSupportedByHostStat 钉住"目录目标"的真实分支：
//
// 宿主 `FilesStat` 对目录返回 `{absolutePath, version}` 并置 `IsDirectory`
// （`cmd/dshd/serve_workspace_api.go` 的 filesStat），目录帧因此走的是与文件帧
// 同一套 `ready + change{absolutePath, version}` 形状。这里用一台假 FilesStat
// 证明 transport 层**不**拦截目录目标，也不再回 501。
func TestOfficialStreamWorkspaceDirectoryIsSupportedByHostStat(t *testing.T) {
	var mu sync.Mutex
	version := "d1"
	dirStat := func(string, string) (OfficialWorkspaceFileStat, error) {
		mu.Lock()
		defer mu.Unlock()
		return OfficialWorkspaceFileStat{AbsolutePath: "/work/dir", Version: version, IsDirectory: true}, nil
	}
	srv := officialWorkspaceTestServer(t, OfficialWorkspaceAPI{FilesStat: dirStat}, OfficialDirectoryPickerAPI{})

	rec, _ := runStream(t, func(w http.ResponseWriter, r *http.Request) {
		srv.serveOfficialWorkspaceAPI(w, r, "workspaceFiles.changes")
	}, `{"workspaceFileScopeId":"s1","path":"dir"}`)

	frames := rec.waitFrames(t, 1)
	if frames[0]["kind"] != "ready" {
		t.Fatalf("目录目标的流第一帧必须是 ready（不是 501、也不是假数据）：%v", frames[0])
	}
	mu.Lock()
	version = "d2"
	mu.Unlock()
	frames = rec.waitFrames(t, 2)
	change, _ := frames[1]["change"].(map[string]any)
	if change["absolutePath"] != "/work/dir" || change["version"] != "d2" {
		t.Fatalf("目录帧必须带绝对路径与目录版本令牌：%v", frames[1])
	}
}

// TestOfficialStreamWorkspaceNonWatchableTargetIsBadRequest 真正不能 watch 的目标
// （symlink / 设备节点）回官方码 `workspace-file/watch-unsupported` + **400**：
// 那是"这个目标不行"，不是"宿主没这个能力"，所以不是 501。
func TestOfficialStreamWorkspaceNonWatchableTargetIsBadRequest(t *testing.T) {
	srv := officialWorkspaceTestServer(t, OfficialWorkspaceAPI{
		FilesStat: func(string, string) (OfficialWorkspaceFileStat, error) {
			return OfficialWorkspaceFileStat{}, OfficialWorkspaceError("workspace-file/not-regular-file",
				"是符号链接，不是普通文件")
		},
	}, OfficialDirectoryPickerAPI{})

	rec, _ := runStream(t, func(w http.ResponseWriter, r *http.Request) {
		srv.serveOfficialWorkspaceAPI(w, r, "workspaceFiles.changes")
	}, `{"workspaceFileScopeId":"s1","path":"link"}`)

	if code := rec.waitStatus(t); code != http.StatusBadRequest {
		t.Fatalf("不可 watch 的目标应 400（官方 watch-unsupported 是输入问题），实际 %d：%s",
			code, rawBody(rec))
	}
	if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("被拒的目标不该开长连接：%q", got)
	}
}

// ---------------------------------------------------------------------------
// toolResults.watch
// ---------------------------------------------------------------------------

func TestOfficialStreamToolResultsWatch(t *testing.T) {
	var mu sync.Mutex
	available := false
	var value json.RawMessage
	srv := officialTestServer(t, OfficialAgentAPI{
		ToolResultsWatch: func(sessionID string, seq int64) (OfficialToolResultWatch, error) {
			if sessionID != "s1" || seq != 7 {
				t.Errorf("watch 收到 %q %d", sessionID, seq)
			}
			mu.Lock()
			defer mu.Unlock()
			return OfficialToolResultWatch{Available: available, Value: value}, nil
		},
	})

	rec, _ := runStream(t, func(w http.ResponseWriter, r *http.Request) {
		srv.serveOfficialAgentAPI(w, r, "toolResults.watch")
	}, `{"sessionId":"s1","seq":7}`)

	frames := rec.waitFrames(t, 1)
	if frames[0]["available"] != false {
		t.Fatalf("冷读必须 available:false：%v", frames[0])
	}
	if _, has := frames[0]["value"]; has {
		t.Fatalf("available:false 时不许带 value：%v", frames[0])
	}

	// 结果落盘 → 一帧 available:true + value（原样字节）。
	mu.Lock()
	available = true
	value = json.RawMessage(`{"callId":"call-1","text":"hi"}`)
	mu.Unlock()
	frames = rec.waitFrames(t, 2)
	if frames[1]["available"] != true {
		t.Fatalf("结果出现后必须 available:true：%v", frames[1])
	}
	got, _ := frames[1]["value"].(map[string]any)
	if got["callId"] != "call-1" || got["text"] != "hi" {
		t.Fatalf("value 必须原样来自 durable tool/result：%v", frames[1]["value"])
	}
}

// TestOfficialStreamMissingCapabilityIsNotAFakeReady 钉住"没有来源就明确未实现"：
// 绝不发一帧 ready 冒充订阅成功。
func TestOfficialStreamMissingCapabilityIsNotAFakeReady(t *testing.T) {
	// 1) 宿主没有 header 来源 → follow 501（不能编 createdAt）。
	noHeader := &noHeaderSource{fakeOfficialSource: fakeOfficialSource{events: officialFixtureEvents(), logEnd: 6}}
	s := newTestServer(t, Options{Source: noHeader, Journals: NewJournals(nil)})
	rec, _ := runStream(t, s.handleSessionFollow, `{"address":{"kind":"session","sessionId":"s1"}}`)
	if code := rec.waitStatus(t); code != http.StatusNotImplemented {
		t.Fatalf("没有 header 来源必须 501：%d body=%s", code, rawBody(rec))
	}
	if !strings.Contains(rawBody(rec), "OfficialSessionHeaderSource") {
		t.Fatalf("501 必须点名缺哪个能力：%s", rawBody(rec))
	}

	// 2) 宿主没有 toolResults 来源 → 501。
	bare := officialTestServer(t, OfficialAgentAPI{})
	rec2, _ := runStream(t, func(w http.ResponseWriter, r *http.Request) {
		bare.serveOfficialAgentAPI(w, r, "toolResults.watch")
	}, `{"sessionId":"s1","seq":7}`)
	if code := rec2.waitStatus(t); code != http.StatusNotImplemented {
		t.Fatalf("没有工具结果来源必须 501：%d body=%s", code, rawBody(rec2))
	}
	if !strings.Contains(rawBody(rec2), "ToolResultsWatch") {
		t.Fatalf("501 必须点名缺哪个能力：%s", rawBody(rec2))
	}
}

// noHeaderSource 刻意**不**实现 OfficialSessionHeaderSource。
type noHeaderSource struct {
	fakeOfficialSource
}

// ---------------------------------------------------------------------------
// A/B 隔离与背压
// ---------------------------------------------------------------------------

// TestOfficialStreamSessionsAreIsolated 钉住 A/B 会话不串帧。
func TestOfficialStreamSessionsAreIsolated(t *testing.T) {
	eventsA := []DurableEvent{eventAt(0, "user/message", `{"content":[],"source":{"kind":"user"}}`)}
	eventsB := []DurableEvent{
		eventAt(0, "user/message", `{"content":[],"source":{"kind":"user"}}`),
		eventAt(1, "assistant/message", `{"content":[]}`),
	}
	srcA := newMutableOfficialSource(eventsA)
	srcB := newMutableOfficialSource(eventsB)
	srvA := officialStreamTestServer(t, srcA)
	srvB := officialStreamTestServer(t, srcB)

	recA, _ := runStream(t, srvA.handleSessionFollow, `{"address":{"kind":"session","sessionId":"a"}}`)
	recB, _ := runStream(t, srvB.handleSessionFollow, `{"address":{"kind":"session","sessionId":"b"}}`)

	framesA := recA.waitFrames(t, 1)
	framesB := recB.waitFrames(t, 1)
	if framesA[0]["cursor"] != float64(0) {
		t.Fatalf("A 的 cursor 应来自 A 的日志：%v", framesA[0]["cursor"])
	}
	if framesB[0]["cursor"] != float64(1) {
		t.Fatalf("B 的 cursor 应来自 B 的日志：%v", framesB[0]["cursor"])
	}

	// 只往 A 追加：B 不许收到。
	srcA.append(eventAt(1, "assistant/message", `{"content":[]}`))
	recA.waitFrames(t, 2)
	time.Sleep(150 * time.Millisecond)
	if got := len(recB.frames()); got != 1 {
		t.Fatalf("B 收到了 A 的帧（隔离破了）：%v", recB.frames())
	}
}

// TestOfficialStreamCancelStopsPull 钉住拉模式：取消就退出，没有新数据就不产帧。
func TestOfficialStreamCancelStopsPull(t *testing.T) {
	src := newMutableOfficialSource(officialFixtureEvents())
	s := officialStreamTestServer(t, src)

	rec, cancel := runStream(t, s.handleSessionFollow, `{"address":{"kind":"session","sessionId":"s1"}}`)
	rec.waitFrames(t, 1)
	// 没有新事件时不产帧（不是忙等刷空帧）。
	time.Sleep(150 * time.Millisecond)
	if got := len(rec.frames()); got != 1 {
		t.Fatalf("没有新事件时不许产帧，实际 %d 帧", got)
	}
	cancel() // t.Cleanup 会断言 goroutine 真的退出
}

// ---------------------------------------------------------------------------
// journal 的 -1 哨兵（seq 0 取不到的根因）
// ---------------------------------------------------------------------------

func TestJournalWindowSinceNothingSentinel(t *testing.T) {
	j := NewJournal(1, 0)
	for i := 0; i < 3; i++ {
		j.Append("user/message", json.RawMessage(`{}`), 1000)
	}

	// -1 = 我什么都没拿：要全部，而且**算补得回**（内存里 1..3 都在）。
	w := j.Window(SinceNothing, 0)
	if len(w.Frames) != 3 {
		t.Fatalf("SinceNothing 必须返回全部帧：%d", len(w.Frames))
	}
	if !w.Covered {
		t.Fatal("SinceNothing 在 1..N 全在内存时必须 Covered=true（以前一律判成假缺口）")
	}

	// 挤掉最老的之后，-1 就真的补不回来了。
	small := NewJournal(1, 2)
	for i := 0; i < 3; i++ {
		small.Append("user/message", json.RawMessage(`{}`), 1000)
	}
	if small.Window(SinceNothing, 0).Covered {
		t.Fatal("最老的帧被挤掉之后 SinceNothing 必须 Covered=false")
	}

	// 排他语义：sinceSeq=0 表示"第 0 帧我有"，只给 > 0。
	if got := j.Window(0, 0); len(got.Frames) != 3 {
		t.Fatalf("journal 从 1 开始，sinceSeq=0 应给 1..3：%d", len(got.Frames))
	}
	if got := j.Window(2, 0); len(got.Frames) != 1 || got.Frames[0].Seq != 3 {
		t.Fatalf("sinceSeq=2 应只给 seq 3：%+v", got.Frames)
	}
}

// TestOfficialEventsAfterIncludesSeqZero 钉住 v4 事件域的同一个坑。
func TestOfficialEventsAfterIncludesSeqZero(t *testing.T) {
	events := officialFixtureEvents() // seq 0..6
	if got := officialEventsAfter(events, SinceNothing); len(got) != len(events) {
		t.Fatalf("-1 必须包含 seq 0：拿到 %d 条，应有 %d 条", len(got), len(events))
	}
	if got := officialEventsAfter(events, 0); len(got) != len(events)-1 || got[0].Seq != 1 {
		t.Fatalf("0 是排他下界：%+v", got)
	}
}

// TestJournalWaitWakesOnAppend 钉住"推送而不是轮询"。
func TestJournalWaitWakesOnAppend(t *testing.T) {
	j := NewJournal(1, 0)
	wait := j.Wait()
	select {
	case <-wait:
		t.Fatal("还没有追加就被唤醒了")
	case <-time.After(20 * time.Millisecond):
	}
	j.Append("user/message", json.RawMessage(`{}`), 1000)
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("追加之后等待者没被唤醒")
	}
}

// ---------------------------------------------------------------------------
// rc.2：team/mode 与事件准入（R1-G01）
// ---------------------------------------------------------------------------

// rc.2 新增 `team/mode`（`dsh-session/lib/types/known-event-types.js:68`），
// 载荷 `{enabled:boolean}`（`dsh-agent-team/lib/types/projection.js:52`）。
// WS 翻译路径必须**把它翻成一个键**，而不是"没有改动的空数组"——
// 空数组在官方语义里等于"这条事件没有客户端可见效果"，也就是把 team 开关静默丢了。
func TestOfficialStreamTeamModeEmitsProjectionKey(t *testing.T) {
	src := newMutableOfficialSource(officialFixtureEvents())
	s := officialStreamTestServer(t, src)

	rec, _ := runStream(t, s.handleSessionObserveProjections, `{"sessionId":"s1"}`)
	if frames := rec.waitFrames(t, 1); frames[0]["type"] != "ready" {
		t.Fatalf("首帧必须是 ready：%v", frames[0])
	}

	src.append(eventAt(7, "team/mode", `{"enabled":true}`))
	frames := rec.waitFrames(t, 2)
	if frames[1]["type"] != "projection" || frames[1]["seq"] != float64(7) {
		t.Fatalf("team/mode 必须产出一帧 projection：%v", frames[1])
	}
	changes, _ := frames[1]["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("team/mode 必须改一个键（静默空数组 = 吞事件）：%v", frames[1]["changes"])
	}
	change, _ := changes[0].(map[string]any)
	if change["key"] != "teamMode" {
		t.Fatalf("team/mode 的投影键 = %v，want teamMode（宿主自有键；官方把它折进 agent-team 插件的 agentTeam，本宿主没有那个投影）", change["key"])
	}
	val, ok := change["val"].(map[string]any)
	if !ok || val["enabled"] != true {
		t.Fatalf("投影值必须是 {enabled:true}：%v", change["val"])
	}

	// 关掉：同一个键 last-wins。
	src.append(eventAt(8, "team/mode", `{"enabled":false}`))
	frames = rec.waitFrames(t, 3)
	changes, _ = frames[2]["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("关掉 team 也必须报改动：%v", frames[2]["changes"])
	}
	change, _ = changes[0].(map[string]any)
	if val, _ = change["val"].(map[string]any); val["enabled"] != false {
		t.Fatalf("关掉后投影值必须是 {enabled:false}：%v", change["val"])
	}
}

// 已有 team/mode 的会话：baseline（follow 的 projections.values）里必须带这个键，
// 否则重连后的客户端看不到当前 team 状态。
func TestOfficialStreamTeamModeInBaselineProjections(t *testing.T) {
	events := append(officialFixtureEvents(), eventAt(7, "team/mode", `{"enabled":true}`))
	src := newMutableOfficialSource(events)
	s := officialStreamTestServer(t, src)

	rec, _ := runStream(t, s.handleSessionFollow, `{"address":{"kind":"session","sessionId":"s1"}}`)
	frames := rec.waitFrames(t, 1)
	projections, ok := frames[0]["projections"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot 必须带 projections：%v", frames[0])
	}
	values, _ := projections["values"].(map[string]any)
	team, ok := values["teamMode"].(map[string]any)
	if !ok || team["enabled"] != true {
		t.Fatalf("baseline 的 teamMode 必须是 {enabled:true}：%v", values["teamMode"])
	}
}

// 读不懂的 `team/mode` 载荷必须**报错**，不能静默当成"这条没改任何键"。
func TestOfficialStreamReportsMalformedTeamMode(t *testing.T) {
	for _, data := range []string{`{"enabled":"yes"}`, `{}`, `{"enabled":true,"extra":1}`} {
		t.Run(data, func(t *testing.T) {
			src := newMutableOfficialSource(officialFixtureEvents())
			s := officialStreamTestServer(t, src)

			rec, _ := runStream(t, s.handleSessionObserveProjections, `{"sessionId":"s1"}`)
			rec.waitFrames(t, 1)
			src.append(eventAt(7, "team/mode", data))
			frames := rec.waitFrames(t, 2)
			if frames[1]["type"] != "error" {
				t.Fatalf("非法 team/mode 必须结束在 error 帧，得到：%v", frames[1])
			}
			envelope, _ := frames[1]["error"].(map[string]any)
			if msg, _ := envelope["message"].(string); !strings.Contains(msg, "team/mode") {
				t.Fatalf("错误帧必须点名 team/mode：%v", frames[1])
			}
		})
	}
}

// 未知类型**不得静默吞掉**（官方 reading path 的准入规则）。
// 非 ignorable → 流以 error 帧结束；ignorable → 照录不解释。
func TestOfficialStreamEventAdmission(t *testing.T) {
	t.Run("未知且非 ignorable 拒绝", func(t *testing.T) {
		events := append(officialFixtureEvents(), eventAt(7, "future/protocol-event", `{"x":1}`))
		src := newMutableOfficialSource(events)
		s := officialStreamTestServer(t, src)

		rec, _ := runStream(t, s.handleSessionFollow, `{"address":{"kind":"session","sessionId":"s1"}}`)
		// opening 之前的准入失败：还没开始写帧，所以是 HTTP 500 + 四字段错误体
		// （writeOfficialStreamFault 的出口），不是 NDJSON 的 error 帧。
		if status := rec.waitStatus(t); status != http.StatusInternalServerError {
			t.Fatalf("未知 required 事件必须报错（500），得到 %d：%v", status, rec.frames())
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(rawBody(rec)), &envelope); err != nil {
			t.Fatalf("错误体不是 JSON：%v", err)
		}
		errObj, _ := envelope["error"].(map[string]any)
		if msg, _ := errObj["message"].(string); !strings.Contains(msg, "future/protocol-event") {
			t.Fatalf("错误必须点名未知类型：%v", envelope)
		}
	})

	t.Run("未知但 ignorable 照录", func(t *testing.T) {
		unknown := eventAt(7, "future/protocol-event", `{"x":1}`)
		unknown.Ignorable = true
		events := append(officialFixtureEvents(), unknown)
		src := newMutableOfficialSource(events)
		s := officialStreamTestServer(t, src)

		rec, _ := runStream(t, s.handleSessionFollow, `{"address":{"kind":"session","sessionId":"s1"}}`)
		frames := rec.waitFrames(t, 1)
		if frames[0]["type"] != "snapshot" {
			t.Fatalf("ignorable 未知事件必须放行（官方保留未解释载荷），得到：%v", frames[0])
		}
		records, _ := frames[0]["records"].([]any)
		if len(records) != len(events) {
			t.Fatalf("ignorable 未知事件必须原样在 records 里：%d != %d", len(records), len(events))
		}
		last, _ := records[len(records)-1].(map[string]any)
		event, _ := last["event"].(map[string]any)
		if event["type"] != "future/protocol-event" || event["ignorable"] != true {
			t.Fatalf("未知事件必须原样交付（带 ignorable:true）：%v", last)
		}
	})
}

// 失败码**不得被折叠**：durable 事件原样交付，`turn/end.reason.error.code` 与
// `tool/result.error.code` 必须逐字到客户端（官方 rc.2 起这些码是真的机器码，
// 客户端按它分支——折成 UNKNOWN 或丢掉就等于"看不出是什么失败"）。
func TestOfficialStreamKeepsErrorCodes(t *testing.T) {
	events := append(officialFixtureEvents(),
		eventAt(7, "turn/end", `{"turn":2,"reason":{"kind":"error","error":{"name":"AttachmentError","code":"ATTACHMENT_NOT_FOUND","message":"读不到附件"}}}`),
		eventAt(8, "tool/result", `{"turn":2,"step":1,"message":{"role":"tool","isError":true,"content":[]},"error":{"name":"AttachmentError","code":"ATTACHMENT_CORRUPT"}}`),
	)
	src := newMutableOfficialSource(events)
	s := officialStreamTestServer(t, src)

	rec, _ := runStream(t, s.handleSessionFollow, `{"address":{"kind":"session","sessionId":"s1"}}`)
	frames := rec.waitFrames(t, 1)
	records, _ := frames[0]["records"].([]any)
	if len(records) != len(events) {
		t.Fatalf("records = %d，want %d", len(records), len(events))
	}
	turnEnd, _ := records[len(records)-2].(map[string]any)
	turnEndEvent, _ := turnEnd["event"].(map[string]any)
	turnEndData, _ := turnEndEvent["data"].(map[string]any)
	reason, _ := turnEndData["reason"].(map[string]any)
	turnEndErr, _ := reason["error"].(map[string]any)
	if turnEndErr["code"] != "ATTACHMENT_NOT_FOUND" {
		t.Fatalf("turn/end.reason.error.code 被折叠了：%v", reason)
	}
	toolResult, _ := records[len(records)-1].(map[string]any)
	toolEvent, _ := toolResult["event"].(map[string]any)
	toolData, _ := toolEvent["data"].(map[string]any)
	toolErr, _ := toolData["error"].(map[string]any)
	if toolErr["code"] != "ATTACHMENT_CORRUPT" {
		t.Fatalf("tool/result.error.code 被折叠了：%v", toolData["error"])
	}
}
