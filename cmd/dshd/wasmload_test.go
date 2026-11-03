package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// wasmFixture 是 internal/wasmhost 的纯 wasm 夹具：60 字节，导出 add / spin。
// 跨包引用而不是复制一份：两个包看到同一份字节，"摘要对不上"才有意义。
// 测试的 cwd 是 cmd/dshd，所以要往上两级。
const wasmFixture = "../../internal/wasmhost/testdata/fixture.wasm"

// wasmToolFixture 是 Go reactor 夹具：导出 _initialize / alloc / call，实现工具 ABI。
//
// 两个夹具的分工：纯 wasm 那个验"装载 / 调用 / 超时打断"；
// Go 这个验"真的是一个工具"——没有它，_initialize 与 ABI 往返都测不到
// （见 internal/wasmhost/abi_test.go 与 docs/wasm-tool-abi.md §1.3）。
const wasmToolFixture = "../../internal/wasmhost/testdata/toolplugin.wasm"

// TestWasmModuleAssemblesAndShowsInGraph：合法 wasm 模块能装配，
// 装配结果里 backend 是 wasm、Provides 为空，且 `modules graph` / `list`
// 能看到它并显示 digest 前 12 位。
func TestWasmModuleAssemblesAndShowsInGraph(t *testing.T) {
	resetWasm(t)
	fixture := fixturePath(t)
	digest := sha256hexFile(t, fixture)
	logs := captureLogs(t)

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-probe"
backend = "wasm"
path    = "`+filepath.ToSlash(fixture)+`"
digest  = "`+digest+`"
`)
	mods, err := loadMods(mp, t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("合法的 wasm 模块必须能装配，实际报错：%v", err)
	}
	defer closeLoadedWasm(context.Background())

	var got *kernel.Module
	for i := range mods {
		if mods[i].ID == "wasm-probe" {
			got = &mods[i]
		}
	}
	if got == nil {
		t.Fatal("装配结果里没有 wasm-probe")
	}
	if got.Backend != kernel.BackendWasm {
		t.Fatalf("backend 应当是 wasm，实际 %q", got.Backend)
	}
	// 这条是"不许假装它提供能力"的落点：装配结果里必须真的是空的，
	// 而不是靠日志里一句话搪塞。
	if len(got.Provides) != 0 {
		t.Fatalf("wasm 模块现在不提供任何能力，Provides 应当为空，实际 %v", got.Provides)
	}
	if got.Register != nil {
		t.Fatal("还没做工具契约映射，wasm 模块不该有 Register")
	}

	list := loadedWasm()
	if len(list) != 1 {
		t.Fatalf("登记的 wasm 模块应当正好 1 个，实际 %d", len(list))
	}
	if list[0].digest != digest {
		t.Fatalf("登记的摘要应当是文件内容的 sha256：%s vs %s", list[0].digest, digest)
	}

	var graph bytes.Buffer
	if err := writeModules(&graph, "graph", mods); err != nil {
		t.Fatalf("modules graph 失败：%v", err)
	}
	g := graph.String()
	// 路径分隔符在 Windows 上是反斜杠，输出比对前统一成正斜杠：
	// 这里要钉的是"打出了哪个文件"，不是分隔符长什么样。
	gSlash := strings.ReplaceAll(g, `\`, "/")
	for _, want := range []string{"wasm-probe", "wasm", digest[:12], filepath.ToSlash(fixture)} {
		if !strings.Contains(gSlash, want) {
			t.Fatalf("modules graph 输出里应当有 %q：\n%s", want, g)
		}
	}

	var listing bytes.Buffer
	if err := writeModules(&listing, "list", mods); err != nil {
		t.Fatalf("modules list 失败：%v", err)
	}
	for _, want := range []string{"wasm-probe", "wasm", digest[:12]} {
		if !strings.Contains(listing.String(), want) {
			t.Fatalf("modules list 输出里应当有 %q：\n%s", want, listing.String())
		}
	}

	// 日志必须明说"它不提供能力"，而不是让人从空 PROVIDES 列里猜。
	if !strings.Contains(logs.String(), "不提供任何能力") {
		t.Fatalf("日志里必须明说 wasm 模块不提供任何能力：\n%s", logs.String())
	}
}

// TestWasmDigestMismatchFails：manifest 里的 digest 与实际文件不符 → 装配失败。
// 这正是 manifest 校验的意义：内容被换掉要在启动时炸。
func TestWasmDigestMismatchFails(t *testing.T) {
	resetWasm(t)
	fixture := fixturePath(t)
	real := sha256hexFile(t, fixture)
	wrong := strings.Repeat("0", 64)
	if wrong == real {
		t.Fatal("夹具摘要不可能是全 0，测试前提被破坏了")
	}

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-probe"
backend = "wasm"
path    = "`+filepath.ToSlash(fixture)+`"
digest  = "`+wrong+`"
`)
	if _, err := loadMods(mp, t.TempDir(), 0, nil); err == nil {
		t.Fatal("digest 不匹配必须让装配失败，不能装作没看见")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, "digest") && !strings.Contains(msg, "摘要") {
			t.Fatalf("错误里要指出是摘要校验失败：%v", err)
		}
		if !strings.Contains(msg, "wasm-probe") {
			t.Fatalf("错误里要点名是哪个模块：%v", err)
		}
		if !strings.Contains(msg, real) {
			t.Fatalf("错误里要给出实际摘要，便于核对：%v", err)
		}
	}
	// 失败后不能留下"已登记"的假象。
	if got := loadedWasm(); len(got) != 0 {
		t.Fatalf("装配失败后不该留下登记的 wasm 模块：%v", got)
	}
}

// TestWasmMissingFileFails：文件不存在 → 装配失败，错误里带路径。
func TestWasmMissingFileFails(t *testing.T) {
	resetWasm(t)
	missing := filepath.Join(t.TempDir(), "no-such-module.wasm")

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-gone"
backend = "wasm"
path    = "`+filepath.ToSlash(missing)+`"
`)
	_, err := loadMods(mp, t.TempDir(), 0, nil)
	if err == nil {
		t.Fatal("路径不存在必须让装配失败")
	}
	if !strings.Contains(err.Error(), "no-such-module.wasm") {
		t.Fatalf("错误里要带路径：%v", err)
	}
	if !strings.Contains(err.Error(), "wasm-gone") {
		t.Fatalf("错误里要点名是哪个模块：%v", err)
	}
}

// TestWasmNonWasmFileFails：文件在，但不是 wasm → 报错要带 wasmhost 的原始原因（魔数）。
func TestWasmNonWasmFileFails(t *testing.T) {
	resetWasm(t)
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake.wasm")
	if err := os.WriteFile(fake, []byte("这不是 wasm，只是一段文本"), 0o644); err != nil {
		t.Fatal(err)
	}

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-fake"
backend = "wasm"
path    = "`+filepath.ToSlash(fake)+`"
`)
	_, err := loadMods(mp, t.TempDir(), 0, nil)
	if err == nil {
		t.Fatal("非 wasm 文件必须让装配失败")
	}
	msg := err.Error()
	if !strings.Contains(msg, "0asm") {
		t.Fatalf("错误里要保留 wasmhost 的原始原因（魔数 \\0asm）：%v", err)
	}
	if !strings.Contains(msg, "fake.wasm") {
		t.Fatalf("错误里要带路径：%v", err)
	}
}

// TestWasmProvidesRejected：manifest 给 wasm 模块写 provides = ["compaction"] → 直接拒绝。
//
// 现在 wasm 只认 provides = ["tool"]（一个模块一个工具）。写别的东西不静默忽略，
// 是因为"写了 provides 却被丢掉"等于配置说谎：读 manifest 的人会以为这条已经生效。
func TestWasmProvidesRejected(t *testing.T) {
	resetWasm(t)
	fixture := fixturePath(t)

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id       = "wasm-probe"
backend  = "wasm"
path     = "`+filepath.ToSlash(fixture)+`"
provides = ["compaction"]
`)
	_, err := loadMods(mp, t.TempDir(), 0, nil)
	if err == nil {
		t.Fatal(`wasm 只认 provides = ["tool"]，写 compaction 必须报错`)
	}
	if !strings.Contains(err.Error(), "provides") || !strings.Contains(err.Error(), "tool") {
		t.Fatalf(`错误里要写清只认 provides = ["tool"]：%v`, err)
	}
	if !strings.Contains(err.Error(), "compaction") {
		t.Fatalf("错误里要点名是哪个能力不认识：%v", err)
	}
}

// TestWasmToolProvidesOneTool 是这一层的落点：provides = ["tool"] 的 wasm 模块
// 真的往工具表里加了一个工具。
//
// 一条用例连着钉四件事，因为它们缺一条都不算"真的提供了一个工具"：
//   - 装配时 Register 真的跑过（工具表里查得到）；
//   - 名字/描述/schema 来自 manifest（不是从客人那里问的）；
//   - Exec 走 ABI 拿到结果（InvokeTool 真的调到了客人）；
//   - `modules graph` 的 wasm 小节把工具名打出来了（人能看见它提供了什么）。
func TestWasmToolProvidesOneTool(t *testing.T) {
	resetWasm(t)
	fixture := toolFixturePath(t)

	mp := writeManifest(t, `[profile]
name = "wasm-tool"

[[module]]
id      = "tools"
backend = "static"

[[module]]
id       = "wasm-echo"
backend  = "wasm"
path     = "`+filepath.ToSlash(fixture)+`"
provides = ["tool"]
tool_name        = "echo"
tool_description = "把 text 原样回显（wasm 夹具工具）"
tool_schema      = '''{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}'''
timeout_ms       = 2000
`)
	mods, err := loadMods(mp, t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("带工具声明的 wasm 模块必须能装配，实际：%v", err)
	}

	var got *kernel.Module
	for i := range mods {
		if mods[i].ID == "wasm-echo" {
			got = &mods[i]
		}
	}
	if got == nil {
		t.Fatal("装配结果里没有 wasm-echo")
	}
	if got.Register == nil {
		t.Fatal(`provides = ["tool"] 的模块必须有 Register，否则工具根本进不了表`)
	}
	// Provides 来自 manifest（Apply 会把它原样复制上来），所以装配结果里是 ["tool"]。
	// 这正是 `modules graph` 的 PROVIDES 列能看到它的原因：**能力来源只有一个**（manifest），
	// 装载层不能替它编一个。注意 "tool" 不是服务键：真正的注册靠 Requires "tools"
	// 注入工具表，两件事不许混。
	if !containsStr(got.Provides, "tool") {
		t.Fatalf(`装配结果里应当保留 manifest 写的 provides = ["tool"]，实际 %v`, got.Provides)
	}
	if !containsStr(got.Requires, "tools") {
		t.Fatalf("要注册进工具表就必须 Requires \"tools\"，实际 %v", got.Requires)
	}

	reg := assembleForTest(t, mods)
	tool, ok := reg.Get("echo")
	if !ok {
		var names []string
		for _, x := range reg.Tools() {
			names = append(names, x.Name())
		}
		t.Fatalf("工具表里没有 echo，实际有 %v", names)
	}
	if tool.Description() != "把 text 原样回显（wasm 夹具工具）" {
		t.Fatalf("Description 必须来自 manifest，实际 %q", tool.Description())
	}
	if !json.Valid(tool.Schema()) || !strings.Contains(string(tool.Schema()), `"text"`) {
		t.Fatalf("Schema 必须是 manifest 里那段 JSON，实际 %s", tool.Schema())
	}
	if tool.Caps().NeedsApproval() {
		t.Fatalf("没写 tool_caps 就该是全 false（沙箱强制它碰不到外面），实际 %+v", tool.Caps())
	}

	// Exec 真的走了一次 ABI。
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"text":"你好"}`))
	if err != nil {
		t.Fatalf("Exec 失败：%v", err)
	}
	if !strings.Contains(res.Text, "你好") {
		t.Fatalf("结果正文应当包含传进去的 text，实际 %q", res.Text)
	}

	// 人能看见它提供了什么：graph 的 wasm 小节要打出工具名与 requires。
	var graph bytes.Buffer
	if err := writeModules(&graph, "graph", mods); err != nil {
		t.Fatalf("modules graph 失败：%v", err)
	}
	g := graph.String()
	for _, want := range []string{"wasm-echo", "echo", "tools"} {
		if !strings.Contains(g, want) {
			t.Fatalf("modules graph 里应当能看到 %q：\n%s", want, g)
		}
	}
}

// TestWasmToolBusinessErrorGivesHint：客人回 isError:true → Exec 返回 error + Hint。
//
// 这是 R3 的落点：失败必须 `Result{Hint:…}, err`。
// 模型能看懂的关键在 Hint 与 err 文本里——只写 "tool failed" 等于没说。
func TestWasmToolBusinessErrorGivesHint(t *testing.T) {
	resetWasm(t)
	tool := loadWasmTool(t, "echo", "")

	res, err := tool.Exec(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("isError:true 必须让 Exec 返回 error（否则 v4 里 isError 会是 false）")
	}
	if res.Hint == "" {
		t.Fatal("R3：失败必须带 Hint，不能空 Result 丢提示")
	}
	if !strings.Contains(res.Hint, "text") {
		t.Fatalf("Hint 应当来自客人（怎么补参数），实际 %q", res.Hint)
	}
	if res.Text == "" {
		t.Fatal("工具失败也要把正文带出来，模型才知道发生了什么")
	}
	// err 的文本会被投影成 "Error: …" 给模型看，必须带客人说的内容。
	if !strings.Contains(err.Error(), "空输入") {
		t.Fatalf("err 里应当有客人的 summary（模型看到的 Error 行），实际 %v", err)
	}
}

// TestWasmToolTrapGivesHint：客人 trap → Exec 返回 error + 「别原样重试」的 Hint。
//
// 与上一条的区别就是本轮的核心区分：trap 不是业务失败，Hint 不能暗示"改参数再试"。
func TestWasmToolTrapGivesHint(t *testing.T) {
	resetWasm(t)
	tool := loadWasmTool(t, "echo", "")

	res, err := tool.Exec(context.Background(), json.RawMessage(`{"boom":true}`))
	if err == nil {
		t.Fatal("客人 trap 必须让 Exec 返回 error")
	}
	if res.Hint == "" {
		t.Fatal("trap 也必须给 Hint（R3）")
	}
	if !strings.Contains(res.Hint, "trap") {
		t.Fatalf("Hint 要说清是插件崩了，实际 %q", res.Hint)
	}
	if !strings.Contains(res.Hint, "不要原样重试") {
		t.Fatalf("trap 的 Hint 必须劝阻「原样重试」，实际 %q", res.Hint)
	}
}

// TestWasmWithoutProvidesKeepsToolTable：没写 provides = ["tool"] 的 wasm 模块，
// 工具表**逐字节不变**。
//
// 为什么用马上比对而不是"数量相等"：工具表是模型侧缓存前缀的一部分（R4），
// 多一个字、顺序变一下都算契约变化。用字节比对才不会漏掉"只多了个空格"。
func TestWasmWithoutProvidesKeepsToolTable(t *testing.T) {
	resetWasm(t)
	fixture := toolFixturePath(t)

	base := `[profile]
name = "wasm-noop"

[[module]]
id      = "tools"
backend = "static"

[[module]]
id      = "tool-nowline"
backend = "static"
`
	withWasm := base + `
[[module]]
id      = "wasm-silent"
backend = "wasm"
path    = "` + filepath.ToSlash(fixture) + `"
`
	before := toolTableBytes(t, writeManifest(t, base))
	after := toolTableBytes(t, writeManifest(t, withWasm))

	if !bytes.Equal(before, after) {
		t.Fatalf("没写 provides 的 wasm 模块不该改变工具表：\n--- 之前 ---\n%s\n--- 之后 ---\n%s", before, after)
	}
	// 顺带把"它确实被装载了"钉住：不然上面那句可能只是因为两边都没装。
	if got := loadedWasm(); len(got) != 1 || got[0].id != "wasm-silent" {
		t.Fatalf("wasm-silent 应当被装载并登记，实际 %+v", got)
	}
	if got := loadedWasm(); got[0].tool != "" {
		t.Fatalf("没写 provides 的模块不该有工具名，实际 %q", got[0].tool)
	}
}

// TestWasmDisabledSkipped：enabled = false 的 wasm 模块连文件都不读——
// 一个关掉的模块不该因为路径坏了把启动拖下水。
func TestWasmDisabledSkipped(t *testing.T) {
	resetWasm(t)
	missing := filepath.Join(t.TempDir(), "never-touched.wasm")

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-off"
backend = "wasm"
path    = "`+filepath.ToSlash(missing)+`"
enabled = false
`)
	mods, err := loadMods(mp, t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("禁用的 wasm 模块不该被装载：%v", err)
	}
	for _, m := range mods {
		if m.ID == "wasm-off" {
			t.Fatal("禁用的模块不该出现在装配结果里")
		}
	}
	if got := loadedWasm(); len(got) != 0 {
		t.Fatalf("禁用的模块不该被登记：%v", got)
	}
}

// TestWasmToolManifestValidation：工具声明写错 → 装配时炸，并且说清缺什么。
//
// 为什么全都必填、且必须在装配期炸：这些错误在运行时的表现是
// "模型不会用这个工具"或"审批判错档"，从现象根本归因不到 manifest 少了一行。
func TestWasmToolManifestValidation(t *testing.T) {
	resetWasm(t)
	fixture := filepath.ToSlash(toolFixturePath(t))

	head := `[profile]
name = "wasm-tool"

[[module]]
id      = "tools"
backend = "static"

[[module]]
id       = "wasm-echo"
backend  = "wasm"
path     = "` + fixture + `"
provides = ["tool"]
`
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "缺 tool_name",
			body: "tool_description = \"x\"\ntool_schema = '''{\"type\":\"object\"}'''\n",
			want: "tool_name",
		},
		{
			name: "缺 tool_description",
			body: "tool_name = \"echo\"\ntool_schema = '''{\"type\":\"object\"}'''\n",
			want: "tool_description",
		},
		{
			name: "缺 tool_schema",
			body: "tool_name = \"echo\"\ntool_description = \"x\"\n",
			want: "tool_schema",
		},
		{
			name: "schema 不是对象",
			body: "tool_name = \"echo\"\ntool_description = \"x\"\ntool_schema = '''[\"a\"]'''\n",
			want: "JSON 对象",
		},
		{
			name: "schema 不是 JSON",
			body: "tool_name = \"echo\"\ntool_description = \"x\"\ntool_schema = '''{nope}'''\n",
			want: "JSON 对象",
		},
		{
			name: "tool_caps 拼错",
			body: "tool_name = \"echo\"\ntool_description = \"x\"\n" +
				"tool_schema = '''{\"type\":\"object\"}'''\ntool_caps = [\"write\"]\n",
			want: "tool_caps",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetWasm(t)
			mp := writeManifest(t, head+c.body)
			_, err := loadMods(mp, t.TempDir(), 0, nil)
			if err == nil {
				t.Fatalf("%s 必须让装配失败", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误里要点出 %q：%v", c.want, err)
			}
			if !strings.Contains(err.Error(), "wasm-echo") {
				t.Fatalf("错误里要点名模块：%v", err)
			}
			// 校验失败不能留下"已装载"的句柄。
			if got := loadedWasm(); len(got) != 0 {
				t.Fatalf("装配失败后不该留下已装载的 wasm 模块：%v", got)
			}
		})
	}
}

// --- 测试辅助 ---

func fixturePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(wasmFixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("夹具不在预期位置（%s）：%v", p, err)
	}
	return p
}

// toolFixturePath 是 Go reactor 夹具的绝对路径。
func toolFixturePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(wasmToolFixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("工具夹具不在预期位置（%s）：%v\n跑 python scripts/build-wasm-fixture.py 生成", p, err)
	}
	return p
}

// assembleForTest 真的跑一遍装配，返回工具表——不直接 new 一个注册表：
// 那样就绕过了 Register，而"Register 真的被调到"正是本轮要证明的事。
func assembleForTest(t *testing.T, mods []kernel.Module) spi.ToolRegistry {
	t.Helper()
	k := kernel.New("test")
	root := k.Root()
	t.Cleanup(root.Dispose)
	if _, err := kernel.Assemble(root, mods); err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	reg, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		t.Fatalf("取工具表失败：%v", err)
	}
	return reg
}

// toolTableBytes 走完整链路（manifest → 装载 → 装配 → 工具表 JSON），返回字节。
func toolTableBytes(t *testing.T, manifest string) []byte {
	t.Helper()
	mods, err := loadMods(manifest, t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	reg := assembleForTest(t, mods)
	table, err := buildToolTable(reg)
	if err != nil {
		t.Fatalf("工具表构建失败：%v", err)
	}
	out, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// loadWasmTool 走生产路径装一个 wasm 工具并取回来。
func loadWasmTool(t *testing.T, name, extra string) spi.Tool {
	t.Helper()
	fixture := toolFixturePath(t)
	mp := writeManifest(t, `[profile]
name = "wasm-tool"

[[module]]
id      = "tools"
backend = "static"

[[module]]
id       = "wasm-echo"
backend  = "wasm"
path     = "`+filepath.ToSlash(fixture)+`"
provides = ["tool"]
tool_name        = "`+name+`"
tool_description = "wasm 夹具工具"
tool_schema      = '''{"type":"object","properties":{"text":{"type":"string"}}}'''
`+extra)
	mods, err := loadMods(mp, t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	reg := assembleForTest(t, mods)
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("工具表里没有 %q", name)
	}
	return tool
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dsh.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func sha256hexFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// captureLogs 把 slog 默认 handler 换成 buffer，返回它以便断言日志内容。
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// resetWasm 让每个用例从"没有已装载 wasm"开始，并在结束时收干净。
// 顺带把 slog 静音：Apply 的白名单告警每个用例 40+ 行，不该淹掉测试输出。
func resetWasm(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	closeLoadedWasm(context.Background())
	t.Cleanup(func() { closeLoadedWasm(context.Background()) })
}
