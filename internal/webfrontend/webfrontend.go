// Package webfrontend 由本宿主托管官方前端 dist。
//
// 官方 Web 壳是一份纯静态 SPA（`@deepseek-ai/dsh-web-frontend/dist`），
// 由宿主进程自己 serve：dist 根与 `index.html` 渲染成注入后的入口文档，
// 其余文件按扩展名直发（`@deepseek-ai/dsh-host-frontend-static` 的契约）。
// 本包只做这一件事，但它有一条不可让步的要求：**资产必须流式发送**。
//
// 为什么流式是硬要求：官方 dist 里有 970 KB 的入口 bundle 与几百 KB 的
// 语言分包，`os.ReadFile` 会把整份文件读进堆——并发打开几个页面就是几份
// 副本，而这份内存从头到尾只有"写给 socket"一个用途。`http.ServeContent`
// 收的是 `io.ReadSeeker`，`*os.File` 天然满足：内核页缓存负责缓存，进程
// 堆里只留一个 32 KiB 的拷贝缓冲（见 TestAssetStreamsWithoutFullAllocation）。
//
// 唯一整份读入内存的是 `index.html`：注入必须做一次字符串替换（官方
// `renderIndexInjections` 同样是 `readFile(index, "utf8")`），而它只有几百字节。
//
// 默认关闭：`DSHD_FRONTEND_DIR` 未设置或目录不存在时，`Wrap` 返回 nil，
// 上游 Start 用的就是原来的 handler——**响应逐字节不变**。
package webfrontend

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// IndexFile 是 dist 的入口文件名（官方 `distIndex` 的 basename）。
const IndexFile = "index.html"

// Config 是建一个前端要的东西。
type Config struct {
	// Dir 是 dist 根目录（含 index.html）。必填。
	Dir string
	// Recovery 是 `__DSH_CONNECTION_RECOVERY__` 的值；nil = 官方默认
	// （DefaultRecoveryConfig）。
	Recovery *RecoveryConfig
	// StreamBaseURL 按请求算 `__DSH_TRANSPORT__.streamBaseUrl`；nil 时用
	// `"http://" + r.Host`（本宿主只监听 loopback 明文，没有 TLS 分支）。
	StreamBaseURL func(*http.Request) string
	// Boot 是官方 SPA 启动三件套（`__DSH_BOOT__` / `__ModuleLoader__` /
	// `__DSH_BOOT_READY__`）的开关；nil = 默认开启（按 dist 里的
	// `dsh-boot.json` 组装，没有它就是空 roster + 响亮诊断）。
	Boot *BootOptions
}

// Frontend 是一份 dist 的托管实例。零值不可用，用 New 建。
//
// 字段在 New 里全部定死，之后只读：并发请求共享同一个实例，没有一把锁
// 也没有一次写——这是"静态站不该有的状态"的最小集合。
type Frontend struct {
	// root 是 **符号链接求值后** 的绝对 dist 根。围栏必须拿它比。
	root string
	// indexPath 是 root/index.html 的绝对路径（官方 distIndex）。
	indexPath string
	// indexHTML 是原始 index.html 文本，进程启动时读一次。
	indexHTML string
	// recovery 是注入进页面的重连时序。
	recovery RecoveryConfig
	// streamBaseURL 见 Config.StreamBaseURL。
	streamBaseURL func(*http.Request) string
	// boot 是启动三件套的载荷；nil = 关掉（只有两条全局的历史形状）。
	boot *bootPayload
}

// New 校验并加载一份 dist。
//
// 这里要把所有"起来之后才发现"的错误提前到启动：目录不是目录、没有
// index.html、index.html 缺注入锚点。第三条尤其重要——见 AnchorError。
func New(cfg Config) (*Frontend, error) {
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		return nil, fmt.Errorf("webfrontend: 缺 dist 目录")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("webfrontend: dist 目录 %q 求绝对路径失败: %w", dir, err)
	}
	// 符号链接必须在这里求值一次：macOS 的 /tmp 就是 /private/tmp 的软链，
	// 不求值的话每个请求的围栏都会把合法路径判成越界（403），而且只在
	// 那种机器上复现。
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("webfrontend: dist 目录 %q 不可用: %w", dir, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("webfrontend: dist 目录 %q 不可用: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("webfrontend: %q 不是目录", root)
	}

	indexPath := filepath.Join(root, IndexFile)
	indexInfo, err := os.Stat(indexPath)
	if err != nil {
		return nil, fmt.Errorf("webfrontend: dist 里没有 %s: %w", IndexFile, err)
	}
	if !indexInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("webfrontend: %s 不是普通文件", indexPath)
	}
	// 唯一允许的整份读：注入是字符串替换，没有第二个办法。
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("webfrontend: 读 %s 失败: %w", indexPath, err)
	}

	recovery := DefaultRecoveryConfig()
	if cfg.Recovery != nil {
		recovery = *cfg.Recovery
	}
	baseURL := cfg.StreamBaseURL
	if baseURL == nil {
		baseURL = defaultStreamBaseURL
	}

	// boot 三件套：清单在 New 里读并校验（形状、dist 内路径、文件存在），
	// 任何一条不成立都在这里炸——起不来的宿主比"起来了但页面停在
	// facade missing"好查得多。
	bootOptions := BootOptions{}
	if cfg.Boot != nil {
		bootOptions = *cfg.Boot
	}
	var boot *bootPayload
	if !bootOptions.Disabled {
		boot, err = loadBoot(root, bootOptions)
		if err != nil {
			return nil, err
		}
	}

	front := &Frontend{
		root:          root,
		indexPath:     indexPath,
		indexHTML:     string(raw),
		recovery:      recovery,
		streamBaseURL: baseURL,
		boot:          boot,
	}
	// 启动就渲染一次：锚点缺失要在进程起来之前炸，不能等浏览器打开页面。
	if _, err := front.render("http://127.0.0.1:0"); err != nil {
		return nil, err
	}
	return front, nil
}

// BootDescription 说明这次注入的启动三件套是什么（给启动日志用）。
//
// 返回 false 表示 boot 三件套被关掉了（只有两条全局的历史形状）。
// 为什么要把这件事说出来：一个"页面停在加载页"的现场，第一句该问的就是
// "manifest 读了没有、几条 entry"——没有这句话只能靠翻 dist 猜。
func (f *Frontend) BootDescription() (string, bool) {
	if f == nil || f.boot == nil {
		return "", false
	}
	return f.boot.description(), true
}

// defaultStreamBaseURL 用请求的 Host 拼 origin。
//
// 为什么不用 `r.TLS != nil` 选 https：本宿主只监听 loopback 明文（transport
// 的 Start 就是 net.Listen + Serve，没有 TLS 分支），而"看起来支持 TLS"
// 的代码会让部署方以为写了就能用。真要 TLS，先有监听再谈这一行。
func defaultStreamBaseURL(r *http.Request) string {
	return "http://" + r.Host
}

// Wrap 把前端套在 next 外面。
//
// 席位语义照抄官方 frontend-static：静态站只回答**没有具名路由命中**的
// 请求。具名路由（/connect、/ws、/debug/stats、/api/*）原样交回 next，
// 否则一次 `/api/session.list` 会被静态站当成文件路径、回 404 空体，
// 界面看到的就是"接口全没了"。
//
// f 为 nil（未启用）时**原样返回 next**：这是"默认关闭时既有行为逐字节
// 不变"的唯一落点，一个包装层都不留。
func (f *Frontend) Wrap(next http.Handler) http.Handler {
	if f == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if namedRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// GET /?token=<launch>：官方 BrowserAuth.authorizeIndex 的语义是
		// "根路径带一次性 token → 303 + Set-Cookie + Location ./"。
		// 本宿主已经在 /connect 里实现了**完全一样**的一套（一次性 token、
		// HttpOnly cookie、303 到 /），所以这里把路径改写成 /connect 交给
		// 同一个 handler，而不是抄第二份 mint 逻辑——两份实现迟早会漂开，
		// 而漂开的那一份是"cookie 发了但认证不过"。
		//
		// 只认恰好一个 token 参数（官方 tokens.length === 1）：多给几个
		// 就是可疑输入，按普通 index 请求走，不换 cookie。
		if r.URL.Path == "/" && r.Method == http.MethodGet && len(r.URL.Query()["token"]) == 1 {
			rewritten := *r
			clone := *r.URL
			clone.Path = "/connect"
			rewritten.URL = &clone
			next.ServeHTTP(w, &rewritten)
			return
		}
		f.serve(w, r)
	})
}

// ServeHTTP 让 Frontend 自己就是一个 handler（等价于 Wrap(nil 兜底)）。
//
// 具名路由在这里没有下一棒，所以**必须**先交出去之前就拒掉：直接落到
// 静态站会把 /api/* 当文件路径。生产路径走 Wrap；这个方法给"只想挂
// 静态站"的测试与嵌入式用法。
func (f *Frontend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f == nil || namedRoute(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	f.serve(w, r)
}

// namedRoute 是 transport.Server.ServeHTTP 自己拥有的路径。
//
// 与 `internal/transport/server.go` 的 switch 保持一字不差；多一条会
// 让静态站吞掉接口，少一条会把 /api 当文件。
func namedRoute(path string) bool {
	switch path {
	case "/connect", "/ws", "/debug/stats":
		return true
	}
	return strings.HasPrefix(path, "/api/")
}

// serve 是静态站本体。
func (f *Frontend) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// 官方：没有命中具名路由的非 GET/HEAD → 405（不是 404，也不是 500）。
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	target, ok := f.resolve(r.URL.Path)
	if !ok {
		// 遍历是攻击形状，不是"页面不存在"：403 与 404 在这条防线上的
		// 区别是"你越界了"和"这里没东西"。
		w.WriteHeader(http.StatusForbidden)
		return
	}

	// dist 根本身与 index.html 都渲染入口文档（官方 target === distRoot ||
	// target === distIndex）。顺带一个效果：`/` 与 `/index.html` 得到同一份
	// 注入后的 HTML，页面里的相对路径怎么算都对。
	if target == f.root || target == f.indexPath {
		f.serveIndex(w, r)
		return
	}
	f.serveAsset(w, r, target)
}

// serveIndex 渲染并发出入口文档。
func (f *Frontend) serveIndex(w http.ResponseWriter, r *http.Request) {
	body, err := f.render(f.streamBaseURL(r))
	if err != nil {
		// 响亮失败：500 + 缺哪个锚点。绝不静默发一份没注入的页面。
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "frontend-static: %v\n", err)
		return
	}
	// 官方：渲染后的 index **不带任何缓存头**（它每次请求都可能不同）。
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.WriteString(w, body)
}

// render 把两条全局 + boot 三件套注入进原始 index.html。
//
// 两条通道分开的理由见 index.go 的 RenderIndex/RenderIndexRows：boot 关掉时
// 走历史形状（RenderIndex，逐字节不变），开时走官方的 head/body 两段语义
// （多了就绪尾脚本）。顺序照官方注入表的顺序：boot 行在前，两条全局在后
// （官方也是 `__DSH_BOOT__` 先于 `__DSH_CONNECTION_RECOVERY__`）。
func (f *Frontend) render(streamBaseURL string) (string, error) {
	globals := []Injection{
		{Name: GlobalConnectionRecovery, Value: f.recovery},
		{Name: GlobalTransport, Value: TransportConfig{OwnsHost: true, StreamBaseURL: streamBaseURL}},
	}
	if f.boot == nil {
		return RenderIndex(f.indexHTML, globals)
	}
	rows := f.boot.rows()
	for _, inj := range globals {
		rows = append(rows, Row{Kind: RowGlobal, Placement: PlacementHead, Name: inj.Name, Value: inj.Value})
	}
	return RenderIndexRows(f.indexHTML, rows)
}

// serveAsset 发一个静态文件。
func (f *Frontend) serveAsset(w http.ResponseWriter, r *http.Request, target string) {
	file, info, encoding, err := f.openAsset(target, r.Header.Get("Accept-Encoding"))
	if err != nil {
		// 官方：dist 内不存在、是目录、或不是普通文件 → **空 404**。
		// 特别注意这里**不做 SPA 回退**：错拼的路径要是一条显式失败，
		// 而不是悄悄给一份 index——那会让"资源 404"永远查不出来。
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer func() { _ = file.Close() }()

	w.Header().Set("Content-Type", contentType(target))
	// 除了 index，每个文件都让浏览器回来验证（官方 cache-control: no-cache）。
	// 资产名带 Vite 内容哈希，所以平时是 304 的往返，不是重新下载。
	w.Header().Set("Cache-Control", "no-cache")
	// ETag 用 (大小, mtime, 编码) 而不是官方那份 SHA-1：算哈希要把整份
	// 文件读一遍，正好是本包要避免的那件事。弱 ETag 的语义是"字节可能
	// 不同但等价"，对本场景足够；ServeContent 会拿它应答 If-None-Match。
	w.Header().Set("ETag", etag(info, encoding))
	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	// 有预压变体时同一 URL 会随 Accept-Encoding 变字节，必须 Vary。
	if encoding != "" || f.hasPrecompressed(target) {
		w.Header().Set("Vary", "Accept-Encoding")
	}
	// 流式发送：name 只用于 Content-Type 兜底（上面已显式设过），
	// seeker 是 *os.File，Range/If-Range/If-None-Match 全由它处理。
	http.ServeContent(w, r, filepath.Base(target), info.ModTime(), file)
}

// openAsset 打开要发的文件，优先用同名预压变体。
//
// 为什么优先预压而不是动态压缩：官方 dist 的部署方（或构建产物）本来
// 就会带 .gz/.br，现压一遍等于每个请求都花一次 CPU、分配一次压缩缓冲，
// 换来的是**完全相同**的字节。任务书也明确禁止动态压缩。
//
// 返回的 info 是**实际发出的那份文件**的（大小/mtime 属于压缩体，ETag
// 与 Content-Length 必须与之一致）。
func (f *Frontend) openAsset(target, acceptEncoding string) (*os.File, os.FileInfo, string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, nil, "", err
	}
	if !info.Mode().IsRegular() {
		// 目录（含符号链接指向的目录）不列表：官方是空 404。
		return nil, nil, "", os.ErrNotExist
	}
	// br 比 gzip 更小，优先；两者都在 dist 里时按客户端支持挑一个。
	for _, variant := range []struct{ suffix, encoding string }{
		{".br", "br"},
		{".gz", "gzip"},
	} {
		if !acceptsEncoding(acceptEncoding, variant.encoding) {
			continue
		}
		file, variantInfo, err := openRegular(target + variant.suffix)
		if err != nil {
			continue
		}
		return file, variantInfo, variant.encoding, nil
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, nil, "", err
	}
	return file, info, "", nil
}

// hasPrecompressed 判断 dist 里有没有这个文件的预压变体（给 Vary 用）。
func (f *Frontend) hasPrecompressed(target string) bool {
	for _, suffix := range []string{".br", ".gz"} {
		if info, err := os.Stat(target + suffix); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// openRegular 打开一个必须是普通文件的路径。
func openRegular(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, os.ErrNotExist
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return file, info, nil
}

// resolve 把 URL 路径映射到 dist 内的绝对路径，越界返回 false。
//
// 两道围栏，缺一不可：
//
//  1. 归一化后必须落在 root 内（官方 `target !== distRoot && !startsWith(distRoot + sep)`）。
//     用 `sep` 而不是 `/`：Windows 上 `resolve()` 给的是反斜杠路径，用 `/`
//     会把每一条合法子路径都判成越界。
//  2. 符号链接求值后**还要**落在 root 内。第 1 道挡不住"dist 里放一个
//     指向 /etc 的软链"——路径本身在根内，读出去的却是根外的文件。
//     官方静态站不做这一步（Node 的 readFile 跟着软链走），本仓的安全
//     要求是"只允许 dist 目录内路径"，所以必须做。
func (f *Frontend) resolve(urlPath string) (string, bool) {
	// filepath.Join 会顺手 Clean 掉 `..` 与重复分隔符，和官方
	// `resolve(normalize(join(distRoot, pathname)))` 等价。
	target := filepath.Join(f.root, filepath.FromSlash(urlPath))
	if !within(f.root, target) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		// 不存在不是越界：交给 serveAsset 回 404。
		// 但注意这里要返回 target——它在根内，围栏已经过了。
		return target, true
	}
	if !within(f.root, resolved) {
		return "", false
	}
	return target, true
}

// within 判断 target 是否是 root 本身或它下面的一项。
func within(root, target string) bool {
	return target == root || strings.HasPrefix(target, root+string(os.PathSeparator))
}

// etag 造一个弱 ETag：大小 + mtime + 编码。
//
// 不用内容哈希的理由见 serveAsset（要整份读一遍）。mtime 到纳秒，
// 加上大小，足以区分"同名但内容变了"——那正是 ETag 要挡的场景。
func etag(info os.FileInfo, encoding string) string {
	return fmt.Sprintf(`W/"%x-%x-%s"`, info.Size(), info.ModTime().UnixNano(), encoding)
}

// mimeTypes 是官方 frontend-static 的 MIME 表（lib/index.js 的 MIME 常量），
// 一个扩展名都不多不少。未列出的按官方回落到 application/octet-stream。
//
// 为什么照抄一张这么小的表：官方已知限制写得很清楚——"starter MIME 表只
// 覆盖 Vite 产出的资产集与 PWA manifest，其余等有资产类再补"。自己扩一张
// 更大的表看起来更友好，实际是让"官方行为"变成"我们以为的官方行为"。
var mimeTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".gz":          "application/gzip",
}

// contentType 按扩展名给 Content-Type。
func contentType(name string) string {
	if mt, ok := mimeTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return mt
	}
	return "application/octet-stream"
}

// acceptsEncoding 判断 Accept-Encoding 是否接受 enc（q=0 视为不接受）。
//
// 手写而不是引第三方：只需要处理 `gzip` / `br` / `*` 与 `q=` 四种形状，
// 而"带 q=0 就是明确拒绝"这条一旦漏掉，就会给一个明确不要压缩的客户端
// 发压缩体——那是最难查的一类客户端兼容问题。
func acceptsEncoding(header, enc string) bool {
	wildcard := false
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		q := 1.0
		for _, param := range strings.Split(params, ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				q = parsed
			}
		}
		switch {
		case name == enc:
			return q > 0
		case name == "*":
			wildcard = q > 0
		}
	}
	return wildcard
}
