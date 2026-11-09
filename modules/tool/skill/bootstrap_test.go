package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 官方 escapeAttr / escapeText：名字走属性转义，正文原样嵌入。
func TestChatMLEscaping(t *testing.T) {
	if got := escapeAttr(`a&b"c<d`); got != `a&amp;b&quot;c&lt;d` {
		t.Fatalf("escapeAttr = %q", got)
	}
	if got := escapeText(`a&b<c>d`); got != `a&amp;b&lt;c&gt;d` {
		t.Fatalf("escapeText = %q", got)
	}
}

// 空 home 自举：skills 根目录必须被建出来，否则干净 home 上这个工具等于不可用。
func TestSkillBootstrapCreatesEmptyHomeRoot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "skills")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("前置：skills 目录还不该存在（%v）", err)
	}
	if err := Bootstrap(root); err != nil {
		t.Fatalf("空 home 自举失败：%v", err)
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		t.Fatalf("自举后 skills 不是目录：%v", err)
	}
	// 幂等：每次启动都会调一遍
	if err := Bootstrap(root); err != nil {
		t.Fatalf("重复自举必须幂等：%v", err)
	}
}

// 自举种子：DSHD_SKILLS_BUNDLED 指到官方自带 skill 目录时，把缺的 skill 复制进来；
// 已经存在（用户改过）的同名 skill 一律不覆盖。
func TestSkillBootstrapSeedsBundledSkills(t *testing.T) {
	bundled := t.TempDir()
	writeSkill(t, bundled, "agent-experience", "---\nname: agent-experience\ndescription: 经验\n---\n官方正文\n")
	if err := os.WriteFile(filepath.Join(bundled, "agent-experience", "references.md"), []byte("参考"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSHD_SKILLS_BUNDLED", bundled)

	home := t.TempDir()
	root := filepath.Join(home, "skills")
	if err := Bootstrap(root); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "agent-experience", "SKILL.md"))
	if err != nil {
		t.Fatalf("种子没复制 SKILL.md：%v", err)
	}
	if !strings.Contains(string(body), "官方正文") {
		t.Fatalf("种子内容不对：%s", body)
	}
	if _, err := os.Stat(filepath.Join(root, "agent-experience", "references.md")); err != nil {
		t.Fatalf("种子必须整树复制（references.md 丢了）：%v", err)
	}

	// 用户版本存在时不许覆盖
	if err := os.WriteFile(filepath.Join(root, "agent-experience", "SKILL.md"), []byte("用户改过的\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Bootstrap(root); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(filepath.Join(root, "agent-experience", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "用户改过的\n" {
		t.Fatalf("自举覆盖了用户版本：%s", body)
	}
}

// 自举只落在显式给的 root 里：绝不写真实用户 home。
func TestSkillBootstrapNeverWritesRealUserHome(t *testing.T) {
	trap := t.TempDir()
	t.Setenv("HOME", trap)
	t.Setenv("USERPROFILE", trap)
	t.Setenv("DSHD_SKILLS_BUNDLED", "")

	root := filepath.Join(t.TempDir(), "skills")
	if err := Bootstrap(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(trap)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("自举写进了真实用户 home 的替身目录：%v", names)
	}
}
