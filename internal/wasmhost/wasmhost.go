// Package wasmhost 是真的能把一个 wasm 模块装载起来并调用它的那一层。
//
// 边界（现在只做这一层）：
//   - 能装载、能按名字调导出函数、能卸载、能给出内容摘要；
//   - **不提供工具契约的跨边界映射**（那要把结构体序列化过去，是下一层的事）；
//   - 不接 manifest、不改 dsh.toml：`implementedBackends` 现在仍然只认 static，
//     接入由监工决定，见 internal/kernel/module.go。
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
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// wasmMagic 是 wasm 的二进制的头四字节（\0asm）。
// 单独检查它而不是"编译失败就当不是 wasm"：错误要能一眼看出是"文件给错了"，
// 还是"文件是对的但里面有问题"。
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

// Module 是一个已装载的 wasm 模块。
//
// 用法：Load → Call（可多次）→ Close。Close 幂等。
type Module struct {
	name   string
	path   string
	digest string

	runtime wazero.Runtime
	mod     api.Module

	mu     sync.Mutex
	closed bool
}

// Load 从文件装载一个 wasm 模块。
//
// 三件事都必须发生，否则调用方会得到一个"看起来能用"的东西：
//   - 文件不存在 / 不是 wasm → 明确报错，错误里带路径与原因；
//   - 算出内容的 sha256（将来 manifest 校验要用）；
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

	compiled, err := rt.CompileModule(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("编译 wasm 失败（%s）: %w", path, err)
	}
	// 名字固定，避免同一 runtime 里重名冲突。
	// 不设 start 函数：夹具与将来的插件都是**被调用**的库形态，
	// 没有 `_start` 可跑；设了反而会因"找不到入口"装载失败。
	mod, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().
		WithName("plugin").
		WithStdout(nil).WithStderr(nil))
	if err != nil {
		return nil, fmt.Errorf("实例化 wasm 失败（%s）: %w", path, err)
	}

	ok = true
	return &Module{name: name, path: path, digest: hex.EncodeToString(sum[:]), runtime: rt, mod: mod}, nil
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
