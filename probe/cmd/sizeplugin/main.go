// sizeplugin: 仅 go-plugin 的体积归因。
package main

import (
	"os"

	"github.com/hashicorp/go-plugin"
)

func main() {
	if len(os.Args) > 1 {
		_ = plugin.ProtocolGRPC
	}
}
