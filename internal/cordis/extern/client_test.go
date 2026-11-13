//go:build windows

package extern_test

// 先红后绿：这些测试在实现体存在之前就是红的（编译不过 = 红）。
// 覆盖：真实 node echo / 结构化错误 / 未知方法 / 超时 / 取消后仍可用 /
// 无 node 响亮降级 / Job Object 整树 kill 无残留 / 并发（配 -race）。

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

	"dshgo/internal/cordis/extern"
)

func mustStart(t *testing.T, opts extern.Options) *extern.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := extern.Start(ctx, opts)
	if err != nil {
		// CI runner 上不一定有可用的 node（2026-11-13 实测：`node --version` 退出 1，
		// 于是 `cordis/node-unusable`）。这**不是产品缺陷**——产品侧的降级行为由
		// `无 node 响亮降级` 那一条单独守；这里要的是"真 node 上的行为"，
		// 没有 node 就跳过，不能判红。与 plugin_test.go 的 startClient 同一口径。
		if strings.Contains(err.Error(), "node") || strings.Contains(err.Error(), "不可用") {
			t.Skipf("本机无可用 node：%v", err)
		}
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// 1) 真实 node 上的 echo：返回值必须等于入参（不是常量假值）。
func TestEchoOnRealNode(t *testing.T) {
	c := mustStart(t, extern.Options{})
	var out struct {
		Echo string `json:"echo"`
		N    int    `json:"n"`
	}
	err := c.Call(context.Background(), "echo", map[string]any{"text": "hello-cordis", "n": 7}, &out)
	if err != nil {
		t.Fatalf("Call echo: %v", err)
	}
	if out.Echo != "hello-cordis" || out.N != 7 {
		t.Fatalf("echo 不是真回显：%+v", out)
	}
	if c.PID() <= 0 {
		t.Fatalf("PID 应为真实子进程 pid，得到 %d", c.PID())
	}
}

// 2) 结构化错误：插件抛错必须变成带 code 的 *RPCError，不是字符串。
func TestStructuredErrorFromPlugin(t *testing.T) {
	c := mustStart(t, extern.Options{})
	err := c.Call(context.Background(), "boom", map[string]any{"why": "bad-input"}, nil)
	if err == nil {
		t.Fatal("boom 应该返回错误")
	}
	var rpc *extern.RPCError
	if !errors.As(err, &rpc) {
		t.Fatalf("错误必须是 *extern.RPCError，得到 %T: %v", err, err)
	}
	if rpc.Code == 0 || rpc.Message == "" {
		t.Fatalf("RPCError 缺 code/message：%+v", rpc)
	}
	if rpc.Method != "boom" {
		t.Fatalf("RPCError.Method 应为 boom，得到 %q", rpc.Method)
	}
}

// 3) 未知方法 → -32601（官方 JSON-RPC 语义，客户端可据此降级）。
func TestUnknownMethodCode(t *testing.T) {
	c := mustStart(t, extern.Options{})
	err := c.Call(context.Background(), "no.such.method", nil, nil)
	var rpc *extern.RPCError
	if !errors.As(err, &rpc) || rpc.Code != extern.CodeMethodNotFound {
		t.Fatalf("应回 %d，得到 %v", extern.CodeMethodNotFound, err)
	}
}

// 4) 超时：ctx 到期必须返回 ErrTimeout，且**不**污染后续调用。
func TestCallTimeout(t *testing.T) {
	c := mustStart(t, extern.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := c.Call(ctx, "sleep", map[string]any{"ms": 5000}, nil)
	if !errors.Is(err, extern.ErrTimeout) {
		t.Fatalf("应为 ErrTimeout，得到 %v", err)
	}
	// 超时后进程必须还活着，且新调用正常（取消只影响那一次）。
	var out struct {
		Echo string `json:"echo"`
	}
	if err := c.Call(context.Background(), "echo", map[string]any{"text": "after-timeout"}, &out); err != nil {
		t.Fatalf("超时后进程不可用：%v", err)
	}
	if out.Echo != "after-timeout" {
		t.Fatalf("超时后回显错：%+v", out)
	}
}

// 5) 取消：ctx 取消返回 ErrCanceled；进程存活，后续调用可用。
func TestCallCancelKeepsProcessAlive(t *testing.T) {
	c := mustStart(t, extern.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()
	err := c.Call(ctx, "sleep", map[string]any{"ms": 5000}, nil)
	if !errors.Is(err, extern.ErrCanceled) {
		t.Fatalf("应为 ErrCanceled，得到 %v", err)
	}
	if !c.Alive() {
		t.Fatal("取消单次调用不应杀死进程")
	}
	var out struct {
		Echo string `json:"echo"`
	}
	if err := c.Call(context.Background(), "echo", map[string]any{"text": "after-cancel"}, &out); err != nil {
		t.Fatalf("取消后进程不可用：%v", err)
	}
}

//  6. 无 node：必须**响亮**降级（可 errors.As 的结构化错误 + 可执行的 Hint），
//     不能静默回空成功，也不能只说 "exec: not found" 了事。
func TestMissingNodeIsLoud(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "definitely-no-node-here")
	_, err := extern.Start(context.Background(), extern.Options{NodePath: bogus})
	if err == nil {
		t.Fatal("node 不存在时必须返回错误")
	}
	var un *extern.UnavailableError
	if !errors.As(err, &un) {
		t.Fatalf("应为 *extern.UnavailableError，得到 %T: %v", err, err)
	}
	if un.Code != extern.CodeNodeMissing {
		t.Fatalf("错误码应为 %q，得到 %q", extern.CodeNodeMissing, un.Code)
	}
	if un.Hint == "" {
		t.Fatal("UnavailableError 必须带可执行 Hint（告诉用户装 node 或用 Go 路线）")
	}
}

// 7) 进程树：插件起孙进程后，Close 必须整树回收，不留孤儿。
func TestProcessTreeKilledOnClose(t *testing.T) {
	c := mustStart(t, extern.Options{})
	var out struct {
		ChildPID int `json:"childPid"`
	}
	if err := c.Call(context.Background(), "spawnChild", nil, &out); err != nil {
		t.Fatalf("spawnChild: %v", err)
	}
	if out.ChildPID <= 0 {
		t.Fatalf("拿不到孙进程 pid：%+v", out)
	}
	if !extern.ProcessAlive(out.ChildPID) {
		t.Fatal("孙进程刚起就查不到，判据不成立")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !extern.ProcessAlive(out.ChildPID) {
			return // 整树回收成功
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Close 后孙进程 %d 仍活着（Job Object 未收整棵树）", out.ChildPID)
}

// 8) 并发：多 goroutine 同时调用必须各自拿到自己的结果（配 -race 跑）。
func TestConcurrentCalls(t *testing.T) {
	c := mustStart(t, extern.Options{})
	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out struct {
				Echo string `json:"echo"`
			}
			want := fmt.Sprintf("m-%d", i)
			if err := c.Call(context.Background(), "echo", map[string]any{"text": want}, &out); err != nil {
				errs[i] = err
				return
			}
			if out.Echo != want {
				errs[i] = fmt.Errorf("第 %d 个调用拿到别人的结果：%q", i, out.Echo)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发调用 %d: %v", i, err)
		}
	}
}

// 9) 参数必须是合法 JSON 才能过线；非法值要本地结构化拒绝（不静默丢）。
func TestParamsMustMarshal(t *testing.T) {
	c := mustStart(t, extern.Options{})
	err := c.Call(context.Background(), "echo", map[string]any{"bad": make(chan int)}, nil)
	if err == nil {
		t.Fatal("chan 不可序列化，必须报错")
	}
	var perr *extern.ParamError
	if !errors.As(err, &perr) {
		t.Fatalf("应为 *extern.ParamError，得到 %T: %v", err, err)
	}
}

// 10) 关闭后再调用：必须是 ErrNotRunning，不是 panic 也不是挂起。
func TestCallAfterClose(t *testing.T) {
	c := mustStart(t, extern.Options{})
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := c.Call(context.Background(), "echo", map[string]any{"text": "x"}, nil)
	if !errors.Is(err, extern.ErrNotRunning) {
		t.Fatalf("关闭后应为 ErrNotRunning，得到 %v", err)
	}
	_ = json.RawMessage(nil)
	_ = os.Getpid
}

//  11. 真实"无 node"探针：把 PATH 清空后，发现层必须给 CodeNodeMissing。
//     （这条不走 NodePath 覆盖，验的是真实 PATH 查找路径。）
func TestMissingNodeByEmptyPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := extern.Start(context.Background(), extern.Options{})
	if err == nil {
		t.Fatal("PATH 里没有 node 时必须失败")
	}
	var un *extern.UnavailableError
	if !errors.As(err, &un) || un.Code != extern.CodeNodeMissing {
		t.Fatalf("应为 CodeNodeMissing 的 UnavailableError，得到 %v", err)
	}
	if !strings.Contains(un.Hint, "node") {
		t.Fatalf("Hint 必须提到 node：%q", un.Hint)
	}
}
