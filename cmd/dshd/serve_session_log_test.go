package main

import (
	"testing"

	"dshgo/internal/settings"
	"dshgo/internal/settingsform"
)

func TestSessionLogConfigLoaderReadsLiveNamespace(t *testing.T) {
	home := t.TempDir()
	loader := sessionLogConfigLoader(home)
	config, err := loader()
	if err != nil {
		t.Fatal(err)
	}
	if !config.Enabled || config.MaxBytes != 8*1024*1024 {
		t.Fatalf("全新 home 必须取官方默认: %+v", config)
	}
	store := settings.Open(home)
	revision, err := store.Write(settingsform.NamespaceSessionLogDeepSeek, map[string]any{
		"enabled": false, "maxBytes": 1024,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	config, err = loader()
	if err != nil {
		t.Fatal(err)
	}
	if config.Enabled || config.MaxBytes != 1024 {
		t.Fatalf("持久覆盖没有在下一次 prepare 生效: %+v", config)
	}
	if _, err := store.Write(settingsform.NamespaceSessionLogDeepSeek, map[string]any{
		"enabled": true, "maxBytes": 2048,
	}, &revision); err != nil {
		t.Fatal(err)
	}
	config, err = loader()
	if err != nil || !config.Enabled || config.MaxBytes != 2048 {
		t.Fatalf("live 重读失败: config=%+v err=%v", config, err)
	}
	if _, err := store.Write(settingsform.NamespaceSessionLogDeepSeek, map[string]any{"maxBytes": 0}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := loader(); err == nil {
		t.Fatal("maxBytes=0 必须让本贡献 fail-loud，不能静默换默认值")
	}
}
