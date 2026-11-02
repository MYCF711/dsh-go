// sizewazero: 仅 wazero 的体积归因。
package main

import (
	"context"
	"os"

	"github.com/tetratelabs/wazero"
)

func main() {
	if len(os.Args) > 1 {
		ctx := context.Background()
		r := wazero.NewRuntime(ctx)
		defer r.Close(ctx)
		_, _ = r.CompileModule(ctx, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
	}
}
