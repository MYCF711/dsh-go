package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"dshgo/internal/session"
)

// 官方 `plan` 投影必须出现在 `session.projections` 的 values 里
// （`@deepseek-ai/dsh-plan-mode/src/types.ts:21-24` 的 wire schema）。
//
// 为什么这条测试在 transport 层：折叠本身由 `internal/session` 的单测覆盖，
// 这里钉的是**接线**——折得再对，只要没被 `officialProjectionValues` 写进
// values，客户端拿到的仍然是"没装 plan-mode 能力"。两个消费方都只认这个键：
//
//   - `dsh-client-ui-conversation/src/client/input/hub.ts:108-119`：`runMode`
//     （进了计划模式的会话，输入框必须显示成计划模式）；
//   - `dsh-client-ui-plan/src/client/PlanModeControl.tsx:33`：
//     `target = pending ? !active : active`。
//
// 这条测试走的是**真实 HTTP 请求 → 真实响应体 → 真实 values**，不是"函数被调了"。
func TestOfficialPlanProjectionWired(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	// 事件的 data 用宿主**真实写侧**的构造器生成（`session.ToolCallEvent` /
	// `session.ToolResultEvent`），不是手写的 JSON——这样这条测试证明的是"宿主
	// 真写出来的日志能被折出正确的 plan"，而不是"我编的 JSON 能被我自己的解析器
	// 读懂"。两个构造器就是 durable 写的唯一形状（cmd/dshd 写 tool/call、
	// tool/result 时用的就是它们，见 internal/session/log.go:168,190）。
	mustJSON := func(value map[string]any) json.RawMessage {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("夹具序列化失败：%v", err)
		}
		return encoded
	}
	// 一段真实的宿主日志：模型进了计划模式（enter_plan_mode 落地），
	// 然后再提交方案（exit_plan_mode 的 tool/call 已写、result 还没写）。
	src := &fakeOfficialSource{logEnd: 4, events: []DurableEvent{
		{Type: "turn/start", Seq: 0, Time: 1000, Data: raw(`{"turn":1}`)},
		{Type: "tool/call", Seq: 1, Time: 1100, Data: mustJSON(
			session.ToolCallEvent(1, 1, "c1", "enter_plan_mode", `{"reason":"先出方案"}`))},
		{Type: "tool/result", Seq: 2, Time: 1200, Data: mustJSON(
			session.ToolResultEvent(1, 1, "c1", nil, false, `D:\work`, nil))},
		{Type: "tool/call", Seq: 3, Time: 1300, Data: mustJSON(
			session.ToolCallEvent(1, 2, "c2", "exit_plan_mode", `{"plan":"# 方案"}`))},
		{Type: "step/start", Seq: 4, Time: 1400, Data: raw(`{"turn":1,"step":2}`)},
	}}
	s := officialServerWith(t, src, Options{})

	rec := postOfficial(t, s.handleSessionProjections, `{"sessionId":"s1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("projections status=%d body=%s", rec.Code, rec.Body.String())
	}
	values := unwrapResult(t, rec.Body.Bytes())["values"].(map[string]any)
	rawPlan, ok := values["plan"]
	if !ok {
		t.Fatalf("values 里必须有 plan 键（否则默认模式的输入框永远画不出计划模式）：%v", values)
	}
	plan, ok := rawPlan.(map[string]any)
	if !ok {
		t.Fatalf("plan 必须是对象：%#v", rawPlan)
	}
	// 官方 zod 两个字段全部必填（`z.object({active: z.boolean(), pending: z.boolean()})`，
	// 不是 `.partial()`）：少一个就是客户端读不到的位。
	if len(plan) != 2 {
		t.Fatalf("plan 字段 = %v，期望恰好 {active, pending}", plan)
	}
	// enter_plan_mode 已落地；exit_plan_mode 只是提交 plan-review，不是退出选择，
	// 所以它在飞期间 active=true、pending=false。只有 Approve//plan off 的 plan/mode 才退出。
	if active, ok := plan["active"].(bool); !ok || !active {
		t.Fatalf("plan.active = %#v，期望 true（enter_plan_mode 已经落地）", plan["active"])
	}
	if pending, ok := plan["pending"].(bool); !ok || pending {
		t.Fatalf("plan.pending = %#v，期望 false（plan-review 提交不是退出选择）", plan["pending"])
	}
}

// 空会话也必须给出 `plan`（官方 init 是 `{active:false, pending:false}`）。
//
// 为什么这一条单独钉：`plan` 键的缺席在官方是"部署里没装 plan-mode 能力"，
// 宿主的计划模式是内置的（`modules/tool/plan`），所以缺席一定是接线漏了，
// 而不是"这个会话没有计划模式"。
func TestOfficialPlanProjectionPresentOnEmptySession(t *testing.T) {
	src := &fakeOfficialSource{logEnd: -1}
	s := officialServerWith(t, src, Options{})

	rec := postOfficial(t, s.handleSessionProjections, `{"sessionId":"s1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("projections status=%d body=%s", rec.Code, rec.Body.String())
	}
	values := unwrapResult(t, rec.Body.Bytes())["values"].(map[string]any)
	plan, ok := values["plan"].(map[string]any)
	if !ok {
		t.Fatalf("空会话也必须给出 plan（官方 init 是 {active:false,pending:false}）：%v", values)
	}
	if active, ok := plan["active"].(bool); !ok || active {
		t.Fatalf("空会话 plan.active = %#v，期望 false", plan["active"])
	}
	if pending, ok := plan["pending"].(bool); !ok || pending {
		t.Fatalf("空会话 plan.pending = %#v，期望 false", plan["pending"])
	}
}

// 计划模式的切换必须在**实时增量通道**上到达客户端，而不只是在下一次
// `session.projections` 取值时才对。
//
// 为什么这一条是独立的：HTTP 面与 `session.observeProjections` 面在本仓是两套
// 折叠（一个整份 fold、一个逐事件 fold），官方客户端切换输入框模式靠的是后者
// 推过来的 `{key:"plan", val}` 帧。只有 HTTP 对上、增量通道不发帧，界面表现就是
// "进了计划模式但输入框还是默认模式"。
func TestOfficialPlanProjectionLiveDeltaFrame(t *testing.T) {
	srv := i04ProjectionServer(t)
	ctx := context.Background()
	stream, fault := srv.OpenOfficialFrameStream(ctx, "session/observeProjections",
		json.RawMessage(`{"sessionId":"s1"}`))
	if fault != nil {
		t.Fatalf("打开 observeProjections 失败：%v", fault)
	}
	defer stream.Close()
	if _, done, sfault := stream.Next(ctx); sfault != nil || done {
		t.Fatalf("observeProjections ready 帧失败：done=%v fault=%v", done, sfault)
	}

	src := srv.opt.Source.(*projectionHostSource)

	// 读帧直到拿到满足条件的 plan 变化帧（超时即失败，不许当成"没发"混过去）。
	waitPlan := func(wantActive bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			raw, done, fault := stream.Next(ctx)
			if fault != nil {
				t.Fatalf("observeProjections 增量帧故障：%v", fault)
			}
			if done {
				return
			}
			var frame struct {
				Type    string                     `json:"type"`
				Changes []officialProjectionChange `json:"changes"`
			}
			if json.Unmarshal(raw, &frame) != nil || frame.Type != "projection" {
				continue
			}
			for _, change := range frame.Changes {
				if change.Key != "plan" {
					continue
				}
				value, ok := change.Val.(map[string]any)
				if !ok {
					t.Fatalf("plan 变化帧的 val 必须是对象：%#v", change.Val)
				}
				if active, ok := value["active"].(bool); ok && active == wantActive {
					return
				}
			}
		}
		t.Fatalf("observeProjections 没有发出 plan.active=%v 的变化帧", wantActive)
	}

	// 进计划模式：tool/call → 在飞（pending），tool/result → 落地（active）。
	src.append(eventAt(7, "tool/call", `{"callId":"p1","name":"enter_plan_mode","arguments":"{}"}`))
	src.append(eventAt(8, "tool/result", `{"turn":2,"step":1,"message":{"role":"tool","source":{"kind":"tool","callId":"p1"},"toolCallId":"p1","content":[],"isError":false,"id":"m1"}}`))
	waitPlan(true)

	// 提交 plan-review：call/result 不改变 active，也不发退出帧。
	src.append(eventAt(9, "tool/call", `{"callId":"p2","name":"exit_plan_mode","arguments":"{\"plan\":\"# 方案\"}"}`))
	src.append(eventAt(10, "tool/result", `{"turn":2,"step":1,"message":{"role":"tool","source":{"kind":"tool","callId":"p2"},"toolCallId":"p2","content":[],"isError":false,"id":"m2"}}`))
	// 用户批准（或显式 /plan off）才由 durable plan/mode 发出退出增量。
	src.append(eventAt(11, "plan/mode", `{"active":false}`))
	waitPlan(false)
}
