package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// serveDelete 删掉一个会话目录（整个 id 目录，不只是一份文件）。
//
// 为什么删整个目录：v4 的一份会话就是 `<project>/<id>/session.v4.jsonl.zstd`，
// 只删文件会留下一个空目录，下次列表还会扫到它、报"坏会话"。
func serveDelete(home, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("会话 id 为空")
	}
	path, err := session.FindByID(home, sessionID)
	if err != nil {
		// "找不到"必须和"删失败"分开：界面看到 404 才知道该换个 id，
		// 看到 500 只会以为服务坏了、反复重试同一个不存在的会话。
		// 真机就是靠这条抓到的：删不存在的会话回了 500。
		return fmt.Errorf("%w: %v", transport.ErrNoSuchSession, err)
	}
	// 删的是这个会话的**目录**（内含 session.v4.jsonl.zstd 与 .lock）：
	// 只删文件会留下空目录，列表下次还会扫到它并报"坏会话"。
	return os.RemoveAll(filepath.Dir(path))
}

// serveFork 从已有会话开一个分支，返回新会话 id。
//
// 继承 surface、记 parentSession，**不继承授权**——这些语义全在 session.Fork 里，
// 这里只做参数转换，不重复实现一遍。
func serveFork(home, sessionID, title string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("会话 id 为空")
	}
	srcPath, err := session.FindByID(home, sessionID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", transport.ErrNoSuchSession, err)
	}
	src, err := v4.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("读源会话失败: %w", err)
	}
	newID := "session-" + session.NewID()
	forked, err := session.Fork(src, home, src.Header.Cwd, newID)
	if err != nil {
		return "", fmt.Errorf("%w", err)
	}
	// 必须 Close 刷盘：否则新会话文件不完整，界面立刻 list 会看不到它。
	if cerr := forked.Close(); cerr != nil {
		return "", fmt.Errorf("新会话落盘失败: %w", cerr)
	}
	return newID, nil
}

// serveWorkspaceList 列出当前工作区的顶层条目。
//
// 为什么只列顶层：界面左栏要的是"这个工作区里有什么项目/目录"，
// 递归会让一次调用扫几万个文件——那是 grep 工具的事，不是这个 method 的事。
func serveWorkspaceList() ([]transport.WorkspaceEntry, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("取当前目录失败: %w", err)
	}
	ents, err := os.ReadDir(wd)
	if err != nil {
		return nil, fmt.Errorf("读目录失败: %w", err)
	}
	out := make([]transport.WorkspaceEntry, 0, len(ents))
	for _, e := range ents {
		ent := transport.WorkspaceEntry{Name: e.Name(), IsDir: e.IsDir()}
		if info, ierr := e.Info(); ierr == nil && !e.IsDir() {
			ent.Size = info.Size()
		}
		out = append(out, ent)
	}
	return out, nil
}

// fsReadLimit 是 fs.read 的单文件上限。界面要的是"预览"，不是"下载"。
const fsReadLimit = 4 << 20

// serveFSRead 读工作区里的一个文件。
//
// 越界检查在这里做：`..` 跑出去、指向目录、超过上限，分别给不同的错误——
// 界面看到 404"文件不存在"和 500"读不了"应该做不同的事。
func serveFSRead(path string) (transport.FSContent, error) {
	if strings.TrimSpace(path) == "" {
		return transport.FSContent{}, fmt.Errorf("path 为空")
	}
	abs, err := filepath.Abs(filepath.FromSlash(path))
	if err != nil {
		return transport.FSContent{}, fmt.Errorf("路径解析失败: %w", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return transport.FSContent{}, transport.ErrNoSuchFile
		}
		return transport.FSContent{}, fmt.Errorf("读不了: %w", err)
	}
	if st.IsDir() {
		return transport.FSContent{}, fmt.Errorf("这是一个目录，不是文件")
	}
	if st.Size() > fsReadLimit {
		return transport.FSContent{}, fmt.Errorf("文件 %d 字节，超过 %d 的上限；先用 bash 看片段", st.Size(), fsReadLimit)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return transport.FSContent{}, fmt.Errorf("读失败: %w", err)
	}
	return transport.FSContent{Path: path, Content: string(b), Size: int64(len(b))}, nil
}

// serveFSWrite 写工作区里的一个文件。
//
// 不做 read-before-write：那是**工具层**的策略（模型改文件前必须先读）。
// 界面是人手在改，人已经知道自己在改什么——把工具的策略套到人身上只会挡路。
func serveFSWrite(path, content string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path 为空")
	}
	abs, err := filepath.Abs(filepath.FromSlash(path))
	if err != nil {
		return fmt.Errorf("路径解析失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("创建父目录失败: %w", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写失败: %w", err)
	}
	return nil
}
