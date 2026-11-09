package webfrontend

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// index 注入：官方 `webserver/index-inject` 的 global 行。
//
// 官方契约（`@deepseek-ai/dsh-host-webserver` lib/index.js 的 renderRow）：
//
//	case "global": return { placement: "head",
//	  markup: `<script>globalThis[${JSON.stringify(row.name).replaceAll("<", "\\u003c")}] = ${
//	    row.value === void 0 ? "undefined" : JSON.stringify(row.value).replaceAll("<", "\\u003c")}<\/script>` }
//
// 也就是说：一行 = 一条 `globalThis["名字"] = <JSON>` 的 <script>，插在开
// `<head …>` 之后。页面侧（`@deepseek-ai/dsh-client-connection` 的 apply()）
// 就是从这个全局读回来：
//
//	const transport = globals.__DSH_TRANSPORT__;
//	installConnection(ctx, { ...transport === void 0 ? {} : { transport },
//	  recovery: resolveConnectionConfig(globals.__DSH_CONNECTION_RECOVERY__), ... });
//
// 两个全局缺了都不会"报错"：`resolveConnectionConfig(undefined)` 直接用默认
// 退避，`transport` 缺省即同源。所以**静默发一份没注入的页面是最坏的结果**
// —— 浏览器里只会看到"能加载但连不上"，没有任何一处说得出原因。
const (
	// GlobalConnectionRecovery 是重连时序（退避与代际就绪超时）。
	GlobalConnectionRecovery = "__DSH_CONNECTION_RECOVERY__"
	// GlobalTransport 是页面传输（streamBaseUrl / ownsHost）。
	GlobalTransport = "__DSH_TRANSPORT__"
)

// RecoveryConfig 是官方 ConnectionRecoveryConfigSchema 的五个字段，逐字照抄
// 官方 `@deepseek-ai/dsh-client-connection` 的 recovery-config：
//
//	backoffBaseMs: z.natural().min(1).default(500)
//	backoffFactor: z.number().min(1).default(2)
//	backoffMaxMs: z.natural().min(1).default(1e4)
//	generationReadyWarnMs: z.natural().min(1).default(3e3)
//	generationReadyTimeoutMs: z.natural().min(1).default(15e3)
//
// 字段名与顺序都要照抄：它是 JSON.stringify 出来的对象字面量，顺序进 golden。
type RecoveryConfig struct {
	BackoffBaseMs            int     `json:"backoffBaseMs"`
	BackoffFactor            float64 `json:"backoffFactor"`
	BackoffMaxMs             int     `json:"backoffMaxMs"`
	GenerationReadyWarnMs    int     `json:"generationReadyWarnMs"`
	GenerationReadyTimeoutMs int     `json:"generationReadyTimeoutMs"`
}

// DefaultRecoveryConfig 是官方 schema 的默认值。
//
// 为什么把默认值写死在这里而不是"不注入"：不注入时页面侧同样得到这套数，
// 但**宿主就再也说不出自己用的是哪一套**了。写出来才可断言、可对比。
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		BackoffBaseMs:            500,
		BackoffFactor:            2,
		BackoffMaxMs:             10_000,
		GenerationReadyWarnMs:    3_000,
		GenerationReadyTimeoutMs: 15_000,
	}
}

// TransportConfig 是 `__DSH_TRANSPORT__` 里**能注入的那部分**字段。
//
// 官方 `ClientTransport` 还有 rpc / fetch / openStream / createWebSocket ——
// 四个都是函数，页面上没法用 JSON 表达，也不该表达：它们缺省时页面走
// `document.baseURI` + 同源 fetch/WebSocket，正是本宿主（同源 serve）要的形态。
// 所以这里只注入官方文档明写的那两个数据字段：
//
//	A static desktop page can provide `__DSH_TRANSPORT__.streamBaseUrl` for the
//	HTTP origin of its owned Host. （官方 dsh-client-connection README）
//
//	globalThis.__DSH_TRANSPORT__ = {ownsHost:true, streamBaseUrl}（官方桌面端）
type TransportConfig struct {
	// OwnsHost 表示"这个页面拥有自己的宿主进程"（loopback 判定用）。
	OwnsHost bool `json:"ownsHost"`
	// StreamBaseURL 是 WebSocket 流的基准 URL（`/api/remote.mux` 相对它解析）。
	StreamBaseURL string `json:"streamBaseUrl"`
}

// Injection 是一条注入行。
//
// 官方行有 global / script / script-src / script-preload / style / html 六种；
// 本包只需要 global（两个全局都是数据）。**故意只实现 global**：其余五种
// 一旦写进来，就会有人用它塞自造脚本，而那正是"不得改写其它内容"要挡的。
//
// 唯一的例外是 boot 三件套（见 boot.go）：它的门面必须是内联 <script>，
// 图必须是 global，就绪信号必须是 body 尾脚本——三条都不是"塞自造脚本"，
// 而是官方 `bootInjections()` 的同一种行。所以那是单独的 Row 通道
// （RenderIndexRows），不是给这里加字段：两条通道各有各的守卫，
// 混在一起等于把"只允许 global"这条约束悄悄去掉。
type Injection struct {
	// Name 是全局名（`globalThis[Name]`）。
	Name string
	// Value 会被 JSON 序列化；nil 渲染成 `undefined`（官方 row.value === void 0）。
	Value any
}

// Row 是官方六种注入行里本包需要的那四种。
//
// 形状照抄官方 `renderRow`（`@deepseek-ai/dsh-host-webserver` lib/index.js
// 的 `lib/types/injections.js` 段）：global / script / script-src /
// script-preload。style 与 html 两种仍然**故意不实现**——它们没有官方
// 启动链在用，实现了只会变成"可以往页面上贴任意 markup"的口子。
type Row struct {
	// Kind 见 RowGlobal 等常量。
	Kind string
	// Placement 是 "head"（默认）或 "body"。
	Placement string
	// Name/Value 仅 global 使用。
	Name  string
	Value any
	// Text 仅 script 使用（内联脚本体，原样，不做任何转义）。
	Text string
	// Src 仅 script-src / script-preload 使用。
	Src string
}

// 官方 kind 词表里本包用到的四个（与 webserver 的 IndexInjection 同名）。
const (
	// RowGlobal 渲染成 `<script>globalThis[名字] = <JSON></script>`。
	RowGlobal = "global"
	// RowScript 渲染成 `<script>正文</script>`。
	RowScript = "script"
	// RowScriptSrc 渲染成 `<script src="…"></script>`。
	RowScriptSrc = "script-src"
	// RowScriptPreload 渲染成 `<link rel="preload" as="script" href="…">`。
	RowScriptPreload = "script-preload"
)

// 官方 placement 词表。
const (
	// PlacementHead 是默认值（官方 head 行）。
	PlacementHead = "head"
	// PlacementBody 是 body 行（官方就绪尾脚本用这一档）。
	PlacementBody = "body"
)

// AnchorError 说明 index.html 缺哪个注入锚点。
//
// 缺锚点必须**响亮失败**：官方实现会退化成"把 head 行贴到文档最前面"，
// 那样注入的内容落进 <html> 与 <head> 之间，浏览器照样解析、页面照样白屏。
// 本宿主选择 500 + 缺哪个锚点，因为"发一份没注入的 200"会让人以为是连接问题。
type AnchorError struct {
	// Anchor 是缺失的锚点字面量（如 `<head>`）。
	Anchor string
}

func (e *AnchorError) Error() string {
	return fmt.Sprintf("index.html 缺少注入锚点 %s（官方 renderIndexInjections 要求文档里有这个开标签）", e.Anchor)
}

// headOpen 是官方锚点正则 `/<head(?:\s[^>]*)?>/i` 的 Go 版本。
//
// 之所以不是字面量 `<head>`：官方 dist 的 index.html 现在就是裸 `<head>`，
// 但带属性（`<head prefix=…>`）是合法 HTML，字面量匹配会在那种文档上
// 静默失败——正是本包要避免的那种失败。
var headOpen = regexp.MustCompile(`(?i)<head(?:\s[^>]*)?>`)

// bodyOpen 是官方 body 锚点正则 `/<body(?:\s[^>]*)?>/i` 的 Go 版本。
//
// 为什么 boot 需要它：官方把"就绪尾脚本"插在**最后一条 body 行之后**，
// 也就是开 `<body …>` 之后（README/`renderIndexInjections`）。入口模组是
// `type="module"`（延迟执行），所以尾脚本只要在文档解析期跑到就足够，
// 而插在 head 里会让"文档已解析完"这个语义变味。
var bodyOpen = regexp.MustCompile(`(?i)<body(?:\s[^>]*)?>`)

// RenderIndex 把注入行渲染成官方 global 行的 markup，插在开 `<head …>` 之后。
//
// 除了这一次插入，**一个字节都不改**：前缀与后缀原样拼回。官方
// `renderIndexInjections` 也是这么做的（`splice(html, open.index+open[0].length, head)`）。
//
// 找不到锚点返回 *AnchorError（调用方据此回 500 并说清缺哪个锚点）。
//
// 这条函数是**历史形状**（只有 head 行、没有就绪尾脚本），保留它是为了
// 既有 golden 与"两条全局逐字节不变"那条判据。需要官方完整的
// head/body 两段渲染时用 RenderIndexRows。
func RenderIndex(html string, injections []Injection) (string, error) {
	loc := headOpen.FindStringIndex(html)
	if loc == nil {
		return "", &AnchorError{Anchor: "<head>"}
	}
	if len(injections) == 0 {
		return html, nil
	}

	var b strings.Builder
	for _, inj := range injections {
		markup, err := renderGlobalRow(inj.Name, inj.Value)
		if err != nil {
			return "", err
		}
		b.WriteString(markup)
	}

	at := loc[1]
	return html[:at] + b.String() + html[at:], nil
}

// RenderIndexRows 按官方 `renderIndexInjections` 的两段语义渲染注入行。
//
// 官方契约（`@deepseek-ai/dsh-host-webserver` lib/index.js：
// `renderIndexInjections`）：
//
//  1. head 行插在开 `<head …>` 之后，body 行插在开 `<body …>` 之后，各自
//     按表内顺序；
//  2. **就绪尾脚本永远追加在最后一条 body 行之后**——它不是可选装饰：
//     入口模组第一句就是 `await globalThis.__DSH_BOOT_READY__?.promise`，
//     页面侧靠它把"注入行已全部落到文档里"这件事变成一个可等待的点。
//     官方那一条是
//     `<script>(globalThis.__DSH_BOOT_READY__ ??= Promise.withResolvers()).resolve()</script>`；
//     本包用等价的 `??=` + 手写 deferred（`promise`/`resolve`/`reject` 三个
//     字段与 Promise.withResolvers 一致），因为 `Promise.withResolvers` 是
//     ES2024，而页面侧只写了 `?.promise`——退化时是静默不等待，不是报错。
//     这里多一层兜底是为了"旧引擎上也照样拿到就绪信号"，不改语义。
//  3. 锚点缺失时**官方会退化**（head 行贴到文档最前面、body 行贴到末尾），
//     本包不退化成"看起来正常"：缺锚点返回 *AnchorError，调用方回 500。
//     静默插错位置的结果是页面白屏而没有任何一处报错，那正是本包要挡的。
func RenderIndexRows(html string, rows []Row) (string, error) {
	head, body, err := renderRows(rows)
	if err != nil {
		return "", err
	}
	body += readyMarkup
	out := html
	if head != "" {
		loc := headOpen.FindStringIndex(out)
		if loc == nil {
			return "", &AnchorError{Anchor: "<head>"}
		}
		out = out[:loc[1]] + head + out[loc[1]:]
	}
	if body != "" {
		loc := bodyOpen.FindStringIndex(out)
		if loc == nil {
			return "", &AnchorError{Anchor: "<body>"}
		}
		out = out[:loc[1]] + body + out[loc[1]:]
	}
	return out, nil
}

// readyMarkup 是官方就绪尾脚本（见 RenderIndexRows 的注释）。
//
// 用 `??=` 而不是直接赋值：官方同一句里也是 `??=`——页面若先自己建过
// deferred（异步 bootstrap 那一档），这里只负责 resolve，不覆盖它。
const readyMarkup = `<script>(globalThis.__DSH_BOOT_READY__ ??= (()=>{let resolve,reject;` +
	`const promise=new Promise((res,rej)=>{resolve=res;reject=rej});return{promise,resolve,reject}})()).resolve()</script>`

// renderRows 把行渲染成 head / body 两段 markup（就绪尾脚本由调用方追加）。
func renderRows(rows []Row) (head, body string, err error) {
	var h, b strings.Builder
	for _, row := range rows {
		markup, err := renderRow(row)
		if err != nil {
			return "", "", err
		}
		switch row.Placement {
		case "", PlacementHead:
			h.WriteString(markup)
		case PlacementBody:
			b.WriteString(markup)
		default:
			return "", "", fmt.Errorf("注入行 %q 的 placement 只能是 head 或 body，收到 %q", row.Kind, row.Placement)
		}
	}
	return h.String(), b.String(), nil
}

// renderRow 是官方 renderRow 的 Go 版本（只覆盖本包实现的四种）。
func renderRow(row Row) (string, error) {
	switch row.Kind {
	case RowGlobal:
		return renderGlobalRow(row.Name, row.Value)
	case RowScript:
		// 官方 script 行是**原文拼接**（`<script>${row.text}</script>`），
		// 所以脚本体里出现 `</script>` 会提前闭合。这里不转义，而是拒绝：
		// 一条会截断自己的注入行是写错了，不是需要"兼容"的输入。
		if strings.Contains(strings.ToLower(row.Text), "</script") {
			return "", fmt.Errorf("注入行 script 的正文含 </script，会把脚本块提前闭合；拒绝注入")
		}
		return "<script>" + row.Text + "</script>", nil
	case RowScriptSrc:
		return `<script src="` + escapeHTMLAttr(row.Src) + `"></script>`, nil
	case RowScriptPreload:
		return `<link rel="preload" as="script" href="` + escapeHTMLAttr(row.Src) + `">`, nil
	default:
		return "", fmt.Errorf("未知注入行 kind %q（本包只实现 global / script / script-src / script-preload）", row.Kind)
	}
}

// renderGlobalRow 渲染官方 global 行。
func renderGlobalRow(name string, value any) (string, error) {
	encodedName, err := encodeJSON(name)
	if err != nil {
		return "", fmt.Errorf("注入行 %q 的名字不能序列化: %w", name, err)
	}
	// 官方 row.value === void 0 时渲染字面量 undefined（不是 "null"）。
	encodedValue := "undefined"
	if value != nil {
		encodedValue, err = encodeJSON(value)
		if err != nil {
			return "", fmt.Errorf("注入行 %q 的值不能序列化: %w", name, err)
		}
	}
	return "<script>globalThis[" + encodedName + "] = " + encodedValue + "</script>", nil
}

// escapeHTMLAttr 是官方 escapeHtmlAttribute 的 Go 版本。
func escapeHTMLAttr(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

// encodeJSON 序列化一个值，并把 `<` 转义成 `\u003c`（官方 replaceAll("<", "\\u003c")）。
//
// 为什么要转义：值是内联进 <script> 的，一个字符串里的 `</script>` 会提前
// 闭合脚本块，剩下的字节就变成了可执行内容。encoding/json 默认已经转义
// `<`/`>`/`&`，这里再兜一次是因为两边都改过默认值时，转义没了也看不出来。
func encodeJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(raw), "<", `\u003c`), nil
}
