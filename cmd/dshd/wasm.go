package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"dshgo/internal/wasmhost"
)

// cmdWasm 是 `dshd wasm` 的入口：把 internal/wasmhost 从"只有测试在用"的库，
// 变成能被人和脚本直接用的命令。
//
// 产品动作只有两个：
//   - inspect：一个模块到底是什么东西——魔数对不对、内容摘要、
//     能调哪些函数；
//   - call：真的调一次，把返回值打成一行；可给超时，
//     超时会**真的**把 wasm 执行打断（不是把结果丢掉然后继续跑）。
//
// 为什么不做"跑 _start"：插件形态的 wasm 是**被调用**的库，
// 没有 CLI 语义的 main；设了 _start 反而会让这类模块装不上。
func cmdWasm(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("用法: dshd wasm inspect|call " +
			"（inspect --module <path>；call --module <path> --fn <name> [--args 1,2] [--timeout 2s]）")
	}
	switch argv[0] {
	case "inspect":
		return cmdWasmInspect(argv[1:])
	case "call":
		return cmdWasmCall(argv[1:])
	default:
		return fmt.Errorf("未知子命令 wasm %s（可用：inspect / call）", argv[0])
	}
}

// cmdWasmInspect 打印一个模块的身份与能力：魔数、sha256、导出函数名。
//
// 这里对魔数和摘要都是**先自己读一遍文件**再交给 wasmhost 装载：
// 魔数不对时也要把 sha256 打出来——"我到底指到了哪个文件"
// 恰恰是给错文件时最需要的信息，而 Load 在魔数那步就会拒绝。
func cmdWasmInspect(argv []string) error {
	fs := flag.NewFlagSet("wasm inspect", flag.ContinueOnError)
	modulePath := fs.String("module", "", "wasm 模块路径")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *modulePath == "" {
		return fmt.Errorf("缺 --module")
	}

	raw, err := os.ReadFile(*modulePath)
	if err != nil {
		return fmt.Errorf("读 wasm 失败（%s）: %w", *modulePath, err)
	}
	sum := sha256.Sum256(raw)
	magicOK := bytes.HasPrefix(raw, []byte("\x00asm"))
	fmt.Printf("模块:   %s（%d 字节）\n", *modulePath, len(raw))
	if magicOK {
		fmt.Printf("魔数:   ok（\\0asm）\n")
	} else {
		fmt.Printf("魔数:   不对（文件不以 \\0asm 开头）\n")
	}
	fmt.Printf("sha256: %x\n", sum)
	if !magicOK {
		return fmt.Errorf("%s 不是 wasm 模块：文件不以 \\0asm 开头", *modulePath)
	}

	ctx := context.Background()
	m, err := wasmhost.Load(ctx, filepath.Base(*modulePath), *modulePath)
	if err != nil {
		return err
	}
	defer m.Close(ctx)

	exports := m.Exports()
	if len(exports) == 0 {
		fmt.Println("导出函数: 无")
		return nil
	}
	fmt.Printf("导出函数 (%d)，按名字排序:\n", len(exports))
	for _, name := range exports {
		fmt.Printf("  %s\n", name)
	}
	return nil
}

// cmdWasmCall 调用一个导出函数，把返回值打成 `= 5` 这样一行。
//
// --timeout 的语义不是"到点就把结果扔掉"：ctx 到期后 wazero 会关闭模块
// （CloseOnContextDone），死循环的 wasm 会真的停下来并返回错误。
// 这条路径由 TestCallTimeoutInterrupts 用夹具里的 spin() 证明。
func cmdWasmCall(argv []string) error {
	fs := flag.NewFlagSet("wasm call", flag.ContinueOnError)
	modulePath := fs.String("module", "", "wasm 模块路径")
	fn := fs.String("fn", "", "要调用的导出函数名")
	rawArgs := fs.String("args", "", "逗号分隔的十进制整数（空=无参；按 uint64 位模式传，负数要写补码）")
	timeout := fs.Duration("timeout", 0, "调用超时（0=不限）；超时会取消 ctx 并真的打断 wasm 执行")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *modulePath == "" {
		return fmt.Errorf("缺 --module")
	}
	if *fn == "" {
		return fmt.Errorf("缺 --fn")
	}
	if *timeout < 0 {
		return fmt.Errorf("--timeout 不能是负数（0 = 不限）")
	}
	args, err := parseWasmArgs(*rawArgs)
	if err != nil {
		return err
	}

	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	m, err := wasmhost.Load(ctx, filepath.Base(*modulePath), *modulePath)
	if err != nil {
		return err
	}
	// 用 Background 关：超时之后上面的 ctx 已经是过期的了。
	defer m.Close(context.Background())

	// 先查名字再调：Call 自己的报错只说"没有这个函数"，
	// 而人此刻想知道的是"那有哪几个"。
	exports := m.Exports()
	if !hasExport(exports, *fn) {
		avail := strings.Join(exports, ", ")
		if avail == "" {
			avail = "（这个模块没有导出任何函数）"
		}
		return fmt.Errorf("模块里没有导出函数 %q；可用：%s", *fn, avail)
	}

	res, err := m.Call(ctx, *fn, args...)
	if err != nil {
		// "我自己设的时间到了"和"模块里出错了"要分开说：
		// 前者换个 --timeout 就行，后者得去看 wasm 本身。
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("调用超时（--timeout %s）：%v", *timeout, err)
		}
		return err
	}
	fmt.Printf("= %s\n", formatWasmResults(res))
	return nil
}

// parseWasmArgs 把 --args 解析成 wasm 调用参数。
//
// 空串 = 无参（不是错误）：调一个 ()->() 的函数本来就该能这么写。
//
// 为什么只收**无符号**十进制：wazero 的 Call 收的是位模式（uint64），
// 而负数的位宽取决于参数签名（i32 还是 i64），CLI 这一步还看不到签名。
// 靠猜不行——实测把 -1 原样按 uint64 传进 add(i32,i32)，i32 的高 32 位
// 会带脏数据回来（add(-1,3) 得到 0xFFFFFFFF00000002），
// 那正是"看起来成功了"的假结果。所以负数直接拒绝，并告诉人该写什么。
func parseWasmArgs(raw string) ([]uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	args := make([]uint64, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("--args 第 %d 个是空的（写成 \"2,3\" 这样）", i+1)
		}
		v, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--args 第 %d 个 %q 不是无符号十进制整数"+
				"（wasm 参数按位模式传：i32 的 -1 写 4294967295，i64 的 -1 写 18446744073709551615）", i+1, p)
		}
		args = append(args, v)
	}
	return args, nil
}

// formatWasmResults 把返回值打成一行。多个结果用逗号分隔；无返回值明确说"无"，
// 而不是打一个空行让人以为命令坏了。
func formatWasmResults(res []uint64) string {
	if len(res) == 0 {
		return "（无返回值）"
	}
	parts := make([]string, len(res))
	for i, v := range res {
		parts[i] = strconv.FormatUint(v, 10)
	}
	return strings.Join(parts, ", ")
}

func hasExport(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
