// dshtui：DeepSeek Harness 的终端界面（客户端），经 Transport 与宿主通信。
//
// 这一版只做一件能验收的事：**开屏**。
// 点阵字体不是手抄的——`splash_gen.go` 由 `scripts/import-splash.py` 从
// dsh-TUI（MIT）的源文件机械生成，逐格一致。手抄 5 行点阵错一格，
// 看起来只会像"这个字母有点怪"，那是最难发现的一类错。
//
// 用法：
//
//	dshtui --dump-splash            打印开屏（无颜色），用来做逐格比对
//	dshtui --dump-splash --color    带真彩 ANSI
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	dump := flag.Bool("dump-splash", false, "打印开屏并退出（用于逐格比对）")
	color := flag.Bool("color", false, "开屏带真彩 ANSI")
	addr := flag.String("addr", "http://127.0.0.1:7788", "宿主 Transport 地址")
	token := flag.String("token", "", "宿主 /connect 的一次性 token")
	prompt := flag.String("send", "", "不带界面地发一条消息并打印收到的帧（验证链路）")
	flag.Parse()

	if *dump {
		fmt.Print(renderSplash(*color))
		return
	}
	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "用法：dshtui --dump-splash | --send <文本> --addr <宿主地址> --token <token>")
		os.Exit(2)
	}
	if err := sendAndTail(*addr, *token, *prompt); err != nil {
		fmt.Fprintln(os.Stderr, "dshtui:", err)
		os.Exit(1)
	}
}

// renderWordmark 把一行文字渲染成 5 行点阵。
//
// 表里没有的字形走 fallback 块——**不是跳过**，跳过会让版面错位，
// 而错位比缺字更难看出问题出在哪。
func renderWordmark(s string) [5]string {
	var rows [5]string
	for _, r := range strings.ToUpper(s) {
		g, ok := splashGlyphs[r]
		if !ok {
			g = splashFallback
		}
		for i := 0; i < 5; i++ {
			rows[i] += g[i]
		}
	}
	return rows
}

// splashPalette 是雾蓝档的深浅两色（品牌色只用在字上，正文永远中性灰）。
const (
	colorBrand = "\x1b[38;2;122;162;247m" // 雾蓝
	colorDim   = "\x1b[38;2;72;96;150m"   // 暗一档
	colorReset = "\x1b[0m"
)

func renderSplash(withColor bool) string {
	top := renderWordmark("DEEPSEEK")
	bottom := renderWordmark("HARNESS")

	// 两行居中对齐。参考实现要求两行**列数相等**（靠字距/缩进撑平）；
	// 这里用居中拿到同样的观感，且不依赖某款字体的特定度量。
	w := runeLen(top[0])
	if w2 := runeLen(bottom[0]); w2 > w {
		w = w2
	}

	var b strings.Builder
	b.WriteString("\n")
	for i := 0; i < 5; i++ {
		b.WriteString("  " + paint(pad(top[i], w), colorBrand, withColor) + "\n")
	}
	b.WriteString("\n")
	for i := 0; i < 5; i++ {
		b.WriteString("  " + paint(pad(bottom[i], w), colorDim, withColor) + "\n")
	}
	b.WriteString("\n")
	b.WriteString("  探索未至之境！\n")
	return b.String()
}

// pad 把一行按显示宽度居中（点阵全是半角块字符，一格=一个 rune）。
func pad(s string, width int) string {
	gap := width - runeLen(s)
	if gap <= 0 {
		return s
	}
	left := gap / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", gap-left)
}

func runeLen(s string) int { return len([]rune(s)) }

func paint(s, color string, withColor bool) string {
	if !withColor {
		return s
	}
	return color + s + colorReset
}
