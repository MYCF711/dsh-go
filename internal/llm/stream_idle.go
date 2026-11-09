package llm

// 流空闲看门狗（官方 `llm.deepseek.streamIdleTimeoutMs` /
// `llm.piAi.streamIdleTimeoutMs`，默认 300000ms 的"单次流读取未完成的最大提供方
// 空闲时间"，见 tmp/GO3-PARAMS/_official/dsh-llm-deepseek/README.md:63）。
//
// # 为什么必须有
//
// `Client.HTTP` 的 `Timeout` 是 **0**（流式响应不能被整体超时砍断），外层 ctx 在
// 真实调用里也没有 deadline。于是上游只要"发了一格就挂住"——半开连接、网关卡在
// 推理里、代理悄悄吞了连接——`Read` 就永远不返回：界面停在"正在生成"，退出码
// 永远不会出现，用户只能杀进程（`docs/parameter-register.md` #17/#18）。
//
// # 判据
//
// 计时器在**每次读到数据**时重置；整整 `Timeout` 没有新数据才判失败。所以"慢但
// 一直在出字"的流不会被误杀（官方同一句话："Maximum provider idle time per
// outstanding stream read"），只有真的静默才触发。
//
// 触发时立刻关掉响应体（让阻塞中的 `Read` 返回），再交出一个**结构化错误**：
// 四字段 `code/message/hint/retryable`（internal/transport/error_contract_test.go
// 的信封形状）加上计时证据（上限、实测静默时长、网关 base）。

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ErrorCodeStreamIdleTimeout 是看门狗错误的机读码。客户端据此判断"这不是
	// 模型答不出来，是链路静默了"，从而重新发起而不是换模型/改提示词。
	ErrorCodeStreamIdleTimeout = "LLM_STREAM_IDLE_TIMEOUT"

	// DefaultStreamIdleTimeout 是官方默认 300000ms。
	DefaultStreamIdleTimeout = 300 * time.Second

	// StreamIdleTimeoutEnv 是覆盖项（毫秒，正整数）。
	StreamIdleTimeoutEnv = "DSHD_LLM_STREAM_IDLE_TIMEOUT_MS"

	// StreamIdleTimeoutProvidersEnv 是**per-provider** 覆盖项（官方
	// `llm.piAi.providers.<name>.streamIdleTimeoutMs`，默认 300000；
	// 桌面 profile 里 magpie 是 600000）。形状是 JSON 对象（毫秒）：
	//
	//	DSHD_LLM_STREAM_IDLE_TIMEOUT_PROVIDERS_MS={"magpie":600000,"deepseek":300000}
	//
	// 为什么需要它：不同 provider 的"正常静默"量级差一个数量级——本地网关
	// 5 分钟没一格就是挂了，而走官方推理的 magpie 在长思考时本来就会静默
	// 好几分钟。一条全局上限只能取最松的那个，否则会误杀慢 provider。
	StreamIdleTimeoutProvidersEnv = "DSHD_LLM_STREAM_IDLE_TIMEOUT_PROVIDERS_MS"

	// maxStreamIdleTimeoutMs 是官方 MAX_TIMER_DELAY_MS（2^31-1）：超过它的定时器
	// 在实现上不可靠，官方同样 fail-loud 而不是截断。
	maxStreamIdleTimeoutMs = math.MaxInt32
)

// StreamIdleError 是流空闲超时的结构化错误。
//
// 四个契约字段用方法给出（`Code` / `Message` / `Hint` / `Retryable`），Transport
// 的 `{code,message,hint,retryable}` 信封可以原样翻译；另外带三份计时/定位证据。
type StreamIdleError struct {
	// Base 是网关 base（不含 /chat/completions），用于定位"是哪条链路静默了"。
	Base string
	// Timeout 是本次生效的空闲上限（环境变量/默认值解析结果）。
	Timeout time.Duration
	// Elapsed 是从最后一次收到数据到判定失败的实测时长（计时证据）。
	Elapsed time.Duration
}

// Code 是四字段错误的 code。
func (e *StreamIdleError) Code() string { return ErrorCodeStreamIdleTimeout }

// Message 是给人看的一句话：说清"发生了什么 + 数值多少"。
func (e *StreamIdleError) Message() string {
	return fmt.Sprintf("模型流空闲超时：%s 连续 %s 没有收到任何数据（上限 %s）",
		displayBase(e.Base), roundDuration(e.Elapsed), roundDuration(e.Timeout))
}

// Hint 是下一步动作：重发一次；反复出现就查链路，不要换模型。
func (e *StreamIdleError) Hint() string {
	return "重新发起这一轮（瞬时静默通常重发就好）；反复出现就查网关/代理的保活与半开连接，" +
		"不是模型 id 的问题，可以用 " + StreamIdleTimeoutEnv + " 调整空闲上限"
}

// Retryable 恒为 true：静默是链路瞬时故障，重新发起是合理动作。
func (e *StreamIdleError) Retryable() bool { return true }

// Error 让 StreamIdleError 满足 error。文案与 Message 相同——调用方拿到的是同一
// 句话，不必再判断类型才知道发生了什么。
func (e *StreamIdleError) Error() string { return e.Message() }

// roundDuration 把时长收成人类可读的毫秒精度（日志里不需要纳秒）。
func roundDuration(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

// displayBase 只留下 base，去掉 `/chat/completions` 与尾斜杠，免得文案里出现两段路径。
func displayBase(base string) string {
	trimmed := strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/chat/completions")
	if trimmed == "" {
		return "模型网关"
	}
	return trimmed
}

// streamIdleTimeoutFromEnv 解析空闲上限：未设置 = 官方默认 300000ms；
// 非法（非整数、<=0、超过 2^31-1）一律 fail-loud——静默回落会让人以为
// "我设了 10 分钟"，实际跑的是 5 分钟。
func streamIdleTimeoutFromEnv(env func(string) string) (time.Duration, error) {
	if env == nil {
		return DefaultStreamIdleTimeout, nil
	}
	raw := strings.TrimSpace(env(StreamIdleTimeoutEnv))
	if raw == "" {
		return DefaultStreamIdleTimeout, nil
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q 不是整数（单位毫秒）：%w", StreamIdleTimeoutEnv, raw, err)
	}
	if ms <= 0 || ms > maxStreamIdleTimeoutMs {
		return 0, fmt.Errorf("%s=%d 越界：必须是 1..%d 的正整数（单位毫秒）",
			StreamIdleTimeoutEnv, ms, maxStreamIdleTimeoutMs)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// streamIdleTimeoutsFromEnv 解析 per-provider 表：provider 名（大小写不敏感）
// → 空闲上限。空 = 没有 per-provider 覆盖。
//
// 与全局项同一套 fail-loud 判据：非 JSON、空 provider 名、非正数、超过 2^31-1
// 一律报错——静默丢掉一行只会让人以为"magpie 我设了 10 分钟"。
func streamIdleTimeoutsFromEnv(env func(string) string) (map[string]time.Duration, error) {
	if env == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(env(StreamIdleTimeoutProvidersEnv))
	if raw == "" {
		return nil, nil
	}
	var table map[string]int64
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&table); err != nil {
		return nil, fmt.Errorf("%s 不是合法 JSON 对象（{provider: 毫秒}）：%w", StreamIdleTimeoutProvidersEnv, err)
	}
	out := make(map[string]time.Duration, len(table))
	for provider, ms := range table {
		name := strings.ToLower(strings.TrimSpace(provider))
		if name == "" {
			return nil, fmt.Errorf("%s 里有空的 provider 名（要覆盖哪一条通道？）", StreamIdleTimeoutProvidersEnv)
		}
		if ms <= 0 || ms > maxStreamIdleTimeoutMs {
			return nil, fmt.Errorf("%s 的 %q=%d 越界：必须是 1..%d 的正整数（单位毫秒）",
				StreamIdleTimeoutProvidersEnv, provider, ms, maxStreamIdleTimeoutMs)
		}
		out[name] = time.Duration(ms) * time.Millisecond
	}
	return out, nil
}

// providerOf 从模型 id 取路由命名空间（`magpie/deepseek-v4` → `magpie`）。
//
// 宿主的模型 id 就是 `<provider>/<model>` 形状（catalog 与 CLI 都用这一份），
// 所以 provider 不需要第二处配置——它是 id 的前缀。
func providerOf(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.Index(model, "/"); i > 0 {
		return strings.ToLower(model[:i])
	}
	return ""
}

// streamIdleTimeoutFor 解析**这条流**的空闲上限（官方 per-provider 语义）。
//
// 优先级：Client 显式字段 > per-provider 表 > 全局 env > 官方默认 300000ms。
func (c *Client) streamIdleTimeoutFor(model string, env func(string) string) (time.Duration, error) {
	global, err := streamIdleTimeoutFromEnv(env)
	if err != nil {
		return 0, err
	}
	if c != nil && c.StreamIdleTimeout > 0 {
		return c.StreamIdleTimeout, nil
	}
	provider := providerOf(model)
	if provider == "" {
		return global, nil
	}
	table, err := streamIdleTimeoutsFromEnv(env)
	if err != nil {
		return 0, err
	}
	if timeout, ok := table[provider]; ok {
		return timeout, nil
	}
	return global, nil
}

// idleWatchdog 是空闲计时器：每次读到数据重置，超时回调一次（之后不再回调）。
//
// 并发形状：Read 在流 goroutine 里跑，回调在 timer goroutine 里跑，stop 在收尾
// 时跑。三者都用同一把锁串行化，避免 `Timer.Reset` 与"已经触发"之间的经典竞态
// （Reset 一个已触发的 timer 会重新武装它，那就变成"超时之后又超时一次"）。
type idleWatchdog struct {
	mu      sync.Mutex
	timeout time.Duration
	base    string
	onIdle  func()
	timer   *time.Timer
	fired   bool
	stopped bool
	last    time.Time
	// idleErr 是触发后留下的结构化错误（nil = 没触发）。
	idleErr *StreamIdleError
}

func newIdleWatchdog(timeout time.Duration, base string, onIdle func()) *idleWatchdog {
	watch := &idleWatchdog{timeout: timeout, base: base, onIdle: onIdle, last: time.Now()}
	watch.timer = time.AfterFunc(timeout, watch.fire)
	return watch
}

// fire 只在"没停止、没触发过"时生效：一次静默只报一次错。
func (w *idleWatchdog) fire() {
	w.mu.Lock()
	if w.stopped || w.fired {
		w.mu.Unlock()
		return
	}
	w.fired = true
	w.idleErr = &StreamIdleError{
		Base:    w.base,
		Timeout: w.timeout,
		Elapsed: time.Since(w.last),
	}
	onIdle := w.onIdle
	w.mu.Unlock()
	if onIdle != nil {
		onIdle() // 关掉响应体：让阻塞中的 Read 立刻返回。
	}
}

// touch 记录"刚刚读到了数据"并重置计时。
func (w *idleWatchdog) touch() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.fired {
		return
	}
	w.last = time.Now()
	w.timer.Reset(w.timeout)
}

// stop 收尾：停止计时器，之后的 touch/fire 都不再起作用。
func (w *idleWatchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	w.timer.Stop()
}

// err 返回触发后的结构化错误；没触发就是 nil。
func (w *idleWatchdog) err() *StreamIdleError {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.idleErr
}

// idleReader 把"读到数据就重置计时"接进流读取链。
type idleReader struct {
	inner io.Reader
	watch *idleWatchdog
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 {
		r.watch.touch()
	}
	return n, err
}
