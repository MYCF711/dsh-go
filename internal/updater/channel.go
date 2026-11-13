// channel.go —— 官方「更新渠道」的**壳侧**状态机（`dsh-client-ui-settings-general`）。
//
// 为什么这是一个独立的包，而不是 settings 命名空间：
//
// 官方原文（`dsh-client-ui-settings-general/src/types.ts:60`）——
//
//	/** UI choices persist in the shell configuration, independently of Host settings. */
//	export interface DesktopChannelBridge { … }
//
// 即**更新渠道不是 Host 设置**，而是**壳配置**。官方的通道数据也不走
// `settings.describe`：它由 Electron 主进程通过隔离的 preload 桥
// （`globalThis.dshDesktop.updates.channels`）暴露给界面。
//
// 本仓是 Go 宿主 + Go GUI，没有 Electron 主进程，所以**由宿主提供同形的壳服务**：
// 同样的三件事（get / set / subscribe）、同样的状态形状、同样的失败分类。
// 用户 2026-11-13 裁定「官方的都要有，关闭的就默认关闭，打开的就默认打开」——
// 这里实现的是官方真实的 channel 语义，不是给界面编一个占位值。
package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Channel 是官方 `DesktopUpdateChannel`（`types.ts:45`）：只有这两个值。
type Channel string

const (
	// ChannelStable 是正式版（官方 `desktop.channel.stable` = 正式版）。
	ChannelStable Channel = "stable"
	// ChannelNightly 是开发版（官方 `desktop.channel.nightly` = 开发版）。
	ChannelNightly Channel = "nightly"
)

// 失败原因，逐字照官方 `DesktopChannelState.error`（`types.ts:52`）。
//
//	error?: 'read' | 'format' | 'channel' | 'cleanup'
const (
	ErrRead    = "read"    // 配置文件读不了
	ErrFormat  = "format"  // 配置文件格式有误
	ErrChannel = "channel" // channel 只能是 nightly 或 stable
	ErrCleanup = "cleanup" // 清理失败
)

// 变更结果的原因，逐字照官方 `DesktopChannelChangeResult`（`types.ts:56-58`）。
const (
	ReasonSave        = "save"
	ReasonFormat      = "format"
	ReasonPermission  = "permission"
	ReasonApply       = "apply"
	ReasonUncertain   = "uncertain"
	ResultApplied     = "applied"
	ResultUnchanged   = "unchanged"
	ResultFailed      = "failed"
	channelConfigName = "update-channel.json"
)

// State 是官方 `DesktopChannelState`（`types.ts:48-53`）。
//
// `Channel` 为 nil 且无 Error = **用户还没选过**（官方 `channel === undefined || null`
// 时界面显示 `desktop.channel.unavailable`「配置不可用」）——本仓照这个语义，
// 不把"没选过"伪装成 stable。
type State struct {
	// Channel 是当前用户选择；nil = 还没选过。
	Channel *Channel `json:"channel"`
	// PackagedChannel 是本构建**自带的**渠道（安装包决定的，用户改不了）。
	PackagedChannel Channel `json:"packagedChannel"`
	// Changing 报告是否有一次切换正在进行。
	Changing bool `json:"changing"`
	// Error 是壳级故障（读不了 / 格式坏 / 值非法 / 清理失败）；空串 = 正常。
	Error string `json:"error,omitempty"`
}

// ChangeResult 是官方 `DesktopChannelChangeResult`。
type ChangeResult struct {
	Kind   string `json:"kind"`
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Service 是更新渠道的壳服务（官方 `DesktopChannelBridge` 的 Go 等价物）。
//
// 三个方法一一对应官方：
//
//	get()       -> State()        当前选择与状态（**不反复读盘**，见 State 的注释）
//	set(c)      -> Set(c)         持久化并应用
//	subscribe() -> Subscribe(fn)  任何窗口的变更都通知
type Service struct {
	mu sync.Mutex
	// path 是壳配置文件（官方：shell configuration，**不是** Host 设置）。
	path string
	// packaged 是本构建自带的渠道。
	packaged Channel
	// state 是缓存真值；State() 只读它，不读盘。
	state State
	// listeners 是订阅者。
	listeners []func(State)
	// loaded 表示已经读过一次盘。
	loaded bool
}

// Options 是 New 的参数。
type Options struct {
	// ConfigPath 是壳配置文件路径；空 = <dir>/update-channel.json。
	Dir string
	// Packaged 是本构建自带的渠道；空 = ChannelStable。
	Packaged Channel
}

// New 造一个渠道服务（**不同步读盘**，第一次 State/Set 时才读）。
func New(opts Options) (*Service, error) {
	packaged := opts.Packaged
	if packaged == "" {
		packaged = ChannelStable
	}
	if !packaged.Valid() {
		return nil, fmt.Errorf("updater: 打包渠道 %q 非法（只能是 stable / nightly）", packaged)
	}
	dir := opts.Dir
	if dir == "" {
		return nil, errors.New("updater: 未给壳配置目录")
	}
	return &Service{
		path:     filepath.Join(dir, channelConfigName),
		packaged: packaged,
		state:    State{PackagedChannel: packaged},
	}, nil
}

// Valid 报告这个值是不是官方认可的两个渠道之一。
func (c Channel) Valid() bool { return c == ChannelStable || c == ChannelNightly }

// channelFile 是壳配置文件的磁盘形状。
//
// 与 Host 设置**分开存**（官方原文：persist in the shell configuration）。
type channelFile struct {
	Channel string `json:"channel"`
}

// State 返回当前渠道状态（官方 `get()`：**不重新读盘**）。
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.snapshotLocked()
}

// Subscribe 注册一个变更监听，返回取消函数（官方 `subscribe` 的等价物）。
func (s *Service) Subscribe(fn func(State)) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	index := len(s.listeners) - 1
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if index < len(s.listeners) {
				s.listeners = append(s.listeners[:index], s.listeners[index+1:]...)
			}
		})
	}
}

// Set 持久化并应用一个渠道选择（官方 `set(channel)`）。
//
// 返回的 `Kind` 三态照官方：
//
//	applied   真的改了并写盘成功
//	unchanged 没变（官方：`state?.channel === channel && !error && !changing` 直接返回）
//	failed    失败，带 `Reason`（save / format / permission / apply / uncertain）
//
// **失败绝不伪装成成功**：官方注释说明「failure never implies a successful switch」。
func (s *Service) Set(next Channel) ChangeResult {
	if !next.Valid() {
		s.mu.Lock()
		s.state.Error = ErrChannel
		state := s.snapshotLocked()
		s.mu.Unlock()
		return ChangeResult{Kind: ResultFailed, State: state, Reason: ReasonFormat}
	}

	s.mu.Lock()
	s.loadLocked()
	current := s.state
	if current.Error == "" && current.Channel != nil && *current.Channel == next {
		// 官方：已是这个值且没在切换 -> 返回 unchanged，不重复写盘。
		state := s.snapshotLocked()
		s.mu.Unlock()
		return ChangeResult{Kind: ResultUnchanged, State: state}
	}
	s.state.Changing = true
	s.notifyLocked()
	s.mu.Unlock()

	result := s.persist(next)

	s.mu.Lock()
	s.state.Changing = false
	change := result
	change.State = s.snapshotLocked()
	s.notifyLocked()
	s.mu.Unlock()
	return change
}

// persist 真正写盘并把结果落进缓存（调用方已置 Changing）。
func (s *Service) persist(next Channel) ChangeResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.Marshal(channelFile{Channel: string(next)})
	if err != nil {
		s.state.Error = ErrFormat
		return ChangeResult{Kind: ResultFailed, Reason: ReasonFormat}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		s.state.Error = ErrFormat
		return ChangeResult{Kind: ResultFailed, Reason: ReasonSave}
	}
	if err := os.WriteFile(s.path, raw, 0o644); err != nil {
		// 官方把"没有写入权限"单独分一档：目录只读时这是最常见的真原因。
		if errors.Is(err, os.ErrPermission) {
			s.state.Error = ErrFormat
			return ChangeResult{Kind: ResultFailed, Reason: ReasonPermission}
		}
		s.state.Error = ErrFormat
		return ChangeResult{Kind: ResultFailed, Reason: ReasonSave}
	}
	value := next
	s.state.Channel = &value
	s.state.Error = ""
	return ChangeResult{Kind: ResultApplied}
}

// loadLocked 读一次壳配置（幂等；只在第一次或显式 Reload 时读盘）。
func (s *Service) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 没配过 = 用户还没选过（官方 `channel === null`，界面显示"配置不可用"）。
			// **不**默认成 packaged：那是自造语义。
			return
		}
		s.state.Error = ErrRead
		return
	}
	var file channelFile
	if err := json.Unmarshal(raw, &file); err != nil {
		s.state.Error = ErrFormat
		return
	}
	value := Channel(file.Channel)
	if file.Channel != "" && !value.Valid() {
		// 官方 `desktop.update.configuration.channel`：
		// 「更新已暂停：channel 只能是 nightly 或 stable」。
		s.state.Error = ErrChannel
		return
	}
	if file.Channel != "" {
		s.state.Channel = &value
	}
}

// snapshotLocked 复制一份可安全外发的状态。
func (s *Service) snapshotLocked() State {
	state := s.state
	if s.state.Channel != nil {
		value := *s.state.Channel
		state.Channel = &value
	}
	return state
}

// notifyLocked 通知所有订阅者（持锁调用；回调里不许再调本服务）。
func (s *Service) notifyLocked() {
	if len(s.listeners) == 0 {
		return
	}
	state := s.snapshotLocked()
	listeners := append([]func(State){}, s.listeners...)
	go func() {
		for _, fn := range listeners {
			fn(state)
		}
	}()
}

// 下面三个方法是 transport.UpdateChannelService 的适配层。
//
// 为什么用 map[string]any 而不是直接返回 State：handler 层只做 wire 序列化，
// 让它依赖 wire 形状（map）比依赖本包的具体类型更松——换实现不用改 handler。

// WireSet 是官方 `DesktopChannelChangeResult` 的线上形状。
func (s *Service) WireSet(channel string) map[string]any {
	result := s.Set(Channel(channel))
	out := map[string]any{
		"kind":  result.Kind,
		"state": stateWire(result.State),
	}
	if result.Reason != "" {
		out["reason"] = result.Reason
	}
	return out
}

// WireSubscribe 把订阅转成 map 形状（取消函数原样交出）。
func (s *Service) WireSubscribe(fn func(map[string]any)) func() {
	if fn == nil {
		return func() {}
	}
	return s.Subscribe(func(state State) { fn(stateWire(state)) })
}

// stateWire 把 State 转成线上形状。
func stateWire(state State) map[string]any {
	out := map[string]any{
		"packagedChannel": string(state.PackagedChannel),
		"changing":        state.Changing,
	}
	if state.Channel != nil {
		out["channel"] = string(*state.Channel)
	} else {
		out["channel"] = nil
	}
	if state.Error != "" {
		out["error"] = state.Error
	}
	return out
}

// StateView 是 `State()` 的 map 版本（供壳服务面直接用）。
//
// 名字与 State 分开是为了让本包自己的测试可以拿强类型 State，
// 而壳层拿 wire 形状——两者不互相污染。
func (s *Service) StateView() map[string]any { return stateWire(s.State()) }
