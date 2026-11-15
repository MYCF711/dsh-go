package settings

// 宿主设置文档（`<home>/state/settings.json`）的存储契约。
//
// 这份文档是"插件声明命名空间 → 值按 namespace 分段 + 每段自带 revision"的最小
// 持久层：settingsform 的 `Store` 接口原样映射到 Read/Write，`llm` 命名空间的
// `providers.<route>.retryPolicy` 就是它的第一个真实使用方（缺口 #2）。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testValue(route string, maxRetries float64) map[string]any {
	return map[string]any{
		"providers": map[string]any{
			route: map[string]any{
				"retryPolicy": map[string]any{
					"mode":       "normal",
					"maxRetries": maxRetries,
				},
			},
		},
	}
}

func encodeForTest(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	return string(raw)
}

// TestStoreRoundTripAcrossReopen：写进去的值 + revision 必须落盘，重开读到同一份。
func TestStoreRoundTripAcrossReopen(t *testing.T) {
	home := t.TempDir()
	store := Open(home)
	if value, revision, err := store.Read("llm"); err != nil || value != nil || revision != 0 {
		t.Fatalf("空 home 上读 llm 应是 (nil,0,nil)，实际 (%v,%d,%v)", value, revision, err)
	}
	revision, err := store.Write("llm", testValue("openai-completions", 2), nil)
	if err != nil {
		t.Fatalf("写 llm: %v", err)
	}
	if revision != 1 {
		t.Fatalf("首次写入 revision 应为 1，实际 %d", revision)
	}

	reopened := Open(home)
	value, gotRevision, err := reopened.Read("llm")
	if err != nil {
		t.Fatalf("重开读 llm: %v", err)
	}
	if gotRevision != 1 {
		t.Fatalf("重开后 revision 应为 1，实际 %d", gotRevision)
	}
	if got, want := encodeForTest(t, value), encodeForTest(t, testValue("openai-completions", 2)); got != want {
		t.Fatalf("重开后值不对：got=%s want=%s", got, want)
	}
	// 其它命名空间互不影响。
	if other, otherRevision, err := reopened.Read("model"); err != nil || other != nil || otherRevision != 0 {
		t.Fatalf("llm 写入不许污染 model：(%v,%d,%v)", other, otherRevision, err)
	}
}

// TestStoreRevisionFenceIsTyped：expectedRevision 冲突必须带 expected/actual，
// 供 settingsform 投影成与其它命名空间同形的 revision_conflict。
func TestStoreRevisionFenceIsTyped(t *testing.T) {
	home := t.TempDir()
	store := Open(home)
	one := int64(1)
	if _, err := store.Write("llm", testValue("r1", 1), nil); err != nil {
		t.Fatalf("第一次写: %v", err)
	}
	if _, err := store.Write("llm", testValue("r2", 3), &one); err != nil {
		t.Fatalf("第二次写（expected=1）: %v", err)
	}
	stale := int64(1)
	_, err := store.Write("llm", testValue("r3", 9), &stale)
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("过期 revision 必须回 *RevisionConflictError，实际 %v", err)
	}
	if conflict.Expected != 1 || conflict.Actual != 2 {
		t.Fatalf("冲突要报 expected=1 actual=2，实际 %+v", conflict)
	}
	// 被拒的写入不许改内存或落盘。
	value, revision, readErr := Open(home).Read("llm")
	if readErr != nil {
		t.Fatalf("读回: %v", readErr)
	}
	if revision != 2 {
		t.Fatalf("被拒的写入不许推进 revision，实际 %d", revision)
	}
	if got, want := encodeForTest(t, value), encodeForTest(t, testValue("r2", 3)); got != want {
		t.Fatalf("被拒的写入不许改值：got=%s want=%s", got, want)
	}
}

// TestStoreReadReturnsCopy：Read 出来的 map 不能被调用方改到存储内部状态。
func TestStoreReadReturnsCopy(t *testing.T) {
	home := t.TempDir()
	store := Open(home)
	if _, err := store.Write("llm", testValue("openai-completions", 2), nil); err != nil {
		t.Fatalf("写: %v", err)
	}
	value, _, err := store.Read("llm")
	if err != nil {
		t.Fatalf("读: %v", err)
	}
	providers := value["providers"].(map[string]any)
	providers["injected"] = map[string]any{"retryPolicy": map[string]any{"mode": "always"}}
	again, _, err := store.Read("llm")
	if err != nil {
		t.Fatalf("再读: %v", err)
	}
	if _, leaked := again["providers"].(map[string]any)["injected"]; leaked {
		t.Fatal("Read 必须返回深拷贝：调用方改返回值不能污染存储状态")
	}
}

// TestStoreCorruptDocumentFailsLoud：文档坏了必须响亮失败，不许当"空文档"覆盖回去。
func TestStoreCorruptDocumentFailsLoud(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "state", FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建 state: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"namespaces":`), 0o644); err != nil {
		t.Fatalf("写坏文档: %v", err)
	}
	store := Open(home)
	if _, _, err := store.Read("llm"); err == nil {
		t.Fatal("损坏文档的 Read 必须失败")
	}
	if _, err := store.Write("llm", testValue("r1", 1), nil); err == nil {
		t.Fatal("损坏文档的 Write 必须失败（不许拿空文档覆盖掉用户的文件）")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回坏文档: %v", err)
	}
	if !strings.Contains(string(raw), `{"version":1,"namespaces":`) {
		t.Fatalf("损坏文档必须原样留在盘上：%s", raw)
	}
}

// TestStoreWriteFailureKeepsMemory：落盘失败时内存不许前移，否则就是"报错但
// 看起来保存成功了"。
func TestStoreWriteFailureKeepsMemory(t *testing.T) {
	home := t.TempDir()
	store := Open(home)
	if _, err := store.Write("llm", testValue("r1", 2), nil); err != nil {
		t.Fatalf("先写一次: %v", err)
	}
	// 把目标文件换成目录：rename 必然失败。
	path := filepath.Join(home, "state", FileName)
	if err := os.Remove(path); err != nil {
		t.Fatalf("移除文档: %v", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("把文档换成目录: %v", err)
	}
	expected := int64(1)
	if _, err := store.Write("llm", testValue("r2", 9), &expected); err == nil {
		t.Fatal("落盘失败必须返回错误")
	}
	value, revision, err := store.Read("llm")
	if err != nil {
		t.Fatalf("失败后读: %v", err)
	}
	if revision != 1 {
		t.Fatalf("落盘失败不许推进 revision，实际 %d", revision)
	}
	if got, want := encodeForTest(t, value), encodeForTest(t, testValue("r1", 2)); got != want {
		t.Fatalf("落盘失败不许改内存值：got=%s want=%s", got, want)
	}
}
