package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// TestMCPHelperServer 是一个**真的** MCP stdio server：它跑在独立子进程里，
// 真的读写 stdin/stdout 的 JSON-RPC，真的会 sleep、真的会非零退出。
//
// 为什么不用 mock：这一层的全部风险都在"跨进程协议 + 生命周期"上。
// 用进程内的假对象测，恰好把要验的东西全绕过去了。
//
// 协议 stdout 只能有 JSON-RPC；测试框架的输出由 os.Exit 阻断。
func TestMCPHelperServer(t *testing.T) {
	if os.Getenv("HS02_MCP_HELPER") != "1" {
		return
	}
	logPath := os.Getenv("HS02_MCP_LOG")
	record := func(event string) {
		if logPath == "" {
			return
		}
		raw, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "event": event})
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = file.Write(append(raw, '\n'))
			_ = file.Close()
		}
	}
	record("start")
	mode := os.Getenv("HS02_MCP_MODE")

	// slow 走并发之后会有多个 goroutine 写同一条 stdout，必须串行化——
	// 交错的行会让 JSON-RPC 解析失败，而失败看起来像宿主的 bug。
	var writeMu sync.Mutex
	write := func(value map[string]any) {
		raw, _ := json.Marshal(value)
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := os.Stdout.Write(append(raw, '\n')); err != nil {
			os.Exit(3)
		}
	}
	reply := func(id json.RawMessage, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	replyErr := func(id json.RawMessage, code int, message string) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	}
	tool := func(name, description string, properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return map[string]any{"name": name, "description": description, "inputSchema": schema}
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintln(os.Stderr, "bad request:", err)
			os.Exit(2)
		}
		var params struct {
			Name      string         `json:"name"`
			URI       string         `json:"uri"`
			Arguments map[string]any `json:"arguments"`
			Cursor    string         `json:"cursor"`
		}
		_ = json.Unmarshal(req.Params, &params)

		switch req.Method {
		case "initialize":
			capabilities := map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"subscribe": false},
				"prompts":   map[string]any{},
			}
			if mode == "notools" {
				capabilities = map[string]any{"resources": map[string]any{}}
			}
			reply(req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    capabilities,
				"instructions":    "  fixture 服务器说明  ",
				"serverInfo":      map[string]any{"name": "hs02-fixture", "version": "1.0.0"},
			})
		case "notifications/initialized":
			// 通知无 id，无响应。
		case "tools/list":
			if mode == "notools" {
				replyErr(req.ID, -32601, "tools 能力未声明")
				continue
			}
			readOnly := tool("readonly_probe", "自报只读", map[string]any{})
			readOnly["annotations"] = map[string]any{"readOnlyHint": true}
			first := []any{
				tool("echo", "原样返回 text", map[string]any{"text": map[string]any{"type": "string"}}, "text"),
				tool("boom", "总是失败", map[string]any{}),
				tool("slow", "先睡再返回", map[string]any{"ms": map[string]any{"type": "integer"}}),
				readOnly,
				tool("bad/name with spaces", "名字需要归一化", map[string]any{}),
			}
			// HS02_MCP_TOOLS 让测试精确控制清单（命名空间撞车要两个 server
			// 各列一个"拼起来同形"的名字，用固定清单做不到）。
			if override := os.Getenv("HS02_MCP_TOOLS"); override != "" {
				first = nil
				for _, name := range strings.Split(override, ",") {
					first = append(first, tool(name, "覆盖清单里的工具", map[string]any{}))
				}
			}
			if mode == "duplicate" {
				first = append(first, tool("echo", "第二份同名工具", map[string]any{}))
			}
			reply(req.ID, map[string]any{"tools": first})
		case "tools/call":
			switch params.Name {
			case "echo":
				text, _ := params.Arguments["text"].(string)
				reply(req.ID, map[string]any{"content": []any{
					map[string]any{"type": "text", "text": "echo:" + text},
				}})
			case "boom":
				// 两种失败形状都要能过：JSON-RPC error 与 isError 结果。
				if params.Arguments["asResult"] == true {
					reply(req.ID, map[string]any{
						"content": []any{map[string]any{"type": "text", "text": "fixture 报告失败"}},
						"isError": true,
					})
					continue
				}
				replyErr(req.ID, -32000, "fixture 拒绝执行 boom")
			case "slow":
				// 并发处理：一次慢调用不许把整条连接堵死，否则"超时之后连接
				// 仍然可用"这条断言测的就不是宿主，而是夹具的串行性。
				ms := 50
				if v, ok := params.Arguments["ms"].(float64); ok {
					ms = int(v)
				}
				id := req.ID
				go func() {
					time.Sleep(time.Duration(ms) * time.Millisecond)
					reply(id, map[string]any{"content": []any{
						map[string]any{"type": "text", "text": "slow:done"},
					}})
				}()
			case "readonly_probe":
				reply(req.ID, map[string]any{"content": []any{
					map[string]any{"type": "text", "text": "readonly"},
					map[string]any{"type": "image", "data": strings.Repeat("A", 32), "mimeType": "image/png"},
					map[string]any{"type": "resource_link", "name": "doc", "uri": "fixture://doc"},
				}})
			default:
				replyErr(req.ID, -32601, "unknown tool "+params.Name)
			}
		case "resources/list":
			reply(req.ID, map[string]any{"resources": []any{
				map[string]any{"uri": "fixture://readme", "name": "readme", "mimeType": "text/plain", "description": "说明"},
				map[string]any{"uri": "fixture://data", "name": "data", "mimeType": "application/json"},
			}})
		case "resources/templates/list":
			reply(req.ID, map[string]any{"resourceTemplates": []any{
				map[string]any{"uriTemplate": "fixture://items/{id}", "name": "item"},
			}})
		case "resources/read":
			if params.URI == "fixture://readme" {
				reply(req.ID, map[string]any{"contents": []any{
					map[string]any{"uri": "fixture://readme", "mimeType": "text/plain", "text": "hello from fixture"},
					map[string]any{"uri": "fixture://blob", "mimeType": "application/octet-stream", "blob": "QUJD"},
				}})
				continue
			}
			replyErr(req.ID, -32002, "resource not found: "+params.URI)
		default:
			replyErr(req.ID, -32601, "unknown method "+req.Method)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	os.Exit(0)
}

// ---- 夹具 ----

type mcpTestServer struct {
	Config    map[string]any
	Mode      string
	Reconnect map[string]any
}

func writeMCPTestHome(t *testing.T, servers map[string]mcpTestServer) (home string, logPath string) {
	t.Helper()
	home = t.TempDir()
	logPath = filepath.Join(t.TempDir(), "mcp-helper.jsonl")
	config := map[string]any{}
	for name, server := range servers {
		entry := map[string]any{
			"command": os.Args[0],
			"args":    []string{"-test.run=^TestMCPHelperServer$"},
			"env": map[string]string{
				"HS02_MCP_HELPER": "1",
				"HS02_MCP_LOG":    logPath,
				"HS02_MCP_MODE":   server.Mode,
			},
			"toolCallTimeoutMs": 60000,
			"reconnect":         map[string]any{"enabled": false},
		}
		for key, value := range server.Config {
			entry[key] = value
		}
		if server.Reconnect != nil {
			entry["reconnect"] = server.Reconnect
		}
		config[name] = entry
	}
	raw, err := json.MarshalIndent(map[string]any{"mcpServers": config}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "mcp.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return home, logPath
}

type mcpHelperLogRow struct {
	PID   int    `json:"pid"`
	Event string `json:"event"`
}

func readMCPHelperLog(t *testing.T, path string) []mcpHelperLogRow {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	defer file.Close()
	var rows []mcpHelperLogRow
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row mcpHelperLogRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时：%s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---- 1. 命名 ----

func TestMCPPublicToolName(t *testing.T) {
	cases := []struct {
		server, raw, want string
	}{
		{"docs", "search", "mcp__docs__search"},
		{"a_b-c", "xy_z", "mcp__a_b-c__xy_z"}, // 全合法字符：原样，不哈希
	}
	for _, tc := range cases {
		if got := MCPPublicToolName(tc.server, tc.raw); got != tc.want {
			t.Fatalf("MCPPublicToolName(%q,%q)=%q want %q", tc.server, tc.raw, got, tc.want)
		}
	}

	// 归一化真的改了字 → 追加 12 位哈希，且总长 ≤64。
	lossy := MCPPublicToolName("fixture", "bad/name with spaces")
	if len(lossy) > mcpMaxPublicNameLength {
		t.Fatalf("公开名超长: %d > %d (%q)", len(lossy), mcpMaxPublicNameLength, lossy)
	}
	if !strings.HasPrefix(lossy, "mcp__fixture__bad_name_with_spaces_") {
		t.Fatalf("归一化前缀不对: %q", lossy)
	}
	if len(lossy) != len("mcp__fixture__bad_name_with_spaces_")+mcpNameHashLength {
		t.Fatalf("哈希位数不对: %q", lossy)
	}

	// 哈希只兜"归一化"这一种塌陷，不兜 "__" 本身的歧义——官方也是这么划界的
	// （publicToolName 是纯函数，看不到别的 server 叫什么）。
	// 所以这里断言的是**它们确实会撞**：撞了之后靠注册期的冲突检查响亮失败，
	// 而不是靠名字自己变长躲过去。TestMCPNamespaceConflictFailsLoud 钉的就是那条。
	if MCPPublicToolName("a", "b__c") != MCPPublicToolName("a__b", "c") {
		t.Fatal(`"__" 歧义应当撞成同一个公开名（由注册期冲突检查兜住）`)
	}

	// 归一化造成的塌陷必须被哈希拆开：两条不同的非法名不能变成同一个。
	lossyA := MCPPublicToolName("fixture", "bad/name")
	lossyB := MCPPublicToolName("fixture", "bad:name")
	if lossyA == lossyB {
		t.Fatalf("有损归一化把两个身份塌成了一个名字: %q", lossyA)
	}

	// 超长截断同样稳定且唯一。
	long1 := MCPPublicToolName("srv", strings.Repeat("x", 200))
	long2 := MCPPublicToolName("srv", strings.Repeat("y", 200))
	if long1 == long2 || len(long1) > mcpMaxPublicNameLength || len(long2) > mcpMaxPublicNameLength {
		t.Fatalf("超长名截断不正确: %q / %q", long1, long2)
	}
	if again := MCPPublicToolName("srv", strings.Repeat("x", 200)); again != long1 {
		t.Fatal("公开名不是纯函数：同输入两次不同")
	}
}

// ---- 2. 真实注册 + 真实调用 ----

func TestMCPRegistersRealToolsAndCallsThem(t *testing.T) {
	home, _ := writeMCPTestHome(t, map[string]mcpTestServer{"fixture": {}})
	manager := OpenMCPManager(home)
	manager.StartAll()
	defer manager.Close()

	reg := New()
	if err := manager.RegisterInto(reg); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	// 工具**真的**进了注册表：这是"只显示 toolCount"那个缺口的直接判据。
	names := map[string]bool{}
	for _, name := range reg.Names() {
		names[name] = true
	}
	for _, want := range []string{"mcp__fixture__echo", "mcp__fixture__boom", "mcp__fixture__slow", "mcp__fixture__readonly_probe"} {
		if !names[want] {
			t.Fatalf("工具表缺 %q；实际=%v", want, reg.Names())
		}
	}
	// 归一化过的名字也要在表里，并且能对上原始 rawName。
	normalized := MCPPublicToolName("fixture", "bad/name with spaces")
	if !names[normalized] {
		t.Fatalf("归一化后的名字不在表里: %q", normalized)
	}

	// 成功：真子进程、真 tools/call。
	echo, ok := reg.Get("mcp__fixture__echo")
	if !ok {
		t.Fatal("拿不到 echo 工具")
	}
	result, err := echo.Exec(context.Background(), json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("tools/call 失败: %v", err)
	}
	if result.Text != "echo:hi" {
		t.Fatalf("结果正文=%q want %q", result.Text, "echo:hi")
	}
	// 规范值 `{content:[...]}` 必须原样保留，程序化调用方才拿得到结构化结果。
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 不是规范值对象: %T", result.Data)
	}
	if _, ok := data["content"].([]any); !ok {
		t.Fatalf("Data.content 不是数组: %T", data["content"])
	}

	// 失败之一：JSON-RPC error。R3 要求失败必须给 Hint。
	boom, _ := reg.Get("mcp__fixture__boom")
	failed, err := boom.Exec(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("boom 应该失败")
	}
	if failed.Hint == "" {
		t.Fatal("失败路径没有 Hint（R3 违规）")
	}
	if !strings.Contains(err.Error(), "fixture 拒绝执行 boom") {
		t.Fatalf("错误里丢了 server 原文: %v", err)
	}

	// 失败之二：`isError: true` 的调用结果同样是失败，不能因为 content 有文字就当成功。
	isErr, err := boom.Exec(context.Background(), json.RawMessage(`{"asResult":true}`))
	if err == nil {
		t.Fatal("isError 结果被当成了成功")
	}
	if isErr.Hint == "" || !strings.Contains(isErr.Text, "fixture 报告失败") {
		t.Fatalf("isError 结果的 Hint/Text 不对: %+v", isErr)
	}

	// 图片与资源链接只能投影成文字：二进制永远不进历史。
	probe, _ := reg.Get("mcp__fixture__readonly_probe")
	projected, err := probe.Exec(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("readonly_probe 失败: %v", err)
	}
	if strings.Contains(projected.Text, strings.Repeat("A", 32)) {
		t.Fatalf("base64 泄漏进了模型可见正文: %q", projected.Text)
	}
	if !strings.Contains(projected.Text, "Resource link: doc (fixture://doc)") {
		t.Fatalf("资源链接投影不对: %q", projected.Text)
	}

	// caps 采信 server 自报的 annotations；没报的一律按"可能写"。
	if caps := echo.Caps(); !caps.Executes || !caps.Network || !caps.Writes {
		t.Fatalf("未声明 annotations 的工具必须是保守 caps: %+v", caps)
	}
	if caps := probe.Caps(); caps.Writes || caps.Destructive {
		t.Fatalf("readOnlyHint 没有收窄 caps: %+v", caps)
	}
}

// ---- 3. 超时 ----

func TestMCPToolCallTimesOut(t *testing.T) {
	home, _ := writeMCPTestHome(t, map[string]mcpTestServer{
		"fixture": {Config: map[string]any{"toolCallTimeoutMs": 120}},
	})
	manager := OpenMCPManager(home)
	manager.StartAll()
	defer manager.Close()
	reg := New()
	if err := manager.RegisterInto(reg); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	slow, ok := reg.Get("mcp__fixture__slow")
	if !ok {
		t.Fatal("拿不到 slow 工具")
	}
	started := time.Now()
	result, err := slow.Exec(context.Background(), json.RawMessage(`{"ms":5000}`))
	if err == nil {
		t.Fatal("slow 应该超时")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("超时没有在预算内生效: %v", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时错误不是 DeadlineExceeded: %v", err)
	}
	if result.Hint == "" || !strings.Contains(result.Hint, "toolCallTimeoutMs") {
		t.Fatalf("超时 Hint 不可操作: %q", result.Hint)
	}

	// 超时之后连接必须仍然可用：迟到的响应由读泵丢掉，不能把整条连接带走。
	echo, _ := reg.Get("mcp__fixture__echo")
	ok2, err := echo.Exec(context.Background(), json.RawMessage(`{"text":"after-timeout"}`))
	if err != nil {
		t.Fatalf("超时之后连接不可用: %v", err)
	}
	if ok2.Text != "echo:after-timeout" {
		t.Fatalf("超时之后结果错位: %q", ok2.Text)
	}
}

// ---- 4. 命名空间冲突 fail-loud ----

func TestMCPNamespaceConflictFailsLoud(t *testing.T) {
	// 同一个 server 把同名工具列两次 = 非法工具列表。
	t.Run("duplicate-tool-in-one-server", func(t *testing.T) {
		home, _ := writeMCPTestHome(t, map[string]mcpTestServer{"fixture": {Mode: "duplicate"}})
		manager := OpenMCPManager(home)
		defer manager.Close()
		err := manager.StartAll()
		if err == nil {
			t.Fatal("重复工具名必须失败")
		}
		if !errors.Is(err, ErrMCPProtocol) {
			t.Fatalf("重复列名不是 ErrMCPProtocol: %v", err)
		}
		if !strings.Contains(err.Error(), "不止一次") {
			t.Fatalf("错误没有说清是重复列名: %v", err)
		}
	})

	// 两个 server 的公开名撞车（`a`+`b__c` 与 `a__b`+`c` 归一化后同形）。
	t.Run("cross-server-namespace-collision", func(t *testing.T) {
		// `a` + 工具 `b__c` 与 `a__b` + 工具 `c` 拼出来都是 `mcp__a__b__c`。
		// 两个身份塌成同一个公开名 = 后注册的静默覆盖前一个。
		home := t.TempDir()
		servers := map[string]any{}
		for _, spec := range []struct{ server, tool string }{{"a", "b__c"}, {"a__b", "c"}} {
			servers[spec.server] = map[string]any{
				"command": os.Args[0],
				"args":    []string{"-test.run=^TestMCPHelperServer$"},
				"env": map[string]string{
					"HS02_MCP_HELPER": "1",
					"HS02_MCP_TOOLS":  spec.tool,
				},
				"reconnect": map[string]any{"enabled": false},
			}
		}
		raw, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "mcp.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		manager := OpenMCPManager(home)
		defer manager.Close()
		if err := manager.StartAll(); err != nil {
			t.Fatalf("连接不该失败: %v", err)
		}
		err = manager.RegisterInto(New())
		if err == nil {
			t.Fatal("跨 server 命名空间撞车必须响亮失败")
		}
		if !errors.Is(err, ErrMCPToolConflict) {
			t.Fatalf("撞车错误不是 ErrMCPToolConflict: %v", err)
		}
	})

	// 与已有工具重名：注册表里先放一个同名工具，MCP 侧必须报错而不是覆盖。
	t.Run("squats-on-existing-tool", func(t *testing.T) {
		home, _ := writeMCPTestHome(t, map[string]mcpTestServer{
			"fixture": {Config: map[string]any{"toolCallTimeoutMs": 200}},
		})
		manager := OpenMCPManager(home)
		manager.StartAll()
		defer manager.Close()
		reg := New()
		if err := reg.Register(&squatterTool{name: "mcp__fixture__echo"}); err != nil {
			t.Fatal(err)
		}
		err := manager.RegisterInto(reg)
		if err == nil {
			t.Fatal("与已有工具重名必须失败")
		}
		if !errors.Is(err, ErrMCPToolConflict) {
			t.Fatalf("错误不是 ErrMCPToolConflict: %v", err)
		}
	})
}

type squatterTool struct{ name string }

func (s *squatterTool) Name() string            { return s.name }
func (s *squatterTool) Description() string     { return "占位" }
func (s *squatterTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *squatterTool) Caps() spi.Caps          { return spi.Caps{} }
func (s *squatterTool) Exec(context.Context, json.RawMessage) (spi.Result, error) {
	return spi.Result{Text: "squatter"}, nil
}

// ---- 5. 没有 tools 能力的 server 不注册工具 ----

func TestMCPServerWithoutToolsCapabilityRegistersNoTools(t *testing.T) {
	home, _ := writeMCPTestHome(t, map[string]mcpTestServer{"fixture": {Mode: "notools"}})
	manager := OpenMCPManager(home)
	manager.StartAll()
	defer manager.Close()
	reg := New()
	if err := manager.RegisterInto(reg); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	for _, name := range reg.Names() {
		if strings.HasPrefix(name, "mcp__fixture__") {
			t.Fatalf("没声明 tools 能力却注册了工具: %q", name)
		}
	}
	// 但它仍然是一个"可见提供方"，所以三个资源共享工具要在。
	for _, want := range []string{mcpListResourcesToolName, mcpListTemplatesToolName, mcpReadResourceToolName} {
		if _, ok := reg.Get(want); !ok {
			t.Fatalf("资源工具 %q 缺失", want)
		}
	}
}

// ---- 6. resources 真实现 ----

func TestMCPResourcesReal(t *testing.T) {
	home, _ := writeMCPTestHome(t, map[string]mcpTestServer{"fixture": {}})
	manager := OpenMCPManager(home)
	manager.StartAll()
	defer manager.Close()
	reg := New()
	if err := manager.RegisterInto(reg); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	list, ok := reg.Get(mcpListResourcesToolName)
	if !ok {
		t.Fatal("list_mcp_resources 没注册")
	}
	result, err := list.Exec(context.Background(), json.RawMessage(`{"server":"fixture"}`))
	if err != nil {
		t.Fatalf("resources/list 失败: %v", err)
	}
	// 官方呈现契约：`MCP server: <name>` 起头，随后是 JSON。
	if !strings.HasPrefix(result.Text, "MCP server: fixture\n") {
		t.Fatalf("资源结果没有按官方形状呈现: %q", result.Text)
	}
	if !strings.Contains(result.Text, "fixture://readme") {
		t.Fatalf("resources/list 少了资源: %q", result.Text)
	}

	templates, _ := reg.Get(mcpListTemplatesToolName)
	tplResult, err := templates.Exec(context.Background(), json.RawMessage(`{"server":"fixture"}`))
	if err != nil {
		t.Fatalf("resources/templates/list 失败: %v", err)
	}
	if !strings.Contains(tplResult.Text, "fixture://items/{id}") {
		t.Fatalf("模板列表不对: %q", tplResult.Text)
	}

	read, _ := reg.Get(mcpReadResourceToolName)
	readResult, err := read.Exec(context.Background(), json.RawMessage(`{"server":"fixture","uri":"fixture://readme"}`))
	if err != nil {
		t.Fatalf("resources/read 失败: %v", err)
	}
	if !strings.Contains(readResult.Text, "hello from fixture") {
		t.Fatalf("资源正文缺失: %q", readResult.Text)
	}
	// blob 必须换成字数说明，二进制永不进历史。
	if strings.Contains(readResult.Text, "QUJD") {
		t.Fatalf("base64 blob 泄漏: %q", readResult.Text)
	}
	if !strings.Contains(readResult.Text, "[binary resource: 4 base64 characters") {
		t.Fatalf("blob 占位说明不对: %q", readResult.Text)
	}

	// 不存在的 uri → 失败且带 Hint。
	bad, err := read.Exec(context.Background(), json.RawMessage(`{"server":"fixture","uri":"fixture://nope"}`))
	if err == nil {
		t.Fatal("不存在的资源应该失败")
	}
	if bad.Hint == "" {
		t.Fatal("资源失败路径没有 Hint（R3 违规）")
	}

	// 未配置的 server 名 → 明确的 not-found，不是空结果。
	missing, err := list.Exec(context.Background(), json.RawMessage(`{"server":"nope"}`))
	if err == nil {
		t.Fatal("未配置的 server 应该失败")
	}
	if missing.Hint == "" || !errors.Is(err, ErrMCPServerNotFound) {
		t.Fatalf("未配置 server 的错误不对: %v", err)
	}

	// 缺必填参数 server → 明确失败。
	if _, err := list.Exec(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("缺 server 参数应该失败")
	}
}

// ---- 7. 停止/重启不泄漏进程 ----

func TestMCPStopRestartNoProcessLeak(t *testing.T) {
	home, logPath := writeMCPTestHome(t, map[string]mcpTestServer{"fixture": {}})
	manager := OpenMCPManager(home)
	manager.StartAll()
	defer manager.Close()

	firstPID := manager.ServerPID("fixture")
	if firstPID == 0 {
		t.Fatal("没有真实子进程")
	}
	rows := readMCPHelperLog(t, logPath)
	if len(rows) == 0 || rows[0].PID != firstPID {
		t.Fatalf("子进程 PID 与日志不一致: pid=%d rows=%+v", firstPID, rows)
	}

	// 重启一代：新 PID 必须不同，旧 PID 必须真的没了。
	if err := manager.Reconnect("fixture"); err != nil {
		t.Fatalf("reconnect 失败: %v", err)
	}
	secondPID := manager.ServerPID("fixture")
	if secondPID == 0 || secondPID == firstPID {
		t.Fatalf("reconnect 没换进程: first=%d second=%d", firstPID, secondPID)
	}
	waitFor(t, fmt.Sprintf("旧 PID %d 退出", firstPID), 10*time.Second, func() bool {
		return !processAliveMCP(firstPID)
	})
	if !processAliveMCP(secondPID) {
		t.Fatalf("新 PID %d 不该已经退出", secondPID)
	}

	// Close 之后一个都不许留。
	manager.Close()
	waitFor(t, fmt.Sprintf("PID %d 退出", secondPID), 10*time.Second, func() bool {
		return !processAliveMCP(secondPID)
	})
	if !manager.Disconnected("fixture") {
		t.Fatal("Close 后仍持有连接")
	}
}

// ---- 8. 子进程环境清洗 ----

func TestMCPChildEnvScrubsCredentials(t *testing.T) {
	t.Setenv("HS02_FAKE_API_KEY", "secret-value")
	t.Setenv("HS02_FAKE_PASSWORD", "hunter2")
	t.Setenv("DSHD_HS02_MARKER", "1")
	env := mcpChildEnv(map[string]string{"HS02_EXPLICIT": "kept"})
	joined := strings.Join(env, "\n")
	for _, banned := range []string{"HS02_FAKE_API_KEY=", "HS02_FAKE_PASSWORD=", "DSHD_HS02_MARKER="} {
		if strings.Contains(joined, banned) {
			t.Fatalf("凭据形状的变量泄漏给 MCP 子进程: %s", banned)
		}
	}
	if !strings.Contains(joined, "HS02_EXPLICIT=kept") {
		t.Fatal("显式 env 必须保留")
	}
}

// ---- 9. 断线自动重连 ----

func TestMCPAutoReconnectAfterProcessDies(t *testing.T) {
	home, _ := writeMCPTestHome(t, map[string]mcpTestServer{
		"fixture": {Reconnect: map[string]any{
			"enabled": true, "initialDelayMs": 50, "maxDelayMs": 200, "maxAttempts": 10,
		}},
	})
	manager := OpenMCPManager(home)
	manager.StartAll()
	defer manager.Close()

	firstPID := manager.ServerPID("fixture")
	if firstPID == 0 {
		t.Fatal("没有真实子进程")
	}
	// 从外面把它杀掉：模拟 server 自己崩了。
	killMCPProcess(t, firstPID)
	waitFor(t, "重连出现新进程", 15*time.Second, func() bool {
		pid := manager.ServerPID("fixture")
		return pid != 0 && pid != firstPID
	})
	reg := New()
	if err := manager.RegisterInto(reg); err != nil {
		t.Fatalf("重连后注册失败: %v", err)
	}
	echo, ok := reg.Get("mcp__fixture__echo")
	if !ok {
		t.Fatal("重连后工具不见了")
	}
	result, err := echo.Exec(context.Background(), json.RawMessage(`{"text":"recovered"}`))
	if err != nil {
		t.Fatalf("重连后调用失败: %v", err)
	}
	if result.Text != "echo:recovered" {
		t.Fatalf("重连后结果不对: %q", result.Text)
	}
}

// ---- 10. 未配置任何 server 时不产生任何工具 ----

func TestMCPNoServersRegistersNothing(t *testing.T) {
	home := t.TempDir()
	manager := OpenMCPManager(home)
	defer manager.Close()
	manager.StartAll()
	reg := New()
	if err := manager.RegisterInto(reg); err != nil {
		t.Fatalf("空配置不该失败: %v", err)
	}
	if got := reg.Names(); len(got) != 0 {
		t.Fatalf("没有配置 server 却注册了工具: %v", got)
	}
	rows, err := manager.Statuses()
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
