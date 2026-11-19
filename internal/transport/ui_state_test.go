package transport

import (
	"encoding/json"
	"os"
	"testing"
)

// TestUIStateAdditiveSnapshot 钉住三件事：
//
//  1. 这一族**没有**污染 canonical 25（快照里 canonicalMethodsUnchanged 仍是 25）；
//  2. 方法集与 `UIStateMethods()` 逐条相同（漏写一条就等于宣布了一个不存在的方法）；
//  3. 每条方法的 params/result 与实现里真正读写的字段一致（契约不是注释）。
func TestUIStateAdditiveSnapshot(t *testing.T) {
	var snap struct {
		Canonical int `json:"canonicalMethodsUnchanged"`
		Methods   map[string]struct {
			Params []string `json:"params"`
			Result []string `json:"result"`
		} `json:"methods"`
	}
	raw, err := os.ReadFile("testdata/ui-state.snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if got := len(SessionMethods()); got != 25 {
		t.Fatalf("canonical 25 被动过：现在是 %d 条", got)
	}
	if snap.Canonical != 25 {
		t.Fatalf("快照里 canonicalMethodsUnchanged=%d，不是 25", snap.Canonical)
	}
	methods := UIStateMethods()
	if len(methods) != 3 || len(snap.Methods) != 3 {
		t.Fatalf("方法数对不上：UIStateMethods=%d 快照=%d", len(methods), len(snap.Methods))
	}
	want := map[string]struct{ params, result []string }{
		"uiState.get":    {params: []string{"key"}, result: []string{"found", "value?"}},
		"uiState.set":    {params: []string{"key", "value"}, result: []string{"key", "bytes"}},
		"uiState.delete": {params: []string{"key"}, result: []string{"deleted"}},
	}
	for _, method := range methods {
		entry, ok := snap.Methods[string(method)]
		if !ok {
			t.Fatalf("快照缺 %s", method)
		}
		expect, ok := want[string(method)]
		if !ok {
			t.Fatalf("%s 不在期望表里——新增方法必须同时改这条测试", method)
		}
		if !sameStrings(entry.Params, expect.params) {
			t.Errorf("%s 的 params = %v，want %v", method, entry.Params, expect.params)
		}
		if !sameStrings(entry.Result, expect.result) {
			t.Errorf("%s 的 result = %v，want %v", method, entry.Result, expect.result)
		}
	}
	// 反向：快照里多出来的方法同样是错（声明了却没实现）。
	for name := range snap.Methods {
		if !methodIn(UIStateMethods(), name) {
			t.Errorf("快照里有 %s，但 UIStateMethods() 没声明它", name)
		}
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func methodIn(methods []Method, name string) bool {
	for _, method := range methods {
		if string(method) == name {
			return true
		}
	}
	return false
}
