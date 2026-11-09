package webfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 真实网页不是纯文本：一份带 title/heading/链接/脚本/样式的 HTML 夹具，
// 工具交给模型的必须是**可读正文**，不是把标签原样倒出来。
//
// 这条用例在旧实现上是红的：旧实现 Content-Type 一律不管，直接把 HTML 字节
// 当正文返回（模型读到的是一堆 <script>/<style>/<h1>）。
func TestFetchHTMLRendersReadableMarkdown(t *testing.T) {
	const page = `<!doctype html>
<html lang="zh"><head>
<meta charset="utf-8">
<title>HS-12 官方同步</title>
<style>.hidden{display:none}</style>
<script>var leak = "should-not-appear";</script>
</head><body>
<h1>官方同步任务书</h1>
<p>先读 <a href="/docs/taskbook">任务书</a>，再改 <code>internal/llm</code>。</p>
<ul><li>HTML 转文本</li><li>实体解码 &amp; 不泄露脚本</li></ul>
<div style="display:none">隐藏内容不该出现</div>
<noscript>无脚本提示</noscript>
<footer>版权 &copy; 2026</footer>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	defer srv.Close()

	tool := &Tool{Client: srv.Client()}
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`/page"}`))
	if err != nil {
		t.Fatalf("取网页失败: %v", err)
	}
	text := res.Text
	t.Logf("HTML 夹具渲染结果:\n%s", text)
	for _, want := range []string{"官方同步任务书", "任务书", "HTML 转文本", "实体解码 & 不泄露脚本", "版权 © 2026"} {
		if !strings.Contains(text, want) {
			t.Fatalf("正文缺少 %q:\n%s", want, text)
		}
	}
	for _, banned := range []string{"<h1>", "<script", "should-not-appear", "<style", ".hidden", "隐藏内容不该出现", "无脚本提示", "<li>"} {
		if strings.Contains(text, banned) {
			t.Fatalf("正文不该出现 %q（说明 HTML 没被转成可读文本）:\n%s", banned, text)
		}
	}
	if !strings.Contains(text, "/docs/taskbook") {
		t.Fatalf("链接目标丢了:\n%s", text)
	}
}

// 同一份夹具走 Data 投影时，必须仍然报告 html 语义与真实字节数（供上层判断）。
func TestFetchHTMLKeepsStructuredKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body><h2>标题</h2><p>正文</p></body></html>`))
	}))
	defer srv.Close()

	tool := &Tool{Client: srv.Client()}
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`/"}`))
	if err != nil {
		t.Fatal(err)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 形状变了: %T", res.Data)
	}
	if d.Status != 200 || d.Bytes == 0 {
		t.Fatalf("Data 不对: %+v", d)
	}
	if strings.Contains(res.Text, "<h2>") {
		t.Fatalf("HTML 不应原样进入正文: %q", res.Text)
	}
}
