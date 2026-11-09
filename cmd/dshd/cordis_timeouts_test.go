package main

import (
	"context"
	"testing"
	"time"
)

// TestCordisTimeoutEnvDefaults 钉住官方两个默认值（5000 / 10000ms）与覆盖语义。
func TestCordisTimeoutEnvDefaults(t *testing.T) {
	if DefaultCordisVMTimeoutMS != 5000 || DefaultCordisClientInspectTimeoutMS != 10000 {
		t.Fatalf("官方默认值必须逐值一致：5000/10000，实际 %d/%d",
			DefaultCordisVMTimeoutMS, DefaultCordisClientInspectTimeoutMS)
	}
	t.Setenv(EnvCordisVMTimeoutMS, "")
	t.Setenv(EnvCordisClientInspectTimeoutMS, "")
	if got := cordisVMTimeout(); got != 5*time.Second {
		t.Fatalf("默认 VM 预算 = %s，期望 5s", got)
	}
	if got := cordisClientInspectTimeout(); got != 10*time.Second {
		t.Fatalf("默认 inspect 预算 = %s，期望 10s", got)
	}
	t.Setenv(EnvCordisVMTimeoutMS, "750")
	t.Setenv(EnvCordisClientInspectTimeoutMS, "1500")
	if got := cordisVMTimeout(); got != 750*time.Millisecond {
		t.Fatalf("env 覆盖后 VM 预算 = %s", got)
	}
	if got := cordisClientInspectTimeout(); got != 1500*time.Millisecond {
		t.Fatalf("env 覆盖后 inspect 预算 = %s", got)
	}
	t.Setenv(EnvCordisVMTimeoutMS, "0")
	if got := cordisVMTimeout(); got != 0 {
		t.Fatalf("env=0 应当表示不设预算，实际 %s", got)
	}
	t.Setenv(EnvCordisVMTimeoutMS, "bogus")
	if got := cordisVMTimeout(); got != 5*time.Second {
		t.Fatalf("非法值应退回默认 5s，实际 %s", got)
	}
}

// TestWithCordisBudgetKeepsShorterDeadline 钉住预算包装的两条边界：
// 上游已有更短期限时不能被放宽；budget=0 时原样返回（不设预算）。
func TestWithCordisBudgetKeepsShorterDeadline(t *testing.T) {
	ctx, cancel := withCordisBudget(context.Background(), 5*time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("套了预算的 ctx 必须有 deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("预算 = %s，期望 0 < d <= 5s", remaining)
	}

	tight, tightCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer tightCancel()
	wrapped, wrappedCancel := withCordisBudget(tight, 5*time.Second)
	defer wrappedCancel()
	wrappedDeadline, _ := wrapped.Deadline()
	tightDeadline, _ := tight.Deadline()
	if !wrappedDeadline.Equal(tightDeadline) {
		t.Fatalf("上游更短的期限被放宽了：%s → %s", tightDeadline, wrappedDeadline)
	}

	raw, rawCancel := withCordisBudget(context.Background(), 0)
	defer rawCancel()
	if _, ok := raw.Deadline(); ok {
		t.Fatal("budget=0 时不该注入 deadline")
	}
}
