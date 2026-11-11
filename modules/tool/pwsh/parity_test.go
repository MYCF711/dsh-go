package pwsh

// 官方 rc.2 的 pwsh 契约（`docs/tool-catalog.md` §`@deepseek-ai/dsh-tool-pwsh`）：
//
//	required: description + command
//	properties: description / command / timeoutMs / workdir / run_in_background
//
// 这里逐条给出行为证据，不只看 schema：
//   - description 必须被接受并出现在结果里（UI 读它）；
//   - timeoutMs 必须真的生效（毫秒），并且与旧别名 timeout_seconds 同时给时官方名优先；
//   - run_in_background 必须真的登记进宿主的 jobs 表（job_output 能取到），
//     不是"起个 goroutine 存私表"的假后台。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	// 测试专用：要证明"job_output 真的看得见这个任务"，就得对着唯一的 jobs 实现
	// （modules/exec/bash 的 Service）验。生产代码不 import 它——工具走 spi.Exec 接缝。
	"dshgo/internal/spi"
	dshbash "dshgo/modules/exec/bash"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// 新工具不能因为环境里没有沙箱档位而装配失败：直接构造工具，admin 判定走
// "gate 未接线 + 无一次性请求 = 放行"的旧行为。
func newTestTool(t *testing.T, opts ...Option) *Tool {
	t.Helper()
	tl, err := newTool(opts...)
	if err != nil {
		t.Fatalf("装配 pwsh 工具失败: %v", err)
	}
	return tl
}

func requirePowerShell(t *testing.T) {
	t.Helper()
	if _, _, err := resolveShell(); err != nil {
		t.Skipf("本机没有 pwsh/powershell，跳过真实进程测试: %v", err)
	}
}

// 官方 required 的两个参数必须在 properties 里，且官方能力参数一个不少。
func TestSchemaMatchesOfficialRc2(t *testing.T) {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("schema 不是合法 JSON: %v", err)
	}
	for _, p := range []string{"description", "command", "timeoutMs", "workdir", "run_in_background"} {
		if _, ok := s.Properties[p]; !ok {
			t.Errorf("官方参数 %q 没有声明", p)
		}
	}
	// 能力下限：旧参数与 per-call 沙箱请求必须保留（只加不减）。
	for _, p := range []string{"timeout_seconds", "sandbox_permissions"} {
		if _, ok := s.Properties[p]; !ok {
			t.Errorf("本仓既有参数 %q 被删掉了（能力下限规则不允许）", p)
		}
	}
	got := map[string]bool{}
	for _, r := range s.Required {
		got[r] = true
	}
	if !got["description"] || !got["command"] || len(got) != 2 {
		t.Fatalf("required = %v，期望只有 description 和 command（官方 rc.2）", s.Required)
	}
}

// description 进结果：Summary 用它当标题，Data 里带原值。
func TestDescriptionSurfacesInResult(t *testing.T) {
	requirePowerShell(t)
	tl := newTestTool(t)
	res, err := tl.Exec(context.Background(), json.RawMessage(
		`{"description":"打个招呼","command":"Write-Output hello-parity"}`))
	if err != nil {
		t.Fatalf("带 description 的调用不应失败: %v", err)
	}
	if !strings.Contains(res.Text, "hello-parity") {
		t.Fatalf("没有拿到命令输出: %q", res.Text)
	}
	if !strings.Contains(res.Summary, "打个招呼") {
		t.Fatalf("Summary 没有用 description 当标题（UI 靠它显示）: %q", res.Summary)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if d.Description != "打个招呼" {
		t.Fatalf("Data.Description = %q", d.Description)
	}
}

// timeoutMs 必须真的生效：毫秒级超时，不能等满命令自己睡完。
func TestTimeoutMsTakesEffect(t *testing.T) {
	requirePowerShell(t)
	tl := newTestTool(t)
	start := time.Now()
	res, err := tl.Exec(context.Background(), json.RawMessage(
		`{"description":"睡很久","command":"Start-Sleep -Seconds 8","timeoutMs":300}`))
	took := time.Since(start)
	if err == nil {
		t.Fatal("300ms 超时的 8 秒命令必须失败")
	}
	if took > 5*time.Second {
		t.Fatalf("timeoutMs 没生效：等了 %s", took)
	}
	if !strings.Contains(err.Error(), "超时") && !strings.Contains(res.Hint, "超过") {
		t.Fatalf("错误没说明是超时: err=%v hint=%q", err, res.Hint)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if d.TimeoutMS != 300 {
		t.Fatalf("生效的超时 = %dms，期望 300ms", d.TimeoutMS)
	}
}

// 官方名优先：同时给 timeoutMs 与旧别名 timeout_seconds 时按 timeoutMs 走。
// 反向（按旧的 60 秒走）会让这次调用挂 60 秒，测试当场就会超时——所以这条是硬判据。
func TestTimeoutMsWinsOverLegacySeconds(t *testing.T) {
	requirePowerShell(t)
	tl := newTestTool(t)
	start := time.Now()
	res, err := tl.Exec(context.Background(), json.RawMessage(
		`{"command":"Start-Sleep -Seconds 8","timeoutMs":250,"timeout_seconds":60}`))
	took := time.Since(start)
	if err == nil {
		t.Fatal("250ms 超时的 8 秒命令必须失败")
	}
	if took > 5*time.Second {
		t.Fatalf("timeout_seconds 抢先生效了：等了 %s", took)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if d.TimeoutMS != 250 {
		t.Fatalf("生效的超时 = %dms，期望官方名给的 250ms", d.TimeoutMS)
	}
}

// 旧别名仍然有效（能力下限：老调用方照旧按秒超时）。
func TestLegacyTimeoutSecondsStillHonored(t *testing.T) {
	requirePowerShell(t)
	tl := newTestTool(t)
	res, err := tl.Exec(context.Background(), json.RawMessage(
		`{"command":"Start-Sleep -Seconds 8","timeout_seconds":1}`))
	if err == nil {
		t.Fatal("1 秒超时的 8 秒命令必须失败")
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if d.TimeoutMS != 1000 {
		t.Fatalf("生效的超时 = %dms，期望 1000ms（旧别名按秒）", d.TimeoutMS)
	}
}

// 没有 jobs 能力时 run_in_background 必须 fail-loud：绝不能静默前台跑、
// 更不能编一个取不到输出的 job id。
func TestBackgroundWithoutSeamFailsLoud(t *testing.T) {
	tl := newTestTool(t) // 没有 scope、没有注入接缝
	res, err := tl.Exec(context.Background(), json.RawMessage(
		`{"description":"后台","command":"Write-Output x","run_in_background":true}`))
	if err == nil {
		t.Fatal("没有执行接缝时 run_in_background 必须失败，不能假成功")
	}
	if !strings.Contains(res.Hint, "jobs") {
		t.Fatalf("Hint 要说清缺 jobs 能力: %q", res.Hint)
	}
	if d, ok := res.Data.(data); ok && d.JobID != "" {
		t.Fatalf("没接缝还编出了 job id: %q", d.JobID)
	}
}

// 真后台：起一条要跑一会儿的命令，立刻拿到 job id，且**宿主的 jobs 表里**能查到它、
// 拿到它的输出——这条就是"与 bash 同一机制"的判据。
func TestBackgroundRegistersRealJobAndOutputIsReadable(t *testing.T) {
	requirePowerShell(t)
	svc := dshbash.NewService(t.TempDir())
	t.Cleanup(func() { svc.Close() })
	tl := newTestTool(t, WithExecSeam(svc))

	start := time.Now()
	res, err := tl.Exec(context.Background(), json.RawMessage(
		`{"description":"后台算一下","command":"Start-Sleep -Milliseconds 600; Write-Output \"a'b done\"","run_in_background":true}`))
	took := time.Since(start)
	if err != nil {
		t.Fatalf("后台启动失败: %v", err)
	}
	if took > 3*time.Second {
		t.Fatalf("run_in_background 应当立刻返回，实际等了 %s", took)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if d.JobID == "" {
		t.Fatalf("没给出 job_id（Data=%+v）", d)
	}
	if !d.Background {
		t.Fatal("Data.Background 必须是 true（消费方据此判断这是任务而不是输出）")
	}
	if !strings.Contains(res.Summary, d.JobID) || !strings.Contains(res.Text, d.JobID) {
		t.Fatalf("摘要/正文明说任务：summary=%q text=%q", res.Summary, res.Text)
	}

	// 关键：这个 id 必须属于宿主那张表，job_output 能取到输出。
	// 命令里带单引号，顺带证明 bash 那层引号转义没把 PowerShell 脚本吃掉。
	var found bool
	for _, j := range svc.List() {
		if j.ID == d.JobID {
			found = true
		}
	}
	if !found {
		t.Fatalf("jobs 表里没有 %s（说明只是编了个 id）", d.JobID)
	}
	out, err := svc.Output(d.JobID, 15*time.Second)
	if err != nil {
		t.Fatalf("job_output 取不到这个任务: %v", err)
	}
	if !strings.Contains(out.Output, "a'b done") {
		t.Fatalf("后台任务的输出不对（引号转义可能坏了）: %q", out.Output)
	}
	_ = svc.Kill(d.JobID)
}

// 接缝只有 Run（没有 RunBackground 扩展）时也要拿到 job id。
func TestBackgroundFallsBackToIdleSeam(t *testing.T) {
	requirePowerShell(t)
	svc := dshbash.NewService(t.TempDir())
	t.Cleanup(func() { svc.Close() })
	tl := newTestTool(t, WithExecSeam(foregroundOnly{svc}))

	res, err := tl.Exec(context.Background(), json.RawMessage(
		`{"command":"Start-Sleep -Milliseconds 400; Write-Output idle-path","run_in_background":true}`))
	if err != nil {
		t.Fatalf("后台启动失败: %v", err)
	}
	d, ok := res.Data.(data)
	if !ok || d.JobID == "" {
		t.Fatalf("走 Run+idle 回退时也要给出 job id: %#v", res.Data)
	}
	out, err := svc.Output(d.JobID, 15*time.Second)
	if err != nil || !strings.Contains(out.Output, "idle-path") {
		t.Fatalf("job_output = %+v, err=%v", out, err)
	}
	_ = svc.Kill(d.JobID)
}

// foregroundOnly 把任何 spi.Exec 裁成"只有 Run"的形状，用来证明回退路径。
type foregroundOnly struct{ inner spi.Exec }

func (f foregroundOnly) Run(cmd string, idle time.Duration) (spi.ExecResult, error) {
	return f.inner.Run(cmd, idle)
}

// 命令行拼装：workdir 要绝对化、脚本要单引号安全（含 ' 与换行）、shell 用 basename。
func TestBackgroundCommandLineQuoting(t *testing.T) {
	spec := backgroundSpec{
		shell:   `C:\Program Files\PowerShell\7\pwsh.exe`,
		script:  "Write-Output \"it's\"\nWrite-Output 2",
		workdir: t.TempDir(),
	}
	line := backgroundCommandLine(spec)
	if !strings.HasPrefix(line, "cd -- '") {
		t.Fatalf("workdir 没有变成 cd -- '<abs>': %s", line)
	}
	if strings.Contains(line, `C:\Program Files\PowerShell`) {
		t.Fatalf("命令行里不该出现 Windows 绝对路径（bash 侧不可靠）: %s", line)
	}
	if !strings.Contains(line, "'pwsh.exe' -NoLogo -NoProfile -NonInteractive -Command ") {
		t.Fatalf("没有用 basename 起 pwsh: %s", line)
	}
	// 单引号必须被转义掉，否则 bash 会提前闭合引号。
	want := `it'\''s`
	if !strings.Contains(line, want) {
		t.Fatalf("单引号没转义（应含 %s）: %s", want, line)
	}
	// 换行在单引号里是普通字符，必须原样保留（不能被拼成一整行）。
	if !strings.Contains(line, "\n") {
		t.Fatalf("脚本里的换行被吃掉了: %q", line)
	}
}
