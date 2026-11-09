package extern_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/cordis/extern"
)

func pluginFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"fixture-plugin","version":"1.2.3","main":"host.cjs"}`)
	write("host.cjs", `
module.exports = {
  echo(args) { return { echo: args.text === undefined ? null : args.text, n: args.n === undefined ? null : args.n }; },
  fail() { throw new Error('fixture boom'); },
  slow(args) { return new Promise(r => setTimeout(() => r({slept: args.ms || 0}), args.ms || 0)); },
};
`)
	write("client/client.js", "export const fixture = 'client-half';\n")
	return dir
}

func startClient(t *testing.T) *extern.Client {
	t.Helper()
	c, err := extern.Start(context.Background(), extern.Options{})
	if err != nil {
		if strings.Contains(err.Error(), "node") || strings.Contains(err.Error(), "不可用") {
			t.Skipf("本机无可用 node：%v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestLoadRealPackageAndMethods(t *testing.T) {
	c := startClient(t)
	dir := pluginFixture(t)
	info, err := c.LoadPackage(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "fixture-plugin" || info.Version != "1.2.3" || info.PID != c.PID() {
		t.Fatalf("info=%+v", info)
	}
	if strings.Join(info.Methods, ",") != "echo,fail,slow" {
		t.Fatalf("methods=%v", info.Methods)
	}
}

func TestGetClientCodeIsByteExact(t *testing.T) {
	c := startClient(t)
	dir := pluginFixture(t)
	if _, err := c.LoadPackage(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	code, err := c.GetClientCode(context.Background(), dir, "client/client.js")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(dir, "client", "client.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(code) != string(want) {
		t.Fatalf("client 字节不一致: %q != %q", code, want)
	}
	if _, err := c.GetClientCode(context.Background(), dir, "../../etc/passwd"); err == nil {
		t.Fatal("越界 client 路径必须失败")
	}
}

func TestInvokeRunRecordSettleAndInspect(t *testing.T) {
	c := startClient(t)
	dir := pluginFixture(t)
	if _, err := c.LoadPackage(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	var out struct{ Echo, N any }
	rec, err := c.Invoke(context.Background(), "req-1", "echo", map[string]any{"text": "hello", "n": 7}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Echo != "hello" || out.N != float64(7) {
		t.Fatalf("invoke 结果=%+v", out)
	}
	if rec.RequestID != "req-1" || rec.ExitCode != 0 || rec.PID != c.PID() || rec.Settled {
		t.Fatalf("run record=%+v", rec)
	}
	got, ok := c.ResolveRequestRun("req-1")
	if !ok || got.RequestID != "req-1" {
		t.Fatalf("resolve=%+v ok=%v", got, ok)
	}
	if _, ok := c.ResolveRequestRun("unknown"); ok {
		t.Fatal("未知 requestId 不能成功")
	}
	settled, err := c.SettleUserRun(context.Background(), "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if !settled.Settled || settled.ExitCode != 0 || settled.PID != c.PID() {
		t.Fatalf("settle=%+v", settled)
	}
	if _, err := c.SettleUserRun(context.Background(), "unknown"); err == nil {
		t.Fatal("未知 requestId settle 必须失败")
	}
	inspect, err := c.ResolveInspectQuery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Name != "fixture-plugin" || len(inspect.Methods) != 3 {
		t.Fatalf("inspect=%+v", inspect)
	}
}

func TestInvokePluginErrorIsStructured(t *testing.T) {
	c := startClient(t)
	dir := pluginFixture(t)
	if _, err := c.LoadPackage(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	_, err := c.Invoke(context.Background(), "req-fail", "fail", nil, nil)
	var rpc *extern.RPCError
	if !errors.As(err, &rpc) || rpc.Code == 0 || !strings.Contains(err.Error(), "fixture boom") {
		t.Fatalf("插件错误必须是结构化 RPCError，得到 %v", err)
	}
}

func TestInvokeTimeoutKeepsProcess(t *testing.T) {
	c := startClient(t)
	dir := pluginFixture(t)
	if _, err := c.LoadPackage(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := c.Invoke(ctx, "req-slow", "slow", map[string]any{"ms": 5000}, nil)
	if !errors.Is(err, extern.ErrTimeout) {
		t.Fatalf("应为 ErrTimeout，得到 %v", err)
	}
	if !c.Alive() {
		t.Fatal("超时不应杀进程")
	}
	if _, err := c.Invoke(context.Background(), "req-after", "echo", map[string]any{"text": "alive"}, &json.RawMessage{}); err != nil {
		t.Fatalf("超时后后续调用应正常：%v", err)
	}
}
