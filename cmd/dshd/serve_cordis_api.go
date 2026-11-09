package main

// PARITY-CORDIS-C3B: dynamicCordisRunner 12 方法的真实宿主实现。
//
// 数据来源只有两处：
//   - Go 核心：internal/cordis（registry/manifest/生命周期）
//   - 外部受管 node：internal/cordis/extern（真实插件包加载/调用/结算）
// 没有 501、没有静态成功；无 node 时返回结构化不可用 + Hint。

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
	"strings"
	"sync"
	"time"

	"dshgo/internal/cordis"
	"dshgo/internal/cordis/extern"
	"dshgo/internal/transport"
)

type cordisEntry struct {
	ID      string   `json:"id"`
	Version string   `json:"version"`
	Kind    string   `json:"kind"` // go | js
	State   string   `json:"state"`
	Dir     string   `json:"dir,omitempty"`
	Methods []string `json:"methods,omitempty"`
	PID     int      `json:"pid,omitempty"`
}

type cordisService struct {
	home string
	reg  *cordis.Registry

	mu       sync.Mutex
	entries  map[string]*cordisEntry
	clients  map[string]*extern.Client
	runs     map[string]string // requestId -> pluginId
	nodePath string
}

func newCordisService(home string) *cordisService {
	return &cordisService{
		home: home, reg: cordis.NewRegistry(nil),
		entries: map[string]*cordisEntry{}, clients: map[string]*extern.Client{},
		runs: map[string]string{},
	}
}

func (s *cordisService) RegisterGo(id, version string) error {
	if err := s.reg.Register(&goCordisPlugin{id: id, version: version}); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[id] = &cordisEntry{ID: id, Version: version, Kind: "go", State: "registered"}
	return nil
}

type goCordisPlugin struct{ id, version string }

func (p *goCordisPlugin) Meta() cordis.Meta { return cordis.Meta{ID: p.id, Version: p.version} }
func (p *goCordisPlugin) Apply(*cordis.Context) error {
	return nil
}
func (p *goCordisPlugin) Dispose(*cordis.Context) error { return nil }

func (s *cordisService) pluginDir(id string) string {
	return filepath.Join(s.home, "cordis-plugins", id)
}

func (s *cordisService) ensureJS(ctx context.Context, id string) (*extern.Client, *cordisEntry, error) {
	s.mu.Lock()
	if c := s.clients[id]; c != nil && c.Alive() {
		e := s.entries[id]
		s.mu.Unlock()
		if e == nil {
			return nil, nil, fmt.Errorf("cordis: 插件 %q 无记录", id)
		}
		return c, e, nil
	}
	s.mu.Unlock()

	dir := s.pluginDir(id)
	c, err := extern.Start(ctx, extern.Options{NodePath: s.nodePath})
	if err != nil {
		return nil, nil, err
	}
	info, err := c.LoadPackage(ctx, dir)
	if err != nil {
		_ = c.Close()
		return nil, nil, err
	}
	e := &cordisEntry{ID: id, Version: info.Version, Kind: "js", State: "running", Dir: dir, Methods: info.Methods, PID: info.PID}
	s.mu.Lock()
	s.clients[id] = c
	s.entries[id] = e
	s.mu.Unlock()
	return c, e, nil
}

func (s *cordisService) findClientFor(requestID string) (*extern.Client, *cordisEntry, bool) {
	s.mu.Lock()
	id, ok := s.runs[requestID]
	c := s.clients[id]
	e := s.entries[id]
	s.mu.Unlock()
	return c, e, ok && c != nil
}

func (s *cordisService) HandleOfficialCordis(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var p map[string]json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("参数不是 JSON 对象: %w", err)
	}
	str := func(key string) string {
		var v string
		_ = json.Unmarshal(p[key], &v)
		return v
	}
	switch method {
	case string(transport.MethodDynamicCordisRunnerInventory):
		return s.inventory(), nil
	case string(transport.MethodDynamicCordisRunnerGetClientCode):
		id := str("pluginId")
		if id == "" {
			return nil, errors.New("pluginId 必填")
		}
		c, _, err := s.ensureJS(ctx, id)
		if err != nil {
			return nil, err
		}
		file := str("file")
		if file == "" {
			file = "client/client.js"
		}
		code, err := c.GetClientCode(ctx, s.pluginDir(id), file)
		if err != nil {
			return nil, err
		}
		return map[string]any{"pluginId": id, "file": file, "code": string(code), "bytes": len(code)}, nil
	case string(transport.MethodDynamicCordisRunnerInvoke):
		id, reqID := str("pluginId"), str("requestId")
		if id == "" || str("method") == "" {
			return nil, errors.New("pluginId/method 必填")
		}
		c, _, err := s.ensureJS(ctx, id)
		if err != nil {
			return nil, err
		}
		var args any
		if rawArgs := p["args"]; len(rawArgs) > 0 {
			_ = json.Unmarshal(rawArgs, &args)
		}
		var out any
		rec, err := c.Invoke(ctx, reqID, str("method"), args, &out)
		s.mu.Lock()
		s.runs[reqID] = id
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": out, "run": rec}, nil
	case string(transport.MethodDynamicCordisRunnerRunHostHalf):
		id := str("pluginId")
		if id == "" {
			return nil, errors.New("pluginId 必填")
		}
		_, e, err := s.ensureJS(ctx, id)
		if err != nil {
			return nil, err
		}
		return e, nil
	case string(transport.MethodDynamicCordisRunnerSettleUserRun):
		reqID := str("requestId")
		c, e, ok := s.findClientFor(reqID)
		if !ok {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "requestId 没有运行记录"}
		}
		rec, err := c.SettleUserRun(ctx, reqID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"requestId": reqID, "pluginId": e.ID, "run": rec}, nil
	case string(transport.MethodDynamicCordisRunnerResolveRequestRun):
		reqID := str("requestId")
		c, _, ok := s.findClientFor(reqID)
		if !ok {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "requestId 没有运行记录"}
		}
		rec, found := c.ResolveRequestRun(reqID)
		if !found {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "requestId 没有运行记录"}
		}
		return rec, nil
	case string(transport.MethodDynamicCordisRunnerResolveInspectQuery):
		id := str("pluginId")
		if id == "" {
			return s.inventory(), nil
		}
		c, _, err := s.ensureJS(ctx, id)
		if err != nil {
			return nil, err
		}
		info, err := c.ResolveInspectQuery(ctx)
		if err != nil {
			return nil, err
		}
		return info, nil
	case string(transport.MethodDynamicCordisRunnerSyncInspectManifest):
		return s.syncManifest()
	case string(transport.MethodDynamicCordisRunnerReportClientGuardFailure),
		string(transport.MethodDynamicCordisRunnerReportRenderFailure):
		return s.report(method, str("pluginId"), str("failure"), raw)
	case string(transport.MethodDynamicCordisRunnerStopFromPanel):
		id := str("pluginId")
		s.mu.Lock()
		c := s.clients[id]
		e := s.entries[id]
		delete(s.clients, id)
		if e != nil {
			e.State = "stopped"
			e.PID = 0
		}
		s.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
		if e == nil {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "pluginId 不存在"}
		}
		return e, nil
	case string(transport.MethodDynamicCordisRunnerUndefineFromPanel):
		id := str("pluginId")
		s.mu.Lock()
		c := s.clients[id]
		_, existed := s.entries[id]
		delete(s.clients, id)
		delete(s.entries, id)
		s.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
		if !existed {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "pluginId 不存在"}
		}
		return map[string]any{"pluginId": id, "undefined": true}, nil
	default:
		return nil, fmt.Errorf("不是 dynamicCordisRunner 方法: %s", method)
	}
}

func (s *cordisService) inventory() []*cordisEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*cordisEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *cordisService) syncManifest() (map[string]any, error) {
	entries := s.inventory()
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	doc := map[string]any{"version": 1, "plugins": entries}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	doc["hash"] = hash
	dir := filepath.Join(s.home, "state", "cordis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(out, '\n'), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"path": filepath.ToSlash(filepath.Join(dir, "manifest.json")), "hash": hash, "plugins": entries}, nil
}

func (s *cordisService) report(method, pluginID, failure string, raw json.RawMessage) (map[string]any, error) {
	dir := filepath.Join(s.home, "state", "cordis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ev := map[string]any{
		"id":     fmt.Sprintf("%s-%d", method, time.Now().UnixNano()),
		"method": method, "pluginId": pluginID, "failure": failure,
		"at": time.Now().UTC().Format(time.RFC3339Nano), "payload": json.RawMessage(raw),
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	return map[string]any{"eventId": ev["id"], "recorded": true, "path": filepath.ToSlash(filepath.Join(dir, "events.jsonl"))}, nil
}

// 编译期证明实现满足 transport 接口；也帮助 C3 的路由接线。
var _ transport.OfficialCordisAPI = (*cordisService)(nil)

func (s *cordisService) String() string { return strings.TrimSpace(filepath.Base(s.home)) }
