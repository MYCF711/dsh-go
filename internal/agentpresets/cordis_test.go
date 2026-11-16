package agentpresets

import (
	"strings"
	"testing"

	"dshgo/internal/modes"
)

func TestCordisDocumentIsOfficialEntryList(t *testing.T) {
	content, err := Document(modes.Mode{ID: "cordis", Name: "Cordis", Builtin: true})
	if err != nil {
		t.Fatalf("Document(cordis): %v", err)
	}
	if !strings.HasPrefix(content, "- id: persona\n") {
		t.Fatalf("cordis content 必须从官方 persona 行开始，而不是宿主单行投影：\n%s", content)
	}
	for _, want := range []string{
		"name: '@deepseek-ai/dsh-persona'",
		"prefix: >-\n      You are a coding agent powered by the {{model}} model.\n      You help the user customize DeepSeek Harness itself.",
		"name: '@deepseek-ai/dsh-agent-instructions'",
		"maxBytes: 65536",
		"name: '@deepseek-ai/dsh-tool-bash'",
		"disabled: !!js process.platform === 'win32'",
		"name: '@deepseek-ai/dsh-tool-pwsh'",
		"disabled: !!js process.platform !== 'win32'",
		"name: '@deepseek-ai/dsh-tool-fs'",
		"applyPatch: false",
		"name: '@deepseek-ai/dsh-tool-jobs'",
		"name: '@deepseek-ai/dsh-tool-schedule'",
		"name: '@deepseek-ai/dsh-tool-goal'",
		"name: '@deepseek-ai/dsh-plan-mode'",
		"name: '@deepseek-ai/dsh-compaction-basic'",
		"thresholdChars: 8192",
		"name: '@deepseek-ai/dsh-tool-subagent'",
		"toolName: subagent_fork",
		"name: '@deepseek-ai/dsh-workflow-ptc'",
		"name: '@deepseek-ai/dsh-tool-ask-user'",
		"allowParallelInProgress: true",
		"searchTimeoutMs: 60000",
		"name: '@deepseek-ai/dsh-tool-cordis'",
		"name: '@deepseek-ai/dsh-skill-filesystem'",
		"customSkillDirs:",
		"name: '@deepseek-ai/dsh-tool-present'",
		"name: '@deepseek-ai/dsh-plugin-manager/tools'",
		"name: '@deepseek-ai/dsh-plugin-market/tools'",
		"disabled: !!js \"!ctx.get('profileContext')\"",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("cordis 官方 entry-list 缺 %q", want)
		}
	}
	if strings.Contains(content, PresetPluginName) || strings.Contains(content, "dsh-go-preset-") {
		t.Fatalf("cordis 不得拿宿主单行插件冒充官方组合：\n%s", content)
	}
	if !strings.HasSuffix(content, "\n") || strings.HasSuffix(content, "\n\n") {
		t.Fatalf("cordis YAML 结尾必须恰好一个换行：%q", content[len(content)-2:])
	}
}

func TestCordisDocumentSpecialCaseDoesNotChangeDefaultProjection(t *testing.T) {
	content, err := Document(modes.Mode{ID: "default", Name: "Default", Builtin: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(content, "- id: \"dsh-go-preset-default\"\n") ||
		!strings.Contains(content, "name: \"dsh-go/agent-preset\"") ||
		!strings.Contains(content, "tools: []") {
		t.Fatalf("其他 preset 的原投影必须保持不变：\n%s", content)
	}
}
