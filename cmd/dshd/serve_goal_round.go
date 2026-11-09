package main

// 官方 0.2.2 `goal-round-driver`（官方插件 #60，包 `@deepseek-ai/dsh-goal-round-driver`）
// 在 Go 宿主里的最小落点：**真实 turn 正常收工之后推进 goal 轮次**。
//
// 官方证据（解 `Desktop-0.2.2-rc.1/resources/app.asar` 只读得到）：
//   - 包/README：`/dsh/node_modules/@deepseek-ai/dsh-goal-round-driver/README.md`
//     §"What each round does"：一个 active + armed 的 goal 在 agent idle 时排一輪
//     `<goal_round>` prompt，"only an entered goal message consumes the cap"；
//     §"When continuation stops"：完成/暂停/阻塞都抑制续轮，轮次用尽写 `round-limit` blocker。
//   - 同一包 `lib/index.js` 的 fold：`user/message` 的 goal source 命中后
//     `state.roundsStarted = source.round`，即**计数来自真实进入历史的轮次**。
//   - goal 服务 `lib/index.js`：`resolveMaxGoalRounds` 要求正整数，create 时物化
//     部署默认值；`resume` 在 `roundsStarted >= maxGoalRounds` 时拒绝。
//
// 本宿主的三处已知差异（都写在 RESULT 的未验项里，不在这里偷偷抹平）：
//  1. 宿主没有 goal-source 消息，所以"哪一轮算 goal round"没有官方那种标记。
//     这里按本任务书口径：**每一轮真实 turn 正常收工 = 一轮**。
//  2. 官方驱动器会自己排 `<goal_round>` prompt 起新 turn；本任务明确不新建 turn，
//     因此这里只推进计数（证据在，能力留给后续接线，见 RESULT）。
//  3. `session.setGoal` 建 goal 时 `maxGoalRounds` 落 0（既有存储语义，本任务不改），
//     官方无法表达 0。这里把 `<=0` 解成"未配置上限"，否则宿主自己建的 goal 永远不计数。
//
// 重启一致性：不靠进程内计数器，而是把"这个 goal 已消化到第几轮真实 turn"做成
// 持久游标（`<home>/state/session-goal-rounds.json`），每次推进都从 v4 日志重算
// 目标值。重复调用算出同一个结果（幂等），崩溃后 `Reconcile` 能把漏记的轮次补上。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/transport"
)

const goalRoundStoreVersion = 1

// goalRoundPromptTemplate 是官方 `renderGoalRoundPrompt`（`dsh-goal-round-driver`
// 的 `lib/types/prompt.js`）的逐字翻译：**一个字都不能改**。
//
// 为什么：这段提示词会作为一条 user/message 进入会话历史，后续每一轮都要重发
// 它（直到压缩遮蔽）。改一个字，等于让模型对"这是官方自动续轮"的判据失真，
// 也让 app.asar 里的官方证据与宿主行为对不上。
const goalRoundPromptTemplate = `<goal_round>
Objective: %s
Round: %d/%d

Continue working toward the objective in this same session. Treat the current workspace, tool results, and durable session state as authoritative; inspect them instead of assuming earlier narration is still current. Make concrete progress and verify the result. Before claiming completion, gather evidence that the whole objective is achieved, read the current goal, and mark it complete. If work remains, leave the goal active for the next round. Follow the configured goal-tool policy before reporting a blocker.
</goal_round>`

// goalRoundCursor 是"这个 goal 已经消化到哪"的持久游标。
//
//   - Turns：已消化的**正常完成**真实 turn 数（v4 `turn/end` 且 reason.kind=completed）。
//   - Rounds：消化完这些 turn 之后 `roundsStarted` 应有的值。
//
// 两个值都存"当前应为多少"而不是增量：崩溃后重算时用它和真实日志对齐，
// 不会因为一次重复调用把轮次多算一格。
type goalRoundCursor struct {
	GoalID string `json:"goalId"`
	Turns  int    `json:"turns"`
	Rounds int    `json:"rounds"`
}

type goalRoundDocument struct {
	Version  int                        `json:"version"`
	Sessions map[string]goalRoundCursor `json:"sessions"`
}

// goalRoundDriver 把 goal 存储接到真实 turn 生命周期上。
//
// 它**不**新建 turn、不排 prompt、不改 prompt 队列：唯一动作是 turn 收工后
// 推进 `roundsStarted`，并沿既有 goal 通道发一帧 `goal/change{operation:"round"}`。
type goalRoundDriver struct {
	goals *goalOverrideStore
	home  string
	path  string

	// agents 是活体 agent 状态（官方 `ctx.agents` 的落点）：只有 whole-agent
	// idle 才允许排下一轮。它是**进程内**状态，重启即空——与官方
	// "activation is process-local, never persisted" 同一口径。
	agents *agent.Registry

	// mu 只保护游标与进程内续轮资格；store 自己的锁在它外层
	// （先 goals.mu 再 mu，绝不反向）。
	mu      sync.Mutex
	cursors map[string]goalRoundCursor
	// arms 是"这个会话当前的目标还允不允许自动续轮"的**推导**结果。
	//
	// 为什么是推导而不是直接读某个 activation 字段：官方把 activation 定义成
	// 进程内资格、永不持久化；本宿主里 goal 的迁移有两条真实写入口
	// （goalOverrideStore.transition 与官方 goals.* 的 agentGoalTransact），
	// 两处都不归本任务写。于是驱动只认**可观测的 durable 事实**：
	// goal id 换了 = 新建，phase 变了 = 一次迁移。这样"重启后不自动复活"
	// 与"resume 之后继续"都能由日志本身推出来，不需要第三个真值。
	arms    map[string]goalArm
	loadErr error
}

// goalArm 是一次观测到的目标身份 + 它当前是否允许自动续轮。
type goalArm struct {
	GoalID   string
	Revision int64
	Phase    string
	// LastOp 是**上一次观测到的**迁移操作（goal/change 帧上的 operation，
	// 续轮帧 round 不算迁移）。它是唯一能区分"编辑"与"暂停后恢复"的
	// durable 证据——这两者都只让 phase 保持 active、只让 revision 增加。
	LastOp string
	Armed  bool
}

func newGoalRoundDriver(goals *goalOverrideStore, home string) *goalRoundDriver {
	return &goalRoundDriver{
		goals:   goals,
		home:    home,
		path:    filepath.Join(home, "state", "session-goal-rounds.json"),
		agents:  agent.NewRegistry(agent.RegistryOptions{}),
		cursors: map[string]goalRoundCursor{},
		arms:    map[string]goalArm{},
	}
}

// Load 读游标文件。缺失 = 全新（空游标）；损坏/不可读 = 记 loadErr 并**响亮**失败，
// 之后每次推进都会报错，而不是把"读不到"当成"从头开始"（那会重复计轮）。
func (d *goalRoundDriver) Load() error {
	raw, err := os.ReadFile(d.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		d.loadErr = fmt.Errorf("读 goal 轮次游标 %s: %w", d.path, err)
		return d.loadErr
	}
	var doc goalRoundDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		d.loadErr = fmt.Errorf("解析 goal 轮次游标 %s: %w", d.path, err)
		return d.loadErr
	}
	if doc.Version != goalRoundStoreVersion || doc.Sessions == nil {
		d.loadErr = fmt.Errorf("goal 轮次游标 %s version/sessions 非法：version=%d", d.path, doc.Version)
		return d.loadErr
	}
	next := make(map[string]goalRoundCursor, len(doc.Sessions))
	for sessionID, cursor := range doc.Sessions {
		if sessionID == "" || cursor.GoalID == "" || cursor.Turns < 0 || cursor.Rounds < 0 {
			d.loadErr = fmt.Errorf("goal 轮次游标 %s 会话 %q 非法：%+v", d.path, sessionID, cursor)
			return d.loadErr
		}
		next[sessionID] = cursor
	}
	d.mu.Lock()
	d.cursors = next
	d.mu.Unlock()
	return nil
}

// wrap 只加一层"turn 收工后推进轮次"，其余能力原样透传。
//
// 必须把 RunContent/Steer/SteerContent 一起透出去：包装层少了任何一个，都会让
// busy steer / 有序附件块在**没有任何提示**的情况下退化成 501——那是回归，
// 不是本任务想要的"只加驱动"。
func (d *goalRoundDriver) wrap(next turnRunner) turnRunner {
	return &goalRoundRunner{next: next, driver: d}
}

type goalRoundRunner struct {
	next   turnRunner
	driver *goalRoundDriver
}

func (r *goalRoundRunner) Run(ctx context.Context, sessionID, turnID, text string) error {
	r.driver.agentStarted(sessionID)
	err := r.next.Run(ctx, sessionID, turnID, text)
	r.driver.agentSettled(sessionID)
	if err != nil {
		// 失败不空转：这一轮没成，就不排下一轮，并撤掉进程内续轮资格
		// （官方 `agent/error` → disarm）。
		r.driver.disarm(sessionID)
		return err
	}
	return r.continueRounds(ctx, sessionID, turnID)
}

func (r *goalRoundRunner) RunContent(ctx context.Context, sessionID, turnID string, blocks []transport.ContentBlock) error {
	next, ok := r.next.(contentTurnRunner)
	if !ok {
		// 与 promptService 的判据保持同一句话：包装层不许把"不支持"变成"先接受再失败"。
		return fmt.Errorf("%w: 当前 runner 未实现有序 content blocks", transport.ErrPlacementUnsupported)
	}
	r.driver.agentStarted(sessionID)
	err := next.RunContent(ctx, sessionID, turnID, blocks)
	r.driver.agentSettled(sessionID)
	if err != nil {
		r.driver.disarm(sessionID)
		return err
	}
	return r.continueRounds(ctx, sessionID, turnID)
}

// continueRounds 在**一轮已经正常收工之后**按官方 goal-round-driver 的判据
// 自动排下一轮，直到：目标不再可驱动、轮次用尽（写 round-limit blocker）、
// 被取消、或自动轮自己失败。
//
// 为什么在这里循环而不是去 promptService 排一条新 prompt：`serve.go` 的接线
// 不归本任务（外部写者串行），驱动只能包在 turnRunner 外面；而 promptService
// 本来就是 `go execute(...)` 跑 runner 的，所以这个循环天然不在 HTTP 请求路径上。
// 代价与收益都写在 RESULT 的"显式差异"里：**整段自动续轮期间会话一直是 busy**，
// 官方是每轮之间回到 idle 再排下一轮。
func (r *goalRoundRunner) continueRounds(ctx context.Context, sessionID, turnID string) error {
	lastRound := 0
	for {
		if err := ctx.Err(); err != nil {
			// 取消：不再续轮（官方 aborted 不自动重启）。
			return nil
		}
		if err := r.driver.Advance(sessionID); err != nil {
			// 记账失败：响亮 + 停。不能拿"没记上"的轮次继续跑。
			fmt.Fprintf(os.Stderr, "goal 自动续轮记账失败（session=%s）：%v\n", sessionID, err)
			return nil
		}
		decision, cont, err := r.driver.decideAfterTurn(sessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "goal 自动续轮判定失败（session=%s）：%v\n", sessionID, err)
			return nil
		}
		switch decision {
		case goalStop:
			return nil
		case goalRoundLimit:
			if err := r.driver.blockRoundLimit(sessionID); err != nil {
				fmt.Fprintf(os.Stderr, "goal 轮次用尽后写 blocker 失败（session=%s）：%v\n", sessionID, err)
			}
			return nil
		}
		if cont.Round <= lastRound {
			// 轮次没有前进（日志里那一轮没落成 completed）：拒绝空转。
			// 没有这一格，一个只返回 nil、不写 turn/end 的 runner 会让宿主
			// 对同一轮无限重跑——这正是"失败不空转"要挡住的东西。
			fmt.Fprintf(os.Stderr,
				"goal 自动续轮停止：round=%d 没有前进（last=%d，session=%s）\n",
				cont.Round, lastRound, sessionID)
			return nil
		}
		lastRound = cont.Round

		nextTurnID := goalRoundTurnID(turnID, cont.Round)
		// 上一轮 writer（serve 收尾的标题/摘要 goroutine 或残留的子进程）
		// 可能还持着会话写锁；先做有界等待，别让自动轮一 spawn 就撞锁失败。
		// 只观察、不删锁；等待超时也交给子进程按原有错误上报。
		r.waitSessionWriteReleased(sessionID)
		r.driver.agentStarted(sessionID)
		runErr := r.next.Run(ctx, sessionID, nextTurnID, cont.Prompt)
		r.driver.agentSettled(sessionID)
		if runErr != nil {
			// 自动轮失败：停 + 撤资格。**不**把错误回传给第一轮调用方——
			// turn/done 已经发出去了，这时改返回值只会得到"客户端看到成功、
			// 宿主内部报失败"的更糟状态（与 afterTurn 的取舍同源）。
			fmt.Fprintf(os.Stderr, "goal 自动续轮第 %d 轮失败（session=%s）：%v\n", cont.Round, sessionID, runErr)
			// 只写 stderr 等于静默：补一帧 transport 错误帧，界面/日志才看得到。
			r.driver.reportAutoRoundFailure(sessionID, nextTurnID, runErr)
			r.driver.disarm(sessionID)
			return nil
		}
	}
}

// waitSessionWriteReleased 在自动续轮 spawn 前做有界等待：上一轮 writer
// （serve 收尾的标题/摘要 goroutine、或刚退出子进程留下的锁）释放
// `session.v4.jsonl.zstd.lock` 后再起下一轮。
//
// 只观察、不删除、不按 PID 强清：未知锁留在盘上，让子进程按原有错误上报；
// 等待超时也不改变失败语义（只是少一次本可避免的"会话正被另一个进程写入"）。
// 真正的兜底是 `internal/session.claimWriter` 对**其他进程**的锁做同样的
// 有界等待——这里先等一轮，减少自动轮白跑。
const (
	autoRoundSessionLockWait = 2 * time.Second
	autoRoundSessionLockPoll = 10 * time.Millisecond
)

func (r *goalRoundRunner) waitSessionWriteReleased(sessionID string) {
	if r.driver == nil || r.driver.home == "" {
		return
	}
	path, err := newSessionSource(r.driver.home, "").resolve(sessionID)
	if err != nil {
		return
	}
	lock := path + ".lock"
	deadline := time.Now().Add(autoRoundSessionLockWait)
	for {
		if _, statErr := os.Stat(lock); os.IsNotExist(statErr) {
			return
		} else if statErr != nil {
			return // 权限/IO 异常：交给子进程按原错误上报，不吞
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr,
				"goal 自动续轮等待会话写锁超时（session=%s lock=%s），交给子进程按原有错误上报\n",
				sessionID, lock)
			return
		}
		time.Sleep(autoRoundSessionLockPoll)
	}
}

// reportAutoRoundFailure 把自动续轮失败变成**可观察**的一帧 turn/error。
//
// 为什么必须有：旧的唯一出口是 stderr。用户/界面看到的就是"目标自己不跑了"：
// journal 没有帧、API 没有错误、界面上什么都没有（T06 F4 的第二个半场）。
// 复用既有的 turn/error（普通 prompt 轮失败同形），不新造帧类型；客户端
// 现有的"这一轮失败了"处理路径直接生效。
//
// 帧写不进去（journal 未接线/写盘失败）时退回 stderr，并且**不**假装成功：
// 这是接线缺失或持久化故障，不是"失败已经可见"。
func (d *goalRoundDriver) reportAutoRoundFailure(sessionID, turnID string, runErr error) {
	if d.goals == nil || d.goals.journal == nil || d.goals.journal.journals == nil {
		fmt.Fprintf(os.Stderr,
			"goal 自动续轮失败：goal/change journal 未接线，无法产生 turn/error 帧（session=%s）\n", sessionID)
		return
	}
	// 子进程自己的失败路径（loopRunner）可能已经写同 turnID 的 turn/error；
	// 已经可观察时不再补第二帧，避免客户端收到重复失败。
	if d.autoRoundTurnErrorReported(sessionID, turnID) {
		return
	}
	data, err := json.Marshal(turnErrorData{TurnID: turnID, Error: runErr.Error()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "goal 自动续轮失败帧序列化失败（session=%s）：%v\n", sessionID, err)
		return
	}
	if _, emitErr := emitFrameResult(
		d.goals.journal.journals, d.goals.journal.pub,
		sessionID, TurnError, data); emitErr != nil {
		fmt.Fprintf(os.Stderr, "goal 自动续轮失败帧写入失败（session=%s）：%v\n", sessionID, emitErr)
	}
}

// autoRoundTurnErrorReported 报告这个 turnID 是否已有一帧 turn/error。
//
// 只用来看"失败是否已经可观察"：有就跳过补帧，没有才补。读的是与
// goal/change 同一份 Transport journal，不新增第二份状态。
func (d *goalRoundDriver) autoRoundTurnErrorReported(sessionID, turnID string) bool {
	if d.goals == nil || d.goals.journal == nil || d.goals.journal.journals == nil {
		return false
	}
	journal := d.goals.journal.journals.Get(sessionID)
	if journal == nil {
		return false
	}
	frames, _, _ := journal.Since(0, 0)
	for _, frame := range frames {
		if frame.Type != TurnError {
			continue
		}
		var data turnErrorData
		if json.Unmarshal(frame.Data, &data) == nil && data.TurnID == turnID {
			return true
		}
	}
	return false
}

// goalRoundTurnID 给自动续轮造一个**会话内唯一**的 turn id。
//
// 不能复用上一轮的 id：`liveTurn`/`registerModelSteer` 都按 (session, turnID)
// 建索引，复用会让两轮互相认领。带上原 id 是为了让人在日志/帧里看得出
// "这一轮是从哪一轮自动接出来的"。
func goalRoundTurnID(turnID string, round int) string {
	if turnID == "" {
		return fmt.Sprintf("goal-round-%d", round)
	}
	return fmt.Sprintf("%s-goal-%d", turnID, round)
}

// renderGoalRoundPrompt 渲染官方那一段续轮提示词。
//
// objective 走 JSON 引号（官方 `JSON.stringify(goal.objective)`）：目标里可能有
// 换行、引号、反斜杠，直接拼进提示词会让模型看到一条断开的指令。
func renderGoalRoundPrompt(goal *transport.GoalSnapshot, round int) string {
	objective, err := json.Marshal(goal.Objective)
	if err != nil {
		// 字符串序列化实际不可能失败；真失败了也不能编一个目标，退回原文。
		objective = []byte(`"` + goal.Objective + `"`)
	}
	return fmt.Sprintf(goalRoundPromptTemplate, objective, round, goal.MaxGoalRounds)
}

// Steer 只是往**当前**轮里注入，不结束 turn，所以不推进轮次。
func (r *goalRoundRunner) Steer(ctx context.Context, sessionID, turnID, text string) error {
	next, ok := r.next.(steerTurnRunner)
	if !ok {
		return fmt.Errorf("%w: 当前 runner 没有真实 steer 注入口", transport.ErrPlacementUnsupported)
	}
	return next.Steer(ctx, sessionID, turnID, text)
}

func (r *goalRoundRunner) SteerContent(ctx context.Context, sessionID, turnID string, blocks []transport.ContentBlock) error {
	next, ok := r.next.(steerContentTurnRunner)
	if !ok {
		return fmt.Errorf("%w: 当前 runner 没有真实 content steer 注入口", transport.ErrPlacementUnsupported)
	}
	return next.SteerContent(ctx, sessionID, turnID, blocks)
}

// ---------------------------------------------------------------------------
// 进程内续轮资格（activation）与活体状态
// ---------------------------------------------------------------------------

// disarm 撤掉一个会话的自动续轮资格（官方 `ctx.goals.disarm`）。
func (d *goalRoundDriver) disarm(sessionID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if arm, ok := d.arms[sessionID]; ok && arm.Armed {
		arm.Armed = false
		d.arms[sessionID] = arm
	}
}

func (d *goalRoundDriver) arm(sessionID string) (goalArm, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	arm, ok := d.arms[sessionID]
	return arm, ok
}

// nextArm 由**可观测的 durable 事实**推导进程内资格（见 goalRoundDriver.arms 的注释）。
//
//	mount         = true → 启动重算/挂载：历史目标一律 disarmed（官方 mount 从不 arm）
//	新 goal id           → 本进程第一次看到的新目标 = create → armed
//	迁移操作变了         → 按操作语义：resume/create 重新 arm，
//	                      pause/complete/block/clear 一律 disarm，edit 保持
//	都没变               → 保持原来的资格
//
// 为什么必须看 operation 而不是只看 phase：pause 与 resume 都只把 phase 在
// active/paused 之间搬，而**两次迁移可能都发生在两轮之间**（用户暂停又恢复，
// 然后才发下一条消息）。只比 phase 会把"暂停过"完全抹掉，于是人明明暂停了
// 一次，驱动器却继续自动跑——这正是"armed 语义"要挡住的东西。
func nextArm(previous goalArm, seen bool, goal *transport.GoalSnapshot, lastOp string, mount bool) goalArm {
	arm := goalArm{GoalID: goal.ID, Revision: goal.Revision, Phase: goal.Phase, LastOp: lastOp}
	switch {
	case mount:
		arm.Armed = false
	case !seen || previous.GoalID != goal.ID:
		arm.Armed = true
	case previous.LastOp != lastOp:
		switch lastOp {
		case "create", "resume":
			arm.Armed = true
		case "pause", "complete", "block", "clear":
			arm.Armed = false
		default:
			// edit 或未知操作：官方 edit 不动资格。
			arm.Armed = previous.Armed
		}
	default:
		arm.Armed = previous.Armed
	}
	return arm
}

// agentStarted / agentSettled 把"一轮开始/收工"登记进活体注册表。
//
// 失败只打日志：状态登记失败不该让一轮 turn 失败（那是可观测性问题，
// 不是执行问题），但也不能静默——静默会让"状态没有真值"永远查不出来。
func (d *goalRoundDriver) agentStarted(sessionID string) {
	if d == nil || d.agents == nil {
		return
	}
	if _, err := d.agents.Start(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "agent 状态登记失败（session=%s）：%v\n", sessionID, err)
	}
}

func (d *goalRoundDriver) agentSettled(sessionID string) {
	if d == nil || d.agents == nil {
		return
	}
	if _, err := d.agents.Stop(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "agent 状态收工失败（session=%s）：%v\n", sessionID, err)
	}
}

// ---------------------------------------------------------------------------
// 续轮判据
// ---------------------------------------------------------------------------

type goalDecision int

const (
	// goalStop 表示不再排下一轮（目标不存在/暂停/完成/阻塞/未配置上限/非 idle）。
	goalStop goalDecision = iota
	// goalContinue 表示排下一轮。
	goalContinue
	// goalRoundLimit 表示轮次预算用尽：写 round-limit blocker 并停。
	goalRoundLimit
)

// goalContinuation 是一轮续行的全部内容。
type goalContinuation struct {
	Round  int
	Prompt string
}

// decideAfterTurn 是"这一轮收工后要不要自动排下一轮"的**唯一**判据。
//
// 逐条对齐官方 `drive()`：
//   - 只有 whole-agent idle 才排（活体注册表是唯一真值）；
//   - goal 必须存在、phase=active、activation=armed；
//   - 轮次预算用尽 → 写 `round-limit` blocker，不排；
//   - 模型被 max tokens 截断 → disarm + 不排（半截回答不接）；
//   - 未配置上限（MaxGoalRounds<=0）→ 不排：本宿主 legacy setGoal 落 0 表示
//     "没有上限"，而"没有上限"在这里等于无限自动跑，两者都不可接受。
func (d *goalRoundDriver) decideAfterTurn(sessionID string) (goalDecision, goalContinuation, error) {
	if d.goals == nil {
		return goalStop, goalContinuation{}, fmt.Errorf("%w: goal store 未接线", transport.ErrStatePersistence)
	}
	if d.agents != nil && !d.agents.Idle(sessionID) {
		return goalStop, goalContinuation{}, nil
	}
	kind, err := d.lastTurnKind(sessionID)
	if err != nil {
		return goalStop, goalContinuation{}, err
	}
	if kind == "max-tokens" {
		d.disarm(sessionID)
		return goalStop, goalContinuation{}, nil
	}

	g := d.goals
	g.mu.Lock()
	loadErr := g.loadErr
	record, exists := g.m[sessionID]
	g.mu.Unlock()
	if loadErr != nil {
		return goalStop, goalContinuation{}, fmt.Errorf("%w: %v", transport.ErrStatePersistence, loadErr)
	}
	if !exists || record.Goal == nil {
		return goalStop, goalContinuation{}, nil
	}
	goal := record.Goal
	arm, seen := d.arm(sessionID)
	if !seen || arm.GoalID != goal.ID || !arm.Armed {
		return goalStop, goalContinuation{}, nil
	}
	if goal.Phase != "active" {
		return goalStop, goalContinuation{}, nil
	}
	rounds := goalRoundsStarted(record)
	if goal.MaxGoalRounds <= 0 {
		return goalStop, goalContinuation{}, nil
	}
	if rounds >= goal.MaxGoalRounds {
		return goalRoundLimit, goalContinuation{Round: rounds}, nil
	}
	round := rounds + 1
	return goalContinue, goalContinuation{Round: round, Prompt: renderGoalRoundPrompt(goal, round)}, nil
}

// blockRoundLimit 在轮次预算用尽时写一条稳定的 `round-limit` blocker。
//
// 官方文案逐字：`Goal reached its configured limit of N rounds.`
func (d *goalRoundDriver) blockRoundLimit(sessionID string) error {
	if d.goals == nil {
		return fmt.Errorf("%w: goal store 未接线", transport.ErrStatePersistence)
	}
	g := d.goals
	g.mu.Lock()
	record, exists := g.m[sessionID]
	g.mu.Unlock()
	if !exists || record.Goal == nil {
		return nil
	}
	limit := record.Goal.MaxGoalRounds
	err := g.Block(sessionID, "round-limit",
		fmt.Sprintf("Goal reached its configured limit of %d rounds.", limit))
	d.disarm(sessionID)
	return err
}

// lastTurnKind 读这份会话**最后一条** turn/end 的 reason.kind。
//
// 为什么不用 completedTurns：那个只数"正常收工"，而这里要区分"正常"
// 与"被 max tokens 截断"——两者都不该继续续轮，但后者要先 disarm。
func (d *goalRoundDriver) lastTurnKind(sessionID string) (string, error) {
	path, err := newSessionSource(d.home, "").resolve(sessionID)
	if err != nil {
		if errors.Is(err, transport.ErrNoSuchSession) {
			return "", nil // 会话文件还没建：没有轮次，不是失败
		}
		return "", fmt.Errorf("goal 续轮读会话 %s: %v", sessionID, err)
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("goal 续轮读 v4 %s: %v", path, err)
	}
	kind := ""
	for _, event := range log.Events {
		if event.Type != "turn/end" {
			continue
		}
		var data struct {
			Reason struct {
				Kind string `json:"kind"`
			} `json:"reason"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return "", fmt.Errorf("解析 %s seq=%d 的 turn/end: %v", path, event.Seq, err)
		}
		kind = data.Reason.Kind
	}
	return kind, nil
}

// Reconcile 在 serve 启动时把"上次进程结束前已经完成、但没来得及记账"的轮次补上。
//
// 没有它，"turn 已落盘 → 进程被杀 → 计数没写"这一格就永久丢一轮；有了它，
// 启动重算从 v4 日志得到真实应得数，游标只记录已经消化到哪。
func (d *goalRoundDriver) Reconcile() error {
	sessions, err := d.sessionsWithGoals()
	if err != nil {
		return err
	}
	var errs []error
	for _, sessionID := range sessions {
		if err := d.advance(sessionID, false); err != nil {
			errs = append(errs, fmt.Errorf("session %s: %w", sessionID, err))
		}
	}
	return errors.Join(errs...)
}

func (d *goalRoundDriver) sessionsWithGoals() ([]string, error) {
	if d.goals == nil {
		return nil, fmt.Errorf("%w: goal store 未接线", transport.ErrStatePersistence)
	}
	g := d.goals
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.loadErr != nil {
		return nil, fmt.Errorf("%w: %v", transport.ErrStatePersistence, g.loadErr)
	}
	out := make([]string, 0, len(g.m))
	for sessionID, record := range g.m {
		if record.Goal != nil {
			out = append(out, sessionID)
		}
	}
	sort.Strings(out) // 固定顺序：重启重算不因 map 遍历随机性产生不同帧序
	return out, nil
}

// Advance 是唯一推进点：把一个会话的 goal 轮次对齐到"真实完成的 turn 数"。
//
// 幂等是核心性质：重复调用（同一轮收工被调用两次、重启重算、崩溃后补算）
// 都只会得到同一个 `roundsStarted`，因为它不是 `+1`，而是"由 v4 日志重算目标值"。
func (d *goalRoundDriver) Advance(sessionID string) error {
	return d.advance(sessionID, true)
}

// advance 的 countCurrentTurn 区分两条真实调用路径：
//   - turn 收工（Advance）：刚刚完成的就是目标的第 1 轮（目标可能在本轮里创建）；
//   - 启动重算（Reconcile）：没有"刚收工的那一轮"，基线就是全部历史轮次。
//
// 别小看这一格：目标是在两次 turn 之间建的（`session.setGoal`）时，少了它
// 第一轮就永远不算，目标的上限也就永远差一格。
func (d *goalRoundDriver) advance(sessionID string, countCurrentTurn bool) error {
	if sessionID == "" {
		return fmt.Errorf("%w: session id 为空", transport.ErrStatePersistence)
	}
	if d.goals == nil {
		return fmt.Errorf("%w: goal store 未接线", transport.ErrStatePersistence)
	}
	if err := d.cursorLoadError(); err != nil {
		return err
	}
	g := d.goals
	// 整段持 store 锁：goal 的 phase/rounds 判定与随后的写入必须是同一份事实，
	// 否则 pause/clear 插在中间就会把轮次推进到一个已经停用的 goal 上。
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.loadErr != nil {
		return fmt.Errorf("%w: %v", transport.ErrStatePersistence, g.loadErr)
	}

	record, exists := g.m[sessionID]
	if !exists || record.Goal == nil {
		// clear-missing 幂等：没有 goal 就没有轮次。不写盘、不发事件。
		// 续轮资格随之消失：没有目标就没有"下一轮"。
		d.clearArm(sessionID)
		return nil
	}
	goal := record.Goal
	rounds := 0
	if record.RoundsStarted != nil {
		rounds = *record.RoundsStarted
	}

	completed, err := d.completedTurns(sessionID)
	if err != nil {
		return err
	}
	previous, hasPrevious := d.cursor(sessionID)
	if !hasPrevious || previous.GoalID != goal.ID {
		// 第一次见到这个 goal：基线是它出现之前已经完成的轮次。
		baseline := completed
		if countCurrentTurn && completed > 0 {
			baseline = completed - 1
		}
		previous = goalRoundCursor{GoalID: goal.ID, Turns: baseline, Rounds: rounds}
	}
	next := goalRoundCursor{GoalID: goal.ID, Turns: completed, Rounds: previous.Rounds}
	if previous.Turns > completed {
		// 日志比游标短（换过 home、文件被截）：游标不倒退，也不倒推轮次。
		next.Turns = previous.Turns
	} else if goal.Phase == "active" && completed > previous.Turns {
		next.Rounds = previous.Rounds + (completed - previous.Turns)
	}
	if next.Rounds < rounds {
		// 别处已经推进过（或游标比存储旧）：以存储为准，绝不倒推。
		next.Rounds = rounds
	}
	if cap := goal.MaxGoalRounds; cap > 0 && next.Rounds > cap {
		next.Rounds = cap
	}

	if next.Rounds > rounds {
		advanced := next.Rounds
		nextRecord := cloneGoalRecord(record)
		nextRecord.RoundsStarted = &advanced
		nextGoals := cloneGoalMap(g.m)
		nextGoals[sessionID] = nextRecord
		if err := g.persistLocked(goalDocument{Version: goalStoreVersion, Goals: nextGoals}); err != nil {
			return err
		}
		previousGoals := cloneGoalMap(g.m)
		g.m = nextGoals
		if err := d.appendRoundFrame(sessionID, advanced); err != nil {
			// state 已写、帧没提交：回滚 state，不留"没有事件的成功计数"。
			if rollbackErr := g.persistLocked(goalDocument{Version: goalStoreVersion, Goals: previousGoals}); rollbackErr != nil {
				g.loadErr = fmt.Errorf("goal round 帧提交失败 %v；roundsStarted 回滚也失败 %v", err, rollbackErr)
				return fmt.Errorf("%w: %v", transport.ErrStatePersistence, g.loadErr)
			}
			g.m = previousGoals
			return err
		}
	}

	if !hasPrevious || previous != next {
		if err := d.setCursor(sessionID, next); err != nil {
			return err
		}
	}
	// 资格推导放在**记账之后**：这一轮已经算进 roundsStarted，下一轮的编号
	// 必须从新的计数上数（官方"only an entered goal message consumes the cap"）。
	lastOp, opErr := d.lastGoalOperation(sessionID)
	if opErr != nil {
		return opErr
	}
	previousArm, hasArm := d.arm(sessionID)
	d.setArm(sessionID, nextArm(previousArm, hasArm, goal, lastOp, !countCurrentTurn))
	return nil
}

// lastGoalOperation 读这份会话**最后一条真实迁移**的 operation。
//
// 为什么走 journal 而不是 goal 存储：goal 的迁移有两条真实写入口
// （`goalOverrideStore.transition` 与官方 `goals.*` 的 `agentGoalTransact`），
// 它们唯一的共同产物就是同一份 Transport journal 里的 `goal/change` 帧。
// 读帧是**不新增真值**的推导；把它塞进存储则会有第三份状态要同步。
//
// 续轮帧（operation=round）不是迁移，跳过——它由本驱动自己写，把它当迁移
// 会让"暂停过"在第一次续轮之后被抹掉。
func (d *goalRoundDriver) lastGoalOperation(sessionID string) (string, error) {
	if d.goals == nil || d.goals.journal == nil || d.goals.journal.journals == nil {
		return "", nil // 没接线：没有可观测迁移（测试里可能没挂 journal）
	}
	journal := d.goals.journal.journals.Get(sessionID)
	if journal == nil {
		return "", nil
	}
	frames, _, _ := journal.Since(0, 0)
	op := ""
	for _, frame := range frames {
		if frame.Type != "goal/change" {
			continue
		}
		var change transport.GoalChange
		if err := json.Unmarshal(frame.Data, &change); err != nil {
			return "", fmt.Errorf("解析 goal/change seq=%d: %w", frame.Seq, err)
		}
		if change.Operation == "round" {
			continue
		}
		op = change.Operation
	}
	return op, nil
}

// setArm 写一次资格推导结果。
func (d *goalRoundDriver) setArm(sessionID string, arm goalArm) {
	d.mu.Lock()
	d.arms[sessionID] = arm
	d.mu.Unlock()
}

// clearArm 在目标消失（clear）时把资格一并删掉：留着会让"清掉再建"复用旧资格。
func (d *goalRoundDriver) clearArm(sessionID string) {
	d.mu.Lock()
	delete(d.arms, sessionID)
	d.mu.Unlock()
}

// cursorLoadError 把"游标文件坏了"变成每次推进都失败的明确错误。
func (d *goalRoundDriver) cursorLoadError() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loadErr != nil {
		return fmt.Errorf("%w: %v", transport.ErrStatePersistence, d.loadErr)
	}
	return nil
}

func (d *goalRoundDriver) cursor(sessionID string) (goalRoundCursor, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cursor, ok := d.cursors[sessionID]
	return cursor, ok
}

func (d *goalRoundDriver) setCursor(sessionID string, cursor goalRoundCursor) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	previous, hadPrevious := d.cursors[sessionID]
	d.cursors[sessionID] = cursor
	if err := d.writeCursorsLocked(); err != nil {
		if hadPrevious {
			d.cursors[sessionID] = previous
		} else {
			delete(d.cursors, sessionID)
		}
		return err
	}
	return nil
}

// writeCursorsLocked 原子落盘（tmp + rename）；调用方必须持有 d.mu。
//
// 游标写失败**不会**把已经提交的 roundsStarted 退回去：state 是权威，游标只是
// 重算用的进度。下一次 Advance 从旧游标重算会得到同一个目标值（幂等），
// 所以这里只报错，不制造第二次状态变更。
func (d *goalRoundDriver) writeCursorsLocked() error {
	doc := goalRoundDocument{Version: goalRoundStoreVersion, Sessions: make(map[string]goalRoundCursor, len(d.cursors))}
	for sessionID, cursor := range d.cursors {
		doc.Sessions[sessionID] = cursor
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: 序列化 goal 轮次游标: %v", transport.ErrStatePersistence, err)
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o755); err != nil {
		return fmt.Errorf("%w: 建 goal 轮次游标目录: %v", transport.ErrStatePersistence, err)
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("%w: 写 goal 轮次游标 %s: %v", transport.ErrStatePersistence, tmp, err)
	}
	if err := os.Rename(tmp, d.path); err != nil {
		return fmt.Errorf("%w: 提交 goal 轮次游标 %s: %v", transport.ErrStatePersistence, d.path, err)
	}
	return nil
}

// appendRoundFrame 沿既有 goal 通道发一帧续轮。
//
// 形状按 `docs/tui-effort-modes-goals-host-note.md` §3：`operation:"round"` +
// 真实非负整数 `round`，**不重复 snapshot**；不新造帧类型，仍是 `goal/change`。
//
// 一次最多一帧：崩溃重启后跨多轮补算时，`round` 是推进后的**绝对计数**，
// 不是增量。这样 journal 里不会出现"中间帧已入账、state 却回滚了"的半套记录
// ——读方只要认最后一次 round 值就能对齐。
func (d *goalRoundDriver) appendRoundFrame(sessionID string, round int) error {
	journal := d.goals.journal
	if journal == nil || journal.journals == nil {
		return fmt.Errorf("%w: goal/change journal 未接线", transport.ErrStatePersistence)
	}
	value := round
	data, err := json.Marshal(transport.GoalChange{
		Kind: "goal/change", Version: 1, Operation: "round", Round: &value,
	})
	if err != nil {
		return fmt.Errorf("%w: 序列化 goal round 帧: %v", transport.ErrStatePersistence, err)
	}
	if _, err := emitFrameResult(journal.journals, journal.pub, sessionID, "goal/change", data); err != nil {
		return fmt.Errorf("%w: 追加 goal round 帧: %v", transport.ErrStatePersistence, err)
	}
	return nil
}

// completedTurns 数这份会话里**正常收工**的真实 turn 数。
//
// 判据只认 v4 日志自己的结算：`turn/end` 且 `reason.kind` 为 completed（空 kind 是
// 早期日志形状，同样表示"这一轮结束了"）。取消（aborted/interrupted）、模型报错
// （error）、被 max tokens 截断（max-tokens）都不算——官方驱动器同样在
// max tokens 收工时停止续轮。
func (d *goalRoundDriver) completedTurns(sessionID string) (int, error) {
	path, err := newSessionSource(d.home, "").resolve(sessionID)
	if err != nil {
		if errors.Is(err, transport.ErrNoSuchSession) {
			return 0, nil // 会话文件还没建：没有已完成轮次，不是失败
		}
		return 0, fmt.Errorf("goal 轮次推进读会话 %s: %v", sessionID, err)
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("goal 轮次推进读 v4 %s: %v", path, err)
	}
	completed := 0
	for _, event := range log.Events {
		if event.Type != "turn/end" {
			continue
		}
		var data struct {
			Reason struct {
				Kind string `json:"kind"`
			} `json:"reason"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return 0, fmt.Errorf("解析 %s seq=%d 的 turn/end: %v", path, event.Seq, err)
		}
		switch data.Reason.Kind {
		case "", "completed":
			completed++
		}
	}
	return completed, nil
}
