package main

import (
	"context"
	"path/filepath"
	"testing"

	"dshgo/internal/modes"
	"dshgo/internal/sandbox"
)

func TestDangerPresetForwardsSandboxEnv(t *testing.T) {
	home := t.TempDir()
	reg := newSandboxTestRegistry(t)
	store := mustSandboxStore(t, home)
	sid := createSandboxSession(t, home, "danger-full-access")
	approvals := newPermissionOverrideStore(home)
	if _, err := sandboxSetPermission(home, sid, "danger-full-access", reg, store, approvals); err != nil {
		t.Fatalf("set danger: %v", err)
	}
	envCapture := filepath.Join(t.TempDir(), "env.json")
	t.Setenv("DSHD_MODEL_WIRE_ARGV", filepath.Join(t.TempDir(), "argv.json"))
	t.Setenv("DSHD_MODEL_WIRE_ENV", envCapture)
	runner := newSandboxForwardRunner(t, home, reg, store)
	if err := runner.Run(context.Background(), sid, "turn-d", "prompt-d"); err != nil {
		t.Fatal(err)
	}
	assertChildSandboxMode(t, readChildEnvFile(t, envCapture), "danger-full-access")
}

func TestPresetSelectClearsStaleNarrowOverride(t *testing.T) {
	home := t.TempDir()
	sid := createSandboxSession(t, home, modes.DefaultID)
	reg := newSandboxTestRegistry(t)
	store := mustSandboxStore(t, home)
	if err := store.Set(sid, sandbox.ModeReadOnly); err != nil {
		t.Fatal(err)
	}
	api := &agentOfficialAPI{deps: agentOfficialDeps{home: home, src: newSessionSource(home, ""), sandboxPresets: reg, sandboxStore: store}}
	if _, err := api.agentPresetSelect(sid, "danger-full-access"); err != nil {
		t.Fatalf("select danger: %v", err)
	}
	src, err := resolveSandboxModeForSession(home, sid, reg, store)
	if err != nil {
		t.Fatal(err)
	}
	if src.PresetMode != sandbox.ModeDangerFullAccess || src.Effective != sandbox.ModeDangerFullAccess || src.Override != "" {
		t.Fatalf("切换 preset 后旧收窄覆盖未清：%+v", src)
	}
}
