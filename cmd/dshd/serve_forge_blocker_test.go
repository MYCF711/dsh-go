package main

// forgePanel 的 `worktree.blocker` 三条探测（官方 `panel.ts:319-326`）。
//
// 官方的枚举全集只有三个词 + null（`types.ts:80-87`、`lib/typert.host.js:516`），
// 判定顺序是 "not-git 先于档位，档位先于路径"（详见 serve_forge_state.go 的
// blockerOf 注释）。这组测试钉三件事：
//  1. 三个词各自都能真触发；
//  2. 官方允许 null 的两种情形（能写 / 拿不到事实）确实是 null，不是编出来的词；
//  3. **多条同时成立时取官方那一条** —— not-git 压过 read-only，档位压过路径。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
	"dshgo/internal/worktree"
)

// blockerFakeGit 按参数回放 git 事实。
//
// 用假 git 而不是真仓库：这里要能精确造出"不是 checkout"、"探不到公共 .git"、
// "git 根本跑不起来"这三种官方会分成不同分支的错误。真 git 的冒烟在
// TestForgePanelBlockerRealGit 里另跑一遍。
type blockerFakeGit struct {
	// root 是 `rev-parse --show-toplevel` 的输出；空串 = 不是 checkout。
	root string
	// commonDir 是 `rev-parse --git-common-dir` 的输出；空串 = 探不到（官方 throw）。
	commonDir string
	// stderr 是 `--show-toplevel` 的失败文本（root 为空时用）。
	stderr string
}

func (g *blockerFakeGit) Run(_ context.Context, _ string, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "--show-toplevel"):
		if g.root == "" {
			return "", errors.New(g.stderr)
		}
		return g.root + "\n", nil
	case strings.Contains(joined, "--git-common-dir"):
		if g.commonDir == "" {
			return "", errors.New("fatal: 读不到公共 .git 目录")
		}
		return g.commonDir + "\n", nil
	case strings.HasPrefix(joined, "symbolic-ref"):
		return "main\n", nil
	case strings.HasPrefix(joined, "rev-parse HEAD"):
		return "deadbeef\n", nil
	}
	return "", nil
}

// newBlockerHost 造一个只关心 blocker 的宿主：不挂 worktree 服务（官方 blocker
// 与它无关），目录固定为 dir，档位来自 mode，策略根来自 workspaceRoot。
func newBlockerHost(t *testing.T, git worktree.Git, dir string, mode func(string) (sandbox.Mode, error), workspaceRoot func() (string, error)) *forgePanelHost {
	t.Helper()
	h, err := newForgePanelHost(t.TempDir(), forgePanelOptions{
		Cwd: func(string) (string, error) { return dir, nil },
		Mode: func(sessionID string) (sandbox.Mode, error) {
			if mode == nil {
				return sandbox.ModeWorkspaceWrite, nil
			}
			return mode(sessionID)
		},
		WorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		t.Fatalf("newForgePanelHost: %v", err)
	}
	h.git = git
	h.ttl = 0
	return h
}

func fixedMode(mode sandbox.Mode) func(string) (sandbox.Mode, error) {
	return func(string) (sandbox.Mode, error) { return mode, nil }
}

func fixedRoot(root string) func() (string, error) {
	return func() (string, error) { return root, nil }
}

// blockerFor 读一次面板状态，返回 blocker 的可打印形态。
func blockerFor(t *testing.T, h *forgePanelHost) string {
	t.Helper()
	state, err := h.state(context.Background(), "s-1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Worktree.Blocker == nil {
		return ""
	}
	return *state.Worktree.Blocker
}

// TestForgePanelBlockerEnumIsOfficial 钉死词表：只有官方的三个词，不许加词。
func TestForgePanelBlockerEnumIsOfficial(t *testing.T) {
	want := map[string]bool{"not-git": true, "outside-writable-root": true, "read-only": true}
	for _, got := range []string{forgeBlockerNotGit, forgeBlockerOutsideWritableRoot, forgeBlockerReadOnly} {
		if !want[got] {
			t.Fatalf("blocker 词 %q 不在官方枚举（types.ts:80-87 / 官方 zod）里", got)
		}
		delete(want, got)
	}
	if len(want) != 0 {
		t.Fatalf("官方枚举还有词没被覆盖：%v", want)
	}
}

// 官方顺序第一条：不是 checkout → not-git，**即使档位是 read-only**。
//
// 依据：panel.ts:274-277 在调用 blockerOf 之前就把 blocker 定成 not-git，
// blockerOf（档位两条在里面）根本不会跑。
func TestForgePanelBlockerNotGitWinsOverSandboxMode(t *testing.T) {
	ws := t.TempDir()
	git := &blockerFakeGit{stderr: "fatal: not a git repository (or any of the parent directories): .git"}
	h := newBlockerHost(t, git, filepath.Join(ws, "plain"), fixedMode(sandbox.ModeReadOnly), fixedRoot(ws))

	if got := blockerFor(t, h); got != forgeBlockerNotGit {
		t.Fatalf("blocker=%q，want %q（not-git 必须先于 read-only）", got, forgeBlockerNotGit)
	}
}

func TestForgePanelBlockerReadOnly(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, "repo")
	git := &blockerFakeGit{root: root, commonDir: filepath.Join(root, ".git")}
	h := newBlockerHost(t, git, root, fixedMode(sandbox.ModeReadOnly), fixedRoot(ws))

	if got := blockerFor(t, h); got != forgeBlockerReadOnly {
		t.Fatalf("blocker=%q，want %q", got, forgeBlockerReadOnly)
	}
}

func TestForgePanelBlockerWorkspaceWriteInsideRoot(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, "repo")
	git := &blockerFakeGit{root: root, commonDir: filepath.Join(root, ".git")}
	h := newBlockerHost(t, git, root, fixedMode(sandbox.ModeWorkspaceWrite), fixedRoot(ws))

	if got := blockerFor(t, h); got != "" {
		t.Fatalf("checkout 与公共 .git 都在可写根内时 blocker 必须是 null，得到 %q", got)
	}
}

func TestForgePanelBlockerOutsideWritableRoot(t *testing.T) {
	ws := t.TempDir()
	other := t.TempDir()
	root := filepath.Join(other, "repo")
	git := &blockerFakeGit{root: root, commonDir: filepath.Join(root, ".git")}
	h := newBlockerHost(t, git, root, fixedMode(sandbox.ModeWorkspaceWrite), fixedRoot(ws))

	if got := blockerFor(t, h); got != forgeBlockerOutsideWritableRoot {
		t.Fatalf("blocker=%q，want %q", got, forgeBlockerOutsideWritableRoot)
	}
}

// 官方第三条是**与**（panel.ts:325）：checkout 根在根内、公共 .git 在根外
// （linked worktree 的典型形态）一样是 blocked。
func TestForgePanelBlockerCommonDirOutsideRoot(t *testing.T) {
	ws := t.TempDir()
	worktreeRoot := filepath.Join(ws, "repo-wt")
	mainGitDir := filepath.Join(t.TempDir(), "main-repo", ".git")
	git := &blockerFakeGit{root: worktreeRoot, commonDir: mainGitDir}
	h := newBlockerHost(t, git, worktreeRoot, fixedMode(sandbox.ModeWorkspaceWrite), fixedRoot(ws))

	if got := blockerFor(t, h); got != forgeBlockerOutsideWritableRoot {
		t.Fatalf("公共 .git 在可写根外时 blocker=%q，want %q", got, forgeBlockerOutsideWritableRoot)
	}
}

// 官方顺序第二条：档位两条命中即 return，**不再比路径**。
func TestForgePanelBlockerDangerFullAccessSkipsPathProbe(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	git := &blockerFakeGit{root: root, commonDir: filepath.Join(root, ".git")}
	h := newBlockerHost(t, git, root, fixedMode(sandbox.ModeDangerFullAccess), fixedRoot(ws))

	if got := blockerFor(t, h); got != "" {
		t.Fatalf("danger-full-access 必须直接 null（不比路径），得到 %q", got)
	}
}

// 官方失败面：git 跑不起来（不是"不是仓库"）→ blocker 停在 null，不是 not-git。
func TestForgePanelBlockerGitFailureIsNull(t *testing.T) {
	ws := t.TempDir()
	git := &blockerFakeGit{stderr: "exec: \"git\": executable file not found in %PATH%"}
	h := newBlockerHost(t, git, filepath.Join(ws, "x"), fixedMode(sandbox.ModeReadOnly), fixedRoot(ws))

	if got := blockerFor(t, h); got != "" {
		t.Fatalf("git 失败时 blocker 必须是 null（官方 throw → null），得到 %q", got)
	}
}

// 档位解析失败 → null（官方 sandboxPolicy.resolve throw 落在 catch 里）。
func TestForgePanelBlockerModeErrorIsNull(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, "repo")
	git := &blockerFakeGit{root: root, commonDir: filepath.Join(root, ".git")}
	h := newBlockerHost(t, git, root, func(string) (sandbox.Mode, error) {
		return "", errors.New("没有 preset 记录")
	}, fixedRoot(ws))

	if got := blockerFor(t, h); got != "" {
		t.Fatalf("档位解析失败时 blocker 必须是 null，得到 %q", got)
	}
}

// 公共 .git 探不到 → null（官方 fs.resolve/git throw → null），不拿 checkout.root 顶替。
func TestForgePanelBlockerCommonDirUnreadableIsNull(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, "repo")
	git := &blockerFakeGit{root: root}
	h := newBlockerHost(t, git, root, fixedMode(sandbox.ModeWorkspaceWrite), fixedRoot(ws))

	if got := blockerFor(t, h); got != "" {
		t.Fatalf("读不到公共 .git 时 blocker 必须是 null，得到 %q", got)
	}
}

// 没有接线（mode / workspaceRoot 为 nil）→ null：这次装配没有真值来源，
// 不许按"默认档"猜一个。
func TestForgePanelBlockerUnwiredIsNull(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, "repo")
	git := &blockerFakeGit{root: root, commonDir: filepath.Join(root, ".git")}

	h, err := newForgePanelHost(t.TempDir(), forgePanelOptions{
		Cwd: func(string) (string, error) { return root, nil },
	})
	if err != nil {
		t.Fatalf("newForgePanelHost: %v", err)
	}
	h.git = git
	h.ttl = 0
	if got := blockerFor(t, h); got != "" {
		t.Fatalf("没有档位/策略根来源时 blocker 必须是 null，得到 %q", got)
	}
}

// 没有工作目录：与 `worktree.Service.repoInfo` 同一判据（worktree.go:1192
// "会话没有工作目录，无法确定源 checkout"）= 不是 checkout = not-git。
// 也保证不会拿宿主进程 cwd 去探（cmd.Dir 空串会让 git 跑在别的目录上）。
func TestForgePanelBlockerNoDirectoryIsNotGit(t *testing.T) {
	git := &blockerFakeGit{root: "", stderr: "fatal: not a git repository"}
	h := newBlockerHost(t, git, "", fixedMode(sandbox.ModeWorkspaceWrite), fixedRoot(t.TempDir()))
	if got := blockerFor(t, h); got != forgeBlockerNotGit {
		t.Fatalf("没有工作目录时 blocker=%q，want %q", got, forgeBlockerNotGit)
	}
}

// 真 git 冒烟：假 git 再怎么像也不是 git。这里用真仓库确认
// `--show-toplevel` / `--git-common-dir` 的真实输出形状能被同一条链路判对。
func TestForgePanelBlockerRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("本机没有 git，跳过真 git 冒烟")
	}
	ctx := context.Background()
	ws := t.TempDir()
	repo := filepath.Join(ws, "repo")
	if err := runGitForTest(ctx, ws, "init", "-q", repo); err != nil {
		t.Skipf("git init 不可用，跳过：%v", err)
	}

	// 1) 仓库在可写根内 → null（workspace-write）。
	h := newBlockerHost(t, worktree.ExecGit{Timeout: 10_000_000_000}, repo, fixedMode(sandbox.ModeWorkspaceWrite), fixedRoot(ws))
	if got := blockerFor(t, h); got != "" {
		t.Fatalf("真仓库在可写根内时 blocker 必须是 null，得到 %q", got)
	}

	// 2) 同一仓库，策略根换成别处 → outside-writable-root。
	elsewhere := t.TempDir()
	h2 := newBlockerHost(t, worktree.ExecGit{Timeout: 10_000_000_000}, repo, fixedMode(sandbox.ModeWorkspaceWrite), fixedRoot(elsewhere))
	if got := blockerFor(t, h2); got != forgeBlockerOutsideWritableRoot {
		t.Fatalf("真仓库在可写根外时 blocker=%q，want %q", got, forgeBlockerOutsideWritableRoot)
	}

	// 3) 真·非仓库目录 → not-git（真 git 的报错文本要能命中官方判据）。
	plain := filepath.Join(ws, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	h3 := newBlockerHost(t, worktree.ExecGit{Timeout: 10_000_000_000}, plain, fixedMode(sandbox.ModeReadOnly), fixedRoot(ws))
	if got := blockerFor(t, h3); got != forgeBlockerNotGit {
		t.Fatalf("真·非仓库目录时 blocker=%q，want %q", got, forgeBlockerNotGit)
	}
}

func runGitForTest(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(string(out) + ": " + err.Error())
	}
	return nil
}

// 线上形状：GUI 侧（`#64`）拿到的必须是官方的词或 `null`，不能是 Go 的枚举名、
// 空串或"缺字段"。三种 blocker 各序列化一次，外加无 blocker 的 `null`。
func TestForgePanelBlockerWireShape(t *testing.T) {
	ws := t.TempDir()
	inside := filepath.Join(ws, "repo")
	outside := filepath.Join(t.TempDir(), "other-repo")
	cases := []struct {
		name string
		git  *blockerFakeGit
		mode sandbox.Mode
		want string // 期望的 JSON 片段
	}{
		{"not-git", &blockerFakeGit{stderr: "fatal: not a git repository"}, sandbox.ModeWorkspaceWrite, `"blocker":"not-git"`},
		{"read-only", &blockerFakeGit{root: inside, commonDir: filepath.Join(inside, ".git")}, sandbox.ModeReadOnly, `"blocker":"read-only"`},
		{"outside-writable-root", &blockerFakeGit{root: outside, commonDir: filepath.Join(outside, ".git")}, sandbox.ModeWorkspaceWrite, `"blocker":"outside-writable-root"`},
		{"可写根内", &blockerFakeGit{root: inside, commonDir: filepath.Join(inside, ".git")}, sandbox.ModeWorkspaceWrite, `"blocker":null`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newBlockerHost(t, c.git, inside, fixedMode(c.mode), fixedRoot(ws))
			state, err := h.state(context.Background(), "s-1")
			if err != nil {
				t.Fatalf("state: %v", err)
			}
			raw, err := json.Marshal(state.Worktree)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(raw), c.want) {
				t.Fatalf("wire 里没有 %s：%s", c.want, raw)
			}
		})
	}
}
