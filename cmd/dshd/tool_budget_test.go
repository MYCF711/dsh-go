package main

import (
	"encoding/json"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/session"
)

// CI 七条第 6 条：工具表 ≤ 7000 token（PLAN.md §15 / docs/ci-rules.md 第 6 条）。
//
// 用本仓自己的估算器（ascii/4 + 非 ASCII 每字 1）而不是真分词器：
// 真分词要么联网、要么引入依赖。估算器与实测的偏差记录在 docs/ci-rules.md，
// **它是代理不是证明**——但比"没人管"强一个数量级。
//
// # 6000 → 6400 的依据（不是给自己加功能）
//
// 官方 0.2.2-rc.2 的工具面比 rc.1 净增 7 个工具（`team_task_*`、`office_*` 三件、
// `user_terminal_*` 两件、`schedule_view`/`schedule_edit` 等），每一个都有官方
// catalog 原文依据（`docs/evidence` + `tmp/GO8-RC2/contracts-rc2.json`）。
// "能力下限"规则要求官方已有的能力必须真实补齐，所以上限随**官方工具数**走；
// 我们自己新加的宿主扩展仍然受同一个上限约束（不是给自造工具开口子）。
// # 6400 → 7000 的依据（同一个判据，不是第二次开口子）
//
// 本仓补上了官方 `dsh-tool-terminal` 的**交互五件套**（`terminal_send/read/signal/
// close/list`，`docs/tool-catalog.md:1363-1521`）：那是官方工具面本来就有的能力，
// "能力下限"规则要求真实补齐（开得出来却打不进去，等于终端只能用一次）。
// 五个工具的 schema 与描述逐字来自官方，没有可裁的自撰文字；而本仓工具总数
// （70）仍小于官方 catalog 的工具数（77，`grep -c '^### \`' tool-catalog.md`），
// 所以按 6000→6400 的同一条判据随**官方工具数**走。自撰描述仍受这个上限约束。
//
// 现值：70 个工具 / 25076 字节 / 估算 6648 token（本树实测；上一档 64 个 /
// 22996 字节 / 6131 token 见 `tmp/GO8-RC2/reports/E8-terminal-bridge.md`）。
//
// 再涨仍要先裁自撰描述，而不是继续抬上限（PLAN §15 第 6 条）。
func TestToolTableTokenBudget(t *testing.T) {
	table := buildToolTableMust(t)
	for i := range table {
		table[i].Parameters = agent.CompactSchema(table[i].Parameters)
	}
	raw, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	est := session.EstimateRunes(string(raw))
	t.Logf("工具表 %d 个，%d 字节，估算 %d token（上限 7000）", len(table), len(raw), est)
	if est > 7000 {
		t.Fatalf("工具表估算 %d token，超过 PLAN §15 的 7000；要么砍自撰描述、要么把低频工具合成一个多 action 的",
			est)
	}
}
