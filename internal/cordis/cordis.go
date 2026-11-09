// Package cordis is the Go-native Cordis core: a plugin registry with
// apply/dispose lifecycles, dependency-ordered activation, service injection
// (DI), and a deterministic inspect manifest.
//
// It intentionally has no JavaScript runtime and no knowledge of the transport
// API. JS capability belongs to the external managed-node track (C2); this
// package is the in-process Go track (C1). Events are emitted through the small
// Emitter interface, which *kernel.Bus already satisfies (Emit(name, payload)),
// so the host bus can be passed in without cordis importing kernel (R1).
package cordis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Meta is stable plugin identity and dependency declaration.
type Meta struct {
	ID       string   `json:"id"`
	Version  string   `json:"version"`
	Requires []string `json:"requires,omitempty"` // service names provided by earlier plugins
}

// Plugin is one Go-native Cordis plugin.
type Plugin interface {
	Meta() Meta
	Apply(*Context) error
	Dispose(*Context) error
}

// Emitter is the event boundary. *kernel.Bus has Emit(string, any), so the host
// bus satisfies this interface without an adapter.
type Emitter interface {
	Emit(name string, payload any)
}

type nopEmitter struct{}

func (nopEmitter) Emit(string, any) {}

// Context is the per-plugin injection context. Services provided by a plugin
// are withdrawn when that plugin is disposed (reverse order), so a later plugin
// can never keep a reference to a dead service.
type Context struct {
	PluginID string
	reg      *Registry
	names    []string
	events   Emitter
}

// Provide registers a service in this plugin's scope. Duplicate names, including
// names already provided by an applied plugin, are hard errors.
func (c *Context) Provide(name string, value any) error {
	if name == "" {
		return errors.New("cordis: service name 不能为空")
	}
	if _, ok := c.reg.services[name]; ok {
		return fmt.Errorf("cordis: service %q 已被提供", name)
	}
	c.reg.services[name] = value
	c.names = append(c.names, name)
	c.events.Emit("cordis/service-provided", map[string]any{"pluginId": c.PluginID, "service": name})
	return nil
}

// Resolve injects a provided service.
func (c *Context) Resolve(name string) (any, bool) {
	v, ok := c.reg.services[name]
	return v, ok
}

// MustResolve is Resolve for plugins that already declared the dependency.
func (c *Context) MustResolve(name string) any {
	v, ok := c.reg.services[name]
	if !ok {
		panic(fmt.Sprintf("cordis: plugin %q 依赖的服务 %q 不存在", c.PluginID, name))
	}
	return v
}

// Emit lets a plugin publish lifecycle/domain events on the shared bus.
func (c *Context) Emit(name string, payload any) { c.events.Emit(name, payload) }

// State is the observable lifecycle state used by inventory.
type State string

const (
	StateRegistered State = "registered"
	StateApplied    State = "applied"
	StateFailed     State = "failed"
	StateDisposed   State = "disposed"
)

type record struct {
	plugin Plugin
	state  State
	order  int
	names  []string // service names provided by this plugin, withdrawal order
}

// Registry owns plugin records. It is not safe for concurrent Apply/Dispose
// calls; the host serializes lifecycle operations (C3 does this).
type Registry struct {
	events   Emitter
	byID     map[string]*record
	services map[string]any
	seq      int
}

func NewRegistry(events Emitter) *Registry {
	if events == nil {
		events = nopEmitter{}
	}
	return &Registry{events: events, byID: map[string]*record{}, services: map[string]any{}}
}

// Register validates identity and dependency syntax but does not apply.
func (r *Registry) Register(p Plugin) error {
	if p == nil {
		return errors.New("cordis: plugin 为 nil")
	}
	m := p.Meta()
	if m.ID == "" || m.Version == "" {
		return errors.New("cordis: plugin 必须有 id 与 version")
	}
	if _, exists := r.byID[m.ID]; exists {
		return fmt.Errorf("cordis: plugin %q 重复注册", m.ID)
	}
	r.seq++
	r.byID[m.ID] = &record{plugin: p, state: StateRegistered, order: r.seq}
	r.events.Emit("cordis/registered", map[string]any{"pluginId": m.ID, "version": m.Version})
	return nil
}

// Plugins returns a stable registration-order snapshot.
func (r *Registry) Plugins() []Plugin {
	recs := make([]*record, 0, len(r.byID))
	for _, rec := range r.byID {
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].order < recs[j].order })
	out := make([]Plugin, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.plugin)
	}
	return out
}

// order computes a stable topological order: dependencies first, registration
// order as tie-breaker. A dependency on an unknown plugin, or a cycle, is a
// structured error (not a silent skip).
func (r *Registry) order() ([]*record, error) {
	recs := make([]*record, 0, len(r.byID))
	for _, rec := range r.byID {
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].order < recs[j].order })
	// Dependent edges: rec -> dependency record. Only Requires plugin IDs are
	// treated as plugin dependencies; Requires are also used as service checks.
	deps := map[string][]string{}
	for _, rec := range recs {
		m := rec.plugin.Meta()
		for _, dep := range m.Requires {
			if _, ok := r.byID[dep]; !ok {
				return nil, fmt.Errorf("cordis: plugin %q 依赖未知插件 %q", m.ID, dep)
			}
			deps[m.ID] = append(deps[m.ID], dep)
		}
	}
	var out []*record
	state := map[string]int{} // 0 unvisited, 1 visiting, 2 done
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("cordis: 插件依赖出现环（%s）", id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		out = append(out, r.byID[id])
		return nil
	}
	for _, rec := range recs {
		if err := visit(rec.plugin.Meta().ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Inventory is an inspect snapshot in activation order.
func (r *Registry) Inventory() []map[string]any {
	ordered, err := r.order()
	if err != nil {
		ordered = nil
	}
	out := make([]map[string]any, 0, len(r.byID))
	for _, rec := range ordered {
		if rec.state == StateDisposed || rec.state == StateFailed {
			continue
		}
		m := rec.plugin.Meta()
		out = append(out, map[string]any{
			"id": m.ID, "version": m.Version, "state": string(rec.state),
			"requires": append([]string(nil), m.Requires...),
		})
	}
	return out
}

// ApplyAll applies in dependency order. On any error (including context
// cancellation) all plugins applied during this call are disposed in reverse
// order; the original error is joined with disposal errors, never swallowed.
func (r *Registry) ApplyAll(ctx context.Context) (err error) {
	ordered, err := r.order()
	if err != nil {
		return err
	}
	var applied []*record
	rollback := func(cause error) error {
		var errs []error
		errs = append(errs, cause)
		for i := len(applied) - 1; i >= 0; i-- {
			rec := applied[i]
			if derr := r.disposeOne(ctx, rec); derr != nil {
				errs = append(errs, derr)
			}
		}
		return errors.Join(errs...)
	}
	for _, rec := range ordered {
		if rec.state == StateApplied {
			continue
		}
		if cerr := ctx.Err(); cerr != nil {
			return rollback(fmt.Errorf("cordis: apply 取消: %w", cerr))
		}
		if err := r.applyOne(rec); err != nil {
			rec.state = StateFailed
			return rollback(err)
		}
		applied = append(applied, rec)
	}
	return nil
}

func (r *Registry) applyOne(rec *record) error {
	m := rec.plugin.Meta()
	ctx := &Context{PluginID: m.ID, reg: r, events: r.events}
	r.events.Emit("cordis/applying", map[string]any{"pluginId": m.ID})
	if err := rec.plugin.Apply(ctx); err != nil {
		r.withdraw(ctx.names)
		return fmt.Errorf("cordis: plugin %q apply 失败: %w", m.ID, err)
	}
	for _, dep := range m.Requires {
		if r.byID[dep].state != StateApplied {
			r.withdraw(ctx.names)
			return fmt.Errorf("cordis: plugin %q 的依赖插件 %q 未处于 applied", m.ID, dep)
		}
	}
	rec.names = append([]string(nil), ctx.names...)
	rec.state = StateApplied
	r.events.Emit("cordis/applied", map[string]any{"pluginId": m.ID, "services": sorted(ctx.names)})
	return nil
}

func sorted(in []string) []string { out := append([]string(nil), in...); sort.Strings(out); return out }

func (r *Registry) withdraw(names []string) {
	for i := len(names) - 1; i >= 0; i-- {
		delete(r.services, names[i])
	}
}

func (r *Registry) disposeOne(ctx context.Context, rec *record) error {
	if rec.state != StateApplied {
		return nil
	}
	m := rec.plugin.Meta()
	ictx := &Context{PluginID: m.ID, reg: r, events: r.events}
	if err := rec.plugin.Dispose(ictx); err != nil {
		rec.state = StateFailed
		return fmt.Errorf("cordis: plugin %q dispose 失败: %w", m.ID, err)
	}
	r.withdraw(rec.names)
	rec.names = nil
	rec.state = StateDisposed
	r.events.Emit("cordis/disposed", map[string]any{"pluginId": m.ID})
	return nil
}

// DisposeAll disposes applied plugins in reverse activation order.
func (r *Registry) DisposeAll(ctx context.Context) error {
	ordered, err := r.order()
	if err != nil {
		return err
	}
	var errs []error
	for i := len(ordered) - 1; i >= 0; i-- {
		if ordered[i].state != StateApplied {
			continue
		}
		if cerr := ctx.Err(); cerr != nil {
			errs = append(errs, fmt.Errorf("cordis: dispose 取消: %w", cerr))
			continue
		}
		if derr := r.disposeOne(ctx, ordered[i]); derr != nil {
			errs = append(errs, derr)
		}
	}
	return errors.Join(errs...)
}

// Manifest is the deterministic inspect document. Hash is over the canonical
// JSON of the other fields (entries sorted by id), so restart with the same
// plugin set yields the same hash.
type Manifest struct {
	Version int             `json:"version"`
	Hash    string          `json:"hash"`
	Plugins []ManifestEntry `json:"plugins"`
}

type ManifestEntry struct {
	ID       string   `json:"id"`
	Version  string   `json:"version"`
	State    string   `json:"state"`
	Requires []string `json:"requires,omitempty"`
}

func (r *Registry) Manifest() (Manifest, error) {
	inv := r.Inventory()
	entries := make([]ManifestEntry, 0, len(inv))
	for _, it := range inv {
		req, _ := it["requires"].([]string)
		entries = append(entries, ManifestEntry{
			ID: it["id"].(string), Version: it["version"].(string),
			State: it["state"].(string), Requires: req,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	doc := Manifest{Version: 1, Plugins: entries}
	raw, err := json.Marshal(doc)
	if err != nil {
		return Manifest{}, err
	}
	sum := sha256.Sum256(raw)
	doc.Hash = hex.EncodeToString(sum[:])
	return doc, nil
}

// SyncManifest writes <home>/state/cordis/manifest.json atomically.
func (r *Registry) SyncManifest(home string) (Manifest, error) {
	doc, err := r.Manifest()
	if err != nil {
		return Manifest{}, err
	}
	dir := filepath.Join(home, "state", "cordis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("cordis: 建 manifest 目录: %w", err)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	raw = append(raw, '\n')
	tmp := filepath.Join(dir, "manifest.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "manifest.json")); err != nil {
		return Manifest{}, err
	}
	return doc, nil
}

// LoadManifest reads and verifies a manifest hash.
func LoadManifest(home string) (Manifest, error) {
	path := filepath.Join(home, "state", "cordis", "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var doc Manifest
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Manifest{}, err
	}
	if doc.Version != 1 {
		return Manifest{}, fmt.Errorf("cordis: manifest version=%d 不支持", doc.Version)
	}
	claim := doc.Hash
	if claim == "" {
		return Manifest{}, errors.New("cordis: manifest 缺 hash")
	}
	doc.Hash = ""
	canonical, err := json.Marshal(doc)
	if err != nil {
		return Manifest{}, err
	}
	sum := sha256.Sum256(canonical)
	if got := hex.EncodeToString(sum[:]); got != claim {
		return Manifest{}, fmt.Errorf("cordis: manifest hash 不匹配（文件 %s，重算 %s）", claim, got)
	}
	doc.Hash = claim
	return doc, nil
}
