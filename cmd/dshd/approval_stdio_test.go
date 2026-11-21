package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// H13：没有终端时，审批走 stdin/stderr 的标记通道。
//
// 这条测试钉住两条规矩：
//
//  1. **读不到应答 = 拒绝**。如果哪天有人把"没回答"改成"放行"，写操作的审批
//     就形同虚设。
//  2. **只认自己那条应答**。父进程每一行应答都带 requestId（serve_approval.go
//     的 replyTo），回给别人的答复不许被当成本次审批的答复——修前 markerAsk
//     只解析 `allow`，这条规矩等于不存在（详见 approval_binding_test.go ①）。
//
// 夹具为什么长这样：`startStdinDispatcher` 是**进程级 once**，且那根 stdin 一旦
// 读到 EOF，应答通道就永久关闭。所以整张表共用**一根** stdin / 一个分发器：
// 每条用例先从 stderr 抓到自己的 requestId，再按它写应答；"没应答（EOF）"必须
// 排在最后，它 close 掉写端来表达"父进程没了"。
func TestMarkerAskReadsResponse(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldErr := os.Stdin, os.Stderr
	os.Stdin = inR // 分发器只会认它第一次看到的 os.Stdin
	t.Cleanup(func() {
		os.Stdin, os.Stderr = oldIn, oldErr
		_ = inR.Close()
		_ = inW.Close()
	})

	cases := []struct {
		name string
		// respond 只对"要写一行回去"的用例非 nil；requestID 是子进程刚问出来的那条。
		respond func(requestID string) string
		// eof=true 表示这一条要 close 写端（之后再也不会有应答）。
		eof  bool
		want bool
	}{
		{"允许", func(id string) string {
			return approvalResponseTag + `{"requestId":"` + id + `","allow":true}` + "\n"
		}, false, true},
		{"拒绝", func(id string) string {
			return approvalResponseTag + `{"requestId":"` + id + `","allow":false}` + "\n"
		}, false, false},
		{"应答格式不对", func(string) string { return "随便一行\n" }, false, false},
		{"JSON 坏了", func(string) string { return approvalResponseTag + "{不是 json}\n" }, false, false},
		// 别的请求的 allow:true 不是自己的批准（串号判据就在这里）。
		{"别的请求的应答", func(string) string {
			return approvalResponseTag + `{"requestId":"perm-other","allow":true}` + "\n"
		}, false, false},
		{"没应答（EOF）", nil, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errR, errW, perr := os.Pipe()
			if perr != nil {
				t.Fatal(perr)
			}
			os.Stderr = errW
			defer func() {
				os.Stderr = oldErr
				_ = errR.Close()
				_ = errW.Close()
			}()

			type result struct {
				allow bool
				err   error
			}
			done := make(chan result, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			go func() {
				allow, askErr := markerAsk(ctx, "s1", "write", `{"path":"a.txt"}`)
				done <- result{allow: allow, err: askErr}
			}()

			// 子进程把请求写到 stderr 的第一行：从里面拿它自己编的 requestId。
			line, rerr := bufio.NewReader(errR).ReadString('\n')
			if rerr != nil && strings.TrimSpace(line) == "" {
				t.Fatalf("没读到审批请求行：%v", rerr)
			}
			req, ok := parseApprovalLine(strings.TrimSpace(line))
			if !ok || req.RequestID == "" {
				t.Fatalf("stderr 上的审批请求行不合法：%q", line)
			}

			if c.eof {
				_ = inW.Close()
			} else if payload := c.respond(req.RequestID); payload != "" {
				if _, werr := io.WriteString(inW, payload); werr != nil {
					t.Fatalf("写应答失败：%v", werr)
				}
			}

			got := <-done
			if got.err != nil {
				t.Fatalf("不该报错：%v", got.err)
			}
			if got.allow != c.want {
				t.Fatalf("%s：得到 allow=%v，期望 %v（requestId=%s）", c.name, got.allow, c.want, req.RequestID)
			}
		})
	}
}

// 开关默认关闭：没这个环境变量就不能改变原来的终端行为。
func TestApprovalViaStdioDefaultOff(t *testing.T) {
	old := os.Getenv("DSHD_APPROVAL_STDIO")
	_ = os.Unsetenv("DSHD_APPROVAL_STDIO")
	defer func() { _ = os.Setenv("DSHD_APPROVAL_STDIO", old) }()
	if approvalViaStdio() {
		t.Fatalf("没设环境变量时不该走父子通道")
	}
	_ = os.Setenv("DSHD_APPROVAL_STDIO", "1")
	if !approvalViaStdio() {
		t.Fatalf("设了 1 就该走")
	}
	if !strings.Contains(approvalRequestTag, "@@APPROVAL") {
		t.Fatalf("标记变了，父进程解析会失配")
	}
}
