package ainstr

// 这些测试钉 `Runner` 的 **step 状态机**：`BeginStep` 置位、`EndStep` 收口，
// 以及"step 打开了却永远等不到 EndStep"时的退路 `AbortStep`。
//
// 为什么单独立一个文件：这条状态机的故障不是"某次投影算错"，而是**标志位卡死**——
// stepOpen 停在 true 之后，`Touch` 会一直返回 flush=false（攒着不投），
// 而唯一的 drain 点是下一次成功的 `step/end`。现象是"文件改了、界面不刷新"，
// 没有任何错误日志，看上去像网络慢。所以这里既钉"收口后必须能立刻投影"，
// 也钉"退路收口是幂等的"。

import (
	"reflect"
	"testing"
)

// TestRunnerBeginStepBuffersTouchUntilEndStep 是正常路径：step 开着时攒批，
// `EndStep` 取出并关闭 step，之后 `Touch` 回到"立刻投影"。
func TestRunnerBeginStepBuffersTouchUntilEndStep(t *testing.T) {
	runner := NewRunner()
	runner.BeginStep()
	for _, path := range []string{"b/AGENTS.md", "a/AGENTS.md"} {
		if flush := runner.Touch(path); flush {
			t.Fatalf("step 打开时 Touch(%q) 不该立刻投影", path)
		}
	}
	if !runner.StepOpen() {
		t.Fatal("BeginStep 之后 StepOpen 必须是 true")
	}
	paths := runner.EndStep()
	if want := []string{"a/AGENTS.md", "b/AGENTS.md"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("EndStep 应取出排好序的整批路径，want %v, got %v", want, paths)
	}
	if runner.StepOpen() {
		t.Fatal("EndStep 之后 StepOpen 必须是 false")
	}
	// step 已收口：不属于任何 step 的 touch 必须立刻投影（官方 projectTouch 的
	// step-open 分支之外那一支）。
	if flush := runner.Touch("c/AGENTS.md"); !flush {
		t.Fatal("step 收口之后 Touch 必须回到立刻投影")
	}
}

// TestRunnerAbortStepClosesAbandonedStep 是核心回归：step 打开了却没走 EndStep
// （模型调用失败 / 空响应 / 参数非法 / 取消的全部形状），退路必须复位标志位。
//
// 变异点：把 `AbortStep` 里的 `r.stepOpen = false` 删掉，本测试在
// "StepOpen 必须复位"与"复位后 Touch 要立刻投影"两处都会红。
func TestRunnerAbortStepClosesAbandonedStep(t *testing.T) {
	runner := NewRunner()
	runner.BeginStep()
	runner.Touch("pkg/AGENTS.md")

	paths := runner.AbortStep()
	if want := []string{"pkg/AGENTS.md"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("AbortStep 必须把这批路径交出来（不丢工作），want %v, got %v", want, paths)
	}
	if runner.StepOpen() {
		t.Fatal("AbortStep 之后 stepOpen 必须复位；不复位的表现是本会话之后再也不刷新指令投影")
	}
	if flush := runner.Touch("pkg/AGENTS.md"); !flush {
		t.Fatal("退路收口之后 Touch 必须立刻投影（否则攒批无限增长且永不落盘）")
	}
}

// TestRunnerAbortStepIsIdempotent 钉"可重复调用"：收尾路径可能被多层兜底各调一次。
func TestRunnerAbortStepIsIdempotent(t *testing.T) {
	runner := NewRunner()
	if paths := runner.AbortStep(); paths != nil {
		t.Fatalf("没有打开过 step 时 AbortStep 应是空操作，got %v", paths)
	}
	runner.BeginStep()
	runner.Touch("AGENTS.md")
	if paths := runner.AbortStep(); len(paths) != 1 {
		t.Fatalf("第一次 AbortStep 应取出 1 条，got %v", paths)
	}
	if paths := runner.AbortStep(); paths != nil {
		t.Fatalf("第二次 AbortStep 必须返回 nil（幂等），got %v", paths)
	}
}
