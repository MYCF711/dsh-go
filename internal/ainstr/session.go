package ainstr

// LoadSessionEvents 是 durable 层给宿主的读取接口：按 seq 顺序给出事件类型与载荷。
//
// 为什么要一个接口而不是直接认 `codecs/v4.Log`：`internal/ainstr` 不 import `codecs/v4`
// 就能被宿主任何一层复用（官方包同样不认 session 文件格式，它认的是 `Session` 接口，
// `src/index.ts:17`）。宿主的 `codecs/v4.Event` 在 cmd/dshd 侧实现这个接口（见
// `v4EventsOf` 的适配器）。
type LoadSessionEvents interface {
	Types() []string
	Payloads() [][]byte
}

// FrameStateFromLog 从一个已实现 `LoadSessionEvents` 的日志重放出跨轮状态。
//
// 与 `FrameStateFrom` 的差别只是输入形状；重放规则完全一致（同一份函数）。
func FrameStateFromLog(log LoadSessionEvents) FrameState {
	types := log.Types()
	payloads := log.Payloads()
	events := make([]eventPair, 0, len(types))
	for i := range types {
		events = append(events, eventPair{Type: types[i], Data: payloads[i]})
	}
	return frameStateFrom(events)
}

// eventPair 是重放输入的一对（类型 + 载荷）。
type eventPair struct {
	Type string
	Data []byte
}

// frameStateFrom 是 `FrameStateFrom` 的真实实现（unexported，输入走内部形状）。
func frameStateFrom(events []eventPair) FrameState {
	state := FrameState{}
	var baselineSources []Source
	for _, event := range events {
		if event.Type != EventTypeUserMessage {
			continue
		}
		text, source, ok := FrameFromData(event.Data)
		if !ok {
			continue
		}
		state.Visible = append(state.Visible, source.Changes...)
		state.LastText = text
		last := source
		state.LastSource = &last
		if IsBaselineFrame(&source) {
			baselineSources = append(baselineSources, source)
		}
	}
	if view, ok := BaselineFromFrames(baselineSources); ok {
		state.Baseline = view
		state.BaselineOK = true
	}
	state.Visible = collapseChanges(state.Visible)
	return state
}
