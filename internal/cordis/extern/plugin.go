package extern

// C2B: real Cordis plugin package primitives on top of the managed process.
// The host still contains no JS engine: package.json/host.cjs/client files are
// read by the external node process; Go only transports bytes and records runs.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type PluginInfo struct {
	Dir     string   `json:"dir"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Main    string   `json:"main"`
	Methods []string `json:"methods"`
	PID     int      `json:"pid"`
}

type RunRecord struct {
	RequestID  string          `json:"requestId"`
	Method     string          `json:"method"`
	Started    time.Time       `json:"started"`
	DurationMS int64           `json:"durationMs"`
	ExitCode   int             `json:"exitCode"`
	PID        int             `json:"pid"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Settled    bool            `json:"settled"`
}

type pluginState struct {
	mu   sync.Mutex
	runs map[string]*RunRecord
}

func (c *Client) plugin() *pluginState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runs == nil {
		c.runs = map[string]*RunRecord{}
	}
	return &pluginState{runs: c.runs}
}

// LoadPackage validates package.json (real file) then asks the node process to
// require the host entry. Returns the real exported method list and pid.
func (c *Client) LoadPackage(ctx context.Context, dir string) (*PluginInfo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(abs, "package.json"))
	if err != nil {
		return nil, fmt.Errorf("cordis/extern: 读 package.json: %w", err)
	}
	var pkg struct{ Name, Version, Main string }
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("cordis/extern: package.json 非法: %w", err)
	}
	if pkg.Main == "" {
		pkg.Main = "host.cjs"
	}
	var out struct {
		Name, Version, Main string
		Methods             []string
		PID                 int
	}
	if err := c.Call(ctx, "load", map[string]any{"dir": abs}, &out); err != nil {
		return nil, err
	}
	info := &PluginInfo{Dir: abs, Name: out.Name, Version: out.Version, Main: out.Main, Methods: out.Methods, PID: out.PID}
	if info.Name == "" {
		info.Name = pkg.Name
	}
	sort.Strings(info.Methods)
	return info, nil
}

// GetClientCode reads a real client-side file through the external process.
func (c *Client) GetClientCode(ctx context.Context, dir, file string) ([]byte, error) {
	var out struct {
		Path       string
		CodeBase64 string
		Bytes      int
	}
	if err := c.Call(ctx, "getClientCode", map[string]any{"dir": dir, "file": file}, &out); err != nil {
		return nil, err
	}
	code, err := base64.StdEncoding.DecodeString(out.CodeBase64)
	if err != nil {
		return nil, fmt.Errorf("cordis/extern: client 代码 base64 非法: %w", err)
	}
	if len(code) != out.Bytes {
		return nil, fmt.Errorf("cordis/extern: client 字节数不符: %d != %d", len(code), out.Bytes)
	}
	return code, nil
}

// Invoke calls a method exported by the loaded host half and records the run.
func (c *Client) Invoke(ctx context.Context, requestID, method string, args any, out any) (*RunRecord, error) {
	start := time.Now()
	rec := &RunRecord{RequestID: requestID, Method: method, Started: start, PID: c.PID()}
	var payload struct {
		Result    json.RawMessage
		PID       int
		RequestID string
	}
	err := c.Call(ctx, "invoke", map[string]any{"method": method, "args": args, "requestId": requestID}, &payload)
	rec.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		rec.Error = err.Error()
		rec.ExitCode = 1
	} else {
		rec.Result = payload.Result
		rec.ExitCode = 0
		if out != nil && len(payload.Result) > 0 {
			if uerr := json.Unmarshal(payload.Result, out); uerr != nil {
				rec.Error = uerr.Error()
				rec.ExitCode = 1
			}
		}
	}
	st := c.plugin()
	st.mu.Lock()
	st.runs[requestID] = rec
	st.mu.Unlock()
	return rec, err
}

// SettleUserRun marks a recorded run settled using the external process' real
// pid/exit code; unknown requestIds are a structured not-found.
func (c *Client) SettleUserRun(ctx context.Context, requestID string) (*RunRecord, error) {
	var out struct {
		RequestID string
		PID       int
		ExitCode  int
		SettledAt int64
	}
	if err := c.Call(ctx, "settleUserRun", map[string]any{"requestId": requestID}, &out); err != nil {
		return nil, err
	}
	st := c.plugin()
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.runs[requestID]
	if !ok {
		return nil, fmt.Errorf("cordis/extern: requestId %q 没有运行记录", requestID)
	}
	rec.ExitCode = out.ExitCode
	rec.PID = out.PID
	rec.Settled = true
	return rec, nil
}

// ResolveRequestRun is read-only: it never invents a run.
func (c *Client) ResolveRequestRun(requestID string) (*RunRecord, bool) {
	st := c.plugin()
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.runs[requestID]
	return rec, ok
}

// ResolveInspectQuery asks the real loaded plugin for its manifest view.
func (c *Client) ResolveInspectQuery(ctx context.Context) (*PluginInfo, error) {
	var out struct {
		Name, Version string
		Methods       []string
		PID           int
	}
	if err := c.Call(ctx, "resolveInspectQuery", nil, &out); err != nil {
		return nil, err
	}
	if out.Name == "" && len(out.Methods) == 0 {
		return nil, errors.New("cordis/extern: 没有已加载的插件")
	}
	return &PluginInfo{Name: out.Name, Version: out.Version, Methods: out.Methods, PID: out.PID}, nil
}
