// Package wasmhost 是真的能把一个 wasm 模块装载起来并调用它的那一层。
//
// 边界（现在只做这一层）：
//   - 能装载、能按名字调导出函数、能卸载、能给出内容摘要；
//   - 工具契约的跨边界映射在 abi.go：`alloc` + `call` 两个导出，
//     args 与结果都是线性内存里的一段 JSON（形状见 docs/wasm-tool-abi.md §3）；
//   - 仍然不接 manifest：谁把这些包装成 spi.Tool 是装配层的事
//     （cmd/dshd/wasmload.go）。kernel 不认识 wasm。
//
// 为什么单独一个包而不是塞进 kernel：kernel 的红线是"不识业务"，
// 而 wazero 是一个有重量的运行时依赖。放在这里，kernel 编译期不背它。
package wasmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// wasmMagic 是 wasm 的二进制的头四字节（\0asm）。
// 单独检查它而不是"编译失败就当不是 wasm"：错误要能一眼看出是"文件给错了"，
// 还是"文件是对的但里面有问题"。
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

// Module 是一个已装载的 wasm 模块。
//
// 用法：Load → Call / InvokeTool（可多次）→ Close。Close 幂等。
type Module struct {
	name   string
	path   string
	digest string

	// tool 是写进 ABI 请求信封的工具名，默认等于模块名。
	tool string
	// timeout 是单次 InvokeTool 的 deadline（0 = DefaultTimeout）。
	timeout time.Duration

	runtime wazero.Runtime
	mod     api.Module
	// log 保留客人最近几 KiB 的 stdout/stderr，trap 时拼进错误。
	log *outputLog

	mu     sync.Mutex
	closed bool
}

// outputLog 是客人 stdout/stderr 的环形缓冲（只留最后 4 KiB）。
//
// 为什么不是 io.Discard：上一轮 spike 的根因就丢在这里——Go 运行时自己把
// "runtime: wasmexport function called before runtime initialization /
// call _initialize first" 写到 stderr，而 Load 接的是 nil，于是宿主只剩
// 一句 "wasm error: unreachable"，把「少跑一个函数」查成了「不支持 Go 产物」。
// 客人的话可以很长，但**最后一句通常就是原因**。
type outputLog struct {
	mu  sync.Mutex
	buf []byte
}

const outputLogCap = 4096

func (l *outputLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	if len(l.buf) > outputLogCap {
		l.buf = l.buf[len(l.buf)-outputLogCap:]
	}
	return len(p), nil
}

func (l *outputLog) tail() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.TrimSpace(string(l.buf))
}

// Load 从文件装载一个 wasm 模块。
//
// 五件事都必须发生，否则调用方会得到一个"看起来能用"的东西：
//   - 文件不存在 / 不是 wasm → 明确报错，错误里带路径与原因；
//   - 算出内容的 sha256（manifest 校验要用）；
//   - 装 WASI 导入：Go / TinyGo 的 wasip1 产物都导入 wasi_snapshot_preview1，
//     不实例化它连模块都装不上（实测：module[wasi_snapshot_preview1] not instantiated）；
//   - 按导出表判形态并跑对的 start：有 `_initialize` 就是 reactor，必须跑它，
//     否则任何 //go:wasmexport 函数一进就被 runtime.notInitialized 打断（unreachable）；
//     只有 `_start` 的是 command 构建，明确报错让人重编，而不是装上一个调不动的模块；
//   - 任何一步失败都清理干净，不留 runtime、不留文件句柄。
func Load(ctx context.Context, name, path string) (m *Module, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读 wasm 失败（%s）: %w", path, err)
	}
	if len(raw) < 4 || string(raw[:4]) != string(wasmMagic) {
		return nil, fmt.Errorf("%s 不是 wasm 模块：文件不以 \\0asm 开头", path)
	}
	sum := sha256.Sum256(raw)

	// CloseOnContextDone：ctx 取消时能把正在跑的 wasm 打断，
	// 而不是把宿主的 goroutine 永久钉在一个死循环里（夹具里 spin() 就是干这个的）。
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
	ok := false
	defer func() {
		if !ok {
			_ = rt.Close(context.Background())
		}
	}()

	// 只挂 wasi_snapshot_preview1 的最小面；**不**挂任何 FS preopen、
	// 不传 args/env。隔离因此是被强制出来的，不是靠客人自我介绍。
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return nil, fmt.Errorf("实例化 wasi_snapshot_preview1 失败（%s）: %w", path, err)
	}

	compiled, err := rt.CompileModule(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("编译 wasm 失败（%s）: %w", path, err)
	}

	// 形态判定必须自己做：配错的 start 名 wazero **不会报错**，静默跳过，
	// 然后调用时才 trap（实测见 docs/wasm-tool-abi.md §1.2(c)）。
	exports := compiled.ExportedFunctions()
	_, hasInit := exports["_initialize"]
	if !hasInit {
		if _, hasStart := exports["_start"]; hasStart {
			return nil, fmt.Errorf("%s 是 command 形态的 wasm（只导出 _start、没有 _initialize）："+
				"这类模块跑完 main 就 proc_exit，宿主调不到任何导出函数。"+
				"Go 客人请用 GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared 重编成 reactor", path)
		}
	}

	log := &outputLog{}
	cfg := wazero.NewModuleConfig().
		WithName("plugin").
		WithStdout(log).WithStderr(log)
	if hasInit {
		// reactor：先把 Go/TinyGo 运行时拉起来，再允许调用导出。
		cfg = cfg.WithStartFunctions("_initialize")
	}
	mod, err := rt.InstantiateModule(ctx, compiled, cfg)
	if err != nil {
		if hasInit {
			// _initialize 失败的错误里不带客人 stderr，补上——原因常常就在那儿。
			if tail := log.tail(); tail != "" {
				return nil, fmt.Errorf("实例化 wasm 失败（%s）: %w\n客人输出: %s", path, err, tail)
			}
		}
		return nil, fmt.Errorf("实例化 wasm 失败（%s）: %w", path, err)
	}

	ok = true
	return &Module{
		name: name, path: path, digest: hex.EncodeToString(sum[:]),
		tool: name, timeout: DefaultTimeout,
		runtime: rt, mod: mod, log: log,
	}, nil
}

// stderrTail 返回客人最近的输出（trap 时拼进错误）。
func (m *Module) stderrTail() string {
	if m == nil || m.log == nil {
		return ""
	}
	return m.log.tail()
}

// Name / Path / Digest 是模块的身份信息。Digest 是内容的 sha256（小写十六进制）。
func (m *Module) Name() string   { return m.name }
func (m *Module) Path() string   { return m.path }
func (m *Module) Digest() string { return m.digest }

// Exports 返回模块导出的**函数**名，按字典序排序。
//
// 只列函数、不列内存/全局/表：这一层能做的事就是 Call，
// 列出来的应当正好是能调的。排序是为了输出稳定——
// 它会被 `dshd wasm inspect` 直接打到屏幕上，也会进将来的清单快照，
// 而 map 的遍历顺序每次都不一样。
//
// 已卸载的模块返回 nil（没有导出可说了），不报错：
// 这个方法描述的是"模块里有什么"，不是一次可能失败的操作。
func (m *Module) Exports() []string {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed || m.mod == nil {
		return nil
	}
	defs := m.mod.ExportedFunctionDefinitions()
	out := make([]string, 0, len(defs))
	for name := range defs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Call 调用一个导出函数。
//
// 找不到这个导出函数时**报错而不是返回零值**：把"没有这个函数"说成"调用成功、结果是 0"
// 是最难查的一类错误。用 ExportedFunction 先查，就是为了这个。
func (m *Module) Call(ctx context.Context, fn string, args ...uint64) ([]uint64, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("模块 %s 已卸载，不能再调用", m.name)
	}
	f := m.mod.ExportedFunction(fn)
	if f == nil {
		return nil, fmt.Errorf("模块 %s 没有导出函数 %q（可能是别的名字，或编译时没带 wasmexport）", m.name, fn)
	}
	res, err := f.Call(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("调用 %s.%s 失败: %w", m.name, fn, err)
	}
	return res, nil
}

// Close 卸载模块并释放 runtime。可重复调用。
func (m *Module) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()

	var first error
	if m.mod != nil {
		if err := m.mod.Close(ctx); err != nil {
			first = err
		}
	}
	if m.runtime != nil {
		if err := m.runtime.Close(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}
