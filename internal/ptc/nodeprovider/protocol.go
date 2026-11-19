package nodeprovider

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
)

// lookNode 从 PATH 找 node；Windows 上再兜一次 node.exe。
func lookNode() (string, error) {
	if p, err := exec.LookPath("node"); err == nil && p != "" {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("node.exe"); err == nil && p != "" {
			return p, nil
		}
	}
	return "", ErrNodeUnavailable
}

// runVersion 真的跑一次 `node --version`，确认它不是坏的可执行文件。
func runVersion(ctx context.Context, nodePath string) (string, error) {
	cmd := exec.CommandContext(ctx, nodePath, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// frame 是控制通道上的一帧。
//
// 通道协议是本 provider **私有**的（官方那套 wire 不是公开契约，我们也从没宣称
// 与之兼容）：stdin/stdout 各一行一帧 JSON。名字只服务本包的 host 与 worker 两边。
//
// `format` / `retainsState` 两个字段是官方 `ProgramBootData`（`src/protocol.ts`）
// 的同名事实：前者选求值器（function-body / script），后者决定结算之后进程是否
// 继续活着。取值跟着官方的词走，不另起名字。
type frame struct {
	T string `json:"t"`

	// ready
	PID     int    `json:"pid,omitempty"`
	NodeVer string `json:"version,omitempty"`

	// eval / log / cancel 共用
	ID   uint64 `json:"id,omitempty"`
	Text string `json:"text,omitempty"`

	// eval 载荷
	Program      string   `json:"program,omitempty"`
	Format       string   `json:"format,omitempty"`
	Cwd          string   `json:"cwd,omitempty"`
	MaxOutput    int64    `json:"maxOutputBytes,omitempty"`
	Namespaces   []wireNS `json:"namespaces,omitempty"`
	RetainsState bool     `json:"retainsState,omitempty"`

	// bind
	Call uint64          `json:"call,omitempty"`
	NS   string          `json:"ns,omitempty"`
	FN   string          `json:"fn,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`

	// bindResult
	OK      *bool           `json:"ok,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	Message string          `json:"message,omitempty"`

	// done / fail
	Has  *bool  `json:"has,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// wireNS 是一个命名空间在通道上的形状：只传"成员名"与可选的错误类契约，
// 函数本体留在宿主侧（调用时按名字回查）。
type wireNS struct {
	Global     string          `json:"global"`
	Members    []string        `json:"members"`
	ErrorClass *wireErrorClass `json:"errorClass,omitempty"`
}

type wireErrorClass struct {
	Name               string `json:"name"`
	MemberNameProperty string `json:"memberNameProperty"`
}

func boolPtr(v bool) *bool { return &v }
