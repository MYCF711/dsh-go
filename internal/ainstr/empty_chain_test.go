package ainstr

// 本文件钉"空链"这条边界（官方 README.md:12、:158 "an empty chain adds nothing" /
// "an empty chain contributes zero tokens"）。
//
// 为什么它值得单独一组：durable 层默认开之后，**绝大多数会话**（没有 AGENTS.md 的项目、
// 干净 home）都会走到这条边界。以前它不显眼，因为层是默认关的；一旦默认开，
// "空链也落一条 `<system-reminder>` 空壳帧"就会变成每个会话都多一条无内容帧——
// 它既白涨 token，又让"指令恰好注一次"的计数里混进一条没有指令的帧。
//
// 两个方向都必须钉：
//  1. 空链 + 此前没有基线 ⇒ **不落盘**（空链不贡献任何东西）；
//  2. 空链 + 此前有基线 ⇒ **必须落盘**（替换型基线：模型得知道上一份指令已经全没了）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// emptyProject 造一个带 `.git` 但**没有任何候选文件**的项目树。
func emptyProject(t *testing.T) (root, cwd string) {
	t.Helper()
	root = t.TempDir()
	markRoot(t, root)
	if err := os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "pkg", "sub")
}

func TestComposeSkipsEmptyChain(t *testing.T) {
	_, cwd := emptyProject(t)
	config := defaultConfig(t)
	runner := NewRunner()
	state := FrameState{}

	var committed int
	composed, err := runner.ComposeAndCommit(Options{Config: config, Cwd: cwd, State: state},
		func() (FrameState, error) { return state, nil },
		func(Frame) error { committed++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if composed.Frame != nil {
		t.Fatalf("空链不该产出帧（官方 an empty chain adds nothing），得到:\n%s", composed.Frame.Text)
	}
	if composed.Discard != DiscardNothing {
		t.Fatalf("空链的丢弃原因应是 nothing-changed，得到 %q", composed.Discard)
	}
	if committed != 0 {
		t.Fatalf("空链不该落盘，实际落了 %d 帧", committed)
	}
}

func TestComposeEmitsReplacementWhenBaselineFilesDisappear(t *testing.T) {
	root, cwd := emptyProject(t)
	config := defaultConfig(t)
	// 旧基线来自**不同深度**的 cwd：基线身份是"项目根相对 cwd 的路径"
	// （`workspaceBaselineIdentity.projectRoot`），同深度的兄弟目录身份相同、走增量，
	// 深度不同才会判"身份不等"⇒ 走**替换型**基线分支（这里要钉的正是那一支）。
	previousCwd := root
	if err := os.MkdirAll(previousCwd, 0o755); err != nil {
		t.Fatal(err)
	}
	previousChange := Change{
		Action: ActionSet,
		Scope:  InstructionScopeKey("AGENTS.md"),
		Path:   "AGENTS.md",
		Digest: Digest("root-rules"),
	}
	state := FrameState{
		BaselineOK: true,
		Baseline:   BaselineView{Identity: Identity(previousCwd, root, config), Changes: []Change{previousChange}},
		Visible:    []Change{previousChange},
	}
	composed, err := Compose(Options{Config: config, Cwd: cwd, State: state})
	if err != nil {
		t.Fatal(err)
	}
	if composed.Frame == nil {
		t.Fatal("此前有基线、现在一个文件都没有：必须发替换型基线，否则模型以为旧指令还在生效")
	}
	if !composed.Frame.Source.Baseline || composed.Frame.Source.BaselineIdentity == "" {
		t.Fatalf("替换帧必须仍是基线帧: %+v", composed.Frame.Source)
	}
	var removed bool
	for _, change := range composed.Frame.Source.Changes {
		if change.Action == ActionRemove && change.Scope == previousChange.Scope {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("替换型基线必须显式 remove 旧 scope（官方 replacementRemovals）: %+v", composed.Frame.Source.Changes)
	}
	if strings.TrimSpace(composed.Frame.Text) == "" {
		t.Fatal("替换帧正文不能为空（至少要有官方那句 workspace instruction baseline 说明）")
	}
}
