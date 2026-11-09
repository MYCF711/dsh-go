package webfrontend

// boot：官方 SPA 的启动三件套（`__DSH_BOOT__` + `__ModuleLoader__` +
// `__DSH_BOOT_READY__`）。
//
// 为什么这三条是一个整体、不能只补其中一条：
//
//   - 官方入口模组（`@deepseek-ai/dsh-web-frontend/dist/assets/index-*.js`
//     的 `WebBoot.run`）第一句是 `await globalThis.__DSH_BOOT_READY__?.promise`，
//     第二句读 `globalThis.__ModuleLoader__`，缺它就直接
//     `throw new Error("web boot: window.__ModuleLoader__ bootstrap facade is missing")`；
//   - 门面自己的 `create(options)` 只从 `options.boot`（也就是
//     `__DSH_BOOT__`）读模块图，图缺/形状不对时 `parseBootManifest` 立刻抛；
//   - 就绪信号缺了不会报错（`?.promise` 是静默不等待），但那样"注入行是否
//     已全部落地"就没有可等待的点了——异步 bootstrap 那一档会读到半份表。
//
// 官方这三条是怎么产出的（`@deepseek-ai/dsh-client-modules` lib/index.js
// 的 `bootInjections()`，逐行对照过）：
//
//	rows = [ {kind:"script",  placement:"head", text: queue 门面} ]
//	     + 每个 application batch 一行 {kind:"script-preload"}
//	     + 每个 bootstrap  batch 一行 {kind:"script-src"}
//	     + {kind:"global", name:"__DSH_BOOT__", value: graph}
//	body 尾：`(globalThis.__DSH_BOOT_READY__ ??= Promise.withResolvers()).resolve()`
//
// 其中 bootstrap batch 必须含 `@deepseek-ai/dsh-client-modules/client.js`：
// 门面的 `create()` 就是把它从排队队列里取出来、调用它的
// `createClientModuleSystem(...)` —— **模块系统本身由官方 bundle 提供**。
// 本宿主不重新实现它：我们只负责"把官方的门面、图、脚本按官方顺序交到页面上"。
//
// 模块图从哪来：官方 Web 宿主是运行期组装的（扫 Loader 条目里的
// `dsh.client` 声明，再把每个包的 browser 产物按 combo 脚本发出去）。
// 纯静态部署没有那半条链，所以图的来源只能是**部署随 dist 一起带上的
// boot 载荷**：`<dist>/dsh-boot.json`，内容就是官方 `__DSH_BOOT__` 的 wire
// JSON，只是 `url` 写成本宿主真的会发的那条 dist 内路径（例如
// `plugins/b1.js?rev=abc123`），绝不写官方宿主地址。
//
// 没有这份载荷时（裸官方 dist）：三条照样注入，门面照样装上（所以不再
// 出现 "facade missing"），但 `create()` 会**响亮报错**并把原因写进
// `__DSH_BOOT_DIAGNOSIS__`，页面停在官方自带的加载页——不是白屏，
// 也不是假装加载成功。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 官方注入的全局名（与 webserver 的 index-inject 行同名）。
const (
	// GlobalBoot 是模块图（`window.__DSH_BOOT__`）。
	GlobalBoot = "__DSH_BOOT__"
	// GlobalBootReady 是就绪 deferred（`window.__DSH_BOOT_READY__`）。
	GlobalBootReady = "__DSH_BOOT_READY__"
	// GlobalBootDiagnosis 是本宿主在"dist 没有 boot 载荷"时写下的结构化
	// 诊断（只在退化路径出现；官方没有这个全局，所以它不参与任何契约）。
	GlobalBootDiagnosis = "__DSH_BOOT_DIAGNOSIS__"
)

// BootManifestFile 是 dist 里的 boot 载荷文件名。
//
// 名字不带 dsh- 前缀以外的东西：它就是官方 `__DSH_BOOT__` 那一份 wire，
// 换名字只会让人以为还有第二种格式。
const BootManifestFile = "dsh-boot.json"

// bootstrapClientModuleID 是提供模块系统实现的官方包。
//
// 门面 `create()` 要求它已经通过 `__ModuleLoader__.load({id, factory})`
// 注册过（官方靠一条 bootstrap `script-src` 行做到）。我们的校验也用它：
// 图里没有这个 id 的 bootstrap batch，就等于"页面没有模块系统实现"。
const bootstrapClientModuleID = "@deepseek-ai/dsh-client-modules"

// BootRelease 是图里的发布标识（官方 `WebBootRelease`）。
type BootRelease struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// BootEntry 是图里的一行模块（官方 `WebBootEntry`）。
//
// 字段顺序与官方一致（id/url/rev/inject/external/immediately）：JSON 顺序
// 会进 golden，也会进"图和官方逐字节相同吗"这类对比。
type BootEntry struct {
	// ID 是包 id（Loader 条目名，也是 `__ModuleLoader__.load({id})` 的 key）。
	ID string `json:"id"`
	// URL 是这份模块的 one-resource 地址（dist 内路径，可带 `?rev=`）。
	URL string `json:"url"`
	// Rev 是不透明修订号（官方用它做缓存与热替换）。
	Rev string `json:"rev"`
	// Inject 是它依赖的服务提供方（官方 `dsh.client.inject`）。
	Inject []string `json:"inject,omitempty"`
	// External 是它 require 的其它模块 id（官方 `dsh.client.external`）。
	External []string `json:"external,omitempty"`
	// Immediately 表示首批预取（官方 `dsh.client.immediately`）。
	Immediately bool `json:"immediately,omitempty"`
}

// BootBatch 是图里的一个 combo 批次（官方 `WebBootBatch`）。
//
// 官方把多个包打进一条 combo 脚本，一条脚本执行时按顺序注册多个 factory，
// 所以"批次"是**投递单位**，`entries` 是"这条脚本注册了谁"。
type BootBatch struct {
	// Phase 是 "bootstrap" 或 "application"。
	Phase string `json:"phase"`
	// URL 是这条脚本的地址（dist 内路径）。
	URL string `json:"url"`
	// Rev 是这条脚本的修订号。
	Rev string `json:"rev"`
	// Entries 是这条脚本注册的包 id，必须非空且都在 entries 里。
	Entries []string `json:"entries"`
}

// BootGraph 是 `window.__DSH_BOOT__` 的 wire 形状（官方 `WebBootGraph`）。
type BootGraph struct {
	// Rev 是整张图的修订号。
	Rev string `json:"rev"`
	// Release 可选，是宿主发布标识。
	Release *BootRelease `json:"release,omitempty"`
	// Entries 是模块行。
	Entries []BootEntry `json:"entries"`
	// Batches 是投递批次。
	Batches []BootBatch `json:"batches"`
}

// 官方 batch phase 词表。
const (
	// BootPhaseBootstrap 是阻塞的首批（必须含模块系统实现）。
	BootPhaseBootstrap = "bootstrap"
	// BootPhaseApplication 是应用批次。
	BootPhaseApplication = "application"
)

// BootOptions 是 boot 三件套的开关。
//
// 零值 = 按 dist 自动探测（默认开启）。`Disabled: true` 退回只有两条全局的
// 历史形状——那是给 A/B 与"新注入出了问题时先关掉"用的，不是默认。
type BootOptions struct {
	// Disabled 关掉 boot 三件套（页面会重新停在 facade missing，这是
	// 历史行为；留这个开关是为了能用一个环境变量做对照）。
	Disabled bool
	// ManifestPath 覆盖清单路径；空 = `<dist>/dsh-boot.json`。
	ManifestPath string
}

// bootPayload 是一份已经校验过的 boot 载荷。
type bootPayload struct {
	// graph 是要注入的图（没有清单时是空 roster）。
	graph BootGraph
	// facade 是门面的脚本体。
	facade string
	// manifestPath 是清单来源（空 = 没有清单）。
	manifestPath string
	// degraded 表示这份载荷没有 bootstrap batch：页面会拿到门面，但
	// 没有模块系统实现（原因写进 __DSH_BOOT_DIAGNOSIS__）。
	degraded bool
}

// rows 按官方 `bootInjections()` 的顺序产出注入行。
func (b *bootPayload) rows() []Row {
	rows := []Row{{Kind: RowScript, Placement: PlacementHead, Text: b.facade}}
	// 官方：application 批次先出 preload（提示浏览器尽早下载，不阻塞），
	// bootstrap 批次再出阻塞 script-src（页面必须等它注册完才读图）。
	for _, batch := range b.graph.Batches {
		if batch.Phase == BootPhaseApplication {
			rows = append(rows, Row{Kind: RowScriptPreload, Placement: PlacementHead, Src: batch.URL})
		}
	}
	for _, batch := range b.graph.Batches {
		if batch.Phase == BootPhaseBootstrap {
			rows = append(rows, Row{Kind: RowScriptSrc, Placement: PlacementHead, Src: batch.URL})
		}
	}
	rows = append(rows, Row{Kind: RowGlobal, Placement: PlacementHead, Name: GlobalBoot, Value: b.graph})
	return rows
}

// description 给启动日志一句人话。
func (b *bootPayload) description() string {
	if b.manifestPath == "" {
		return fmt.Sprintf("没有 %s（boot 图为空：SPA 会停在官方加载页，console 里有诊断）", BootManifestFile)
	}
	return fmt.Sprintf("%s：entries=%d batches=%d rev=%s",
		b.manifestPath, len(b.graph.Entries), len(b.graph.Batches), b.graph.Rev)
}

// loadBoot 读并校验一份 boot 载荷。
//
// 失败一律**在 New 里炸**（不是等浏览器打开页面才炸）：清单写了却指不到
// 文件，是"配置看起来对、实际做不到"，越早说越值钱。
func loadBoot(dist string, opts BootOptions) (*bootPayload, error) {
	manifest := strings.TrimSpace(opts.ManifestPath)
	if manifest == "" {
		manifest = filepath.Join(dist, BootManifestFile)
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		if os.IsNotExist(err) && strings.TrimSpace(opts.ManifestPath) == "" {
			// 裸 dist：没有 boot 载荷不是错误，是"这份部署只有 shell"。
			// 三条仍然注入（页面不再停在 facade missing），原因进诊断。
			return &bootPayload{graph: emptyBootGraph(dist), facade: bootFacadeScript, degraded: true}, nil
		}
		return nil, fmt.Errorf("webfrontend: 读 boot 清单 %s 失败: %w", manifest, err)
	}
	var graph BootGraph
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("webfrontend: boot 清单 %s 不是合法 JSON: %w", manifest, err)
	}
	if err := validateBootGraph(dist, &graph); err != nil {
		return nil, fmt.Errorf("webfrontend: boot 清单 %s 不合官方 wire 形状: %w", manifest, err)
	}
	degraded := true
	for _, batch := range graph.Batches {
		if batch.Phase != BootPhaseBootstrap {
			continue
		}
		for _, id := range batch.Entries {
			if id == bootstrapClientModuleID {
				degraded = false
			}
		}
	}
	return &bootPayload{graph: graph, facade: bootFacadeScript, manifestPath: manifest, degraded: degraded}, nil
}

// emptyBootGraph 是"没有清单"时的空 roster。
//
// 为什么不是"干脆不注入"：不注入就还是 facade missing——入口第一句就抛，
// 页面上除了那句英文没有任何信息。注入一份**合法的空图**之后，门面装上、
// 模块系统按官方形状返回空 roster，页面走到官方自带的加载页，而缺什么
// 由门面写进 console 与 __DSH_BOOT_DIAGNOSIS__。
//
// Rev 用 dist 入口的指纹而不是常数：同一份 dist 得到同一个 rev（可断言），
// 换了 dist 就变（不会让页面以为图没变）。
func emptyBootGraph(dist string) BootGraph {
	entry := sha256.Sum256([]byte(IndexFile + "\x00" + dist))
	return BootGraph{
		Rev:     hex.EncodeToString(entry[:])[:12],
		Entries: []BootEntry{},
		Batches: []BootBatch{},
	}
}

// validateBootGraph 是官方 `parseBootManifest` 的 Go 版本（node 侧同样的
// 判据：官方页面侧解析器抛什么，这里就在起进程前抛什么），外加两条
// 本部署专属的约束：
//
//  1. **url 必须是 dist 内路径**：绝对 URL（含官方宿主地址）一律拒绝。
//     理由不是洁癖：本宿主只 serve 一个目录，图里出现外站地址就意味着
//     "页面要去的代码不在这台机器上"，那是配置错，不是可运行状态。
//  2. url 指向的文件必须真的存在（去掉 `?rev=` 之后）。图上有一条 404，
//     浏览器只会说"某个 chunk 加载失败"，说不清是哪一行。
func validateBootGraph(dist string, graph *BootGraph) error {
	if strings.TrimSpace(graph.Rev) == "" {
		return fmt.Errorf("rev 必须是非空字符串")
	}
	if graph.Release != nil && strings.TrimSpace(graph.Release.Version) == "" {
		return fmt.Errorf("release 出现时 version 必须是非空字符串")
	}
	if graph.Entries == nil {
		return fmt.Errorf("entries 必须是数组")
	}
	if graph.Batches == nil {
		return fmt.Errorf("batches 必须是数组")
	}
	entries := make(map[string]BootEntry, len(graph.Entries))
	for i := range graph.Entries {
		entry := graph.Entries[i]
		where := fmt.Sprintf("entries[%d]", i)
		if strings.TrimSpace(entry.ID) == "" {
			return fmt.Errorf("%s 缺 id", where)
		}
		where = fmt.Sprintf("entries[%q]", entry.ID)
		if strings.TrimSpace(entry.URL) == "" || strings.TrimSpace(entry.Rev) == "" {
			return fmt.Errorf("%s 必须同时带 url 与 rev", where)
		}
		if _, dup := entries[entry.ID]; dup {
			return fmt.Errorf("id 重复：%q", entry.ID)
		}
		if err := checkBootURL(dist, where, entry.URL); err != nil {
			return err
		}
		entries[entry.ID] = entry
	}

	seenBatchURL := make(map[string]bool, len(graph.Batches))
	initial := make(map[string]string, len(graph.Entries))
	for i := range graph.Batches {
		batch := graph.Batches[i]
		if batch.Phase != BootPhaseBootstrap && batch.Phase != BootPhaseApplication {
			return fmt.Errorf("batches[%d].phase 只能是 %q 或 %q，收到 %q",
				i, BootPhaseBootstrap, BootPhaseApplication, batch.Phase)
		}
		where := fmt.Sprintf("batches[%d]（%s）", i, batch.Phase)
		if strings.TrimSpace(batch.URL) == "" || strings.TrimSpace(batch.Rev) == "" {
			return fmt.Errorf("%s 必须同时带 url 与 rev", where)
		}
		if seenBatchURL[batch.URL] {
			return fmt.Errorf("batch url 重复：%q", batch.URL)
		}
		seenBatchURL[batch.URL] = true
		if err := checkBootURL(dist, where, batch.URL); err != nil {
			return err
		}
		if len(batch.Entries) == 0 {
			return fmt.Errorf("%s 的 entries 必须是非空数组", where)
		}
		for _, id := range batch.Entries {
			if _, ok := entries[id]; !ok {
				return fmt.Errorf("%s 指向了不存在的 entry %q", where, id)
			}
			if first, dup := initial[id]; dup {
				return fmt.Errorf("entry %q 同时属于多条批次（%q 与 %q）", id, first, batch.URL)
			}
			initial[id] = batch.URL
		}
	}
	for _, entry := range graph.Entries {
		if _, ok := initial[entry.ID]; !ok {
			return fmt.Errorf("entry %q 不属于任何批次（页面永远拿不到它的脚本）", entry.ID)
		}
	}
	return nil
}

// checkBootURL 校验一条注入 URL：dist 内相对路径、文件真实存在。
func checkBootURL(dist, where, raw string) error {
	pathOnly, err := bootURLPath(raw)
	if err != nil {
		return fmt.Errorf("%s 的 url %q %w", where, raw, err)
	}
	full := filepath.Join(dist, filepath.FromSlash(pathOnly))
	info, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("%s 的 url %q 指向的文件不存在：%w", where, raw, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 的 url %q 不是普通文件", where, raw)
	}
	return nil
}

// bootURLPath 从注入 URL 里取出 dist 内路径。
//
// 拒绝形状：带 scheme 的绝对 URL（`http://…`、`https://…`、`//host/…`）、
// 反斜杠（在 Windows 上等于另一个分隔符，两种解释会给出两个不同的文件）、
// 以及任何 `..` 段（能出 dist 就说明这条 URL 不是 dist 内路径）。
func bootURLPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("是空的")
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") {
		return "", fmt.Errorf("是绝对 URL（必须是 dist 内路径，不许指向别的宿主）")
	}
	if strings.HasPrefix(value, "//") {
		return "", fmt.Errorf("是协议相对 URL（必须是 dist 内路径）")
	}
	if strings.Contains(value, `\`) {
		return "", fmt.Errorf("含反斜杠（dist 内路径必须用 /）")
	}
	pathOnly := value
	if at := strings.IndexAny(pathOnly, "?#"); at >= 0 {
		pathOnly = pathOnly[:at]
	}
	pathOnly = strings.TrimPrefix(pathOnly, "/")
	if pathOnly == "" {
		return "", fmt.Errorf("没有路径部分")
	}
	for _, segment := range strings.Split(pathOnly, "/") {
		if segment == ".." {
			return "", fmt.Errorf("含 .. 段（能出 dist）")
		}
		if segment == "" {
			return "", fmt.Errorf("含空路径段")
		}
	}
	return pathOnly, nil
}

// bootFacadeScript 是门面本体。
//
// 前 22 行是官方 `bootInjections()` 里那段 queue 门面**逐字照抄**
// （`@deepseek-ai/dsh-client-modules` lib/index.js）：排队模式、`pendingQueue`、
// `load()` 入队、`create()` 从队列取出 bootstrap bundle 并把它交给
// `createClientModuleSystem` —— 模块系统的解析语义完全由官方 bundle 决定，
// 本宿主一句都不改（这正是"不许自造模块系统"的落点）。
//
// 唯一的增加是 `registration===undefined` 那一支：官方在这里直接 throw
// （页面变成 "Failed to load plugins: … did not preload …"）。本宿主在
// "dist 没有 boot 载荷"这种部署上选择**先响亮报错、再返回空 roster**：
//   - console.error 点名缺什么、怎么补；
//   - 写 `__DSH_BOOT_DIAGNOSIS__`（结构化，便于 CDP/自动化断言）；
//   - 返回一份合法但空的模块系统，让 SPA 的 boot 步骤走完、停在官方
//     自带的加载页（"Loading plugins…"），而不是白屏或永久 facade missing。
//
// 脚本体里**不许出现 `</script`**（内联脚本会被提前闭合）；renderRow 会拒绝。
const bootFacadeScript = `(()=>{
const pendingQueue=[]
window.__ModuleLoader__={
  mode:"queue",
  pendingQueue,
  load(registration){pendingQueue.push(registration)},
  create(options){
    if(this.mode!=="queue")throw new Error("client-modules: window.__ModuleLoader__.create called after module-system boot")
    const index=pendingQueue.findIndex(registration=>registration.id==="@deepseek-ai/dsh-client-modules")
    const registration=pendingQueue[index]
    if(registration===undefined)return bootWithoutBootstrap(this,options)
    pendingQueue.splice(index,1)
    const exports=registration.factory(specifier=>{
      throw new Error('client-modules: @deepseek-ai/dsh-client-modules/client.js requested external "'+specifier+'" before the module system existed')
    })
    if(typeof exports!=="object"||exports===null||typeof exports.createClientModuleSystem!=="function"||typeof exports.apply!=="function"){
      throw new Error("client-modules: @deepseek-ai/dsh-client-modules/client.js did not export the bootstrap module face")
    }
    return exports.createClientModuleSystem(this,{id:registration.id,exports},options)
  }
}
function bootWithoutBootstrap(facade,options){
  const graph=options&&options.boot?options.boot:undefined
  const entries=graph&&Array.isArray(graph.entries)?graph.entries:[]
  const batches=graph&&Array.isArray(graph.batches)?graph.batches:[]
  const detail={
    reason:"client-modules: HTML did not preload @deepseek-ai/dsh-client-modules/client.js",
    entries:entries.length,
    batches:batches.length,
    fix:"put dsh-boot.json plus the plugins/*.js it names into DSHD_FRONTEND_DIR; a bare web-frontend dist ships the shell only and no client plugin bundles"
  }
  globalThis.__DSH_BOOT_DIAGNOSIS__=detail
  console.error("[dsh-boot] official SPA cannot mount: __DSH_BOOT__ carries no bootstrap batch, so the page has no module system. This is not a missing facade (the facade is installed), it is a missing boot payload. "+JSON.stringify(detail))
  facade.mode="live"
  facade.pendingQueue.length=0
  facade.load=function(registration){console.error("[dsh-boot] late __ModuleLoader__.load("+JSON.stringify(String(registration&&registration.id))+") dropped: no module system is installed")}
  return {
    version:"client",
    manifest:{rev:graph&&typeof graph.rev==="string"?graph.rev:"",modules:[],plugins:[]},
    import:function(specifier){const error=new Error("[dsh-boot] cannot import "+JSON.stringify(specifier)+": this dist has no boot payload");console.error(error);throw error},
    prefetch:function(){return Promise.resolve()},
    importError:function(){return undefined},
    entries:{start:function(loader){return loader.await()}}
  }
}
})()`
