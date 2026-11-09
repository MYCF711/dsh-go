package agentpresets

import (
	"strings"
	"testing"

	"dshgo/internal/modes"
)

func TestDocumentIsEntryListYAMLNotTOML(t *testing.T) {
	content, err := Document(modes.Mode{
		ID: "read-only", Name: "Read only", Description: "只读", Builtin: true,
		Tools: []string{"read", "grep"}, Approval: "deny", Sandbox: "read-only",
	})
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if !strings.HasPrefix(content, "- id: ") || !strings.HasPrefix(content, "- ") {
		t.Fatalf("content 不是顶层 entry-list：%q", content)
	}
	for _, forbidden := range []string{"id = ", "tools = ", "[", "builtin = "} {
		if strings.Contains(content, forbidden) && forbidden != "[" {
			t.Fatalf("content 含 TOML 形状 %q：%q", forbidden, content)
		}
	}
	for _, want := range []string{
		"name: \"dsh-go/agent-preset\"",
		"config:",
		"id: \"read-only\"",
		"builtin: true",
		"tools:",
		"- \"read\"",
		"- \"grep\"",
		"approval: \"deny\"",
		"sandbox: \"read-only\"",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("YAML 缺 %q：\n%s", want, content)
		}
	}
	if strings.HasSuffix(content, "\n\n") {
		t.Fatal("content 不应以空行结尾")
	}
}

func TestDocumentDefaultToolsEmpty(t *testing.T) {
	content, err := Document(modes.Mode{ID: "default", Name: "Default", Builtin: true})
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if !strings.Contains(content, "tools: []") {
		t.Fatalf("空工具白名单应写成 tools: []：\n%s", content)
	}
}

func TestDocumentRejectsEmptyID(t *testing.T) {
	if _, err := Document(modes.Mode{}); err == nil {
		t.Fatal("空 preset id 竟然渲染成功")
	}
}
