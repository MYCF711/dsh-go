package transport

// speech.* 6 个官方方法入口。识别/准备/取消/流式状态由宿主注入的真实
// 引擎提供（本仓 Windows Speech / System.Speech）；transport 不做静态文本。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

const (
	MethodSpeechCatalog Method = "speech.catalog"
	MethodSpeechFollow  Method = "speech.follow"
)

// SpeechAPIError 让宿主把无引擎/非法音频映射成结构化 HTTP，而不是 500/假文本。
type SpeechAPIError struct {
	Status  int
	Code    string
	Message string
}

func (e *SpeechAPIError) Error() string { return e.Message }

type OfficialSpeechProvider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language"`
}

type OfficialSpeechTranscription struct {
	Text             string  `json:"text"`
	AudioSeconds     float64 `json:"audioSeconds"`
	InferenceSeconds float64 `json:"inferenceSeconds"`
}

type OfficialSpeechConfig struct {
	ProviderID string `json:"providerId,omitempty"`
	Language   string `json:"language,omitempty"`
}

type OfficialSpeechStatus struct {
	Type       string `json:"type"`
	ProviderID string `json:"providerId"`
	State      string `json:"state"`
	Prepared   bool   `json:"prepared"`
	Error      string `json:"error,omitempty"`
}

type OfficialSpeechAPI interface {
	CatalogOfficialSpeech(ctx context.Context) ([]OfficialSpeechProvider, error)
	PrepareOfficialSpeech(ctx context.Context, providerID, downloadSource string) error
	ConfigureOfficialSpeech(ctx context.Context, patch OfficialSpeechConfig) (OfficialSpeechConfig, error)
	CancelOfficialSpeechPreparation(ctx context.Context, providerID string) error
	TranscribeOfficialSpeech(ctx context.Context, providerID, language, audioBase64 string) (OfficialSpeechTranscription, error)
	FollowOfficialSpeech(ctx context.Context, sink func(OfficialSpeechStatus) error) error
}

var officialSpeechMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialSpeechAPI
}{m: map[*Server]OfficialSpeechAPI{}}

func (s *Server) AttachOfficialSpeechAPI(api OfficialSpeechAPI) {
	officialSpeechMount.mu.Lock()
	defer officialSpeechMount.mu.Unlock()
	officialSpeechMount.m[s] = api
}
func (s *Server) DetachOfficialSpeechAPI() {
	officialSpeechMount.mu.Lock()
	defer officialSpeechMount.mu.Unlock()
	delete(officialSpeechMount.m, s)
}
func (s *Server) officialSpeechAPI() (OfficialSpeechAPI, bool) {
	officialSpeechMount.mu.RLock()
	defer officialSpeechMount.mu.RUnlock()
	api, ok := officialSpeechMount.m[s]
	return api, ok
}

func IsOfficialSpeechMethod(name string) bool {
	switch Method(name) {
	case MethodSpeechCatalog, MethodSpeechConfigure, MethodSpeechPrepare, MethodSpeechTranscribe,
		MethodSpeechCancelPreparation, MethodSpeechFollow:
		return true
	}
	return false
}

func (s *Server) ServeOfficialSpeechAPI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if !IsOfficialSpeechMethod(name) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", "不是 speech 方法"))
		return
	}
	api, ok := s.officialSpeechAPI()
	if !ok {
		writeOfficialError(w, officialNotImplemented("%s 未接线：宿主没有 AttachOfficialSpeechAPI", name))
		return
	}
	var raw json.RawMessage
	if r.Body != nil {
		var err error
		raw, err = io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			writeOfficialError(w, officialBadRequest("读参数失败: %v", err))
			return
		}
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil {
		writeOfficialError(w, officialBadRequest("参数不是 JSON 对象"))
		return
	}
	str := func(k string) string { var v string; _ = json.Unmarshal(p[k], &v); return v }
	ctx := r.Context()
	obj := func(key string) map[string]json.RawMessage {
		var out map[string]json.RawMessage
		if rawObj, ok := p[key]; ok {
			_ = json.Unmarshal(rawObj, &out)
		}
		return out
	}
	objStr := func(m map[string]json.RawMessage, k string) string {
		var v string
		if m != nil {
			_ = json.Unmarshal(m[k], &v)
		}
		return v
	}
	switch Method(name) {
	case MethodSpeechCatalog:
		providers, err := api.CatalogOfficialSpeech(ctx)
		if err != nil {
			writeSpeechError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, providers)
	case MethodSpeechPrepare:
		id := str("providerId")
		if id == "" {
			writeOfficialError(w, officialBadRequest("providerId 必填"))
			return
		}
		opts := obj("options")
		if err := api.PrepareOfficialSpeech(ctx, id, objStr(opts, "downloadSource")); err != nil {
			writeSpeechError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case MethodSpeechConfigure:
		patchObj := obj("patch")
		if patchObj == nil {
			patchObj = p
		}
		patch := OfficialSpeechConfig{ProviderID: objStr(patchObj, "providerId"), Language: objStr(patchObj, "language")}
		cfg, err := api.ConfigureOfficialSpeech(ctx, patch)
		if err != nil {
			writeSpeechError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	case MethodSpeechCancelPreparation:
		id := str("providerId")
		if id == "" {
			writeOfficialError(w, officialBadRequest("providerId 必填"))
			return
		}
		if err := api.CancelOfficialSpeechPreparation(ctx, id); err != nil {
			writeSpeechError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case MethodSpeechTranscribe:
		req := obj("request")
		if req == nil {
			req = p
		}
		audio := objStr(req, "audioBase64")
		if audio == "" {
			writeOfficialError(w, officialBadRequest("request.audioBase64 必填"))
			return
		}
		tr, err := api.TranscribeOfficialSpeech(ctx, objStr(req, "providerId"), objStr(req, "language"), audio)
		if err != nil {
			writeSpeechError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, tr)
	case MethodSpeechFollow:
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_ = api.FollowOfficialSpeech(ctx, func(st OfficialSpeechStatus) error {
			line, err := json.Marshal(st)
			if err != nil {
				return err
			}
			if _, err := w.Write(append(line, 10)); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		})
	}
}

func writeSpeechError(w http.ResponseWriter, err error) {
	var se *SpeechAPIError
	if errors.As(err, &se) {
		writeJSON(w, se.Status, errorBodyFor(se.Code, se.Message))
		return
	}
	var fe *ForgeGitError
	if errors.As(err, &fe) {
		writeJSON(w, fe.Status, errorBodyFor(fe.Code, fe.Message))
		return
	}
	writeOfficialError(w, officialInternal("%v", err))
}

// startOfficialSpeechFollowStream 把 speech.follow 接到 mux 帧机：与 HTTP NDJSON
// 共用同一个 FollowOfficialSpeech 真实状态源，帧就是 OfficialSpeechStatus JSON。
func (s *Server) startOfficialSpeechFollowStream() (OfficialFrameStream, *OfficialStreamFault) {
	api, ok := s.officialSpeechAPI()
	if !ok {
		return nil, officialStreamNotImplemented(MethodSpeechFollow, "speech 未接线：宿主没有 AttachOfficialSpeechAPI")
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &officialSpeechFollowStream{ctx: ctx, cancel: cancel, ch: make(chan json.RawMessage, 8), done: make(chan struct{})}
	go func() {
		defer close(st.done)
		_ = api.FollowOfficialSpeech(ctx, func(v OfficialSpeechStatus) error {
			b, err := json.Marshal(v)
			if err != nil {
				return err
			}
			select {
			case st.ch <- b:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return st, nil
}

type officialSpeechFollowStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	ch     chan json.RawMessage
	done   chan struct{}
}

func (s *officialSpeechFollowStream) Next(ctx context.Context) (json.RawMessage, bool, *OfficialStreamFault) {
	select {
	case frame := <-s.ch:
		return frame, false, nil
	case <-s.done:
		select {
		case frame := <-s.ch:
			return frame, false, nil
		default:
			return nil, true, nil
		}
	case <-ctx.Done():
		return nil, true, nil
	}
}

func (s *officialSpeechFollowStream) Close() { s.cancel() }
