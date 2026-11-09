package transport

// session/control：官方 Host-wide 控制流。
//
// 官方证据（0.2.2-rc.1）：
//   - dsh-api-session-controller/lib/typert.host.js:1970-1985：零参数、mode:'stream'。
//   - lib/types/control.js:30-64：首帧 baseline，随后只发 projection。
//   - typert.host.js:3587-3592/3695-3696/3735-3736：逐字段 schema。
//
// 这条流没有 ready/cursor/records/hasMore，也没有 sinceSeq 参数。重连语义就是
// 重新读取一份完整 baseline，再跟随新事件；单个 sinceSeq 无法表示多个会话各自
// 独立的 durable seq 序域，因此不把 follow 的游标字段错塞进 control。
//
// 数据只来自 Server 的真实 List、OfficialSessionHost 的 durable v4 事件与 Load
// 投影；等待复用每会话现有 Journal.Wait。这里不 Append、不 Reset、不创建第二条
// 事件日志，也不生成空 ready 或周期性假 projection。

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
)

const methodSessionControl Method = "session.control"

// officialControlBaselineFrame 是官方首帧：
// {type:'baseline', value:{projections:{[sessionId]:{asOfSeq,values}}}}。
type officialControlBaselineFrame struct {
	Type  string                  `json:"type"`
	Value officialControlBaseline `json:"value"`
}

type officialControlBaseline struct {
	Projections map[string]OfficialProjections `json:"projections"`
}

// officialControlProjectionFrame 是官方后续帧；seq 属于 sessionId 指定的会话。
type officialControlProjectionFrame struct {
	Type      string                     `json:"type"`
	SessionID string                     `json:"sessionId"`
	Seq       int64                      `json:"seq"`
	Changes   []officialProjectionChange `json:"changes"`
}

type officialControlSession struct {
	cursor int64
	fold   *officialProjectionFold
}

// officialControlStream 是 Host-wide baseline → projection 帧机。
type officialControlStream struct {
	server *Server
	source officialStreamSource

	sessions map[string]*officialControlSession
	opened   bool
}

var _ OfficialFrameStream = (*officialControlStream)(nil)

func (s *officialControlStream) Close() {}

func (s *officialControlStream) Next(ctx context.Context) (json.RawMessage, bool, *OfficialStreamFault) {
	if !s.opened {
		return s.open()
	}
	for {
		frame, more, fault := s.step()
		if fault != nil {
			return nil, false, fault
		}
		if more {
			return frame, false, nil
		}

		// 先注册现有每会话 journal 的等待，再复查一次：注册与读之间提交的事件
		// 不会丢。新增会话由 100ms 兜底发现，与既有会话流使用同一档间隔。
		waiting := s.waitCases(ctx)
		frame, more, fault = s.step()
		if fault != nil {
			stopControlTimer(waiting)
			return nil, false, fault
		}
		if more {
			stopControlTimer(waiting)
			return frame, false, nil
		}
		chosen, _, _ := reflect.Select(waiting.cases)
		stopControlTimer(waiting)
		if chosen == 0 { // ctx.Done()
			return nil, true, nil
		}
	}
}

// controlWaitSet 是 reflect.Select 需要的等待集合；index 0 固定为 ctx.Done，
// index 1 固定为兜底 timer，其余为各会话现有 journal 的 Wait channel。
type controlWaitSet struct {
	cases []reflect.SelectCase
	timer *time.Timer
}

func (s *officialControlStream) waitCases(ctx context.Context) controlWaitSet {
	set := controlWaitSet{cases: make([]reflect.SelectCase, 0, len(s.sessions)+2)}
	set.cases = append(set.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())})
	set.timer = time.NewTimer(officialStreamPollInterval)
	set.cases = append(set.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(set.timer.C)})
	for _, id := range s.sortedSessionIDs() {
		wait, cleanup := s.source.wait(id)
		if cleanup != nil {
			// 当前生产 wait 返回 no-op；保留调用点以兼容其它 source 实现。
			_ = cleanup
		}
		if wait != nil {
			set.cases = append(set.cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(wait)})
		}
	}
	return set
}

func stopControlTimer(set controlWaitSet) {
	if set.timer != nil && !set.timer.Stop() {
		select {
		case <-set.timer.C:
		default:
		}
	}
}

// open 读一份完整的真实 baseline。清单能力缺失时明确 501；只有真实 List
// 返回空集时才给 projections:{}，不把“不知道有哪些会话”伪装成空宿主。
func (s *officialControlStream) open() (json.RawMessage, bool, *OfficialStreamFault) {
	if s.server == nil || s.server.opt.List == nil {
		return nil, false, officialStreamNotImplemented(methodSessionControl,
			"宿主没有真实会话清单（Options.List 未接线），不能生成空 baseline")
	}
	summaries, err := s.server.opt.List()
	if err != nil {
		return nil, false, officialStreamFaultOf(methodSessionControl, err)
	}
	s.sessions = make(map[string]*officialControlSession, len(summaries))
	baseline := officialControlBaseline{Projections: make(map[string]OfficialProjections, len(summaries))}
	for _, summary := range summaries {
		id := strings.TrimSpace(summary.ID)
		if id == "" {
			return nil, false, officialStreamInternal(methodSessionControl, "真实会话清单含空 sessionId")
		}
		if _, duplicate := s.sessions[id]; duplicate {
			return nil, false, officialStreamInternal(methodSessionControl, "真实会话清单含重复 sessionId：%s", id)
		}
		events, logEnd, err := s.source.events(id)
		if err != nil {
			return nil, false, officialStreamFaultOf(methodSessionControl, err)
		}
		// baseline 与 HTTP session.projections 同源（T04 WIRING §3）：
		// 有宿主就复用 officialProjectionValues，没有才退回事件折叠。
		projections, projFault := s.server.officialStreamProjectionValues(id, events, logEnd)
		if projFault != nil {
			return nil, false, projFault
		}
		fold := newOfficialProjectionFoldFor(id)
		for _, event := range events {
			fold.Apply(event)
		}
		s.sessions[id] = &officialControlSession{cursor: logEnd, fold: fold}
		baseline.Projections[id] = projections
	}
	s.opened = true
	raw, fault := marshalStreamFrame(methodSessionControl, officialControlBaselineFrame{Type: "baseline", Value: baseline})
	return raw, false, fault
}

// syncSessions 发现 baseline 之后新增/删除的真实会话。新会话从它被发现时的
// logEnd 开始跟随，旧事件只用于初始化 fold，绝不在控制流里重放历史 projection。
func (s *officialControlStream) syncSessions() *OfficialStreamFault {
	if s.server == nil || s.server.opt.List == nil {
		return officialStreamNotImplemented(methodSessionControl, "真实会话清单在流存续期间消失")
	}
	summaries, err := s.server.opt.List()
	if err != nil {
		return officialStreamFaultOf(methodSessionControl, err)
	}
	current := make(map[string]struct{}, len(summaries))
	for _, summary := range summaries {
		id := strings.TrimSpace(summary.ID)
		if id == "" {
			return officialStreamInternal(methodSessionControl, "真实会话清单含空 sessionId")
		}
		if _, duplicate := current[id]; duplicate {
			return officialStreamInternal(methodSessionControl, "真实会话清单含重复 sessionId：%s", id)
		}
		current[id] = struct{}{}
		if _, exists := s.sessions[id]; exists {
			continue
		}
		events, logEnd, err := s.source.events(id)
		if err != nil {
			return officialStreamFaultOf(methodSessionControl, err)
		}
		fold := newOfficialProjectionFoldFor(id)
		for _, event := range events {
			fold.Apply(event)
		}
		s.sessions[id] = &officialControlSession{cursor: logEnd, fold: fold}
	}
	for id := range s.sessions {
		if _, exists := current[id]; !exists {
			delete(s.sessions, id)
		}
	}
	return nil
}

func (s *officialControlStream) step() (json.RawMessage, bool, *OfficialStreamFault) {
	if fault := s.syncSessions(); fault != nil {
		return nil, false, fault
	}
	for _, id := range s.sortedSessionIDs() {
		state := s.sessions[id]
		events, _, err := s.source.eventsSince(id, state.cursor)
		if err != nil {
			return nil, false, officialStreamFaultOf(methodSessionControl, err)
		}
		if len(events) == 0 {
			continue
		}
		event := events[0]
		state.cursor = event.Seq
		frame := officialControlProjectionFrame{
			Type: "projection", SessionID: id, Seq: event.Seq, Changes: state.fold.Apply(event),
		}
		raw, fault := marshalStreamFrame(methodSessionControl, frame)
		if fault != nil {
			return nil, false, fault
		}
		return raw, true, nil
	}
	return nil, false, nil
}

func (s *officialControlStream) sortedSessionIDs() []string {
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
