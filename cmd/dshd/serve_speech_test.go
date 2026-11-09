package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/speech"
	"dshgo/internal/transport"
)

func speechTestServer(t *testing.T, api *speechAPI) (*httptest.Server, func()) {
	t.Helper()
	srv := &transport.Server{}
	srv.AttachOfficialSpeechAPI(api)
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeOfficialSpeechAPI))
	return ts, ts.Close
}

func postSpeech(t *testing.T, ts *httptest.Server, method, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/"+method, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestSpeechSixMethodsRealWAVHTTP(t *testing.T) {
	api := newSpeechAPI()
	ts, closeSrv := speechTestServer(t, api)
	defer closeSrv()

	code, body := postSpeech(t, ts, "speech.catalog", `{}`)
	if code != 200 || !bytes.Contains(body, []byte("MS-2052")) {
		t.Skipf("本机无 Windows Speech 识别器：%d %s", code, body)
	}
	var env struct {
		Result []transport.OfficialSpeechProvider `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Result) == 0 {
		t.Fatalf("catalog=%s err=%v", body, err)
	}
	id := env.Result[0].ID

	if code, body = postSpeech(t, ts, "speech.configure", `{"patch":{"providerId":"`+id+`","language":"zh-CN"}}`); code != 200 {
		t.Fatalf("configure=%d %s", code, body)
	}
	if code, body = postSpeech(t, ts, "speech.prepare", `{"providerId":"`+id+`","options":{"downloadSource":"local"}}`); code != 200 {
		t.Fatalf("prepare=%d %s", code, body)
	}
	if code, body = postSpeech(t, ts, "speech.cancelPreparation", `{"providerId":"`+id+`"}`); code != 200 {
		t.Fatalf("cancel=%d %s", code, body)
	}
	if code, body = postSpeech(t, ts, "speech.prepare", `{"providerId":"`+id+`"}`); code != 200 {
		t.Fatalf("re-prepare=%d %s", code, body)
	}

	wav := filepath.Join(t.TempDir(), "hello.wav")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := api.engine.Synthesize(ctx, "你好世界", wav); err != nil {
		t.Skipf("无法合成真实 WAV: %v", err)
	}
	audio, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	reqBody, _ := json.Marshal(map[string]any{"request": map[string]any{
		"audioBase64": base64.StdEncoding.EncodeToString(audio), "providerId": id, "language": "zh-CN",
	}})
	code, body = postSpeech(t, ts, "speech.transcribe", string(reqBody))
	if code != 200 {
		t.Fatalf("transcribe=%d %s", code, body)
	}
	var trEnv struct {
		Result transport.OfficialSpeechTranscription `json:"result"`
	}
	if err := json.Unmarshal(body, &trEnv); err != nil {
		t.Fatalf("transcribe JSON=%s err=%v", body, err)
	}
	if strings.TrimSpace(trEnv.Result.Text) != "你好世界" {
		t.Fatalf("真实转写=%q（不允许固定文本）", trEnv.Result.Text)
	}

	followCtx, followCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer followCancel()
	req, _ := http.NewRequestWithContext(followCtx, "POST", ts.URL+"/api/speech.follow", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	frames, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(frames, []byte(`"type":"status"`)) {
		t.Fatalf("follow=%d %s", resp.StatusCode, frames)
	}
}

func TestSpeechNoEngineIsStructured(t *testing.T) {
	api := newSpeechAPI()
	api.engine.CSC = filepath.Join(t.TempDir(), "missing-csc.exe")
	ts, closeSrv := speechTestServer(t, api)
	defer closeSrv()
	code, body := postSpeech(t, ts, "speech.catalog", `{}`)
	if code == 501 || code == 200 {
		t.Fatalf("无引擎必须结构化失败而不是 501/200：%d %s", code, body)
	}
}

var _ = speech.Status{}
