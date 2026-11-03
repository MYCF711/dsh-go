package v4

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLog(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.v4.jsonl.zstd")
	lg, err := Create(path, Header{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := lg.Append(Event{Type: "user/message", Seq: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// 从 0 读全部；再从那次的 next 读 → 必须一条都没有（没有新东西就是没有）。
func TestReadSinceIsIncremental(t *testing.T) {
	path := writeLog(t, 20)
	evs, next, err := ReadSince(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatal("从 0 读应当拿到事件")
	}
	again, next2, err := ReadSince(path, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("没有新事件时不该再返回 %d 条", len(again))
	}
	if next2 != next {
		t.Fatalf("没有新事件时起点不该前进：%d → %d", next, next2)
	}
}

func TestReadSinceZeroOnMissing(t *testing.T) {
	if _, _, err := ReadSince(os.DevNull+"/nope", 0); err == nil {
		t.Fatal("文件不存在应当报错")
	}
}
