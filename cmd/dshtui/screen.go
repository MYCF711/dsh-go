package main

// 全屏渲染：进 alt-screen、按"行数组"整屏重画、输出时把光标放回去。
//
// 为什么先整屏重画而不是差分：差分要维护"上一帧每一格是什么"，
// 只要有一处算错就会留下残影，而残影比闪一下难查得多。
// 先整屏画（够快：一屏 30 行），把正确性钉住，diff 是后面的事。

import (
	"fmt"
	"os"
	"strings"
)

// Screen 是一块可重画的全屏区域。
type Screen struct {
	out    *os.File
	width  int
	height int
	inAlt  bool
	// prev 是上一帧的每一行。差分渲染靠它：**只重画变了的行**。
	// 整屏重画在 30 行时够快，但流式输出每 100ms 一帧就会看出闪，
	// 而且终端越慢（Windows 控制台）越明显。
	prev []string
}

func NewScreen() *Screen {
	w, h := termSize()
	return &Screen{out: os.Stdout, width: w, height: h}
}

// Enter 进 alt-screen 并隐藏光标。
func (s *Screen) Enter() {
	if !enableVT() {
		// 打不开 VT 时不要硬来：直接告诉人原因，比满屏乱码有用。
		fmt.Fprintln(os.Stderr, "这个终端不支持 ANSI 转义（Win10 以前的控制台）；请用 Windows Terminal")
		os.Exit(1)
	}
	s.inAlt = true
	fmt.Fprint(s.out, "\x1b[?1049h\x1b[?25l\x1b[2J")
}

// Leave 恢复原屏幕与光标。**必须幂等**：异常退出路径也会调它。
func (s *Screen) Leave() {
	if !s.inAlt {
		return
	}
	s.inAlt = false
	fmt.Fprint(s.out, "\x1b[?25h\x1b[?1049l")
}

// Resize 更新尺寸（每帧开始前调用，处理窗口变化）。
func (s *Screen) Resize() {
	s.width, s.height = termSize()
}

// Render 把若干行画满整屏：第 0 行 = 顶部。
//
// 超宽的行按显示宽度硬切（点阵与正文都是等宽假设），
// 少行就补空行——**不清屏不重画**会让上一帧的尾巴留在下面。
func (s *Screen) Render(lines []string) {
	var b strings.Builder
	b.WriteString("\x1b[H") // 光标回左上
	for i := 0; i < s.height; i++ {
		b.WriteString("\x1b[2K") // 清整行
		if i < len(lines) {
			b.WriteString(fitWidth(lines[i], s.width))
		}
		if i < s.height-1 {
			b.WriteString("\r\n")
		}
	}
	fmt.Fprint(s.out, b.String())
}

// fitWidth 把一行裁/补到恰好 w 个显示列。
func fitWidth(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		if w <= 1 {
			return string(r[:w])
		}
		return string(r[:w-1]) + "…"
	}
	if len(r) < w {
		return s + strings.Repeat(" ", w-len(r))
	}
	return s
}
