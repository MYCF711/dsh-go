// Package pluginmgr manages the task-scoped dsh.local-plugin@1 directory format.
//
// 两层，职责不混：
//   - `Manager` 是**存储**：复制、校验、事务、`state.json`。它自己从不执行插件代码。
//   - `Loader`（loader.go）是**运行时**：把已安装的包真的装载成工具、执行、卸载；
//     它由 `NewLoader` 挂到管理器上，之后 enable/disable/uninstall/update/restore
//     会自动同步装载集合——"enabled 但没跑起来"与"disabled 但还能调"因此不可能出现。
//
// 受支持的是 **Go/WASM** 插件（`kind = "wasm"`，GOOS=wasip1 GOARCH=wasm
// -buildmode=c-shared）。JS/Cordis 与 process 形态在安装边界上显式拒绝
// （`unsupported_plugin`）——本仓不嵌 JS 运行时（AGENTS R6），也没有 process 后端。
package pluginmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const (
	ManifestSchema = "dsh.local-plugin@1"
	stateSchema    = "dsh.local-plugin-state@1"
	txSchema       = "dsh.local-plugin-transaction@1"
)

// PluginInfo is the additive Transport projection of one installed package.
type PluginInfo struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	Kind            string `json:"kind"`
	Entry           string `json:"entry"`
	Enabled         bool   `json:"enabled"`
	Source          string `json:"source"`
	PreviousVersion string `json:"previousVersion,omitempty"`
}

// ReconcileAction describes one recovery decision; it is never a fabricated success.
type ReconcileAction struct {
	TransactionID string `json:"transactionId"`
	Action        string `json:"action"`
	ID            string `json:"id"`
	Detail        string `json:"detail"`
}

// ReconcileResult reports all incomplete transactions processed in this call.
type ReconcileResult struct {
	Actions   []ReconcileAction `json:"actions"`
	Recovered int               `json:"recovered"`
}

// Error is the stable four-field error used by both HTTP and CLI adapters.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Hint      string `json:"hint"`
	Retryable bool   `json:"retryable"`
	Status    int    `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func errorf(code string, status int, retryable bool, hint, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Retryable: retryable, Hint: hint, Message: fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...any) *Error {
	return errorf("bad_request", http.StatusBadRequest, false, "检查裸参数中的 id/path/version", format, args...)
}

func notFound(id string) *Error {
	return errorf("not_found", http.StatusNotFound, false, "先调用 plugin.list 核对已安装 id", "插件 %s 不存在", id)
}

func internalError(err error) *Error {
	if err == nil {
		return nil
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return errorf("internal", http.StatusInternalServerError, true, "可重试；持续失败时检查管理器目录与事务记录", "%v", err)
}

// AsError converts an arbitrary I/O failure without discarding its message.
func AsError(err error) *Error { return internalError(err) }

// Manifest is the complete restricted local package manifest.
//
// Engines / PeerDependencies 是**可选**的兼容性字段（见 compat.go）：它们的语义
// 与官方 package.json 的同名字段一致，只用来回答"这个包能不能在当前的 dsh
// runtime 上跑"。没有这两个字段的包（既有全部包）行为不变。
type Manifest struct {
	Schema           string            `json:"schema"`
	ID               string            `json:"id"`
	Version          string            `json:"version"`
	Kind             string            `json:"kind"`
	Entry            string            `json:"entry"`
	Files            map[string]string `json:"files"`
	Engines          *Engines          `json:"engines,omitempty"`
	PeerDependencies map[string]string `json:"peerDependencies,omitempty"`
	// Tools 是这个包提供、并且**真的能被模型调用**的工具声明（可选）。
	//
	// 为什么工具元数据必须写在 manifest 里：Name/Description/Schema/Caps 是
	// 稳定性契约（R4），审批与沙箱也靠 Caps 判定（AGENTS §2）——让 wasm 客人
	// 自己声明"我只会读"，等于让它自己给自己发许可证。客人只负责执行，
	// 身份与能力由包自己声明、由宿主校验。
	//
	// 没有这个字段的包（既有全部包）行为不变：装载后不提供任何能力，
	// 只能经 `dshd wasm call` 手动调。
	Tools []ToolDeclaration `json:"tools,omitempty"`
}

// ToolDeclaration 是包内一个可执行工具的声明。
//
// 与 dsh.toml 的 `[[module.tool]]` 同义，区别只是**随包一起走**：
// dsh.toml 是宿主侧白名单，plugin.json 是包自己的身份。
type ToolDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Caps        []string        `json:"caps,omitempty"`
	TimeoutMS   int             `json:"timeout_ms,omitempty"`
}

type versionSource struct {
	Source string `json:"source"`
}

type pluginRecord struct {
	ID              string                   `json:"id"`
	Version         string                   `json:"version"`
	Enabled         bool                     `json:"enabled"`
	PreviousVersion string                   `json:"previousVersion,omitempty"`
	Versions        map[string]versionSource `json:"versions"`
}

type persistentState struct {
	Schema  string                   `json:"schema"`
	Plugins map[string]*pluginRecord `json:"plugins"`
	// ToolsMounted 是官方 `pluginManager.toolsMounted`（dsh-base/cordis.patch.yml:15-18）
	// 在宿主侧的真实落点：**插件包提供的工具要不要挂进模型工具表**。
	//
	// 为什么是指针而不是 bool：三态才是真话——nil = 这份状态从没被设过（旧 state.json、
	// 新装机），此时按宿主既有行为挂载；显式 false = 用户/部署把插件工具下线了。
	// 用 bool 会把"没设"与"设成 false"混成一个零值，重开管理器后无法区分。
	ToolsMounted *bool `json:"toolsMounted,omitempty"`
}

func emptyState() *persistentState {
	return &persistentState{Schema: stateSchema, Plugins: map[string]*pluginRecord{}}
}

func cloneState(in *persistentState) *persistentState {
	out := emptyState()
	if in.ToolsMounted != nil {
		value := *in.ToolsMounted
		out.ToolsMounted = &value
	}
	for id, record := range in.Plugins {
		copyRecord := *record
		copyRecord.Versions = make(map[string]versionSource, len(record.Versions))
		for version, source := range record.Versions {
			copyRecord.Versions[version] = source
		}
		out.Plugins[id] = &copyRecord
	}
	return out
}
