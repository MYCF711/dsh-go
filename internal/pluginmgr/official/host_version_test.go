package official_test

// 与 `internal/pluginmgr` 的测试装配同理（见 internal/pluginmgr/host_version_test.go）：
// 适配器层的用例会走到真实兼容性判定，所以测试进程必须像真实宿主一样注入 runtime 版本，
// 而注入值从唯一出处 `cmd/dshd/main.go` 现读。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/versiontag"
)

func TestMain(m *testing.M) {
	path := filepath.Join("..", "..", "..", "cmd", "dshd", "main.go")
	version, err := versiontag.FromMainSource(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "official 测试装配：读不到版本唯一出处 %s：%v\n", path, err)
		os.Exit(1)
	}
	pluginmgr.SetHostRuntimeVersion(version)
	os.Exit(m.Run())
}
