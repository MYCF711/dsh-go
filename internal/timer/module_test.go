package timer

// 官方 #3 `timer` 的**接线验收**。
//
// 补这一层之前 `internal/timer` 零生产 importer：实现是真的，但没有任何模块
// 拿得到它。所以这里的判据不是"实现能跑"（`scope_test.go` 已覆盖），而是：
//
//  1. 走 `kernel.Module` 真的装得上，别的模块真的**注入得到一个可用的定时器**；
//  2. 内核作用域撤销 = 定时器全部停掉，**而且**等到正在跑的回调退出（不是
//     "标记一下就走"）；
//  3. 官方 `ctx.timeout(delay)` 的等待形状（`Wait`）取消后 channel 不关闭——
//     那是"取消成功"和"到点了"唯一可区分的证据。

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dshgo/internal/kernel"
)

// 消费者模块：像真实插件那样**通过注入**拿定时器，而不是 import 包里的变量。
//
// 这就是"接线"的定义——模块只拿 *kernel.Scope（R2），经 Key 注入拿到服务。
func consumerModule(started *int32, ran *int32) kernel.Module {
	return kernel.Module{
		ID:       "test-timer-consumer",
		Backend:  kernel.BackendStatic,
		Requires: []string{Key.String()},
		Register: func(s *kernel.Scope) error {
			scope, err := kernel.Inject(s, Key)
			if err != nil {
				return err
			}
			if _, err := scope.Every(20*time.Millisecond, func() {
				atomic.AddInt32(started, 1)
				atomic.AddInt32(ran, 1)
			}); err != nil {
				return err
			}
			return nil
		},
	}
}

// mount 走**真实装配**（`kernel.Assemble`：能力解析 → 拓扑排序 → 实例化），
// 不是手工拼一个 scope。这样"消费者能注入到 timer"才有意义：它证明依赖关系
// 在装配期就被解析过，而不是测试自己把两个东西摆在一起。
func mount(t *testing.T, mods ...kernel.Module) *kernel.Scope {
	t.Helper()
	k := kernel.New("timer-test")
	if _, err := kernel.Assemble(k.Root(), mods); err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	return k.Root()
}

// 走模块装配 → 消费者注入 → 定时器真的在跳。
func TestModuleProvidesTimerToConsumers(t *testing.T) {
	var started, ran int32
	_ = started
	root := mount(t, Module(), consumerModule(&started, &ran))
	defer root.Dispose()

	// 注入路径本身必须是通的（不是"模块装上了但别人注入不到"）。
	scope, err := kernel.Inject(root, Key)
	if err != nil {
		t.Fatalf("注入 timer 失败：%v", err)
	}
	if scope == nil {
		t.Fatal("注入到 nil timer")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&ran) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&ran); got < 2 {
		t.Fatalf("消费者注册的 Every 没有真的在跳（跑了 %d 次）", got)
	}
}

// 内核作用域关闭 = 定时器停掉，**且**等到正在跑的回调退出。
//
// 这是本能力存在的理由：ad-hoc ticker 的生命周期绑在进程上，模块卸载后回调还在
// 摸已经失效的句柄。这条测试钉的就是"关闭之后真的不会再有人碰我的状态"。
func TestModuleCloseReleasesAndWaitsForRunningCallback(t *testing.T) {
	var (
		mu       sync.Mutex
		inFlight bool
		after    int32
	)
	entered := make(chan struct{}, 1)
	root := mount(t, Module(), kernel.Module{
		ID:       "test-timer-slow-consumer",
		Backend:  kernel.BackendStatic,
		Requires: []string{Key.String()},
		Register: func(s *kernel.Scope) error {
			scope, err := kernel.Inject(s, Key)
			if err != nil {
				return err
			}
			_, err = scope.Every(5*time.Millisecond, func() {
				mu.Lock()
				inFlight = true
				mu.Unlock()
				select {
				case entered <- struct{}{}:
				default:
				}
				time.Sleep(150 * time.Millisecond)
				mu.Lock()
				inFlight = false
				mu.Unlock()
				atomic.AddInt32(&after, 1)
			})
			return err
		},
	})

	// 等第一次回调进到"正在跑"。
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		root.Dispose()
		t.Fatal("定时器没有在预期时间内触发")
	}

	root.Dispose() // 必须阻塞到正在跑的回调结束

	mu.Lock()
	stillInFlight := inFlight
	mu.Unlock()
	if stillInFlight {
		t.Fatal("作用域关闭返回之后回调仍在跑：Release 没有等到它结束")
	}

	scope, err := kernel.Inject(root, Key)
	if err == nil {
		// 作用域已经关闭，注入不该再拿到活着的服务；退一步也必须拿到"已释放"。
		if scope != nil && !scope.Released() {
			t.Fatal("作用域关闭之后定时器没有被释放")
		}
	}
}

// 官方 `ctx.timeout(delay)` 的等待形状：到点关闭 Done；取消之后 Done 不关闭。
func TestWaitMatchesOfficialTimeoutShape(t *testing.T) {
	scope := NewScope()
	defer scope.Release()

	// 到点：Done 被关闭。
	w, err := scope.Wait(10 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait 到点后 Done 没有被关闭（官方 ctx.timeout(delay) 语义）")
	}

	// 取消：Done **不**关闭（关闭 = 到点，这两件事必须可区分）。
	cancelled, err := scope.Wait(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !cancelled.Cancel() {
		t.Fatal("Cancel 应当报告真的撤掉了")
	}
	select {
	case <-cancelled.Done:
		t.Fatal("取消之后 Done 被关闭了——那会让消费者把它当成'到点了'")
	case <-time.After(50 * time.Millisecond):
	}
	if cancelled.Cancel() {
		t.Fatal("重复 Cancel 应当返回 false（已经撤过了）")
	}
}

// 释放之后的等待要 fail-loud（ErrClosed 可判），不能静默成功。
func TestWaitAfterReleaseFailsLoud(t *testing.T) {
	scope := NewScope()
	scope.Release()
	_, err := scope.Wait(time.Millisecond)
	if err == nil {
		t.Fatal("scope 已释放还起等待，必须报错")
	}
	if !IsClosed(err) {
		t.Fatalf("错误要能用 IsClosed 判定，实际 %v", err)
	}
}
