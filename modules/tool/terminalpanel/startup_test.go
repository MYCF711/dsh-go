package terminalpanel

// 启动等待的判据用例：**"打印过"看的是可见文本**，不是原始字节。
//
// 为什么这条值得单独钉：官方 `startup` 的 `printed` 判据（`terminal-panel/src/backend.ts:38-41`）
// 走的是 xterm 缓冲区重放后的逻辑行——模式序列/清屏渲染成空文本。本宿主没有屏幕模型，
// 读的是原始字节尾部，而 ConPTY 一启动就会吐 `\x1b[?9001h…`。把那段字节当成"已经打印"，
// startup 就会在 shell 起来之前返回；模型随后 terminal_send 的输入会被丢掉
// （2026-11-18 整链用例在满负载下实测到过一次，表现是 read 里只有模式序列）。
//
// 2026-11-18 修复：`readStartup` 用 `stripTerminalEscapes` 判可见文本。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dshgo/internal/sandbox"
	"dshgo/internal/terminal"
)

// TestStripTerminalEscapes 钉住"只留可见字节"的四种形态：CSI、OSC（BEL 与 ST 两种结束）、
// 两字节转义、以及没有 ESC 的纯文本。
func TestStripTerminalEscapes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"无转义", "plain text", "plain text"},
		{"CSI 私有模式（ConPTY 启动序列）", "\x1b[?9001h\x1b[?1004h", ""},
		{"CSI 颜色 + 文本", "\x1b[32m$\x1b[m ", "$ "},
		{"清屏 + 光标归位", "\x1b[2J\x1b[H$ ", "$ "},
		{"OSC 标题（BEL 结束）", "\x1b]0;MINGW64:/tmp\a$ ", "$ "},
		{"OSC 标题（ST 结束）", "\x1b]0;title\x1b\\$ ", "$ "},
		{"两字节转义", "\x1b(B$ ", "$ "},
		{"末尾截断的 ESC", "abc\x1b", "abc"},
		{"嵌套", "\x1b[1m\x1b[32mok\x1b[m\x1b]0;t\a done", "ok done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripTerminalEscapes(tc.in); got != tc.want {
				t.Fatalf("stripTerminalEscapes(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// timedScreenOpener 是按时间推进的假终端：`promptAfter` 之前只有模式序列，之后才有提示符。
type timedScreenOpener struct {
	fakeOpener
	promptAfter time.Duration
	started     time.Time
}

func (o *timedScreenOpener) ReadUserScreen(context.Context, string, string, int) (terminal.ScreenPage, error) {
	const modes = "\x1b[?9001h\x1b[?1004h"
	if time.Since(o.started) < o.promptAfter {
		return terminal.ScreenPage{Text: modes, TotalLines: 1, LineBegin: 1, LineEnd: 1}, nil
	}
	return terminal.ScreenPage{Text: modes + "\x1b[32m$ \x1b[m", TotalLines: 2, LineBegin: 1, LineEnd: 2}, nil
}

func (o *timedScreenOpener) OpenAgentTerminal(context.Context, string, terminal.AgentOpenRequest) (terminal.PanelTerminal, error) {
	return agentInfo(), nil
}

// TestReadStartupWaitsForVisibleText 钉住"只有模式序列时不算就绪"：
// 前两次读屏只有 ConPTY 的模式序列，第三次才是提示符；`readStartup` 必须等到
// 第三次之后才返回，且 motd 里必须带提示符。
//
// 没有这条修复时，实现会在第一屏 + silence 之后立刻返回，motd 只有 `\x1b[?9001h…`。
func TestReadStartupWaitsForVisibleText(t *testing.T) {
	t.Setenv(sandbox.EnvMode, "danger-full-access")
	t.Setenv(sandbox.EnvWorkspaceRoot, `C:\ws`)
	// 按**时间**推进（而不是"读几次"）：前 80ms 只吐 ConPTY 的模式序列，之后才是提示符。
	// 用逐帧列表的假数据源区分不出"提前返回"——轮询很快就把提示符读到了。
	opener := &timedScreenOpener{
		fakeOpener:  fakeOpener{origin: terminal.PanelOriginUser},
		promptAfter: 80 * time.Millisecond,
		started:     time.Now(),
	}
	p := &panel{
		cfg:            DefaultConfig(),
		svc:            opener,
		startupSilence: 20 * time.Millisecond,
		startupTimeout: 2 * time.Second,
		startupPoll:    time.Millisecond,
	}
	res, err := p.openTool().Exec(sessionCtx("s1"), json.RawMessage(`{"type":"shell"}`))
	if err != nil {
		t.Fatalf("terminal_open 失败: %v（%+v）", err, res)
	}
	if !strings.Contains(res.Text, "$ ") {
		t.Fatalf("startup 在只有模式序列时就返回了（shell 还没就绪）: %q", res.Text)
	}
}
