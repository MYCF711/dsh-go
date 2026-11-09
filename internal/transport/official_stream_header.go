package transport

// follow snapshot 的 header 来源可以**在 Server 构造之后**挂上来。
//
// # 为什么需要这个注册点
//
// 官方 follow 的首帧是 `{type:"snapshot", header, projections, cursor, records, hasMore}`，
// 其中 `header` 是 **durable 会话头**（v4 日志首帧：version/id/createdAt/isSeeded/cwd/…）。
// 它只能由宿主侧读盘拿到——transport 不 import 日志编解码包、也不碰文件系统（红线 R7）。
//
// 而宿主侧（`cmd/dshd` 的 `officialSessionSource`）并没有实现
// `OfficialSessionHeaderSource`。没有 header 来源时，帧机只能回
// `gateway/method-unavailable`：**语法上是诚实未实现，产品上是官方最常用的
// 会话流直接不可用**（`session.follow` 是客户端打开会话时最先要做的事）。
//
// 所以这里留一个显式注册点：宿主拿到 `*Server` 之后把它真正读盘的 header 来源挂上。
// transport 只定义接口与查找顺序，实现仍然在宿主那一侧。
//
// # 查找顺序
//
//	1. 显式注册的来源（AttachOfficialSessionHeaderSource）
//	2. host 自己实现的 OfficialSessionHeaderSource
//	3. 都没有 → nil → follow 继续回 501（**不编 header**：createdAt=0 会被
//	   客户端当成 1970 年，是假数据里最难查的一类）
//
// # 生命周期
//
// 注册表按 `*Server` 索引，与 `officialWorkspaceMount` 同形（A/B 两个 Server
// 各挂各的，进程内不会共享一份）。Server 目前没有统一的 Close 钩子可挂，
// 所以生产场景（一个进程一份挂载、进程退出即回收）不做自动摘除；
// 测试用 `DetachOfficialSessionHeaderSource` 显式清理。

import "sync"

// officialSessionHeaderMount 是 `*Server → header 来源` 的注册表。
var officialSessionHeaderMount = struct {
	mu      sync.RWMutex
	sources map[*Server]OfficialSessionHeaderSource
}{sources: map[*Server]OfficialSessionHeaderSource{}}

// AttachOfficialSessionHeaderSource 给这个 Server 挂上 follow 的 header 来源。
//
// 与 `AttachOfficialAgentAPI` 同形：按 *Server 索引，必须发生在开始接受
// /api 调用之前（装配阶段）。nil 的 Server 或 source 是空操作（挂载失败的宿主
// 继续走"没有 header 能力"的分支，而不是 panic 在一条官方请求上）。
func (s *Server) AttachOfficialSessionHeaderSource(source OfficialSessionHeaderSource) {
	if s == nil || source == nil {
		return
	}
	officialSessionHeaderMount.mu.Lock()
	officialSessionHeaderMount.sources[s] = source
	officialSessionHeaderMount.mu.Unlock()
}

// DetachOfficialSessionHeaderSource 摘掉注册（测试与重新挂载用）。
func (s *Server) DetachOfficialSessionHeaderSource() {
	if s == nil {
		return
	}
	officialSessionHeaderMount.mu.Lock()
	delete(officialSessionHeaderMount.sources, s)
	officialSessionHeaderMount.mu.Unlock()
}

// officialSessionHeaderSource 按"注册优先、host 其次"返回可用来源；nil = 没有。
func (s *Server) officialSessionHeaderSource() OfficialSessionHeaderSource {
	if s == nil {
		return nil
	}
	officialSessionHeaderMount.mu.RLock()
	attached := officialSessionHeaderMount.sources[s]
	officialSessionHeaderMount.mu.RUnlock()
	if attached != nil {
		return attached
	}
	// host 自己实现了就直接用（接口断言在 nil 上安全）。
	if source, ok := s.officialHost().(OfficialSessionHeaderSource); ok {
		return source
	}
	return nil
}
