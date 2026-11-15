package updater_test

// status_test.go —— 宿主「更新检测」的行为断言。
//
// 断言的是**行为**（拿到的 phase / version / failure、以及"发了几次请求"），
// 不是"函数被调过"。每一条都对应一个真实会出错的判断：
//
//	有新版 -> available + 正确 version      发布页有比运行中更新的东西就得说出来
//	已是最新 -> idle                        不能把"没有更新"说成"更新失败"
//	draft / prerelease 在 stable 下被跳过   拿 draft 当更新 = 让用户装一个没发布的东西
//	网络失败 -> check-network               归错档会让用户去查配置而不是查网
//	解析失败 -> check                       同上，反方向
//	版本比较边界                             go.9 vs go.10 是数字比还是字符串比
//	TTL 内只发一次请求                       不许打爆 GitHub
//	并发只发一次（单飞）                     同上
//	只产生 4 态、percent 永远没有            没有下载器就不许报下载进度

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"dshgo/internal/updater"
)

// runningVersion 是测试里的"运行中版本"（仓库规约要求夹具用假版本，
// 不能让真版本号的前缀出现在代码里——`cmd/dshd/version_guard_test.go` 会扫）。
const runningVersion = "9.8.2-go.3"

// ---------- 测试替身 ----------

// fakeChannel 是壳渠道的替身：状态由测试显式摆布。
type fakeChannel struct {
	mu    sync.Mutex
	state updater.State
}

func newFakeChannel() *fakeChannel {
	return &fakeChannel{state: updater.State{PackagedChannel: updater.ChannelStable}}
}

func (f *fakeChannel) State() updater.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeChannel) selectChannel(channel updater.Channel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value := channel
	f.state.Channel = &value
}

func (f *fakeChannel) fail(code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Error = code
}

// fakeClock 是可注入时钟：TTL 判定用得上，测试不真的等 5 分钟。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// githubStub 是一个只回答 `GET /repos/<owner>/<repo>/releases` 的假 GitHub。
//
// 它记两个数：总请求数（钉"不许打爆"）与带 `If-None-Match` 的请求数（钉条件请求）。
type githubStub struct {
	server *httptest.Server

	mu          sync.Mutex
	requests    int
	conditional int
	// gate 非 nil 时，每个请求先等它关闭——用来把"请求正在飞"这个瞬间钉住。
	gate chan struct{}
	// respond 决定这一条怎么答；nil 时回 500。
	respond func(r *http.Request) (status int, etag string, body string)
}

func newGitHubStub(t *testing.T) *githubStub {
	t.Helper()
	stub := &githubStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.requests++
		if r.Header.Get("If-None-Match") != "" {
			stub.conditional++
		}
		gate := stub.gate
		respond := stub.respond
		stub.mu.Unlock()

		if gate != nil {
			<-gate
		}
		if respond == nil {
			http.Error(w, "stub 没写应答", http.StatusInternalServerError)
			return
		}
		status, etag, body := respond(r)
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (g *githubStub) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.requests
}

func (g *githubStub) conditionalCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.conditional
}

func (g *githubStub) setRespond(fn func(r *http.Request) (int, string, string)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.respond = fn
}

func (g *githubStub) setGate(gate chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gate = gate
}

// answerReleases 让 stub 回一份 release 数组（200 + ETag）。
func answerReleases(t *testing.T, releases ...updater.Release) func(*http.Request) (int, string, string) {
	t.Helper()
	raw, err := json.Marshal(releases)
	if err != nil {
		t.Fatalf("编 release 夹具：%v", err)
	}
	return func(*http.Request) (int, string, string) {
		return http.StatusOK, `W/"stub-1"`, string(raw)
	}
}

// ---------- 组装 ----------

type statusHarness struct {
	service *updater.StatusService
	stub    *githubStub
	channel *fakeChannel
	clock   *fakeClock
}

func newStatusHarness(t *testing.T, opts ...func(*updater.StatusOptions)) *statusHarness {
	t.Helper()
	stub := newGitHubStub(t)
	channel := newFakeChannel()
	clock := newFakeClock()
	settings := updater.StatusOptions{
		Version: runningVersion,
		Channel: channel,
		APIBase: stub.server.URL,
		WebBase: "https://example.invalid",
		Repo:    "acme/dsh-go",
		Now:     clock.Now,
	}
	for _, apply := range opts {
		apply(&settings)
	}
	service, err := updater.NewStatusService(settings)
	if err != nil {
		t.Fatalf("NewStatusService: %v", err)
	}
	return &statusHarness{service: service, stub: stub, channel: channel, clock: clock}
}

// settle 等到"结论"出现（不再是 checking），返回那一刻的状态。
func (h *statusHarness) settle(t *testing.T) updater.Presentation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last updater.Presentation
	for time.Now().Before(deadline) {
		last = h.service.Status()
		if last.Phase != updater.PhaseChecking {
			return last
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等结论超时，最后拿到 %+v", last)
	return updater.Presentation{}
}

// settleWithClock 推进假时钟跨越 TTL，再等结论（用于"过期后重查"）。
func (h *statusHarness) refresh(t *testing.T) updater.Presentation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.service.Refresh(ctx, false)
}

// ---------- 官方词表 ----------

// 官方 phase 与 failure 的**逐字**词表必须一字不差。
//
// 为什么单独钉：这些字符串要经 wire 交给渲染层查文案表
// （`DesktopUpdateIndicator.tsx:18-45` 是按 phase / failure 去查 key 的），
// 拼错一个字母界面上就是一句空白，而不是一个报错。
func TestOfficialVocabularyIsVerbatim(t *testing.T) {
	wantFailures := []string{
		"configuration", "check", "check-network",
		"download", "download-network", "download-space", "download-permission", "download-busy",
		"install", "install-network", "stop-failed",
		"tasks-changed", "tasks-unavailable", "fullscreen-timeout",
	}
	if got := updater.FailureKinds(); !reflect.DeepEqual(got, wantFailures) {
		t.Fatalf("failure 词表不是官方那 14 个：\n got %v\nwant %v", got, wantFailures)
	}

	// 本仓只产生 3 个 failure（其余 11 个都要求下载/安装链路）。
	wantProducible := []string{"configuration", "check", "check-network"}
	if got := updater.ProducibleFailures(); !reflect.DeepEqual(got, wantProducible) {
		t.Fatalf("本仓可产生的 failure 应是 %v，实得 %v", wantProducible, got)
	}
	for _, failure := range updater.ProducibleFailures() {
		if !containsString(wantFailures, failure) {
			t.Fatalf("可产生的 failure %q 不在官方词表里", failure)
		}
	}

	// phase：本仓只产生 4 态；官方另外 4 态在代码里存在但**永不出现在结果里**。
	wantPhases := []updater.Phase{updater.PhaseIdle, updater.PhaseChecking, updater.PhaseAvailable, updater.PhaseError}
	if got := updater.ProduciblePhases(); !reflect.DeepEqual(got, wantPhases) {
		t.Fatalf("本仓可产生的 phase 应是 %v，实得 %v", wantPhases, got)
	}
	officialOnly := map[updater.Phase]string{
		updater.PhaseDownloading: "downloading",
		updater.PhaseVerifying:   "verifying",
		updater.PhaseInstalling:  "installing",
		updater.PhaseReady:       "ready",
	}
	for phase, literal := range officialOnly {
		if string(phase) != literal {
			t.Fatalf("phase 常量 %q 与官方字面量 %q 不一致", phase, literal)
		}
		if containsPhase(wantPhases, phase) {
			t.Fatalf("%q 不该出现在可产生列表里（本仓没有那条链路）", phase)
		}
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func containsPhase(list []updater.Phase, want updater.Phase) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// ---------- status 的四态 ----------

// 冷启动第一次 status() 是 checking，第二次（检查已落地）就是结论。
//
// 这是官方形状：`status()` 拿"此刻已知的状态"，检查在后台跑
// （`desktop-update-source.ts:22-31`）。
func TestStatusFirstCallIsCheckingThenAvailable(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{
		TagName: "v9.8.2-go.4", Name: "next", HTMLURL: "https://example.invalid/releases/tag/v9.8.2-go.4",
	}))

	first := h.service.Status()
	if first.Phase != updater.PhaseChecking {
		t.Fatalf("冷启动第一次必须是 checking，得到 %q", first.Phase)
	}

	got := h.settle(t)
	if got.Phase != updater.PhaseAvailable {
		t.Fatalf("有更新的发布时应 available，得到 %+v", got)
	}
	if got.Version != "9.8.2-go.4" {
		t.Fatalf("available 必须带**规范化后**的 version（不带前导 v），得到 %q", got.Version)
	}
	if got.Failure != "" {
		t.Fatalf("available 不该带 failure，得到 %q", got.Failure)
	}
}

// 已是最新（release 版本 == 运行中版本）-> idle。
func TestStatusIdleWhenSameVersion(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.8.2-go.3", HTMLURL: "https://example.invalid/x"}))

	got := h.settle(t)
	if got.Phase != updater.PhaseIdle {
		t.Fatalf("同版本应 idle，得到 %+v", got)
	}
	if got.Version != "" {
		t.Fatalf("idle 不该带 version，得到 %q", got.Version)
	}
}

// 运行中的版本比最新发布还新（本地跑在前头）-> idle，不许报"有更新"。
func TestStatusIdleWhenRunningIsAhead(t *testing.T) {
	h := newStatusHarness(t, func(o *updater.StatusOptions) { o.Version = "9.9.1-go.7" })
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.8.2-go.9"}))

	got := h.settle(t)
	if got.Phase != updater.PhaseIdle {
		t.Fatalf("跑在前头应 idle，得到 %+v", got)
	}
}

// 一条 release 都没有 -> idle（不是 error：仓库真的没有发布，那是确定的事实）。
func TestStatusIdleWhenNoReleases(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t))

	got := h.settle(t)
	if got.Phase != updater.PhaseIdle {
		t.Fatalf("空发布列表应 idle，得到 %+v", got)
	}
}

// ---------- channel 过滤 ----------

// stable 通道必须跳过 draft、跳过 GitHub 标记的 prerelease、跳过 `-gobeta.`。
func TestStableChannelSkipsDraftPrereleaseAndBeta(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t,
		updater.Release{TagName: "v9.9.9-go.1", Draft: true, HTMLURL: "https://example.invalid/draft"},
		updater.Release{TagName: "v9.9.8-go.1", Prerelease: true, HTMLURL: "https://example.invalid/pre"},
		updater.Release{TagName: "v9.9.7-gobeta.1", HTMLURL: "https://example.invalid/beta"},
		updater.Release{TagName: "v9.8.2-go.4", HTMLURL: "https://example.invalid/stable"},
	))

	got := h.settle(t)
	if got.Phase != updater.PhaseAvailable {
		t.Fatalf("应 available，得到 %+v", got)
	}
	if got.Version != "9.8.2-go.4" {
		t.Fatalf("stable 只能看到正式版，得到 %q（draft / prerelease / beta 都被漏过了）", got.Version)
	}
}

// 只有 draft 时 stable 就是 idle——draft 在官方语义里**还没发布**。
func TestStableChannelIdleWhenOnlyDrafts(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t,
		updater.Release{TagName: "v9.9.9-go.1", Draft: true},
		updater.Release{TagName: "v9.9.8-go.1", Draft: true, Prerelease: true},
	))

	got := h.settle(t)
	if got.Phase != updater.PhaseIdle {
		t.Fatalf("只有 draft 就该 idle，得到 %+v", got)
	}
}

// nightly 通道包含 prerelease（这里用 GitHub 的 prerelease 标记 + `-gobeta.`）。
func TestNightlyChannelIncludesPrerelease(t *testing.T) {
	h := newStatusHarness(t)
	h.channel.selectChannel(updater.ChannelNightly)
	h.stub.setRespond(answerReleases(t,
		updater.Release{TagName: "v9.8.2-go.4", HTMLURL: "https://example.invalid/stable"},
		updater.Release{TagName: "v9.8.3-go.1", Prerelease: true, HTMLURL: "https://example.invalid/pre"},
		updater.Release{TagName: "v9.9.0-gobeta.2", HTMLURL: "https://example.invalid/beta"},
	))

	got := h.settle(t)
	if got.Phase != updater.PhaseAvailable {
		t.Fatalf("应 available，得到 %+v", got)
	}
	if got.Version != "9.9.0-gobeta.2" {
		t.Fatalf("nightly 应取最高的那个（含 beta），得到 %q", got.Version)
	}
}

// nightly 也不许把 draft 当发布。
func TestNightlyChannelStillSkipsDraft(t *testing.T) {
	h := newStatusHarness(t)
	h.channel.selectChannel(updater.ChannelNightly)
	h.stub.setRespond(answerReleases(t,
		updater.Release{TagName: "v9.9.9-go.9", Draft: true},
		updater.Release{TagName: "v9.8.2-go.4"},
	))

	got := h.settle(t)
	if got.Version != "9.8.2-go.4" {
		t.Fatalf("nightly 也要跳过 draft，得到 %q", got.Version)
	}
}

// 不是我们命名规范的 tag（例如上游的 rc）不算我们的发布。
//
// 依据：用户 2026-11-13 裁定的版本规范是 `<上游主版本>-go.<N>`，`.N` 是我们自己的
// 发版序号、不因上游换 rc 而重置。把上游 rc 当"新版本"会让界面叫用户去装官方包。
func TestForeignTagsAreNotOurReleases(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t,
		updater.Release{TagName: "v9.9.9-rc.2", HTMLURL: "https://example.invalid/rc"},
		updater.Release{TagName: "nightly", HTMLURL: "https://example.invalid/nightly"},
		updater.Release{TagName: "v9.9.9", HTMLURL: "https://example.invalid/bare"},
	))

	got := h.settle(t)
	if got.Phase != updater.PhaseIdle {
		t.Fatalf("非本仓命名的 tag 不得算更新，得到 %+v", got)
	}
}

// 没选过渠道时用**打包渠道**检查（打包渠道是这个构建自己的事实）。
func TestUnselectedChannelFallsBackToPackaged(t *testing.T) {
	h := newStatusHarness(t)
	h.channel.mu.Lock()
	h.channel.state.PackagedChannel = updater.ChannelNightly
	h.channel.mu.Unlock()
	h.stub.setRespond(answerReleases(t,
		updater.Release{TagName: "v9.9.0-gobeta.1", HTMLURL: "https://example.invalid/beta"},
	))

	got := h.settle(t)
	if got.Phase != updater.PhaseAvailable || got.Version != "9.9.0-gobeta.1" {
		t.Fatalf("打包渠道是 nightly 时应看到 beta，得到 %+v", got)
	}
}

// 渠道变更后缓存作废：stable 下的 idle 结论不能沿用到 nightly。
func TestInvalidateOnChannelChange(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.9.0-gobeta.1"}))

	if got := h.settle(t); got.Phase != updater.PhaseIdle {
		t.Fatalf("stable 下 beta 不算更新，应 idle，得到 %+v", got)
	}
	if h.stub.count() != 1 {
		t.Fatalf("第一次检查应只发 1 个请求，实得 %d", h.stub.count())
	}

	h.channel.selectChannel(updater.ChannelNightly)
	h.service.Invalidate()
	got := h.settle(t)
	if got.Phase != updater.PhaseAvailable {
		t.Fatalf("换到 nightly 且缓存已作废时应收敛到 available，得到 %+v", got)
	}
	if h.stub.count() != 2 {
		t.Fatalf("作废后应重新查一次（共 2 个请求），实得 %d", h.stub.count())
	}
}

// ---------- failure 分类 ----------

// 网络不通 -> check-network（不是 check：用户该去查网，不是查配置）。
func TestNetworkFailureIsCheckNetwork(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()

	service, err := updater.NewStatusService(updater.StatusOptions{
		Version: runningVersion,
		Channel: newFakeChannel(),
		APIBase: url,
		WebBase: "https://example.invalid",
		Repo:    "acme/dsh-go",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := service.Refresh(ctx, true)
	if got.Phase != updater.PhaseError || got.Failure != updater.FailureCheckNetwork {
		t.Fatalf("连不上应 error/check-network，得到 %+v", got)
	}
}

// 超时 -> check-network。
func TestTimeoutIsCheckNetwork(t *testing.T) {
	h := newStatusHarness(t, func(o *updater.StatusOptions) {
		o.HTTP = &http.Client{Timeout: 60 * time.Millisecond}
	})
	h.stub.setRespond(func(*http.Request) (int, string, string) {
		time.Sleep(400 * time.Millisecond)
		return http.StatusOK, "", "[]"
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := h.service.Refresh(ctx, true)
	if got.Phase != updater.PhaseError || got.Failure != updater.FailureCheckNetwork {
		t.Fatalf("超时应 error/check-network，得到 %+v", got)
	}
}

// 响应解析不了 -> check（拿到了确定答复，但答案不可用）。
func TestUnparsableBodyIsCheck(t *testing.T) {
	for name, body := range map[string]string{
		"不是 JSON": `{"message":`,
		"不是数组":    `{"message":"Not Found"}`,
		"是数组但元素错": `[{"tag_name": 42}]`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newStatusHarness(t)
			h.stub.setRespond(func(*http.Request) (int, string, string) {
				return http.StatusOK, "", body
			})
			got := h.service.Refresh(context.Background(), true)
			if got.Phase != updater.PhaseError || got.Failure != updater.FailureCheck {
				t.Fatalf("%s 应 error/check，得到 %+v", name, got)
			}
		})
	}
}

// HTTP 状态码各自的归类：任何非 2xx 都是"答复不可用"（check）。
//
// 依据是官方分类器：它只看 message 命不命中网络正则（rc.2 app.asar `lib/main.js:16644`
// 的 NETWORK_FAILURE：ERR_CONNECTION_*/ETIMEDOUT），HTTP 状态码本身不算网络失败
// （`:16654-16655`）。所以 5xx 也归 `check`，而不是 `check-network`。
func TestHTTPStatusClassification(t *testing.T) {
	cases := map[int]string{
		http.StatusUnauthorized:        updater.FailureCheck,
		http.StatusForbidden:           updater.FailureCheck,
		http.StatusNotFound:            updater.FailureCheck,
		http.StatusTooManyRequests:     updater.FailureCheck,
		http.StatusInternalServerError: updater.FailureCheck,
		http.StatusBadGateway:          updater.FailureCheck,
		http.StatusServiceUnavailable:  updater.FailureCheck,
	}
	for status, wantFailure := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newStatusHarness(t)
			h.stub.setRespond(func(*http.Request) (int, string, string) {
				return status, "", `{"message":"nope"}`
			})
			got := h.service.Refresh(context.Background(), true)
			if got.Phase != updater.PhaseError || got.Failure != wantFailure {
				t.Fatalf("HTTP %d 应 error/%s，得到 %+v", status, wantFailure, got)
			}
		})
	}
}

// 壳配置坏 -> configuration，并且**一个请求都不发**。
//
// 同时把官方 `configurationError` 填上渠道那四个错误码：渲染层先判它
// （`DesktopUpdateIndicator.tsx:17`），它给的文案比 failure 那句更具体。
func TestBrokenShellConfigurationShortCircuits(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.9.9-go.1"}))
	h.channel.fail("format")

	got := h.settle(t)
	if got.Phase != updater.PhaseError {
		t.Fatalf("壳配置坏应 error，得到 %+v", got)
	}
	if got.Failure != updater.FailureConfiguration {
		t.Fatalf("壳配置坏应 failure=configuration，得到 %q", got.Failure)
	}
	if got.ConfigurationError != "format" {
		t.Fatalf("configurationError 要带上渠道的原始错误码，得到 %q", got.ConfigurationError)
	}
	if h.stub.count() != 0 {
		t.Fatalf("壳配置坏时不该出网，实得 %d 个请求", h.stub.count())
	}

	// 修好配置后能自己恢复（不粘在 error 上）。
	h.channel.mu.Lock()
	h.channel.state.Error = ""
	h.channel.mu.Unlock()
	h.service.Invalidate()
	if again := h.settle(t); again.Phase != updater.PhaseAvailable {
		t.Fatalf("配置修好后应恢复检查，得到 %+v", again)
	}
}

// ---------- 缓存与单飞 ----------

// TTL 内无论问多少次都只发一次请求；越过 TTL 才再查一次。
func TestTTLServesFromCache(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.8.2-go.4"}))

	if got := h.settle(t); got.Phase != updater.PhaseAvailable {
		t.Fatalf("应 available，得到 %+v", got)
	}
	if h.stub.count() != 1 {
		t.Fatalf("第一次检查应只发 1 个请求，实得 %d", h.stub.count())
	}

	for i := 0; i < 20; i++ {
		if got := h.service.Status(); got.Phase != updater.PhaseAvailable {
			t.Fatalf("第 %d 次 status 应从缓存回 available，得到 %+v", i, got)
		}
	}
	if h.stub.count() != 1 {
		t.Fatalf("TTL 内不该再出网，实得 %d 个请求", h.stub.count())
	}

	// 推进到 TTL 边界之内仍然不出网。
	h.clock.advance(4 * time.Minute)
	_ = h.service.Status()
	if h.stub.count() != 1 {
		t.Fatalf("TTL 未过不该出网，实得 %d", h.stub.count())
	}

	// 越过 TTL：重新查一次。
	h.clock.advance(2 * time.Minute)
	if got := h.settle(t); got.Phase != updater.PhaseAvailable {
		t.Fatalf("过期重查后应 available，得到 %+v", got)
	}
	if h.stub.count() != 2 {
		t.Fatalf("TTL 过后应重查（共 2 次），实得 %d", h.stub.count())
	}
}

// 并发观察只发一次请求（单飞）。
func TestConcurrentObserveSingleFlight(t *testing.T) {
	h := newStatusHarness(t)
	gate := make(chan struct{})
	h.stub.setGate(gate)
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.8.2-go.4"}))

	// 第一个 status() 起一次检查，并把它钉在"在飞"状态上。
	if got := h.service.Status(); got.Phase != updater.PhaseChecking {
		t.Fatalf("第一次应 checking，得到 %+v", got)
	}

	var wg sync.WaitGroup
	results := make([]updater.Presentation, 8)
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results[index] = h.service.Refresh(ctx, false)
		}(i)
	}
	// 等到 8 个 goroutine 都挂在那一次在飞的检查上，再放行。
	time.Sleep(80 * time.Millisecond)
	close(gate)
	wg.Wait()

	if h.stub.count() != 1 {
		t.Fatalf("并发只需 1 个请求（单飞），实得 %d", h.stub.count())
	}
	for i, got := range results {
		if got.Phase != updater.PhaseAvailable {
			t.Fatalf("第 %d 个并发调用应拿到 available，得到 %+v", i, got)
		}
	}
}

// 条件请求：第二次带上 ETag，服务端 304 时复用上次结论、不算失败。
func TestConditionalRequestReusesVerdict(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.8.2-go.4", HTMLURL: "https://example.invalid/r"}))

	if got := h.settle(t); got.Phase != updater.PhaseAvailable {
		t.Fatalf("应 available，得到 %+v", got)
	}
	h.stub.setRespond(func(r *http.Request) (int, string, string) {
		if r.Header.Get("If-None-Match") == `W/"stub-1"` {
			return http.StatusNotModified, "", ""
		}
		return http.StatusOK, `W/"stub-1"`, "[]"
	})

	h.clock.advance(10 * time.Minute)
	got := h.settle(t)
	if got.Phase != updater.PhaseAvailable || got.Version != "9.8.2-go.4" {
		t.Fatalf("304 时应复用上次结论，得到 %+v", got)
	}
	if h.stub.conditionalCount() != 1 {
		t.Fatalf("第二次请求应带 If-None-Match，实得 %d 次", h.stub.conditionalCount())
	}
}

// ---------- open ----------

// open 绕过 TTL 真查一次，并把**那条 release 的页面**交出来。
func TestOpenBypassesTTLAndReturnsReleaseURL(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{
		TagName: "v9.8.2-go.4", HTMLURL: "https://example.invalid/releases/tag/v9.8.2-go.4",
	}))

	if got := h.settle(t); got.Phase != updater.PhaseAvailable {
		t.Fatalf("应 available，得到 %+v", got)
	}
	if h.stub.count() != 1 {
		t.Fatalf("settle 后应只有 1 个请求，实得 %d", h.stub.count())
	}

	result := h.service.Open(context.Background())
	if h.stub.count() != 2 {
		t.Fatalf("open 是用户点了，应绕过 TTL 真查一次（共 2 次），实得 %d", h.stub.count())
	}
	if result.URL != "https://example.invalid/releases/tag/v9.8.2-go.4" {
		t.Fatalf("open 应给出对应 release 的 URL，得到 %q", result.URL)
	}
	if result.Presentation.Phase != updater.PhaseAvailable {
		t.Fatalf("open 的结果里应带 available，得到 %+v", result.Presentation)
	}
	if result.Channel != updater.ChannelStable {
		t.Fatalf("open 应报实际用的渠道，得到 %q", result.Channel)
	}
}

// 没有更新时 open 给出 releases 列表页（入口一定非空）。
func TestOpenWithoutUpdateReturnsReleasesPage(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.8.2-go.3"}))

	result := h.service.Open(context.Background())
	if result.URL != "https://example.invalid/acme/dsh-go/releases" {
		t.Fatalf("没有具体 release 时应给 releases 页，得到 %q", result.URL)
	}
	if result.Presentation.Phase != updater.PhaseIdle {
		t.Fatalf("应 idle，得到 %+v", result.Presentation)
	}
}

// open 失败（网络）时 URL 仍然非空，且失败如实写在 presentation 里。
func TestOpenOnFailureStillReturnsEntryAndFailure(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(func(*http.Request) (int, string, string) {
		return http.StatusForbidden, "", `{"message":"rate limited"}`
	})

	result := h.service.Open(context.Background())
	if result.URL == "" {
		t.Fatal("open 的入口不许为空——失败时至少要给 releases 列表页")
	}
	if result.Presentation.Phase != updater.PhaseError || result.Presentation.Failure != updater.FailureCheck {
		t.Fatalf("403 应如实进 error/check，得到 %+v", result.Presentation)
	}
}

// ---------- 订阅 ----------

// 订阅先给当前状态，检查有结论后再推一帧。
func TestSubscribePushesCurrentThenConclusion(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.8.2-go.4"}))

	frames, unsubscribe := h.service.WireSubscribe()
	defer unsubscribe()

	first := <-frames
	if first["phase"] != string(updater.PhaseIdle) {
		t.Fatalf("首帧应是「当前状态」（冷启动 = idle），得到 %v", first)
	}

	_ = h.service.Status()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case frame := <-frames:
			if frame["phase"] == string(updater.PhaseAvailable) {
				if frame["version"] != "9.8.2-go.4" {
					t.Fatalf("available 帧要带 version，得到 %v", frame)
				}
				return
			}
		case <-deadline:
			t.Fatal("没等到 available 帧")
		}
	}
}

// ---------- wire 形状 ----------

// 空字段不出现；`percent` 永远不出现（本仓没有下载进度）。
func TestWireShapeOmitsEmptyFields(t *testing.T) {
	h := newStatusHarness(t)
	h.stub.setRespond(answerReleases(t, updater.Release{TagName: "v9.8.2-go.3"}))

	h.settle(t)
	idle := h.service.StatusView()
	if len(idle) != 1 || idle["phase"] != string(updater.PhaseIdle) {
		t.Fatalf("idle 的线上形状应只有 phase，得到 %v", idle)
	}
	if _, ok := idle["percent"]; ok {
		t.Fatalf("percent 永远不该出现，得到 %v", idle)
	}

	h.stub.setRespond(func(*http.Request) (int, string, string) {
		return http.StatusUnauthorized, "", ""
	})
	h.clock.advance(10 * time.Minute)
	h.settle(t)
	failed := h.service.StatusView()
	if failed["phase"] != string(updater.PhaseError) || failed["failure"] != updater.FailureCheck {
		t.Fatalf("失败帧应带 phase+failure，得到 %v", failed)
	}
	if len(failed) != 2 {
		t.Fatalf("失败帧不该带别的键，得到 %v", failed)
	}
}

// 所有链路的返回值都必须在 4 态之内，且 percent 永远为空。
func TestOnlyFourPhasesAreEverProduced(t *testing.T) {
	scenarios := []struct {
		name    string
		respond func(*http.Request) (int, string, string)
	}{
		{"有更新", answerReleases(t, updater.Release{TagName: "v9.8.2-go.4"})},
		{"无更新", answerReleases(t, updater.Release{TagName: "v9.8.2-go.3"})},
		{"空列表", answerReleases(t)},
		{"只有 draft", answerReleases(t, updater.Release{TagName: "v9.9.9-go.1", Draft: true})},
		{"404", func(*http.Request) (int, string, string) { return http.StatusNotFound, "", "" }},
		{"500", func(*http.Request) (int, string, string) { return http.StatusInternalServerError, "", "" }},
		{"坏 JSON", func(*http.Request) (int, string, string) { return http.StatusOK, "", "{" }},
	}
	producible := updater.ProduciblePhases()
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			h := newStatusHarness(t)
			h.stub.setRespond(scenario.respond)
			got := h.settle(t)
			if !containsPhase(producible, got.Phase) {
				t.Fatalf("产出了不该有的 phase %q（本仓只有 %v）", got.Phase, producible)
			}
			if got.Percent != nil {
				t.Fatalf("percent 必须永远为空（没有下载器），得到 %d", *got.Percent)
			}
			if got.Phase == updater.PhaseAvailable && got.Version == "" {
				t.Fatal("available 必须带 version")
			}
			if got.Phase == updater.PhaseError && got.Failure == "" {
				t.Fatal("error 必须带 failure")
			}
			if got.Phase != updater.PhaseError && got.Failure != "" {
				t.Fatalf("%s 不该带 failure，得到 %q", got.Phase, got.Failure)
			}
			if got.Phase != updater.PhaseError && got.ConfigurationError != "" {
				t.Fatalf("%s 不该带 configurationError，得到 %q", got.Phase, got.ConfigurationError)
			}
		})
	}
}

// ---------- 装配错误要响亮 ----------

// 没有运行中版本 / 版本解析不了 / 没有渠道来源都必须是**装配错误**，
// 不许静默退化成一个"永远查不到更新"的空壳。
func TestNewStatusServiceRejectsBadWiring(t *testing.T) {
	cases := map[string]updater.StatusOptions{
		"缺版本":              {Channel: newFakeChannel()},
		"版本解析不了":           {Version: "nightly", Channel: newFakeChannel()},
		"缺渠道来源":            {Version: runningVersion},
		"仓库名不是 owner/name": {Version: runningVersion, Channel: newFakeChannel(), Repo: "dsh-go"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := updater.NewStatusService(opts); err == nil {
				t.Fatalf("%s 必须报错（响亮失败），却成功构造了服务", name)
			}
		})
	}
}
