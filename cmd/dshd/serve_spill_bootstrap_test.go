package main

// T15B：serve 必须自举自己拥有的 <home>/artifacts SpillDir。
//
// 全新空 home 第一条 prompt 会因 sandbox extra read root 不存在而 fail-closed；
// 这里钉住启动自举本身，真实首条 prompt 由 tmp/GO2-TEAM/T15B 探针覆盖。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSpillDirCreatesFreshHomeArtifacts(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fresh-home") // 故意不存在
	if err := ensureSpillDir(home); err != nil {
		t.Fatalf("全新 home 自举 SpillDir: %v", err)
	}
	dir := spillDir(home)
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("artifacts 没建出来: %v", err)
	}
	if !st.IsDir() {
		t.Fatalf("artifacts 不是目录")
	}
}

func TestEnsureSpillDirFailsOnFile(t *testing.T) {
	home := t.TempDir()
	dir := spillDir(home)
	if err := os.WriteFile(dir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureSpillDir(home); err == nil {
		t.Fatal("artifacts 是文件时必须 fail-loud，不能静默跳过")
	}
}

// TestEnsureSpillDirDoesNotCreateOtherHomeDirs：启动自举只建宿主自己的
// SpillDir，不顺手替用户预建 sessions/state 等目录。
func TestEnsureSpillDirDoesNotCreateOtherHomeDirs(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fresh-only-artifacts")
	if err := ensureSpillDir(home); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "artifacts" {
		t.Fatalf("home 下只应有 artifacts，实际 %v", entries)
	}
}
