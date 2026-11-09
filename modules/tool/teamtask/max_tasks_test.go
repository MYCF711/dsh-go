package teamtask

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"dshgo/internal/spi"
)

// GO.7 / t11 #24：官方 `agentTeam.maxTasks`（默认 256）——团队任务板的条数上限。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-experimental-agent-team/lib/index.js）：
//
//	:1443  maxTasks: z.number().step(1).min(1).default(DEFAULT_MAX_TASKS)   // 256
//	:1458  maxTasks: positiveLimit("maxTasks", config.maxTasks ?? DEFAULT_MAX_TASKS)
//	:1224  if (state.tasks.filter((task) => task.status !== "deleted").length >= this.maxTasks)
//	         throw new TeamError(`Team task limit ${this.maxTasks} reached`, "TEAM_TASK_LIMIT");
//
// 宿主的任务板是单个 team-tasks.json，本轮之前**没有任何条数闸**：表可无限增长
// （内存 + 文件），第 257 条照样建得出来。下面三条先红后绿。

// 默认值必须与官方一致：256（不是 200、不是 1000）。
func TestTaskBoardDefaultMaxTasksIs256(t *testing.T) {
	b := &board{dir: t.TempDir()}
	if got := b.limit(); got != defaultMaxTasks {
		t.Fatalf("默认条数上限 = %d，期望官方默认 %d", got, defaultMaxTasks)
	}
	if defaultMaxTasks != 256 {
		t.Fatalf("官方默认 maxTasks = 256，当前常量 = %d", defaultMaxTasks)
	}
}

// 第 257 条被拒：拒绝要有结构化 Hint（说清上限与怎么办），且**不落盘**。
func TestTaskBoardRejects257thTask(t *testing.T) {
	dir := t.TempDir()
	ctx := spi.WithSession(context.Background(), "session-board")

	for i := 1; i <= defaultMaxTasks; i++ {
		if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(
			fmt.Sprintf(`{"subject":"任务 %03d","description":"填充板子"}`, i))); err != nil {
			t.Fatalf("第 %d 条应当建得出来: %v", i, err)
		}
	}

	res, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{"subject":"第 257 条","description":"应当被拒"}`))
	if err == nil {
		t.Fatalf("第 %d 条必须被拒，实际建成了: %#v", defaultMaxTasks+1, res.Data)
	}
	if !strings.Contains(res.Hint, "256") {
		t.Fatalf("Hint 要点名上限 256，实际 %q", res.Hint)
	}
	if !strings.Contains(res.Hint, "team_task") {
		t.Fatalf("Hint 要指向任务板工具（怎么腾位置），实际 %q", res.Hint)
	}

	// 拒绝必须是"没写进去"：板上仍是 256 条。
	var f taskFile
	raw, readErr := readFileRaw(dir)
	if readErr != nil {
		t.Fatalf("回读任务板: %v", readErr)
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("任务板不是合法 JSON: %v", err)
	}
	if len(f.Tasks) != defaultMaxTasks {
		t.Fatalf("被拒后板上应是 %d 条，实际 %d 条", defaultMaxTasks, len(f.Tasks))
	}

	// 腾一条位置（完成 + 删除）之后必须又能建：上限不是"终身额度"。
	id := f.Tasks[0].ID
	if _, err := mk(dir, kindUpdate).Exec(ctx, json.RawMessage(
		`{"task_id":"`+id+`","action":"delete","force":true}`)); err != nil {
		t.Fatalf("删掉一条腾位置: %v", err)
	}
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{"subject":"补位","description":"现在有位置了"}`)); err != nil {
		t.Fatalf("腾出位置后应当能再建: %v", err)
	}
}

// 上限可配置（官方 maxTasks 是配置项），非法值 fail-loud 而不是静默退回默认。
func TestTaskBoardMaxTasksConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if got, err := maxTasksFromEnv(env(nil)); err != nil || got != defaultMaxTasks {
		t.Fatalf("未设置 = 官方默认 %d，得到 (%d, %v)", defaultMaxTasks, got, err)
	}
	if got, err := maxTasksFromEnv(env(map[string]string{envMaxTasks: " 512 "})); err != nil || got != 512 {
		t.Fatalf("%s=512 应生效，得到 (%d, %v)", envMaxTasks, got, err)
	}
	for _, bad := range []string{"0", "-1", "abc", "2.5"} {
		if _, err := maxTasksFromEnv(env(map[string]string{envMaxTasks: bad})); err == nil {
			t.Fatalf("%s=%q 是非法上限，必须 fail-loud", envMaxTasks, bad)
		}
	}

	// 小上限也要真的生效（不是只认 256 这一个数）。
	dir := t.TempDir()
	b := &board{dir: dir, maxTasks: 1}
	ctx := spi.WithSession(context.Background(), "session-small")
	tool := &Tool{b: b, kind: kindCreate}
	if _, err := tool.Exec(ctx, json.RawMessage(`{"subject":"第一条","description":"占满"}`)); err != nil {
		t.Fatalf("上限 1 时第一条应成功: %v", err)
	}
	if _, err := tool.Exec(ctx, json.RawMessage(`{"subject":"第二条","description":"应被拒"}`)); err == nil {
		t.Fatal("上限 1 时第二条必须被拒")
	}
}

// readFileRaw 回读任务板原文（测试只读，不走 board.load 的裁剪逻辑）。
func readFileRaw(dir string) ([]byte, error) {
	b := &board{dir: dir}
	return os.ReadFile(b.path())
}
