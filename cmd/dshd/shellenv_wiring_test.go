package main

// 端到端：走**生产同一条装配路径**（`builtinModules`）之后，模型可见的 shell 工具
// 真的把受管 `DSH_*` 注入了子进程。
//
// 为什么必须有这一层：`internal/shellenv` 与两个工具包的单元测试用的都是 stub，
// 它们只能证明"拿到能力时会注入"。能力到底有没有被装进模块图、工具能不能从
// 作用域里 inject 到它，只有真装配才能回答——"能力存在、生产没接"是本仓最容易
// 交付假绿的形状（见 checkpoint_dispatch_test.go 的同一条理由）。

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// shellEnvWiringTool 装配 builtinModules 并按名字取一个工具。
func shellEnvWiringTool(t *testing.T, wd, home, name string) spi.Tool {
	t.Helper()
	k := kernel.New("shellenv-wiring")
	root := k.Root()
	t.Cleanup(root.Dispose)
	if _, err := kernel.Assemble(root, builtinModules(wd, home, 0, 0, nil)); err != nil {
		t.Fatalf("装配 builtinModules: %v", err)
	}
	if _, err := kernel.Inject(root, spi.ShellEnvKey); err != nil {
		t.Fatalf("装配后没有 shellEnv 能力: %v", err)
	}
	reg, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		t.Fatalf("注入 ToolsKey: %v", err)
	}
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("装配后没有 %s 工具", name)
	}
	return tool
}

// TestShellEnvListedInRepoManifest 钉住"从仓根启动的生产路径真的会装配它"。
//
// dsh.toml 是**白名单**：没列出来的模块会被 `Manifest.Apply` 静默跳过（只打一行
// Warn，`Assemble` 不报错）。所以"模块写进了 builtinModules"还不够——漏一行白名单，
// 受管 DSH_* 在生产上就永远不注入，且没有任何错误。
func TestShellEnvListedInRepoManifest(t *testing.T) {
	path := filepath.Join("..", "..", "dsh.toml")
	m, err := kernel.LoadManifest(path)
	if err != nil {
		t.Fatalf("读仓根 dsh.toml: %v", err)
	}
	if !m.Present {
		t.Skip("仓根没有 dsh.toml（manifest 缺席 = 默认全开）")
	}
	if len(m.Modules) == 0 {
		t.Skip("dsh.toml 没有模块白名单（= 全开）")
	}
	for _, e := range m.Modules {
		if e.ID == "shell-env" {
			return
		}
	}
	t.Fatal("dsh.toml 白名单里没有 shell-env：生产装配会静默跳过它，受管 DSH_* 不会注入")
}

func TestShellEnvWiredIntoBashTool(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("本机没有 bash：", err)
	}
	// 继承环境里先放一个 DSH_*：它必须被子进程丢掉。
	t.Setenv("DSH_INHERITED", "from-host")
	wd, home := t.TempDir(), t.TempDir()
	tool := shellEnvWiringTool(t, wd, home, "bash")

	raw := json.RawMessage(`{"command":"env | grep '^DSH' | sort","description":"列受管环境"}`)
	res, err := tool.Exec(spi.WithSession(context.Background(), "sess-wiring"), raw)
	if err != nil {
		t.Fatalf("bash 工具执行失败: %v（输出 %s）", err, res.Text)
	}
	// DSH_HOME 必须是宿主传下来的那个 home——不是继承环境里的，也不是编的。
	for _, want := range []string{
		"DSH_HOME=" + home,
		"DSH_SHELL=1",
		"DSH_SESSION_ID=sess-wiring",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("子进程环境里没有 %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "DSH_INHERITED") {
		t.Fatalf("继承来的 DSH_* 没被清掉:\n%s", res.Text)
	}
}

func TestShellEnvWiredIntoPwshTool(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		if _, legacyErr := exec.LookPath("powershell"); legacyErr != nil {
			t.Skip("本机没有 pwsh/powershell：", err)
		}
	}
	t.Setenv("DSH_INHERITED", "from-host")
	wd, home := t.TempDir(), t.TempDir()
	tool := shellEnvWiringTool(t, wd, home, "pwsh")

	command := "Get-ChildItem env: | Where-Object { $_.Name -like 'DSH*' } | ForEach-Object { $_.Name + '=' + $_.Value }"
	raw, err := json.Marshal(map[string]string{"description": "列受管环境", "command": command})
	if err != nil {
		t.Fatalf("构造参数: %v", err)
	}
	res, err := tool.Exec(spi.WithSession(context.Background(), "sess-wiring"), raw)
	if err != nil {
		t.Fatalf("pwsh 工具执行失败: %v（输出 %s）", err, res.Text)
	}
	for _, want := range []string{
		"DSH_HOME=" + home,
		"DSH_SHELL=1",
		"DSH_SESSION_ID=sess-wiring",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("子进程环境里没有 %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "DSH_INHERITED") {
		t.Fatalf("继承来的 DSH_* 没被清掉:\n%s", res.Text)
	}
}
