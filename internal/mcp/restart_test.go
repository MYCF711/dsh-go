package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeController 是可编排的宿主侧记录：restart 只会通过它动真实进程。
type fakeController struct {
	disposed bool
	exists   bool
	enabled  bool
	stopSt   CloseResult
	stopOK   bool
	mountErr error

	calls    []string
	publishd []Status
}

func (c *fakeController) Disposed() bool      { return c.disposed }
func (c *fakeController) Exists(string) bool  { return c.exists }
func (c *fakeController) Enabled(string) bool { return c.enabled }
func (c *fakeController) Mount(string) error  { c.calls = append(c.calls, "mount"); return c.mountErr }
func (c *fakeController) Publish(_ string, st Status) error {
	c.calls = append(c.calls, "publish:"+st.State)
	c.publishd = append(c.publishd, st)
	return nil
}
func (c *fakeController) Stop(string) (CloseResult, bool) {
	c.calls = append(c.calls, "stop")
	return c.stopSt, c.stopOK
}

// TestRestartStopsThenMounts 钉住官方 restart 的顺序：先 dispose 旧一代，
// 再挂新一代（dsh-mcp-manager/src/index.ts:348 → remount:481）。
func TestRestartStopsThenMounts(t *testing.T) {
	c := &fakeController{exists: true, enabled: true, stopOK: true,
		stopSt: CloseResult{CloseConfirmed: true}}
	if err := Restart(context.Background(), c, "files"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if strings.Join(c.calls, ",") != "stop,mount" {
		t.Fatalf("顺序必须是 stop→mount，得到 %v", c.calls)
	}
}

// TestRestartRejectsUnknownServer 钉住官方错误原文：
// `mcp-manager: unknown server "x" — it is neither configured nor registered`。
func TestRestartRejectsUnknownServer(t *testing.T) {
	c := &fakeController{exists: false}
	err := Restart(context.Background(), c, "ghost")
	if err == nil {
		t.Fatal("未知 server 必须报错")
	}
	if kind, ok := KindOf(err); !ok || kind != KindUnknownServer {
		t.Fatalf("错误分类不对: %v", err)
	}
	if !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("错误里必须点名 server: %v", err)
	}
	if len(c.calls) != 0 {
		t.Fatalf("未知 server 不许动任何实例: %v", c.calls)
	}
}

// TestRestartRejectsDisposedManager 钉住官方 `the manager is disposed`。
func TestRestartRejectsDisposedManager(t *testing.T) {
	c := &fakeController{disposed: true, exists: true, enabled: true}
	err := Restart(context.Background(), c, "files")
	if kind, ok := KindOf(err); !ok || kind != KindDisposed {
		t.Fatalf("错误分类不对: %v", err)
	}
	if len(c.calls) != 0 {
		t.Fatalf("已释放的管理器不许动实例: %v", c.calls)
	}
}

// TestRestartBlocksOnIncompleteCleanup 钉住官方那条硬规则：
// "Incomplete cleanup blocks replacement until the Host restarts."
// 此时**不许**挂新一代（两个实例抢同一个 tool namespace 比不重启更糟），
// 并把 failed/cleanup 状态发布出去，让管理面看得见原因。
func TestRestartBlocksOnIncompleteCleanup(t *testing.T) {
	for name, st := range map[string]CloseResult{
		"关闭未确认":     {CloseConfirmed: false},
		"关闭确认但清理失败": {CloseConfirmed: true, CleanupError: "kill 超时"},
	} {
		t.Run(name, func(t *testing.T) {
			c := &fakeController{exists: true, enabled: true, stopOK: false, stopSt: st}
			err := Restart(context.Background(), c, "files")
			if kind, ok := KindOf(err); !ok || kind != KindCleanupIncomplete {
				t.Fatalf("错误分类不对: %v", err)
			}
			if !strings.Contains(err.Error(), `"files"`) {
				t.Fatalf("错误里必须点名 server: %v", err)
			}
			for _, call := range c.calls {
				if call == "mount" {
					t.Fatalf("清理未完成却挂了新一代: %v", c.calls)
				}
			}
			if len(c.publishd) != 1 || c.publishd[0].State != StateFailed ||
				c.publishd[0].Reason != ReasonCleanup || c.publishd[0].Close != st {
				t.Fatalf("必须发布 failed/cleanup 并带上清理结果: %+v", c.publishd)
			}
		})
	}
}

// TestRestartDisabledEntryDoesNotMount 钉住官方 remount 的 disabled 分支：
// 旧一代照样收掉，新一代不挂（禁用就是禁用），restart 本身**不是错误**。
func TestRestartDisabledEntryDoesNotMount(t *testing.T) {
	c := &fakeController{exists: true, enabled: false, stopOK: true,
		stopSt: CloseResult{CloseConfirmed: true}}
	if err := Restart(context.Background(), c, "files"); err != nil {
		t.Fatalf("禁用 server 的 restart 不该报错: %v", err)
	}
	if strings.Join(c.calls, ",") != "stop,publish:disabled" {
		t.Fatalf("应该是 stop→发布 disabled，得到 %v", c.calls)
	}
}

// TestRestartPropagatesMountFailure 钉住"挂不起来必须报错"：
// 静默成功会让管理面以为 server 已经在跑。
func TestRestartPropagatesMountFailure(t *testing.T) {
	boom := errors.New("spawn ENOENT")
	c := &fakeController{exists: true, enabled: true, stopOK: true,
		stopSt: CloseResult{CloseConfirmed: true}, mountErr: boom}
	if err := Restart(context.Background(), c, "files"); !errors.Is(err, boom) {
		t.Fatalf("挂载失败必须原样返回: %v", err)
	}
}

// TestRestartHonorsCanceledContext 钉住取消：已经取消的调用不许开始动进程。
func TestRestartHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &fakeController{exists: true, enabled: true, stopOK: true,
		stopSt: CloseResult{CloseConfirmed: true}}
	if err := Restart(ctx, c, "files"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消必须原样返回: %v", err)
	}
	if len(c.calls) != 0 {
		t.Fatalf("取消后不许动实例: %v", c.calls)
	}
}
