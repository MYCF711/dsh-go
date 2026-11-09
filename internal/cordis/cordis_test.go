package cordis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeBus struct{ events []string }

func (b *fakeBus) Emit(name string, _ any) { b.events = append(b.events, name) }

type sample struct {
	id, version string
	requires    []string
	provide     string
	applyErr    error
	disposeErr  error
	onApply     func()
	applied     *[]string
	disposed    *[]string
	value       any
}

func (s *sample) Meta() Meta { return Meta{ID: s.id, Version: s.version, Requires: s.requires} }
func (s *sample) Apply(c *Context) error {
	if s.applyErr != nil {
		return s.applyErr
	}
	if s.onApply != nil {
		s.onApply()
	}
	if s.provide != "" {
		if err := c.Provide(s.provide, s.value); err != nil {
			return err
		}
	}
	if s.applied != nil {
		*s.applied = append(*s.applied, s.id)
	}
	return nil
}
func (s *sample) Dispose(c *Context) error {
	if s.disposed != nil {
		*s.disposed = append(*s.disposed, s.id)
	}
	return s.disposeErr
}

func TestApplyOrderInventoryDispose(t *testing.T) {
	bus := &fakeBus{}
	r := NewRegistry(bus)
	var applied, disposed []string
	a := &sample{id: "a", version: "1", provide: "svc", value: 42, applied: &applied, disposed: &disposed}
	b := &sample{id: "b", version: "1", requires: []string{"a"}, applied: &applied, disposed: &disposed}
	// register b first: dependency order must still put a before b.
	if err := r.Register(b); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if got := len(r.Inventory()); got != 2 {
		t.Fatalf("apply 前 inventory=%d，want 2", got)
	}
	if err := r.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(applied, ",") != "a,b" {
		t.Fatalf("apply 顺序=%v，want a,b", applied)
	}
	inv := r.Inventory()
	if len(inv) != 2 || inv[0]["state"] != "applied" || inv[1]["state"] != "applied" {
		t.Fatalf("inventory=%v", inv)
	}
	if err := r.DisposeAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(disposed, ",") != "b,a" {
		t.Fatalf("dispose 顺序=%v，want b,a", disposed)
	}
	if len(r.Inventory()) != 0 {
		t.Fatalf("dispose 后 inventory 应为空：%v", r.Inventory())
	}
	if len(bus.events) == 0 || bus.events[0] != "cordis/registered" {
		t.Fatalf("kernel bus 没收到生命周期事件：%v", bus.events)
	}
}

func TestDuplicateAndUnknownDependencyFailLoud(t *testing.T) {
	r := NewRegistry(nil)
	if err := r.Register(&sample{id: "a", version: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&sample{id: "a", version: "2"}); err == nil {
		t.Fatal("重复 plugin id 必须报错")
	}
	r2 := NewRegistry(nil)
	if err := r2.Register(&sample{id: "b", version: "1", requires: []string{"nope"}}); err != nil {
		t.Fatal(err)
	}
	err := r2.ApplyAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "未知插件") {
		t.Fatalf("未知依赖必须结构化失败，得到 %v", err)
	}
}

func TestApplyErrorRollsBackPreviouslyApplied(t *testing.T) {
	r := NewRegistry(nil)
	var disposed []string
	a := &sample{id: "a", version: "1", disposed: &disposed}
	b := &sample{id: "b", version: "1", requires: []string{"a"}, applyErr: os.ErrInvalid}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(b); err != nil {
		t.Fatal(err)
	}
	err := r.ApplyAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "apply 失败") {
		t.Fatalf("apply 失败必须返回错误，得到 %v", err)
	}
	if strings.Join(disposed, ",") != "a" {
		t.Fatalf("已 applied 的 a 必须回滚 dispose，实际 %v", disposed)
	}
	if len(r.Inventory()) != 0 {
		t.Fatalf("回滚后不应留下 applied 记录：%v", r.Inventory())
	}
}

func TestCancelDuringApplyRollsBack(t *testing.T) {
	r := NewRegistry(nil)
	ctx, cancel := context.WithCancel(context.Background())
	var disposed []string
	a := &sample{id: "a", version: "1", onApply: cancel, disposed: &disposed}
	b := &sample{id: "b", version: "1", requires: []string{"a"}}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(b); err != nil {
		t.Fatal(err)
	}
	err := r.ApplyAll(ctx)
	if err == nil || !strings.Contains(err.Error(), "取消") {
		t.Fatalf("取消必须返回错误，得到 %v", err)
	}
	if strings.Join(disposed, ",") != "a" {
		t.Fatalf("取消后必须回滚 a，实际 %v", disposed)
	}
}

func TestManifestHashStableAcrossRestart(t *testing.T) {
	home := t.TempDir()
	build := func() *Registry {
		r := NewRegistry(nil)
		if err := r.Register(&sample{id: "a", version: "1", provide: "svc", value: 1}); err != nil {
			t.Fatal(err)
		}
		if err := r.Register(&sample{id: "b", version: "1", requires: []string{"a"}}); err != nil {
			t.Fatal(err)
		}
		if err := r.ApplyAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		return r
	}
	first, err := build().SyncManifest(home)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Hash != first.Hash {
		t.Fatalf("重启前后 hash 不一致：%s != %s", loaded.Hash, first.Hash)
	}
	second, err := build().Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if second.Hash != first.Hash {
		t.Fatalf("同插件集重算 hash 不一致：%s != %s", second.Hash, first.Hash)
	}
	raw, err := os.ReadFile(filepath.Join(home, "state", "cordis", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["hash"] != first.Hash {
		t.Fatalf("落盘 hash 字段不对：%v", doc["hash"])
	}
	if err := os.WriteFile(filepath.Join(home, "state", "cordis", "manifest.json"), []byte(`{"version":1,"hash":"bad","plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(home); err == nil || !strings.Contains(err.Error(), "hash 不匹配") {
		t.Fatalf("被改写的 manifest 必须 hash 校验失败，得到 %v", err)
	}
}
