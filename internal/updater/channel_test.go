package updater_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dshgo/internal/updater"
)

// newService 造一个指向临时目录的渠道服务。
func newService(t *testing.T, packaged updater.Channel) *updater.Service {
	t.Helper()
	svc, err := updater.New(updater.Options{Dir: t.TempDir(), Packaged: packaged})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

// 官方 `channel === null` 语义：没配过 = 用户还没选过，**不**默认成 packaged。
//
// 官方 DesktopChannelRow 在 channel 为空时显示 `desktop.channel.unavailable`
// （「配置不可用」）。若本仓默认成 stable，界面会显示"正式版"——那是**自造事实**。
func TestFreshHomeHasNoSelection(t *testing.T) {
	svc := newService(t, updater.ChannelStable)
	state := svc.State()
	if state.Channel != nil {
		t.Fatalf("全新 home 不该有选择（官方 channel === null），实得 %v", *state.Channel)
	}
	if state.PackagedChannel != updater.ChannelStable {
		t.Fatalf("packagedChannel 必须是构建自带的 stable，实得 %q", state.PackagedChannel)
	}
	if state.Error != "" {
		t.Fatalf("没配过不是错误，实得 error=%q", state.Error)
	}
}

// 首次设值 -> applied 且落盘；重开后读回同一个值（持久化真的生效）。
func TestSetPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	first, err := updater.New(updater.Options{Dir: dir, Packaged: updater.ChannelStable})
	if err != nil {
		t.Fatal(err)
	}
	result := first.Set(updater.ChannelNightly)
	if result.Kind != updater.ResultApplied {
		t.Fatalf("首次设置应当是 applied，实得 %s（reason=%s）", result.Kind, result.Reason)
	}
	if result.State.Channel == nil || *result.State.Channel != updater.ChannelNightly {
		t.Fatalf("设置后状态应当是 nightly：%+v", result.State)
	}

	// 重开（模拟重启）：必须读回 nightly，不是回默认。
	second, err := updater.New(updater.Options{Dir: dir, Packaged: updater.ChannelStable})
	if err != nil {
		t.Fatal(err)
	}
	state := second.State()
	if state.Channel == nil || *state.Channel != updater.ChannelNightly {
		t.Fatalf("重启后必须读回 nightly，实得 %+v", state)
	}
}

// 设成同一个值 -> unchanged，且**不重复写盘**（官方：已相等且没在切换就直接返回）。
func TestSetSameValueIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	svc, err := updater.New(updater.Options{Dir: dir, Packaged: updater.ChannelStable})
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.Set(updater.ChannelStable); got.Kind != updater.ResultApplied {
		t.Fatalf("首次设置应当 applied：%s", got.Kind)
	}
	path := filepath.Join(dir, "update-channel.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	got := svc.Set(updater.ChannelStable)
	if got.Kind != updater.ResultUnchanged {
		t.Fatalf("同值设置应当是 unchanged，实得 %s", got.Kind)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("unchanged 不该重写配置文件")
	}
}

// 非法值 -> failed + format，且**不覆盖**已有选择（失败不伪装成功）。
func TestSetRejectsIllegalChannelWithoutTouchingDisk(t *testing.T) {
	dir := t.TempDir()
	svc, err := updater.New(updater.Options{Dir: dir, Packaged: updater.ChannelStable})
	if err != nil {
		t.Fatal(err)
	}
	svc.Set(updater.ChannelNightly)

	got := svc.Set(updater.Channel("beta"))
	if got.Kind != updater.ResultFailed {
		t.Fatalf("非法值必须 failed，实得 %s", got.Kind)
	}
	if got.Reason != updater.ReasonFormat {
		t.Fatalf("非法值的 reason 应当是 format，实得 %q", got.Reason)
	}
	if got.State.Channel == nil || *got.State.Channel != updater.ChannelNightly {
		t.Fatalf("失败后不得改动已有选择（仍是 nightly）：%+v", got.State)
	}
	if got.State.Error != updater.ErrChannel {
		t.Fatalf("失败后 error 应当是 channel，实得 %q", got.State.Error)
	}
}

// 配置文件坏掉 -> error=format，且**响亮**上报（不静默退回默认）。
func TestCorruptConfigReportsFormatError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "update-channel.json"), []byte("{不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := updater.New(updater.Options{Dir: dir, Packaged: updater.ChannelStable})
	if err != nil {
		t.Fatal(err)
	}
	state := svc.State()
	if state.Error != updater.ErrFormat {
		t.Fatalf("坏文件应当报 format，实得 %q", state.Error)
	}
	if state.Channel != nil {
		t.Fatalf("坏文件时不该读出一个值：%v", *state.Channel)
	}
}

// 文件里写了官方不认的值 -> error=channel（官方 `desktop.update.configuration.channel`：
// 「更新已暂停：channel 只能是 nightly 或 stable」）。
func TestUnknownChannelInFileReportsChannelError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "update-channel.json"), []byte(`{"channel":"beta"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := updater.New(updater.Options{Dir: dir, Packaged: updater.ChannelStable})
	if err != nil {
		t.Fatal(err)
	}
	if state := svc.State(); state.Error != updater.ErrChannel {
		t.Fatalf("不认的 channel 应当报 channel 错误，实得 %q", state.Error)
	}
}

// 订阅：变更后必须收到通知（官方 subscribe 的等价物）。
func TestSubscribeReceivesChanges(t *testing.T) {
	svc := newService(t, updater.ChannelStable)
	got := make(chan updater.State, 4)
	unsubscribe := svc.Subscribe(func(state updater.State) { got <- state })
	defer unsubscribe()

	svc.Set(updater.ChannelNightly)

	// 一次切换会发**两条**通知，顺序是官方的两段式：
	//   1. `changing: true`  —— 开始切换（官方 `state.changing`），此时 channel 还没变；
	//   2. `changing: false` —— 收口，channel 已经是新值。
	// 只断言"收到了"是不够的：必须断言**收口那条**带着新值，否则界面会停在转圈态。
	var states []updater.State
	deadline := time.After(2 * time.Second)
	for len(states) < 2 {
		select {
		case state := <-got:
			states = append(states, state)
		case <-deadline:
			t.Fatalf("设置变更后必须通知订阅者（已收到 %d 条）：%+v", len(states), states)
		}
	}
	if !states[0].Changing {
		t.Fatalf("第一条通知应当是 changing=true（开始切换）：%+v", states[0])
	}
	final := states[1]
	if final.Changing {
		t.Fatalf("第二条通知应当是收口（changing=false）：%+v", final)
	}
	if final.Channel == nil || *final.Channel != updater.ChannelNightly {
		t.Fatalf("收口那条的状态应当是 nightly：%+v", final)
	}
	if final.Error != "" {
		t.Fatalf("成功后不该带错误：%q", final.Error)
	}
}

// 取消订阅后不再收到通知。
func TestUnsubscribeStopsNotifications(t *testing.T) {
	svc := newService(t, updater.ChannelStable)
	var mu sync.Mutex
	var count int
	unsubscribe := svc.Subscribe(func(updater.State) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	unsubscribe()
	svc.Set(updater.ChannelNightly)
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Fatalf("取消订阅后不该再收到通知，实际收到 %d 次", count)
	}
}

// 构造期校验：打包渠道非法直接报错（fail-loud，不静默降级成 stable）。
func TestNewRejectsIllegalPackagedChannel(t *testing.T) {
	if _, err := updater.New(updater.Options{Dir: t.TempDir(), Packaged: updater.Channel("beta")}); err == nil {
		t.Fatal("非法的打包渠道必须构造期报错")
	}
	if _, err := updater.New(updater.Options{}); err == nil {
		t.Fatal("没给目录必须构造期报错")
	}
}
