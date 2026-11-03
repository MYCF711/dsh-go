package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"dshgo/internal/session"
)

// 审批的"父子通道"（H13）。

// 为什么需要它：`dshd serve` 起子进程跑一轮时带 --policy ask，
// 但子进程没有终端——没人能回答那个 [y/N]。
// 于是子进程把"要问什么"写到 stderr 的一行，父进程解析后经 WS 问界面，
// 再把决定写回子进程 stdin 的一行。
//
// 为什么走 stderr/stdin 而不新开 fd：
//   - Windows 上继承额外 fd 麻烦，而 stderr/stdin 本来就通着；
//   - stderr 已经承担"给人看的诊断"，加一个行首标记就能解析；
//   - **不碰 stdout**——那里只有一行 ready 握手（H11 是进程级验过的契约）。
const (
	approvalRequestTag  = "@@APPROVAL@@ "
	approvalResponseTag = "@@APPROVAL-RESP@@ "
)

// markerAsk 是没有终端、但有宿主在旁边时的审批通道。
//
// 读不到应答（管道断了、父进程死了）一律**拒绝**：
// 没人能回答的时候，"默认同意"是最坏的选择。
func markerAsk(ctx context.Context, sessionID, tool, args string) (bool, error) {
	req, err := json.Marshal(map[string]any{
		"requestId": "perm-" + session.NewID(),
		"sessionId": sessionID,
		"tool":      tool,
		"args":      args,
	})
	if err != nil {
		return false, nil
	}
	fmt.Fprintf(os.Stderr, "%s%s\n", approvalRequestTag, req)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, nil
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, approvalResponseTag) {
		return false, nil
	}
	var resp struct {
		Allow bool `json:"allow"`
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(line, approvalResponseTag)), &resp) != nil {
		return false, nil
	}
	return resp.Allow, nil
}

// approvalViaStdio 决定这一轮要不要走父子通道：
// 宿主明确开了这个开关才走，否则**不动**原来的终端行为。
func approvalViaStdio() bool {
	return os.Getenv("DSHD_APPROVAL_STDIO") == "1"
}
