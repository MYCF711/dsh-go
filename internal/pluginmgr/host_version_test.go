package pluginmgr

// 库的测试环境必须与真实二进制**一致**：宿主在启动时把 `main.version` 注入进来
// （`cmd/dshd/host_version.go`），测试进程不走 `main()`，所以在这里做同一件事。
//
// 注入的值从**唯一出处** `cmd/dshd/main.go` 现读（`versiontag.FromMainSource`），
// 而不是在测试里再抄一份版本号——抄一份就等于把"第二个版本源"搬进了测试，
// 防复发守卫（`cmd/dshd/version_guard_test.go`）要拦的正是这种形态。
//
// 读不到就直接失败退出：让整包测试在"没有 runtime 版本"的环境里跑，等于测一个
// 真实宿主不会出现的状态，比不跑更糟。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/versiontag"
)

func TestMain(m *testing.M) {
	path := filepath.Join("..", "..", "cmd", "dshd", "main.go")
	version, err := versiontag.FromMainSource(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pluginmgr 测试装配：读不到版本唯一出处 %s：%v\n", path, err)
		os.Exit(1)
	}
	SetHostRuntimeVersion(version)
	os.Exit(m.Run())
}

// TestCompatibilityNeedsVersionOnlyWhenThereAreRequirements 钉住 fail-closed 的**边界**：
//
//   - 没有任何 `engines` / dsh peer 要求的包：一律放行，**不需要** runtime 版本
//     （官方口径；也保证"宿主还没来得及注入"不会把普通插件挡在门外）；
//   - 有要求的包 + 宿主没注入版本：fail-closed 返回 ErrHostRuntimeVersionUnknown，
//     而不是拿一个假版本去判（那正是本任务要消灭的"宿主对自己版本说谎"）。
func TestCompatibilityNeedsVersionOnlyWhenThereAreRequirements(t *testing.T) {
	saved := HostRuntimeVersion
	defer SetHostRuntimeVersion(saved)

	plain := Manifest{Schema: ManifestSchema, ID: "acme.plain", Version: "1.0.0",
		PeerDependencies: map[string]string{"react": "^18.0.0"}}
	needs := Manifest{Schema: ManifestSchema, ID: "acme.needs", Version: "1.0.0",
		Engines: &Engines{DSH: "^0.2.0"}}

	HostRuntimeVersion = ""
	if issue, err := EvaluateCompatibility(plain, nil, HostRuntimeVersion); err != nil || issue != nil {
		t.Fatalf("没有要求的包必须放行（不依赖宿主版本）：issue=%+v err=%v", issue, err)
	}
	if _, err := EvaluateCompatibility(needs, nil, HostRuntimeVersion); !errors.Is(err, ErrHostRuntimeVersionUnknown) {
		t.Fatalf("有要求但宿主未注入版本时必须 fail-closed，得到 %v", err)
	}

	HostRuntimeVersion = saved
	if issue, err := EvaluateCompatibility(needs, nil, HostRuntimeVersion); err != nil || issue != nil {
		t.Fatalf("注入 %q 之后 ^0.2.0 必须判兼容：issue=%+v err=%v", saved, issue, err)
	}
}
