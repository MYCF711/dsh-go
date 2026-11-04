// dshtui：DeepSeek Harness 的终端界面（客户端），经 Transport 与宿主通信。
//
// 产品名是 **DSH TUI GO**（可执行文件仍叫 dshtui.exe，见 brand.go）。
//
// 这一版只做一件能验收的事：**开屏**。
// 点阵字体不是手抄的——`splash_gen.go` 由 `scripts/import-splash.py` 从
// dsh-TUI（MIT）的源文件机械生成，逐格一致。手抄 5 行点阵错一格，
// 看起来只会像"这个字母有点怪"，那是最难发现的一类错。
//
// 用法：
//
//	dshtui --version                只打印版本（不连宿主、不启动界面）
//	dshtui --dump-splash            打印开屏（无颜色），用来做逐格比对
//	dshtui --dump-splash --color    带真彩 ANSI
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 把"命令行 → 行为"写成可测的函数。
//
// 为什么要拆出来：`--version` 的判据是「只读版本号、**不启动界面**」——
// 这件事只有把 main 变成纯函数才验得了。真跑 main 会去连宿主，
// 在测试环境里那不是"失败"，是挂在那里等超时。stdout/stderr 也传进来，
// 测试可以逐字节断言输出。
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dshtui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dump := fs.Bool("dump-splash", false, "打印开屏并退出（用于逐格比对）")
	color := fs.Bool("color", false, "开屏带真彩 ANSI")
	addr := fs.String("addr", "http://127.0.0.1:7788", "宿主 Transport 地址")
	token := fs.String("token", "", "宿主 /connect 的一次性 token")
	prompt := fs.String("send", "", "不带界面地发一条消息并打印收到的帧（验证链路）")
	// 回放一串"人本来会敲的键"，把每一轮的帧与屏幕快照写成一份 JSON。
	// 只给探测用（scripts/tui-session-e2e.py）：不给 --script 时行为与以前完全一致。
	script := fs.String("script", "", "回放一份脚本（JSON）并把帧与屏幕快照写到 --out")
	out := fs.String("out", "", "配合 --script：报告 JSON 的落点")
	// 只读版本：**不连宿主、不进 alt-screen**，脚本和任务管理器都能安全地调它。
	showVersion := fs.Bool("version", false, "只打印版本号并退出（不启动界面）")
	// flag 默认的 Usage 是一行 "Usage of dshtui"，连产品名都没有。
	fs.Usage = func() {
		usage(stderr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0 // -h/--help：已经打印过说明
		}
		return 2 // 与 flag 包默认的用法错误退出码一致
	}

	if *showVersion {
		fmt.Fprintln(stdout, versionLine())
		return 0
	}
	if *dump {
		fmt.Fprint(stdout, renderSplash(*color))
		return 0
	}
	// --script 走无界面回放（见 script.go）；它不改默认路径，只是多一个入口。
	if *script != "" {
		if *token == "" {
			fmt.Fprintln(stderr, "dshtui: 缺 --token（宿主 ready 行里的 token，一次性）")
			return 1
		}
		if err := runScript(*addr, *token, *script, *out); err != nil {
			fmt.Fprintln(stderr, "dshtui:", err)
			return 1
		}
		return 0
	}
	// 没给 --send 就是进全屏界面（这才是默认用法）
	if *prompt == "" {
		a, err := newApp(*addr, *token)
		if err != nil {
			fmt.Fprintln(stderr, "dshtui:", err)
			return 1
		}
		if err := a.run(); err != nil {
			fmt.Fprintln(stderr, "dshtui:", err)
			return 1
		}
		return 0
	}
	if err := sendAndTail(*addr, *token, *prompt); err != nil {
		fmt.Fprintln(stderr, "dshtui:", err)
		return 1
	}
	return 0
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
