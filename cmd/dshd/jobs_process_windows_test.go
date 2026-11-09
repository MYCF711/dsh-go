//go:build windows

package main

// T08：`verifyProcessImage` 的瞬时错误重试验收（T06 F4 的 Windows 现场）。
//
// 为什么要注入而不是"多跑几次真实进程"：`ERROR_GEN_FAILURE` 是进程对象已建、
// 映像段尚未完全就绪时的一次瞬时现象，靠真实竞态复现是概率题；这里把
// `queryProcessImageName` 换成受控实现，把"两次瞬时失败后成功"和"持续失败"
// 都变成确定性用例。

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// stubProcessImageQuery 把两个系统调用点换成受控实现，并在测试结束时还原。
func stubProcessImageQuery(t *testing.T, query func(windows.Handle) (string, error)) {
	t.Helper()
	oldOpen, oldQuery := openProcessForImage, queryProcessImageName
	openProcessForImage = func(int) (windows.Handle, error) { return windows.Handle(1), nil }
	queryProcessImageName = query
	t.Cleanup(func() {
		openProcessForImage, queryProcessImageName = oldOpen, oldQuery
	})
}

func windowsTestImage(t *testing.T) string {
	t.Helper()
	canonical, err := canonicalProcessImage(filepath.Join(t.TempDir(), "dshd.exe"))
	if err != nil {
		t.Fatalf("canonicalProcessImage: %v", err)
	}
	return canonical
}

// TestProcessImageRetriesTransientGenFailure：
// 两次 ERROR_GEN_FAILURE 后成功 → 必须恢复，且只对这一种错误重试。
func TestProcessImageRetriesTransientGenFailure(t *testing.T) {
	image := windowsTestImage(t)
	var calls int32
	stubProcessImageQuery(t, func(windows.Handle) (string, error) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			return "", windows.ERROR_GEN_FAILURE
		}
		return image, nil
	})
	if err := verifyProcessImage(4242, image); err != nil {
		t.Fatalf("两次瞬时失败后必须恢复，实际: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("查询次数=%d，want 3（1 次 + 2 次重试）", got)
	}
}

// TestProcessImageTransientRecoveryIsBounded：持续失败只许 3 次，
// 不能无限重试把一轮 turn 挂死。
func TestProcessImageTransientRecoveryIsBounded(t *testing.T) {
	image := windowsTestImage(t)
	var calls int32
	stubProcessImageQuery(t, func(windows.Handle) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", windows.ERROR_GEN_FAILURE
	})
	err := verifyProcessImage(4242, image)
	if err == nil {
		t.Fatal("持续失败必须返回错误，不许放行")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("持续失败查询次数=%d，want 3（有界）", got)
	}
	if !errors.Is(err, windows.ERROR_GEN_FAILURE) {
		t.Fatalf("错误必须保留原始错误链（便于定位 31）：%v", err)
	}
}

// TestProcessImageDoesNotRetryAccessDenied：ACCESS_DENIED 不是瞬时错误，
// 一次就失败，绝不重试、绝不放行。
func TestProcessImageDoesNotRetryAccessDenied(t *testing.T) {
	image := windowsTestImage(t)
	var calls int32
	stubProcessImageQuery(t, func(windows.Handle) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", windows.ERROR_ACCESS_DENIED
	})
	if err := verifyProcessImage(4242, image); err == nil {
		t.Fatal("ACCESS_DENIED 必须失败")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("ACCESS_DENIED 查询次数=%d，want 1（不得重试）", got)
	}
}

// TestProcessImageMismatchIsNotRetriedOrPassed：映像不匹配不是瞬时错误，
// 一次就失败；重试只会把攻击/错配窗口拖长。
func TestProcessImageMismatchIsNotRetriedOrPassed(t *testing.T) {
	image := windowsTestImage(t)
	other := windowsTestImage(t)
	var calls int32
	stubProcessImageQuery(t, func(windows.Handle) (string, error) {
		atomic.AddInt32(&calls, 1)
		return other, nil
	})
	if err := verifyProcessImage(4242, image); err == nil {
		t.Fatal("映像不匹配必须失败")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("映像不匹配查询次数=%d，want 1（不得重试）", got)
	}
}

// TestProcessImageOpenErrorIsNotRetried：打开句柄失败（含 ACCESS_DENIED）
// 不属于"查询瞬时失败"，不得重试。
func TestProcessImageOpenErrorIsNotRetried(t *testing.T) {
	image := windowsTestImage(t)
	oldOpen, oldQuery := openProcessForImage, queryProcessImageName
	openProcessForImage = func(int) (windows.Handle, error) { return 0, windows.ERROR_ACCESS_DENIED }
	var calls int32
	queryProcessImageName = func(windows.Handle) (string, error) {
		atomic.AddInt32(&calls, 1)
		return image, nil
	}
	t.Cleanup(func() { openProcessForImage, queryProcessImageName = oldOpen, oldQuery })
	err := verifyProcessImage(4242, image)
	if err == nil {
		t.Fatal("打开句柄失败必须失败")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("打开失败后不该再查询，实际 %d 次", got)
	}
}

// TestProcessImageTransientRecoveryRepeats：T06 的现场是"5 跑 1 红"，
// 单次恢复证明不了稳定；这里连续 20 轮、每轮先两次瞬时失败，全部必须恢复。
func TestProcessImageTransientRecoveryRepeats(t *testing.T) {
	image := windowsTestImage(t)
	for round := 0; round < 20; round++ {
		var calls int32
		stubProcessImageQuery(t, func(windows.Handle) (string, error) {
			if atomic.AddInt32(&calls, 1) <= 2 {
				return "", windows.ERROR_GEN_FAILURE
			}
			return image, nil
		})
		started := time.Now()
		if err := verifyProcessImage(4242, image); err != nil {
			t.Fatalf("第 %d 轮：两次瞬时失败后必须恢复，实际: %v", round+1, err)
		}
		if calls != 3 {
			t.Fatalf("第 %d 轮：查询次数=%d，want 3", round+1, calls)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("第 %d 轮：重试耗时 %v，超出有界退避范围", round+1, elapsed)
		}
	}
}

// 有界重试的上限必须仍是 3（改数字要显式改测试，不许悄悄放大）。
const processImageQueryAttemptsWant = 3
