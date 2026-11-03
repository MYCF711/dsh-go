//go:build !windows

package main

import "os"

// 非 Windows 平台：终端本来就有 ANSI，大小用环境变量兜底
// （不引 ioctl 依赖；真要精确尺寸再补，这里只保证能编译）。
func enableVT() bool { return true }

func termSize() (int, int) {
	w, h := 100, 30
	if s := os.Getenv("COLUMNS"); s != "" {
		if n, err := atoi(s); err == nil && n > 20 {
			w = n
		}
	}
	if s := os.Getenv("LINES"); s != "" {
		if n, err := atoi(s); err == nil && n > 8 {
			h = n
		}
	}
	return w, h
}

func setTitle(string) {}

func atoi(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// rawInput 在非 Windows 上是空操作（真实现要走 termios，暂不需要）。
func rawInput() func() { return func() {} }
