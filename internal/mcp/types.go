// Package mcp 是 MCP 服务管理面的**唯一状态机**与官方协议算法层。
//
// 它只放两样东西，都不含真实进程代码（进程控制留在 modules/tools 的既有客户端上，
// 那里有 Windows Job Object 与有界的子进程回收）：
//
//   - Registry：一份服务状态（官方 `McpServerManager.list()` 的 View 形状）与它的
//     `watch` 订阅流。`mcp.list`/`mcp.reconnect`/`mcp.toggle` 与
//     `mcpManager.probe`/`restart`/`watch` **共用**这一份，任何一侧改状态都经过
//     `Publish`，所以不存在两套状态机。
//   - probe/restart 的算法：官方 `probe.ts` 的整场预算、翻页与截断、阶段归属、
//     清理确认位；以及 `restart` 的"清理未确认就禁止替换"。
//
// 契约来源（逐字段，不在本包解释成别的形状）：
//   - docs/evidence/official-method-contract-delta-0.2.2-rc.2.md 的
//     `### mcpManager/probe`、`### mcpManager/restart`、`### mcpManager/watch`
//   - @deepseek-ai/dsh-mcp-manager/src/{index,types}.ts
//   - @deepseek-ai/dsh-mcp-client/src/{types,probe,generation,status}.ts
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// MCP 连接状态词表（官方 McpConnectionStatus + McpManagedServerStatus 的并集）。
const (
	StateClosed             = "closed"
	StateWaitingCredentials = "waiting-credentials"
	StateConnecting         = "connecting"
	StateConnected          = "connected"
	StateReconnecting       = "reconnecting"
	StateFailed             = "failed"
	StateDisabled           = "disabled"
)

// failed 的 reason 词表：前三个来自连接监管者，后两个来自管理面本身。
const (
	ReasonGaveUp             = "gave-up"
	ReasonReconnectDisabled  = "reconnect-disabled"
	ReasonUnclosedGeneration = "unclosed-generation"
	ReasonLoad               = "load"
	ReasonCleanup            = "cleanup"
)

// CloseResult 是官方 `McpCloseResult`：传输关闭与本地清理**各自独立**报告。
type CloseResult struct {
	// CloseConfirmed 传输关闭已确认，或者压根没建 transport；
	// 未完成的协商保持未确认。
	CloseConfirmed bool
	// CleanupError 清理失败或超出预算（即使传输关闭已确认也会给）。
	CleanupError string
}

func (c CloseResult) json() ([]byte, error) {
	fields := []jsonField{{"closeConfirmed", c.CloseConfirmed}}
	if c.CleanupError != "" {
		fields = append(fields, jsonField{"cleanupError", c.CleanupError})
	}
	return marshalFields(fields)
}

// ServerInfo 是 initialize 里 server 自报的身份（SDK 拿到时才有）。
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Status 是官方 `McpManagedServerStatus` 的 Go 形状。
//
// 所有字段都是可比较的（没有指针/切片），所以 Registry 能用 `==` 做官方那条
// "状态没变就不发帧"的去抖——watch 的每一帧都对应一次真实变化。
type Status struct {
	State string
	// Close 用于 closed 与 failed(cleanup / unclosed-generation) 两个分支。
	Close CloseResult
	// LastError 用于 waiting-credentials / reconnecting / failed。
	LastError string
	// Attempt 用于 connecting / reconnecting。
	Attempt int
	// ToolCount、ConnectedAt 用于 connected。
	ToolCount   int
	ConnectedAt int64
	// MaxAttempts、NextRetryAt 用于 reconnecting。
	MaxAttempts int
	NextRetryAt int64
	// Reason 用于 failed。
	Reason string
}

// Validate 按官方 oneOf 逐分支校验；不合法的组合宁可报错，也不输出一个
// 官方客户端读不懂的状态。
func (s Status) Validate() error {
	empty := s.LastError == "" && s.Attempt == 0 && s.ToolCount == 0 &&
		s.ConnectedAt == 0 && s.MaxAttempts == 0 && s.NextRetryAt == 0 &&
		s.Reason == "" && s.Close == CloseResult{}
	switch s.State {
	case StateDisabled:
		if !empty {
			return fmt.Errorf("mcp: disabled 状态不许带其它字段: %+v", s)
		}
		return nil
	case StateClosed:
		sawOther := s.LastError != "" || s.Attempt != 0 || s.ToolCount != 0 || s.ConnectedAt != 0 ||
			s.MaxAttempts != 0 || s.NextRetryAt != 0 || s.Reason != ""
		if sawOther {
			return fmt.Errorf("mcp: closed 状态只带 result: %+v", s)
		}
		return nil
	case StateWaitingCredentials:
		sawOther := s.Attempt != 0 || s.ToolCount != 0 || s.ConnectedAt != 0 ||
			s.MaxAttempts != 0 || s.NextRetryAt != 0 || s.Reason != "" || s.Close != CloseResult{}
		if sawOther {
			return fmt.Errorf("mcp: waiting-credentials 只带 lastError: %+v", s)
		}
		return nil
	case StateConnecting:
		sawOther := s.LastError != "" || s.ToolCount != 0 || s.ConnectedAt != 0 ||
			s.MaxAttempts != 0 || s.NextRetryAt != 0 || s.Reason != "" || s.Close != CloseResult{}
		if sawOther {
			return fmt.Errorf("mcp: connecting 只带 attempt: %+v", s)
		}
		return nil
	case StateConnected:
		sawOther := s.LastError != "" || s.Attempt != 0 || s.MaxAttempts != 0 ||
			s.NextRetryAt != 0 || s.Reason != "" || s.Close != CloseResult{}
		if sawOther {
			return fmt.Errorf("mcp: connected 只带 toolCount/connectedAt: %+v", s)
		}
		return nil
	case StateReconnecting:
		sawOther := s.ToolCount != 0 || s.ConnectedAt != 0 || s.Reason != "" || s.Close != CloseResult{}
		if sawOther {
			return fmt.Errorf("mcp: reconnecting 只带 attempt/maxAttempts/nextRetryAtMs/lastError: %+v", s)
		}
		if s.MaxAttempts < 0 || s.Attempt < 0 {
			return fmt.Errorf("mcp: reconnecting 的 attempt 不许为负: %+v", s)
		}
		return nil
	case StateFailed:
		switch s.Reason {
		case ReasonGaveUp, ReasonReconnectDisabled, ReasonUnclosedGeneration:
			sawOther := s.Attempt != 0 || s.ToolCount != 0 || s.ConnectedAt != 0 || s.MaxAttempts != 0 || s.NextRetryAt != 0
			if sawOther {
				return fmt.Errorf("mcp: failed/%s 只带 lastError/result: %+v", s.Reason, s)
			}
			return nil
		case ReasonLoad:
			if s.LastError == "" {
				return fmt.Errorf("mcp: failed/load 必须带 lastError: %+v", s)
			}
			return nil
		case ReasonCleanup:
			sawOther := s.LastError != "" || s.Attempt != 0 || s.ToolCount != 0 ||
				s.ConnectedAt != 0 || s.MaxAttempts != 0 || s.NextRetryAt != 0
			if sawOther {
				return fmt.Errorf("mcp: failed/cleanup 只带 result: %+v", s)
			}
			return nil
		case "":
			return fmt.Errorf("mcp: failed 必须给出 reason: %+v", s)
		default:
			return fmt.Errorf("mcp: failed 的 reason 非法 %q", s.Reason)
		}
	case "":
		return fmt.Errorf("mcp: 状态必须给出 state")
	default:
		return fmt.Errorf("mcp: 未知 state %q", s.State)
	}
}

// MarshalJSON 逐分支输出官方 oneOf 的形状（键顺序固定，方便金样本比对）。
func (s Status) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	fields := []jsonField{{"state", s.State}}
	switch s.State {
	case StateClosed:
		raw, err := s.Close.json()
		if err != nil {
			return nil, err
		}
		fields = append(fields, jsonField{"result", rawJSON(raw)})
	case StateWaitingCredentials:
		fields = append(fields, jsonField{"lastError", s.LastError})
	case StateConnecting:
		fields = append(fields, jsonField{"attempt", s.Attempt})
	case StateConnected:
		fields = append(fields, jsonField{"toolCount", s.ToolCount}, jsonField{"connectedAt", s.ConnectedAt})
	case StateReconnecting:
		fields = append(fields,
			jsonField{"attempt", s.Attempt},
			jsonField{"maxAttempts", s.MaxAttempts},
			jsonField{"nextRetryAtMs", s.NextRetryAt},
			jsonField{"lastError", s.LastError})
	case StateFailed:
		fields = append(fields, jsonField{"reason", s.Reason})
		switch s.Reason {
		case ReasonLoad:
			fields = append(fields, jsonField{"lastError", s.LastError})
		case ReasonCleanup, ReasonUnclosedGeneration:
			raw, err := s.Close.json()
			if err != nil {
				return nil, err
			}
			fields = append(fields, jsonField{"result", rawJSON(raw)})
		default:
			if s.LastError != "" {
				fields = append(fields, jsonField{"lastError", s.LastError})
			}
		}
	case StateDisabled:
		// 只有 state。
	}
	return marshalFields(fields)
}

// View 是官方 `McpManagedServerView`：watch 数组的元素、list 的行。
type View struct {
	// Name 是配置里的 `servers` 键或注册名，也是这个实例的工具命名空间。
	Name string `json:"name"`
	// Configured 表示配置项或注册项是否还在；删除后仍留存清理失败的记录为 false。
	Configured bool `json:"configured"`
	// Status 是当前运行状态。
	Status Status `json:"status"`
}

// ---- 有序 JSON 小工具 ----

type jsonField struct {
	key   string
	value any
}

// rawJSON 让已经编码好的片段原样嵌入（保持嵌套顺序与金样本一致）。
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) { return r, nil }

// marshalFields 按给定顺序编码对象。encoding/json 对 map 会按字母排序，
// 而契约金样本与官方输出都按语义顺序，所以这里显式拼。
func marshalFields(fields []jsonField) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, field := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(field.key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		value, err := json.Marshal(field.value)
		if err != nil {
			return nil, err
		}
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
