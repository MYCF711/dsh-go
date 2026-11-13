package transport

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

// fakeChannelService 是壳渠道服务的测试替身：状态由测试显式摆布。
type fakeChannelService struct {
	mu          sync.Mutex
	state       map[string]any
	setCalls    []string
	subscribers []func(map[string]any)
}

func newFakeChannelService() *fakeChannelService {
	return &fakeChannelService{state: map[string]any{
		"channel": nil, "packagedChannel": "stable", "changing": false,
	}}
}

func (f *fakeChannelService) StateView() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]any{}
	for k, v := range f.state {
		out[k] = v
	}
	return out
}

func (f *fakeChannelService) WireSet(channel string) map[string]any {
	f.mu.Lock()
	f.setCalls = append(f.setCalls, channel)
	state := map[string]any{"channel": channel, "packagedChannel": "stable", "changing": false}
	f.state = state
	subs := append([]func(map[string]any){}, f.subscribers...)
	f.mu.Unlock()
	for _, fn := range subs {
		fn(state)
	}
	return map[string]any{"kind": "applied", "state": state}
}

func (f *fakeChannelService) WireSubscribe(fn func(map[string]any)) func() {
	f.mu.Lock()
	f.subscribers = append(f.subscribers, fn)
	index := len(f.subscribers) - 1
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if index < len(f.subscribers) {
			f.subscribers = append(f.subscribers[:index], f.subscribers[index+1:]...)
		}
	}
}

// 三个方法都必须被识别（路由只做一次判断）。
func TestDesktopUpdateChannelMethodsAreRecognized(t *testing.T) {
	for _, name := range []string{
		"desktopUpdate.channel.get",
		"desktopUpdate.channel.set",
		"desktopUpdate.channel.subscribe",
	} {
		if !IsOfficialDesktopUpdateChannelMethod(name) {
			t.Fatalf("%s 必须被识别为壳更新渠道方法", name)
		}
	}
	if IsOfficialDesktopUpdateChannelMethod("desktopUpdate.channel.bogus") {
		t.Fatal("未声明的方法不该被认领")
	}
}

// 没接线 -> 501，且错误体**说清缺什么**（不假装"没有渠道可选"）。
func TestDesktopUpdateChannelUnwiredIs501(t *testing.T) {
	server := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/desktopUpdate.channel.get", strings.NewReader(`{}`))
	server.ServeOfficialDesktopUpdateChannel(rec, req, "desktopUpdate.channel.get")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("未接线要 501，得到 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "UpdateChannel") {
		t.Fatalf("错误体必须点名缺的是 Options.UpdateChannel：%s", rec.Body.String())
	}
}

// get -> 官方四个键。没选过时 channel 是 **null**（不是空串、不是默认 stable）。
func TestDesktopUpdateChannelGetReturnsNullWhenUnselected(t *testing.T) {
	server := &Server{}
	service := newFakeChannelService()
	server.AttachDesktopUpdateChannel(service)
	defer server.DetachDesktopUpdateChannel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/desktopUpdate.channel.get", strings.NewReader(`{}`))
	server.ServeOfficialDesktopUpdateChannel(rec, req, "desktopUpdate.channel.get")

	if rec.Code != http.StatusOK {
		t.Fatalf("get 应 200，得到 %d", rec.Code)
	}
	var body struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("结果必须是 JSON：%v", err)
	}
	for _, key := range []string{"channel", "packagedChannel", "changing"} {
		if _, ok := body.Result[key]; !ok {
			t.Fatalf("结果缺官方字段 %q：%v", key, body.Result)
		}
	}
	if body.Result["channel"] != nil {
		t.Fatalf("没选过时 channel 必须是 null（官方 channel === null），得到 %v", body.Result["channel"])
	}
	if body.Result["packagedChannel"] != "stable" {
		t.Fatalf("packagedChannel 应是 stable，得到 %v", body.Result["packagedChannel"])
	}
}

// set -> 透传 channel；缺参数要 400 并说清只能是哪两个值。
func TestDesktopUpdateChannelSetForwardsAndValidates(t *testing.T) {
	server := &Server{}
	service := newFakeChannelService()
	server.AttachDesktopUpdateChannel(service)
	defer server.DetachDesktopUpdateChannel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/desktopUpdate.channel.set", strings.NewReader(`{"channel":"nightly"}`))
	server.ServeOfficialDesktopUpdateChannel(rec, req, "desktopUpdate.channel.set")
	if rec.Code != http.StatusOK {
		t.Fatalf("set 应 200（失败也走 result.kind），得到 %d：%s", rec.Code, rec.Body.String())
	}
	if len(service.setCalls) != 1 || service.setCalls[0] != "nightly" {
		t.Fatalf("channel 必须原样透传到服务：%v", service.setCalls)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/desktopUpdate.channel.set", strings.NewReader(`{}`))
	server.ServeOfficialDesktopUpdateChannel(rec, req, "desktopUpdate.channel.set")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 channel 要 400，得到 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "stable") || !strings.Contains(rec.Body.String(), "nightly") {
		t.Fatalf("错误体要说清只能是 stable / nightly：%s", rec.Body.String())
	}
}

// ---------- 「更新状态」这一族（status / open / subscribe） ----------

// fakeStatusService 是更新状态服务的测试替身。
type fakeStatusService struct {
	mu           sync.Mutex
	status       map[string]any
	open         map[string]any
	frames       chan map[string]any
	unsubscribed int
}

func newFakeStatusService() *fakeStatusService {
	return &fakeStatusService{
		status: map[string]any{"phase": "idle"},
		open:   map[string]any{"url": "https://example.invalid/releases", "channel": "stable"},
	}
}

func (f *fakeStatusService) StatusView() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]any{}
	for k, v := range f.status {
		out[k] = v
	}
	return out
}

func (f *fakeStatusService) OpenView(context.Context) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]any{}
	for k, v := range f.open {
		out[k] = v
	}
	return out
}

func (f *fakeStatusService) WireSubscribe() (<-chan map[string]any, func()) {
	f.mu.Lock()
	f.frames = make(chan map[string]any, 8)
	frames := f.frames
	f.mu.Unlock()
	// 首帧：当前状态（与真实实现同一条规矩）。
	frames <- map[string]any{"phase": "idle"}
	return frames, func() {
		f.mu.Lock()
		f.unsubscribed++
		f.mu.Unlock()
	}
}

func (f *fakeStatusService) push(frame map[string]any) {
	f.mu.Lock()
	frames := f.frames
	f.mu.Unlock()
	if frames == nil {
		return
	}
	select {
	case frames <- frame:
	default:
	}
}

func (f *fakeStatusService) unsubscribeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unsubscribed
}

// 三个方法都必须被识别，且不能让渠道那一族的判断认领它们（反之亦然）。
func TestDesktopUpdateStatusMethodsAreRecognized(t *testing.T) {
	for _, name := range []string{
		"desktopUpdate.status",
		"desktopUpdate.open",
		"desktopUpdate.subscribe",
	} {
		if !IsOfficialDesktopUpdateStatusMethod(name) {
			t.Fatalf("%s 必须被识别为壳更新状态方法", name)
		}
		if IsOfficialDesktopUpdateChannelMethod(name) {
			t.Fatalf("%s 不该被渠道那一族认领（两族各有自己的官方来源）", name)
		}
	}
	if IsOfficialDesktopUpdateStatusMethod("desktopUpdate.bogus") {
		t.Fatal("未声明的方法不该被认领")
	}
	if got := OfficialDesktopUpdateStatusMethods(); len(got) != 3 {
		t.Fatalf("这一族恰好 3 个方法，实得 %v", got)
	}
}

// 没接线 -> 501，且错误体**说清缺什么**。
func TestDesktopUpdateStatusUnwiredIs501(t *testing.T) {
	server := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/desktopUpdate.status", strings.NewReader(`{}`))
	server.ServeOfficialDesktopUpdateStatus(rec, req, "desktopUpdate.status")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("未接线要 501，得到 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "UpdateStatus") {
		t.Fatalf("错误体必须点名缺的是 Options.UpdateStatus：%s", rec.Body.String())
	}
}

// status -> 200 + result 就是官方 presentation 的形状。
func TestDesktopUpdateStatusReturnsPresentation(t *testing.T) {
	server := &Server{}
	service := newFakeStatusService()
	service.status = map[string]any{"phase": "available", "version": "9.8.2-go.4"}
	server.AttachDesktopUpdateStatus(service)
	defer server.DetachDesktopUpdateStatus()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/desktopUpdate.status", strings.NewReader(`{}`))
	server.ServeOfficialDesktopUpdateStatus(rec, req, "desktopUpdate.status")

	if rec.Code != http.StatusOK {
		t.Fatalf("status 应 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("结果必须是 JSON：%v", err)
	}
	if body.Result["phase"] != "available" || body.Result["version"] != "9.8.2-go.4" {
		t.Fatalf("result 应是官方 presentation 形状，得到 %v", body.Result)
	}
	// 没有下载能力：percent 不该出现（出现了就是伪造进度）。
	if _, ok := body.Result["percent"]; ok {
		t.Fatalf("percent 不该出现：%v", body.Result)
	}
}

// open -> 200 + result.url（入口一定非空）。
func TestDesktopUpdateOpenReturnsEntry(t *testing.T) {
	server := &Server{}
	service := newFakeStatusService()
	server.AttachDesktopUpdateStatus(service)
	defer server.DetachDesktopUpdateStatus()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/desktopUpdate.open", strings.NewReader(`{}`))
	server.ServeOfficialDesktopUpdateStatus(rec, req, "desktopUpdate.open")

	if rec.Code != http.StatusOK {
		t.Fatalf("open 应 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("结果必须是 JSON：%v", err)
	}
	if body.Result["url"] == "" || body.Result["url"] == nil {
		t.Fatalf("open 必须给出入口 URL，得到 %v", body.Result)
	}
}

// subscribe -> NDJSON 流：先推当前状态，之后推变更；客户端断开即退订。
func TestDesktopUpdateStatusSubscribeStreamsFrames(t *testing.T) {
	server := &Server{}
	service := newFakeStatusService()
	server.AttachDesktopUpdateStatus(service)
	defer server.DetachDesktopUpdateStatus()

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeOfficialDesktopUpdateStatus(w, r, "desktopUpdate.subscribe")
	}))
	defer httpServer.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(httpServer.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("订阅请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("订阅应 200，得到 %d", resp.StatusCode)
	}

	decoder := json.NewDecoder(resp.Body)
	var frame map[string]any
	if err := decoder.Decode(&frame); err != nil {
		t.Fatalf("读首帧：%v", err)
	}
	if frame["phase"] != "idle" {
		t.Fatalf("首帧应是当前状态 idle，得到 %v", frame)
	}

	service.push(map[string]any{"phase": "available", "version": "9.8.2-go.4"})
	if err := decoder.Decode(&frame); err != nil {
		t.Fatalf("读第二帧：%v", err)
	}
	if frame["phase"] != "available" || frame["version"] != "9.8.2-go.4" {
		t.Fatalf("第二帧应是变更后的状态，得到 %v", frame)
	}

	// 断开 -> 服务侧退订（有界等待，不做无限轮询）。
	_ = resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && service.unsubscribeCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if service.unsubscribeCount() == 0 {
		t.Fatal("客户端断开后必须退订（不退订就是泄漏订阅者）")
	}
}

// 两族共用一张挂载表，但**接线互不牵连**：接了一族，另一族该 501 还是 501。
func TestDesktopUpdateFamiliesAreIndependent(t *testing.T) {
	server := &Server{}

	// 只接状态族：渠道族仍然未接线。
	server.AttachDesktopUpdateStatus(newFakeStatusService())
	rec := httptest.NewRecorder()
	server.ServeOfficialDesktopUpdateChannel(rec, httptest.NewRequest(http.MethodPost, "/x", nil), "desktopUpdate.channel.get")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("只接了状态族时渠道族应仍 501，得到 %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	server.ServeOfficialDesktopUpdateStatus(rec, httptest.NewRequest(http.MethodPost, "/x", nil), "desktopUpdate.status")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态族应 200，得到 %d", rec.Code)
	}
	server.DetachDesktopUpdateStatus()

	// 摘掉后状态族回到 501（不留空壳挂载）。
	rec = httptest.NewRecorder()
	server.ServeOfficialDesktopUpdateStatus(rec, httptest.NewRequest(http.MethodPost, "/x", nil), "desktopUpdate.status")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("摘掉后应 501，得到 %d", rec.Code)
	}

	// 只接渠道族：状态族仍然未接线。
	server.AttachDesktopUpdateChannel(newFakeChannelService())
	defer server.DetachDesktopUpdateChannel()
	rec = httptest.NewRecorder()
	server.ServeOfficialDesktopUpdateStatus(rec, httptest.NewRequest(http.MethodPost, "/x", nil), "desktopUpdate.status")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("只接了渠道族时状态族应仍 501，得到 %d", rec.Code)
	}
}
