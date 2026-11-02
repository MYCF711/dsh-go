// size: 只为测量「把 wazero + go-plugin 编进去」的空二进制体积。
package main

import (
	"context"
	"os"

	"github.com/hashicorp/go-plugin"
	"github.com/tetratelabs/wazero"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		ctx := context.Background()
		r := wazero.NewRuntime(ctx)
		defer r.Close(ctx)
		_, _ = r.CompileModule(ctx, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
		_ = plugin.ProtocolGRPC
	}
}
