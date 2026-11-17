package terminal

// agent 终端交互契约的判据用例（`internal/terminal/agenttools.go`）。
//
// 权威：官方 `pageLines`（`dsh-api-terminal-controller/lib/index.js:194-220`）与
// TS 定义（`src/types.ts:83-93`、`src/screen.ts:86-97`）。这里的期望值都是照着那段
// JS 逐行推出来的，不是"跑一遍看结果"抄的。

import (
	"testing"
)

// TestAgentScreenPageMatchesOfficialPageLines 钉住官方 `pageLines` 的六条语义：
// newest-relative 分页、行号口径、字节预算从新到旧、超长单行只留尾部、空屏幕、CRLF。
func TestAgentScreenPageMatchesOfficialPageLines(t *testing.T) {
	cases := []struct {
		name      string
		screen    string
		offset    int
		count     int
		maxBytes  int
		wantText  string
		wantBegin int
		wantEnd   int
		wantTotal int
		wantTrunc bool
	}{
		{
			name: "最新两行，offset 0", screen: "a\nb\nc\nd\ne",
			offset: 0, count: 2, maxBytes: 1024,
			wantText: "d\ne", wantBegin: 0, wantEnd: 2, wantTotal: 5,
		},
		{
			// offset 从最新一行往回数：offset=1 时 end=4，取 [2,4) = c,d。
			name: "offset 1 / count 2", screen: "a\nb\nc\nd\ne",
			offset: 1, count: 2, maxBytes: 1024,
			wantText: "c\nd", wantBegin: 1, wantEnd: 3, wantTotal: 5,
		},
		{
			// offset 越过屏幕：官方不回退成"最后一页"，而是空页 + 原样回 offset。
			name: "offset 越过末尾", screen: "a\nb\nc",
			offset: 10, count: 2, maxBytes: 1024,
			wantText: "", wantBegin: 10, wantEnd: 10, wantTotal: 3,
		},
		{
			// 字节预算从新到旧累加（行间换行算 1 字节）：cccc(4) 之后放不下 bbbb+1。
			name: "字节上限先淘汰更旧的行", screen: "aaaa\nbbbb\ncccc",
			offset: 0, count: 3, maxBytes: 5,
			wantText: "cccc", wantBegin: 0, wantEnd: 1, wantTotal: 3, wantTrunc: true,
		},
		{
			// 最新那一行自己就超上限：留它的尾部字节（官方 tailWithin），并置 truncated。
			name: "单行超上限只留尾部", screen: "0123456789",
			offset: 0, count: 1, maxBytes: 4,
			wantText: "6789", wantBegin: 0, wantEnd: 1, wantTotal: 1, wantTrunc: true,
		},
		{
			name: "空屏幕", screen: "",
			offset: 0, count: 500, maxBytes: 1024,
			wantText: "", wantBegin: 0, wantEnd: 0, wantTotal: 0,
		},
		{
			// PTY 的换行是 CRLF：切行前必须先归一，否则每行尾部会带着 \r。
			name: "CRLF 与末尾换行", screen: "x\r\nY\r\n",
			offset: 0, count: 500, maxBytes: 1024,
			wantText: "x\nY", wantBegin: 0, wantEnd: 2, wantTotal: 2,
		},
		{
			// count 比屏幕大时按屏幕给，lineEnd 是"实际返回行数"，不是请求值。
			name: "count 超过总行数", screen: "a\nb",
			offset: 0, count: 500, maxBytes: 1024,
			wantText: "a\nb", wantBegin: 0, wantEnd: 2, wantTotal: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := AgentScreenPage(tc.screen, tc.offset, tc.count, tc.maxBytes)
			if page.Text != tc.wantText {
				t.Fatalf("text = %q，期望 %q", page.Text, tc.wantText)
			}
			if page.LineBegin != tc.wantBegin || page.LineEnd != tc.wantEnd {
				t.Fatalf("行号 = %d-%d，期望 %d-%d", page.LineBegin, page.LineEnd, tc.wantBegin, tc.wantEnd)
			}
			if page.TotalLines != tc.wantTotal {
				t.Fatalf("totalLines = %d，期望 %d", page.TotalLines, tc.wantTotal)
			}
			if page.Truncated != tc.wantTrunc {
				t.Fatalf("truncated = %v，期望 %v", page.Truncated, tc.wantTrunc)
			}
		})
	}
}

// TestAgentScreenLinesKeepsBlankLinesInside 钉住"行是按换行切的"：
// 中间的空行必须保留（那是屏幕上真实的一行），只有末尾那个换行造成的空尾巴要去掉。
func TestAgentScreenLinesKeepsBlankLinesInside(t *testing.T) {
	lines := AgentScreenLines("a\n\nb\n")
	if len(lines) != 3 || lines[0] != "a" || lines[1] != "" || lines[2] != "b" {
		t.Fatalf("切行结果 = %#v，期望 [a, \"\", b]", lines)
	}
	if got := AgentScreenLines(""); got != nil {
		t.Fatalf("空屏幕应切成 0 行，拿到 %#v", got)
	}
}

// TestAgentSignalAllowed 钉住官方枚举（`dsh-terminal/src/types.ts:21`、`tool-terminal/src/index.ts:344`）。
func TestAgentSignalAllowed(t *testing.T) {
	for _, ok := range []string{"SIGINT", "SIGTERM", "SIGKILL", "SIGTSTP", "SIGHUP"} {
		if !AgentSignalAllowed(ok) {
			t.Errorf("%s 应当被允许", ok)
		}
	}
	for _, bad := range []string{"", "sigint", "SIGQUIT", "SIGUSR1", "INT"} {
		if AgentSignalAllowed(bad) {
			t.Errorf("%q 不在官方枚举里，不该被允许", bad)
		}
	}
}

// TestAgentStatusShapes 钉住官方 status 的两支：running 只有 kind；exited 必带可空字段。
func TestAgentStatusShapes(t *testing.T) {
	running := AgentTerminalRunning()
	if running.Kind != "running" || running.ExitCode != nil || running.Signal != nil {
		t.Fatalf("running 状态形状不对: %+v", running)
	}
	code := 3
	exited := AgentTerminalExited(&code, nil)
	if exited.Kind != "exited" || exited.ExitCode == nil || *exited.ExitCode != 3 || exited.Signal != nil {
		t.Fatalf("exited 状态形状不对: %+v", exited)
	}
}
