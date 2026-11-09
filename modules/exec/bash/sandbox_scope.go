package bash

// per-call sandbox 语义（spi.SandboxScope）。
//
// 宿主（ACP / serve 的准入层）要在**起进程之前**知道这次调用到底碰什么：
// 执行类操作的 cwd 是 shell 当前目录、命令原文要绑定一次性审批、权限请求来自
// 工具参数。把这些交给工具自己报告，宿主就不用按工具名或参数名猜。

import (
	"encoding/json"
	"fmt"

	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
)

// 编译期确认实现的是宿主认的那个可选能力（签名漂了就不会静默失去闸门）。
var _ spi.SandboxScope = (*shellTool)(nil)

// SandboxCall 实现 spi.SandboxScope。
func (t *shellTool) SandboxCall(raw json.RawMessage) (spi.SandboxCall, error) {
	var a shellArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.SandboxCall{}, fmt.Errorf("参数解析失败: %w", err)
	}
	permission, err := sandbox.ParsePermission(a.SandboxPermissions)
	if err != nil {
		return spi.SandboxCall{}, err
	}
	return spi.SandboxCall{
		Operation:  string(sandbox.OperationExecute),
		Path:       t.svc.Cwd(),
		Command:    a.Command,
		Permission: string(permission),
	}, nil
}
