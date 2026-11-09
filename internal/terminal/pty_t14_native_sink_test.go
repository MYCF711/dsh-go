//go:build windows

package terminal_test

// T14 cold-start evidence: a native Go child process reads the ConPTY stdin
// byte stream. MSYS head.exe is a slow console reader; the terminal write path
// returns 64KB in ~0.15s, so a native reader isolates Write steady state from
// the reader implementation. The bytes still come from the real ConPTY.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("T14_NATIVE_SINK") == "1" {
		dst := os.Getenv("T14_SINK_FILE")
		ready := os.Getenv("T14_SINK_READY")
		if ready != "" {
			_ = os.WriteFile(ready, []byte("ready"), 0o644)
		}
		n := 65536
		if raw := os.Getenv("T14_SINK_N"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				n = parsed
			}
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(os.Stdin, buf); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(dst, buf, 0o644); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func shellQuote(s string) string { return "'" + s + "'" }

func TestT14ColdNativeSink(t *testing.T) { runNativeSink(t, 65536) }

func runNativeSink(t *testing.T, n int) {
	cwd := t.TempDir()
	sess := openLongInputSession(t, cwd)
	waitLongPrompt(t, sess)

	if err := sess.Input("stty -icanon -echo\n"); err != nil {
		t.Fatalf("stty failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe = strings.ReplaceAll(exe, "\\", "/")
	sinkFile := strings.ReplaceAll(filepath.Join(cwd, "payload.bin"), "\\", "/")
	readyFile := strings.ReplaceAll(filepath.Join(cwd, "sink.ready"), "\\", "/")
	cmdline := fmt.Sprintf("T14_NATIVE_SINK=1 T14_SINK_FILE=%s T14_SINK_READY=%s %s -test.run=TestT14NativeSinkHelper\n",
		shellQuote(sinkFile), shellQuote(readyFile), shellQuote(exe))
	if err := sess.Input(cmdline); err != nil {
		t.Fatalf("start native sink: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, statErr := os.Stat(readyFile); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			scr, _, _ := sess.Output(0)
			t.Fatalf("native sink not ready; screen=%q", string(scr))
		}
		time.Sleep(10 * time.Millisecond)
	}

	payload := make([]byte, n)
	for i := range payload {
		payload[i] = byte('A' + i%26)
	}
	var cursor int64
	start := time.Now()
	if err := sess.Input(string(payload)); err != nil {
		t.Fatalf("native sink write: %v", err)
	}
	marker := "T14-NATIVE-DONE"
	if err := sess.Input("echo " + marker + "\n"); err != nil {
		t.Fatalf("post-short command: %v", err)
	}
	readUntil(t, sess, &cursor, marker, 30*time.Second)
	duration := time.Since(start)
	got, err := os.ReadFile(sinkFile)
	if err != nil {
		t.Fatalf("read native sink: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("native sink bytes mismatch: got=%d want=%d", len(got), len(payload))
	}
	t.Logf("cold_native_sink n=%d len=%d bytes_equal=true duration=%v after_short_command_ok=true", n, len(got), duration)
}
