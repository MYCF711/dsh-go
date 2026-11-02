package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// diag 是运行期的"事后能查"的东西。
//
// 之前出问题只有一行 stderr，会话一关就没了。人要回答"它到底跑到哪一步、
// 前置状态是什么"全靠复述。这里把结构化事件写到 <home>/logs/<日期>.log：
// 一行一个 JSON，带会话 id、步骤、工具名、耗时、错误原文。
//
// 它不是指标系统——就一个文件，能 grep、能贴给人看。
type diag struct {
	Path string
	file *os.File
	prev *slog.Logger
}

// openDiag 打开当天的日志文件，并把 slog 的默认输出指过去。
//
// stderr 的行为**不变**（原来怎么打还怎么打）：日志是附加的，不是替代。
func openDiag(home string) (*diag, error) {
	dir := filepath.Join(home, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("建日志目录失败（%s）: %w", dir, err)
	}
	name := "dshd-" + time.Now().Format("2006-01-02") + ".log"
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开日志失败（%s）: %w", path, err)
	}
	d := &diag{Path: path, file: f, prev: slog.Default()}
	// JSON 一行一条；带上时间。
	h := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(h))
	return d, nil
}

// Close 关文件并把 slog 还回去。
func (d *diag) Close() {
	if d == nil || d.file == nil {
		return
	}
	slog.SetDefault(d.prev)
	d.file.Close()
	d.file = nil
}

// 会话开始的上下文：哪份会话、在哪个 cwd、什么模式。
// 出事之后第一件要确认的就是"是哪一份"。
func logSessionStart(home, id, path, cwd, mode string) {
	slog.Info("会话开始",
		"session", id, "path", path, "cwd", cwd, "mode", mode, "home", home)
}
