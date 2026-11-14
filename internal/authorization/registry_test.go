package authorization

// 官方 #23 `authorization` 的验收。
//
// 判据是官方的**终态契约**，不是"注册表里多了一条记录"：
//
//   - begin 成功 = `authorized`，且**只有**在本次尝试真的提交过才成立；
//   - 人拒绝 / 调用方撤销 = `cancelled`（不是错误）；
//   - 其它失败 = 错误（不是 outcome）；
//   - notice / prompt 只到发起方那个 interaction；
//   - 四个官方错误码：NO_FLOW / ALREADY_IN_FLIGHT / NOT_COMMITTED / UNKNOWN_METHOD；
//   - 业务代码可以"cancel 之后重启下一次"（key 在结算前就释放了）。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// surface 是一个测试用的 interaction，记录它收到的每一条通知/提示。
type surface struct {
	mu      sync.Mutex
	notices []Notice
	prompts []Prompt
	// answers 是每个 prompt 的答复；对应位置为空 = 人拒绝。
	answers []string
	// notifyErr / askErr 用来让 surface 自己失败。
	notifyErr error
	askErr    error
}

func (s *surface) Notify(_ context.Context, notice Notice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notifyErr != nil {
		return s.notifyErr
	}
	s.notices = append(s.notices, notice)
	return nil
}

func (s *surface) Ask(_ context.Context, prompt Prompt) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.askErr != nil {
		return "", s.askErr
	}
	s.prompts = append(s.prompts, prompt)
	idx := len(s.prompts) - 1
	if idx >= len(s.answers) || s.answers[idx] == "" {
		return "", ErrDeclined
	}
	return s.answers[idx], nil
}

// 正常路径：notice 到发起方、prompt 拿到答案、Commit 之后才是 authorized。
func TestBeginAuthorizedOnlyAfterCommit(t *testing.T) {
	reg := NewRegistry()
	acme := &surface{answers: []string{"123456"}}
	var (
		gotNotice  Notice
		gotPrompt  Prompt
		commitSeen bool
	)
	unregister, err := reg.RegisterFlow(Flow{
		Key:     "acme/thing",
		Label:   "Acme 账号",
		Methods: []Method{{ID: "code", Label: "输入验证码"}, {ID: "sso", Label: "单点登录"}},
		Run: func(ctx context.Context, session *Surface) error {
			if session.Method().ID != "code" {
				t.Errorf("没点名 method 时应当选第一个（code），实际 %s", session.Method().ID)
			}
			if err := session.Notify(Notice{Message: "去页面输入验证码", URL: "https://acme/start", Code: "AB-12"}); err != nil {
				return err
			}
			// 从"发起方"那里读回来，证明 notice 真的到了它。
			acme.mu.Lock()
			gotNotice = acme.notices[len(acme.notices)-1]
			acme.mu.Unlock()
			answer, err := session.Prompt(Prompt{Kind: PromptSecret, Message: "输入验证码"})
			if err != nil {
				return err
			}
			acme.mu.Lock()
			gotPrompt = acme.prompts[len(acme.prompts)-1]
			acme.mu.Unlock()
			if answer != "123456" {
				t.Errorf("答案应当是 123456，实际 %q", answer)
			}
			commitSeen = true
			return session.Commit()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unregister() }()

	// entry 的投影要先对：label / methods / inFlight=false。
	entry, ok := reg.Describe("acme/thing")
	if !ok || entry.Label != "Acme 账号" || len(entry.Methods) != 2 || entry.InFlight {
		t.Fatalf("Describe 投影不对：%+v ok=%v", entry, ok)
	}

	out, err := reg.Begin(context.Background(), "acme/thing", "", acme)
	if err != nil {
		t.Fatalf("Begin 失败：%v", err)
	}
	if out.Status != StatusAuthorized {
		t.Fatalf("终态应当是 authorized，实际 %s", out.Status)
	}
	if !commitSeen {
		t.Fatal("flow 根本没跑")
	}
	if gotNotice.Code != "AB-12" || gotNotice.URL != "https://acme/start" {
		t.Fatalf("notice 内容丢了：%+v", gotNotice)
	}
	if gotPrompt.Kind != PromptSecret {
		t.Fatalf("prompt kind 应当是 secret，实际 %s", gotPrompt.Kind)
	}
	if reg.InFlight("acme/thing") {
		t.Fatal("结算之后不该还在飞（key 必须在结算前释放）")
	}
	if entries := reg.List(); len(entries) != 1 || entries[0].InFlight {
		t.Fatalf("List 投影不对：%+v", entries)
	}
}

// NOT_COMMITTED：flow 正常返回却没提交 → 不是 authorized。
func TestBeginWithoutCommitIsNotCommitted(t *testing.T) {
	reg := NewRegistry()
	if _, err := reg.RegisterFlow(Flow{
		Key:     "acme/nocommit",
		Label:   "Acme",
		Methods: []Method{{ID: "code", Label: "code"}},
		Run:     func(ctx context.Context, session *Surface) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Begin(context.Background(), "acme/nocommit", "", &surface{})
	if CodeOf(err) != ErrCodeNotCommitted {
		t.Fatalf("应当回 NOT_COMMITTED，实际 %v（code=%q）", err, CodeOf(err))
	}
}

// 人拒绝是 cancelled，不是错误。
func TestBeginDeclinedIsCancelled(t *testing.T) {
	reg := NewRegistry()
	if _, err := reg.RegisterFlow(Flow{
		Key:     "acme/declined",
		Label:   "Acme",
		Methods: []Method{{ID: "code", Label: "code"}},
		Run: func(ctx context.Context, session *Surface) error {
			if _, err := session.Prompt(Prompt{Kind: PromptText, Message: "code?"}); err != nil {
				return err
			}
			return session.Commit()
		},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Begin(context.Background(), "acme/declined", "", &surface{}) // 没有答案 = 人拒绝
	if err != nil {
		t.Fatalf("拒绝不该是错误：%v", err)
	}
	if out.Status != StatusCancelled {
		t.Fatalf("终态应当是 cancelled，实际 %s", out.Status)
	}
}

// 其它 flow 失败是**错误**，不是 outcome（官方："failures remain errors"）。
func TestBeginFlowFailureIsError(t *testing.T) {
	reg := NewRegistry()
	boom := errors.New("网络断了")
	if _, err := reg.RegisterFlow(Flow{
		Key:     "acme/fail",
		Label:   "Acme",
		Methods: []Method{{ID: "code", Label: "code"}},
		Run:     func(ctx context.Context, session *Surface) error { return boom },
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Begin(context.Background(), "acme/fail", "", &surface{}); !errors.Is(err, boom) {
		t.Fatalf("flow 失败必须原样传到调用方，实际 %v", err)
	}
}

// 调用方撤销 = cancelled（即便 flow 不理会 signal 也不会卡住）。
func TestBeginCancelIsCancelled(t *testing.T) {
	reg := NewRegistry()
	if _, err := reg.RegisterFlow(Flow{
		Key:     "acme/cancel",
		Label:   "Acme",
		Methods: []Method{{ID: "code", Label: "code"}},
		Run: func(ctx context.Context, session *Surface) error {
			<-ctx.Done() // 理会 signal
			return ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	out, err := reg.Begin(ctx, "acme/cancel", "", &surface{})
	if err != nil {
		t.Fatalf("撤销不该是错误：%v", err)
	}
	if out.Status != StatusCancelled {
		t.Fatalf("撤销的终态应当是 cancelled，实际 %s", out.Status)
	}
}

// Cancel(key) 能让第二次调用撤掉第一次（HTTP Cancel 按钮的形状）。
func TestCancelFromSecondCall(t *testing.T) {
	reg := NewRegistry()
	started := make(chan struct{})
	if _, err := reg.RegisterFlow(Flow{
		Key:     "acme/second",
		Label:   "Acme",
		Methods: []Method{{ID: "code", Label: "code"}},
		Run: func(ctx context.Context, session *Surface) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	go func() {
		<-started
		if reg.InFlight("acme/second") {
			reg.Cancel("acme/second")
		}
	}()
	out, err := reg.Begin(context.Background(), "acme/second", "", &surface{})
	if err != nil {
		t.Fatalf("Cancel 之后不该是错误：%v", err)
	}
	if out.Status != StatusCancelled {
		t.Fatalf("Cancel 之后终态应当是 cancelled，实际 %s", out.Status)
	}
	if reg.Cancel("acme/second") {
		t.Fatal("已经没有在跑的尝试，Cancel 应当返回 false")
	}
}

// 官方错误码：NO_FLOW / ALREADY_IN_FLIGHT / UNKNOWN_METHOD。
func TestOfficialErrorCodes(t *testing.T) {
	reg := NewRegistry()
	block := make(chan struct{})
	release := make(chan struct{})
	if _, err := reg.RegisterFlow(Flow{
		Key:     "acme/codes",
		Label:   "Acme",
		Methods: []Method{{ID: "code", Label: "code"}},
		Run: func(ctx context.Context, session *Surface) error {
			close(block)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return session.Commit()
		},
	}); err != nil {
		t.Fatal(err)
	}

	// NO_FLOW
	if _, err := reg.Begin(context.Background(), "acme/nobody", "", &surface{}); CodeOf(err) != ErrCodeNoFlow {
		t.Fatalf("没有 flow 应当回 NO_FLOW，实际 %q（%v）", CodeOf(err), err)
	}
	// UNKNOWN_METHOD
	if _, err := reg.Begin(context.Background(), "acme/codes", "sso", &surface{}); CodeOf(err) != ErrCodeUnknownMethod {
		t.Fatalf("点名没有的 method 应当回 UNKNOWN_METHOD，实际 %q", CodeOf(err))
	}

	// ALREADY_IN_FLIGHT：第一次还挂着，第二次必须被拒。
	done := make(chan Outcome, 1)
	go func() {
		out, err := reg.Begin(context.Background(), "acme/codes", "", &surface{})
		if err != nil {
			t.Errorf("第一次 Begin 失败：%v", err)
		}
		done <- out
	}()
	<-block
	if _, err := reg.Begin(context.Background(), "acme/codes", "", &surface{}); CodeOf(err) != ErrCodeAlreadyInFlight {
		t.Fatalf("第二次 Begin 应当回 ALREADY_IN_FLIGHT，实际 %q（%v）", CodeOf(err), err)
	}
	entry, _ := reg.Describe("acme/codes")
	if !entry.InFlight {
		t.Fatal("在飞的时候 Describe 的 inFlight 必须是 true（UI 靠它禁按钮）")
	}
	close(release)
	if out := <-done; out.Status != StatusAuthorized {
		t.Fatalf("放行之后应当是 authorized，实际 %s", out.Status)
	}
	if reg.InFlight("acme/codes") {
		t.Fatal("结算之后 key 必须已经释放")
	}
}

// 注册期的 fail-loud：key 形状、label、methods、重复 key。
func TestRegisterFlowValidation(t *testing.T) {
	reg := NewRegistry()
	cases := []Flow{
		{Key: "badkey", Label: "x", Methods: []Method{{ID: "a", Label: "a"}}, Run: func(context.Context, *Surface) error { return nil }},
		{Key: "acme/", Label: "x", Methods: []Method{{ID: "a", Label: "a"}}, Run: func(context.Context, *Surface) error { return nil }},
		{Key: "acme/ok", Label: "", Methods: []Method{{ID: "a", Label: "a"}}, Run: func(context.Context, *Surface) error { return nil }},
		{Key: "acme/ok", Label: "x", Methods: nil, Run: func(context.Context, *Surface) error { return nil }},
		{Key: "acme/ok", Label: "x", Methods: []Method{{ID: "", Label: "a"}}, Run: func(context.Context, *Surface) error { return nil }},
		{Key: "acme/ok", Label: "x", Methods: []Method{{ID: "a", Label: "a"}, {ID: "a", Label: "b"}}, Run: func(context.Context, *Surface) error { return nil }},
		{Key: "acme/ok", Label: "x", Methods: []Method{{ID: "a", Label: "a"}}},
	}
	for index, flow := range cases {
		if _, err := reg.RegisterFlow(flow); err == nil {
			t.Fatalf("第 %d 个非法 flow 被接受了：%+v", index+1, flow)
		}
	}
	good := Flow{Key: "acme/ok", Label: "x", Methods: []Method{{ID: "a", Label: "a"}}, Run: func(context.Context, *Surface) error { return nil }}
	if _, err := reg.RegisterFlow(good); err != nil {
		t.Fatalf("合法 flow 被拒：%v", err)
	}
	if _, err := reg.RegisterFlow(good); err == nil {
		t.Fatal("同一个 key 注册两次必须拒绝（一条记录只能有一个写者）")
	}
}

// 反注册：幂等；还有在飞的尝试时必须拒绝。
func TestUnregisterRefusesWhileInFlight(t *testing.T) {
	reg := NewRegistry()
	block := make(chan struct{})
	unregister, err := reg.RegisterFlow(Flow{
		Key:     "acme/unreg",
		Label:   "Acme",
		Methods: []Method{{ID: "code", Label: "code"}},
		Run: func(ctx context.Context, session *Surface) error {
			close(block)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = reg.Begin(context.Background(), "acme/unreg", "", &surface{})
	}()
	<-block
	if err := unregister(); CodeOf(err) != ErrCodeAlreadyInFlight {
		t.Fatalf("还有在飞的尝试时反注册必须拒绝，实际 %v", err)
	}
	reg.Cancel("acme/unreg")
	<-done
	if err := unregister(); err != nil {
		t.Fatalf("结算之后反注册应当成功：%v", err)
	}
	if err := unregister(); err != nil {
		t.Fatalf("反注册必须幂等：%v", err)
	}
	if _, ok := reg.Describe("acme/unreg"); ok {
		t.Fatal("反注册之后 Describe 应当找不到")
	}
	if keys := reg.Keys(); len(keys) != 0 {
		t.Fatalf("Keys 应当为空，实际 %v", keys)
	}
}
