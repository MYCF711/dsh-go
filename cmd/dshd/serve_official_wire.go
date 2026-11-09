package main

// 官方桌面端 0.2.2 的 **L2 wire 挂载点与映射表**（M1）。
//
// 分层：
//
//	internal/officialwire/**   协议层：信封、descriptor、mux 七帧、multipart、
//	                           authority 绑定 cookie、Router 并存分派。**不含业务**
//	本文件                     接线层：把本仓**已有**的 L1 方法映射成官方
//	                           `<namespace>/<method>`，并把每会话 journal 接成一条真实流
//
// 为什么不做第二套业务实现：session.page 的窗口/游标规则、workspaceFiles.readBytes
// 的路径边界都已经在 L1 里写死并有测试；在官方 wire 里再写一遍就是两套真相，
// 改一处忘一处，界面上表现成"官方客户端看到的内容跟界面看到的不一样"。
//
// 开关：`DSHD_OFFICIAL_WIRE=1`（默认关）。关闭时 officialWireMount 返回 nil，
// transport 原样使用自己的 handler —— 既有行为**字节不变**（构造性成立）。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"dshgo/internal/officialwire"
	"dshgo/internal/transport"
)

const (
	// officialWireEnv 是 M1 的独立开关。只有显式写 1/true/yes 才挂载。
	//
	// 为什么必须独立开关：官方 wire 与既有 `POST /api/<dotted>` 是**两套语义**
	// （envelope vs 裸参数、200-on-error vs 状态码、mux 流 vs 每会话 WS journal）。
	// 混在一起出事时无法判断是哪一套坏了，也无法单独回滚。
	officialWireEnv = "DSHD_OFFICIAL_WIRE"
	// officialWireSecretEnv 可指定 HMAC 密钥（hex）。不给则每进程随机生成，
	// 因为 cookie 只在**本进程**签发与校验，不跨重启。
	officialWireSecretEnv = "DSHD_OFFICIAL_WIRE_SECRET"
)

// officialWireDeps 是官方 wire 需要的宿主能力。
type officialWireDeps struct {
	// Home 只用于 `$events` ready 帧（官方字段 `host.home`）。
	Home string
	// Journals 是每会话 Transport journal（H14）。nil 时 `session/events`
	// 流回 method-unavailable，而不是伪造一条空流。
	Journals *transport.Journals
	// Token 返回 launch token；由挂载点从 transport.Server 取。
	Token func() string
	// Server 是 L1 的 Server 实例（mux 流方法要用它开帧机）。由 mount 闭包注入。
	Server *transport.Server
	// LegacyCookie 返回一份进程内有效的 L1 cookie。**优先于 TokenSource**：
	// launch token 是一次性的，桌面壳启动时已经兑过（见 L2 E2E P0-2）。
	LegacyCookie func() string
	// Attachments 是既有本地附件 store。非 nil 时上行裸字节路由
	// （/api/session/uploadFileBinary）落盘后登记进它；nil 时该路由明确回 501。
	Attachments transport.AttachmentStore
	// Events 是 `$events` 的宿主转发事件源（emit/waterfall/cancel）。
	//
	// nil = 这个宿主没有事件源：`$events` 只发 ready 帧，`$events/result`
	// 回 gateway/method-unavailable。**不伪造 emit**（见 internal/officialwire
	// 的 EventSource 注释）。
	//
	// 生产接线（一处）：cmd/dshd/serve.go 的 officialWireMount 调用点加
	//   Events: newApprovalEventSource(broker),
	// broker 是 serve.go:322 造的那个真实审批 broker。这一句不在本任务的
	// 可写范围内（serve.go 被显式禁改），所以生产当前是 nil —— 见交付报告
	// 的「未接线项」。
	Events officialwire.EventSource
}

// officialWireEnabled 读开关。
func officialWireEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(officialWireEnv))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// officialWireMount 是 Lead 唯一接线点。
//
// 用法（serve.go，一行）：
//
//	HandlerWrap: officialWireMount(officialWireDeps{Home: *home, Journals: journals}),
//
// 返回 nil 表示"不包装"：transport.Start 里 `if opt.HandlerWrap != nil` 不会执行，
// 既有 handler 原样上阵。
func officialWireMount(deps officialWireDeps) func(*transport.Server) http.Handler {
	if !officialWireEnabled() {
		return nil
	}
	return func(srv *transport.Server) http.Handler {
		deps.Token = srv.Token
		// launch token 是一次性的，桌面壳启动时已经兑过；这里改发一张进程内
		// cookie，否则官方 unary 会被 L1 判成 remote-access/denied（L2 E2E P0-2）。
		deps.LegacyCookie = srv.MintInternalCookie
		deps.Server = srv
		handler, err := officialWireHandler(srv, deps)
		if err != nil {
			// 开关开了但挂不上：打一行 stderr 并**继续用 L1**。
			// 不在这里 os.Exit：宿主还没开始服务，让上层决定怎么处理更合适；
			// 而静默退回 L1 会让"我开了却没生效"变成永久看不出来的事。
			fmt.Fprintf(os.Stderr, "officialwire: 挂载失败，继续用既有 L1：%v\n", err)
			return nil
		}
		return handler
	}
}

// officialWireHandler 是挂载的真正实现（测试直接调它，不必改 transport）。
//
// legacy 是既有 transport.Server（它自己就是 http.Handler）。
func officialWireHandler(legacy http.Handler, deps officialWireDeps) (http.Handler, error) {
	server, err := newOfficialWireServer(legacy, deps)
	if err != nil {
		return nil, err
	}
	return server.Handler(legacy), nil
}

// newOfficialWireServer 建协议层并注册映射表。
func newOfficialWireServer(legacy http.Handler, deps officialWireDeps) (*officialwire.Server, error) {
	// mux 的流方法要拿 Server 开帧机。harness 与生产都把 *transport.Server
	// 当 legacy handler 传进来，这里统一取一次，避免两处漂移成 nil。
	if srv, ok := legacy.(*transport.Server); ok {
		deps.Server = srv
		// follow 的 `snapshot.header` 必须是**真实**的 v4 会话头
		// （见 serve_official_wire_follow.go）。挂在挂载点这里有两个理由：
		// ① 它晚于 transport.NewServer、早于任何请求；
		// ② follow 的 501 判据查的就是这份注册，放到别处就会"开关开了但 follow 仍 501"。
		if strings.TrimSpace(deps.Home) != "" {
			srv.AttachOfficialSessionHeaderSource(officialWireSessionHeaderSource{home: deps.Home})
		}
	}
	secret, err := officialWireSecret()
	if err != nil {
		return nil, err
	}
	client := &officialwire.LegacyClient{
		Handler:      legacy,
		TokenSource:  deps.Token,
		CookieSource: deps.LegacyCookie,
	}
	config := officialwire.ServerConfig{
		Secret:      secret,
		TokenSource: deps.Token,
		Home:        deps.Home,
		// 流工厂：官方 mux 的 open 走这里，与 unary 的 binding 表**互不相干**。
		// mux 的 open 走官方帧机（6 个流方法共用一台），尾部流走 journal。
		Opener: officialMuxStreamOpener{server: deps.Server},
		// `$events` 的宿主事件源。nil 时只发 ready（不伪造 emit）。
		Events: deps.Events,
	}
	if deps.Attachments != nil {
		// 上行裸字节 → 写盘 → 登记进既有 attachment store。
		// 这是"真实业务映射"：字节由 L1 同一个 store 持有，官方 wire 不自建仓库。
		config.RawUpload = attachmentUploadSink{home: deps.Home, store: deps.Attachments}
	}
	server, failure := officialwire.NewServer(config)
	if failure != nil {
		return nil, failure
	}

	// 1) session/page → L1 session.page。
	//
	// args 原样转交：官方 address/throughSeq/beforeSeq/maxMessages/turnWindow
	// 与 L1 session.page 的字段名是同一套（handlers_session_official.go:384-390），
	// 所以这里不需要字段改写——**不改写**才不会有第二套字段语义。
	if err := server.Handle("session/page", client.Binding("session.page", sessionPageDescriptor(), nil)); err != nil {
		return nil, err
	}

	// 2) session/events（unary）→ L1 session.events。
	//    官方 events 是 page 的推荐入口，两者共用同一个 L1 实现。
	if err := server.Handle("session/events", client.Binding("session.events", sessionPageDescriptor(), nil)); err != nil {
		return nil, err
	}

	// 3) workspaceFiles/readBytes → L1 workspaceFiles.readBytes + 下行附件。
	//
	// L1 用 `data` + `encoding:"base64"` 表达字节；官方 wire 上 `data` 必须是
	// **附件**（`data:null` + attachments[{path:["data"],codec:"bytes"}]，
	// EVIDENCE §5.2）。所以这里把 base64 解回 []byte，交给协议层的附件编码器。
	if err := server.Handle("workspaceFiles/readBytes",
		client.Binding("workspaceFiles.readBytes", readBytesDescriptor(), decodeReadBytesResult)); err != nil {
		return nil, err
	}

	// 4) $events/result：客户端对一条 waterfall 的回答。
	//
	// 为什么必须真实现（而不是回 method-unavailable 了事）：官方客户端的
	// `answer()` 里是
	//
	//	const response = await this.connection.rpc.call('/api', '$events/result', {args: result}, signal)
	//	if (!response.ok) throw new Error(response.error.message)
	//
	// 而 `answer()` 的调用方 `pumpEvents` 在 catch 里 `failed.abort(error)` ——
	// 也就是说**只要宿主发过一条 waterfall，ok:false 就等于把整个 generation 打死并重连**
	// （lib/types/client/remote-events.js:126-131, 138-143）。所以这条方法与
	// "开始下发 waterfall" 必须在同一个提交里落地。
	//
	// 只有 interactions=answer 的 Peer 才有资格进来（EVIDENCE §3.2）。
	eventsResult := officialwire.Binding{
		Descriptor:    eventsResultDescriptor(),
		RequireAnswer: true,
		Handle: func(_ context.Context, args map[string]json.RawMessage) (any, *officialwire.Error) {
			if deps.Events == nil {
				// 宿主没有事件源：方法在官方面上存在，但这个宿主这一侧没有能力。
				// 回 method-unavailable（**不是** unknown-method），客户端据此知道
				// "这个方法我认识、这个宿主不支持"，而不是"我拼错了方法名"。
				return nil, officialwire.MethodUnavailable(officialwire.EndpointEventResult,
					"本宿主没有转发事件源（waterfall 事件源未接线）；这里不伪造 ok:true")
			}
			result, failure := officialwire.ParseEventResult(officialwire.EndpointEventResult, args)
			if failure != nil {
				// 非法 outcome 一律 fail-loud：静默放行会让"客户端答错了"
				// 变成"宿主收下了垃圾"，而那条 waterfall 会当成 next 继续跑。
				return nil, failure
			}
			if failure := deps.Events.Answer(result.ClientID, result.EventID, result.Outcome); failure != nil {
				return nil, failure
			}
			// 官方返回 `{ok:true, value: void 0}`（dispatchRpc 的 `$events/result` 分支
			// 返回 `{ok:true, value: undefined}`）：value 键在 JSON 里**不出现**。
			// 这里返回 nil，信封的 `value,omitempty` 正好把它省掉。
			return nil, nil
		},
	}
	if err := server.Handle(officialwire.EndpointEventResult, eventsResult); err != nil {
		return nil, err
	}

	// 4.5) pluginMarket / pluginManager / pluginRegistryProbe 的官方 unary 面。
	//
	// 这些方法在 L1 已有真实实现（internal/market + pluginmgr），但官方 wire
	// 此前没有绑定：官方客户端调 `POST /api/pluginMarket/listIndexes` 会落到
	// unknown-method/method-unavailable，看起来像"宿主没做"，实际是分派漏接。
	// 未配置索引时 L1 返回 503 registry_unavailable，这里按 503 原样保留。
	for _, b := range []struct {
		endpoint string
		l1       string
		optional []string
	}{
		{"pluginMarket/listIndexes", "pluginMarket.listIndexes", nil},
		{"pluginMarket/setIndexes", "pluginMarket.setIndexes", []string{"urls"}},
		{"pluginMarket/search", "pluginMarket.search", []string{"url", "query"}},
		{"pluginMarket/readEntryDetail", "pluginMarket.readEntryDetail", []string{"url", "name", "version", "language"}},
		{"pluginRegistryProbe/fastest", "pluginRegistryProbe.fastest", []string{"urls"}},
		{"pluginManager/registries", "pluginManager.registries", nil},
		{"pluginManager/listVersions", "pluginManager.listVersions", []string{"name"}},
	} {
		desc := &officialwire.Descriptor{Optional: b.optional}
		if err := server.Handle(b.endpoint, client.Binding(b.l1, desc, nil)); err != nil {
			return nil, err
		}
	}

	// 5) settings/describe → L1 settings.describe + 官方投影。
	//
	// L1 的 `secrets` 是 []string（58 方法快照，冻结不改）；官方 zod 要
	// `[{path,set}]`，且 value/base/user 是 redacted。投影在
	// officialSettingsDescribeBinding → transport.OfficialSettingsDescribeResultOf
	// 里完成，业务读取仍是同一份 settingsform.Service；L1 形状一个字节不动。
	if err := server.Handle("settings/describe", officialSettingsDescribeBinding(client)); err != nil {
		return nil, err
	}

	// 6) 官方 job.*/terminal.* 的 unary 面（T05 WIRING §4；此前是
	// gateway/unknown-method 的 BLOCKER）。
	//
	// 只注册 unary 七个：job/list、job/follow、terminal/attach、terminal/retain
	// 官方是 mode:'stream'，走 remote.mux 的 open 帧（T05 的
	// OpenFrameStream），**不能**在这里重复注册成 unary。
	//
	// args 形状按官方 typert 校过（app.asar 0.2.2-rc.1 的
	// terminal-controller/job-controller lib/typert.host.js）：
	//   terminal/create     {agentId, request:{shellPath?, id, cols, rows}}
	//   terminal/close      {agentId, id}
	//   terminal/environment{agentId}
	//   terminal/shells     {agentId}
	//   terminal/rename     {agentId, id, title}
	//   terminal/list       {sessionId}
	//   job/kill            {request:{sessionId, jobId}}  ← 需扁平化（见下）
	//
	// L1 的 terminal.* 已经接受 agentId 与嵌套 request（handlers_job_terminal_official.go
	// 的 officialIdentity/HandleTerminalCreate），所以除 job/kill 外都直接透传。
	for _, reg := range []struct {
		endpoint   string
		dotted     string
		descriptor *officialwire.Descriptor
	}{
		{"terminal/create", "terminal.create", &officialwire.Descriptor{Required: []string{"agentId", "request"}}},
		{"terminal/close", "terminal.close", &officialwire.Descriptor{Required: []string{"agentId", "id"}}},
		{"terminal/environment", "terminal.environment", &officialwire.Descriptor{Required: []string{"agentId"}}},
		{"terminal/shells", "terminal.shells", &officialwire.Descriptor{Required: []string{"agentId"}}},
		{"terminal/rename", "terminal.rename", &officialwire.Descriptor{Required: []string{"agentId", "id", "title"}}},
		{"terminal/list", "terminal.list", &officialwire.Descriptor{Required: []string{"sessionId"}}},
	} {
		if err := server.Handle(reg.endpoint, client.Binding(reg.dotted, reg.descriptor, nil)); err != nil {
			return nil, err
		}
	}
	if err := server.Handle("job/kill", officialJobKillBinding(client)); err != nil {
		return nil, err
	}
	if err := registerOfficialAnalyticsWire(server, client); err != nil {
		return nil, err
	}
	return server, nil
}

// officialJobKillBinding 把官方 `{request:{sessionId, jobId}}` 翻成本仓 L1 的扁平
// `{sessionId, jobId}`（job-controller/lib/typert.host.js:150-181 的 JobKillRequest）。
//
// 为什么不能直接 client.Binding：L1 的 job.kill 收扁平字段（handlers_job_terminal_official.go
// 的 officialJobParams），而官方 wire 的 json 参数名是 request；嵌套原样下行会 400。
func officialJobKillBinding(client *officialwire.LegacyClient) officialwire.Binding {
	return officialwire.Binding{
		Descriptor: &officialwire.Descriptor{Required: []string{"request"}},
		Handle: func(ctx context.Context, args map[string]json.RawMessage) (any, *officialwire.Error) {
			raw, ok := args["request"]
			if !ok {
				return nil, officialwire.ArgumentsInvalid("job/kill", "request", "缺 request（官方 json 参数名）")
			}
			var request struct {
				SessionID string `json:"sessionId"`
				JobID     string `json:"jobId"`
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, officialwire.ArgumentsInvalid("job/kill", "request", "request 必须是 {sessionId, jobId}")
			}
			if request.SessionID == "" || request.JobID == "" {
				return nil, officialwire.ArgumentsInvalid("job/kill", "request", "request 需要非空 sessionId 与 jobId")
			}
			flat, err := json.Marshal(map[string]string{"sessionId": request.SessionID, "jobId": request.JobID})
			if err != nil {
				return nil, officialwire.Errorf(officialwire.CodeInternal, "job/kill 参数序列化失败：%v", err)
			}
			result, failure := client.Call(ctx, "job.kill", flat)
			if failure != nil {
				return nil, failure
			}
			var value any
			if err := json.Unmarshal(result, &value); err != nil {
				return nil, officialwire.Errorf(officialwire.CodeInternal, "job/kill 的 L1 结果不是 JSON：%v", err)
			}
			return value, nil
		},
	}
}

// officialWireSecret 取（或生成）cookie 的 HMAC 密钥。
//
// 缺省随机：cookie 只在本进程签发与校验，重启后旧 cookie 失效是**期望行为**
// （客户端本来就要重新 /connect）。写死一个默认密钥才是真问题。
func officialWireSecret() ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv(officialWireSecretEnv)); raw != "" {
		secret, err := hex.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("%s 必须是 hex：%w", officialWireSecretEnv, err)
		}
		if len(secret) < 16 {
			return nil, fmt.Errorf("%s 至少要 16 字节（32 个 hex 字符），实际 %d", officialWireSecretEnv, len(secret))
		}
		return secret, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("生成 official wire 密钥失败：%w", err)
	}
	return buf, nil
}

// sessionPageDescriptor 是官方 session/page、session/events 的 args 字段集。
//
// 来源：本仓对官方 zod 契约的实现（handlers_session_official.go:384-390 的
// officialPageRequest）。**这是一处推断**：官方 descriptor 的完整 `mode`/`lookup`
// 元数据没有全量导出（EVIDENCE U5），所以字段集以本仓已对齐的契约为准。
func sessionPageDescriptor() *officialwire.Descriptor {
	return &officialwire.Descriptor{
		Required: []string{"address"},
		Optional: []string{"throughSeq", "beforeSeq", "maxMessages", "turnWindow"},
	}
}

// readBytesDescriptor 是官方 workspaceFiles/readBytes 的 args 字段集。
//
// 来源：EVIDENCE §5.2（`workspaceFileScopeId` / `path` / `options{range,baseFile}`）。
// `options` 在官方是可选对象；`workspaceFileScopeId` 是 lookup（本地单工作区可省）。
func readBytesDescriptor() *officialwire.Descriptor {
	return &officialwire.Descriptor{
		Required: []string{"path", "options"},
		Optional: []string{"workspaceFileScopeId"},
	}
}

// eventsResultDescriptor 是官方 $events/result 的 args 字段集（EVIDENCE §3.2）。
func eventsResultDescriptor() *officialwire.Descriptor {
	return &officialwire.Descriptor{
		Required: []string{"clientId", "eventId", "outcome"},
	}
}

// decodeReadBytesResult 把 L1 的 base64 `data` 解成真字节。
//
// 官方结果里**没有** `encoding` 字段（那是 L1 为了在 JSON 里表达字节而加的），
// 所以这里把它摘掉、把 `data` 换成 []byte：协议层的附件编码器会把它抽成
// `bytes-0` part，原位置留 null。
func decodeReadBytesResult(result json.RawMessage) (any, *officialwire.Error) {
	var value map[string]any
	if err := json.Unmarshal(result, &value); err != nil {
		return nil, officialwire.Errorf(officialwire.CodeInternal,
			"workspaceFiles.readBytes 的 L1 结果不是对象：%v", err)
	}
	encoded, _ := value["data"].(string)
	if encoded == "" {
		return nil, officialwire.Errorf(officialwire.CodeInternal,
			"workspaceFiles.readBytes 的 L1 结果没有 data（宿主这一侧没给字节）")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, officialwire.Errorf(officialwire.CodeInternal,
			"workspaceFiles.readBytes 的 data 不是合法 base64：%v", err)
	}
	delete(value, "encoding")
	value["data"] = data
	return value, nil
}

// 为什么这里**没有**另一套 journal 尾流实现：
//
// 曾经有一个 `journalStreamOpener`（按 100ms 轮询 `Journal.Window` 跟随），但它
// 从未被挂载（生产 Opener 一直是 `officialMuxStreamOpener`），而且它的默认游标是
// `next = 0` + 拒绝负 sinceSeq —— 也就是"我已有第 0 帧"的语义，接上去就会**丢第一帧**。
// 尾流只有一条实现（`internal/transport` 的 `OpenOfficialJournalTail`，
// `sinceSeq=-1` = 从头），留着第二份只会等某天有人把它接上去。
// 删除而不是保留：死代码不会被测试覆盖，接上它的那一刻没人会知道它少了第一帧。

// attachmentUploadSink 把上行裸字节落盘后登记进既有 attachment store。
//
// 为什么先写盘再 Put：AttachmentStore 的契约就是 `Put(path)`（它负责读文件、
// 算 sha256、识别图片尺寸），不是 `Put(bytes)`。绕开它自己造一份附件元数据，
// 就会出现"官方 wire 存进来的附件，界面侧读不到/没有 sha256"的两套真相。
type attachmentUploadSink struct {
	home  string
	store transport.AttachmentStore
}

var _ officialwire.RawUploadSink = attachmentUploadSink{}

// Upload 收字节、落盘、登记，返回 `{attachmentId,name,byteSize,mime}` 的 JSON。
//
// 响应形状说明：本路由的官方响应形状没有逐字取证（EVIDENCE U1/U5），
// 所以这里只回"宿主自己确定的那份 JSON"，不套猜出来的官方壳。
func (s attachmentUploadSink) Upload(_ context.Context, request officialwire.RawUploadRequest) (officialwire.RawUploadResponse, *officialwire.Error) {
	if s.store == nil {
		return officialwire.RawUploadResponse{}, officialwire.MethodUnavailable("session/uploadFileBinary",
			"本宿主没有 attachment store，无法接收裸字节上传")
	}
	if len(request.Body) == 0 {
		return officialwire.RawUploadResponse{}, officialwire.ArgumentsInvalid("session/uploadFileBinary", "body",
			"上传体是空字节：官方 uploadFileBinary 的 body 就是文件内容本身")
	}
	dir := filepath.Join(s.home, "state", "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return officialwire.RawUploadResponse{}, officialwire.Errorf(officialwire.CodeInternal,
			"创建上传目录失败：%v", err)
	}
	id, err := serveID()
	if err != nil {
		return officialwire.RawUploadResponse{}, officialwire.Errorf(officialwire.CodeInternal,
			"生成上传文件名失败：%v", err)
	}
	path := filepath.Join(dir, id+"-"+sanitizeUploadName(request.Name))
	if err := os.WriteFile(path, request.Body, 0o600); err != nil {
		return officialwire.RawUploadResponse{}, officialwire.Errorf(officialwire.CodeInternal,
			"写上传文件失败：%v", err)
	}
	attachment, err := s.store.Put(path)
	if err != nil {
		if typed, ok := err.(interface{ AttachmentError() (string, int) }); ok {
			code, status := typed.AttachmentError()
			return officialwire.RawUploadResponse{}, officialwire.TranslateLegacy("session", status, code, err.Error())
		}
		return officialwire.RawUploadResponse{}, officialwire.Errorf(officialwire.CodeInternal,
			"登记附件失败：%v", err)
	}
	payload, marshalErr := json.Marshal(map[string]any{
		"attachmentId": attachment.ID,
		"name":         attachment.Name,
		"byteSize":     attachment.ByteSize,
		"mime":         attachment.MIME,
	})
	if marshalErr != nil {
		return officialwire.RawUploadResponse{}, officialwire.Errorf(officialwire.CodeInternal,
			"序列化上传结果失败：%v", marshalErr)
	}
	return officialwire.RawUploadResponse{
		ContentType: "application/json; charset=utf-8",
		Body:        payload,
	}, nil
}

// sanitizeUploadName 只留字母数字与 ._-，并且丢掉任何目录成分。
//
// 上传名是**客户端给的**：不裁剪就等于让客户端往 `<home>/state/uploads`
// 之外写文件（`../` 或绝对路径）。这条不是风格问题，是路径穿越。
func sanitizeUploadName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "upload.bin"
	}
	name = filepath.Base(filepath.Clean(name))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return "upload.bin"
	}
	var builder strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '.', r == '_', r == '-':
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "upload.bin"
	}
	return builder.String()
}
