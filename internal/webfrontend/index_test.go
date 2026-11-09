package webfrontend

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// officialIndex 是官方 dist 的 index.html（18 行，逐字取自
// app.asar 的 `@deepseek-ai/dsh-web-frontend/dist/index.html`）。
//
// 用真文件而不是拼一份"差不多"的 HTML：注入锚点是 `<head>`，任何一份
// 自造的极简 HTML 都能过，而真文件里的 `<script type="module" …>` 与
// 两个 CSS link 都在 head 里——插入点错一字节就会让 970 KB 的入口
// 变成不可执行的文本。
const officialIndex = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <link rel="manifest" href="./manifest.webmanifest" />
    <title>DeepSeek Harness</title>
    <script type="module" crossorigin src="./assets/index-BgUyXZcc.js"></script>
    <link rel="stylesheet" crossorigin href="./assets/index-lh_YM6sT.css">
  </head>
  <body>
    <div id="root"></div>
  </body>
</html>
`

// TestRenderIndexMatchesOfficialGlobalRow 钉住注入行的字节形状。
//
// 官方 renderRow 的 global 分支是：
//
//	<script>globalThis[<name>] = <value></script>
//
// 这里把两个全局一起渲染，断言的是**完整 markup**——包括两行之间的
// 分隔符（没有）、`globalThis[` 的方括号、以及 JSON 的字段顺序。
func TestRenderIndexMatchesOfficialGlobalRow(t *testing.T) {
	const wantMarkup = `<script>globalThis["__DSH_CONNECTION_RECOVERY__"] = ` +
		`{"backoffBaseMs":500,"backoffFactor":2,"backoffMaxMs":10000,"generationReadyWarnMs":3000,"generationReadyTimeoutMs":15000}` +
		`</script><script>globalThis["__DSH_TRANSPORT__"] = ` +
		`{"ownsHost":true,"streamBaseUrl":"http://127.0.0.1:52341"}</script>`

	got, err := RenderIndex(officialIndex, []Injection{
		{Name: GlobalConnectionRecovery, Value: DefaultRecoveryConfig()},
		{Name: GlobalTransport, Value: TransportConfig{OwnsHost: true, StreamBaseURL: "http://127.0.0.1:52341"}},
	})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 插入点：开 <head> 之后，其余一个字节都不许改。
	const headOpen = "<head>"
	at := strings.Index(officialIndex, headOpen) + len(headOpen)
	want := officialIndex[:at] + wantMarkup + officialIndex[at:]
	if got != want {
		t.Fatalf("注入后的 index 与官方 markup 不一致\n got: %s\nwant: %s", got, want)
	}
	// 原文（除插入点外）必须逐字节保留：官方契约是"一次字符串替换"。
	if !strings.HasPrefix(got, officialIndex[:at]) {
		t.Fatalf("前缀被改写")
	}
	if !strings.HasSuffix(got, officialIndex[at:]) {
		t.Fatalf("后缀被改写")
	}
}

// TestRenderIndexAnchorWithAttributes 钉住锚点正则：带属性的 <head> 也要命中。
//
// 官方正则是 `/<head(?:\s[^>]*)?>/i`；写成字面量 `<head>` 的实现在
// 这种文档上会**静默**把注入贴到文档最前面（官方缺锚点时的退化行为），
// 页面照样白屏，而没有任何一处报错。
func TestRenderIndexAnchorWithAttributes(t *testing.T) {
	const html = "<!doctype html>\n<html>\n<HEAD profile=\"http://a.b\">\n<body></body>\n</html>\n"
	got, err := RenderIndex(html, []Injection{{Name: GlobalTransport, Value: TransportConfig{StreamBaseURL: "http://x"}}})
	if err != nil {
		t.Fatalf("带属性的 <HEAD> 必须被认成锚点: %v", err)
	}
	want := "<!doctype html>\n<html>\n<HEAD profile=\"http://a.b\">" +
		`<script>globalThis["__DSH_TRANSPORT__"] = {"ownsHost":false,"streamBaseUrl":"http://x"}</script>` +
		"\n<body></body>\n</html>\n"
	if got != want {
		t.Fatalf("注入点不在开 <HEAD …> 之后\ngot:  %q\nwant: %q", got, want)
	}
}

// TestRenderIndexMissingAnchorFailsLoudly 钉住"缺锚点响亮失败"。
//
// 官方在缺 head 时会退化成 `head + html`（贴到文档最前面）。本宿主的
// 契约相反：宁可 500 并说清缺哪个锚点，也不发一份没注入的 200——
// 后者在浏览器里就是白屏，看不出任何原因。
func TestRenderIndexMissingAnchorFailsLoudly(t *testing.T) {
	_, err := RenderIndex("<div id=\"root\"></div>", []Injection{{Name: GlobalTransport, Value: 1}})
	if err == nil {
		t.Fatalf("缺 <head> 时必须报错")
	}
	var anchor *AnchorError
	if !errors.As(err, &anchor) {
		t.Fatalf("错误类型必须是 *AnchorError，实际 %T: %v", err, err)
	}
	if anchor.Anchor != "<head>" {
		t.Fatalf("必须说清缺哪个锚点，实际 %q", anchor.Anchor)
	}
	if !strings.Contains(err.Error(), "<head>") {
		t.Fatalf("错误文本里必须出现锚点字面量: %q", err.Error())
	}
}

// TestRenderIndexEscapesScriptClose 钉住值里的 `</script>` 转义。
//
// 不转义的话，一个字符串值就能提前闭合脚本块、把后面的 JSON 当代码跑。
// 官方做的是 `replaceAll("<", "\\u003c")`，本包逐字对齐。
func TestRenderIndexEscapesScriptClose(t *testing.T) {
	got, err := RenderIndex("<head></head>", []Injection{{Name: GlobalTransport, Value: TransportConfig{StreamBaseURL: "http://x/</script><script>alert(1)"}}})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Count(got, "</script>") != 1 {
		t.Fatalf("值里的 </script> 必须被转义，实际: %s", got)
	}
	if !strings.Contains(got, `\u003c/script`) {
		t.Fatalf("转义必须是 \\u003c，实际: %s", got)
	}
}

// TestRecoveryConfigDefaultsMatchOfficialSchema 钉住官方五个默认值。
//
// 数字改了不会有任何测试红，只会在真机上表现为"重连退避变慢/变快"，
// 那是最难归因的一类回归——所以逐字段钉死。
func TestRecoveryConfigDefaultsMatchOfficialSchema(t *testing.T) {
	got, err := json.Marshal(DefaultRecoveryConfig())
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	const want = `{"backoffBaseMs":500,"backoffFactor":2,"backoffMaxMs":10000,"generationReadyWarnMs":3000,"generationReadyTimeoutMs":15000}`
	if string(got) != want {
		t.Fatalf("默认重连时序与官方 schema 不一致\ngot:  %s\nwant: %s", got, want)
	}
}

// TestTransportConfigJSONFieldNames 钉住 __DSH_TRANSPORT__ 的两个字段名。
//
// 官方页面侧读的是 `transport?.streamBaseUrl` 与 `transport?.ownsHost === true`；
// 名字打错不会报错，只会**静默**退回 document.baseURI 与同源判定。
func TestTransportConfigJSONFieldNames(t *testing.T) {
	got, err := json.Marshal(TransportConfig{OwnsHost: true, StreamBaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	const want = `{"ownsHost":true,"streamBaseUrl":"http://127.0.0.1:1"}`
	if string(got) != want {
		t.Fatalf("字段名/顺序与官方不一致\ngot:  %s\nwant: %s", got, want)
	}
}
