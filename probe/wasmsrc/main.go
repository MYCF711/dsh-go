//go:build wasip1

// 用 GOOS=wasip1 GOARCH=wasm 编译成 wasm，导出 add/mul 供 wazero 调用。
package main

//go:wasmexport add
func add(a, b uint32) uint32 { return a + b }

//go:wasmexport mul
func mul(a, b uint32) uint32 { return a * b }

func main() {}
