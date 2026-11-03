package wasmhost

// ABI 层：把「一个已装载的 wasm 模块」变成「一段能被调用的工具逻辑」。
//
// 形状（docs/wasm-tool-abi.md §3，全部有实测支撑）：
//
//	alloc(len) -> ptr                 客人在线性内存里划一块，返回偏移
//	call(ptr,len) -> (ptr<<32)|len    客人吃 args JSON，吐结果 JSON
//
// 输入输出都走线性内存里的一段 JSON，没有 WIT、没有 component model、
// 没有新的 IDL —— 宿主与客人只约定两个导出和两个信封。
//
// **本文件最重要的一条：失败分三层，不许混。**
//
//   - 客人 trap（panic / 越界 / 断言）→ 返回 error，且 errors.Is(err, ErrGuestTrap)；
//     这不是工具失败，是插件崩了，模型不该看到「参数错了」这种暗示；
//   - 工具失败（文件不存在、参数不合法、被拒绝）→ **nil error**，
//     结果 JSON 里 isError:true，hint 留给 Exec 转成 Result.Hint；
//   - 宿主协议错（指针越界、长度超限、信封坏、版本不认识）→ error，
//     且 errors.Is(err, ErrProtocol)；这是宿主与客人对不上，重试没有意义。
//
// 为什么三层必须分开：v4 的 isError 是**按 err != nil** 编码的。
// 把 trap 说成「工具返回了一个空结果」会让模型以为工具说「没事」，
// 于是它拿着空结果继续往下做——这类错误在会话里查起来最费劲。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// ABI 版本与上限。超限属于**宿主协议错**，不是工具失败：
// 客人没做错什么，是宿主与它谈的条件对不上。
const (
	// ABIVersion 是宿主认的信封版本。客人回的信封也要带它。
	ABIVersion = 1
	// MaxArgsBytes 是 args 上限（1 MiB）。
	MaxArgsBytes = 1 << 20
	// MaxResultBytes 是结果上限（1 MiB）。
	MaxResultBytes = 1 << 20
	// DefaultTimeout 是单次调用默认 deadline。
	//
	// 不是「性能参数」而是安全参数：wasm 里的死循环不会自己结束，
	// 没有 deadline 的话它会把宿主的 goroutine 永久钉住。
	DefaultTimeout = 10 * time.Second
)

// ABI 的两个导出名。名字写死在这里，客人换编译器（Go / TinyGo）不用改宿主。
const (
	exportAlloc = "alloc"
	exportCall  = "call"
)

// 两个哨兵错误，给调用方分类用。
//
// 分开的理由是**处理方式不同**：trap 可以重建实例后重试（将来），
// 协议错重试一万次还是错，得改代码或改插件。
var (
	// ErrGuestTrap 表示客人在执行中崩溃了（wasm trap）。
	ErrGuestTrap = errors.New("wasm 客人 trap")
	// ErrProtocol 表示宿主与客人的 ABI 对不上。
	ErrProtocol = errors.New("wasm ABI 协议错")
)

// ProtocolError 是一条宿主侧的协议错，带上是哪一步对不上。
type ProtocolError struct {
	Step   string // alloc / call / 读回 / 校验
	Reason string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("wasm ABI 协议错（%s）：%s", e.Step, e.Reason)
}

// Is 让 errors.Is(err, ErrProtocol) 成立。
func (e *ProtocolError) Is(target error) bool { return target == ErrProtocol }

// TrapError 是客人在执行中 trap。
//
// Stderr 是客人自己打的最后几 KiB 输出：Go 运行时的
// "call _initialize first" 就是从这里出来的——上一轮 spike 正是把它丢了，
// 才把「少跑一个函数」查成了「wasm 不支持 Go 产物」。
type TrapError struct {
	Module string
	Fn     string
	Cause  error
	Stderr string
}

func (e *TrapError) Error() string {
	s := fmt.Sprintf("wasm 模块 %q 在 %s 里 trap（插件内部崩溃，不是工具失败）: %v", e.Module, e.Fn, e.Cause)
	if e.Stderr != "" {
		s += "\n客人 stderr: " + e.Stderr
	}
	return s
}

// Unwrap 让 errors.Is 能穿透到 wazero 的原始错误。
func (e *TrapError) Unwrap() error { return e.Cause }

// Is 让 errors.Is(err, ErrGuestTrap) 成立。
func (e *TrapError) Is(target error) bool { return target == ErrGuestTrap }

// Request 是宿主写进客人线性内存的请求信封。
//
// Tool 现在看着多余（一个模块一个工具），但它让「一个模块挂多个工具」
// 将来不需要改 ABI：客人不用从 args 里反推自己在被当成谁用。
type Request struct {
	V    int             `json:"v"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// Reply 是客人吐出的结果信封。Summary / Text / IsError 三个必填。
type Reply struct {
	V       int    `json:"v"`
	Summary string `json:"summary"`
	Text    string `json:"text"`
	IsError bool   `json:"isError"`
	Hint    string `json:"hint,omitempty"`
}

// ParseReply 校验并解析客人返回的结果信封。
//
// 校验放在一处：InvokeTool 用它证明「我交出去的是一段合法信封」，
// Exec 用它把 isError/hint 翻成 Result。两处不各写一份解析。
func ParseReply(raw []byte) (Reply, error) {
	var r Reply
	if len(raw) == 0 {
		return r, &ProtocolError{Step: "读回", Reason: "客人返回了空结果（len=0）；ABI 要求至少返回一个 JSON 信封"}
	}
	if !json.Valid(raw) {
		return r, &ProtocolError{Step: "读回", Reason: fmt.Sprintf("客人返回的不是合法 JSON：%q", truncateForErr(raw))}
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, &ProtocolError{Step: "读回", Reason: fmt.Sprintf("客人返回的结果信封解析失败: %v", err)}
	}
	if r.V != ABIVersion {
		return r, &ProtocolError{
			Step:   "读回",
			Reason: fmt.Sprintf("客人回的信封 v=%d，宿主只认 %d；两边 ABI 版本不一致", r.V, ABIVersion),
		}
	}
	return r, nil
}

// SetToolName 记下这个模块对外暴露的工具名（写进请求信封的 tool 字段）。
//
// 默认是装载时给的模块名；工具名与模块 id 不同时才需要调。
func (m *Module) SetToolName(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name != "" {
		m.tool = name
	}
}

// ToolName 返回写进请求信封的工具名。
func (m *Module) ToolName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tool
}

// SetTimeout 改单次调用的 deadline（0 = 用 DefaultTimeout）。
//
// 由装配层按 manifest 的 timeout_ms 设：一个工具要跑 30 秒是**部署决定**，
// 不该让每个调用方各自记得传 ctx。
func (m *Module) SetTimeout(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d <= 0 {
		d = DefaultTimeout
	}
	m.timeout = d
}

// InvokeTool 把 args 交给客人，拿回结果信封（原样 JSON 字节）。
//
// 三步：alloc(len) → 写 args → call(ptr,len) → 按返回值读回结果。
//
// **返回 nil error 不等于工具成功**：成功与否在结果 JSON 的 isError 里。
// 这里只对「客人崩了」和「协议对不上」报错，因为这两类根本读不到结果。
func (m *Module) InvokeTool(ctx context.Context, args []byte) ([]byte, error) {
	if len(args) == 0 {
		args = []byte("{}")
	}
	if len(args) > MaxArgsBytes {
		return nil, &ProtocolError{
			Step:   "alloc",
			Reason: fmt.Sprintf("args %d 字节，超过上限 %d；这是宿主该拦的事，别指望客人", len(args), MaxArgsBytes),
		}
	}
	if !json.Valid(args) {
		// 参数本来该由 Runner/中间件保证是合法 JSON；到这里说明是宿主自己的 bug。
		return nil, &ProtocolError{
			Step:   "alloc",
			Reason: fmt.Sprintf("args 不是合法 JSON（宿主侧问题）：%q", truncateForErr(args)),
		}
	}

	// 一个实例同一时刻只能跑一个 call：call 是同步导出，
	// 重入会踩客人自己的全局状态（比如那个 keep 切片）。
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed || m.mod == nil {
		return nil, fmt.Errorf("模块 %s 已卸载，不能再调用", m.name)
	}

	payload, err := json.Marshal(Request{V: ABIVersion, Tool: m.tool, Args: args})
	if err != nil {
		return nil, &ProtocolError{Step: "alloc", Reason: fmt.Sprintf("请求信封编码失败: %v", err)}
	}

	timeout := m.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	// 上一层 ctx 更紧时 WithTimeout 自然取更早的那个，不需要在这里比较。
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return m.invokeLocked(callCtx, payload)
}

// invokeLocked 是 ABI 的三步本体。调用方必须持有 m.mu。
func (m *Module) invokeLocked(ctx context.Context, payload []byte) ([]byte, error) {
	mem := m.mod.Memory()
	if mem == nil {
		return nil, &ProtocolError{Step: "alloc", Reason: "模块没有导出线性内存：ABI 至少要一个 memory"}
	}
	allocFn := m.mod.ExportedFunction(exportAlloc)
	callFn := m.mod.ExportedFunction(exportCall)
	if allocFn == nil || callFn == nil {
		missing := exportAlloc
		if allocFn != nil {
			missing = exportCall
		}
		return nil, &ProtocolError{
			Step: "alloc",
			Reason: fmt.Sprintf("模块没有导出 %q（缺 %q）：它不是一个工具插件——"+
				"ABI 要求导出 alloc(len)->ptr 与 call(ptr,len)->(ptr<<32|len)，"+
				"Go 客人要用 -buildmode=c-shared 构建", missing, missing),
		}
	}

	// 1) alloc：让客人自己划一块内存。宿主只往客人给的区间里写。
	res, err := allocFn.Call(ctx, uint64(len(payload)))
	if err != nil {
		return nil, m.wrapCallError(exportAlloc, err)
	}
	if len(res) != 1 {
		return nil, &ProtocolError{
			Step:   "alloc",
			Reason: fmt.Sprintf("alloc 返回了 %d 个值，期望 1 个（ptr）；导出签名对不上", len(res)),
		}
	}
	if res[0] > math.MaxUint32 {
		return nil, &ProtocolError{
			Step:   "alloc",
			Reason: fmt.Sprintf("alloc 返回的指针 %d 超出 32 位线性内存范围", res[0]),
		}
	}
	ptr := uint32(res[0])
	if !mem.Write(ptr, payload) {
		return nil, &ProtocolError{
			Step: "alloc",
			Reason: fmt.Sprintf("alloc 返回的区间 [%d, %d+%d) 不在线性内存里（当前 %d 字节）；"+
				"客人给的指针不能越界", ptr, ptr, len(payload), mem.Size()),
		}
	}

	// 2) call：客人吃 args、吐 (ptr<<32)|len。
	res, err = callFn.Call(ctx, uint64(ptr), uint64(len(payload)))
	if err != nil {
		return nil, m.wrapCallError(exportCall, err)
	}
	if len(res) != 1 {
		return nil, &ProtocolError{
			Step:   "call",
			Reason: fmt.Sprintf("call 返回了 %d 个值，期望 1 个（ptr<<32|len）；导出签名对不上", len(res)),
		}
	}
	packed := res[0]
	outPtr := uint32(packed >> 32)
	outLen := uint32(packed & 0xFFFFFFFF)

	// 3) 读回结果。
	if outLen == 0 {
		// 空结果是**协议错**，不是「工具说没事」：这条区分正是本轮的重点之一。
		return nil, &ProtocolError{
			Step:   "读回",
			Reason: "call 返回长度 0：客人没有给出结果信封（工具失败请用 isError:true 表达，别返回空）",
		}
	}
	if outLen > MaxResultBytes {
		return nil, &ProtocolError{
			Step:   "读回",
			Reason: fmt.Sprintf("结果 %d 字节，超过上限 %d", outLen, MaxResultBytes),
		}
	}
	buf, ok := mem.Read(outPtr, outLen)
	if !ok {
		return nil, &ProtocolError{
			Step: "读回",
			Reason: fmt.Sprintf("结果区间 [%d, %d+%d) 不在线性内存里（当前 %d 字节）",
				outPtr, outPtr, outLen, mem.Size()),
		}
	}
	// 拷出来再交给上层：wazero 的 Read 是拷贝，但别让这层依赖别人的实现细节。
	out := make([]byte, len(buf))
	copy(out, buf)

	if _, err := ParseReply(out); err != nil {
		return nil, err
	}
	return out, nil
}

// wrapCallError 把一次导出调用失败分成「被中断」与「客人 trap」两类。
//
// 为什么必须分：超时/取消时 wazero 会把整个 module 关掉（IsClosed），
// 那不是客人写错了，是宿主给的时间到了；把它说成 trap，会让人去查客人的 bug。
func (m *Module) wrapCallError(fn string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("wasm 模块 %q 的 %s 被中断（%w）："+
			"wazero 在 ctx 结束时关闭实例，这个模块需要重建才能再用", m.name, fn, err)
	}
	return &TrapError{Module: m.name, Fn: fn, Cause: err, Stderr: m.stderrTail()}
}

// truncateForErr 截断要进错误信息的内容：错误是给人看的，不是把 1 MiB 打出来。
func truncateForErr(b []byte) string {
	const limit = 160
	if len(b) <= limit {
		return string(b)
	}
	return string(b[:limit]) + fmt.Sprintf("…（共 %d 字节）", len(b))
}
