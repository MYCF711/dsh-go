package bash

// 宽限链（官方 `bash.graceMs`）的两条结局必须能被**确定性地**区分开：
//
//	宽限内退出（Graceful）→ 不强杀，命令保留自己的退出码；
//	宽限用完    （Escalated）→ 走原来的强杀路径（Windows: TerminateJobObject）。
//
// 真实进程那条（grace_red_test.go）证明 Windows 上 CTRL_BREAK 真的能投递且
// 宽限内退出的进程不被强杀；本文件用假 native 把**链本身**（投递 → 等待 →
// 升级 → 释放归属）逐拍钉住，两平台都跑。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// fakeTree 记录宽限链每一步的调用次数。
type fakeTree struct {
	mu         sync.Mutex
	delivery   string
	graceErr   error
	gracefuls  int
	terminates int
	releases   int
}

func (f *fakeTree) graceful() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gracefuls++
	if f.graceErr != nil {
		return "", f.graceErr
	}
	if f.delivery == "" {
		return DeliveryCtrlBreak, nil
	}
	return f.delivery, nil
}

func (f *fakeTree) terminate() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminates++
	return nil
}

func (f *fakeTree) release() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return nil
}

func (f *fakeTree) terminateOnClose(bool) bool { return true }

func (f *fakeTree) counts() (gracefuls, terminates, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gracefuls, f.terminates, f.releases
}

// liveControl 用**本测试进程**的真实身份造一个 processControl：
// 身份复核走真实 OS 查询，只有 native 是假的。
func liveControl(t *testing.T, native processTreeNative) *processControl {
	t.Helper()
	identity, err := queryProcessIdentity(os.Getpid())
	if err != nil || !identity.Active || identity.Image == "" || identity.CreationID == "" {
		t.Fatalf("取本进程身份失败: %+v, %v", identity, err)
	}
	identity.ProcessTree = "fake-tree"
	return &processControl{identity: identity, native: native}
}

// 宽限内自行退出：不投强杀，结局是 Graceful。
func TestStopChainGracefulExitWithinGrace(t *testing.T) {
	native := &fakeTree{delivery: DeliverySIGTERM}
	control := liveControl(t, native)
	exited := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(exited)
	}()

	out, err := control.stopWithGrace(2*time.Second, exited)
	if err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if !out.Graceful || out.Escalated {
		t.Fatalf("宽限内退出必须记 Graceful、不记 Escalated: %+v", out)
	}
	if out.Delivery != DeliverySIGTERM || out.Grace != 2*time.Second {
		t.Fatalf("结局必须记录投递方式与预算: %+v", out)
	}
	if out.Waited < 150*time.Millisecond || out.Waited > 1500*time.Millisecond {
		t.Fatalf("等待时长应≈200ms（宽限窗口内提前结束），实际 %v", out.Waited)
	}
	gracefuls, terminates, releases := native.counts()
	if gracefuls != 1 || terminates != 0 {
		t.Fatalf("宽限内退出不该强杀: graceful=%d terminate=%d", gracefuls, terminates)
	}
	if releases != 1 {
		t.Fatalf("宽限内退出也必须释放归属句柄（Job 不放弃）: release=%d", releases)
	}
	if _, ok := control.outcome(); !ok {
		t.Fatal("结局必须可从 processControl 上取回")
	}
}

// 宽限用完：升级到强杀，并且只投递一次宽限请求。
func TestStopChainEscalatesAfterGrace(t *testing.T) {
	native := &fakeTree{delivery: DeliveryCtrlBreak}
	control := liveControl(t, native)

	start := time.Now()
	out, err := control.stopWithGrace(150*time.Millisecond, make(chan struct{}))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if !out.Escalated || out.Graceful {
		t.Fatalf("宽限用完必须记 Escalated、不记 Graceful: %+v", out)
	}
	if elapsed < 100*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("必须真的等满宽限再强杀，实际 %v", elapsed)
	}
	gracefuls, terminates, releases := native.counts()
	if gracefuls != 1 || terminates != 1 || releases != 1 {
		t.Fatalf("宽限链应恰好一次请求 + 一次强杀 + 一次释放: %d/%d/%d", gracefuls, terminates, releases)
	}
}

// 请求投递不出去（例如 dshd 没有控制台）：仍要等满宽限再强杀，并如实记 unavailable。
func TestStopChainUndeliverableRequestStillEscalates(t *testing.T) {
	native := &fakeTree{graceErr: errors.New("GenerateConsoleCtrlEvent: 没有控制台")}
	control := liveControl(t, native)

	out, err := control.stopWithGrace(100*time.Millisecond, make(chan struct{}))
	if err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if out.Delivery != DeliveryUnavailable {
		t.Fatalf("投递失败必须记 unavailable，不能假装礼数已尽: %+v", out)
	}
	if !out.Escalated {
		t.Fatalf("投递不出去也必须最终强杀: %+v", out)
	}
	if _, terminates, _ := native.counts(); terminates != 1 {
		t.Fatalf("强杀应只发生一次: %d", terminates)
	}
}

// 没有退出通道（PTY 之外的兜底）：用身份轮询判断宽限内是否退出。
func TestStopChainPollsIdentityWithoutExitChannel(t *testing.T) {
	native := &fakeTree{delivery: DeliveryCtrlBreak}
	control := liveControl(t, native)

	out, err := control.stopWithGrace(80*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if !out.Escalated || out.Graceful {
		t.Fatalf("root 仍在（本进程）时必须判强杀: %+v", out)
	}
}

// 已结束的树：重复停止必须明确拒绝，且不再投递任何终止请求。
func TestStopChainRejectsDuplicate(t *testing.T) {
	native := &fakeTree{delivery: DeliveryCtrlBreak}
	control := liveControl(t, native)
	exited := make(chan struct{})
	close(exited)
	if _, err := control.stopWithGrace(50*time.Millisecond, exited); err != nil {
		t.Fatalf("第一次停止失败: %v", err)
	}
	if _, err := control.stopWithGrace(50*time.Millisecond, exited); !errors.Is(err, spi.ErrJobFinished) {
		t.Fatalf("重复停止必须拒绝: %v", err)
	}
	if gracefuls, _, _ := native.counts(); gracefuls != 1 {
		t.Fatalf("重复停止不该再投递请求: %d", gracefuls)
	}
}

// 并发停止：第二次等第一次收尾后按「已结束」拒绝，不会重复强杀。
func TestStopChainSerializesConcurrentStop(t *testing.T) {
	native := &fakeTree{delivery: DeliveryCtrlBreak}
	control := liveControl(t, native)
	exited := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		close(exited)
	}()

	first := make(chan error, 1)
	go func() { _, err := control.stopWithGrace(2*time.Second, exited); first <- err }()
	time.Sleep(30 * time.Millisecond)
	if _, err := control.stopWithGrace(2*time.Second, exited); !errors.Is(err, spi.ErrJobFinished) {
		t.Fatalf("并发停止必须按已结束拒绝: %v", err)
	}
	if err := <-first; err != nil {
		t.Fatalf("第一次停止失败: %v", err)
	}
	if gracefuls, _, _ := native.counts(); gracefuls != 1 {
		t.Fatalf("并发停止只能投递一次请求: %d", gracefuls)
	}
}

// 真实 bash + 真实 Job Object：停止后**子孙进程也必须消失**。
//
// 这条钉住「宽限不等于放弃 Job 归属」：哪怕 root 是先礼后兵里自己走的，
// 收尾仍走 native.release()，Windows 的 KILL_ON_JOB_CLOSE 会把残留子进程一起收掉。
// 心跳文件比 PID 更可靠——MSYS 的 PID 与 Windows PID 不是同一个数，而且这里
// 关心的本来就是「树里还有没有活人」。
func TestBashStopKeepsJobObjectOwnership(t *testing.T) {
	requireBash(t)
	svc := newSvc(t)
	beat := filepath.ToSlash(filepath.Join(t.TempDir(), "heartbeat.txt"))
	id := startTestJob(t, svc, fmt.Sprintf(`i=0; while true; do echo $i > '%s'; i=$((i+1)); sleep 0.05; done`, beat))

	readBeat := func() string {
		raw, err := os.ReadFile(beat)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}
	deadline := time.Now().Add(10 * time.Second)
	for readBeat() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if readBeat() == "" {
		t.Fatalf("心跳文件没出现（%s）：测试无效", beat)
	}

	if err := svc.Stop(id); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	out, ok := svc.StopOutcome(id)
	if !ok {
		t.Fatal("停止结局必须可查")
	}
	if !out.Graceful && !out.Escalated {
		t.Fatalf("结局必须是两种之一: %+v", out)
	}

	time.Sleep(200 * time.Millisecond) // 让可能的收尾写入落盘
	settled := readBeat()
	time.Sleep(500 * time.Millisecond)
	if now := readBeat(); now != settled {
		t.Fatalf("停止后心跳仍在推进（%q → %q）：Job 归属被放弃了（结局 %s）", settled, now, out)
	}
}

// 宽限预算的解析：显式配置 > 环境变量 > 官方默认 3000ms，非法值回落默认。
func TestGraceBudgetResolution(t *testing.T) {
	t.Setenv(envGraceMS, "1500")
	if got := (Options{}).grace(); got != 1500*time.Millisecond {
		t.Fatalf("DSHD_BASH_GRACE_MS=1500 应解析成 1500ms，实际 %v", got)
	}
	for _, bad := range []string{"abc", "0", "-5", "  "} {
		t.Setenv(envGraceMS, bad)
		if got := (Options{}).grace(); got != DefaultGrace {
			t.Fatalf("非法值 %q 必须回落默认 %v，实际 %v", bad, DefaultGrace, got)
		}
	}
	if got := (Options{GraceBudget: 250 * time.Millisecond}).grace(); got != 250*time.Millisecond {
		t.Fatalf("显式配置优先，实际 %v", got)
	}
}

// job_kill 必须把「宽限内退出 / 强杀」如实报给模型：两个结局的下一步不同。
func TestJobKillReportsStopOutcome(t *testing.T) {
	requireBash(t)
	svc := newSvc(t)
	var logs []string
	var mu sync.Mutex
	svc.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	id := startTestJob(t, svc, `sleep 30`)

	res, err := (&jobKillTool{svc: svc}).Exec(context.Background(), json.RawMessage(`{"job_id":"`+id+`"}`))
	if err != nil {
		t.Fatalf("job_kill 失败: %v", err)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 形状变了: %#v", res.Data)
	}
	raw, ok := data["stop"]
	if !ok {
		t.Fatalf("job_kill 结果必须带停止结局（宽限内退出 vs 强杀）: %#v", data)
	}
	stop, ok := raw.(stopOutcomeData)
	if !ok {
		t.Fatalf("stop 字段类型: %#v", raw)
	}
	if stop.Delivery == "" || (!stop.Graceful && !stop.Escalated) {
		t.Fatalf("停止结局不完整: %+v", stop)
	}
	if !strings.Contains(res.Summary, ackPrefix) {
		t.Fatalf("摘要要点明结局: %q", res.Summary)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "停止任务") {
		t.Fatalf("停止结局必须落一条事件日志: %v", logs)
	}
}

// ackPrefix 是两种结局摘要的公共前缀（"已终止" / "已强杀"）。
const ackPrefix = "已"
