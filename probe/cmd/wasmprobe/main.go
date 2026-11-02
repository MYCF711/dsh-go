// wasmprobe: 用 wazero 加载 wasip1 模块并调用导出函数。
// Go 的默认 wasip1 产物是 command 模块（有 _start，跑完就 proc_exit），
// 必须在 wazero 里禁用 start function 才当 reactor 用；c-shared 产物则有 _initialize。
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

func main() {
	bin := "bin/probe.wasm"
	if len(os.Args) > 1 {
		bin = os.Args[1]
	}
	wasm, err := os.ReadFile(bin)
	if err != nil {
		fmt.Println("FAIL read:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	start := time.Now()
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		fmt.Println("FAIL CompileModule:", err)
		os.Exit(1)
	}
	compileMs := time.Since(start).Milliseconds()
	fmt.Print("exports=")
	for _, f := range compiled.ExportedFunctions() {
		fmt.Print(f.Name(), " ")
	}
	fmt.Println()

	// 不自动跑 _start（Go 的 command 模块会在此 proc_exit(0)）。
	mod, err := rt.InstantiateModule(ctx, compiled,
		wazero.NewModuleConfig().WithStartFunctions().WithStdout(os.Stdout))
	if err != nil {
		fmt.Println("FAIL InstantiateModule:", err)
		os.Exit(1)
	}
	instMs := time.Since(start).Milliseconds()

	if initFn := mod.ExportedFunction("_initialize"); initFn != nil {
		if _, err := initFn.Call(ctx); err != nil {
			fmt.Println("FAIL _initialize:", err)
			os.Exit(1)
		}
		fmt.Println("called _initialize (reactor)")
	}

	add := mod.ExportedFunction("add")
	if add == nil {
		fmt.Println("FAIL: add not exported")
		os.Exit(1)
	}
	res, err := add.Call(ctx, api.EncodeU32(40), api.EncodeU32(2))
	if err != nil {
		fmt.Println("FAIL add.Call:", err)
		os.Exit(1)
	}
	fmt.Printf("add(40,2)=%d\n", api.DecodeU32(res[0]))

	if m := mod.ExportedFunction("mul"); m != nil {
		r2, err := m.Call(ctx, api.EncodeU32(6), api.EncodeU32(7))
		if err != nil {
			fmt.Println("FAIL mul.Call:", err)
			os.Exit(1)
		}
		fmt.Printf("mul(6,7)=%d\n", api.DecodeU32(r2[0]))
	}
	fmt.Printf("wasm_bytes=%d compile_ms=%d instantiate_total_ms=%d\n", len(wasm), compileMs, instMs)
}
