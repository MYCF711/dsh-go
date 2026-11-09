package main

// PARITY-W4-SPEECH：六方法宿主实现，真实 Windows Speech 引擎。

import (
	"context"
	"encoding/base64"

	"dshgo/internal/speech"
	"dshgo/internal/transport"
)

type speechAPI struct{ engine *speech.Engine }

func newSpeechAPI() *speechAPI { return &speechAPI{engine: speech.NewWindowsEngine()} }

func (a *speechAPI) CatalogOfficialSpeech(ctx context.Context) ([]transport.OfficialSpeechProvider, error) {
	providers, err := a.engine.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]transport.OfficialSpeechProvider, 0, len(providers))
	for _, p := range providers {
		out = append(out, transport.OfficialSpeechProvider{ID: p.ID, Name: p.Name, Language: p.Language})
	}
	return out, nil
}

func (a *speechAPI) PrepareOfficialSpeech(ctx context.Context, providerID, downloadSource string) error {
	return a.engine.Prepare(ctx, providerID, downloadSource)
}

func (a *speechAPI) ConfigureOfficialSpeech(ctx context.Context, patch transport.OfficialSpeechConfig) (transport.OfficialSpeechConfig, error) {
	cfg, err := a.engine.Configure(ctx, speech.Settings{ProviderID: patch.ProviderID, Language: patch.Language})
	return transport.OfficialSpeechConfig{ProviderID: cfg.ProviderID, Language: cfg.Language}, err
}

func (a *speechAPI) CancelOfficialSpeechPreparation(ctx context.Context, providerID string) error {
	return a.engine.CancelPreparation(ctx, providerID)
}

func (a *speechAPI) TranscribeOfficialSpeech(ctx context.Context, providerID, language, audioBase64 string) (transport.OfficialSpeechTranscription, error) {
	audio, err := base64.StdEncoding.DecodeString(audioBase64)
	if err != nil {
		return transport.OfficialSpeechTranscription{}, &transport.ForgeGitError{Status: 400, Code: "invalid_audio", Message: "audioBase64 不是合法 base64"}
	}
	tr, err := a.engine.Transcribe(ctx, providerID, language, audio)
	if err != nil {
		return transport.OfficialSpeechTranscription{}, err
	}
	return transport.OfficialSpeechTranscription{Text: tr.Text, AudioSeconds: tr.AudioSeconds, InferenceSeconds: tr.InferenceSeconds}, nil
}

func (a *speechAPI) FollowOfficialSpeech(ctx context.Context, sink func(transport.OfficialSpeechStatus) error) error {
	return a.engine.Follow(ctx, func(st speech.Status) error {
		return sink(transport.OfficialSpeechStatus{Type: st.Type, ProviderID: st.ProviderID, State: st.State, Prepared: st.Prepared, Error: st.Error})
	})
}

var _ transport.OfficialSpeechAPI = (*speechAPI)(nil)
