package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeSession 是一次探针会话的替身：把官方 probe.ts 的每条分支都做成可编排的开关。
type fakeSession struct {
	connectErr  error
	info        ServerInfo
	hasInfo     bool
	hasTools    bool
	pages       map[string]page
	pageCursors []string
	listErr     error
	listCalls   int
	block       bool // 每次 tools/list 都阻塞到 ctx 结束（用来看超时/取消）
	closeSleep  time.Duration
	closeSt     CloseResult
	closed      bool
	closeCalls  int
}

type page struct {
	names []string
	next  string
}

func (s *fakeSession) Connect(context.Context) error { return s.connectErr }

func (s *fakeSession) ServerInfo() (ServerInfo, bool) { return s.info, s.hasInfo }

func (s *fakeSession) HasTools() bool { return s.hasTools }

func (s *fakeSession) ListTools(ctx context.Context, cursor string) ([]string, string, error) {
	s.listCalls++
	s.pageCursors = append(s.pageCursors, cursor)
	if s.block {
		<-ctx.Done()
		return nil, "", ctx.Err()
	}
	if s.listErr != nil {
		return nil, "", s.listErr
	}
	got := s.pages[cursor]
	return got.names, got.next, nil
}

func (s *fakeSession) Close(context.Context) CloseResult {
	s.closeCalls++
	if s.closeSleep > 0 {
		time.Sleep(s.closeSleep)
	}
	s.closed = true
	return s.closeSt
}

type fakeTarget struct {
	sess ProbeSession
	err  error
}

func (t fakeTarget) Open(context.Context) (ProbeSession, error) { return t.sess, t.err }

// TestProbeOptionsResolveDefaults 钉住官方 probe.ts 的四个默认值：
// 整场预算 10000ms、翻页上限 16、工具名上限 512、清理预算 5000ms
// （dsh-mcp-client/src/probe.ts:35-41 与 index.ts:49）。
func TestProbeOptionsResolveDefaults(t *testing.T) {
	got, err := ProbeOptions{}.Resolve()
	if err != nil {
		t.Fatalf("默认值不该报错: %v", err)
	}
	if got.TimeoutMs != 10_000 || got.MaxPages != 16 || got.MaxToolNames != 512 || got.CloseTimeoutMs != 5_000 {
		t.Fatalf("默认值不对: %+v", got)
	}
	for _, bad := range []ProbeOptions{
		{TimeoutMs: -1}, {MaxPages: -1}, {MaxToolNames: -3}, {CloseTimeoutMs: -1},
	} {
		if _, err := bad.Resolve(); err == nil {
			t.Fatalf("%+v 应该被拒绝", bad)
		}
	}
}

// TestProbeSuccess 覆盖官方成功支：serverInfo 有就带、工具名按页聚合、不计清理耗时。
func TestProbeSuccess(t *testing.T) {
	sess := &fakeSession{
		hasInfo:  true,
		info:     ServerInfo{Name: "fixture", Version: "9.9.9"},
		hasTools: true,
		pages: map[string]page{
			"":   {names: []string{"echo"}, next: "p2"},
			"p2": {names: []string{"slow"}},
		},
		closeSleep: 120 * time.Millisecond,
		closeSt:    CloseResult{CloseConfirmed: true},
	}
	got, err := ProbeServer(context.Background(), fakeTarget{sess: sess}, ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if !got.OK || got.Stage != "" || got.Message != "" {
		t.Fatalf("必须是成功结果: %+v", got)
	}
	if got.ServerInfo == nil || got.ServerInfo.Name != "fixture" || got.ServerInfo.Version != "9.9.9" {
		t.Fatalf("serverInfo 丢了: %+v", got.ServerInfo)
	}
	if strings.Join(got.ToolNames, ",") != "echo,slow" {
		t.Fatalf("工具名聚合不对: %v", got.ToolNames)
	}
	if got.Truncated {
		t.Fatal("两页以内的完整列表不该标 truncated")
	}
	// durationMs 只算协商+列举，**不含**清理（官方 probe.ts:96 在 close 之前取时间）。
	if got.DurationMs >= 120 {
		t.Fatalf("durationMs 把清理耗时算进去了: %dms", got.DurationMs)
	}
	if !sess.closed || sess.closeCalls != 1 {
		t.Fatalf("探针必须关掉自己那一代连接: %+v", sess)
	}
	if !got.Close.CloseConfirmed || got.Close.CleanupError != "" {
		t.Fatalf("清理结果不对: %+v", got.Close)
	}
	if strings.Join(sess.pageCursors, "|") != "|p2" {
		t.Fatalf("翻页游标传错了: %v", sess.pageCursors)
	}
}

// TestProbeWithoutToolsCapabilitySkipsListing 钉住官方判据：
// server 没声明 `tools` 能力就**不发** tools/list，结果是一张空列表且不标 truncated。
func TestProbeWithoutToolsCapabilitySkipsListing(t *testing.T) {
	sess := &fakeSession{hasTools: false, closeSt: CloseResult{CloseConfirmed: true}}
	got, err := ProbeServer(context.Background(), fakeTarget{sess: sess}, ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if !got.OK || len(got.ToolNames) != 0 || got.Truncated {
		t.Fatalf("结果不对: %+v", got)
	}
	if sess.listCalls != 0 {
		t.Fatalf("没声明 tools 能力却发了 %d 次 tools/list", sess.listCalls)
	}
}

// TestProbeTruncatesByToolNameCap 钉住 maxToolNames 的截断语义。
func TestProbeTruncatesByToolNameCap(t *testing.T) {
	sess := &fakeSession{hasTools: true, pages: map[string]page{
		"":   {names: []string{"a", "b"}, next: "p2"},
		"p2": {names: []string{"c"}, next: "p3"},
	}, closeSt: CloseResult{CloseConfirmed: true}}
	got, err := ProbeServer(context.Background(), fakeTarget{sess: sess}, ProbeOptions{MaxToolNames: 2})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if !got.Truncated || strings.Join(got.ToolNames, ",") != "a,b" {
		t.Fatalf("按名字上限截断不对: %+v", got)
	}
	// 官方 probe.ts:88 是在**看到第 max+1 个候选**时才判截断（页内 break），
	// 所以第一页正好凑满上限、又给了游标时它会再取一页来确认——两次调用是对的。
	if sess.listCalls != 2 {
		t.Fatalf("确认截断的翻页次数不对: %d", sess.listCalls)
	}
}

// TestProbeTruncatesByPageCap 钉住 maxPages：最后一页仍有游标 → truncated。
func TestProbeTruncatesByPageCap(t *testing.T) {
	sess := &fakeSession{hasTools: true, pages: map[string]page{
		"":   {names: []string{"a"}, next: "p2"},
		"p2": {names: []string{"b"}, next: "p3"},
		"p3": {names: []string{"c"}},
	}, closeSt: CloseResult{CloseConfirmed: true}}
	got, err := ProbeServer(context.Background(), fakeTarget{sess: sess}, ProbeOptions{MaxPages: 2})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if !got.Truncated || strings.Join(got.ToolNames, ",") != "a,b" || sess.listCalls != 2 {
		t.Fatalf("按页上限截断不对: %+v calls=%d", got, sess.listCalls)
	}
}

// TestProbeClassifiesStages 钉住官方 stage 归属：起不来/握手失败=connect，
// 列举失败=list。这个分类是管理面渲染"卡在哪一步"的唯一依据。
func TestProbeClassifiesStages(t *testing.T) {
	connectCase := &fakeSession{connectErr: errors.New("initialize 被拒"), closeSt: CloseResult{CloseConfirmed: true}}
	got, err := ProbeServer(context.Background(), fakeTarget{sess: connectCase}, ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if got.OK || got.Stage != StageConnect || !strings.Contains(got.Message, "initialize 被拒") {
		t.Fatalf("握手失败必须归到 connect: %+v", got)
	}
	if connectCase.listCalls != 0 {
		t.Fatal("connect 失败不该进列举阶段")
	}

	listCase := &fakeSession{hasTools: true, listErr: errors.New("tools/list 拒绝"), closeSt: CloseResult{CloseConfirmed: true}}
	got, err = ProbeServer(context.Background(), fakeTarget{sess: listCase}, ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if got.OK || got.Stage != StageList || !strings.Contains(got.Message, "tools/list 拒绝") {
		t.Fatalf("列举失败必须归到 list: %+v", got)
	}
}

// TestProbeOpenFailureHasNoTransport 钉住"没有创建 transport 就算确认关闭"：
// 起不来进程时 closeConfirmed 必须是 true，否则管理面会把一次纯配置错误显示成
// "清理未完成、要重启宿主"。
func TestProbeOpenFailureHasNoTransport(t *testing.T) {
	got, err := ProbeServer(context.Background(), fakeTarget{err: errors.New("spawn ENOENT")}, ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if got.OK || got.Stage != StageConnect || !got.Close.CloseConfirmed {
		t.Fatalf("起不来进程的结果不对: %+v", got)
	}
}

// TestProbeCleanupFailureIsReportedIndependently 钉住官方那条独立报告：
// 连接关闭是否确认（closeConfirmed）与本地清理是否成功（cleanupError）互不冒充。
func TestProbeCleanupFailureIsReportedIndependently(t *testing.T) {
	sess := &fakeSession{hasTools: false, closeSt: CloseResult{CloseConfirmed: true, CleanupError: "kill 超时"}}
	got, err := ProbeServer(context.Background(), fakeTarget{sess: sess}, ProbeOptions{})
	if err != nil {
		t.Fatalf("ProbeServer: %v", err)
	}
	if !got.OK || !got.Close.CloseConfirmed || got.Close.CleanupError == "" {
		t.Fatalf("清理失败必须被单独报出来: %+v", got.Close)
	}
}

// TestProbeTimeoutAndCancel 钉住两种中断都归到当前阶段、且都真的清理了连接。
func TestProbeTimeoutAndCancel(t *testing.T) {
	t.Run("预算到期", func(t *testing.T) {
		sess := &fakeSession{hasTools: true, block: true, closeSt: CloseResult{CloseConfirmed: true}}
		started := time.Now()
		got, err := ProbeServer(context.Background(), fakeTarget{sess: sess}, ProbeOptions{TimeoutMs: 80})
		if err != nil {
			t.Fatalf("ProbeServer: %v", err)
		}
		if got.OK || got.Stage != StageList || !strings.Contains(got.Message, "timed out") {
			t.Fatalf("超时结果不对: %+v", got)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatalf("预算没生效，等了 %v", time.Since(started))
		}
		if !sess.closed {
			t.Fatal("超时后必须关掉连接")
		}
		if !got.Close.CloseConfirmed {
			t.Fatalf("清理确认位丢了: %+v", got.Close)
		}
	})

	t.Run("调用方取消", func(t *testing.T) {
		sess := &fakeSession{hasTools: true, block: true, closeSt: CloseResult{CloseConfirmed: true}}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		got, err := ProbeServer(ctx, fakeTarget{sess: sess}, ProbeOptions{})
		if err != nil {
			t.Fatalf("ProbeServer: %v", err)
		}
		if got.OK || got.Stage != StageList || got.Message != probeAbortedMessage {
			t.Fatalf("取消结果不对: %+v", got)
		}
		if !sess.closed {
			t.Fatal("取消后必须关掉连接")
		}
	})

	t.Run("开始前就被取消", func(t *testing.T) {
		sess := &fakeSession{hasTools: true, closeSt: CloseResult{CloseConfirmed: true}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := ProbeServer(ctx, fakeTarget{sess: sess}, ProbeOptions{})
		if err != nil {
			t.Fatalf("ProbeServer: %v", err)
		}
		if got.OK || got.Stage != StageConnect || got.Message != probeAbortedMessage {
			t.Fatalf("取消结果不对: %+v", got)
		}
	})
}

// TestProbeRejectsBadOptions 钉住"选项非法就抛"（官方 probe.ts:47-49 TypeError）：
// 静默夹取默认值会让管理面以为它给的预算生效了。
func TestProbeRejectsBadOptions(t *testing.T) {
	if _, err := ProbeServer(context.Background(), fakeTarget{sess: &fakeSession{}}, ProbeOptions{TimeoutMs: -1}); err == nil {
		t.Fatal("非法 timeoutMs 必须报错")
	}
}
