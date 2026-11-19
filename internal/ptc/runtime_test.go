package ptc

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
)

func TestResolveTimeout(t *testing.T) {
	spec := TimeoutSpec{DefaultMs: 120_000, MaxMs: 600_000}
	num := func(v int64) *int64 { return &v }

	cases := []struct {
		name      string
		choice    TimeoutChoice
		supported bool
		want      *int64
		wantErr   bool
	}{
		{"没给 + 有默认", TimeoutDefault(), true, num(120_000), false},
		{"没给 + 无默认能力", TimeoutDefault(), false, nil, false},
		{"显式 null", TimeoutNone(), true, nil, false},
		{"显式 null + 无默认能力", TimeoutNone(), false, nil, false},
		{"正数", TimeoutMs(5_000), true, num(5_000), false},
		{"正数封顶", TimeoutMs(999_999), true, num(600_000), false},
		{"零不是正数", TimeoutMs(0), true, nil, true},
		{"负数是误用", TimeoutMs(-1), true, nil, true},
		{"不支持数字覆盖时必须拒绝", TimeoutMs(5_000), false, nil, true},
	}
	for _, tc := range cases {
		got, err := ResolveTimeout(tc.choice, tc.supported, spec)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s：应该报错", tc.name)
			} else if !IsMisuse(err) {
				t.Errorf("%s：错误必须是误用身份：%v", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s：不该报错：%v", tc.name, err)
			continue
		}
		if (got == nil) != (tc.want == nil) {
			t.Fatalf("%s：nil 状态不一致，got=%v want=%v", tc.name, deref(got), deref(tc.want))
		}
		if got != nil && *got != *tc.want {
			t.Errorf("%s：got %d want %d", tc.name, *got, *tc.want)
		}
	}
}

func deref(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func TestResolveSandboxRejectsExplicitModeWithoutConfinement(t *testing.T) {
	mode := sandbox.ModeReadOnly
	if _, err := ResolveSandbox(&mode, false); err == nil {
		t.Fatal("没有 confinement 的 provider 收到显式档位必须拒绝（悄悄吞掉才是最危险的那种顺从）")
	} else if !IsMisuse(err) {
		t.Fatalf("应该是误用身份：%v", err)
	}
	if _, err := ResolveSandbox(nil, false); err != nil {
		t.Fatalf("没给档位时不该报错：%v", err)
	}
	if _, err := ResolveSandbox(&mode, true); err != nil {
		t.Fatalf("支持 confinement 时不该报错：%v", err)
	}
}

func TestResolveCwd(t *testing.T) {
	got, err := ResolveCwd("", "")
	if err == nil {
		t.Fatalf("既没给也没默认值时必须报错，got %q", got)
	}
	cwd, err := ResolveCwd(".", "")
	if err != nil {
		t.Fatalf("相对路径应该被解析成绝对路径：%v", err)
	}
	if !strings.HasPrefix(cwd, string(cwd[0])) || len(cwd) < 2 {
		t.Fatalf("解析结果不像绝对路径：%q", cwd)
	}
}

func TestLosslessJSONProblem(t *testing.T) {
	type custom struct{ A int }
	cases := []struct {
		name  string
		value any
		bad   bool
	}{
		{"nil", nil, false},
		{"bool", true, false},
		{"string", "x", false},
		{"int", 42, false},
		{"float", 1.5, false},
		{"NaN", math.NaN(), true},
		{"slice", []any{1, "a", nil}, false},
		{"map", map[string]any{"a": 1}, false},
		{"nested bad", map[string]any{"a": []any{1, func() {}}}, true},
		{"channel", make(chan int), true},
		{"custom struct", custom{A: 1}, true},
		{"bytes", []byte("ab"), true},
		{"json.Number", json.Number("42"), false},
		{"坏 json.Number", json.Number("4x"), true},
	}
	for _, tc := range cases {
		problem := LosslessJSONProblem(tc.value)
		if tc.bad && problem == "" {
			t.Errorf("%s：应该报不可无损序列化", tc.name)
		}
		if !tc.bad && problem != "" {
			t.Errorf("%s：不该报错，得到 %q", tc.name, problem)
		}
	}
}

func TestMisuseIdentity(t *testing.T) {
	err := Misuse("坏参数 %d", 1)
	if !IsMisuse(err) {
		t.Fatal("Misuse 造的错误必须能被 IsMisuse 认出")
	}
	if !errors.Is(err, ErrMisuse) {
		t.Fatal("必须能 errors.Is 到 ErrMisuse")
	}
	if !strings.Contains(err.Error(), "坏参数 1") {
		t.Fatalf("上下文丢了：%v", err)
	}
	if IsMisuse(errors.New("别的错误")) {
		t.Fatal("普通错误不该被认成误用")
	}
}

func TestFailureKindValid(t *testing.T) {
	kinds := []FailureKind{FailureException, FailureTimeout, FailureAbort, FailureWorkerExit,
		FailureInvalidOutput, FailureOutputLimit, FailureProtocol, FailureSandboxUnavailable}
	for _, k := range kinds {
		if !k.Valid() {
			t.Errorf("%q 应该在官方词表里", string(k))
		}
	}
	if FailureKind("boom").Valid() {
		t.Fatal("自造的 kind 不该算合法")
	}
}

func TestFailKeepsLogs(t *testing.T) {
	result := Fail(FailureTimeout, "超时", []string{"a", "b"})
	if !result.Failed() {
		t.Fatal("Fail 必须产生失败结果")
	}
	if len(result.Logs) != 2 {
		t.Fatalf("失败结果必须保留已捕获输出，got %v", result.Logs)
	}
	if result.Error.Kind != FailureTimeout {
		t.Fatalf("kind 丢了：%v", result.Error.Kind)
	}
}

func TestValidateProgramFormat(t *testing.T) {
	for _, f := range []ProgramFormat{FormatFunctionBody, FormatREPL, FormatScript} {
		if err := ValidateProgramFormat(f); err != nil {
			t.Errorf("%q 应该合法：%v", string(f), err)
		}
	}
	if err := ValidateProgramFormat("jsx"); err == nil {
		t.Fatal("官方之外的形态必须被拒")
	}
}

// 这条只是让接口本身被编译期检查：实现漏一个方法这里就红。
var _ Runtime = (*fakeRuntime)(nil)

type fakeRuntime struct{}

func (fakeRuntime) Language() string                                { return "typescript" }
func (fakeRuntime) Isolation() string                               { return "process" }
func (fakeRuntime) ProgramFormat() ProgramFormat                    { return FormatFunctionBody }
func (fakeRuntime) RetainsState() bool                              { return false }
func (fakeRuntime) Release(context.Context, any) error              { return nil }
func (fakeRuntime) ExecutionInstructions() string                   { return "" }
func (fakeRuntime) SandboxMode() (sandbox.Mode, bool)               { return "", false }
func (fakeRuntime) Timeout() (TimeoutSpec, bool)                    { return TimeoutSpec{}, false }
func (fakeRuntime) Resolve(RunRequest) (RunSpec, error)             { return RunSpec{}, nil }
func (fakeRuntime) Run(context.Context, RunSpec) (RunResult, error) { return RunResult{}, nil }
