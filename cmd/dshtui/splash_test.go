package main

import (
	"strings"
	"testing"
)

// 点阵表的结构契约：每个字形**恰好 5 行**，且 5 行**等宽**。
//
// 为什么值得一测：手抄或生成错一格，观感上只是"这个字母有点怪"，
// 而版面会整体错位——那是最难定位的一类错。
func TestGlyphsAreFiveEqualWidthRows(t *testing.T) {
	if len(splashGlyphs) < 18 {
		t.Fatalf("字形表只有 %d 个，生成多半断了", len(splashGlyphs))
	}
	for ch, rows := range splashGlyphs {
		if len(rows) != 5 {
			t.Fatalf("字形 %c 是 %d 行，应为 5 行", ch, len(rows))
		}
		w := len([]rune(rows[0]))
		for i, r := range rows {
			if got := len([]rune(r)); got != w {
				t.Fatalf("字形 %c 第 %d 行宽 %d，第 0 行宽 %d——不等宽会让版面错位", ch, i, got, w)
			}
		}
	}
}

// 两款标题用到的字必须都在表里，否则会静默走 fallback 块——
// 屏幕上出现一个方框，没人知道是"缺字"还是设计如此。
func TestWordmarkCharsExist(t *testing.T) {
	need := "DEEPSEEKHARN"
	for _, ch := range need {
		if _, ok := splashGlyphs[ch]; !ok {
			t.Fatalf("字形表缺 %c，开屏会渲染成 fallback 块", ch)
		}
	}
}

// 表里没有的字形走 fallback，**不许跳过**：跳过会让后面所有字符左移，
// 错位比缺字更难看出问题在哪。
func TestUnknownRuneUsesFallbackNotSkip(t *testing.T) {
	rows := renderWordmark("D?E")
	if got := len([]rune(rows[0])); got != len([]rune(splashGlyphs['D'][0]))*3 {
		t.Fatalf("未知字符应当占一个 fallback 宽度：整行宽 %d", got)
	}
}

// 开屏的版面契约：两行标题最终**等宽**（居中后），提示行在。
func TestSplashLinesAreAligned(t *testing.T) {
	out := renderSplash(false)
	var ws []int
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		ws = append(ws, len([]rune(l)))
	}
	if len(ws) < 11 {
		t.Fatalf("开屏行数不对：%d", len(ws))
	}
	want := ws[0]
	for i := 0; i < 10; i++ {
		if ws[i] != want {
			t.Fatalf("第 %d 行宽 %d，与首行 %d 不一致——两行标题没有对齐", i, ws[i], want)
		}
	}
	if !strings.Contains(out, "探索未至之境！") {
		t.Fatal("提示行不见了")
	}
}

// 带颜色的版本必须真的带 ANSI，且**复位过**：忘了复位会让之后所有输出都染色。
func TestColorModeEmitsReset(t *testing.T) {
	out := renderSplash(true)
	if !strings.Contains(out, "\x1b[38;2;") {
		t.Fatal("--color 没有输出真彩序列")
	}
	if strings.Count(out, colorReset) != 10 {
		t.Fatalf("每个着色行都该复位一次，实际 %d 次", strings.Count(out, colorReset))
	}
}
