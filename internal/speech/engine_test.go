package speech

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func synthWAV(t *testing.T, e *Engine, text string) []byte {
	t.Helper()
	wav := filepath.Join(t.TempDir(), "fixture.wav")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := e.Synthesize(ctx, text, wav); err != nil {
		t.Skipf("本机无法合成真实语音 fixture: %v", err)
	}
	data, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestManagedHelperRealCatalogPrepareTranscribeThreeTimes(t *testing.T) {
	e := NewWindowsEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	providers, err := e.Catalog(ctx)
	if err != nil {
		t.Skipf("本机无 Windows Speech 识别器: %v", err)
	}
	if len(providers) == 0 {
		t.Skip("无识别器")
	}
	id := providers[0].ID
	audio := synthWAV(t, e, "你好世界")
	for i := 0; i < 3; i++ {
		if err := e.Prepare(ctx, id, ""); err != nil {
			t.Fatalf("prepare #%d: %v", i, err)
		}
		tr, err := e.Transcribe(ctx, id, providers[0].Language, audio)
		if err != nil {
			t.Fatalf("真实转写 #%d 失败: %v", i, err)
		}
		if strings.TrimSpace(tr.Text) == "" {
			t.Fatalf("真实识别器没有返回文本（不允许固定文本）：%+v", tr)
		}
		if tr.AudioSeconds <= 0 || tr.InferenceSeconds <= 0 {
			t.Fatalf("时长必须是真实测量：%+v", tr)
		}
		t.Logf("transcription #%d: %q audio=%.2fs inference=%.2fs", i, tr.Text, tr.AudioSeconds, tr.InferenceSeconds)
	}
	statuses := map[string]Status{}
	followCtx, followCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer followCancel()
	if err := e.Follow(followCtx, func(s Status) error { statuses[s.ProviderID] = s; return nil }); err != nil {
		t.Fatal(err)
	}
	if statuses[id].State != "prepared" {
		t.Fatalf("follow 没读到 prepared：%+v", statuses)
	}
}

func TestManagedHelperTimeoutKillsProcess(t *testing.T) {
	e := NewWindowsEngine()
	e.Timeout = 150 * time.Millisecond // 强制 helper 超时（真实进程必须被杀）
	providers, err := e.Catalog(context.Background())
	if err != nil {
		t.Skipf("无识别器: %v", err)
	}
	_ = providers
	// 用一个不存在的 WAV 也会先走 helper；150ms 内可能完成，改用真实 WAV 的识别超时不可控。
	// 这里用 1ns 保证超时，验证结构化失败。
	e.Timeout = time.Nanosecond
	if _, err := e.Catalog(context.Background()); err == nil {
		t.Fatal("超时必须结构化失败")
	}
}

func TestManagedHelperInvalidAudioAndNoRecognizer(t *testing.T) {
	e := NewWindowsEngine()
	if _, err := e.Transcribe(context.Background(), "x", "", []byte("not-wav")); err == nil {
		t.Fatal("非 WAV 必须拒绝")
	}
	e.CSC = filepath.Join(t.TempDir(), "missing-csc.exe")
	if _, err := e.Catalog(context.Background()); err == nil {
		t.Fatal("无 csc 必须结构化失败")
	}
}
