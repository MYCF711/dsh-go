package transport

// rc.2 `dsh-terminal-panel` 的两个**只读工具**用到的唯一宿主适配层。
//
// # 这不是方法面
//
// 官方把 `user_terminal_list` / `user_terminal_read` 定义在
// `@deepseek-ai/dsh-terminal-panel` 的 `ctx.tools` 上（`terminal-panel/src/tools.ts:74`、
// `:144`），**没有**同名 wire 方法：rc.2 的 `terminal/*` 方法集合仍是
// create / attach / list / watch / close / environment / shells / rename / retain
// （`tmp/GO8-RC2/contracts-rc2.json`；本仓 `internal/transport/handlers_job_terminal_official.go:91`
// 的 Method 常量表同形）。所以本文件**不加路由、不加方法名、不进 `Handles`**。
//
// 它做的是另一件事：把 `OfficialJobTerminalService`（真 roster）翻译成工具层要的
// `terminal.UserTerminals`。这样"侧栏看到的终端"和"模型读到的终端"是**同一份**
// 数据——`control` 字段直接来自 `WebTerminalInfo.control`，不另建状态机。
//
// # 与官方读屏的差距（如实记录，不假装）
//
//   - 官方 `readScreen` 走终端控制器的**屏幕模型**（重放光标/清屏序列后的渲染结果，
//     `dsh-api-terminal-controller` 的 `TerminalScreenPage`）。本宿主没有屏幕模型，
//     `terminal.attach` 的 `screen` 是有界滚动缓冲的尾部字节，所以这里按换行切行
//     （见 `terminal.NewestLinesPage` 的注释）。
//   - 官方 `running` 来自 `inspectActivity`（前台是否有进程在跑，缺陷时给 unknown）。
//     本宿主没有前台进程探针：**运行中的终端一律如实回 `unknown`**，非 running 状态
//     回 `no`。给 `yes` 就是编的。

import (
	"context"
	"errors"

	"dshgo/internal/terminal"
)

// UserTerminalRoster 是这条适配器唯一需要的终端服务子集。
//
// 抽成窄接口（而不是直接吃 `OfficialJobTerminalService` 的 11 个方法）的理由：
// 测试要能只造两个方法；而 `*OfficialJobTerminal` 天然满足它，接线处不用转换。
type UserTerminalRoster interface {
	// TerminalList 与 `terminal.list` / `terminal.watch` 共用同一份 roster。
	TerminalList(sessionID string) ([]OfficialWebTerminalInfo, error)
	// TerminalAttach 回一帧真实快照（`screen` 是有界滚动缓冲尾部）。
	TerminalAttach(ctx context.Context, sessionID, id string) (OfficialTerminalSnapshot, error)
}

// UserTerminalSource 把终端服务适配成 `terminal.UserTerminals`。
type UserTerminalSource struct {
	roster UserTerminalRoster
	origin func(ctx context.Context, sessionID string) (string, error)
}

// NewUserTerminalSource 造一个数据源。
//
// origin 必须由宿主提供：官方对子代理的拒绝判据是
// `agent.session.header.origin === 'subagent'`（`terminal-panel/src/tools.ts:35`），
// 而本宿主的会话头没有 origin 字段（子代理身份的事实来源是子会话日志的
// `subagent/spawn` 帧，见 `internal/agent.FindChild`）。所以"这条会话是不是子代理"
// 只能由知道会话谱系的那一层回答，不在本包猜。
//
// 传 nil 不会被当成"恒为主会话"静默放过：`SessionOrigin` 会返回明确错误，
// 让工具给出结构化失败（宁可不说话，也不假装子代理可以读用户的终端）。
func NewUserTerminalSource(roster UserTerminalRoster, origin func(ctx context.Context, sessionID string) (string, error)) *UserTerminalSource {
	return &UserTerminalSource{roster: roster, origin: origin}
}

// 编译期钉住：适配器必须满足工具层的接口。
var _ terminal.UserTerminals = (*UserTerminalSource)(nil)

// ListUserTerminals 列这条会话的全部终端，字段换算见文件头。
func (s *UserTerminalSource) ListUserTerminals(ctx context.Context, sessionID string) ([]terminal.PanelTerminal, error) {
	if s == nil || s.roster == nil {
		return nil, errors.New("终端数据源没有 roster（接线缺失）")
	}
	infos, err := s.roster.TerminalList(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]terminal.PanelTerminal, 0, len(infos))
	for _, info := range infos {
		// 与 wire 边界同一套 rc.2 补齐规则（origin/control 的默认值只有一个出处，
		// 见 `OfficialWebTerminalInfo.aligned`）。
		info = info.aligned()
		out = append(out, terminal.PanelTerminal{
			ID:       info.ID,
			Title:    info.Title,
			Cwd:      info.Cwd,
			Control:  info.Control,
			State:    info.State,
			Running:  panelRunning(info.State),
			ExitCode: info.ExitCode,
		})
	}
	return out, nil
}

// ReadUserScreen 读一条终端的渲染屏幕最新 lines 行。
func (s *UserTerminalSource) ReadUserScreen(ctx context.Context, sessionID, id string, lines int) (terminal.ScreenPage, error) {
	if s == nil || s.roster == nil {
		return terminal.ScreenPage{}, errors.New("终端数据源没有 roster（接线缺失）")
	}
	snapshot, err := s.roster.TerminalAttach(ctx, sessionID, id)
	if err != nil {
		return terminal.ScreenPage{}, err
	}
	// 字节上限由工具层按 maxReadBytes 施加（配置项属于工具包）；这里只做行分页。
	return terminal.NewestLinesPage(snapshot.Screen, lines, 0), nil
}

// SessionOrigin 回答"这条会话是不是子代理会话"。
func (s *UserTerminalSource) SessionOrigin(ctx context.Context, sessionID string) (string, error) {
	if s == nil || s.origin == nil {
		return "", errors.New("宿主没有提供会话来源判定（子代理拒绝需要它）：请把会话谱系（internal/agent.FindChild）接进 NewUserTerminalSource")
	}
	return s.origin(ctx, sessionID)
}

// panelRunning 把官方 `WebTerminalInfo.state` 投影成工具面的 `running` 三态。
//
// 官方映射（`terminal-panel/src/tools.ts:75-83`）：
//
//	state !== 'running'            → 'no'
//	state === 'running' + busy     → 'yes'
//	state === 'running' + idle     → 'no'
//	state === 'running' + 观测失败  → 'unknown'
//
// 本宿主没有 `inspectActivity`（前台进程探针）：观测不到就如实 'unknown'，
// 这一档正是官方为"观测不可用"准备的值。
func panelRunning(state string) string {
	if state != "running" {
		return terminal.PanelRunningNo
	}
	return terminal.PanelRunningUnknown
}
