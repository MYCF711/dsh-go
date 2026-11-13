package settingsform

// 官方 0.2.2-rc.2 的 4 个客户端 UI 设置命名空间：`locale` / `ui-theme` /
// `ui-chat` / `ui-settings`。设置面板的「语言 / 外观 / 工作细节 / 开发者工具」
// 行读的就是这 4 个命名空间（官方 `settings.general.item` 的 NS+Path）。
//
// # 权威源（随 rc.2 asar 发布的 TypeScript 原件）
//
// <official-rc2>/dsh/node_modules/@deepseek-ai/
//
//	dsh-client-locale/src/locale-settings.ts:6-35
//	  LOCALE_SETTINGS_NAMESPACE='locale'、LOCALE_PREFERENCE_FIELD='preference'
//	  :12 LOCALE_ID_PATTERN=/^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8})*$/u
//	  :31 preference: z.string().pattern(...).required(false)  —— 无默认值（缺省交给浏览器）
//	dsh-client-ui-theme/src/theme-settings.ts:6-147
//	  :21 DEFAULT_PREFERENCE='system'、:24/:27 FONT_SIZE_MIN=10 / FONT_SIZE_MAX=22
//	  :30 DEFAULT_FONT_SIZE=14
//	  :57-61 FONT_SIZE_SPECS：text 10..22=14 / code 10..16=11 / terminal 10..20=13
//	  :72-76 FONT_FAMILY_FIELDS：textFontFamily / codeFontFamily / terminalFontFamily
//	  :139-147 ThemeSettingsSchema：三个 *FontFamily 都是 z.string().default('')
//	dsh-client-ui-chat/src/chat-settings.ts:6-98
//	  :36 TRANSCRIPT_VIEW_MODES={compact,standard,detailed,verbose}
//	  :45/:48 LEGACY normal / expanded（读作 detailed、不作为选项、不回写）
//	  :56 DEFAULT_TRANSCRIPT_VIEW_MODE='detailed'
//	  :21 DEFAULT_COLLAPSE_TIMING='completion'、:33 DEFAULT_FOLDED_REPLIES='all'
//	  :65 DEFAULT_PERFORMANCE_USAGE='detailed'、:71 DEFAULT_LINK_OPENING='sidebar'
//	  :88-95 ChatSettingsFields：transcriptView 是
//	    z.union([...current, normal, expanded]).loose()（无默认值，写入宽松）
//	dsh-client-ui-settings/src/developer-tools-settings.ts:5-19
//	  :5 DEVELOPER_TOOLS_NAMESPACE='ui-settings'、:15 enabled: z.boolean().default(true)
//
// # 线上形状
//
// 注册到 Host 的不是 `*SettingsSchema`，而是各包 host 侧的 `Config`
// （dsh-client-*/src/index.ts：`export const Config = z.object({...})`，字段与
// 默认值同 *SettingsSchema，另外每个叶子 `.volatile()`）。`settings.describe`
// 下发 `volatileForm(Config).toJSON()`——见 dsh-settings/src/index.ts:338
// （`schema: form.toJSON()`）、src/schema.ts:16-60（plainSchema 只删
// `meta.volatile`，其余 meta 原样）。所以线上 envelope 的每个叶子仍带
// default/min/max/step/pattern/required/loose，且根 object 带 `meta.default={}`。
//
// 本文件的 envelope 与官方 node_modules 实跑结果逐字段一致
// （复现脚本：tmp/GO8-RC2/_ns/dump-schema.mjs → _ns/official-envelopes.json）。
//
// # 与官方有意不同的两处（都是"更严"，并已在报告里登记）
//
//  1. `transcriptView` 的官方 schema 是 `.loose()`：坏值在官方会被**静默退回
//     默认**。本宿主沿用既有 fail-loud 政策（不许静默截断），坏值直接四字段
//     错误；schema 仍按官方发布 `meta.loose=true`（形状不失真）。
//  2. 未知字段：官方 object 解析会把额外键原样带过；本宿主拒绝
//     （`unknown_field`），与 `llm` 命名空间既有行为一致。

// 4 个命名空间的名字（官方常量逐字）。
const (
	// NamespaceLocale 是 dsh-client-locale 的命名空间。
	NamespaceLocale = "locale"
	// NamespaceUITheme 是 dsh-client-ui-theme 的命名空间。
	NamespaceUITheme = "ui-theme"
	// NamespaceUIChat 是 dsh-client-ui-chat 的命名空间。
	NamespaceUIChat = "ui-chat"
	// NamespaceUISettings 是 dsh-client-ui-settings（开发者工具）的命名空间。
	NamespaceUISettings = "ui-settings"
)

// 字段名（官方常量逐字）。
const (
	// FieldPreference 是 locale / ui-theme 共用的 `preference`。
	FieldPreference = "preference"
	// FieldFontSize 是会话正文的字号（px）。
	FieldFontSize = "fontSize"
	// FieldCodeFontSize 是代码块字号（px）。
	FieldCodeFontSize = "codeFontSize"
	// FieldTerminalFontSize 是侧栏终端字号（px）。
	FieldTerminalFontSize = "terminalFontSize"
	// FieldTextFontFamily / FieldCodeFontFamily / FieldTerminalFontFamily 是三类
	// 字体列表；空串表示沿用内置字体栈。
	FieldTextFontFamily     = "textFontFamily"
	FieldCodeFontFamily     = "codeFontFamily"
	FieldTerminalFontFamily = "terminalFontFamily"
	// FieldTranscriptView 是工作细节的展示模式。
	FieldTranscriptView = "transcriptView"
	// FieldCollapseTiming 是 Turn 收起的时机。
	FieldCollapseTiming = "collapseTiming"
	// FieldFoldedReplies 是收起时保留哪些助手回复。
	FieldFoldedReplies = "foldedReplies"
	// FieldPerformanceUsage 是性能与用量明细的粒度。
	FieldPerformanceUsage = "performanceUsage"
	// FieldLinkOpening 是点击 HTTP(S) 链接的落点。
	FieldLinkOpening = "linkOpening"
	// FieldDeveloperToolsEnabled 是开发者工具总开关。
	FieldDeveloperToolsEnabled = "enabled"
)

// 官方默认值（逐条对应上面权威源的 file:line）。
const (
	// DefaultThemePreference 是 theme-settings.ts:21 的 DEFAULT_PREFERENCE。
	DefaultThemePreference = "system"
	// DefaultFontSize / DefaultCodeFontSize / DefaultTerminalFontSize 是
	// theme-settings.ts:57-61 FONT_SIZE_SPECS 的 default。
	DefaultFontSize         = 14
	DefaultCodeFontSize     = 11
	DefaultTerminalFontSize = 13
	// FontSizeMin / FontSizeMax / CodeFontSizeMin / CodeFontSizeMax /
	// TerminalFontSizeMin / TerminalFontSizeMax 是同一张表的 min/max。
	FontSizeMin         = 10
	FontSizeMax         = 22
	CodeFontSizeMin     = 10
	CodeFontSizeMax     = 16
	TerminalFontSizeMin = 10
	TerminalFontSizeMax = 20
	// DefaultTranscriptView 是 chat-settings.ts:56 的 DEFAULT_TRANSCRIPT_VIEW_MODE
	// （客户端默认值：schema 本身没有 meta.default）。
	DefaultTranscriptView = "detailed"
	// DefaultCollapseTiming / DefaultFoldedReplies / DefaultPerformanceUsage /
	// DefaultLinkOpening 是 chat-settings.ts:21/:33/:65/:71。
	DefaultCollapseTiming   = "completion"
	DefaultFoldedReplies    = "all"
	DefaultPerformanceUsage = "detailed"
	DefaultLinkOpening      = "sidebar"
	// DefaultDeveloperToolsEnabled 是 developer-tools-settings.ts:15。
	DefaultDeveloperToolsEnabled = true
	// LegacyTranscriptViewNormal / LegacyTranscriptViewExpanded 是
	// chat-settings.ts:45/:48 的两代旧值：读作 detailed，不作为选项、不回写。
	LegacyTranscriptViewNormal   = "normal"
	LegacyTranscriptViewExpanded = "expanded"
	// LocaleIDPattern 是 locale-settings.ts:12 的 LOCALE_ID_PATTERN.source。
	LocaleIDPattern = `^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8})*$`
)

// 官方枚举（顺序即官方定义序）。
var (
	// ThemePreferences 是 theme-settings.ts:6 THEME_PREFERENCES。
	ThemePreferences = []string{"light", "dark", "system"}
	// TranscriptViewModes 是 chat-settings.ts:36 TRANSCRIPT_VIEW_MODES。
	TranscriptViewModes = []string{"compact", "standard", "detailed", "verbose"}
	// CollapseTimings 是 chat-settings.ts:15 COLLAPSE_TIMINGS。
	CollapseTimings = []string{"completion", "next-input"}
	// FoldedRepliesValues 是 chat-settings.ts:27 FOLDED_REPLIES。
	FoldedRepliesValues = []string{"all", "final"}
	// PerformanceUsageModes 是 chat-settings.ts:59 PERFORMANCE_USAGE_MODES。
	PerformanceUsageModes = []string{"compact", "detailed"}
	// LinkOpenings 是 chat-settings.ts:68 LinkOpening 的两个分支。
	LinkOpenings = []string{"sidebar", "new-tab"}
)

// 字段种类（envelope 节点形状）。
const (
	uiKindEnum    = "enum"
	uiKindString  = "string"
	uiKindInteger = "integer"
	uiKindBoolean = "boolean"
)

// uiField 是一个官方字段声明。**schema envelope 与 sections 字段投影都从这一张
// 表生成**：否则「schema 说 min 10 / section 说 min 22」这类漂移只能靠人肉核对。
type uiField struct {
	name string
	kind string
	// enum 只对 uiKindEnum 生效（顺序即官方定义序）。它是**schema 的 union 分支**：
	// 旧值（transcriptView 的 normal/expanded）必须在里面，否则读一份存着旧值的
	// 文档就会在校验期失败。
	enum []string
	// choices 是 sections[] 里给界面的**可选项**；nil 表示与 enum 相同。
	// transcriptView 用它把两代旧值排除在选项之外（官方裁定的"不作为选项"）。
	choices []string
	// min/max 只对 uiKindInteger 生效。
	min *int64
	max *int64
	// def 是官方 meta.default；nil 表示官方**没有**默认值（locale.preference、
	// ui-chat.transcriptView 就是这种）。
	def any
	// pattern / patternFlags 只对 uiKindString 生效（官方 meta.pattern）。
	pattern      string
	patternFlags string
	// loose 只对 uiKindEnum 生效（官方 transcriptView 的 .loose()）。
	loose bool
	// baseOnly 表示"Base 里有默认值，但官方 schema 没有 meta.default"：
	// 默认值来自客户端常量（transcriptView 的 DEFAULT_TRANSCRIPT_VIEW_MODE）。
	baseOnly bool
	// required 非 nil 时写进 meta.required（locale.preference 是 false）。
	required *bool
}

func uiEnumField(name string, values []string, def any) uiField {
	return uiField{name: name, kind: uiKindEnum, enum: append([]string(nil), values...), def: def}
}

func uiStringField(name string, def any) uiField {
	return uiField{name: name, kind: uiKindString, def: def}
}

func uiIntegerField(name string, minimum, maximum, def int64) uiField {
	low, high := minimum, maximum
	return uiField{name: name, kind: uiKindInteger, min: &low, max: &high, def: def}
}

func uiBooleanField(name string, def bool) uiField {
	return uiField{name: name, kind: uiKindBoolean, def: def}
}

func (f uiField) withPattern(pattern, flags string) uiField {
	f.pattern, f.patternFlags = pattern, flags
	return f
}

func (f uiField) withRequired(required bool) uiField {
	value := required
	f.required = &value
	return f
}

func (f uiField) withLoose() uiField {
	f.loose = true
	return f
}

// withBaseOnly 声明"Base 的默认值来自客户端常量，schema 里没有 meta.default"。
func (f uiField) withBaseOnly() uiField {
	f.baseOnly = true
	return f
}

// withChoices 单独声明界面可选项（不给默认值 = 与 schema 分支相同）。
func (f uiField) withChoices(choices []string) uiField {
	f.choices = append([]string(nil), choices...)
	return f
}

// meta 返回官方的 node meta：先放默认值，再放该字段特有的约束。
//
// 官方 toJSON 的 meta 里数字是 number（JSON 解码后就是 float64），
// normalizeSchemaMeta 负责把 int 归一成 float64。
func (f uiField) meta() map[string]any {
	meta := map[string]any{}
	switch f.kind {
	case uiKindEnum:
		if f.def != nil {
			meta["default"] = f.def
		}
		if f.loose {
			meta["loose"] = true
		}
	case uiKindString:
		if f.def != nil {
			meta["default"] = f.def
		}
		if f.pattern != "" {
			pattern := map[string]any{"source": f.pattern}
			if f.patternFlags != "" {
				pattern["flags"] = f.patternFlags
			}
			meta["pattern"] = pattern
		}
	case uiKindInteger:
		meta["step"] = 1
		if f.min != nil {
			meta["min"] = *f.min
		}
		if f.max != nil {
			meta["max"] = *f.max
		}
		if f.def != nil {
			meta["default"] = f.def
		}
	case uiKindBoolean:
		if f.def != nil {
			meta["default"] = f.def
		}
	}
	if f.required != nil {
		meta["required"] = *f.required
	}
	return meta
}

// schemaNode 把一个字段建成官方 envelope 节点，返回 refs 里的 uid。
//
// union 的每个分支都是 `z.const(v).required()`（官方 `z.union([...strings])`
// 走 Schema.from(primitive) → const().required()，实跑 toJSON 也是
// meta.required=true）。注意与 `llm` 的 retryableCodes 不同：那里官方是裸
// `z.const(code)`，分支上没有 required。
func (f uiField) schemaNode(b *schemaEnvelope) int {
	switch f.kind {
	case uiKindEnum:
		branches := make([]int, 0, len(f.enum))
		for _, value := range f.enum {
			branches = append(branches, b.constNode(value, true))
		}
		return b.unionNode(branches, f.meta())
	case uiKindString:
		return b.node("string", f.meta(), nil)
	case uiKindInteger:
		return b.node("number", f.meta(), nil)
	case uiKindBoolean:
		return b.node("boolean", f.meta(), nil)
	}
	return -1
}

// fieldSpec 是同一个字段在 sections[] 里的机器可读投影。
//
// sections 是本仓 L1 的 additive 面（官方 describe 只有 namespaces）：GUI 的设置
// 面板按 `sections[].fields[].current` 画当前值，所以声明式命名空间也必须给
// 字段投影，否则界面只能看到 schema、看不到值。
func (f uiField) fieldSpec() FieldSpec {
	spec := FieldSpec{Name: f.name}
	switch f.kind {
	case uiKindEnum:
		spec.Type = FieldTypeString
		if f.choices != nil {
			spec.Enum = append([]string(nil), f.choices...)
		} else {
			spec.Enum = append([]string(nil), f.enum...)
		}
	case uiKindString:
		spec.Type = FieldTypeString
	case uiKindInteger:
		spec.Type = FieldTypeInteger
		if f.min != nil {
			value := *f.min
			spec.Minimum = &value
		}
		if f.max != nil {
			value := *f.max
			spec.Maximum = &value
		}
	case uiKindBoolean:
		spec.Type = FieldTypeBoolean
	}
	return spec
}

// uiNamespace 是一个客户端 UI 命名空间的完整声明。
type uiNamespace struct {
	ns     string
	title  string
	fields []uiField
	// base 是官方默认值（Store 没有该命名空间时 describe 用它）。
	// 与字段自己的 meta.default 允许不同：`transcriptView` 的官方 schema 没有
	// default（缺省由客户端按 DEFAULT_TRANSCRIPT_VIEW_MODE 补），而设置表单的
	// 默认值就是那个客户端默认值。
	base map[string]any
	// normalize 是读路径的有效值投影（官方客户端读取时做的那一步）。
	// **只影响 describe/get 的 value 与 section.current，从不写回存储。**
	normalize func(value map[string]any)
}

// schema 生成官方 schemastery envelope（根 object 带 meta.default={}，与官方
// toJSON 一致）。
func (ns uiNamespace) schema() any {
	b := newSchemaEnvelope()
	dict := make(map[string]any, len(ns.fields))
	for _, field := range ns.fields {
		dict[field.name] = field.schemaNode(b)
	}
	root := b.node("object", map[string]any{"default": map[string]any{}}, map[string]any{"dict": dict})
	return b.envelope(root)
}

// fieldSpecs 返回 sections[] 用的字段投影（顺序即官方字段序）。
func (ns uiNamespace) fieldSpecs() []FieldSpec {
	out := make([]FieldSpec, 0, len(ns.fields))
	for _, field := range ns.fields {
		out = append(out, field.fieldSpec())
	}
	return out
}

// validate 是写/读路径共用的语义校验：先把原因说成精确的四字段错误
// （invalid_type / invalid_enum / invalid_value / out_of_range / unknown_field），
// 再交给 envelope 兜一遍（validateDeclaredValue 的顺序）。
func (ns uiNamespace) validate(value any) error {
	if value == nil {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return newInvalidTypeError([]string{ns.ns}, "object", value)
	}
	known := make(map[string]bool, len(ns.fields))
	for _, field := range ns.fields {
		known[field.name] = true
	}
	for key := range object {
		if !known[key] {
			return &Error{
				Code:    "unknown_field",
				Message: ns.ns + " 没有字段 " + key,
				Hint:    "只提交 settings.describe 里 " + ns.ns + " 的 schema 字段",
				Status:  400,
			}
		}
	}
	for _, field := range ns.fields {
		raw, present := object[field.name]
		if !present || raw == nil {
			// 官方 schema 里这些字段都可空：缺省/null 取 meta.default 或交给客户端。
			continue
		}
		if err := field.validate(ns.ns, raw); err != nil {
			return err
		}
	}
	return nil
}

func (f uiField) validate(namespace string, raw any) error {
	path := []string{namespace, f.name}
	switch f.kind {
	case uiKindEnum:
		text, ok := raw.(string)
		if !ok {
			return newInvalidTypeError(path, "string", raw)
		}
		for _, candidate := range f.enum {
			if text == candidate {
				return nil
			}
		}
		return &Error{
			Code:    "invalid_enum",
			Message: joinPath(path) + " 的值 " + quoteValue(text) + " 不在 " + joinEnum(f.enum) + " 中",
			Hint:    "只提交 settings.describe 里该字段 schema 列出的值",
			Status:  400,
		}
	case uiKindString:
		text, ok := raw.(string)
		if !ok {
			return newInvalidTypeError(path, "string", raw)
		}
		if f.pattern != "" {
			if err := matchPattern(f.pattern, f.patternFlags, text, path); err != nil {
				return err
			}
		}
		return nil
	case uiKindInteger:
		number, ok := integerValue(raw)
		if !ok {
			return newInvalidTypeError(path, "integer", raw)
		}
		if f.min != nil && number < *f.min {
			return newOutOfRangeError(path, raw, formatInt(*f.min), formatInt(*f.max))
		}
		if f.max != nil && number > *f.max {
			return newOutOfRangeError(path, raw, formatInt(*f.min), formatInt(*f.max))
		}
		return nil
	case uiKindBoolean:
		if _, ok := raw.(bool); !ok {
			return newInvalidTypeError(path, "boolean", raw)
		}
		return nil
	}
	return nil
}

// quoteValue / joinEnum / formatInt 是错误文案的小工具（保持与既有错误同风格）。
func quoteValue(value string) string { return "\"" + value + "\"" }

func joinEnum(values []string) string {
	out := "["
	for index, value := range values {
		if index > 0 {
			out += " "
		}
		out += value
	}
	return out + "]"
}

func formatInt(value int64) string {
	return formatNumber(float64(value))
}

// normalizeTranscriptView 是官方 chat-settings.ts:41-48 的读映射：
// 两代旧值（normal / expanded）一律读作 detailed。
//
// **不回写**：这里只改 describe/get 投影出来的 value，存储里的 user 段原样保留
// （`NamespaceView.User` 与持久文档都不动），所以下次读还是同一份旧值。
func normalizeTranscriptView(value map[string]any) {
	raw, ok := value[FieldTranscriptView].(string)
	if !ok {
		return
	}
	if raw == LegacyTranscriptViewNormal || raw == LegacyTranscriptViewExpanded {
		value[FieldTranscriptView] = DefaultTranscriptView
	}
}

// clientUINamespaces 返回 4 个命名空间的声明（顺序：locale / ui-theme /
// ui-chat / ui-settings，与官方 settings.general.item 的行序一致）。
func clientUINamespaces() []uiNamespace {
	return []uiNamespace{
		{
			ns:    NamespaceLocale,
			title: "Language",
			fields: []uiField{
				uiStringField(FieldPreference, nil).
					withPattern(LocaleIDPattern, "u").
					withRequired(false),
			},
			// 官方默认值就是"缺省"：不写 preference，交给浏览器。
			base: map[string]any{},
		},
		{
			ns:    NamespaceUITheme,
			title: "Appearance",
			fields: []uiField{
				uiEnumField(FieldPreference, ThemePreferences, DefaultThemePreference),
				uiIntegerField(FieldFontSize, FontSizeMin, FontSizeMax, DefaultFontSize),
				uiIntegerField(FieldCodeFontSize, CodeFontSizeMin, CodeFontSizeMax, DefaultCodeFontSize),
				uiIntegerField(FieldTerminalFontSize, TerminalFontSizeMin, TerminalFontSizeMax, DefaultTerminalFontSize),
				uiStringField(FieldTextFontFamily, ""),
				uiStringField(FieldCodeFontFamily, ""),
				uiStringField(FieldTerminalFontFamily, ""),
			},
			base: map[string]any{
				FieldPreference:         DefaultThemePreference,
				FieldFontSize:           DefaultFontSize,
				FieldCodeFontSize:       DefaultCodeFontSize,
				FieldTerminalFontSize:   DefaultTerminalFontSize,
				FieldTextFontFamily:     "",
				FieldCodeFontFamily:     "",
				FieldTerminalFontFamily: "",
			},
		},
		{
			ns:    NamespaceUIChat,
			title: "Chat",
			fields: []uiField{
				// 官方合并"当前 4 个模式 + 2 个旧值"为一个 loose union：
				// 旧值是**可读可存**的（既不作为选项展示，也不回写），所以
				// schema 的 union 带旧值、section 的可选项只给当前 4 个。
				uiEnumField(FieldTranscriptView, transcriptViewAcceptableValues(), nil).
					withLoose().
					withChoices(TranscriptViewModes).
					withBaseOnly(),
				uiEnumField(FieldCollapseTiming, CollapseTimings, DefaultCollapseTiming),
				uiEnumField(FieldFoldedReplies, FoldedRepliesValues, DefaultFoldedReplies),
				uiEnumField(FieldPerformanceUsage, PerformanceUsageModes, DefaultPerformanceUsage),
				uiEnumField(FieldLinkOpening, LinkOpenings, DefaultLinkOpening),
			},
			base: map[string]any{
				FieldTranscriptView:   DefaultTranscriptView,
				FieldCollapseTiming:   DefaultCollapseTiming,
				FieldFoldedReplies:    DefaultFoldedReplies,
				FieldPerformanceUsage: DefaultPerformanceUsage,
				FieldLinkOpening:      DefaultLinkOpening,
			},
			normalize: normalizeTranscriptView,
		},
		{
			ns:    NamespaceUISettings,
			title: "Developer tools",
			fields: []uiField{
				uiBooleanField(FieldDeveloperToolsEnabled, DefaultDeveloperToolsEnabled),
			},
			base: map[string]any{FieldDeveloperToolsEnabled: DefaultDeveloperToolsEnabled},
		},
	}
}

// transcriptViewAcceptableValues 是官方 TRANSCRIPT_VIEW_SETTING_VALUES：
// 当前模式在前、旧值在后（chat-settings.ts:51-53 的拼接顺序）。
func transcriptViewAcceptableValues() []string {
	out := make([]string, 0, len(TranscriptViewModes)+2)
	out = append(out, TranscriptViewModes...)
	out = append(out, LegacyTranscriptViewNormal, LegacyTranscriptViewExpanded)
	return out
}

// buildClientUINamespaceSpecs 把声明转成注册表能收的 NamespaceSpec。
func buildClientUINamespaceSpecs() []NamespaceSpec {
	namespaces := clientUINamespaces()
	specs := make([]NamespaceSpec, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespace := namespace
		specs = append(specs, NamespaceSpec{
			NS:      namespace.ns,
			Title:   namespace.title,
			Applies: "live",
			Fields:  namespace.fieldSpecs(),
			Schema:  namespace.schema(),
			Base:    deepCloneMap(namespace.base),
			Validate: func(value any) error {
				return namespace.validate(value)
			},
			Normalize: namespace.normalize,
		})
	}
	return specs
}
