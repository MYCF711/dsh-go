package transport

// 官方流式方法（job.follow / terminal.attach / terminal.retain）的**真流**契约。
//
// 官方三个方法都是 typert `mode:'stream'`（只读镜像 /tmp/official-api-src）：
//
//	job-controller/lib/typert.host.js:7/13         job/follow    opened|output|status
//	terminal-controller/lib/typert.host.js:22      terminal/attach snapshot|output|state|ack（+uplink）
//	terminal-controller/lib/typert.host.js:511/199 terminal/retain retained + 保持打开的寿命流
//
// 本仓 HTTP 面与既有 6 个流方法（official_stream_face.go）**同一形状**：
//
//	200 + application/x-ndjson; charset=utf-8
//	一行 = 官方一帧（没有 result 包壳）
//	正常结束 = 连接关闭
//	中途失败 = 最后一行 {"type":"error","error":{code,message,hint,retryable}}
//
// 为什么这组测试必须存在：旧实现回的是 `{"result":{…,"oneShot":true}}` 的一次性帧。
// 它**不是**流——客户端拿到 `oneShot:true` 只能把 follow 降级成轮询、
// 把 attach 降级成快照、把 retain 当成"确认完就断"。这就是 HS-09 指出的残口：
// 语法上诚实（标了 oneShot），产品上三个官方方法全部不可用。

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ndjsonLines 把长连接响应体按行拆成帧（一行一帧），并断言 Content-Type。
func ndjsonLines(t *testing.T, resp *http.Response) []map[string]any {
	t.Helper()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("流式方法必须是 application/x-ndjson 长连接，拿到 Content-Type=%q（旧残口：一次性 result 包壳）", got)
	}
	if v := resp.Header.Get("Cache-Control"); v != "no-store" {
		t.Fatalf("长连接必须 Cache-Control: no-store，拿到 %q", v)
	}
	var frames []map[string]any
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("每一行都必须是 JSON 帧（不是 result 包壳）：%v（line=%q）", err, line)
		}
		if _, has := frame["result"]; has {
			t.Fatalf("流式方法不许再包 {result:…}（旧残口）：%q", line)
		}
		if _, has := frame["oneShot"]; has {
			t.Fatalf("流式方法不许再标 oneShot（那是旧残口）：%q", line)
		}
		frames = append(frames, frame)
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatalf("读长连接失败：%v", err)
	}
	return frames
}

// startOfficialStreamServer 起一个只挂一个流式 handler 的真 HTTP 服务。
func startOfficialStreamServer(t *testing.T, handler http.HandlerFunc, body string) (*http.Response, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(body))
	if err != nil {
		cancel()
		t.Fatalf("造请求失败：%v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatalf("请求失败：%v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, cancel
}

// ---------------------------------------------------------------------------
// job.follow：opened → output* → status，然后连接关闭
// ---------------------------------------------------------------------------

func TestOfficialJobFollowStreamsOpenedOutputStatus(t *testing.T) {
	f := newFakeOfficialJobTerminal()
	f.followScript = []any{OfficialJobFollowOutput{
		Type: "output",
		Chunks: []OfficialJobChunk{
			{At: 16, Text: "line-2\n"},
			{At: 23, Text: "line-3\n", GapBefore: true},
		},
		Next: 30,
	}}
	resp, _ := startOfficialStreamServer(t, (&OfficialJobTerminal{Svc: f}).HandleJobFollow,
		`{"sessionId":"s1","jobId":"job-7","from":16}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("流式方法打开成功要 200，拿到 %d", resp.StatusCode)
	}
	if len(f.followFrom) != 1 || f.followFrom[0] != 16 {
		t.Fatalf("from 没传到底层：%v", f.followFrom)
	}
	frames := ndjsonLines(t, resp)
	if len(frames) == 0 {
		t.Fatal("必须至少有一帧（opened）")
	}
	// 第一帧必须是 opened（官方 JobFollowFrame 的开场帧，带 from 锚点）。
	if frames[0]["type"] != "opened" {
		t.Fatalf("第一帧必须是 opened，拿到 %v", frames[0])
	}
	if _, ok := frames[0]["from"].(float64); !ok {
		t.Fatalf("opened 帧必须带 from（数字）：%v", frames[0])
	}
	if _, ok := frames[0]["job"].(map[string]any); !ok {
		t.Fatalf("opened 帧必须带 job：%v", frames[0])
	}
	// 最后一帧必须是 output（带 chunks/next），不是一次性 result。
	last := frames[len(frames)-1]
	if last["type"] != "output" {
		t.Fatalf("最后一帧必须是 output，拿到 %v", last)
	}
	chunks, ok := last["chunks"].([]any)
	if !ok || len(chunks) != 2 {
		t.Fatalf("output 帧必须带 chunks 数组（2 个），拿到 %v", last["chunks"])
	}
	first, _ := chunks[0].(map[string]any)
	if _, ok := first["at"].(float64); !ok {
		t.Fatalf("chunk 必须带绝对偏移 at：%v", first)
	}
	if _, ok := first["text"].(string); !ok {
		t.Fatalf("chunk 必须带 text：%v", first)
	}
	if _, ok := last["next"].(float64); !ok {
		t.Fatalf("output 帧必须带 next：%v", last)
	}
}

// ---------------------------------------------------------------------------
// terminal.attach：snapshot → output* → state，然后连接关闭
// ---------------------------------------------------------------------------

func TestOfficialTerminalAttachStreamsSnapshotOutputState(t *testing.T) {
	f := newFakeOfficialJobTerminal()
	f.attachScript = []any{
		OfficialTerminalOutputFrame{Type: "output", Sequence: 200, Data: "hi\r\n"},
		OfficialTerminalStateFrame{Type: "state",
			Info: OfficialWebTerminalInfo{ID: "term-a", Title: "bash", State: "exited",
				Shell: OfficialTerminalShell{Path: "x", Args: []string{}, Name: "x"}},
			Writable: false},
	}
	resp, _ := startOfficialStreamServer(t, (&OfficialJobTerminal{Svc: f}).HandleTerminalAttach,
		`{"sessionId":"s1","id":"term-a"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attach 打开成功要 200，拿到 %d", resp.StatusCode)
	}
	frames := ndjsonLines(t, resp)
	if len(frames) == 0 || frames[0]["type"] != "snapshot" {
		t.Fatalf("第一帧必须是 snapshot，拿到 %v", frames)
	}
	for _, key := range []string{"sequence", "screen", "info", "writable"} {
		if _, ok := frames[0][key]; !ok {
			t.Fatalf("snapshot 帧缺官方必填字段 %q：%v", key, frames[0])
		}
	}
	// 最后一帧必须是 state（进程退出后的终态），不是一次性快照。
	last := frames[len(frames)-1]
	if last["type"] != "state" {
		t.Fatalf("attach 流必须以 state 帧结束，拿到 %v", last)
	}
	if _, ok := last["info"].(map[string]any); !ok {
		t.Fatalf("state 帧必须带 info：%v", last)
	}
	if writable, _ := last["writable"].(bool); writable {
		t.Fatalf("已退出的终端 state 帧必须 writable=false：%v", last)
	}
}

// ---------------------------------------------------------------------------
// terminal.retain：retained 帧之后**保持打开**，直到取消
// ---------------------------------------------------------------------------

func TestOfficialTerminalRetainStreamHoldsOpenUntilCancel(t *testing.T) {
	f := newFakeOfficialJobTerminal()
	resp, cancel := startOfficialStreamServer(t, (&OfficialJobTerminal{Svc: f}).HandleTerminalRetain,
		`{"sessionId":"s1","id":"term-a"}`)
	defer cancel()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retain 打开成功要 200，拿到 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("retain 是 stream（官方 result 描述「a hold acknowledgement followed by an open lifetime stream」），拿到 Content-Type=%q", got)
	}
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("读第一帧失败：%v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &frame); err != nil {
		t.Fatalf("第一帧不是 JSON：%v（%q）", err, line)
	}
	if frame["type"] != "retained" {
		t.Fatalf("第一帧必须是 retained，拿到 %v", frame)
	}
	if _, has := frame["oneShot"]; has {
		t.Fatalf("retain 不再是一次性确认（旧残口）：%q", line)
	}

	// 关键：retained 之后流**必须还开着**（官方：open lifetime stream）。
	// 一次性实现会立刻 EOF——这里用 300ms 窗口判死。
	done := make(chan struct{})
	go func() {
		_, _ = reader.ReadString('\n')
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("retained 帧之后流立刻结束了：retain 必须保持打开（open lifetime stream），不能是一次性确认")
	case <-time.After(300 * time.Millisecond):
		// 正确：流还开着。
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消之后 retain 流必须关掉（不能泄漏一条永远挂着的连接）")
	}
}

// ---------------------------------------------------------------------------
// 未实现必须是**明确 501**，不是一次性帧冒充流
// ---------------------------------------------------------------------------

func TestOfficialStreamMethodsWithoutCapabilityAre501(t *testing.T) {
	// 只实现 unary 面的 Svc（不实现流能力）→ 三个流方法必须 501 点名缺什么。
	f := &unaryOnlyOfficialSvc{}
	cases := []struct {
		method  string
		handler http.HandlerFunc
		body    string
	}{
		{"job.follow", (&OfficialJobTerminal{Svc: f}).HandleJobFollow, `{"sessionId":"s1","jobId":"j1"}`},
		{"terminal.attach", (&OfficialJobTerminal{Svc: f}).HandleTerminalAttach, `{"sessionId":"s1","id":"t1"}`},
		{"terminal.retain", (&OfficialJobTerminal{Svc: f}).HandleTerminalRetain, `{"sessionId":"s1","id":"t1"}`},
	}
	for _, c := range cases {
		rec := officialPost(t, c.handler, c.body)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s 缺流能力必须 501，拿到 %d：%s", c.method, rec.Code, rec.Body.String())
		}
		err := errEnvelope(t, rec)
		if err["code"] != "not_implemented" {
			t.Fatalf("%s 的 code 要是 not_implemented，拿到 %v", c.method, err["code"])
		}
		if msg, _ := err["message"].(string); !strings.Contains(msg, c.method) {
			t.Fatalf("%s 的错误消息要点名方法，拿到 %q", c.method, msg)
		}
		if _, has := unwrapResultOK(t, rec.Body.Bytes()); has {
			t.Fatalf("%s 501 不许带 result（那就是假数据）", c.method)
		}
	}
}

// unaryOnlyOfficialSvc 只实现 unary 方法，**不实现**流能力。
// 用它证明"没有流能力时是明确 501，不是回一次性帧"。
type unaryOnlyOfficialSvc struct{}

func (u *unaryOnlyOfficialSvc) JobList(string) (OfficialJobListFrame, error) {
	return OfficialJobListFrame{Type: "rows"}, nil
}

func (u *unaryOnlyOfficialSvc) JobKill(context.Context, string, string) (OfficialJobKillValue, error) {
	return OfficialJobKillValue{Outcome: "requested"}, nil
}

func (u *unaryOnlyOfficialSvc) JobFollow(context.Context, string, string, int64) (OfficialJobFollowPage, error) {
	panic("handler 不许再走一次性 JobFollow（那是残口）")
}

func (u *unaryOnlyOfficialSvc) TerminalCreate(context.Context, string, OfficialTerminalCreateRequest) (OfficialWebTerminalInfo, error) {
	panic("unused")
}

func (u *unaryOnlyOfficialSvc) TerminalAttach(context.Context, string, string) (OfficialTerminalSnapshot, error) {
	panic("handler 不许再走一次性 TerminalAttach（那是残口）")
}

func (u *unaryOnlyOfficialSvc) TerminalList(string) ([]OfficialWebTerminalInfo, error) {
	return nil, nil
}

func (u *unaryOnlyOfficialSvc) TerminalClose(context.Context, string, string) error { return nil }

func (u *unaryOnlyOfficialSvc) TerminalEnvironment(context.Context, string) (OfficialTerminalEnvironment, error) {
	return OfficialTerminalEnvironment{}, nil
}

func (u *unaryOnlyOfficialSvc) TerminalShells(context.Context, string) ([]OfficialTerminalShell, error) {
	return nil, nil
}

func (u *unaryOnlyOfficialSvc) TerminalRename(context.Context, string, string, string) error {
	return nil
}

func (u *unaryOnlyOfficialSvc) TerminalRetain(context.Context, string, string) error {
	panic("handler 不许再走一次性 TerminalRetain（那是残口）")
}
