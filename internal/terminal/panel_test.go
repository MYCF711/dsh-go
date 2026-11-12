package terminal_test

// 终端面板工具的**唯一**共享状态契约（rc.2 `dsh-terminal-panel` 的两个只读工具）
// 的纯逻辑测试：行分页、字节上限、UTF-8 边界。
//
// 为什么这些逻辑放在 internal/terminal 而不是工具包里：官方 `TerminalScreenPage`
// 是终端控制器产出的（`dsh-api-terminal-controller` 的 `readScreen`），工具只是
// 把它渲染出来。本宿主没有屏幕模型，分页只能对"输出尾部"做——这段换算属于终端层，
// 工具层不该各写一份（写两份必然两份行号语义）。

import (
	"strings"
	"testing"

	"dshgo/internal/terminal"
)

func TestNewestLinesPageKeepsRequestedTail(t *testing.T) {
	screen := "one\ntwo\nthree\nfour\nfive"
	page := terminal.NewestLinesPage(screen, 2, 1<<20)
	if page.Text != "four\nfive" {
		t.Fatalf("应取最新 2 行，实际 %q", page.Text)
	}
	if page.TotalLines != 5 || page.LineBegin != 4 || page.LineEnd != 5 {
		t.Fatalf("行号不对: total=%d begin=%d end=%d", page.TotalLines, page.LineBegin, page.LineEnd)
	}
	if page.Truncated {
		t.Fatal("没有截断却标了 truncated")
	}
}

func TestNewestLinesPageMoreLinesThanScreen(t *testing.T) {
	page := terminal.NewestLinesPage("a\nb", 500, 1<<20)
	if page.Text != "a\nb" || page.LineBegin != 1 || page.LineEnd != 2 || page.TotalLines != 2 {
		t.Fatalf("要 500 行只有 2 行时应全给: %+v", page)
	}
}

func TestNewestLinesPageEmpty(t *testing.T) {
	page := terminal.NewestLinesPage("", 500, 1<<20)
	if page.Text != "" || page.TotalLines != 0 || page.LineBegin != 0 || page.LineEnd != 0 {
		t.Fatalf("空屏幕应是零值页: %+v", page)
	}
}

// 尾部换行不算一行：PTY 每条命令输出都以换行结束，多算一行会让
// `[lines: N-N of M]` 的位置永远偏 1，模型据此翻页就会重叠。
func TestNewestLinesPageDropsTrailingNewline(t *testing.T) {
	page := terminal.NewestLinesPage("a\nb\n", 500, 1<<20)
	if page.TotalLines != 2 || page.Text != "a\nb" {
		t.Fatalf("尾部换行不该算一行: %+v", page)
	}
}

// PTY 的换行是 CRLF；行尾留着 \r 会让模型看到的正文带控制字符。
func TestNewestLinesPageNormalizesCRLF(t *testing.T) {
	page := terminal.NewestLinesPage("a\r\nb\r\n", 500, 1<<20)
	if page.Text != "a\nb" {
		t.Fatalf("CRLF 应规范成 LF，实际 %q", page.Text)
	}
	if page.TotalLines != 2 {
		t.Fatalf("CRLF 分行不对: %d", page.TotalLines)
	}
}

// 字节上限先按**整行**淘汰最旧的：这样 lineBegin 仍然是真实行号
// （官方 page.lineBegin/lineEnd 描述的就是这一段在屏幕上的位置）。
func TestNewestLinesPageByteBoundDropsWholeLines(t *testing.T) {
	screen := "aaaa\nbbbb\ncccc\ndddd"
	page := terminal.NewestLinesPage(screen, 500, 10)
	if page.Text != "cccc\ndddd" {
		t.Fatalf("应只保留能装下的最新整行，实际 %q", page.Text)
	}
	if page.LineBegin != 3 || page.LineEnd != 4 || page.TotalLines != 4 {
		t.Fatalf("淘汰整行后行号要跟着走: %+v", page)
	}
	if !page.Truncated {
		t.Fatal("淘汰了行就要标 truncated")
	}
}

// 单行本身超上限时只能截字节，此时行号仍指向该行。
func TestNewestLinesPageHugeSingleLine(t *testing.T) {
	page := terminal.NewestLinesPage(strings.Repeat("x", 100), 500, 10)
	if len(page.Text) > 10 {
		t.Fatalf("超上限的单行必须被截: %d 字节", len(page.Text))
	}
	if !page.Truncated {
		t.Fatal("截了字节就要标 truncated")
	}
	if page.LineBegin != 1 || page.LineEnd != 1 || page.TotalLines != 1 {
		t.Fatalf("单行截字节不动行号: %+v", page)
	}
}

func TestNewestBytesKeepsValidUTF8(t *testing.T) {
	// "汉" 是 3 字节；上限 4 只能装下 1 个完整的"汉"，不能切开第二个。
	got, truncated := terminal.NewestBytes("汉字", 4)
	if !truncated {
		t.Fatal("截断了却没报 truncated")
	}
	if !strings.HasSuffix("汉字", got) || !utf8Valid(got) {
		t.Fatalf("必须是原文的完整 UTF-8 后缀，实际 %q", got)
	}
	if got != "字" {
		t.Fatalf("应保留最新那个完整字符，实际 %q", got)
	}
}

func TestNewestBytesUnderLimit(t *testing.T) {
	got, truncated := terminal.NewestBytes("abc", 3)
	if got != "abc" || truncated {
		t.Fatalf("没超上限不该动: %q %v", got, truncated)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}
