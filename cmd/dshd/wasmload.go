package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	toml "github.com/pelletier/go-toml/v2"

	"dshgo/internal/kernel"
	"dshgo/internal/wasmhost"
)

// 这一层只做**装载与校验**，不做工具契约映射。
//
// manifest 里写 `backend = "wasm"` 的模块，装配前会真的被 wasmhost 装起来：
//   - 文件不存在 / 不是 wasm / 编译不过 → 装配失败，错误里带路径与 wasmhost 的原始原因；
//   - 写了 digest 但和文件内容对不上 → 装配失败。这正是 manifest 校验存在的意义：
//     内容被换掉要在**启动时**炸，而不是等第一次调用才发现；
//   - 通过校验的模块登记进装配结果，但 **Provides 恒为空**：它不进工具表、
//     模型看不到它，只能被人用 `dshd wasm call` 手动调。
//
// 为什么不把 digest 塞进 kernel.Module：kernel 的红线是"不识业务"，
// 也不该为了一次显示多一个字段；模块的身份（路径 / 摘要 / 导出）留在这一层。

// wasmLoaded 是本次装配里真正装起来的一个 wasm 模块。
//
// module 句柄留在这里而不是用完就关：装载意味着它确实能被调用，
// 将来接工具契约映射时直接从这儿取；现在没人调它（Provides 为空）。
type wasmLoaded struct {
	id      string
	path    string
	digest  string
	exports []string
	module  *wasmhost.Module
}

var (
	wasmMu      sync.Mutex
	wasmCurrent []wasmLoaded
)

// wasmEntry 是 manifest 里 wasm 关心的那几个字段。
//
// 为什么不把 digest 加到 kernel.ModuleEntry：kernel 的红线是"不识业务"，
// digest 是 **wasm 文件内容**的身份，属于装载层；kernel 只需要 id/backend，
// 不需要知道这条路径后面躺着一个 .wasm。
//
// 代价是同一份 manifest 在这一层再解析一遍——只解析 [[module]] 行，
// 读的是同一份字节，不存在"两份真相"。
type wasmEntry struct {
	ID       string   `toml:"id"`
	Backend  string   `toml:"backend"`
	Path     string   `toml:"path"`
	Digest   string   `toml:"digest"`
	Enabled  *bool    `toml:"enabled"`
	Provides []string `toml:"provides"`
}

type wasmManifest struct {
	Modules []wasmEntry `toml:"module"`
}

// readWasmEntries 只把 manifest 里的 [[module]] 行读出来。文件不存在 = 没有 wasm 模块。
func readWasmEntries(manifestPath string) ([]wasmEntry, error) {
	if strings.TrimSpace(manifestPath) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 没有 manifest 不是错误（与 kernel.LoadManifest 一致）
		}
		return nil, fmt.Errorf("读 manifest %s 失败: %w", manifestPath, err)
	}
	var f wasmManifest
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析 manifest %s 的 wasm 段失败: %w", manifestPath, err)
	}
	return f.Modules, nil
}

// loadWasmModules 装载并校验 manifest 里所有 backend = "wasm" 的模块，
// 返回要追加进候选列表的 kernel.Module（Provides 为空、Register 为 nil）。
//
// 返回的模块必须在 Apply **之前**并进候选：Apply 按 id 命中，
// 不在候选里的 id 会被判成"没有编译进二进制"——那正是这次要拆掉的那堵墙。
func loadWasmModules(manifestPath string) ([]kernel.Module, error) {
	entries, err := readWasmEntries(manifestPath)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		replaceWasm(nil)
		return nil, nil
	}
	base := manifestDirOf(manifestPath)
	ctx := context.Background()

	var (
		mods   []kernel.Module
		loaded []wasmLoaded
	)
	// fail 统一收尾：装到一半失败的模块也要关掉，不留 runtime、不留文件句柄。
	fail := func(err error) ([]kernel.Module, error) {
		closeWasmModules(loaded)
		return nil, err
	}

	for _, e := range entries {
		if kernel.Backend(e.Backend) != kernel.BackendWasm {
			continue
		}
		if e.ID == "" {
			// 缺 id 的报错归 Apply，这里不抢它的台词。
			continue
		}
		// enabled = false 就是真的关掉：连文件都不读。
		// 一个被禁用的模块不该因为路径坏了把整个启动拖下水。
		//
		// 但 id 仍要登记进候选：Apply 是"先按 id 找模块、再判断 enabled"，
		// 不登记的话它会把"我写了这行、这次关掉"报成"没编译进二进制"。
		if e.Enabled != nil && !*e.Enabled {
			mods = append(mods, kernel.Module{ID: e.ID, Backend: kernel.BackendWasm})
			continue
		}
		if len(e.Provides) > 0 {
			return fail(fmt.Errorf(
				"模块 %q 声明了 backend = \"wasm\" 和 provides = %v，但 wasm 后端现在**不提供任何能力**："+
					"这一层只做装载与校验，不把 wasm 函数映射成工具。"+
					"在工具契约映射落地之前写 provides，就是在骗下一个读 manifest 的人",
				e.ID, e.Provides))
		}

		p := strings.TrimSpace(e.Path)
		if p == "" {
			return fail(fmt.Errorf(
				"模块 %q 声明了 backend = \"wasm\"，但没有 path：wasm 后端必须给出 .wasm 文件路径", e.ID))
		}
		if !filepath.IsAbs(p) {
			// 相对路径按 manifest 所在目录解析，不是按当前工作目录：
			// 否则同一条命令换个 cwd 跑，指到的就是另一个文件。
			p = filepath.Join(base, p)
		}
		// 再取绝对路径：报错和 `modules graph` 里都能一眼看出到底读了哪个文件，
		// 而不是一个要靠 cwd 去补全的相对路径。
		if abs, aerr := filepath.Abs(p); aerr == nil {
			p = abs
		}

		m, err := wasmhost.Load(ctx, e.ID, p)
		if err != nil {
			return fail(fmt.Errorf("装配 wasm 模块 %q 失败（%s）: %w", e.ID, p, err))
		}
		actual := m.Digest()
		exports := m.Exports()
		// 先登记再校验：任何一处校验失败，fail 都会把它一起关掉。
		loaded = append(loaded, wasmLoaded{id: e.ID, path: p, digest: actual, exports: exports, module: m})

		if want := strings.ToLower(strings.TrimSpace(e.Digest)); want != "" {
			if len(want) != 64 {
				return fail(fmt.Errorf(
					"模块 %q 的 digest 不是 64 位十六进制 sha256（写的是 %q，长度 %d）",
					e.ID, e.Digest, len(want)))
			}
			if _, herr := hex.DecodeString(want); herr != nil {
				return fail(fmt.Errorf(
					"模块 %q 的 digest 不是十六进制（写的是 %q）: %v", e.ID, e.Digest, herr))
			}
			if want != actual {
				return fail(fmt.Errorf(
					"模块 %q 的 digest 不匹配（摘要校验失败）：manifest 写的是 %s，文件 %s 实际的 sha256 是 %s。"+
						"内容被换过就该在启动时炸，而不是等第一次调用才发现",
					e.ID, want, p, actual))
			}
		}

		mods = append(mods, kernel.Module{
			ID:      e.ID,
			Backend: kernel.BackendWasm,
			// Provides 必须为空：它不提供能力这件事要在**装配结果里**就是真的，
			// 不能只靠日志里一句话。
		})

		slog.Warn("wasm 模块已装载并通过校验，但**不提供任何能力**：不进工具表、模型调不到，只能 `dshd wasm call` 手动调",
			"module", e.ID, "path", p, "digest", shortDigest(actual),
			"exports", strings.Join(exports, ","))
	}

	replaceWasm(loaded)
	return mods, nil
}

// replaceWasm 换掉"当前装配里的 wasm 模块"，并关掉上一批。
//
// 为什么敢直接关旧的：这些句柄没有任何出口——模块 Provides 为空、没有 Register，
// 拿不到模块指针的调用方不可能还在用它。
func replaceWasm(next []wasmLoaded) {
	wasmMu.Lock()
	old := wasmCurrent
	wasmCurrent = next
	wasmMu.Unlock()
	closeWasmModules(old)
}

// closeLoadedWasm 卸载本次装配装起来的全部 wasm 模块。可重复调用。
//
// 谁该调：打印完就退出的命令（`modules graph|list`）。进程活着的命令
// （serve / acp / run）不调——模块本来就该活到进程结束。
func closeLoadedWasm(ctx context.Context) {
	wasmMu.Lock()
	cur := wasmCurrent
	wasmCurrent = nil
	wasmMu.Unlock()
	closeWasmModulesWith(ctx, cur)
}

func closeWasmModules(list []wasmLoaded) {
	closeWasmModulesWith(context.Background(), list)
}

func closeWasmModulesWith(ctx context.Context, list []wasmLoaded) {
	for _, x := range list {
		if x.module == nil {
			continue
		}
		if err := x.module.Close(ctx); err != nil {
			slog.Debug("关闭 wasm 模块失败", "module", x.id, "err", err)
		}
	}
}

// loadedWasm 返回本次装配里装起来的 wasm 模块（按 manifest 顺序）。
func loadedWasm() []wasmLoaded {
	wasmMu.Lock()
	defer wasmMu.Unlock()
	out := make([]wasmLoaded, len(wasmCurrent))
	copy(out, wasmCurrent)
	return out
}

// writeWasmSection 把 wasm 模块单独打一节：backend 与 digest 前 12 位。
//
// 为什么单独一节而不是塞进主表：主表的列是"装配后能干什么"（PROVIDES），
// 而它们的 Provides 是**空的**——混进主表，读的人会以为它们提供了能力。
func writeWasmSection(w io.Writer) {
	list := loadedWasm()
	if len(list) == 0 {
		return
	}
	fmt.Fprintf(w, "\n# wasm 后端（已装载 / 已校验 / **不提供任何能力**：不进工具表，只能 `dshd wasm call` 手动调）\n")
	fmt.Fprintf(w, "%-22s %-9s %-12s %-18s %s\n", "MODULE", "BACKEND", "DIGEST", "EXPORTS", "PATH")
	for _, x := range list {
		exports := strings.Join(x.exports, ",")
		if exports == "" {
			exports = "（无导出函数）"
		}
		fmt.Fprintf(w, "%-22s %-9s %-12s %-18s %s\n",
			x.id, string(kernel.BackendWasm), shortDigest(x.digest), exports, x.path)
	}
}

// shortDigest 取 sha256 的前 12 位十六进制：够认出一个文件，又不至于把行撑爆。
func shortDigest(d string) string {
	if len(d) <= 12 {
		return d
	}
	return d[:12]
}

// manifestDirOf 是 manifest 里相对 path 的解析基准目录。
func manifestDirOf(manifestPath string) string {
	if strings.TrimSpace(manifestPath) == "" {
		return "."
	}
	d := filepath.Dir(manifestPath)
	if d == "" {
		return "."
	}
	return d
}
