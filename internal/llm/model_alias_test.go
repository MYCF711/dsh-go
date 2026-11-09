package llm

import "testing"

// ResolveModelID 是容量/窗口解析共用的唯一别名归一入口。这里钉住它的四类
// 语义：精确优先、唯一后缀命中、候选不唯一不猜、非斜杠边界不算候选。
func TestResolveModelID(t *testing.T) {
	known := []string{
		"group/deepseek-v4.1-flash-1017",
		"gw/model-snake",
		"a/ambig",
		"b/ambig",
		"suffix/notm",
		"plain",
	}
	tests := []struct {
		name   string
		model  string
		wantID string
		wantOK bool
	}{
		{
			name:   "精确命中优先，原样返回",
			model:  "group/deepseek-v4.1-flash-1017",
			wantID: "group/deepseek-v4.1-flash-1017",
			wantOK: true,
		},
		{
			name:   "无前缀唯一后缀命中",
			model:  "deepseek-v4.1-flash-1017",
			wantID: "group/deepseek-v4.1-flash-1017",
			wantOK: true,
		},
		{
			name:   "唯一后缀也可以是带斜杠输入的后半段",
			model:  "model-snake",
			wantID: "gw/model-snake",
			wantOK: true,
		},
		{
			name:   "两个前缀候选同时存在不猜",
			model:  "ambig",
			wantID: "ambig",
			wantOK: false,
		},
		{
			name:   "后缀不在斜杠边界不算候选",
			model:  "m",
			wantID: "m",
			wantOK: false,
		},
		{
			name:   "目录里没有这个模型就原样返回",
			model:  "other/model",
			wantID: "other/model",
			wantOK: false,
		},
		{
			name:   "空模型不归一",
			model:  "",
			wantID: "",
			wantOK: false,
		},
		{
			name:   "整串等于别的 id 的后缀但精确命中自身",
			model:  "plain",
			wantID: "plain",
			wantOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotOK := ResolveModelID(tc.model, known)
			if gotID != tc.wantID || gotOK != tc.wantOK {
				t.Fatalf("ResolveModelID(%q) = (%q, %v)，期望 (%q, %v)", tc.model, gotID, gotOK, tc.wantID, tc.wantOK)
			}
		})
	}
}

// 精确命中优先于后缀：known 同时有 "plain" 与 "prefix/plain" 时，输入 "plain"
// 必须仍是 "plain"，不能被归一到带前缀的那条。
func TestResolveModelIDExactBeatsSuffix(t *testing.T) {
	known := []string{"prefix/plain", "plain"}
	if id, ok := ResolveModelID("plain", known); !ok || id != "plain" {
		t.Fatalf("ResolveModelID(\"plain\") = (%q, %v)，期望 (\"plain\", true)", id, ok)
	}
}
