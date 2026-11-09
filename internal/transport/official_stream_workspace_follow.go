package transport

// 官方 `workspace.follow`（mode:'stream'）的真实现。
//
// # 契约（只读镜像）
//
//	%TEMP%/official-api-src/dsh/node_modules/@deepseek-ai/dsh-api-workspace-controller/lib/typert.host.js
//	workspace/follow  result: WorkspaceFollowFrame（union）
//	  {type:'baseline', value: WorkspaceBaseline}
//	  {type:'replace',  value: WorkspaceBaseline}          ← 非 owner 过滤用，本宿主 owner-only
//	  {type:'upsert',   workspace: WorkspaceView}
//	  {type:'remove',   workspaceId}
//	  {type:'order',    workspaceIds}
//	  {type:'archived', archivedSessionIds}
//	  {type:'pinned',   pinnedSessionIds}
//	WorkspaceView = {workspaceId,path,title,sessionIds,createdAt,updatedAt}
//
// # 数据源
//
// `OfficialWorkspaceFollowSource`：宿主把它接在**同一份**真实 registry 上
// （cmd/dshd 的 `<home>/state/workspaces.json`，见 serve_workspace_follow.go），
// 没有第二个账本。帧机每 `officialStreamPollInterval` 读一次快照并做差异：
//
//	首帧                  → baseline（完整 items + archived + pinned）
//	新增/字段变化          → upsert
//	消失                  → remove
//	顺序变化               → order
//	归档/pin 集变化         → archived / pinned
//
// 官方实现是"事件驱动 + 已发布队列"（index.js WorkspaceFollower）；本宿主没有
// workspace 变更事件总线，所以用**快照差异**得到同一组帧。差异帧的组合顺序
// 固定为 remove → upsert → order → archived → pinned：一次快照里同时出现多类
// 变化时结果确定，不依赖 map 遍历顺序。这是显式差异，登记在 RESULT。
//
// # 为什么不在本文件改共享分派
//
// server.go / official_stream_face.go 的分派由 integrator 串行接线（WIRING）：
// 本文件只提供 `OpenOfficialWorkspaceFollowStream` + `ServeOfficialWorkspaceFollowHTTP`。
// 两条面共用同一台帧机，不存在 "HTTP 对了 mux 错了" 的分叉。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// MethodWorkspaceFollow 是官方 workspace 族的流方法。
const MethodWorkspaceFollow Method = "workspace.follow"

// OfficialWorkspaceFollowSnapshot 是一次 registry 快照。
//
// Items 必须按 registry 的 Order 排好（顺序真相只有一份）；Archived/Pinned
// 是 registry 的全局集合。切片 nil 会在编码前归一成 []，不会上线成 null。
type OfficialWorkspaceFollowSnapshot struct {
	Items              []OfficialWorkspace
	ArchivedSessionIDs []string
	PinnedSessionIDs   []string
}

// OfficialWorkspaceFollowSource 是 workspace.follow 的真实数据源。
type OfficialWorkspaceFollowSource interface {
	WorkspaceFollowSnapshot() (OfficialWorkspaceFollowSnapshot, error)
}

var officialWorkspaceFollowMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialWorkspaceFollowSource
}{m: map[*Server]OfficialWorkspaceFollowSource{}}

// AttachOfficialWorkspaceFollowSource 把 registry 快照源挂到 Server 上。
func (s *Server) AttachOfficialWorkspaceFollowSource(src OfficialWorkspaceFollowSource) {
	if s == nil {
		return
	}
	officialWorkspaceFollowMount.mu.Lock()
	defer officialWorkspaceFollowMount.mu.Unlock()
	officialWorkspaceFollowMount.m[s] = src
}

// DetachOfficialWorkspaceFollowSource 摘掉来源（测试收尾用）。
func (s *Server) DetachOfficialWorkspaceFollowSource() {
	if s == nil {
		return
	}
	officialWorkspaceFollowMount.mu.Lock()
	defer officialWorkspaceFollowMount.mu.Unlock()
	delete(officialWorkspaceFollowMount.m, s)
}

func (s *Server) officialWorkspaceFollowSource() (OfficialWorkspaceFollowSource, bool) {
	if s == nil {
		return nil, false
	}
	officialWorkspaceFollowMount.mu.RLock()
	defer officialWorkspaceFollowMount.mu.RUnlock()
	src, ok := officialWorkspaceFollowMount.m[s]
	return src, ok
}

// ---------------------------------------------------------------------------
// 帧
// ---------------------------------------------------------------------------

type officialWorkspaceFollowBaseline struct {
	Items              []OfficialWorkspace `json:"items"`
	ArchivedSessionIDs []string            `json:"archivedSessionIds"`
	PinnedSessionIDs   []string            `json:"pinnedSessionIds"`
}

func (s OfficialWorkspaceFollowSnapshot) baseline() officialWorkspaceFollowBaseline {
	items := s.Items
	if items == nil {
		items = []OfficialWorkspace{}
	}
	for i := range items {
		if items[i].SessionIDs == nil {
			items[i].SessionIDs = []string{}
		}
	}
	return officialWorkspaceFollowBaseline{
		Items:              items,
		ArchivedSessionIDs: nonNilStrings(s.ArchivedSessionIDs),
		PinnedSessionIDs:   nonNilStrings(s.PinnedSessionIDs),
	}
}

func marshalWorkspaceFollowFrame(frame map[string]any) (json.RawMessage, *OfficialStreamFault) {
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, officialStreamInternal(MethodWorkspaceFollow, "序列化帧失败：%v", err)
	}
	return raw, nil
}

// ---------------------------------------------------------------------------
// 帧机
// ---------------------------------------------------------------------------

// officialWorkspaceFollowStream 是 workspace.follow 的快照差异帧机。
type officialWorkspaceFollowStream struct {
	source OfficialWorkspaceFollowSource

	opened   bool
	known    map[string]OfficialWorkspace
	order    []string
	archived []string
	pinned   []string

	pending []json.RawMessage
}

var _ OfficialFrameStream = (*officialWorkspaceFollowStream)(nil)

func (s *officialWorkspaceFollowStream) Close() {}

func (s *officialWorkspaceFollowStream) Next(ctx context.Context) (json.RawMessage, bool, *OfficialStreamFault) {
	for {
		if len(s.pending) > 0 {
			frame := s.pending[0]
			s.pending = s.pending[1:]
			return frame, false, nil
		}
		snapshot, err := s.source.WorkspaceFollowSnapshot()
		if err != nil {
			return nil, false, officialStreamFaultOf(MethodWorkspaceFollow, err)
		}
		if !s.opened {
			s.opened = true
			s.record(snapshot)
			raw, fault := marshalWorkspaceFollowFrame(map[string]any{
				"type": "baseline", "value": snapshot.baseline(),
			})
			return raw, false, fault
		}
		frames, fault := s.diff(snapshot)
		if fault != nil {
			return nil, false, fault
		}
		s.record(snapshot)
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

// record 更新基线状态（只在真正交付了帧之后调用）。
func (s *officialWorkspaceFollowStream) record(snapshot OfficialWorkspaceFollowSnapshot) {
	s.known = make(map[string]OfficialWorkspace, len(snapshot.Items))
	s.order = s.order[:0]
	for _, item := range snapshot.Items {
		s.known[item.WorkspaceID] = item
		s.order = append(s.order, item.WorkspaceID)
	}
	s.archived = append([]string(nil), snapshot.ArchivedSessionIDs...)
	s.pinned = append([]string(nil), snapshot.PinnedSessionIDs...)
}

// diff 生成一次快照差异对应的帧（顺序固定，见文件头）。
func (s *officialWorkspaceFollowStream) diff(snapshot OfficialWorkspaceFollowSnapshot) ([]json.RawMessage, *OfficialStreamFault) {
	nextIDs := make([]string, 0, len(snapshot.Items))
	next := make(map[string]OfficialWorkspace, len(snapshot.Items))
	for _, item := range snapshot.Items {
		nextIDs = append(nextIDs, item.WorkspaceID)
		next[item.WorkspaceID] = item
	}

	var frames []json.RawMessage

	// 1) remove：上一轮有、这一轮没有的（按上一轮 order 输出，稳定）。
	for _, id := range s.order {
		if _, ok := next[id]; ok {
			continue
		}
		raw, fault := marshalWorkspaceFollowFrame(map[string]any{"type": "remove", "workspaceId": id})
		if fault != nil {
			return nil, fault
		}
		frames = append(frames, raw)
	}

	// 2) upsert：新增或字段变化的（按新 order 输出，与 registry 顺序一致）。
	for _, item := range snapshot.Items {
		before, existed := s.known[item.WorkspaceID]
		if existed && sameWorkspaceView(before, item) {
			continue
		}
		view := item
		if view.SessionIDs == nil {
			view.SessionIDs = []string{}
		}
		raw, fault := marshalWorkspaceFollowFrame(map[string]any{"type": "upsert", "workspace": view})
		if fault != nil {
			return nil, fault
		}
		frames = append(frames, raw)
	}

	// 3) order：Order 是唯一顺序真相，变了就整份下发。
	if !sameStringSlices(s.order, nextIDs) {
		raw, fault := marshalWorkspaceFollowFrame(map[string]any{"type": "order", "workspaceIds": nextIDs})
		if fault != nil {
			return nil, fault
		}
		frames = append(frames, raw)
	}

	// 4) archived / pinned：全局集合变化整份下发。
	if !sameStringSlices(s.archived, snapshot.ArchivedSessionIDs) {
		raw, fault := marshalWorkspaceFollowFrame(map[string]any{
			"type": "archived", "archivedSessionIds": nonNilStrings(snapshot.ArchivedSessionIDs),
		})
		if fault != nil {
			return nil, fault
		}
		frames = append(frames, raw)
	}
	if !sameStringSlices(s.pinned, snapshot.PinnedSessionIDs) {
		raw, fault := marshalWorkspaceFollowFrame(map[string]any{
			"type": "pinned", "pinnedSessionIds": nonNilStrings(snapshot.PinnedSessionIDs),
		})
		if fault != nil {
			return nil, fault
		}
		frames = append(frames, raw)
	}
	return frames, nil
}

func sameWorkspaceView(a, b OfficialWorkspace) bool {
	if a.WorkspaceID != b.WorkspaceID || a.Path != b.Path || a.Title != b.Title ||
		a.CreatedAt != b.CreatedAt || a.UpdatedAt != b.UpdatedAt {
		return false
	}
	return sameStringSlices(a.SessionIDs, b.SessionIDs)
}

func sameStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 入口：HTTP + 预取（mux 复用同一个 stream 工厂）
// ---------------------------------------------------------------------------

// OpenOfficialWorkspaceFollowStream 接通真实 registry 并预取 baseline。
//
// 预取的用途与其它流一致：能力错误必须在写响应头之前暴露。
func (s *Server) OpenOfficialWorkspaceFollowStream() (OfficialFrameStream, *OfficialStreamFault) {
	source, ok := s.officialWorkspaceFollowSource()
	if !ok || source == nil {
		return nil, officialStreamNotImplemented(MethodWorkspaceFollow,
			"宿主没有工作区注册表快照源（OfficialWorkspaceFollowSource 未接线）")
	}
	snapshot, err := source.WorkspaceFollowSnapshot()
	if err != nil {
		return nil, officialStreamFaultOf(MethodWorkspaceFollow, err)
	}
	stream := &officialWorkspaceFollowStream{source: source}
	stream.opened = true
	stream.record(snapshot)
	head, fault := marshalWorkspaceFollowFrame(map[string]any{
		"type": "baseline", "value": snapshot.baseline(),
	})
	if fault != nil {
		return nil, fault
	}
	return &officialFollowHeadStream{stream: stream, head: head}, nil
}

// officialFollowHeadStream 是"首帧已预取"的包装（shared 的 officialHeadStream
// 绑定在 Server.OpenOfficialFrameStream 上；这里只借用同一形状）。
type officialFollowHeadStream struct {
	stream OfficialFrameStream
	head   json.RawMessage
}

func (h *officialFollowHeadStream) Next(ctx context.Context) (json.RawMessage, bool, *OfficialStreamFault) {
	if h.head != nil {
		frame := h.head
		h.head = nil
		return frame, false, nil
	}
	return h.stream.Next(ctx)
}

func (h *officialFollowHeadStream) Close() { h.stream.Close() }

// ServeOfficialWorkspaceFollowHTTP 是 `POST /api/workspace.follow` 的处理器。
//
// 官方 parameters 是空列表：只接受空请求体或 `{}`，任何字段都是 400
// （与 initializeDefault 的"参数表为空"判据一致）。
func (s *Server) ServeOfficialWorkspaceFollowHTTP(w http.ResponseWriter, r *http.Request) {
	if fault := validateOfficialNoParamStreamBody(r); fault != nil {
		writeOfficialStreamFault(w, fault)
		return
	}
	stream, fault := s.OpenOfficialWorkspaceFollowStream()
	if fault != nil {
		writeOfficialStreamFault(w, fault)
		return
	}
	serveOfficialFollowNDJSON(w, r, stream)
}

// validateOfficialNoParamStreamBody 校验"官方 parameters: []"的方法体：
// 空 body 与 `{}` 等价；非对象 / 含字段 / null 都是 400。
func validateOfficialNoParamStreamBody(r *http.Request) *OfficialStreamFault {
	raw, err := io.ReadAll(io.LimitReader(r.Body, officialStreamBodyLimit))
	if err != nil {
		return officialStreamBadRequest("", "读请求体失败：%v", err)
	}
	trimmed := string(bytes.TrimSpace(raw))
	if trimmed == "" {
		return nil
	}
	if trimmed == "{}" {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return officialStreamBadRequest("", "这个流方法的参数为空；请求体只能是 {} 或省略")
	}
	if len(object) > 0 {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return officialStreamBadRequest(keys[0], "这个流方法不接受参数；不认识的字段：%s", keys[0])
	}
	return nil
}

// serveOfficialFollowNDJSON 把一台已打开的帧机写成 NDJSON（一行一帧）。
//
// 与 shared 的 serveOfficialStream 同一形状：成功头 200 + x-ndjson；
// 中途故障写一行 `{"type":"error",…}` 四字段后关闭。
func serveOfficialFollowNDJSON(w http.ResponseWriter, r *http.Request, stream OfficialFrameStream) {
	defer stream.Close()
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	writeLine := func(frame json.RawMessage) bool {
		if len(frame) == 0 {
			return true
		}
		if _, err := w.Write(append(frame, '\n')); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	for {
		frame, done, fault := stream.Next(r.Context())
		if fault != nil {
			body := errorBodyFor(faultCodeForEnvelope(fault), fault.Message)
			body["type"] = "error"
			if fault.Field != "" {
				body["field"] = fault.Field
			}
			writeLine(mustMarshalStreamLine(body))
			return
		}
		if done {
			return
		}
		if !writeLine(frame) {
			return
		}
	}
}
