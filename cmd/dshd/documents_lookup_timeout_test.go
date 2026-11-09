package main

import (
	"errors"
	"testing"
	"time"
)

// TestDocumentsLookupTimeoutEnv 钉住官方默认 10000ms 与覆盖语义。
func TestDocumentsLookupTimeoutEnv(t *testing.T) {
	if DefaultDocumentsLookupTimeoutMS != 10000 {
		t.Fatalf("官方默认值必须逐值一致：10000，实际 %d", DefaultDocumentsLookupTimeoutMS)
	}
	t.Setenv(EnvDocumentsLookupTimeoutMS, "")
	if got := documentsLookupTimeout(); got != 10*time.Second {
		t.Fatalf("默认预算 = %s，期望 10s", got)
	}
	t.Setenv(EnvDocumentsLookupTimeoutMS, "250")
	if got := documentsLookupTimeout(); got != 250*time.Millisecond {
		t.Fatalf("env 覆盖后 = %s，期望 250ms", got)
	}
	t.Setenv(EnvDocumentsLookupTimeoutMS, "0")
	if got := documentsLookupTimeout(); got != 0 {
		t.Fatalf("env=0 应当表示不设预算，实际 %s", got)
	}
	t.Setenv(EnvDocumentsLookupTimeoutMS, "bogus")
	if got := documentsLookupTimeout(); got != 10*time.Second {
		t.Fatalf("非法值应退回默认 10s，实际 %s", got)
	}
}

// TestDocumentsDirWithTimeoutReturnsValueAndError 钉住两条正常路径：
// 查找成功要原样返回；查找本身报错要原样传出去（不吞错）。
func TestDocumentsDirWithTimeoutReturnsValueAndError(t *testing.T) {
	got, err := documentsDirWithTimeout(func() (string, error) { return `C:\Users\u\Documents`, nil }, 2*time.Second)
	if err != nil || got != `C:\Users\u\Documents` {
		t.Fatalf("正常查找 = (%q, %v)", got, err)
	}
	sentinel := errors.New("known folder 失败")
	if _, err := documentsDirWithTimeout(func() (string, error) { return "", sentinel }, 2*time.Second); !errors.Is(err, sentinel) {
		t.Fatalf("查找错误没有原样传出: %v", err)
	}
}

// TestDocumentsDirWithTimeoutBoundsHungLookup 是关键一条：卡住的查找必须有预算，
// 不能把调用方一直拖住（官方给 10000ms 的理由就是这种"卡住的 shell 查询"）。
func TestDocumentsDirWithTimeoutBoundsHungLookup(t *testing.T) {
	block := make(chan struct{})
	defer close(block) // 让挂住的 goroutine 最后能退出，不污染其它测试
	start := time.Now()
	_, err := documentsDirWithTimeout(func() (string, error) {
		<-block
		return "", nil
	}, 50*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("挂住的查找必须超时报错，实际成功了")
	}
	if elapsed > time.Second {
		t.Fatalf("超时预算没生效：耗时 %s", elapsed)
	}
}
