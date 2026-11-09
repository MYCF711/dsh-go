package pwsh

// per-call sandbox 语义（spi.SandboxScope）。
//
// 与 bash 同一套：命令原文用于绑定一次性审批，cwd 用**这次调用真正会用的**
// workdir（未给时取进程当前目录），权限请求从参数解析，宿主不需要按工具名猜。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
)

// 编译期确认实现的是宿主认的那个可选能力（签名漂了就不会静默失去闸门）。
var _ spi.SandboxScope = (*Tool)(nil)

// SandboxCall 实现 spi.SandboxScope。
func (t *Tool) SandboxCall(raw json.RawMessage) (spi.SandboxCall, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.SandboxCall{}, fmt.Errorf("参数解析失败: %w", err)
	}
	permission, err := sandbox.ParsePermission(a.SandboxPermissions)
	if err != nil {
		return spi.SandboxCall{}, err
	}
	cwd := strings.TrimSpace(a.Workdir)
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return spi.SandboxCall{}, fmt.Errorf("取当前目录失败: %w", err)
		}
		cwd = wd
	}
	cwd = filepath.FromSlash(cwd)
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	return spi.SandboxCall{
		Operation:  string(sandbox.OperationExecute),
		Path:       cwd,
		Command:    strings.TrimSpace(a.Command),
		Permission: string(permission),
	}, nil
}
