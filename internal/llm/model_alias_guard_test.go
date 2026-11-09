package llm

// 模型 id 归一守卫（t7）。
//
// # 它拦什么
//
// 「什么叫同一个模型」只能有一份判据。改前的形态是每个使用点各写一段前缀匹配：
// `ResolveModelCapacity` 只认精确 id（`group/deepseek-v4.1-flash-1017`），
// `CatalogContextWindow` 也是 `entry.ID != modelID` 直接比——于是不带 `group/`
// 前缀的同一模型会掉进「未声明自定义模型」分支：窗口恰好同为 1M（碰巧），
// 但 `MaxOutput` 从 0 变成 128_000，请求里平白多出 `max_tokens`。
//
// 现在两处共用 `ResolveModelID`（`model_alias.go`）。本守卫钉两件事：
//
//  1. **实现只有一份**：非测试源码里 `HasSuffix` 只允许出现在 `model_alias.go`；
//  2. **两个消费者判据一致**：同一个别名在容量表与目录里必须归一到同一个 id
//     （目录有该 id 的条目时，别名查询与规范 id 查询必须给出同一个窗口值）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardModelAliasHasSingleImplementation(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("扫描 internal/llm 失败: %v", err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		src := string(raw)
		// 归一实现的形状是 `suffix := "/" + model` + `HasSuffix(id, suffix)`。
		// 只拦这个形状：`HasSuffix(baseURL, "/")`、`HasSuffix(path, "/models")` 一类
		// 路径拼接与模型 id 无关，不该被误伤。
		if !strings.Contains(src, `"/" +`) && !strings.Contains(src, `+"/"`) {
			continue
		}
		if filepath.Base(name) != "model_alias.go" {
			t.Fatalf("%s 里出现了 \"/\" + model 形式的后缀拼接：模型 id 的归一只能走 ResolveModelID（model_alias.go）。\n"+
				"每个调用点各写一段匹配，就会出现两套「什么叫同一个模型」的判据——"+
				"容量表认、目录不认，或者反过来。请改成调用 ResolveModelID。", name)
		}
	}
}

// 容量表与目录必须对同一个别名给出同一个规范 id。
func TestGuardCapacityAndCatalogAgreeOnAlias(t *testing.T) {
	const alias = "deepseek-v4.1-flash-1017"
	const canonical = "group/deepseek-v4.1-flash-1017"

	// 1) 内置表：无前缀别名必须归一到内置的规范 id。
	id, ok := ResolveModelID(alias, builtInModelIDs())
	if !ok || id != canonical {
		t.Fatalf("ResolveModelID(%q, builtInModelIDs()) = (%q, %v)，期望 (%q, true)", alias, id, ok, canonical)
	}

	// 2) 目录：同一别名与规范 id 必须给出同一个窗口值。
	view := ModelCatalogView{Models: []json.RawMessage{
		json.RawMessage(`{"id":"` + canonical + `","contextWindow":1000000}`),
		json.RawMessage(`{"id":"other/model","contextWindow":65536}`),
	}}
	if got, want := CatalogContextWindow(view, alias), CatalogContextWindow(view, canonical); got != want || got != 1_000_000 {
		t.Fatalf("CatalogContextWindow(别名) = %d，CatalogContextWindow(规范 id) = %d（期望都是 1000000）——"+
			"两个消费者对「同一个模型」的判据已经漂了", got, want)
	}
}

// 别名归一必须保持「不猜」：多个前缀候选并存时返回 false，不能随便挑一个。
func TestGuardAliasAmbiguityIsNotGuessed(t *testing.T) {
	known := []string{"group/deepseek-v4.1-flash-1017", "minekuai/deepseek-v4.1-flash-1017"}
	if id, ok := ResolveModelID("deepseek-v4.1-flash-1017", known); ok {
		t.Fatalf("两个前缀候选并存时不许猜，得到 (%q, %v)", id, ok)
	}
}
