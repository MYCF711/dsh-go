package main

// 官方 0.2.2 #14 `session-title-llm` / `session-title-first-prompt-llm`：
// 首条人类消息之后，用**一次独立、无工具、不落历史**的模型调用生成会话标题。
//
// # 官方证据（app.asar 内的真实包，逐条对齐）
//
//   - `node_modules/@deepseek-ai/dsh-session-title-first-prompt-llm/lib/index.js`
//     system prompt 原文、`Generate the session title from this JSON array of human messages:` 帧、
//     `titleFromOutput`（首个非空行 + 去整行 1–3 个 `*` 包裹）、
//     `messageSeqs` 记录来源消息、finish 分类（tool-calls / max-tokens 都算失败）。
//   - `node_modules/@deepseek-ai/dsh-session-title/lib/types/normalize.js`
//     `normalizeSessionTitle`：清 OSC/CSI/ESC、C0/C1 控制字符、方向与不可见字符，
//     `\s+` → 单空格，trim，按 UTF-8 字节上限截断且不拆码点。
//   - `node_modules/@deepseek-ai/dsh-session-title/lib/index.js`
//     触发条件：`!header.isSeeded && header.origin !== "subagent" && count === 1 && 当前无 title`；
//     用户显式标题会 supersede 在飞的自动生成（`source.kind === "user"` 永不被覆盖）。
//   - `node_modules/@deepseek-ai/dsh-session-title-llm/lib/index.js`
//     共享执行控制（maxInputBytes / maxOutputTokens / timeoutMs）与
//     「标题请求与主对话请求相互独立、不进入对话历史」的硬约束。
//
// # 为什么这个文件在 cmd/dshd，而不是 internal/*
//
// 官方把标题生成挂在 session-title 服务上，触发点在主请求身份记录之后。宿主这里
// 唯一可挂的"这一轮真收工"位置是 `modelOverrideLoopRunner`（cmdServe 的默认 runner），
// 而 v4 子进程（`dshd run`）里的 agent loop 不持有 Transport journal/索引写口。
// 于是：**父进程每轮收工后**在后台跑一次独立调用，写 v4 + 索引 + journal 帧。
//
// # 三条硬边界
//
//  1. 只有"首轮已正常收口 + 当前没有任何标题"才生成；用户显式标题永远优先
//     （写前在会话写锁内**重读**，用户标题在场就放弃）；
//  2. 失败/超时/空标题一律**不写标题**、不重试、不阻塞主 turn（Kick 是后台 goroutine，
//     本函数的 Run 只被测试与它同步调用）；
//  3. 标题调用不写任何 user/assistant/turn 事件，因此不进模型历史、不进 token 计量；
//     v4 只多两条：`session/title-llm-request`（这次请求的完整审计，流式前落盘）
//     与 `session/title`（标题本身）。
//
// # 与官方的逐字对齐点（HS-03 收口的五项 KNOWN-DELTA）
//
//   - `messageSeqs` 记录来源消息的真实 seq（官方 first-prompt provider `[first.seq]`）；
//   - `source` 用官方 provider 闭集 `{kind:"provider",provider,model}`（`auto` 不在闭集里）；
//     读索引 `session-title.json` 仍是本宿主的 `user|auto` 契约，两侧各司其职；
//   - 标题调用发官方默认 effort = 模型声明顺序里的第一档（`efforts[0].id`）；
//   - 流式前先落 `session/title-llm-request`，它记录的就是真实发出的那次请求；
//   - 输入超过 `maxInputBytes` **整次拒绝**，绝不截断（截断会让审计帧与真实请求不一致）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// 官方 session-title-llm 的共享执行控制与 first-prompt 的长度目标都**没有库默认值**，
// 由装配方显式给。这里取官方 0.2.2 包内随附配置里出现的那组值：
// `maxInputBytes: 4096`、`maxOutputTokens: 64`、`targetWords: 5`、`targetCjkCharacters: 10`；
// 官方标题服务随附配置有 `maxTitleBytes: 80` 与 `120` 两档，这里取较宽的一档
// （标题是给人看的，120 字节仍远小于侧栏宽度，且不拆码点）。
//
// timeoutMs 官方同样必填：这里取 20s。标题调用是后台任务，超时只意味着"这一格
// 不写标题"，不重试；健康调用（输入 ≤4KB、输出 ≤64 token）远小于它。
const (
	autoTitleMaxInputBytes   = 4096
	autoTitleMaxOutputTokens = 64
	autoTitleMaxBytes        = 120
	autoTitleTargetWords     = 5
	autoTitleTargetCJK       = 10
	autoTitleTimeout         = 20 * time.Second

	// autoTitleProviderID 是官方 first-prompt provider 的注册 id（包里的 `name`）。
	// 它同时是 `session/title.source.provider`（谁产出了这个标题）与
	// `session/title-llm-request.titleProvider`（这次请求是哪个 provider 发的）。
	autoTitleProviderID = "session-title-first-prompt-llm"
	// autoTitleMessageSource 是官方给标题请求那条 user 消息打的 source.kind
	// （`createUserMessage({content, source:{kind:"dsh-session-title-llm"}})`）。
	autoTitleMessageSource = "dsh-session-title-llm"
	// autoTitleIndexSource 是本宿主 `session-title.json` 读索引里"自动标题"的取值。
	// 它是 Go 侧契约（`internal/session/metadata.go` 只认 user|auto），**不是** v4 事件的
	// `source.kind`：v4 走官方闭集 provider/fallback/user。
	autoTitleIndexSource = "auto"
)

// autoTitleSysPrompt 逐字来自官方 first-prompt provider 的 systemPrompt()。
func autoTitleSysPrompt() string {
	return strings.Join([]string{
		"Create a concise title for an AI coding-assistant session from the supplied human messages.",
		"Return only the title on one line, **in plain text of natural language**, with no quotes, prefix, explanation, Markdown, XML, or terminal control codes. No code is allowed.",
		"Use the language of the messages.",
		fmt.Sprintf("Aim for about %d words in non-CJK languages or %d CJK characters.", autoTitleTargetWords, autoTitleTargetCJK),
		"If the messages give little to name, still return a short best-effort title, such as Greeting, instead of explaining.",
	}, "\n")
}

// autoTitleMessage 是官方 `collectSessionTitleMessages` 的元素形状：`{seq, text}`。
//
// seq 是**官方帧输入的一部分**（用户文本进 JSON，不拼进结构分隔符），也是写进
// `session/title` 的 `messageSeqs` 的唯一来源（官方 first-prompt：`[first.seq]`）。
type autoTitleMessage struct {
	Seq  int64  `json:"seq"`
	Text string `json:"text"`
}

// autoTitleInput 逐字对齐官方 `frameMessages()`：整条输入是一个 JSON 数组。
func autoTitleInput(messages []autoTitleMessage) string {
	raw, err := json.Marshal(messages)
	if err != nil {
		// 元素只有数字与字符串，实际不可能失败；真失败了也不能把半截文字当帧发出去。
		raw = []byte("[]")
	}
	return "Generate the session title from this JSON array of human messages:\n" + string(raw)
}

// checkAutoTitleInput 是官方 `executeSessionTitleLlm()` 的输入上界检查：
// `Buffer.byteLength(input,"utf8") > maxInputBytes` 就抛错，**整次拒绝**。
//
// 这里不做"截断到 4096 再发"：截断会让审计帧记录的不是真实请求，也会让一段
// 粘贴的长消息得到基于半截文本的标题。宁可这一格没有标题，并留下可读的报错。
func checkAutoTitleInput(input string) error {
	if size := len(input); size > autoTitleMaxInputBytes {
		return fmt.Errorf("session-title-llm: input is %d bytes, exceeding maxInputBytes %d", size, autoTitleMaxInputBytes)
	}
	return nil
}

// ---------- 官方 normalize 的移植 ----------

// 这五条正则逐条对应官方 normalize.js 里的同名常量。Go 的 RE2 没有反向引用，
// 但官方这几条都不需要；字符类与量词保持一字不差。
var (
	autoTitleOSC         = regexp.MustCompile(`(?:\x1b\]|` + "\u009d" + `)[^\x07\x1b]*(?:\x07|\x1b\\|$)`)
	autoTitleCSI         = regexp.MustCompile(`(?:\x1b\[|` + "\u009b" + `)[0-?]*[ -/]*[@-~]`)
	autoTitleESC         = regexp.MustCompile(`\x1b[@-_]`)
	autoTitleControl     = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\u009f]")
	autoTitleDirectional = regexp.MustCompile("[\u200b\u200e\u200f\u202a-\u202e\u2060-\u2064\u2066-\u206f\ufeff]")
	autoTitleSpace       = regexp.MustCompile(`\s+`)
)

// autoTitleCleanText 是官方 cleanTitleText()：清控制序列 → 归一空白 → trim。
func autoTitleCleanText(input string) string {
	input = autoTitleOSC.ReplaceAllString(input, "")
	input = autoTitleCSI.ReplaceAllString(input, "")
	input = autoTitleESC.ReplaceAllString(input, "")
	input = autoTitleControl.ReplaceAllString(input, "")
	input = autoTitleDirectional.ReplaceAllString(input, "")
	input = autoTitleSpace.ReplaceAllString(input, " ")
	return strings.TrimSpace(input)
}

// autoTitleTruncateUTF8 是官方 truncateTitleUtf8()：按字节上限取最长前缀，
// 且绝不切开一个码点（否则 JSON/索引里会留下半个 rune）。
func autoTitleTruncateUTF8(input string, maxBytes int) string {
	if maxBytes <= 0 || len(input) <= maxBytes {
		return input
	}
	var b strings.Builder
	used := 0
	for _, r := range input {
		n := utf8.RuneLen(r)
		if n < 0 {
			n = 1
		}
		if used+n > maxBytes {
			break
		}
		b.WriteRune(r)
		used += n
	}
	return b.String()
}

// normalizeAutoTitle 是官方 normalizeSessionTitle()：清洗 → 加权截断 → trimEnd。
// 空串返回空串，由调用方当作"这次不写标题"。
func normalizeAutoTitle(input string, maxBytes int) string {
	return strings.TrimRight(autoTitleTruncateUTF8(autoTitleCleanText(input), maxBytes), " ")
}

// autoTitleFromOutput 是官方 titleFromOutput()：
// 取首个非空行；整行被 1–3 个 `*` 包裹时剥掉包裹；内层再出现同一串 `*` 视为
// 模型本意（不剥），首/尾是空白也不剥（官方正则要求 `\S` 开头结尾）。
func autoTitleFromOutput(text string) string {
	line := ""
	for _, item := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			line = trimmed
			break
		}
	}
	for _, marker := range []string{"***", "**", "*"} {
		if !strings.HasPrefix(line, marker) || !strings.HasSuffix(line, marker) || len(line) < 2*len(marker) {
			continue
		}
		inner := line[len(marker) : len(line)-len(marker)]
		if inner == "" || strings.Contains(inner, marker) {
			continue
		}
		first, _ := utf8.DecodeRuneInString(inner)
		last, _ := utf8.DecodeLastRuneInString(inner)
		if unicode.IsSpace(first) || unicode.IsSpace(last) {
			continue
		}
		return inner
	}
	return line
}

// ---------- 触发条件 ----------

// autoTitleUserMessage 是官方 `sessionTitleUserMessageOf()`：只认 `source.kind=="user"`
// 的 user/message，文本按 text 块以 "\n" 连接，normalize 之后为空的不算。
func autoTitleUserMessage(ev v4.Event) (autoTitleMessage, bool) {
	if ev.Type != "user/message" {
		return autoTitleMessage{}, false
	}
	var data struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Source struct {
			Kind string `json:"kind"`
		} `json:"source"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return autoTitleMessage{}, false
	}
	if data.Source.Kind != "user" {
		return autoTitleMessage{}, false
	}
	texts := make([]string, 0, len(data.Content))
	for _, block := range data.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	text := strings.Join(texts, "\n")
	if normalizeAutoTitle(text, autoTitleMaxInputBytes) == "" {
		return autoTitleMessage{}, false
	}
	return autoTitleMessage{Seq: ev.Seq, Text: text}, true
}

func autoTitleTurnEndKind(ev v4.Event) string {
	var data struct {
		Reason struct {
			Kind string `json:"kind"`
		} `json:"reason"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return ""
	}
	return data.Reason.Kind
}

// autoTitlePrompt 判定"这一格现在该不该生成标题"，并返回首条人类消息。
//
// 条件是官方 first-prompt 触发的宿主版：
//   - 头不是 seeded、不是子代理（本宿主用 DelegationDepth 表达；ParentSession 是 fork）；
//   - 只有一条合格用户消息（steer 出第二条也不算，和官方 count===1 一致）；
//   - 当前日志里**没有任何** session/title（含用户显式标题、fork 带过来的标题）；
//   - 恰好一轮 turn 已收口，且收口原因是 completed / max-tokens。
//
// 任一条不满足就返回 false：调用方只把它当"跳过"，不重试、不写任何东西。
func autoTitlePrompt(log *v4.Log) (autoTitleMessage, bool) {
	if log == nil || log.Header.IsSeeded || log.Header.DelegationDepth > 0 {
		return autoTitleMessage{}, false
	}
	var (
		first        autoTitleMessage
		users        int
		starts, ends int
		open         bool
		lastEnd      string
	)
	for _, ev := range log.Events {
		switch ev.Type {
		case "session/title":
			// 已有任何标题（user / auto / fork 带过来的）都不再补。
			return autoTitleMessage{}, false
		case "turn/start":
			starts++
			open = true
		case "turn/end":
			ends++
			open = false
			lastEnd = autoTitleTurnEndKind(ev)
		case "user/message":
			if msg, ok := autoTitleUserMessage(ev); ok {
				users++
				if users == 1 {
					first = msg
				}
			}
		}
	}
	if users != 1 || starts != 1 || ends != 1 || open {
		return autoTitleMessage{}, false
	}
	switch lastEnd {
	case "completed", "max-tokens":
		// 这一轮真的产出了回答（max-tokens 只是被截断，不是失败）。
	case "":
		// 老日志的 turn/end 可能没有 reason：按"已收口"接受，不因格式差异丢掉标题。
	default:
		return autoTitleMessage{}, false
	}
	if first.Text == "" {
		return autoTitleMessage{}, false
	}
	return first, true
}

// ---------- 官方 route / 默认 effort / 审计帧 ----------

// autoTitleRouteData 是官方 `route` 与 `source.model` 共用的形状：`{provider, model}`。
type autoTitleRouteData struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// autoTitleEffortLevel 是模型目录里一个 reasoning level 的 (key, wire) 对。
type autoTitleEffortLevel struct {
	Level string
	Wire  string
}

// autoTitleCallRoute 是这一格标题调用的真实路由，以及官方 `selectReasoningEffort` 选中的 effort。
type autoTitleCallRoute struct {
	// Provider 是本宿主的 provider 身份：目录 entry 的 `owned_by`。
	// 官方有 provider-adapter id；本宿主没有这一层，`owned_by` 是唯一可核对的真值。
	Provider string
	Model    string
	// Effort 是官方 `model.reasoning?.efforts[0]?.id`（目录声明顺序里的第一档）；
	// Wire 是它的线上取值（`off:null` 表示不发 reasoning_effort，Wire 为空）。
	Effort string
	Wire   string
}

// sourceModel 是官方 `session/title.source.model`；目录里没有该模型时返回 nil
// （官方该字段可选），避免写出 `{"provider":"","model":"…"}` 这种半真半假的形状。
func (r autoTitleCallRoute) sourceModel() *autoTitleRouteData {
	if r.Provider == "" {
		return nil
	}
	return &autoTitleRouteData{Provider: r.Provider, Model: r.Model}
}

// resolveAutoTitleCallRoute 读一次模型目录，取 (owned_by, 声明顺序里的第一档 reasoning level)。
//
// 为什么是"第一档"：官方 `selectReasoningEffort = (model) => model.reasoning?.efforts[0]?.id`
// —— 标题调用固定用该模型支持的最小一档。本宿主的 `reasoningEfforts` 对象保留网关的
// 声明顺序（env catalog 就是按 `supported_reasoning_levels` 的顺序写的），所以第一个
// key 就是官方 `efforts[0]`。目录里没有这个模型（legacy 路由）时返回空 provider 且不声明 effort。
func resolveAutoTitleCallRoute(home, modelID string, catalog *llm.ModelCatalogStore) (autoTitleCallRoute, error) {
	route := autoTitleCallRoute{Model: modelID}
	if catalog == nil {
		catalog = llm.OpenModelCatalog(home)
	}
	view, err := catalog.List()
	if err != nil {
		return route, fmt.Errorf("session-title: 读模型目录: %w", err)
	}
	for _, raw := range view.Models {
		var model struct {
			ID               string          `json:"id"`
			OwnedBy          string          `json:"owned_by"`
			ReasoningEfforts json.RawMessage `json:"reasoningEfforts"`
		}
		if err := json.Unmarshal(raw, &model); err != nil {
			return route, fmt.Errorf("session-title: 解析模型目录行: %w", err)
		}
		if model.ID != modelID {
			continue
		}
		route.Provider = model.OwnedBy
		levels, err := autoTitleEffortLevels(model.ReasoningEfforts)
		if err != nil {
			return route, fmt.Errorf("session-title: 模型 %q 的 reasoningEfforts: %w", modelID, err)
		}
		if len(levels) > 0 {
			route.Effort, route.Wire = levels[0].Level, levels[0].Wire
		}
		return route, nil
	}
	return route, nil
}

// autoTitleEffortLevels 按**声明顺序**解析 `reasoningEfforts`。
//
// 不能用 map[string]…：Go 这样解 JSON 会丢顺序，而"最小一档"正是靠顺序定义的。
// 形状与 `internal/llm` 的校验一致：`false`/缺失 = 不支持 reasoning；`off:null` 合法；
// 其余 level 的 wire 必须是非空 string。
func autoTitleEffortLevels(raw json.RawMessage) ([]autoTitleEffortLevel, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("false")) || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("不是合法 JSON: %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("不是 object")
	}
	var levels []autoTitleEffortLevel
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		level, ok := keyToken.(string)
		if !ok || level == "" {
			return nil, errors.New("含空 level")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			if level != "off" {
				return nil, fmt.Errorf("level %q 不是 off 却是 null wire", level)
			}
			levels = append(levels, autoTitleEffortLevel{Level: level})
			continue
		}
		var wire string
		if err := json.Unmarshal(value, &wire); err != nil || wire == "" {
			return nil, fmt.Errorf("level %q 的 wire 不是非空 string", level)
		}
		levels = append(levels, autoTitleEffortLevel{Level: level, Wire: wire})
	}
	return levels, nil
}

// autoTitleTextBlock 是官方 `{type:"text",text}` 的内容块。
type autoTitleTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// autoTitleRequestMessage 是官方 `createUserMessage()` 的消息形状：
// content → source → role → id（官方 createMessage 的字段顺序，id 是新生成的稳定身份）。
type autoTitleRequestMessage struct {
	Content []autoTitleTextBlock `json:"content"`
	Source  struct {
		Kind string `json:"kind"`
	} `json:"source"`
	Role string `json:"role"`
	ID   string `json:"id"`
}

// autoTitleLLMRequest 是官方 `session/title-llm-request` 的载荷，字段顺序同官方 append。
//
// 它是**审计帧**：记录这次标题调用真实发出去的 (route,system,messages,maxTokens,reasoningEffort)，
// 可据此逐字重建那次请求；它本身不产生任何 message、不进模型历史。
type autoTitleLLMRequest struct {
	TitleProvider   string                    `json:"titleProvider"`
	MessageSeqs     []int64                   `json:"messageSeqs"`
	Route           autoTitleRouteData        `json:"route"`
	System          string                    `json:"system"`
	Messages        []autoTitleRequestMessage `json:"messages"`
	MaxTokens       int                       `json:"maxTokens"`
	ReasoningEffort string                    `json:"reasoningEffort,omitempty"`
}

// newAutoTitleLLMRequest 把这次标题请求整理成官方审计帧载荷。
//
// 传进来的 input/system 必须就是**马上要发出去**的那份；只要它们一致，
// "审计帧 == 真实请求" 就是可逐字节核对的。
func newAutoTitleLLMRequest(system, input string, messageSeqs []int64, route autoTitleCallRoute) autoTitleLLMRequest {
	message := autoTitleRequestMessage{
		Content: []autoTitleTextBlock{{Type: "text", Text: input}},
		Role:    "user",
		ID:      session.NewID(),
	}
	message.Source.Kind = autoTitleMessageSource
	segs := make([]int64, len(messageSeqs))
	copy(segs, messageSeqs)
	return autoTitleLLMRequest{
		TitleProvider:   autoTitleProviderID,
		MessageSeqs:     segs,
		Route:           autoTitleRouteData{Provider: route.Provider, Model: route.Model},
		System:          system,
		Messages:        []autoTitleRequestMessage{message},
		MaxTokens:       autoTitleMaxOutputTokens,
		ReasoningEffort: route.Effort,
	}
}

// ---------- 独立模型调用 ----------

// autoTitleCall 是一次性的标题请求。没有任何工具、没有主 turn 的身份、
// 没有 follow/journal 写口——它只能返回一段文本。
type autoTitleCall struct {
	model      string
	base       string
	credential string
	api        string
	input      string
	// reasoning 是官方默认 effort 的**线上取值**（catalog level → wire）；
	// 空串表示该模型没有声明 reasoning，或者最小一档就是 off。
	reasoning string
}

func (c autoTitleCall) generate(ctx context.Context) (string, error) {
	if strings.TrimSpace(c.model) == "" {
		return "", errors.New("session-title: 没有可用的标题模型（模型 id 为空）")
	}
	if strings.TrimSpace(c.input) == "" {
		return "", errors.New("session-title: 标题输入为空")
	}
	chat := llm.NewWithAPI(c.base, c.credential, c.api)
	applyAccountRouteEnv(chat, os.Getenv)
	chat.UserAgent = "dshd/" + version + " session-title"
	resp, err := chat.Stream(ctx, llm.Request{
		Model: c.model,
		Messages: []llm.Message{
			{Role: "system", Content: autoTitleSysPrompt()},
			{Role: "user", Content: c.input},
		},
		// 独立上限：不继承主对话的 max tokens（官方同款约束）。
		MaxTokens: autoTitleMaxOutputTokens,
		// 官方 `selectReasoningEffort` 的结果：最小一档，不是本轮主对话的 effort。
		Reasoning: c.reasoning,
	}, nil)
	if err != nil {
		return "", redactServeProviderError(err, c.credential)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if resp == nil {
		return "", errors.New("session-title: 模型返回 nil response")
	}
	if len(resp.ToolCalls) > 0 || resp.FinishReason == "tool_calls" {
		return "", errors.New("session-title: 标题输出只能是文本（模型给出了工具调用）")
	}
	if resp.FinishReason == "length" {
		return "", fmt.Errorf("session-title: 标题输出撞上 maxOutputTokens=%d", autoTitleMaxOutputTokens)
	}
	title := normalizeAutoTitle(autoTitleFromOutput(resp.Content), autoTitleMaxBytes)
	if title == "" {
		return "", errors.New("session-title: 模型没有给出标题")
	}
	return title, nil
}

// ---------- 耐久写：v4 + intent + 读索引 ----------

// 下面两个结构逐字对应 internal/session 里**未导出**的 intent/索引形状。
// 那一侧写 user 标题的实现（AppendSessionTitle → appendSessionMetadata）不在本任务
// 写作用域内，而 auto 标题必须落进同一套只读恢复协议，所以在这里按同一形状重写一遍。
// 任何一侧改形状，另一侧都会立刻读不出来——这是刻意让它们互相约束。
type autoTitleTransaction struct {
	Version  int             `json:"version"`
	EventSeq int64           `json:"eventSeq"`
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data"`
}

type autoTitleIndex struct {
	Version  int     `json:"version"`
	EventSeq *int64  `json:"eventSeq,omitempty"`
	Title    *string `json:"title,omitempty"`
	Source   string  `json:"source,omitempty"`
}

func autoTitleIndexPath(sessionPath string) string {
	return filepath.Join(filepath.Dir(sessionPath), "session-title.json")
}

func autoTitleTransactionPath(sessionPath string) string {
	return filepath.Join(filepath.Dir(sessionPath), "session-title.txn.json")
}

// autoTitleIndexPresent 是便宜的前置门：有索引就说明这个会话已经有标题，
// 直接跳过而不必解压整份 v4。索引损坏也当"有标题"跳过——auto 不该去动它。
func autoTitleIndexPresent(sessionPath string) bool {
	_, err := os.Stat(autoTitleIndexPath(sessionPath))
	return err == nil
}

// writeAutoTitleIndex 与 internal/session.writeSessionMetadataIndex 同一算法：
// 同目录随机临时名 → Sync → Close → 原子 rename。不共享临时名，避免并发写者互相踩。
func writeAutoTitleIndex(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auto-title-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// ---------- 服务 ----------

// autoTitleService 是进程内的自动标题写口。
//
// 字段全部显式（home/journal/pub/超时），测试因此能构造短超时或空 journal；
// 生产由 `modelOverrideLoopRunner.autoTitleService()` 懒建一份。
type autoTitleService struct {
	home     string
	journals *transport.Journals
	pub      func(topic string, f transport.ServerFrame)
	timeout  time.Duration
	// catalog 是生产 serve 的同一目录视图（runner.catalog）。nil 时按 home 现开一份：
	// 它只用于取"官方默认 effort"与 provider 身份，不改变任何写入路径。
	catalog *llm.ModelCatalogStore

	mu     sync.Mutex
	busy   map[string]bool
	closed map[string]bool // 已判定"这一格不会再有标题"（有界，见 markDone）
	// stopping 是 stop() 之后的闸门：Kick 不再启动新 goroutine。
	stopping bool
	// ctx/cancel 是后台标题调用的父上下文：stop() 取消它，让 in-flight 的
	// 标题调用在写审计/标题/索引之前就看到 ctx.Err 并退出。
	ctx    context.Context
	cancel context.CancelFunc
	// wg 是后台标题 goroutine 的 quiescence 计数：测试清理与优雅停止必须
	// 能确定地等到它们退出，而不是靠 sleep 猜。
	wg sync.WaitGroup
}

func newAutoTitleService(home string, journals *transport.Journals, pub func(string, transport.ServerFrame)) *autoTitleService {
	ctx, cancel := context.WithCancel(context.Background())
	return &autoTitleService{
		home: home, journals: journals, pub: pub, timeout: autoTitleTimeout,
		busy: map[string]bool{}, closed: map[string]bool{},
		ctx: ctx, cancel: cancel,
	}
}

// autoTitleClosedMemoLimit 是"已判定无标题"缓存的上界：只是一层性能记忆，
// 满了就整体清空，绝不为了它无限长。判据本身永远靠 v4 现场重算。
const autoTitleClosedMemoLimit = 512

func (s *autoTitleService) isClosed(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed[sessionID]
}

func (s *autoTitleService) markClosed(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.closed) >= autoTitleClosedMemoLimit {
		s.closed = map[string]bool{}
	}
	s.closed[sessionID] = true
}

func (s *autoTitleService) begin(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[sessionID] {
		return false
	}
	s.busy[sessionID] = true
	return true
}

func (s *autoTitleService) end(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, sessionID)
}

// wait 等待所有已启动的后台标题 goroutine 退出。
//
// 测试在 TempDir 清理前、优雅停在退出前都必须走到这里：标题 goroutine 会写
// 审计帧/标题/索引，**不能**让清理与它并发（I04 三次里两次的 TempDir 竞态）。
func (s *autoTitleService) wait() {
	if s != nil {
		s.wg.Wait()
	}
}

// stop 取消并等待所有后台标题调用，之后不再接受新的 Kick。
//
// 取消语义：`Run` 在写审计之前与写标题/index 之前都检查 `ctx.Err()`，所以
// stop 之后不会再有新的审计/title/index 落盘；已在写盘中的调用由 wg.Wait
// 保证在 stop 返回前收尾。主 turn 的非阻塞语义不变（Kick 仍是后台 goroutine，
// stop 只由测试/优雅停止显式调用）。
func (s *autoTitleService) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// Kick 起一个后台 goroutine：主 turn 已经把 turn/done 推出去了，标题调用
// 绝不能挡在它前面。失败只留一行可读的宿主 stderr（凭据已脱敏），不重试。
func (s *autoTitleService) Kick(sessionID, model, base, credential string, api ...string) {
	if s == nil || s.home == "" || sessionID == "" {
		return
	}
	timeout := s.timeout
	if timeout <= 0 {
		timeout = autoTitleTimeout
	}
	s.mu.Lock()
	if s.stopping || s.ctx == nil {
		s.mu.Unlock()
		return
	}
	parent := s.ctx
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		if _, err := s.Run(ctx, sessionID, model, base, credential, api...); err != nil {
			fmt.Fprintf(os.Stderr, "自动标题未写入（会话 %s）：%v\n", sessionID, err)
		}
	}()
}

// Run 是同步入口（测试与 Kick 共用）。返回 (标题, nil) 表示写成功；
// ("", nil) 表示"这一格按规则不生成"（已有标题 / 不是首轮收口 / 并发中）；
// ("", err) 表示真实失败，调用方只记日志、不重试。
func (s *autoTitleService) Run(ctx context.Context, sessionID, model, base, credential string, api ...string) (string, error) {
	apiValue := ""
	if len(api) > 0 {
		apiValue = api[0]
	}
	// 已取消（stop/调用方取消）就什么都不写：连审计帧都不落。
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if s == nil || s.home == "" {
		return "", errors.New("session-title: 服务未接线")
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("session-title: 会话 id 为空")
	}
	if s.isClosed(sessionID) {
		return "", nil
	}
	path, err := newSessionSource(s.home, "").resolve(sessionID)
	if err != nil {
		return "", fmt.Errorf("session-title: 解析会话 %s: %w", sessionID, err)
	}
	if autoTitleIndexPresent(path) {
		s.markClosed(sessionID)
		return "", nil
	}
	if !s.begin(sessionID) {
		return "", nil
	}
	defer s.end(sessionID)

	// 一次调用一个总时限：Kick 与测试的同步调用共用同一道门，超时就"这一格不写标题"。
	// 调用方 ctx 更早到期时以它为准（WithTimeout 取更早的 deadline）。
	if timeout := s.timeout; timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("session-title: 读会话 %s: %w", sessionID, err)
	}
	first, ok := autoTitlePrompt(log)
	if !ok {
		s.markClosed(sessionID)
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// 没有模型 id 就没有可发的标题请求：明确降级，不伪装成功、不写任何帧。
	if strings.TrimSpace(model) == "" {
		return "", errors.New("session-title: 没有可用的标题模型（模型 id 为空）")
	}
	// ⑤ 输入上界：超了整次拒绝（官方 `executeSessionTitleLlm` 同款），不截断。
	input := autoTitleInput([]autoTitleMessage{first})
	if err := checkAutoTitleInput(input); err != nil {
		return "", err
	}
	// ③ 官方默认 effort：该模型声明顺序里的第一档（`selectReasoningEffort`）。
	route, err := resolveAutoTitleCallRoute(s.home, model, s.catalog)
	if err != nil {
		return "", err
	}
	// ④ 官方在流式前落审计帧：它记录的就是这次真实请求（system/input/maxTokens/route/effort）。
	request := newAutoTitleLLMRequest(autoTitleSysPrompt(), input, []int64{first.Seq}, route)
	if err := s.recordRequest(sessionID, path, request); err != nil {
		return "", err
	}
	title, err := autoTitleCall{
		model: model, base: base, credential: credential, api: apiValue,
		input: input, reasoning: route.Wire,
	}.generate(ctx)
	if err != nil {
		return "", err
	}
	// 模型调用期间被取消（stop/调用方）：审计帧已按官方顺序留下，但标题与
	// index 一律不写——"取消后不再写 title/index"的边界就在这里。
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	written, err := s.commit(sessionID, path, title, first.Seq, route)
	if err != nil {
		return "", err
	}
	if !written {
		// 提交点发现用户已经写了标题（或不再满足触发条件）：按"跳过"返回，
		// **不能**把生成结果报成成功——调用方会以为它落了盘。
		return "", nil
	}
	return title, nil
}

// recordRequest 在模型调用**之前**把这次标题请求写成 `session/title-llm-request`。
//
// 官方 `executeSessionTitleLlm()` 的顺序就是"输入/路由校验 → append 审计帧 → 流式"：
// 于是即使模型调用随后失败，日志里也留下了"当时到底请求了什么"。这条事件不产生
// message，也不进模型历史（v4 的派生投影只认 system/developer/user/assistant/tool）。
//
// 帧必须在 v4 里真正落盘之后才镜像到 journal：客户端看到的永远等于重启后读到的。
func (s *autoTitleService) recordRequest(sessionID, sessionPath string, request autoTitleLLMRequest) error {
	logw, err := session.OpenSessionAt(sessionPath)
	if err != nil {
		return fmt.Errorf("session-title: 打开会话 %s 的审计写口: %w", sessionID, err)
	}
	appendErr := logw.Append("session/title-llm-request", request)
	closeErr := logw.Close()
	if appendErr != nil {
		return fmt.Errorf("session-title: 写 session/title-llm-request 失败: %w", appendErr)
	}
	if closeErr != nil {
		return fmt.Errorf("session-title: 落盘 session/title-llm-request 失败: %w", closeErr)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("session-title: 序列化审计帧: %w", err)
	}
	if s.journals != nil {
		emitFrameAt(s.journals, s.pub, sessionID, "session/title-llm-request", raw, time.Now().UnixMilli())
	}
	return nil
}

// autoTitleTitleSource 是官方 `SessionTitleSource` 闭集里的 provider 分支：
// `{kind:"provider",provider,model?}`（fallback/user 由别的写者产出）。
type autoTitleTitleSource struct {
	Kind     string              `json:"kind"`
	Provider string              `json:"provider"`
	Model    *autoTitleRouteData `json:"model,omitempty"`
}

// autoTitleTitleEvent 是官方 `session/title` 的 provider 形状（字段顺序同官方 append）。
type autoTitleTitleEvent struct {
	Title       string               `json:"title"`
	MessageSeqs []int64              `json:"messageSeqs"`
	Source      autoTitleTitleSource `json:"source"`
}

// commit 在会话写锁内做"重读 → 判定 → intent → v4 → 索引 → 发布"。
//
// 为什么必须在写锁里重读：用户 `session.rename` 走的是同一把 `session.Log` 写锁
// （`sessionMetadataService.mutate` 先 AppendSessionTitle 再提交 journal）。
// 于是两种顺序都只有一种结局——用户标题先落盘，我重读时看到它就放弃；
// 用户标题后到，它无条件追加并成为最新值（auto 永远不覆盖 user）。
func (s *autoTitleService) commit(sessionID, sessionPath, title string, sourceSeq int64, route autoTitleCallRoute) (written bool, retErr error) {
	logw, err := session.OpenSessionAt(sessionPath)
	if err != nil {
		return false, fmt.Errorf("session-title: 打开会话 %s 的写入: %w", sessionID, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = logw.Close()
		}
	}()
	current, err := v4.ReadFile(sessionPath)
	if err != nil {
		return false, fmt.Errorf("session-title: 重读会话 %s: %w", sessionID, err)
	}
	if _, ok := autoTitlePrompt(current); !ok {
		// 拿锁这一瞬间用户已经写了标题（或这一轮不再满足条件）：静默放弃。
		s.markClosed(sessionID)
		return false, nil
	}
	if autoTitleIndexPresent(sessionPath) {
		s.markClosed(sessionID)
		return false, nil
	}

	// ① 来源消息的真实 seq（官方 first-prompt：`[first.seq]`）。
	// ② 官方 provider 来源：`{kind:"provider",provider:<titleProvider>,model:{provider,model}}`。
	//    官方闭集是 provider/fallback/user，`auto` 不在其中（旧版写它是 KNOWN-DELTA）。
	event := autoTitleTitleEvent{
		Title:       title,
		MessageSeqs: []int64{sourceSeq},
		Source: autoTitleTitleSource{
			Kind:     "provider",
			Provider: autoTitleProviderID,
			Model:    route.sourceModel(),
		},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return false, fmt.Errorf("session-title: 序列化标题: %w", err)
	}
	seq := logw.Seq()
	// intent 用的仍是**读索引形状**（source.kind=auto），不是 v4 事件的官方形状：
	// `internal/session/metadata.go` 的 intent 恢复只接受 user|auto（本任务写作用域不含
	// 该文件）。正常读路径只比对 title/source/eventSeq，两侧仍是同一份事实；差别只在
	// "v4 已写、索引未写"的崩溃窗口少一次自动恢复 —— 已在 WIRING.md 登记给 Lead。
	intent := map[string]any{
		"title":       title,
		"messageSeqs": []int64{sourceSeq},
		"source":      map[string]any{"kind": autoTitleIndexSource},
	}
	intentRaw, err := json.Marshal(intent)
	if err != nil {
		return false, fmt.Errorf("session-title: 序列化标题 intent: %w", err)
	}
	transaction := autoTitleTransaction{Version: 1, EventSeq: seq, Type: "session/title", Data: intentRaw}
	transactionPath := autoTitleTransactionPath(sessionPath)
	if err := writeAutoTitleIndex(transactionPath, transaction); err != nil {
		return false, fmt.Errorf("session-title: 写 intent 失败（v4 尚未追加）: %w", err)
	}
	if err := logw.Append("session/title", json.RawMessage(raw)); err != nil {
		// intent 从未对应过 v4 事件，留着只会让下一次 mutation 多恢复一次无用记录。
		_ = os.Remove(transactionPath)
		return false, fmt.Errorf("session-title: 写 session/title 失败: %w", err)
	}
	indexSeq, indexTitle := seq, title
	index := autoTitleIndex{Version: 1, EventSeq: &indexSeq, Title: &indexTitle, Source: autoTitleIndexSource}
	if err := writeAutoTitleIndex(autoTitleIndexPath(sessionPath), index); err != nil {
		return false, fmt.Errorf("session-title: 提交标题索引失败（intent 已保留，可从 v4 恢复）: %w", err)
	}
	if err := logw.Close(); err != nil {
		closed = true
		return false, fmt.Errorf("session-title: 落盘会话 %s 的标题: %w", sessionID, err)
	}
	closed = true

	// 耐久事实成立后才发帧：客户端看到的标题永远等于重启后读到的标题。
	// 没有 journal 就没有合法 seq 域，宁可不推（session.list 仍会回读）。
	if s.journals != nil {
		emitFrameAt(s.journals, s.pub, sessionID, "session/title", raw, time.Now().UnixMilli())
	}
	return true, nil
}

// ---------- 与默认 runner 的唯一注册点 ----------

// autoTitleService 懒建并缓存到 runner 上：cmdServe 那侧的构造点不在本任务
// 写作用域内，而服务本身只是"home + journal 出口 + 两个小缓存"，不需要外部状态。
func (r *modelOverrideLoopRunner) autoTitleService() *autoTitleService {
	if r == nil || r.base == nil || r.base.home == "" {
		return nil
	}
	r.titlesMu.Lock()
	defer r.titlesMu.Unlock()
	if r.titles == nil {
		r.titles = newAutoTitleService(r.base.home, r.base.journals, r.base.pub)
		// 生产路径带上 serve 的同一份目录视图（官方默认 effort / provider 身份都从这里取）。
		r.titles.catalog = r.catalog
	}
	return r.titles
}

// autoTitleRoute 与这一轮同一条路由：会话走 provider 覆盖时，标题调用也必须
// 打到同一个网关/凭据；否则会出现"回答在 A 网关、标题打到默认地址"的错位。
func autoTitleRoute(provider llm.ModelProviderConfig, routed bool) (base, credential, api string) {
	if routed {
		return provider.BaseURL, provider.Credential(), provider.API
	}
	return envOr("DSHD_LLM_BASE", defaultLLMBase), os.Getenv("DSHD_LLM_KEY"), os.Getenv("DSHD_LLM_API")
}

// kickAutoTitle 是 Run/RunContent 唯一的收尾钩子：只有**这一轮正常收工**才触发，
// 且在后台跑。错误只进宿主 stderr，绝不改写这一轮的结局。
func (r *modelOverrideLoopRunner) kickAutoTitle(sessionID, model string, provider llm.ModelProviderConfig, routed bool) {
	service := r.autoTitleService()
	if service == nil {
		return
	}
	base, credential, api := autoTitleRoute(provider, routed)
	service.Kick(sessionID, model, base, credential, api)
}
