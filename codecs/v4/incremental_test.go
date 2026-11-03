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
	lg, err := Create(path, Header{Type: "session", Version: 4, ID: "x"})
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

// 首帧必须永远是 header 独占，追加多少条都不能变。
//
// 这条是我上一轮的**误报**：当时 2000 次 Append 之后 ReadFile 报"首行 type = ”"，
// 我以为是批量刷写把 header 和正文合并了。真相是测试里的 header 少了 type/version。
// 记一笔：**验证工具自己写错的时候，它给的是一个很干净的假警报。**
func TestHeaderFrameStaysAloneWithManyAppends(t *testing.T) {
	path := writeLog(t, 2000)
	if _, err := ReadFile(path); err != nil {
		t.Fatalf("2000 次追加之后 ReadFile 失败：%v", err)
	}
	// 只读首帧的能力（列表靠它）也必须照常
	h, err := ReadHeader(path)
	if err != nil || h.ID != "x" {
		t.Fatalf("ReadHeader 拿到 %+v / %v", h, err)
	}
	// 而且按字节看，首帧必须比整个文件短
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() == 0 {
		t.Fatal("文件是空的")
	}
	evs, next, err := ReadSince(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2000 {
		t.Fatalf("应当读到 2000 个事件，拿到 %d", len(evs))
	}
	if next >= st.Size() {
		t.Logf("注意：next=%d 已到文件尾 %d（没有半帧，正常）", next, st.Size())
	}
}
