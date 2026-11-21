package bash

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/spi"
)

func startTestJob(t *testing.T, svc *Service, command string) string {
	t.Helper()
	res, err := svc.Run(command, 25*time.Millisecond)
	if err != nil {
		t.Fatalf("启动后台任务失败: %v", err)
	}
	if res.JobID == "" {
		t.Fatalf("命令没有转入后台: %+v", res)
	}
	return res.JobID
}

// waitJobEnded 用输出分页 API 反复续读，直到任务**真的结算**（Done=true）或超时，
// 返回最后一页。
//
// 为什么不能只调一次 OutputPage 就断言 Done：`wait` 只承诺"有新输出或已结束"就
// 返回（service.go:308 `if page.Output != "" || page.Done || …`）。而任务的输出
// 管道是 `2>&1` 合流的（shellenv.go:138），**子进程运行时自己会往里写东西**——
// MSYS/Cygwin 在并发起进程的压力下偶发（实测原文，pid/地址每次不同）：
//
//	0 [main] sleep (54480) child_copy: cygheap read copy failed,
//	0x800000000..0x80000B9D0, done 0, windows pid 54480, Win32 error 299
//
// （ERROR_PARTIAL_COPY：子进程拷父进程 cygheap 时失败）。这类字节与我们的代码无关，
// 但会先于"结束"进入缓冲：调用方一看到它，第一次 OutputPage 就带着 Done=false 回来，
// 断言随之变红——而任务其实毫秒之后就结算了（实测失败那次的 OutputPage 根本没等到
// 10s，整条用例 0.10s 就红了）。
//
// 所以"等到结束"必须自己续读：下一轮从 NextCursor 接着等（官方语义就是这样用的，
// 见 spi.JobMaxPageWait："调用方可用 nextCursor 继续等"）。判据不变——最终仍必须
// 读到 Done，等不到照样红。
//
// 返回的是**最后一页**；是否真的读到了 ended 由调用方断言，不在这里替它下结论。
func waitJobEnded(t *testing.T, svc *Service, id string, timeout time.Duration) spi.JobOutputPage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var page spi.JobOutputPage
	for {
		started := time.Now()
		next, err := svc.OutputPage(id, page.NextCursor, spi.JobDefaultPageLimit, time.Until(deadline))
		if err != nil {
			t.Fatalf("读任务 %s 输出失败: %v", id, err)
		}
		page = next
		if page.Done || !time.Now().Before(deadline) {
			return page
		}
		// 和宿主自己的适配器一样留一个下限（cmd/dshd/jobs_out.go:183）：Pager 若因为
		// "一直有新输出"而立即返回，这里不忙等占满 CPU。
		if time.Since(started) < time.Millisecond {
			time.Sleep(time.Millisecond)
		}
	}
}

func TestOutputPageAfterDoneAndBoundaries(t *testing.T) {
	requireBash(t)
	svc := newSvc(t)
	id := startTestJob(t, svc, `printf 0123456789abcde; sleep 0.15`)
	legacy, err := svc.Output(id, 3*time.Second)
	if err != nil || !legacy.Done {
		t.Fatalf("等待结束失败: %+v, %v", legacy, err)
	}

	var want = []struct {
		cursor int64
		text   string
		next   int64
	}{{0, "0123", 4}, {4, "4567", 8}, {8, "89ab", 12}, {12, "cde", 15}, {15, "", 15}}
	for _, tc := range want {
		page, err := svc.OutputPage(id, tc.cursor, 4, 0)
		if err != nil {
			t.Fatalf("cursor=%d: %v", tc.cursor, err)
		}
		if page.Output != tc.text || page.Offset != tc.cursor || page.NextCursor != tc.next || page.Total != 15 || !page.Done || page.ExitCode != 0 || page.Truncated {
			t.Fatalf("cursor=%d 页错误: %+v", tc.cursor, page)
		}
		again, err := svc.OutputPage(id, tc.cursor, 4, 0)
		if err != nil || again != page {
			t.Fatalf("结束后同游标必须幂等: first=%+v again=%+v err=%v", page, again, err)
		}
	}
	if _, err := svc.OutputPage(id, -1, 4, 0); !errors.Is(err, spi.ErrJobInvalidCursor) {
		t.Fatalf("负游标必须拒绝: %v", err)
	}
	if _, err := svc.OutputPage(id, 16, 4, 0); !errors.Is(err, spi.ErrJobInvalidCursor) {
		t.Fatalf("越过 total 的游标必须拒绝: %v", err)
	}
	if _, err := svc.OutputPage(id, 0, -1, 0); err == nil {
		t.Fatal("负 limit 必须拒绝")
	}
	page, err := svc.OutputPage(id, 0, spi.JobMaxPageLimit+1, 0)
	if err != nil || len(page.Output) > spi.JobMaxPageLimit {
		t.Fatalf("limit 必须有硬上限: %+v, %v", page, err)
	}
}

func TestOutputPageWaitsForOutputAndDone(t *testing.T) {
	requireBash(t)
	svc := newSvc(t)
	id := startTestJob(t, svc, `sleep 0.1; printf ok`)
	// 判据是"**没有等满等待窗**"，不是"这台机器在 1.5s 内跑完"。
	// 等待窗 2s → 30s、阈值 1.5s → 5s：两个数一起放大，判据的区分力不变
	// （真的等满窗口 ⇒ 30s 远大于 5s，照样红），但对满载机器留出噪声余量。
	start := time.Now()
	page, err := svc.OutputPage(id, 0, 4, 30*time.Second)
	if err != nil || page.Output != "ok" || page.NextCursor != 2 {
		t.Fatalf("等待输出失败: %+v, %v", page, err)
	}
	// 新输出和结束哨兵可以分两次到；第二页必须一直等到真实 done。
	ended, err := svc.OutputPage(id, page.NextCursor, 4, 30*time.Second)
	if err != nil || !ended.Done || ended.ExitCode != 0 || ended.Total != 2 || ended.NextCursor != 2 {
		t.Fatalf("等待 done 失败: %+v, %v", ended, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("结束后不应继续等满 wait: %s", elapsed)
	}
}

func TestStopUnknownCrossInstanceAndDuplicate(t *testing.T) {
	requireBash(t)
	a := newSvc(t)
	b := newSvc(t)
	idA := startTestJob(t, a, `sleep 30`)
	idB := startTestJob(t, b, `sleep 30`)
	if idA == idB {
		t.Fatalf("不同 Service 不能分配相同任务 id: %q", idA)
	}
	if err := b.Stop(idA); !errors.Is(err, spi.ErrJobWrongInstance) {
		t.Fatalf("跨实例停止必须拒绝: %v", err)
	}
	if page, err := b.OutputPage(idB, 0, 1, 0); err != nil || page.Done {
		t.Fatalf("跨实例拒绝不得误杀本实例任务: %+v, %v", page, err)
	}
	if err := a.Stop("missing"); !errors.Is(err, spi.ErrJobNotFound) {
		t.Fatalf("未知 id 必须返回 not-found: %v", err)
	}
	if err := a.Stop(idA); err != nil {
		t.Fatalf("第一次停止失败: %v", err)
	}
	if err := a.Stop(idA); !errors.Is(err, spi.ErrJobFinished) {
		t.Fatalf("重复停止必须明确拒绝: %v", err)
	}
	// 判据是"停止后**最终**能读到 ended"。给 10s 而不是 2s：这条会真起
	// `sleep 30` 子进程，满载（并行包 + 覆盖率/race 构建）时收尾结算可能远超 2s，
	// 2s 会误报成"停止没生效"；而"停止真的没生效"的表现是这条永远读不到 ended。
	//
	// 走 waitJobEnded 而不是单次 OutputPage：停止后在结算完成之前，管道里可能先到
	// 一个字节（MSYS 运行时写的 stderr 告警，见该函数说明），单次调用会立刻带着
	// Done=false 返回。续读到 Done 才是"读到 ended"这个判据的真实含义。
	page := waitJobEnded(t, a, idA, 10*time.Second)
	if !page.Done {
		t.Fatalf("停止后应可读 ended 状态: %+v", page)
	}

	doneID := startTestJob(t, a, `printf done; sleep 0.1`)
	// 同上：0.1s 的命令在满载机器上排队/启动可能就要几秒。
	if out, err := a.Output(doneID, 10*time.Second); err != nil || !out.Done {
		t.Fatalf("等待自然结束: %+v, %v", out, err)
	}
	if err := a.Stop(doneID); !errors.Is(err, spi.ErrJobFinished) {
		t.Fatalf("自然结束后停止必须拒绝: %v", err)
	}
}

func TestStopRejectsPIDReuse(t *testing.T) {
	requireBash(t)
	svc := newSvc(t)
	id := startTestJob(t, svc, `read _`)
	j, err := svc.job(id)
	if err != nil {
		t.Fatal(err)
	}
	control := j.sess.control
	control.mu.Lock()
	original := control.identity
	control.identity.CreationID = original.CreationID + "-stale"
	control.mu.Unlock()
	t.Cleanup(func() {
		control.mu.Lock()
		control.identity = original
		control.mu.Unlock()
	})

	if err := svc.Stop(id); !errors.Is(err, spi.ErrJobProcessReused) {
		t.Fatalf("创建代次不匹配必须按 PID 复用拒绝: %v", err)
	}
	if page, err := svc.OutputPage(id, 0, 1, 0); err != nil || page.Done {
		t.Fatalf("身份不匹配不得杀掉真实进程: %+v, %v", page, err)
	}
	control.mu.Lock()
	control.identity = original
	control.mu.Unlock()
	if err := svc.Stop(id); err != nil {
		t.Fatalf("恢复正确代次后应能安全停止: %v", err)
	}
}

func TestProcessControlRejectsExitedRoot(t *testing.T) {
	requireBash(t)
	svc := newSvc(t)
	id := startTestJob(t, svc, `read _`)
	j, err := svc.job(id)
	if err != nil {
		t.Fatal(err)
	}
	identity := j.sess.control.metadata()
	current, err := queryProcessIdentity(identity.PID)
	if err != nil || validateProcessIdentity(identity, current) != nil {
		t.Fatalf("杀测试 root 前必须核对归属: %+v, %v", current, err)
	}
	// 仅测试进程直接杀自己刚核对的 bash root，制造真实已退出分支。
	if err := j.sess.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-j.sess.waitDone
	if err := j.sess.control.stop(); !errors.Is(err, spi.ErrJobProcessExited) {
		t.Fatalf("已退出 root 必须拒绝: %v", err)
	}
}

func TestJobMetadataIsReal(t *testing.T) {
	requireBash(t)
	svc := newSvc(t)
	id := startTestJob(t, svc, `sleep 30`)
	meta, err := svc.Metadata(id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != id || meta.OwnerID == "" || !strings.Contains(id, meta.OwnerID) || meta.RootPID <= 0 || meta.RootImage == "" || meta.CreationID == "" {
		t.Fatalf("元数据缺真实归属: %+v", meta)
	}
	if !strings.Contains(strings.ToLower(meta.RootImage), "bash") {
		t.Fatalf("root 映像不是本次启动的 bash: %+v", meta)
	}
	if runtime.GOOS == "windows" && meta.ProcessTree != "windows-job-object" {
		t.Fatalf("Windows 必须记录 Job Object: %+v", meta)
	}
	if runtime.GOOS != "windows" && (meta.ProcessTree != "unix-process-group" || meta.ProcessGroupID != meta.RootPID) {
		t.Fatalf("Unix 必须记录独立 process group: %+v", meta)
	}
	list := svc.List()
	if len(list) != 1 || list[0].RootPID != meta.RootPID || list[0].CreationID != meta.CreationID || list[0].OwnerID != meta.OwnerID {
		t.Fatalf("List 与 Metadata 不能分叉: list=%+v meta=%+v", list, meta)
	}
}

func TestConcurrentOutputMetadataAndStop(t *testing.T) {
	requireBash(t)
	svc := newSvc(t)
	id := startTestJob(t, svc, `i=0; while [ "$i" -lt 1000 ]; do printf x; i=$((i+1)); sleep 0.005; done`)
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var cursor int64
			for {
				page, err := svc.OutputPage(id, cursor, 32, 0)
				if err != nil {
					errCh <- err
					return
				}
				if page.NextCursor < cursor {
					errCh <- fmt.Errorf("游标倒退: %+v", page)
					return
				}
				cursor = page.NextCursor
				if _, err := svc.Metadata(id); err != nil {
					errCh <- err
					return
				}
				if page.Done {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	time.Sleep(30 * time.Millisecond)
	if err := svc.Stop(id); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestTaskOutputRetentionIsBounded(t *testing.T) {
	task := newTask(1)
	payload := []byte(strings.Repeat("x", maxRetainedJobOutput+123))
	task.append(payload)
	task.finish(0, "")
	base, total, truncated := task.retention()
	if total != int64(len(payload)) || base != 123 || !truncated || task.bufferedBytes() != maxRetainedJobOutput {
		t.Fatalf("保留窗口错误: base=%d total=%d truncated=%v buffered=%d", base, total, truncated, task.bufferedBytes())
	}
	svc := NewService(t.TempDir())
	id, err := svc.register("bounded", nil, task)
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.OutputPage(id, 0, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Offset != 123 || page.NextCursor != 128 || page.Total != total || !page.Truncated || !page.Done || page.Output != "xxxxx" {
		t.Fatalf("过期游标必须明确跳到保留起点: %+v", page)
	}
}

func buildTreeHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	exe := filepath.Join(dir, "tree-helper")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	const source = `package main
import (
 "fmt"
 "os"
 "os/exec"
 "path/filepath"
 "strconv"
 "time"
)
func record(dir, mode string) {
 p := filepath.Join(dir, mode+".pid")
 if err := os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil { panic(err) }
}
func main() {
 if len(os.Args) != 3 { panic("mode dir required") }
 mode, dir := os.Args[1], os.Args[2]
 // DSHBASH_HELPER_DELAY_MS 是**故障注入**旋钮，默认 0（不设 = 与以前逐字节一致）：
 // 用它模拟"2 核 CI runner 上这个 helper 要好几秒才变得可观察"。
 // 有了它，realProcWait 的修复可以被**确定性地**验：同样的延迟下，旧预算红、新预算绿。
 if ms, err := strconv.Atoi(os.Getenv("DSHBASH_HELPER_DELAY_MS")); err == nil && ms > 0 {
  time.Sleep(time.Duration(ms) * time.Millisecond)
 }
 if mode == "parent" {
  c := exec.Command(os.Args[0], "child", dir)
  if err := c.Start(); err != nil { panic(err) }
  record(dir, "parent")
  record(dir, "parent-child")
  _ = c.Process.Release()
 } else if mode == "child" { record(dir, "child") } else { panic("bad mode") }
 deadline := time.Now().Add(30*time.Second)
 for time.Now().Before(deadline) {
  _ = os.WriteFile(filepath.Join(dir, mode+".beat"), []byte(fmt.Sprintf("%d %d", os.Getpid(), time.Now().UnixNano())), 0600)
  time.Sleep(20*time.Millisecond)
 }
}
`
	if err := os.WriteFile(src, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", exe, src)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建临时 helper 失败: %v\n%s", err, out)
	}
	return exe
}

func bashQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type recordedPID struct {
	pid      int
	identity processIdentity
	name     string
}

// realProcWait 是本包**真进程**用例的统一等待预算：等"某个真进程出现了/消失了"。
//
// # 为什么需要它（task-86，2026-11-19）
//
// 这里原本是写死的 `5 * time.Second`：本机 16 核上一直够用，于是看起来没问题。
// 但 CI runner 只有 2 核，还套着 `-race`，测试自己又要 `go build` 一个 helper 再起 bash
// 子进程；未修改的树上实测就红过：
//
//	--- FAIL: TestRealHelperProcessTreeStopAndDispose/stop (5.06s)
//	    process_tree_test.go:391: 等待 helper parent PID/身份超时
//
// 5.06s —— 正好是预算用完的那一刻，也就是"机器比预算慢"而不是"产品没把进程起来"。
// 这与 `cmd/dshd` 的 `realChainWait` 是同一类问题：**超时是环境容差，不是行为契约**。
//
// # 它不降低任何断言强度
//
// 事件没发生照样 fail-closed（进程没起来 / 没退出都还是红），只是给慢机器更多时间；
// 每个用例的**内容**断言一条都没动。要调就调这一处，别再散写字面量。
const realProcWait = 60 * time.Second

func recordHelperPID(t *testing.T, dir, name string) recordedPID {
	t.Helper()
	path := filepath.Join(dir, name+".pid")
	deadline := time.Now().Add(realProcWait)
	for {
		if b, err := os.ReadFile(path); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil && pid > 0 {
				identity, err := queryProcessIdentity(pid)
				if err == nil && identity.Active {
					r := recordedPID{pid: pid, identity: identity, name: name}
					t.Logf("OWNED_HELPER name=%s pid=%d image=%q creation=%q", name, pid, identity.Image, identity.CreationID)
					return r
				}
			}
		}
		if time.Now().After(deadline) {
			// 失败时把"卡在哪一步"说清楚：是 pid 文件根本没出现（helper 没起来），
			// 还是文件在但进程身份查不到（起来了但立刻没了）。
			_, statErr := os.Stat(path)
			t.Fatalf("等待 helper %s PID/身份超时（预算 %v，pid 文件 %s：%v）",
				name, realProcWait, path, statErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitOwnedProcessExit(t *testing.T, r recordedPID) {
	t.Helper()
	deadline := time.Now().Add(realProcWait)
	for {
		current, err := queryProcessIdentity(r.pid)
		if err != nil || !current.Active || current.CreationID != r.identity.CreationID {
			t.Logf("OWNED_HELPER_EXIT name=%s pid=%d creation=%q", r.name, r.pid, r.identity.CreationID)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("归属进程仍存活（等了 %v）: name=%s pid=%d current=%+v", realProcWait, r.name, r.pid, current)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRealHelperProcessTreeStopAndDispose(t *testing.T) {
	requireBash(t)
	helper := buildTreeHelper(t)
	for _, mode := range []string{"stop", "dispose"} {
		t.Run(mode, func(t *testing.T) {
			svc := newSvc(t)
			dir := t.TempDir()
			command := bashQuote(filepath.ToSlash(helper)) + " parent " + bashQuote(filepath.ToSlash(dir))
			id := startTestJob(t, svc, command)
			parent := recordHelperPID(t, dir, "parent")
			child := recordHelperPID(t, dir, "child")
			meta, err := svc.Metadata(id)
			if err != nil {
				t.Fatal(err)
			}
			root := recordedPID{pid: meta.RootPID, name: "bash-root"}
			root.identity, err = queryProcessIdentity(root.pid)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				svc.Close()
				for _, r := range []recordedPID{child, parent} {
					cleanupOwnedTestProcess(r)
				}
			})

			if mode == "stop" {
				if err := svc.Stop(id); err != nil {
					t.Fatalf("停止进程树失败: %v", err)
				}
			} else {
				svc.Close()
			}
			waitOwnedProcessExit(t, root)
			waitOwnedProcessExit(t, parent)
			waitOwnedProcessExit(t, child)
			page, err := svc.OutputPage(id, 0, 1, 2*time.Second)
			if err != nil || !page.Done {
				t.Fatalf("收工后输出仍应可读: %+v, %v", page, err)
			}
		})
	}
}
