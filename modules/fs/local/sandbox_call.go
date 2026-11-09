package local

// FS 的 per-call 沙盒入口。
//
// 旧路径（CheckWritable/Write）只有进程级边界：`spi.FS` 的签名没有 ctx，
// 所以"这一次调用"的会话、权限与一次性审批**无法表达**。这里补的是带 ctx 的
// 可选能力（spi.FS 不变，旧调用方行为不变）：
//
//   - 身份：会话来自 spi.SessionFrom、调用来自 sandbox.CallIDFrom、
//     权限来自 sandbox.PermissionFrom、一次性审批来自 sandbox.EscalationFrom；
//   - 判定：`sandbox.Gate.EvaluateFS` —— 与 bash/pwsh 用的是同一个策略引擎；
//   - 拒绝：写盘前返回错误，不留下半截文件；
//   - 一次性：批准绑定 (session, call, path, permission)，换调用/换路径都失败。
//
// write/edit 工具改用这两个方法属于跨包接线，见 $S/WIRING.md。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dshgo/internal/sandbox"
)

// CheckWritableCall 是 CheckWritable 的 per-call 版本：先过策略闸门，
// 再实施读后写观测。策略拒绝时不会碰盘。
func (f *FS) CheckWritableCall(ctx context.Context, p, op string) error {
	decision, err := f.evaluateCall(ctx, sandbox.OperationWrite, p)
	if err != nil {
		return err
	}
	if decision.Action != sandbox.ActionAllow {
		return f.writePolicyError(p, decision.Error())
	}
	return f.checkReadBeforeWrite(p, op, decision.CanonicalPath)
}

// WriteCall 是 Write 的 per-call 版本：策略允许后才写；带一次性批准的
// danger-full-access 调用可以在**这一次**越过工作区写边界，且不改变进程档位。
func (f *FS) WriteCall(ctx context.Context, p string, data []byte) error {
	decision, err := f.evaluateCall(ctx, sandbox.OperationWrite, p)
	if err != nil {
		return err
	}
	if decision.Action != sandbox.ActionAllow {
		return f.writePolicyError(p, decision.Error())
	}
	abs := decision.CanonicalPath
	if abs == "" {
		return fmt.Errorf("策略没有给出 canonical 路径：%q", p)
	}
	if decision.EffectiveMode != sandbox.ModeDangerFullAccess {
		// 防御性再走一次 FS 自己的 canonical 写边界：per-call 判定与进程边界
		// 必须一致地收窄；只有显式批准过的 danger 调用才跳过这一步。
		_, boundary, _ := f.snapshot()
		if _, werr := boundary.CheckWrite(abs); werr != nil {
			return f.writePolicyError(p, werr)
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("建目录失败: %w", err)
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return fmt.Errorf("写 %s 失败: %w", p, err)
	}
	if st, err := os.Stat(abs); err == nil {
		f.mark(filepath.ToSlash(abs), st.ModTime(), st.Size())
	}
	return nil
}

// evaluateCall 把 ctx 上的调用身份交给策略闸门。
func (f *FS) evaluateCall(ctx context.Context, operation sandbox.Operation, p string) (sandbox.Decision, error) {
	if strings.TrimSpace(p) == "" {
		return sandbox.Decision{}, fmt.Errorf("路径为空")
	}
	if f.gate == nil {
		return sandbox.Decision{}, fmt.Errorf("%w: 本 FS 没有 per-call 策略闸门（%s 未设置）",
			sandbox.ErrPolicyUnwired, sandbox.EnvMode)
	}
	abs, err := f.absoluteForPolicy(p)
	if err != nil {
		return sandbox.Decision{}, err
	}
	return f.gate.EvaluateFS(ctx, operation, filepath.ToSlash(abs), sandbox.PermissionFrom(ctx))
}

// absoluteForPolicy 把调用方给的路径变成绝对形式，**不做边界判定**：
// 判定属于策略层（它会 canonicalize 并分类）；这里只负责 ~ 通道与相对根。
func (f *FS) absoluteForPolicy(p string) (string, error) {
	root, _, _ := f.snapshot()
	slashed := strings.ReplaceAll(p, "\\", "/")
	if slashed == "~" || strings.HasPrefix(slashed, "~/") {
		home, err := f.resolveHome()
		if err != nil {
			return "", err
		}
		rest := strings.TrimLeft(strings.TrimPrefix(slashed, "~"), "/")
		return filepath.Join(filepath.FromSlash(home), filepath.FromSlash(rest)), nil
	}
	if filepath.IsAbs(filepath.FromSlash(slashed)) {
		return filepath.FromSlash(slashed), nil
	}
	return filepath.Join(filepath.FromSlash(root), filepath.FromSlash(slashed)), nil
}

// checkReadBeforeWrite 是读后写观测（与 CheckWritable 同一套账）。
func (f *FS) checkReadBeforeWrite(p, op, abs string) error {
	key := filepath.ToSlash(abs)
	st, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return nil // 新建文件不需要先读
	}
	if err != nil {
		return fmt.Errorf("检查 %s 失败: %w", p, err)
	}
	// 别名要在拿锁之前解析：Resolve 自己会取同一把锁（快照）。
	alias := ""
	if resolved, rerr := f.Resolve(p); rerr == nil {
		alias = resolved
	}
	f.mu.Lock()
	s, seen := f.seen[key]
	if !seen && alias != "" {
		// Read 用的是非 canonical 形式时也登记过别名；两个都认。
		s, seen = f.seen[alias]
	}
	f.mu.Unlock()
	if !seen {
		return fmt.Errorf(`cannot modify %q: file has not been read — read the file, then retry`, p)
	}
	if !s.mod.Equal(st.ModTime()) || s.size != st.Size() {
		verb := op
		if verb == "" {
			verb = "modify"
		}
		return fmt.Errorf(`cannot %s %q: file changed since it was read — re-read the file, then retry`, verb, p)
	}
	return nil
}
