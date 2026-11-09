package main

// GO.2 T04：agent 侧宿主增量（agentPresets.list 真名册 / attachWait claim 语义）。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// TestAgentPresetsListHostReadsRealRoster：list 的真值是 agentPresetRoster()
// （内置 + `<home>/modes/*.toml`），isDefault 只落在真实回落值上。
func TestAgentPresetsListHostReadsRealRoster(t *testing.T) {
	f := newAgentAPIFixture(t)
	dir := filepath.Join(f.home, "modes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t04.toml"),
		[]byte("id = \"t04-mode\"\nname = \"T04\"\ndescription = \"user preset\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roster, err := f.api.AgentPresetsList()
	if err != nil {
		t.Fatalf("AgentPresetsList: %v", err)
	}
	seen := map[string]bool{}
	defaults := 0
	for _, preset := range roster.Presets {
		if seen[preset.ID] {
			t.Fatalf("名册里 id 重复：%s", preset.ID)
		}
		seen[preset.ID] = true
		if preset.IsDefault {
			defaults++
			if preset.ID != modes.DefaultID {
				t.Fatalf("isDefault 只能落在回落 preset %q，实际 %q", modes.DefaultID, preset.ID)
			}
		}
	}
	if !seen["t04-mode"] {
		t.Fatalf("用户 TOML 没进名册：%+v", roster.Presets)
	}
	if !seen[modes.DefaultID] || defaults != 1 {
		t.Fatalf("默认 preset 必须唯一且存在：%+v", roster.Presets)
	}
	// 坏名册必须响亮失败，不许静默丢文件编一个"残缺但成功"的 list。
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("name = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.AgentPresetsList(); err == nil {
		t.Fatal("缺 id 的 TOML 必须让 list 失败")
	}
}

// TestQuestionAttachWaitClaimPausesSettlement：官方 claim 语义——流持有期间宿主
// 不走 deadline 结算；释放 claim 后等待仍在（关流不等于结算问题）。
func TestQuestionAttachWaitClaimPausesSettlement(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", func(lg *session.Log) {
		if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, "call-claim", "ask_user_question", `{"questions":[]}`)); err != nil {
			t.Fatalf("追加 tool/call: %v", err)
		}
		if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, "call-claim", nil, false, "", nil)); err != nil {
			t.Fatalf("追加 tool/result: %v", err)
		}
	})
	now := time.Now()
	f.core.questionWaits = transport.NewOfficialQuestionWaits(transport.OfficialQuestionWaitOptions{
		Timeout: 10 * time.Second,
		Now:     func() time.Time { return now },
	})
	first, err := f.api.UserQuestionsAttachWait(sessionID, "call-claim")
	if err != nil || first == nil {
		t.Fatalf("attachWait 建等待：%v %+v", err, first)
	}

	// 流持有 claim：把时钟拨过 deadline，再探一次——不 live，但**不结算**。
	f.api.UserQuestionsClaimed(sessionID, "call-claim", true)
	now = now.Add(60 * time.Second)
	if wait, err := f.api.UserQuestionsAttachWait(sessionID, "call-claim"); err != nil || wait != nil {
		t.Fatalf("持有期间超过 deadline 不该再发帧：%v %+v", err, wait)
	}
	f.api.UserQuestionsClaimed(sessionID, "call-claim", false)

	// 释放 claim 后把时钟拨回窗口内：等待必须还在（流没有把它结算掉）。
	now = now.Add(-59 * time.Second)
	wait, err := f.api.UserQuestionsAttachWait(sessionID, "call-claim")
	if err != nil || wait == nil || wait.RemainingMs <= 0 {
		t.Fatalf("关流后等待必须仍在（答案仍可投递）：%v %+v", err, wait)
	}
}
