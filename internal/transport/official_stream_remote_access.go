package transport

// 官方 `remoteAccess.follow`（mode:'stream'）的真实现。
//
// # 契约（只读镜像）
//
//	%TEMP%/official-api-src/dsh/node_modules/@deepseek-ai/dsh-api-remote-access/lib/typert.host.js
//	remoteAccess/follow  result: RemoteAccessFrame（union）
//	  {type:'baseline', owner:true}
//	  {type:'baseline', owner:false, grants:[{resource, actions}]}
//	  {type:'upsert',   grant:{resource, actions}}
//	  {type:'remove',   resource:{kind:'global'|'session'|'workspace', id?}}
//
//	官方实现（lib/index.js RemoteAccessService.follow）：
//	  - principal === undefined（owner）→ 只发 `{owner:true}`，然后一直挂到取消；
//	  - 非 owner → baseline(grants) 之后每次 invalidate/策略变化重算 grants：
//	    新资源或 actions 变化 → upsert；资源消失 → remove；
//	  - actions 描述"无调用参数即可允许的端点动作"，**排序**后返回。
//
// # 本宿主的数据源与诚实边界
//
// `OfficialRemoteAccessAPI.Snapshot(peerID)` 由宿主注入。**本宿主当前没有
// remote-access 策略表**：经认证的 HTTP 调用者与 mux Peer 都是 owner，所以
// cmd/dshd 的实现在 peer 存在时返回 `{owner:true}`——这正是官方的 owner 帧，
// 不是"单帧冒充"（owner 在官方语义里本来就只有这一帧，之后挂起）。
//
// 非 owner 的 grants 路径由同一台帧机真实实现：宿主可以注入按 peer 计算的
// 快照（测试用真实可变 grant 源验证 baseline→upsert→remove）。**在策略表落地前
// 不会伪造 grants**；`peerId` 从 context 传入（`WithOfficialRemoteAccessPeer`），
// HTTP 面没有 peer 身份时按 owner 处理（cookie 认证的就是 owner）。
//
// # 为什么不在本文件改共享分派
//
// 与 workspace.follow 一致：共享 server/stream 分派由 integrator 接线（WIRING），
// 本文件提供 `OpenOfficialRemoteAccessFollowStream` + HTTP 处理器。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// MethodRemoteAccessFollow 是官方 remoteAccess 族的流方法。
const MethodRemoteAccessFollow Method = "remoteAccess.follow"

// OfficialRemoteAccessResource 是官方 RemoteAccessResource。
type OfficialRemoteAccessResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

// OfficialRemoteAccessGrant 是官方 RemoteAccessGrant。
type OfficialRemoteAccessGrant struct {
	Resource OfficialRemoteAccessResource `json:"resource"`
	Actions  []string                     `json:"actions"`
}

// OfficialRemoteAccessSnapshot 是某个 peer 的当前访问快照。
type OfficialRemoteAccessSnapshot struct {
	Owner  bool
	Grants []OfficialRemoteAccessGrant
}

// OfficialRemoteAccessAPI 是宿主注入的 peer/grant 快照来源。
type OfficialRemoteAccessAPI struct {
	// Snapshot 按 peer 计算真实访问快照。peerID 为空 = 无法识别 peer（HTTP owner）。
	Snapshot func(peerID string) (OfficialRemoteAccessSnapshot, error)
}

var officialRemoteAccessMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialRemoteAccessAPI
}{m: map[*Server]OfficialRemoteAccessAPI{}}

// AttachOfficialRemoteAccessAPI 给 Server 挂上 peer/grant 快照来源。
func (s *Server) AttachOfficialRemoteAccessAPI(api OfficialRemoteAccessAPI) {
	if s == nil {
		return
	}
	officialRemoteAccessMount.mu.Lock()
	defer officialRemoteAccessMount.mu.Unlock()
	officialRemoteAccessMount.m[s] = api
}

// DetachOfficialRemoteAccessAPI 摘掉来源（测试收尾用）。
func (s *Server) DetachOfficialRemoteAccessAPI() {
	if s == nil {
		return
	}
	officialRemoteAccessMount.mu.Lock()
	defer officialRemoteAccessMount.mu.Unlock()
	delete(officialRemoteAccessMount.m, s)
}

func (s *Server) officialRemoteAccessAPI() (OfficialRemoteAccessAPI, bool) {
	if s == nil {
		return OfficialRemoteAccessAPI{}, false
	}
	officialRemoteAccessMount.mu.RLock()
	defer officialRemoteAccessMount.mu.RUnlock()
	api, ok := officialRemoteAccessMount.m[s]
	return api, ok
}

type officialRemoteAccessPeerKey struct{}

// WithOfficialRemoteAccessPeer 把 mux Peer id 放进 ctx。
//
// integrator 接线时在 `officialMuxStreamOpener.Open` 里带上真实 Peer id；
// 没有这一步时 remoteAccess.follow 按 owner 处理（本宿主所有 Peer 都是 owner，
// 所以不会因此伪造非 owner 的 grants）。
func WithOfficialRemoteAccessPeer(ctx context.Context, peerID string) context.Context {
	return context.WithValue(ctx, officialRemoteAccessPeerKey{}, peerID)
}

// OfficialRemoteAccessPeer 返回 ctx 里的 Peer id（没有则为空）。
func OfficialRemoteAccessPeer(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	peerID, _ := ctx.Value(officialRemoteAccessPeerKey{}).(string)
	return peerID
}

// ---------------------------------------------------------------------------
// 帧
// ---------------------------------------------------------------------------

func validateRemoteAccessSnapshot(snapshot OfficialRemoteAccessSnapshot) (OfficialRemoteAccessSnapshot, error) {
	if snapshot.Owner {
		// owner 帧只有 {owner:true}；携带 grants 说明来源把两种身份混了，拒掉
		// 比"忽略 grants 上线"更安全（后者会让客户端看到不该看到的东西）。
		if len(snapshot.Grants) > 0 {
			return OfficialRemoteAccessSnapshot{}, errRemoteAccessSnapshot(
				"owner 快照不能带 grants（官方 owner 帧只有 {owner:true}）")
		}
		return OfficialRemoteAccessSnapshot{Owner: true}, nil
	}
	normalized := make([]OfficialRemoteAccessGrant, 0, len(snapshot.Grants))
	seen := map[string]bool{}
	for _, grant := range snapshot.Grants {
		resource, err := normalizeRemoteAccessResource(grant.Resource)
		if err != nil {
			return OfficialRemoteAccessSnapshot{}, err
		}
		key := remoteAccessResourceKey(resource)
		if seen[key] {
			return OfficialRemoteAccessSnapshot{}, errRemoteAccessSnapshot("grants 里有重复资源：" + key)
		}
		seen[key] = true
		actions := sortedUniqueStrings(grant.Actions)
		if actions == nil {
			actions = []string{}
		}
		normalized = append(normalized, OfficialRemoteAccessGrant{Resource: resource, Actions: actions})
	}
	sort.Slice(normalized, func(i, j int) bool {
		return remoteAccessResourceKey(normalized[i].Resource) < remoteAccessResourceKey(normalized[j].Resource)
	})
	return OfficialRemoteAccessSnapshot{Grants: normalized}, nil
}

func normalizeRemoteAccessResource(resource OfficialRemoteAccessResource) (OfficialRemoteAccessResource, error) {
	switch resource.Kind {
	case "global":
		// 官方 global 分支只有 kind；多余 id 会变成"另一个资源"，按 void 处理。
		return OfficialRemoteAccessResource{Kind: "global"}, nil
	case "session", "workspace":
		if strings.TrimSpace(resource.ID) == "" {
			return OfficialRemoteAccessResource{}, errRemoteAccessSnapshot(
				"resource.kind=" + resource.Kind + " 需要非空 id")
		}
		return OfficialRemoteAccessResource{Kind: resource.Kind, ID: resource.ID}, nil
	default:
		return OfficialRemoteAccessResource{}, errRemoteAccessSnapshot(
			"resource.kind 只能是 global/session/workspace，收到 " + resource.Kind)
	}
}

func remoteAccessResourceKey(resource OfficialRemoteAccessResource) string {
	if resource.Kind == "global" {
		return "global"
	}
	return resource.Kind + "/" + resource.ID
}

func errRemoteAccessSnapshot(message string) error {
	return &remoteAccessSnapshotError{message: message}
}

type remoteAccessSnapshotError struct{ message string }

func (e *remoteAccessSnapshotError) Error() string { return e.message }

func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func marshalRemoteAccessFrame(frame map[string]any) (json.RawMessage, *OfficialStreamFault) {
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, officialStreamInternal(MethodRemoteAccessFollow, "序列化帧失败：%v", err)
	}
	return raw, nil
}

func remoteAccessBaselineFrame(snapshot OfficialRemoteAccessSnapshot) map[string]any {
	if snapshot.Owner {
		return map[string]any{"type": "baseline", "owner": true}
	}
	grants := snapshot.Grants
	if grants == nil {
		grants = []OfficialRemoteAccessGrant{}
	}
	return map[string]any{"type": "baseline", "owner": false, "grants": grants}
}

// ---------------------------------------------------------------------------
// 帧机
// ---------------------------------------------------------------------------

type officialRemoteAccessFollowStream struct {
	api    OfficialRemoteAccessAPI
	peerID string

	opened  bool
	owner   bool
	sent    []OfficialRemoteAccessGrant
	pending []json.RawMessage
}

var _ OfficialFrameStream = (*officialRemoteAccessFollowStream)(nil)

func (s *officialRemoteAccessFollowStream) Close() {}

func (s *officialRemoteAccessFollowStream) Next(ctx context.Context) (json.RawMessage, bool, *OfficialStreamFault) {
	for {
		if len(s.pending) > 0 {
			frame := s.pending[0]
			s.pending = s.pending[1:]
			return frame, false, nil
		}
		if s.opened && s.owner {
			// 官方 owner 只收一帧，然后挂到取消/服务卸载：这里**阻塞等 ctx**，
			// 不轮询（owner 没有增量可观察，轮询只会空转）。
			<-ctx.Done()
			return nil, true, nil
		}
		snapshot, err := s.api.Snapshot(s.peerID)
		if err != nil {
			return nil, false, s.faultOf(err)
		}
		normalized, err := validateRemoteAccessSnapshot(snapshot)
		if err != nil {
			return nil, false, officialStreamInternal(MethodRemoteAccessFollow, "%v", err)
		}
		if !s.opened {
			s.opened = true
			s.owner = normalized.Owner
			s.sent = normalized.Grants
			raw, fault := marshalRemoteAccessFrame(remoteAccessBaselineFrame(normalized))
			return raw, false, fault
		}
		if normalized.Owner != s.owner {
			// 同一代里 owner 身份翻转不可能真实发生；与其发一串语义不明的帧，
			// 不如让这条 generation 失败并由客户端重开（官方也是"重载后从新
			// baseline 开始"）。
			return nil, false, officialStreamInternal(MethodRemoteAccessFollow,
				"peer 的 owner 身份在流打开后发生变化（peer=%q）：拒绝在同一代里混用两种快照", s.peerID)
		}
		frames, fault := s.diff(normalized.Grants)
		if fault != nil {
			return nil, false, fault
		}
		if len(frames) > 0 {
			s.pending = frames[1:]
			return frames[0], false, nil
		}
		select {
		case <-ctx.Done():
			return nil, true, nil
		case <-time.After(officialStreamPollInterval):
		}
	}
}

// diff 生成非 owner 的增量帧（官方顺序：先 upsert，再 remove）。
func (s *officialRemoteAccessFollowStream) diff(next []OfficialRemoteAccessGrant) ([]json.RawMessage, *OfficialStreamFault) {
	previous := make(map[string]OfficialRemoteAccessGrant, len(s.sent))
	for _, grant := range s.sent {
		previous[remoteAccessResourceKey(grant.Resource)] = grant
	}
	nextByKey := make(map[string]OfficialRemoteAccessGrant, len(next))
	var frames []json.RawMessage
	for _, grant := range next {
		key := remoteAccessResourceKey(grant.Resource)
		nextByKey[key] = grant
		before, existed := previous[key]
		if existed && sameStringSlices(before.Actions, grant.Actions) {
			continue
		}
		raw, fault := marshalRemoteAccessFrame(map[string]any{"type": "upsert", "grant": grant})
		if fault != nil {
			return nil, fault
		}
		frames = append(frames, raw)
	}
	for _, grant := range s.sent {
		if _, ok := nextByKey[remoteAccessResourceKey(grant.Resource)]; ok {
			continue
		}
		raw, fault := marshalRemoteAccessFrame(map[string]any{"type": "remove", "resource": grant.Resource})
		if fault != nil {
			return nil, fault
		}
		frames = append(frames, raw)
	}
	s.sent = next
	return frames, nil
}

func (s *officialRemoteAccessFollowStream) faultOf(err error) *OfficialStreamFault {
	var snapshotErr *remoteAccessSnapshotError
	if errors.As(err, &snapshotErr) {
		return officialStreamBadRequest("", "%v", err)
	}
	return officialStreamFaultOf(MethodRemoteAccessFollow, err)
}

// ---------------------------------------------------------------------------
// 入口
// ---------------------------------------------------------------------------

// OpenOfficialRemoteAccessFollowStream 接通 peer 快照并预取 baseline。
func (s *Server) OpenOfficialRemoteAccessFollowStream(ctx context.Context) (OfficialFrameStream, *OfficialStreamFault) {
	api, ok := s.officialRemoteAccessAPI()
	if !ok || api.Snapshot == nil {
		return nil, officialStreamNotImplemented(MethodRemoteAccessFollow,
			"宿主没有 remoteAccess peer/grant 快照来源（OfficialRemoteAccessAPI.Snapshot 未接线）")
	}
	peerID := OfficialRemoteAccessPeer(ctx)
	snapshot, err := api.Snapshot(peerID)
	if err != nil {
		// 快照来源是宿主内部能力：它的错误是宿主问题（500），不是客户端参数错。
		return nil, officialStreamFaultOf(MethodRemoteAccessFollow, err)
	}
	normalized, err := validateRemoteAccessSnapshot(snapshot)
	if err != nil {
		return nil, officialStreamInternal(MethodRemoteAccessFollow, "%v", err)
	}
	stream := &officialRemoteAccessFollowStream{api: api, peerID: peerID}
	stream.opened = true
	stream.owner = normalized.Owner
	stream.sent = normalized.Grants
	head, fault := marshalRemoteAccessFrame(remoteAccessBaselineFrame(normalized))
	if fault != nil {
		return nil, fault
	}
	return &officialFollowHeadStream{stream: stream, head: head}, nil
}

// ServeOfficialRemoteAccessFollowHTTP 是 `POST /api/remoteAccess.follow` 的处理器。
//
// HTTP 面的调用者由 cookie 认证为 owner：没有 peer 身份时按 `{owner:true}` 作答
// （官方 owner 帧），不伪造 grants。
func (s *Server) ServeOfficialRemoteAccessFollowHTTP(w http.ResponseWriter, r *http.Request) {
	if fault := validateOfficialNoParamStreamBody(r); fault != nil {
		writeOfficialStreamFault(w, fault)
		return
	}
	stream, fault := s.OpenOfficialRemoteAccessFollowStream(r.Context())
	if fault != nil {
		writeOfficialStreamFault(w, fault)
		return
	}
	serveOfficialFollowNDJSON(w, r, stream)
}
