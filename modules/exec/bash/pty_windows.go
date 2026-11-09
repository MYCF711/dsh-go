//go:build windows

package bash

import (
	"fmt"

	"dshgo/internal/spi"
	"dshgo/internal/terminal"
)

// ptyProcessTree 把 PTY 会话包成 processTreeNative：
//
//	terminate  杀整棵 Job Object（父+子+孙）
//	release    关伪控制台与管道句柄
//
// 与 windowsJobTree 的分工：那个从 *exec.Cmd 的 PID 重新 OpenProcess 拿句柄，
// 这个的句柄在 CreateProcess 时就已持有（PTY 必须自己起进程），归属校验仍然做。
type ptyProcessTree struct {
	sess *terminal.Session
}

func (t *ptyProcessTree) terminate() error { return t.sess.Kill() }

func (t *ptyProcessTree) release() error { return t.sess.Close() }

func (t *ptyProcessTree) terminateOnClose(bool) bool { return true }

// attachPTYProcessTree 用**真实进程元数据**建立归属，再交给 processControl。
//
// 归属校验与管道式会话同一套：Active + 映像 + 创建代次三者都要对上，
// 这样 Close/Stop 时不会误杀一个复用了 PID 的无关进程。
func attachPTYProcessTree(sess *terminal.Session) (*processControl, error) {
	if sess == nil || sess.PID() <= 0 {
		return nil, fmt.Errorf("%w: PTY 会话没有子进程 PID", spi.ErrProcessTreeUnsafe)
	}
	identity, err := queryProcessIdentity(sess.PID())
	if err != nil {
		return nil, fmt.Errorf("核对 PTY 进程归属失败: %w", err)
	}
	if !identity.Active || identity.Image == "" || identity.CreationID == "" {
		return nil, fmt.Errorf("%w: 新 PTY 元数据不完整: %+v", spi.ErrProcessTreeUnsafe, identity)
	}
	identity.ProcessTree = "windows-conpty-job-object"
	return &processControl{identity: identity, native: &ptyProcessTree{sess: sess}}, nil
}
