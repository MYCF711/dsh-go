package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// heartbeatScript 起一棵**真实**进程树：root shell 先等一下（让进程树归属落在
// root 派生子进程之前），再起一个子 shell 循环往 stdout 写心跳。写端被整棵树共享，
// 所以只要还有活着的子孙，读侧就不会 EOF。
const heartbeatScript = `sleep 0.3; sh -c 'while :; do echo tick; sleep 0.02; done' & wait`

// heartbeatTree 是一棵真实进程树 + 它的归属句柄。
//
// Windows 用 Job Object（KILL_ON_JOB_CLOSE），Unix 用独立进程组——和产品里
// modules/exec/bash 的进程树归属是同一套机制。为什么不用 `taskkill /T`：
// MSYS 的 fork 是模拟出来的，Windows 上的父子关系会被中间进程打断，
// `taskkill /T` 在 Git Bash 起的进程上返回 0 却收不到整棵树（本任务实测：
// 孙子进程照旧往管道里写）。判据必须是"写端全关"，不是"命令成功"。
type heartbeatTree interface {
	// Out 是整棵树共享的 stdout 读侧。
	Out() *bufio.Reader
	// Wait 回收 root 进程。
	Wait() error
	// Kill 终止整棵树（不是只杀 root）。
	Kill() error
}

// per-call deadline 到点必须**终止整棵进程树**（不是丢下它不管），并且结构化错误
// 原样保留。
//
// 为什么用真实进程：只断言"工具收到了 ctx.Done()"证明不了树被收掉——真正会留下
// 垃圾的是 shell 里的子孙进程。这里起一棵 root → 子 shell → 循环写心跳的树，
// 只有**所有**写端都死了，读侧才会 EOF。
func TestPerCallDeadlineCancelsProcessTree(t *testing.T) {
	tl := &treeTool{name: "tree", started: make(chan struct{})}
	p, k := newPipe(t, Options{ToolTimeout: 30 * time.Minute}, tl)
	defer k.Root().Dispose()

	// budget 必须**显著大于"进程树启动时间"**，否则测的就不是"deadline 杀树"，
	// 而是抢跑：脚本里那 300ms 是 Job Object 归属窗口（见 startHeartbeatTree 注释：
	// 子 shell 必须晚于 assign 被创建，否则它不属于任何 Job），第一个 tick 还要等
	// root 起好 + 300ms + 子 shell 起来。
	//
	// 实测「从发起调用到首行心跳」空载就要 0.49–0.58s（MSYS 进程创建是主要成本），
	// 旧预算 1s 的余量只剩约 0.42–0.51s（≈1.7–2×）——全量并行测试一忙就翻车
	// （实测 red：`读心跳失败：EOF`——预算先到、写端被收掉，测试却还在等第一个 tick）。
	// 这里给 3s：余量 2.4–2.5s（≈4.9–5.2×），测试总时长 3s 仍然可接受。
	//
	// **放宽的只是"等树起来"的等待窗，判据一条没动**：deadline 到点仍必须真的杀掉
	// 整棵树（waitGone 断言写端全关）、错误仍是 *ToolTimeoutError 且 Budget/Source
	// 不变、结构化的 Summary/Data/Hint 仍逐样检查。所以它掩盖不了"没杀树"或
	// "没到点"的真实回归——那两种都会在下面各自的断言上红。
	budget := 3 * time.Second
	ctx := spi.WithToolDeadline(context.Background(), budget)

	type outcome struct {
		res spi.Result
		err error
	}
	// 预算的**时钟起点**在这里（ctx 一建好就开始走），所以余量要按"从这里到
	// 首行心跳"算，而不是"从树起来到首行心跳"。下面那行日志记的就是这个数。
	start := time.Now()
	done := make(chan outcome, 1)
	go func() {
		res, err := p.Run(ctx, "tree", json.RawMessage(`{}`))
		done <- outcome{res: res, err: err}
	}()

	select {
	case <-tl.started:
	case <-time.After(10 * time.Second):
		t.Fatal("进程树没有起来")
	}
	// 先证明树是**活的**：读到一行心跳。否则"管道 EOF"可能只是因为根本没进程。
	//
	// 这一行同时记下"树从起到能写"的真实耗时：将来有人再调 budget 时，
	// 这个数字就是判断余量够不够的依据（而不是凭感觉）。
	line := tl.readLine(t, 10*time.Second)
	elapsed := time.Since(start)
	t.Logf("从发起调用到首行心跳 %s（per-call 预算 %s，余量 %s）", elapsed, budget, budget-elapsed)
	if !strings.Contains(line, "tick") {
		t.Fatalf("进程树没有心跳输出：%q", line)
	}

	var out outcome
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		tl.cleanup()
		t.Fatal("per-call deadline 没有在预算附近返回")
	}

	var te *ToolTimeoutError
	if !errors.As(out.err, &te) {
		tl.cleanup()
		t.Fatalf("超时必须是 *ToolTimeoutError，得到 %T: %v", out.err, out.err)
	}
	if te.Budget != budget || te.Source != timeoutSourcePerCall {
		tl.cleanup()
		t.Fatalf("预算/来源不对：%+v", te)
	}
	// 结构化错误三样都在：Summary 给人看、Data 给模型事实、Hint 给下一步。
	if out.res.Summary == "" || out.res.Data == nil || out.res.Hint == "" {
		tl.cleanup()
		t.Fatalf("超时的结构化错误丢了：%+v", out.res)
	}
	if !strings.Contains(out.res.Hint, budget.String()) {
		tl.cleanup()
		t.Fatalf("Hint 要说清这次的预算，得到 %q", out.res.Hint)
	}

	// 整棵树都必须死掉：心跳管道的写端全部消失之后，读侧才会 EOF。
	tl.waitGone(t, 10*time.Second)
}

// treeTool 是"会起真实进程树"的假工具：Exec 里起 shell，shell 再起子进程循环
// 写心跳；取消时终止整棵树（这正是 bash/pwsh 这类工具该做的事）。
type treeTool struct {
	name    string
	started chan struct{}

	mu     sync.Mutex
	tree   heartbeatTree
	killed bool
}

func (t *treeTool) Name() string            { return t.name }
func (t *treeTool) Description() string     { return "起真实进程树的假工具" }
func (t *treeTool) Caps() spi.Caps          { return spi.Caps{Executes: true} }
func (t *treeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (t *treeTool) Exec(ctx context.Context, _ json.RawMessage) (spi.Result, error) {
	tree, err := startHeartbeatTree()
	if err != nil {
		return spi.Result{Hint: "测试夹具起不了进程树（本机没有可用的 bash/sh）"}, err
	}
	t.mu.Lock()
	t.tree = tree
	t.mu.Unlock()
	close(t.started)

	// 本工具自己不会跑完：它只在取消时收工——这就是"挂死"的形状。
	<-ctx.Done()
	if err := tree.Kill(); err != nil {
		return spi.Result{Hint: "终止进程树失败；先 job_list 看有没有残留任务"}, err
	}
	t.mu.Lock()
	t.killed = true
	t.mu.Unlock()
	return spi.Result{Hint: "进程树已按取消终止"}, ctx.Err()
}

// readLine 从心跳管道读一行（证明树在写）。
func (t *treeTool) readLine(tb testing.TB, within time.Duration) string {
	tb.Helper()
	t.mu.Lock()
	tree := t.tree
	t.mu.Unlock()
	if tree == nil {
		tb.Fatal("进程树从没起来过")
	}
	type lineOut struct {
		line string
		err  error
	}
	ch := make(chan lineOut, 1)
	go func() {
		line, err := tree.Out().ReadString('\n')
		ch <- lineOut{line: line, err: err}
	}()
	select {
	case out := <-ch:
		if out.err != nil {
			// EOF 在这里只有一种来路：写端全被收掉了。而测试刚开始读时树还活着，
			// 所以收到的 EOF 几乎总是"per-call 截止时间先到、流水线把树收了"——
			// 也就是等待窗太小（树还没吐出第一个 tick）。把这条推断写进失败信息，
			// 省得下次有人把它当成产品缺陷去查。
			tb.Fatalf("读心跳失败：%v（EOF=写端已全关；若第一个 tick 还没出现，"+
				"就是 per-call 截止时间先到——见本测试 budget 的注释）", out.err)
		}
		return out.line
	case <-time.After(within):
		tb.Fatalf("%s 内没读到心跳", within)
		return ""
	}
}

// waitGone 断言心跳管道的写端全部消失（= root 与它的子孙都死了）。
func (t *treeTool) waitGone(tb testing.TB, within time.Duration) {
	tb.Helper()
	t.mu.Lock()
	tree := t.tree
	t.mu.Unlock()
	if tree == nil {
		tb.Fatal("进程树从没起来过")
	}
	errc := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, tree.Out())
		errc <- err
	}()
	select {
	case <-errc:
		// EOF：所有写端都关了。
	case <-time.After(within):
		t.cleanup()
		tb.Fatalf("%s 之后进程树还在往管道里写：取消没有终止整棵树", within)
	}
}

// cleanup 兜底收尾，避免失败路径留下进程。
func (t *treeTool) cleanup() {
	t.mu.Lock()
	tree := t.tree
	killed := t.killed
	t.mu.Unlock()
	if tree == nil || killed {
		return
	}
	_ = tree.Kill()
	_ = tree.Wait()
}
