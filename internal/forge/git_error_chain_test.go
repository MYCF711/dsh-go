package forge

// git 失败的错误链与退出码分类。
//
// 修之前 `Executor.run` 用 `%v` 拼消息，`*exec.ExitError` 不在链上；调用方只能
// 匹配 "exit status N" 文本判错，而 `strings.Contains(..., "exit status 1")`
// 会把 129 / 10x / 1xx 一起当成 1 —— 真实失败会被静默读成"没有 fork point"。

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

// TestGitRunFailureKeepsExitErrorChain 钉住错误链：失败必须能 errors.As 到
// *exec.ExitError（结构判据），而不是只留一段文本。
func TestGitRunFailureKeepsExitErrorChain(t *testing.T) {
	e := newExecutor()
	_, _, err := e.run(context.Background(), t.TempDir(), "no-such-git-subcommand")
	if err == nil {
		t.Fatal("不存在的 git 子命令应当失败")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("失败链里没有 *exec.ExitError（exit error 被 %%v 吞了）：%v", err)
	}
}

// TestMergeBaseClassifiesByExitCode 钉住"只有 1/128 才算没有 fork point"。
// 129 是真实失败（未知选项），旧文本判据会把它当成 exit status 1 吞掉。
func TestMergeBaseClassifiesByExitCode(t *testing.T) {
	repo := gitInit(t)
	commitFile(t, repo, "a.txt", "a\n", "c1")
	e := newExecutor()

	// 128（ref 不存在）：官方语义是"没有可用 fork point"，不是错误。
	if commit, err := e.mergeBase(context.Background(), repo, "HEAD", "no-such-ref"); err != nil || commit != "" {
		t.Fatalf("exit 128 应判成没有 fork point：commit=%q err=%v", commit, err)
	}
	// 129（未知选项）：真实失败，必须报错。
	if commit, err := e.mergeBase(context.Background(), repo, "--no-such-option", "HEAD"); err == nil {
		t.Fatalf("exit 129 被误判成“没有 fork point”（失败当成成功）：commit=%q", commit)
	}
}
