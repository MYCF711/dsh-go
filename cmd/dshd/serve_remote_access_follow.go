package main

// remoteAccess.follow 的宿主快照来源。
//
// # 本宿主的真实访问模型
//
// 本仓是**单 owner 本地宿主**：HTTP `/api` 由 cookie 认证为 owner，mux 的每个
// Peer 也都由同一份 `/connect` cookie 认证。仓库里**没有** remote-access 策略表
// （没有 principal→policy→bindings 的注册表），所以每个经认证的调用者都是
// owner。官方对 owner 的 follow 语义就是**一帧** `{type:'baseline',owner:true}`
// 然后挂到取消——这是真值，不是"单帧冒充"。
//
// 非 owner 的 grants 路径由 transport 的同一台帧机实现；宿主将来接入策略表后，
// 只需在这里换成按 peer 计算的真实快照（`OfficialRemoteAccessAPI.Snapshot`），
// 不需要改协议层。**在策略表落地前不伪造 grants**。

import (
	"dshgo/internal/transport"
)

// attachOfficialRemoteAccessFollow 挂上当前宿主的真实访问快照（owner-only）。
func attachOfficialRemoteAccessFollow(srv *transport.Server) {
	if srv == nil {
		return
	}
	srv.AttachOfficialRemoteAccessAPI(transport.OfficialRemoteAccessAPI{
		Snapshot: func(peerID string) (transport.OfficialRemoteAccessSnapshot, error) {
			// peerID 来自 mux 身份（WIRING 会把真实 Peer id 放进 ctx）；HTTP 面为空。
			// 两种都是经认证的本地调用者 → owner。这里不按未知 peer 回错误：
			// peer 是否存活由 mux 认证层判定，本方法只回答"它能看什么"。
			_ = peerID
			return transport.OfficialRemoteAccessSnapshot{Owner: true}, nil
		},
	})
}
