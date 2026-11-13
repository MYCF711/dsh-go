package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
