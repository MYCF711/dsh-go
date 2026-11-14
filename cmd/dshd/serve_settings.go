package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"dshgo/internal/settings"
	"dshgo/internal/settingsform"
)

// 两个保留键落在 runner 已经读取的 session override 文件中。NUL 不可能出现在
// session id 中，因此不会冒充会话；其它会话键与既有读写路径原样保留。
const (
	settingsDefaultKey  = "\x00settingsFormDefault"
	settingsRevisionKey = "\x00settingsFormRevision"
)

// serveSettingsStore 把 ConfigForm 核心接到既有 model/permission override store。
// Base 是本次 serve 启动 flag；User 是同文件中的默认覆盖，Revision 与其原子提交。
type serveSettingsStore struct {
	models            *modelOverrideStore
	permissions       *permissionOverrideStore
	baseModel         string
	basePolicy        string
	modelLoadErr      error
	permissionLoadErr error
	// namespaces 是"由插件声明的命名空间"的通用存储（<home>/state/settings-namespaces.json）。
	//
	// rc.2 起落在它上面的是 `llm`（`providers.<route>.retryPolicy`）与官方客户端的
	// 4 个 UI 命名空间（locale / ui-theme / ui-chat / ui-settings）。internal/llm
	// 解析 provider 时读**同一份**文档里 `llm` 段的策略，所以这里写进去下一轮真生效。
	namespaces *settings.Store
}

// setLoadErrors 在 serve 启动装配时记录既有 store 的真实读盘结果。
// 设置 API 不得把“文件损坏、启动时沿用了空 map”描述成正常默认值。
func (s *serveSettingsStore) setLoadErrors(modelErr, permissionErr error) {
	s.modelLoadErr = modelErr
	s.permissionLoadErr = permissionErr
}

func newServeSettingsStore(baseModel, basePolicy string, models *modelOverrideStore, permissions *permissionOverrideStore) *serveSettingsStore {
	store := &serveSettingsStore{models: models, permissions: permissions, baseModel: baseModel, basePolicy: basePolicy}
	if models != nil {
		// home = <home>/state/session-model.json 的上两级；声明式命名空间的持久段
		// 落在同一 state 目录下的 settings-namespaces.json。这样它们与既有 override
		// 文件同源同 home，不需要在装配点再传一份 home。
		store.namespaces = settings.Open(filepath.Dir(filepath.Dir(models.path)))
	}
	return store
}

// homeDir 返回这份 store 所属的 home（namespace 文档落点的上两级）。
//
// 为什么不在构造时存一份：`serveSettingsStore` 的既有构造签名被四处调用点
// （含测试）钉住，而 home 只有一个真实来源——`settings.Store` 自己的路径。
// 从这里推出来，两个 store 永远指的是同一个 home。
func (s *serveSettingsStore) homeDir() string {
	if s == nil || s.namespaces == nil {
		return ""
	}
	return filepath.Dir(filepath.Dir(s.namespaces.Path()))
}

func (s *serveSettingsStore) Read(namespace string) (settingsform.Document, error) {
	switch namespace {
	case settingsform.NamespaceModel:
		if s.models == nil {
			return settingsform.Document{}, fmt.Errorf("model override store 未接线")
		}
		s.models.mu.Lock()
		defer s.models.mu.Unlock()
		if s.modelLoadErr != nil {
			return settingsform.Document{}, fmt.Errorf("既有 model 设置存储读取失败: %w", s.modelLoadErr)
		}
		return readSettingsDocumentLocked(s.models.m, s.baseModel, settingsform.FieldDefaultModel)
	case settingsform.NamespacePermission:
		if s.permissions == nil {
			return settingsform.Document{}, fmt.Errorf("permission override store 未接线")
		}
		s.permissions.mu.Lock()
		defer s.permissions.mu.Unlock()
		if s.permissionLoadErr != nil {
			return settingsform.Document{}, fmt.Errorf("既有 permission 设置存储读取失败: %w", s.permissionLoadErr)
		}
		return readSettingsDocumentLocked(s.permissions.m, s.basePolicy, settingsform.FieldDefaultPreset)
	case settingsform.NamespaceLLM,
		settingsform.NamespaceLocale, settingsform.NamespaceUITheme,
		settingsform.NamespaceUIChat, settingsform.NamespaceUISettings,
		// 2026-11-13 补：官方另两个 general slot 的命名空间。
		settingsform.NamespaceUIConversation, settingsform.NamespaceSessionLogDeepSeek,
		// 2026-11-14 补：官方 dsh-session-telemetry-otel 的命名空间（本仓无 OTel 后端，
		// mode=FEEDBACK_ONLY 由 settingsform 的语义校验回 501）。
		settingsform.NamespaceSessionTelemetryOTel,
		// 2026-11-15 补：官方 agent-preset registry 的 selectedDefault（后续会话的
		// 默认 preset；官方 UI 的"设为默认"写的就是这个命名空间）。
		settingsform.NamespaceAgentPreset:
		if s.namespaces == nil {
			return settingsform.Document{}, settingsStoreNotWiredError(namespace)
		}
		// Base 由 settingsform 的声明默认值补（readOrigin 的 doc.Base==nil 分支）：
		// 这里只给持久段的 user 与 revision。
		value, revision, err := s.namespaces.Read(namespace)
		if err != nil {
			return settingsform.Document{}, fmt.Errorf("既有 %s 设置存储读取失败: %w", namespace, err)
		}
		return settingsform.Document{User: value, Revision: revision}, nil
	default:
		return settingsform.Document{}, &settingsform.Error{Code: "not_found", Message: "未知设置 namespace: " + namespace, Hint: "只使用 settings.describe 返回的命名空间", Status: http.StatusNotFound}
	}
}

func readSettingsDocumentLocked(values map[string]string, base, field string) (settingsform.Document, error) {
	doc := settingsform.Document{Base: map[string]any{field: base}}
	if raw, ok := values[settingsRevisionKey]; ok {
		revision, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 0 {
			return settingsform.Document{}, fmt.Errorf("设置 revision 损坏 %q", raw)
		}
		doc.Revision = revision
	}
	if value, ok := values[settingsDefaultKey]; ok {
		doc.User = map[string]any{field: value}
	}
	return doc, nil
}

func (s *serveSettingsStore) Write(namespace string, user map[string]any, expected *int64) (int64, error) {
	switch namespace {
	case settingsform.NamespaceModel:
		if s.models == nil {
			return 0, fmt.Errorf("model override store 未接线")
		}
		s.models.mu.Lock()
		defer s.models.mu.Unlock()
		if s.modelLoadErr != nil {
			return 0, fmt.Errorf("既有 model 设置存储读取失败，拒绝写入: %w", s.modelLoadErr)
		}
		return writeSettingsDocumentLocked(&s.models.m, user, expected, settingsform.NamespaceModel, settingsform.FieldDefaultModel, s.models.flushLocked)
	case settingsform.NamespacePermission:
		if s.permissions == nil {
			return 0, fmt.Errorf("permission override store 未接线")
		}
		s.permissions.mu.Lock()
		defer s.permissions.mu.Unlock()
		if s.permissionLoadErr != nil {
			return 0, fmt.Errorf("既有 permission 设置存储读取失败，拒绝写入: %w", s.permissionLoadErr)
		}
		return writeSettingsDocumentLocked(&s.permissions.m, user, expected, settingsform.NamespacePermission, settingsform.FieldDefaultPreset, s.permissions.flushLocked)
	case settingsform.NamespaceLLM,
		settingsform.NamespaceLocale, settingsform.NamespaceUITheme,
		settingsform.NamespaceUIChat, settingsform.NamespaceUISettings,
		// 2026-11-13 补：官方另两个 general slot 的命名空间。
		settingsform.NamespaceUIConversation, settingsform.NamespaceSessionLogDeepSeek,
		// 2026-11-14 补：官方 dsh-session-telemetry-otel 的命名空间（见上面 Read 的说明）。
		settingsform.NamespaceSessionTelemetryOTel,
		// 2026-11-15 补：agent-preset registry（写路径另做"id 真实存在"校验，
		// 见 agent_preset_default.go；不写进去一个装不起来的默认值）。
		settingsform.NamespaceAgentPreset:
		if s.namespaces == nil {
			return 0, settingsStoreNotWiredError(namespace)
		}
		if namespace == settingsform.NamespaceAgentPreset {
			if err := validateAgentPresetSelection(s.homeDir(), user); err != nil {
				return 0, err
			}
		}
		revision, err := s.namespaces.Write(namespace, user, expected)
		if err != nil {
			var conflict *settings.RevisionConflictError
			if errors.As(err, &conflict) {
				// 与 model/permission 逐字同形的 409：客户端只需重新 describe。
				return 0, settingsform.RevisionConflict(namespace, conflict.Expected, conflict.Actual)
			}
			return 0, fmt.Errorf("%s 设置存储写入失败: %w", namespace, err)
		}
		return revision, nil
	default:
		return 0, &settingsform.Error{Code: "not_found", Message: "未知设置 namespace: " + namespace, Hint: "只使用 settings.describe 返回的命名空间", Status: http.StatusNotFound}
	}
}

// settingsStoreNotWiredError 是 llm 文档存储没接线时的结构化错误（装配 bug）：
// 说清"没写进去"，不假装成功。
func settingsStoreNotWiredError(namespace string) error {
	return &settingsform.Error{
		Code:      "internal",
		Message:   fmt.Sprintf("%s 命名空间的设置文档存储未接线（serve 装配缺 settings.Open）", namespace),
		Hint:      "检查 newServeSettingsStore 的 models override store 是否接线",
		Retryable: true,
		Status:    http.StatusInternalServerError,
	}
}

// writeSettingsDocumentLocked 在同一个既有 store 锁内检查 revision、改 map、原子写盘。
// flush 失败恢复旧 map，避免 HTTP 报错后内存却显示新值的假成功。
func writeSettingsDocumentLocked(target *map[string]string, user map[string]any, expected *int64, namespace, field string, flush func() error) (int64, error) {
	values := *target
	var revision int64
	if raw, ok := values[settingsRevisionKey]; ok {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("设置 revision 损坏 %q", raw)
		}
		revision = parsed
	}
	if expected != nil && *expected != revision {
		return 0, settingsform.RevisionConflict(namespace, *expected, revision)
	}
	if revision == math.MaxInt64 {
		return 0, fmt.Errorf("%s revision 已溢出", namespace)
	}
	old := make(map[string]string, len(values))
	for key, value := range values {
		old[key] = value
	}
	next := make(map[string]string, len(values)+2)
	for key, value := range values {
		next[key] = value
	}
	if value, ok := user[field]; ok {
		text, ok := value.(string)
		if !ok {
			return 0, fmt.Errorf("%s.%s 持久值不是 string", namespace, field)
		}
		next[settingsDefaultKey] = text
	} else {
		delete(next, settingsDefaultKey)
	}
	revision++
	next[settingsRevisionKey] = strconv.FormatInt(revision, 10)
	*target = next
	if err := flush(); err != nil {
		*target = old
		return 0, err
	}
	return revision, nil
}

// createWithSettingsDefaults 固化“后续新会话默认”语义。总是写有效值（包括
// 尚未显式覆盖的启动默认），因此未来 set/reset 不会 retroactive 改旧会话。
func createWithSettingsDefaults(create func(string) (string, error), settings *serveSettingsStore) func(string) (string, error) {
	return func(mode string) (string, error) {
		id, err := create(mode)
		if err != nil {
			return "", err
		}
		model, err := settings.Read(settingsform.NamespaceModel)
		if err != nil {
			return "", fmt.Errorf("会话 %s 已创建，但读取默认 model 失败: %w", id, err)
		}
		permission, err := settings.Read(settingsform.NamespacePermission)
		if err != nil {
			return "", fmt.Errorf("会话 %s 已创建，但读取默认 permission 失败: %w", id, err)
		}
		modelValue := effectiveSettingsValue(model, settingsform.FieldDefaultModel)
		permissionValue := effectiveSettingsValue(permission, settingsform.FieldDefaultPreset)
		if err := settings.models.Set(id, modelValue); err != nil {
			return "", fmt.Errorf("会话 %s 已创建，但保存默认 model 失败: %w", id, err)
		}
		if err := settings.permissions.Set(id, permissionValue); err != nil {
			return "", fmt.Errorf("会话 %s 已创建，但保存默认 permission 失败: %w", id, err)
		}
		return id, nil
	}
}

func effectiveSettingsValue(doc settingsform.Document, field string) string {
	if value, ok := doc.User[field].(string); ok {
		return value
	}
	value, _ := doc.Base[field].(string)
	return value
}

// settingsParams 是所有 additive HTTP 裸参数的并集；每个方法另有键白名单。
type settingsParams struct {
	NS               string                `json:"ns,omitempty"`
	Ops              []settingsform.PathOp `json:"ops,omitempty"`
	ExpectedRevision *int64                `json:"expectedRevision,omitempty"`
	Path             *[]string             `json:"path,omitempty"`
	Value            any                   `json:"value,omitempty"`
}

type settingsEnvelope struct {
	Result any                 `json:"result,omitempty"`
	Error  *settingsform.Error `json:"error,omitempty"`
}

var settingsMethodParams = map[string][]string{
	"settings.describe":      {},
	"settings.mutate":        {"ns", "ops", "expectedRevision"},
	"settings.form.describe": {},
	"settings.form.get":      {"ns"},
	"settings.form.set":      {"ns", "path", "value", "expectedRevision"},
	"settings.form.reset":    {"ns", "path", "expectedRevision"},
}

func newSettingsFormHTTPHandler(store *serveSettingsStore) http.Handler {
	// 官方 0.2.2-rc.2 起 `settings.describe` 要发布 `llm` 命名空间里的
	// `providers.<route>.retryPolicy` 节点（重试条件/次数/等待时间的可选项就是
	// 从这份 schema 读的）。HostRegistry 在默认两命名空间之上补 `llm`；
	// 默认 Registry 保持不变（单独用它的人行为一字未变）。
	return newSettingsFormHTTPHandlerWithService(settingsform.NewService(store, settingsform.HostRegistry()))
}

// newSettingsFormHTTPHandlerWithService 用显式 Service 构造 handler。
//
// 保留 newSettingsFormHTTPHandler(store) 作为生产默认入口（DefaultRegistry，
// 行为不变）；这个构造器给"插件声明 namespace 的 Registry"和测试用：
// 它们需要 settingsform.NewService(store, registry) 造出的 Service，而
// newSettingsFormHTTPHandler 只知道 store。
func newSettingsFormHTTPHandlerWithService(service *settingsform.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/api/")
		allowed, ok := settingsMethodParams[method]
		if r.Method != http.MethodPost || !ok || "/api/"+method != r.URL.Path {
			writeSettingsFormError(w, &settingsform.Error{Code: "not_found", Message: "未知设置 Transport 方法", Hint: "检查 /api/settings.* 路径与 POST 方法", Status: http.StatusNotFound})
			return
		}
		var params settingsParams
		if err := decodeSettingsParams(r, method, allowed, &params); err != nil {
			writeSettingsFormError(w, err)
			return
		}
		if method != "settings.describe" && method != "settings.form.describe" && params.NS == "" {
			writeSettingsFormError(w, &settingsform.Error{Code: "bad_request", Message: method + " 缺 ns", Hint: "ns 必须是 settings.describe 返回的命名空间", Status: http.StatusBadRequest})
			return
		}
		var result any
		var err error
		switch method {
		case "settings.describe", "settings.form.describe":
			var described settingsform.DescribeResult
			described, err = service.Describe()
			result = redactSettingsDescribe(described)
		case "settings.form.get":
			var section settingsform.Section
			section, err = service.Get(params.NS)
			if err == nil {
				var secrets []string
				secrets, err = settingsSecretPaths(service, params.NS)
				if err == nil {
					result = redactSettingsSection(section, secrets)
				}
			}
		case "settings.mutate":
			secretValues := settingsSecretInputValues(service, params.NS, params.Ops)
			var mutation settingsform.MutationResult
			mutation, err = service.Mutate(params.NS, params.Ops, params.ExpectedRevision)
			if err != nil {
				err = redactSettingsErrorValues(err, secretValues)
			}
			result = redactSettingsNamespace(mutation.Namespace)
		case "settings.form.set":
			if params.Path == nil {
				err = &settingsform.Error{Code: "invalid_path", Message: "settings.form.set 缺 path", Hint: "path 必须是 [field]", Status: http.StatusBadRequest}
				break
			}
			ops := []settingsform.PathOp{{Op: "set", Path: *params.Path, Value: params.Value}}
			secretValues := settingsSecretInputValues(service, params.NS, ops)
			var mutation settingsform.MutationResult
			mutation, err = service.Mutate(params.NS, ops, params.ExpectedRevision)
			if err != nil {
				err = redactSettingsErrorValues(err, secretValues)
			}
			result = redactSettingsSection(mutation.Section, mutation.Namespace.Secrets)
		case "settings.form.reset":
			var path []string
			if params.Path != nil {
				path = *params.Path
			}
			var mutation settingsform.MutationResult
			mutation, err = service.Reset(params.NS, path, params.ExpectedRevision)
			result = redactSettingsSection(mutation.Section, mutation.Namespace.Secrets)
		}
		if err != nil {
			writeSettingsFormError(w, err)
			return
		}
		if result == nil {
			writeSettingsFormError(w, fmt.Errorf("%s 没有返回设置描述符", method))
			return
		}
		writeSettingsFormJSON(w, http.StatusOK, settingsEnvelope{Result: result})
	})
}

// ---------------------------------------------------------------------------
// L1 secret 值剥离（T09 / R4 安全行为审查）
// ---------------------------------------------------------------------------
//
// R4 冻结的是**形状**：方法名、字段名、NamespaceView.Secrets 仍是 []string、
// sections/errors 与非 secret 值的行为不变。安全要求是另一条：任何经本文件
// 返回的 value/base/user（以及 Section.Fields 的 current/default）都不许携带
// namespace 声明的 secret 槽位值。这里只做值剥离，不删字段、不改 secrets 列表。
//
// 所有 L1 出口都走下面三个函数：settings.describe / settings.form.describe
// （redactSettingsDescribe）、settings.mutate（redactSettingsNamespace）、
// settings.form.get/set/reset（redactSettingsSection）。新增方法时必须复用，
// 否则就是给密钥开了一条新出口。

// redactSettingsDescribe 剥离整个 DescribeResult 的 secret 值。
//
// NamespaceView.Secrets 是 []string（L1 冻结形状），它同时是"哪些字段是 secret"
// 的唯一公开来源；secrets 列表本身原样保留，只把 value/base/user 里的同名键删掉。
// Sections 没有 Secrets 字段，因此按 ns 从 Namespaces 取同一份列表再剥
// Field.Current / Field.Default。
func redactSettingsDescribe(described settingsform.DescribeResult) settingsform.DescribeResult {
	if len(described.Namespaces) == 0 && len(described.Sections) == 0 {
		return described
	}
	secretByNS := make(map[string][]string, len(described.Namespaces))
	namespaces := make([]settingsform.NamespaceView, len(described.Namespaces))
	for index, namespace := range described.Namespaces {
		secretByNS[namespace.NS] = append([]string(nil), namespace.Secrets...)
		namespaces[index] = redactSettingsNamespace(namespace)
	}
	sections := make([]settingsform.Section, len(described.Sections))
	for index, section := range described.Sections {
		sections[index] = redactSettingsSection(section, secretByNS[section.NS])
	}
	described.Namespaces = namespaces
	described.Sections = sections
	return described
}

// redactSettingsNamespace 返回一个副本：Value/Base/User 中删除 Secrets 列出的键。
// 输入 map 不修改（NamespaceView 的 map 可能仍与 DescribeResult/存储快照共享）。
func redactSettingsNamespace(namespace settingsform.NamespaceView) settingsform.NamespaceView {
	if len(namespace.Secrets) == 0 {
		return namespace
	}
	namespace.Value = redactSettingsSecretMap(namespace.Value, namespace.Secrets)
	namespace.Base = redactSettingsSecretMap(namespace.Base, namespace.Secrets)
	namespace.User = redactSettingsSecretMap(namespace.User, namespace.Secrets)
	return namespace
}

// redactSettingsSection 返回一个副本：secret 字段的 current/default 置 nil。
//
// 为什么保留 Field 条目而不是整条删除：Section.Fields 是 L1 的字段描述列表，
// 删条目会让客户端以为字段不存在；保留 name/path/type/writable，只把值置 nil，
// 形状与非 secret 字段的行为保持不变。Overridden 仍能表达"用户是否覆盖过"，
// 与 NamespaceView.Secrets 的 set 语义一致（只报有没有，不报值）。
func redactSettingsSection(section settingsform.Section, secrets []string) settingsform.Section {
	if len(secrets) == 0 {
		return section
	}
	secretNames := make(map[string]struct{}, len(secrets))
	for _, name := range secrets {
		secretNames[name] = struct{}{}
	}
	fields := make([]settingsform.Field, len(section.Fields))
	copy(fields, section.Fields)
	for index := range fields {
		if _, secret := secretNames[fields[index].Name]; secret {
			fields[index].Current = nil
			fields[index].Default = nil
		}
	}
	section.Fields = fields
	return section
}

func redactSettingsSecretMap(values map[string]any, secrets []string) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	for _, name := range secrets {
		delete(out, name)
	}
	return out
}

// settingsSecretPaths 返回一个 namespace 的 secret 字段列表。
//
// settingsform.Section 不带 Secrets 元数据，settings.form.get 只能从同一次
// Service 的公开 describe 描述符取；settings.describe/mutate/form.set/reset 都
// 已经天然带 Secrets/可由 MutationResult.Namespace 取到，不需要这次额外读取。
// Describe 失败必须 fail-loud：静默返回空列表等于让 form.get 绕过剥离。
func settingsSecretPaths(service *settingsform.Service, namespace string) ([]string, error) {
	described, err := service.Describe()
	if err != nil {
		return nil, err
	}
	for _, view := range described.Namespaces {
		if view.NS == namespace {
			return append([]string(nil), view.Secrets...), nil
		}
	}
	return nil, nil
}

// settingsSecretInputValues 从一次写请求的 PathOp 里取 secret 字段的字符串值。
// 只用于错误信息回抹：settingsform 的 invalid_enum 会把提交值 %q 写进 message；
// 如果 secret 字段同时声明了 enum，不在错误路径回抹就等于把密钥写进响应。
func settingsSecretInputValues(service *settingsform.Service, namespace string, ops []settingsform.PathOp) []string {
	secretNames, err := settingsSecretPaths(service, namespace)
	if err != nil {
		return nil
	}
	secretSet := make(map[string]struct{}, len(secretNames))
	for _, name := range secretNames {
		secretSet[name] = struct{}{}
	}
	var values []string
	for _, op := range ops {
		if op.Op != "set" || len(op.Path) != 1 {
			continue
		}
		if _, secret := secretSet[op.Path[0]]; !secret {
			continue
		}
		if text, ok := op.Value.(string); ok && text != "" {
			values = append(values, text)
		}
	}
	return values
}

// redactSettingsErrorValues 把本次请求里确认属于 secret 字段的原文从错误信息里
// 替换掉；非 secret 字段的错误文案保持原样（R4 非 secret 行为冻结）。
func redactSettingsErrorValues(err error, secretValues []string) error {
	if err == nil || len(secretValues) == 0 {
		return err
	}
	apiErr := settingsform.AsError(err)
	if apiErr == nil {
		return err
	}
	redacted := *apiErr
	for _, value := range secretValues {
		if value == "" {
			continue
		}
		redacted.Message = strings.ReplaceAll(redacted.Message, value, "[redacted]")
		redacted.Hint = strings.ReplaceAll(redacted.Hint, value, "[redacted]")
	}
	return &redacted
}

func decodeSettingsParams(r *http.Request, method string, allowed []string, target *settingsParams) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return &settingsform.Error{Code: "bad_request", Message: "读取设置参数失败: " + err.Error(), Hint: "直接发送该方法定义的 JSON 裸对象", Status: http.StatusBadRequest}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte(`{}`)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return &settingsform.Error{Code: "bad_request", Message: method + " 参数必须是 JSON 裸对象", Hint: "不要包 params，也不要发送数组或 null", Status: http.StatusBadRequest}
	}
	if _, wrapped := fields["params"]; wrapped {
		return &settingsform.Error{Code: "bad_request", Message: method + " 不接受 params 包装", Hint: "把参数直接放在顶层 JSON 对象中", Status: http.StatusBadRequest}
	}
	whitelist := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		whitelist[key] = true
	}
	for key := range fields {
		if !whitelist[key] {
			return &settingsform.Error{Code: "bad_request", Message: method + " 不接受参数 " + key, Hint: "只发送 settings-form.snapshot.json 中此 method 列出的参数", Status: http.StatusBadRequest}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &settingsform.Error{Code: "bad_request", Message: method + " 参数类型错误: " + err.Error(), Hint: "检查 ns/op/path/value/expectedRevision 的 JSON 类型", Status: http.StatusBadRequest}
	}
	if rawPath, exists := fields["path"]; exists && bytes.Equal(bytes.TrimSpace(rawPath), []byte("null")) {
		return &settingsform.Error{Code: "invalid_path", Message: method + " 的 path 不能为 null", Hint: "reset 省略 path 才清整个 namespace；否则传 [field]", Status: http.StatusBadRequest}
	}
	if method == "settings.mutate" {
		rawOps, exists := fields["ops"]
		if !exists || bytes.Equal(bytes.TrimSpace(rawOps), []byte("null")) {
			return &settingsform.Error{Code: "bad_request", Message: "settings.mutate 的 ops 必须是数组，不能省略或 null", Hint: "无字段要改时发送空数组 []，不要发 null", Status: http.StatusBadRequest}
		}
	}
	return nil
}

func writeSettingsFormError(w http.ResponseWriter, err error) {
	apiErr := settingsform.AsError(err)
	status := apiErr.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	writeSettingsFormJSON(w, status, settingsEnvelope{Error: apiErr})
}

func writeSettingsFormJSON(w http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		fallback := &settingsform.Error{Code: "internal", Message: err.Error(), Hint: "检查宿主设置响应序列化错误", Retryable: true}
		raw, _ = json.Marshal(settingsEnvelope{Error: fallback})
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
}
