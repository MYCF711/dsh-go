package main

import "testing"

// CLI 帮助里的「0=不传」语义：0 映射成关闭哨兵 -1，负值原样透传。
func TestWorkspaceBytesZeroMapsToDisabledSentinel(t *testing.T) {
	if got := workspaceBytesForPrompt(0); got != -1 {
		t.Fatalf("0 必须映射成 -1（不传），得到 %d", got)
	}
	if got := workspaceBytesForPrompt(1234); got != 1234 {
		t.Fatalf("正数必须原样透传，得到 %d", got)
	}
	if got := workspaceBytesForPrompt(-1); got != -1 {
		t.Fatalf("负值必须原样透传，得到 %d", got)
	}
}
