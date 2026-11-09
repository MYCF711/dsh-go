package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lineWriter 收集 stdout，并在出现 "listening on http://<addr>" 时把地址发到 channel。
type lineWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	addr chan string
	once sync.Once
}

func newLineWriter() *lineWriter {
	return &lineWriter{addr: make(chan string, 1)}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if idx := strings.Index(w.buf.String(), "listening on http://"); idx >= 0 {
		rest := w.buf.String()[idx+len("listening on http://"):]
		if end := strings.IndexAny(rest, "\r\n"); end >= 0 {
			addr := rest[:end]
			w.once.Do(func() { w.addr <- addr })
		}
	}
	return len(p), nil
}

func (w *lineWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// startRelay 起一个 in-process relayd，返回监听地址与取消函数。
func startRelay(t *testing.T, extraArgs ...string) (string, context.CancelFunc, *lineWriter, chan int) {
	t.Helper()
	home := t.TempDir()
	args := append([]string{"--addr", "127.0.0.1:0", "--home", home}, extraArgs...)
	ctx, cancel := context.WithCancel(context.Background())
	out := newLineWriter()
	exit := make(chan int, 1)
	var stderr bytes.Buffer
	go func() { exit <- run(ctx, args, out, &stderr) }()
	select {
	case addr := <-out.addr:
		t.Cleanup(cancel)
		return addr, cancel, out, exit
	case code := <-exit:
		t.Fatalf("relayd 退出码 = %d，stderr=%s", code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("relayd 10s 内没打印监听地址，stdout=%s", out.String())
	}
	return "", nil, nil, nil
}

func TestRunServesHealthAndShutsDown(t *testing.T) {
	addr, cancel, out, exit := startRelay(t, "--print-token")
	resp, err := http.Get("http://" + addr + "/v1/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("health = %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(out.String(), "owner token: ") {
		t.Fatalf("--print-token 没打印 token：%s", out.String())
	}
	cancel()
	select {
	case code := <-exit:
		if code != 0 {
			t.Fatalf("优雅退出返回 %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 relayd 没退出")
	}
	// 监听端口必须已释放。
	if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		conn.Close()
		t.Fatalf("退出后端口 %s 仍可连接", addr)
	}
}

func TestRunPersistsOwnerToken(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	out := newLineWriter()
	var stderr bytes.Buffer
	exit := make(chan int, 1)
	go func() {
		exit <- run(ctx, []string{"--addr", "127.0.0.1:0", "--home", home, "--print-token"}, out, &stderr)
	}()
	select {
	case <-out.addr:
	case <-time.After(10 * time.Second):
		t.Fatalf("relayd 没启动：%s", out.String())
	}
	cancel()
	if code := <-exit; code != 0 {
		t.Fatalf("退出码 = %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(home, "owner-token.json"))
	if err != nil {
		t.Fatalf("token 文件没落盘: %v", err)
	}
	printed := out.String()
	idx := strings.Index(printed, "owner token: ")
	if idx < 0 {
		t.Fatalf("没打印 token：%s", printed)
	}
	token := strings.TrimSpace(strings.SplitN(printed[idx+len("owner token: "):], "\n", 2)[0])
	if token == "" || !strings.Contains(string(raw), token) {
		t.Fatalf("token 文件与打印值不一致: file=%s token=%q", raw, token)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	badViewers := filepath.Join(t.TempDir(), "viewers.json")
	if err := os.WriteFile(badViewers, []byte(`[{"subjectId":"alice"}]`), 0o600); err != nil {
		t.Fatalf("写 viewers: %v", err)
	}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--addr", "127.0.0.1:0", "--home", t.TempDir(), "--viewers", badViewers},
		io.Discard, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "secret") {
		t.Fatalf("坏 viewers 退出码 = %d，stderr=%q", code, stderr.String())
	}
	code = run(context.Background(), []string{"--addr", "not an addr", "--home", t.TempDir()}, io.Discard, &stderr)
	if code != 1 {
		t.Fatalf("坏 addr 退出码 = %d", code)
	}
}

// TestRelaydHelperProcess 是子进程入口：DSH_RELAYD_HELPER=1 时把 `--` 之后的
// 参数交给 run，用来验证真实进程的启动/杀灭/端口回收。
func TestRelaydHelperProcess(t *testing.T) {
	if os.Getenv("DSH_RELAYD_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(run(context.Background(), os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}

func TestRelaydProcessLifecycle(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	home := t.TempDir()
	cmd := exec.Command(exe, "-test.run=TestRelaydHelperProcess", "--",
		"--addr", "127.0.0.1:0", "--home", home, "--print-token")
	cmd.Env = append(os.Environ(), "DSH_RELAYD_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	addrCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if idx := strings.Index(line, "listening on http://"); idx >= 0 {
				select {
				case addrCh <- strings.TrimSpace(line[idx+len("listening on http://"):]):
				default:
				}
			}
		}
	}()
	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(20 * time.Second):
		t.Fatalf("子进程没打印监听地址，stderr=%s", stderr.String())
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/v1/health")
	if err != nil {
		t.Fatalf("子进程 health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("子进程 health 状态 = %d", resp.StatusCode)
	}

	// 硬杀（模拟被外部回收）：进程必须真的没了，端口必须释放。
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("被 Kill 的进程 Wait 不该返回 nil")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("进程退出后端口 %s 仍可连接", addr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
