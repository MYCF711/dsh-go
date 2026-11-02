package transport

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// updateSnapshot: go test ./internal/transport -run TestSessionAPISnapshot -update
// 只在"确认是故意改契约"时才用。
var updateSnapshot = flag.Bool("update", false, "rewrite testdata/api.snapshot.json")

// snapshotFile 是契约快照的落盘位置。
const snapshotFile = "testdata/api.snapshot.json"

// apiSnapshot 是快照的形状。
//
// 结构体顺序（Json 字段顺序）就是快照里的顺序，固定写死；
// 但每一族里的**键名**一律从结构体 tag 反射取得，绝不手抄——
// 手抄等于复制一份契约，抄错了测试照样是绿的。
type apiSnapshot struct {
	Methods []string `json:"methods"`
	Types   struct {
		Message        []string `json:"Message"`
		SessionSummary []string `json:"SessionSummary"`
		LoadResult     []string `json:"LoadResult"`
		PromptResult   []string `json:"PromptResult"`
	} `json:"types"`
}

// latestSnapshot 从**代码本身**算出当前契约。
func latestSnapshot() apiSnapshot {
	var s apiSnapshot
	for _, m := range SessionMethods() {
		s.Methods = append(s.Methods, string(m))
	}
	s.Types.Message = jsonKeys(Message{})
	s.Types.SessionSummary = jsonKeys(SessionSummary{})
	s.Types.LoadResult = jsonKeys(LoadResult{})
	s.Types.PromptResult = jsonKeys(PromptResult{})
	return s
}

// jsonKeys 取结构体的 JSON 键名（按字段声明顺序，去掉 ",omitempty" 之类的选项）。
// json:"-" 与无 tag 的字段跳过——它们本来就不出现在线上形状里。
func jsonKeys(v any) []string {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		panic("jsonKeys: 只接受结构体")
	}
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if idx := strings.IndexByte(tag, ','); idx >= 0 {
			tag = tag[:idx]
		}
		if tag == "" {
			continue
		}
		keys = append(keys, tag)
	}
	return keys
}

// encodeSnapshot 固定序列化方式：两空格缩进 + 末尾换行，路径与平台无关。
func encodeSnapshot(s apiSnapshot) ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func TestSessionAPISnapshot(t *testing.T) {
	// 先自证契约本身没退化：6 个方法、不重名。
	if got, want := len(SessionMethods()), 6; got != want {
		t.Fatalf("SessionMethods() 有 %d 个方法，U0 第一版要 %d 个", got, want)
	}
	seen := map[Method]bool{}
	for _, m := range SessionMethods() {
		if m == "" {
			t.Fatalf("SessionMethods() 里有空方法名")
		}
		if seen[m] {
			t.Fatalf("SessionMethods() 里方法名重复: %q", m)
		}
		seen[m] = true
	}

	want, err := encodeSnapshot(latestSnapshot())
	if err != nil {
		t.Fatalf("序列化快照失败: %v", err)
	}

	if *updateSnapshot {
		if err := os.MkdirAll(filepath.Dir(snapshotFile), 0o755); err != nil {
			t.Fatalf("建 testdata 目录失败: %v", err)
		}
		// 用 os.WriteFile 而不是文本模式：Go 不翻译行尾，快照在哪个平台都是同一份字节（实测 CR=0）。
		if err := os.WriteFile(snapshotFile, want, 0o644); err != nil {
			t.Fatalf("写快照失败: %v", err)
		}
		t.Logf("已更新快照: %s", snapshotFile)
		return
	}

	got, err := os.ReadFile(snapshotFile)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("快照不存在: %s\n先生成一次:\n  go test ./internal/transport -run TestSessionAPISnapshot -update\n新内容是:\n%s", snapshotFile, want)
		}
		t.Fatalf("读快照失败: %v", err)
	}

	if string(got) != string(want) {
		t.Fatalf("接口契约变了（快照 %s 与代码不一致）。\n"+
			"确认是故意改的就更新快照:\n"+
			"  go test ./internal/transport -run TestSessionAPISnapshot -update\n"+
			"——不一致意味着界面那边也得跟着改，别偷偷让它绿。\n"+
			"新的快照内容:\n%s", snapshotFile, want)
	}
}
