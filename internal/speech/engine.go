// Package speech 是本机语音能力适配层（Windows Speech / System.Speech）。
//
// 不使用 PowerShell/COM 直接跑在宿主进程：那会在 Go 的 exec 管道下挂起
// （t108 实测 60s 不返回）。这里改为**受管 C# helper**：源码编译成独立 exe，
// 由 Go 以单次超时 + 取消 + 进程 kill + 退出等待管理；helper 自己只做一次
// catalog / 合成 / 识别，跑完即退，不常驻、无子进程。
//
// 宿主二进制仍不嵌 ASR 模型；没有识别器时结构化返回缺什么，绝不返回固定文本。
package speech

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoEngine     = errors.New("speech: 没有可用的 Windows Speech 识别器")
	ErrInvalidAudio = errors.New("speech: 音频无效")
	ErrNotPrepared  = errors.New("speech: 识别器尚未准备")
	ErrProviderGone = errors.New("speech: provider 不存在")
	ErrHelperFailed = errors.New("speech: ASR helper 失败")
)

type Provider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language"`
}

type Transcription struct {
	Text             string  `json:"text"`
	AudioSeconds     float64 `json:"audioSeconds"`
	InferenceSeconds float64 `json:"inferenceSeconds"`
}

type Status struct {
	Type       string `json:"type"`
	ProviderID string `json:"providerId"`
	State      string `json:"state"`
	Prepared   bool   `json:"prepared"`
	Error      string `json:"error,omitempty"`
}

type Settings struct {
	ProviderID string `json:"providerId,omitempty"`
	Language   string `json:"language,omitempty"`
}

type Engine struct {
	// CSC 是 C# 编译器；空 = 自动探测 .NET Framework 4 csc.exe。
	CSC string
	// Timeout 是单次 helper 的超时；到时 kill 并结构化失败。
	Timeout time.Duration

	mu       sync.Mutex
	status   map[string]*Status
	settings Settings
	helper   string
}

func NewWindowsEngine() *Engine {
	return &Engine{Timeout: 30 * time.Second, status: map[string]*Status{}}
}

// helperSource 是独立受管 ASR helper 的完整源码。
const helperSource = `using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Speech.Recognition;
using System.Speech.Synthesis;
using System.Text;

static class Program {
    static string Esc(string s) {
        var b = new StringBuilder();
        foreach (char c in s) {
            switch (c) {
                case '\\': b.Append("\\\\"); break;
                case '"': b.Append("\\\""); break;
                case '\n': b.Append("\\n"); break;
                case '\r': b.Append("\\r"); break;
                case '\t': b.Append("\\t"); break;
                default:
                    if (c < ' ') b.Append("\\u" + ((int)c).ToString("x4")); else b.Append(c);
                    break;
            }
        }
        return b.ToString();
    }

    static int Catalog() {
        var sb = new StringBuilder("[");
        bool first = true;
        foreach (RecognizerInfo info in SpeechRecognitionEngine.InstalledRecognizers()) {
            if (!first) sb.Append(',');
            first = false;
            sb.Append("{\"id\":\"").Append(Esc(info.Id)).Append("\",\"name\":\"")
              .Append(Esc(info.Name)).Append("\",\"language\":\"")
              .Append(Esc(info.Culture.Name)).Append("\"}");
        }
        sb.Append(']');
        Console.Out.Write(sb.ToString());
        return 0;
    }

    static int Synth(string text, string wav) {
        using (var s = new SpeechSynthesizer()) {
            s.SetOutputToWaveFile(wav);
            s.Speak(text);
        }
        return 0;
    }

    static int Transcribe(string id, string wav) {
        RecognizerInfo info = null;
        foreach (RecognizerInfo candidate in SpeechRecognitionEngine.InstalledRecognizers()) {
            if (candidate.Id == id) { info = candidate; break; }
        }
        if (info == null) { Console.Error.Write("recognizer not found: " + id); return 3; }
        if (!File.Exists(wav)) { Console.Error.Write("wav not found"); return 4; }
        using (var rec = new SpeechRecognitionEngine(info)) {
            rec.SetInputToWaveFile(wav);
            rec.LoadGrammar(new DictationGrammar());
            RecognitionResult result = rec.Recognize(TimeSpan.FromSeconds(10));
            if (result == null) { Console.Out.Write("{\"text\":\"\"}"); return 0; }
            Console.Out.Write("{\"text\":\"" + Esc(result.Text) + "\"}");
            return 0;
        }
    }

    static int Main(string[] args) {
        Console.OutputEncoding = new UTF8Encoding(false);
        try {
            if (args.Length == 1 && args[0] == "--catalog") return Catalog();
            if (args.Length == 3 && args[0] == "--synth") return Synth(args[1], args[2]);
            if (args.Length == 3 && args[0] == "--wav") return Transcribe(args[1], args[2]);
            Console.Error.Write("usage: --catalog | --synth text wav | --wav recognizer wav");
            return 2;
        } catch (Exception ex) {
            Console.Error.Write(ex.ToString());
            return 1;
        }
    }
}
`

func findCSC(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("%w: csc 不存在: %v", ErrHelperFailed, err)
		}
		return explicit, nil
	}
	candidates := []string{
		filepath.Join(os.Getenv("WINDIR"), "Microsoft.NET", "Framework64", "v4.0.30319", "csc.exe"),
		filepath.Join(os.Getenv("WINDIR"), "Microsoft.NET", "Framework", "v4.0.30319", "csc.exe"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w: 找不到 .NET Framework 4 csc.exe", ErrHelperFailed)
}

// ensureHelper 编译一次 helper 到稳定缓存路径（内容 hash 变化则重建）。
func (e *Engine) ensureHelper() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.helper != "" {
		if _, err := os.Stat(e.helper); err == nil {
			return e.helper, nil
		}
	}
	csc, err := findCSC(e.CSC)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(helperSource))
	dir := filepath.Join(os.TempDir(), "dsh-speech-helper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(dir, "speech-"+hex.EncodeToString(sum[:8]))
	src := base + ".cs"
	exe := base + ".exe"
	if _, err := os.Stat(exe); err != nil {
		if err := os.WriteFile(src, []byte(helperSource), 0o600); err != nil {
			return "", err
		}
		speechRef := filepath.Join(os.Getenv("WINDIR"), "Microsoft.NET", "Framework64", "v4.0.30319", "WPF", "System.Speech.dll")
		cmd := exec.Command(csc, "/nologo", "/target:exe", "/r:"+speechRef, "/out:"+exe, src)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("%w: 编译 helper: %v: %s", ErrHelperFailed, err, strings.TrimSpace(string(out)))
		}
	}
	e.helper = exe
	return exe, nil
}

// run 在单次超时内跑 helper；超时/取消时 kill 并等待退出（无残留）。
func (e *Engine) run(ctx context.Context, args ...string) (string, string, error) {
	exe, err := e.ensureHelper()
	if err != nil {
		return "", "", err
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, exe, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	if runCtx.Err() != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return out.String(), errb.String(), fmt.Errorf("%w: helper 超时/取消（%v）", ErrHelperFailed, runCtx.Err())
	}
	if err != nil {
		return out.String(), errb.String(), fmt.Errorf("%w: %v: %s", ErrHelperFailed, err, strings.TrimSpace(errb.String()))
	}
	return out.String(), errb.String(), nil
}

func (e *Engine) Catalog(ctx context.Context) ([]Provider, error) {
	out, _, err := e.run(ctx, "--catalog")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoEngine, err)
	}
	var providers []Provider
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &providers); err != nil {
		return nil, fmt.Errorf("%w: helper 编目 JSON 非法: %v", ErrNoEngine, err)
	}
	if len(providers) == 0 {
		return nil, ErrNoEngine
	}
	return providers, nil
}

// Synthesize 用真实 System.Speech 合成 WAV（fixture/探针用）。
func (e *Engine) Synthesize(ctx context.Context, text, wav string) error {
	_, _, err := e.run(ctx, "--synth", text, wav)
	return err
}

func (e *Engine) SetStatus(id, state string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := &Status{Type: "status", ProviderID: id, State: state, Prepared: state == "prepared"}
	if err != nil {
		s.Error = err.Error()
	}
	e.status[id] = s
}

func (e *Engine) Prepare(ctx context.Context, providerID, downloadSource string) error {
	e.SetStatus(providerID, "preparing", nil)
	providers, err := e.Catalog(ctx)
	if err != nil {
		e.SetStatus(providerID, "error", err)
		return err
	}
	for _, p := range providers {
		if p.ID == providerID {
			e.SetStatus(providerID, "prepared", nil)
			return nil
		}
	}
	err = fmt.Errorf("%w: %q（downloadSource=%q 未下载任何模型：本仓只使用 Windows 已安装识别器）", ErrProviderGone, providerID, downloadSource)
	e.SetStatus(providerID, "error", err)
	return err
}

func (e *Engine) CancelPreparation(_ context.Context, providerID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.status[providerID]
	if s == nil || (s.State != "preparing" && s.State != "prepared") {
		return fmt.Errorf("%w: %q 没有可取消的准备任务", ErrProviderGone, providerID)
	}
	s.State = "canceled"
	s.Prepared = false
	return nil
}

func (e *Engine) Configure(_ context.Context, patch Settings) (Settings, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if patch.ProviderID != "" {
		e.settings.ProviderID = patch.ProviderID
	}
	if patch.Language != "" {
		e.settings.Language = patch.Language
	}
	return e.settings, nil
}

func (e *Engine) Transcribe(ctx context.Context, providerID, language string, audio []byte) (Transcription, error) {
	if len(audio) < 44 || string(audio[0:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return Transcription{}, fmt.Errorf("%w: 不是 RIFF/WAVE", ErrInvalidAudio)
	}
	if providerID == "" {
		e.mu.Lock()
		providerID = e.settings.ProviderID
		e.mu.Unlock()
	}
	if providerID == "" {
		return Transcription{}, fmt.Errorf("%w: 缺 providerId（识别器未指定）", ErrNotPrepared)
	}
	providers, err := e.Catalog(ctx)
	if err != nil {
		return Transcription{}, err
	}
	found := false
	for _, p := range providers {
		if p.ID == providerID {
			found = true
			break
		}
	}
	if !found {
		return Transcription{}, fmt.Errorf("%w: %q", ErrProviderGone, providerID)
	}
	dir, err := os.MkdirTemp("", "dsh-speech-audio-*")
	if err != nil {
		return Transcription{}, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(wav, audio, 0o600); err != nil {
		return Transcription{}, err
	}
	e.SetStatus(providerID, "transcribing", nil)
	start := time.Now()
	out, _, err := e.run(ctx, "--wav", providerID, wav)
	elapsed := time.Since(start).Seconds()
	if err != nil {
		e.SetStatus(providerID, "error", err)
		return Transcription{}, fmt.Errorf("speech: 识别失败: %w", err)
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		e.SetStatus(providerID, "error", err)
		return Transcription{}, fmt.Errorf("speech: 识别结果 JSON 非法: %v", err)
	}
	e.SetStatus(providerID, "prepared", nil)
	return Transcription{Text: result.Text, AudioSeconds: wavSeconds(audio), InferenceSeconds: elapsed}, nil
}

func wavSeconds(audio []byte) float64 {
	if len(audio) < 44 {
		return 0
	}
	byteRate := binary.LittleEndian.Uint32(audio[28:32])
	if byteRate == 0 {
		return 0
	}
	for off := 12; off+8 <= len(audio); {
		id := string(audio[off : off+4])
		size := int(binary.LittleEndian.Uint32(audio[off+4 : off+8]))
		if id == "data" {
			return float64(size) / float64(byteRate)
		}
		off += 8 + size + (size & 1)
	}
	return 0
}

// Follow 有真实状态变化就发 status 帧；ctx 取消即结束（无假流）。
func (e *Engine) Follow(ctx context.Context, sink func(Status) error) error {
	e.mu.Lock()
	snapshot := make([]Status, 0, len(e.status))
	for _, s := range e.status {
		snapshot = append(snapshot, *s)
	}
	configured := e.settings.ProviderID
	e.mu.Unlock()
	// 空状态也必须给订阅者一个**基线帧**：否则 HTTP/mux 客户端在收到首个状态
	// 变化前连响应头都拿不到（open 一直挂到超时）。基线是真实"未准备"状态，
	// 不是假进度。
	if len(snapshot) == 0 {
		snapshot = append(snapshot, Status{Type: "status", ProviderID: configured, State: "idle", Prepared: false})
	}
	last := map[string]string{}
	for _, s := range snapshot {
		last[s.ProviderID] = s.State
		if err := sink(s); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			e.mu.Lock()
			changed := make([]Status, 0)
			for id, s := range e.status {
				if last[id] != s.State {
					last[id] = s.State
					changed = append(changed, *s)
				}
			}
			e.mu.Unlock()
			for _, s := range changed {
				if err := sink(s); err != nil {
					return err
				}
			}
		}
	}
}
