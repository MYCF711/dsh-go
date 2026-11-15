package pwsh

// 官方 `pwsh-sandbox` 的两条预算（`timeoutMs` / `maxOutputBytes`）在这里证明
// 是**设置值 → 真实消费方**的接线，而不是"读得到但没用"的字段：
//
//	官方权威源：@deepseek-ai/dsh-pwsh-local/src/index.ts
//	  :128  timeoutMs:      z.number().default(120_000)
//	  :129  maxTimeoutMs:   z.number().default(600_000)
//	  :130  maxOutputBytes: z.number().default(64_000)
//
// 判据四条：
//  1. 出厂默认逐字等于官方（120s / 64000 / 上限 600s）；
//  2. 注入预算后，"模型没给"时用它，且结果 Data 里的 timeout_ms 就是注入值；
//  3. 模型显式给 timeoutMs 时仍以模型为准（既有语义一个字没改）；
//  4. 坏值（0/负）不改变默认——不能把工具悄悄变成"永远超时/永远空输出"。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 出厂默认必须与官方三个数字逐字一致（rc.2 原件：pwsh-local/src/index.ts:128-130）。
func TestBudgetsDefaultsMatchOfficial(t *testing.T) {
	if DefaultTimeout != 120*time.Second {
		t.Fatalf("DefaultTimeout = %s，官方 timeoutMs 默认 120_000ms", DefaultTimeout)
	}
	if DefaultMaxOutputBytes != 64000 {
		t.Fatalf("DefaultMaxOutputBytes = %d，官方 maxOutputBytes 默认 64_000（不是 64KiB=65536）",
			DefaultMaxOutputBytes)
	}
	if maxTimeout != 600*time.Second {
		t.Fatalf("maxTimeout = %s，官方 maxTimeoutMs 默认 600_000ms", maxTimeout)
	}

	tl := newTestTool(t)
	if got := tl.defaultTimeout(); got != DefaultTimeout {
		t.Fatalf("未注入时默认超时 = %s，期望 %s", got, DefaultTimeout)
	}
	if got := tl.outputLimit(); got != DefaultMaxOutputBytes {
		t.Fatalf("未注入时输出上限 = %d，期望 %d", got, DefaultMaxOutputBytes)
	}
	timeout, timeoutMS := tl.resolveTimeout(args{})
	if timeout != DefaultTimeout || timeoutMS != 120000 {
		t.Fatalf("未注入且模型没给超时: %s/%dms，期望 %s/120000ms",
			timeout, timeoutMS, DefaultTimeout)
	}
}

// 真进程复核出厂默认：结果 Data 里的 timeout_ms 就是实际生效值 120000。
func TestBudgetsDefaultShowsInResult(t *testing.T) {
	requirePowerShell(t)
	tl := newTestTool(t)
	res, err := tl.Exec(context.Background(), json.RawMessage(
		`{"description":"默认预算","command":"Write-Output budget-default"}`))
	if err != nil {
		t.Fatalf("普通命令不该失败: %v", err)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if d.TimeoutMS != 120000 {
		t.Fatalf("生效的超时 = %dms，期望官方默认 120000ms", d.TimeoutMS)
	}
	if d.Truncated {
		t.Fatal("小输出不该被截断（默认上限 64000 字节）")
	}
}

// 注入 `pwsh-sandbox.timeoutMs` 后：没给超时时用它；模型显式给了仍以模型为准。
func TestWithTimeoutBudgetSetsDefaultButModelWins(t *testing.T) {
	tl := newTestTool(t, WithTimeoutBudget(90*time.Second))

	if got := tl.defaultTimeout(); got != 90*time.Second {
		t.Fatalf("注入后默认超时 = %s，期望 90s", got)
	}
	timeout, timeoutMS := tl.resolveTimeout(args{})
	if timeout != 90*time.Second || timeoutMS != 90000 {
		t.Fatalf("没给超时应走注入值: %s/%dms，期望 90s/90000ms", timeout, timeoutMS)
	}
	// 既有语义：模型显式给了就用模型的（官方名优先于旧别名）。
	if _, ms := tl.resolveTimeout(args{TimeoutMs: 1000}); ms != 1000 {
		t.Fatalf("模型显式 timeoutMs=1000 被注入值盖掉了: %dms", ms)
	}
	if _, ms := tl.resolveTimeout(args{TimeoutSeconds: 30}); ms != 30000 {
		t.Fatalf("旧别名 timeout_seconds=30 没生效: %dms", ms)
	}
	// 既有语义：模型给的值超过 600s 仍夹到 600s。
	if got, _ := tl.resolveTimeout(args{TimeoutMs: 900000}); got != maxTimeout {
		t.Fatalf("超过上限没夹住: %s，期望 %s", got, maxTimeout)
	}
}

// 注入的超时是**真生效**的默认值：结果里的 timeout_ms 是 90000，且短预算会把
// 长命令提前掐掉（不是只写进 Data 好看）。
func TestWithTimeoutBudgetAppliesToRun(t *testing.T) {
	requirePowerShell(t)

	tl := newTestTool(t, WithTimeoutBudget(90*time.Second))
	res, err := tl.Exec(context.Background(), json.RawMessage(
		`{"command":"Write-Output budget-90"}`))
	if err != nil {
		t.Fatalf("短命令不该失败: %v", err)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if d.TimeoutMS != 90000 {
		t.Fatalf("生效的超时 = %dms，期望注入的 90000ms", d.TimeoutMS)
	}

	// 1 秒预算 + 8 秒命令：必须提前结束，说明预算真的进了 context。
	short := newTestTool(t, WithTimeoutBudget(time.Second))
	start := time.Now()
	res, err = short.Exec(context.Background(), json.RawMessage(
		`{"command":"Start-Sleep -Seconds 8"}`))
	took := time.Since(start)
	if err == nil {
		t.Fatal("1 秒预算的 8 秒命令必须失败")
	}
	// 判据与 parity_test.go 一致：8 秒命令必须早于 8 秒返回；满载机器上启动+杀进程
	// 本身要几秒，所以留到 7 秒，而"预算没生效"的表现是命令自己睡满 8 秒。
	if took > 7*time.Second {
		t.Fatalf("注入的超时没生效：等了 %s", took)
	}
	d, ok = res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if d.TimeoutMS != 1000 {
		t.Fatalf("生效的超时 = %dms，期望注入的 1000ms", d.TimeoutMS)
	}
}

// 注入 `pwsh-sandbox.maxOutputBytes` 后：超长输出按注入值截断并带 truncated 标记。
func TestWithMaxOutputBytesTruncates(t *testing.T) {
	requirePowerShell(t)

	// 同一条命令在默认上限（64000）下不该截断：证明截断确实来自注入值。
	def := newTestTool(t)
	res, err := def.Exec(context.Background(), json.RawMessage(
		`{"command":"Write-Output ('x' * 4096)"}`))
	if err != nil {
		t.Fatalf("默认上限下不该失败: %v", err)
	}
	if d, ok := res.Data.(data); !ok || d.Truncated {
		t.Fatalf("默认上限 64000 下 4096 字节输出不该截断: %#v", res.Data)
	}

	tl := newTestTool(t, WithMaxOutputBytes(1024))
	if got := tl.outputLimit(); got != 1024 {
		t.Fatalf("注入后输出上限 = %d，期望 1024", got)
	}
	res, err = tl.Exec(context.Background(), json.RawMessage(
		`{"command":"Write-Output ('x' * 4096)"}`))
	if err != nil {
		t.Fatalf("截断不是错误，不该失败: %v", err)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if !d.Truncated {
		t.Fatalf("超过注入上限没有置 truncated（bytes=%d）", d.Bytes)
	}
	if d.Bytes > 1024 {
		t.Fatalf("截断后仍保留了 %d 字节，超过注入的 1024", d.Bytes)
	}
	if d.Bytes == 0 {
		t.Fatal("注入上限后一个字节都没留住")
	}
	if !strings.Contains(res.Text, "已截断") {
		t.Fatalf("正文没说明被截断: %q", res.Text)
	}
	if !strings.Contains(res.Text, "1024") {
		t.Fatalf("截断文案里的数字不是实际生效值 1024: %q", res.Text)
	}
}

// 坏值不改变默认：0/负数一律当"没注入"（官方 assertPositiveFinite 的宿主等价语义）。
func TestInvalidBudgetsKeepDefaults(t *testing.T) {
	for _, budget := range []time.Duration{0, -time.Second, -10 * time.Minute} {
		tl := newTestTool(t, WithTimeoutBudget(budget))
		if got := tl.defaultTimeout(); got != DefaultTimeout {
			t.Fatalf("WithTimeoutBudget(%s) 改变了默认值: %s，期望 %s",
				budget, got, DefaultTimeout)
		}
		if _, ms := tl.resolveTimeout(args{}); ms != 120000 {
			t.Fatalf("WithTimeoutBudget(%s) 后默认生效值 = %dms，期望 120000ms", budget, ms)
		}
	}
	for _, n := range []int{0, -1, -4096} {
		tl := newTestTool(t, WithMaxOutputBytes(n))
		if got := tl.outputLimit(); got != DefaultMaxOutputBytes {
			t.Fatalf("WithMaxOutputBytes(%d) 改变了默认上限: %d，期望 %d",
				n, got, DefaultMaxOutputBytes)
		}
	}
	// 超过 maxTimeout 的注入值按上限夹住，而不是原样生效。
	clamped := newTestTool(t, WithTimeoutBudget(10*time.Minute))
	if got := clamped.defaultTimeout(); got != maxTimeout {
		t.Fatalf("超过上限的注入值 = %s，期望夹到 %s", got, maxTimeout)
	}
}
