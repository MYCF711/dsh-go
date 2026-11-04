package main

// --script 的解析与"选项 → 键"的表。
//
// 这两样东西错了，错的不是"探测脚本"，而是**通过与否**：
// approve 拼错会被静默忽略（审批没人答，最后按超时拒绝），
// 而 y/a 弄反就是"点了允许却什么都没执行"——正是 h13 要抓的那种错。

import (
	"os"
	"path/filepath"
	"testing"
)

func writePlan(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadPlanAcceptsRoundTrip(t *testing.T) {
	got, err := readPlan(writePlan(t, `{
	  "size": [100, 40],
	  "turnTimeoutMs": 1000,
	  "steps": [
	    {"kind": "send", "text": "第一轮", "label": "t1"},
	    {"kind": "send", "text": "第三轮", "label": "t3", "approve": "allow_once"},
	    {"kind": "key", "key": "ctrl+t", "label": "t3-expanded"},
	    {"kind": "snapshot", "label": "final"}
	  ]}`))
	if err != nil {
		t.Fatalf("合法脚本被拒: %v", err)
	}
	if len(got.Steps) != 4 || got.Steps[1].Approve != "allow_once" {
		t.Fatalf("解析结果不对: %+v", got)
	}
	if got.Size[0] != 100 || got.TurnTimeoutMs != 1000 {
		t.Fatalf("尺寸/超时没解析出来: %+v", got)
	}
}

// 选项到键的映射只有一个来源；弄反的后果是"允许"变成"拒绝"。
func TestApprovalKeyMapping(t *testing.T) {
	want := map[string]rune{"allow_once": 'y', "allow_always": 'a', "reject_once": 'n'}
	for opt, key := range want {
		if got, ok := approvalKeys[opt]; !ok || got != key {
			t.Fatalf("%s 应映射到 %q，实际 %q（存在=%v）", opt, key, got, ok)
		}
	}
}

// 拼错的 approve 必须当场报错：静默忽略的话，这一轮没人答审批，
// 最后按超时拒绝，而失败信息看起来像"工具没执行"。
func TestReadPlanRejectsBadSteps(t *testing.T) {
	cases := map[string]string{
		"没 text":     `{"steps":[{"kind":"send"}]}`,
		"text 带换行":   `{"steps":[{"kind":"send","text":"第一行\n第二行"}]}`,
		"approve 拼错": `{"steps":[{"kind":"send","text":"x","approve":"allow_onece"}]}`,
		"kind 不认识":   `{"steps":[{"kind":"type","text":"x"}]}`,
		"key 不认识":    `{"steps":[{"kind":"key","key":"ctrl+z"}]}`,
		"没有 steps":   `{"steps":[]}`,
	}
	for name, body := range cases {
		if _, err := readPlan(writePlan(t, body)); err == nil {
			t.Fatalf("%s：应该报错却通过了", name)
		}
	}
}
