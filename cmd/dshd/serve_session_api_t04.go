package main

// GO.2 T04：官方 session 残余的宿主真值
// （projections 的宿主键、selectModel 的 provider、modelCatalog、control、
// initializeDefaultModel）。
//
// 本文件与 serve_session_api.go 同属官方 session 面的宿主装配；拆开是因为
// 前者是 HS-16A 已交付的基础能力，本文件只放 T04 的增量，便于后来复核。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
	"dshgo/internal/modes"
	"dshgo/internal/settingsform"
	"dshgo/internal/transport"
)

// officialGatewayProviderID 是本宿主"没有目录级 provider 声明"时的真实路由身份。
//
// 本仓的 provider 身份沿用既有口径：目录 entry 的 `owned_by`（serve_title_auto.go
// 已把这定为唯一可核对的真值）。目录没给 owned_by 的模型走进程网关，那也是一条
// 真实路由，不能编一个不存在的 provider 名字，所以给这个稳定 id。
const officialGatewayProviderID = "gateway"

// modelSelectionRecord 是一条 provider 维度的选择（model 本身在 session-model.json）。
type modelSelectionRecord struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

type modelSelectionDocument struct {
	Version  int                             `json:"version"`
	Sessions map[string]modelSelectionRecord `json:"sessions"`
}

// modelSelectionStore 是 `session.selectModel` 的 provider/effort 元数据真值。
//
// 为什么不写进既有 `state/session-model.json`：那个文件的形状是 map[string]string
// 且由 serve.go 的 modelOverrideStore 单写（既有 58 方法共用）；本任务写作用域
// 不含 serve_model_override.go，也不该把既有形状改成结构体（R4）。所以 provider
// 元数据落在**另一份单写者文件**里：本文件是它唯一的写者。
//
// 一致性：读路径永远按 model 交叉核对——record.Model != 当前 model 时 record
// 视为陈旧（回落到目录解析），所以 `session.setModel`（只写 model）不会让旧的
// provider 记录冒充新选择。
type modelSelectionStore struct {
	mu      sync.Mutex
	path    string
	doc     modelSelectionDocument
	loadErr error
}

func newModelSelectionStore(home string) *modelSelectionStore {
	store := &modelSelectionStore{
		path: filepath.Join(home, "state", "session-model-selection.json"),
		doc:  modelSelectionDocument{Version: 1, Sessions: map[string]modelSelectionRecord{}},
	}
	store.loadErr = store.load()
	return store
}

func (s *modelSelectionStore) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读会话模型选择 %s: %w", s.path, err)
	}
	var doc modelSelectionDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("解析会话模型选择 %s: %w", s.path, err)
	}
	if doc.Version != 1 || doc.Sessions == nil {
		return fmt.Errorf("会话模型选择 %s 的 version/sessions 非法", s.path)
	}
	s.doc = doc
	return nil
}

func (s *modelSelectionStore) Get(sessionID string) (modelSelectionRecord, error) {
	if s == nil {
		return modelSelectionRecord{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return modelSelectionRecord{}, fmt.Errorf("%w: %v", transport.ErrStatePersistence, s.loadErr)
	}
	return s.doc.Sessions[sessionID], nil
}

func (s *modelSelectionStore) Set(sessionID string, record modelSelectionRecord) error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(record.Provider) == "" || strings.TrimSpace(record.Model) == "" {
		return fmt.Errorf("%w: 模型选择需要非空 sessionId/provider/model", transport.ErrOfficialBadInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("%w: 拒绝覆盖损坏的模型选择存储: %v", transport.ErrStatePersistence, s.loadErr)
	}
	next := make(map[string]modelSelectionRecord, len(s.doc.Sessions)+1)
	for id, value := range s.doc.Sessions {
		next[id] = value
	}
	next[sessionID] = record
	return s.flushLocked(next)
}

func (s *modelSelectionStore) Clear(sessionID string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("%w: 拒绝覆盖损坏的模型选择存储: %v", transport.ErrStatePersistence, s.loadErr)
	}
	next := make(map[string]modelSelectionRecord, len(s.doc.Sessions))
	for id, value := range s.doc.Sessions {
		if id == sessionID {
			continue
		}
		next[id] = value
	}
	return s.flushLocked(next)
}

func (s *modelSelectionStore) flushLocked(next map[string]modelSelectionRecord) error {
	doc := modelSelectionDocument{Version: 1, Sessions: next}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.doc = doc
	return nil
}

// catalogModelRow 是模型目录里一行**只读**投影（provider 面只需要这几个字段）。
type catalogModelRow struct {
	ID               string
	Name             string
	OwnedBy          string
	ReasoningEfforts map[string]json.RawMessage
}

// catalogModelRows 读一次真实模型目录（`<home>/state/model-catalog.json`）。
//
// 目录损坏是**真错误**：不能把"读不懂目录"降级成"没有模型"，那会让 selectModel/
// initializeDefaultModel 静默改错状态。
func catalogModelRows(home string) ([]catalogModelRow, error) {
	view, err := llm.OpenModelCatalog(home).List()
	if err != nil {
		return nil, fmt.Errorf("读模型目录失败: %w", err)
	}
	rows := make([]catalogModelRow, 0, len(view.Models))
	for _, raw := range view.Models {
		var entry struct {
			ID               string                     `json:"id"`
			Name             string                     `json:"name"`
			OwnedBy          string                     `json:"owned_by"`
			ReasoningEfforts map[string]json.RawMessage `json:"reasoningEfforts"`
		}
		if json.Unmarshal(raw, &entry) != nil || strings.TrimSpace(entry.ID) == "" {
			continue
		}
		rows = append(rows, catalogModelRow{
			ID: entry.ID, Name: entry.Name, OwnedBy: entry.OwnedBy,
			ReasoningEfforts: entry.ReasoningEfforts,
		})
	}
	return rows, nil
}

// providerOfModel 返回模型在目录里的真实路由身份（owned_by，缺省 gateway）。
func providerOfModel(rows []catalogModelRow, modelID string) (string, bool) {
	for _, row := range rows {
		if row.ID != modelID {
			continue
		}
		if owned := strings.TrimSpace(row.OwnedBy); owned != "" {
			return owned, true
		}
		return officialGatewayProviderID, true
	}
	return officialGatewayProviderID, false
}

// reasoningLevels 返回模型目录声明里的推理档 id（排除 off，排序固定）。
func reasoningLevels(row catalogModelRow) []string {
	levels := make([]string, 0, len(row.ReasoningEfforts))
	for level := range row.ReasoningEfforts {
		if level == "off" {
			continue
		}
		levels = append(levels, level)
	}
	sort.Strings(levels)
	return levels
}

// ModelSelection 是 transport.OfficialProjectionHost 的实现。
func (h *officialSessionHost) ModelSelection(sessionID string) (lastUsed, next *transport.OfficialModelSelection, err error) {
	if h == nil || strings.TrimSpace(h.home) == "" {
		return nil, nil, nil
	}
	rows, err := catalogModelRows(h.home)
	if err != nil {
		return nil, nil, err
	}
	events, _, err := h.DurableEvents(sessionID)
	if err != nil {
		return nil, nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != "model/selection" {
			continue
		}
		var data struct {
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoningEffort"`
		}
		if json.Unmarshal(ev.Data, &data) != nil || strings.TrimSpace(data.Model) == "" {
			continue
		}
		provider, _ := providerOfModel(rows, data.Model)
		lastUsed = &transport.OfficialModelSelection{
			Provider: provider, Model: data.Model, ReasoningEffort: data.ReasoningEffort,
		}
		break
	}
	// next = 会话级覆盖（`state/session-model.json`）。没有显式选择时官方是 null
	// （初始 options 供路由），不是拿进程默认模型冒充一次"选择"。
	overrides := newModelOverrideStore(h.home)
	if err := overrides.Load(); err != nil {
		return nil, nil, fmt.Errorf("读会话模型覆盖失败: %w", err)
	}
	model := overrides.Get(sessionID)
	if strings.TrimSpace(model) == "" {
		return lastUsed, nil, nil
	}
	provider, _ := providerOfModel(rows, model)
	effort := ""
	record, rerr := h.selections.Get(sessionID)
	if rerr != nil {
		return nil, nil, rerr
	}
	if record.Model == model {
		provider = record.Provider
		effort = record.ReasoningEffort
	} else if es := newEffortOverrideStore(h.home); es.Load() == nil {
		// record 陈旧时 effort 仍可从每轮真实生效的 level 覆盖读（同一个文件口径）。
		effort, _ = es.Selection(sessionID)
	}
	next = &transport.OfficialModelSelection{Provider: provider, Model: model, ReasoningEffort: effort}
	return lastUsed, next, nil
}

// AgentPreset 是 transport.OfficialProjectionHost 的实现。
func (h *officialSessionHost) AgentPreset(sessionID string) (*string, error) {
	if h == nil || strings.TrimSpace(h.home) == "" {
		return nil, nil
	}
	var doc agentPresetDocument
	found, err := agentReadDoc(filepath.Join(h.home, "state", "session-preset.json"), &doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", transport.ErrStatePersistence, err)
	}
	preset := modes.DefaultID
	if found {
		if doc.Version != 1 || doc.Sessions == nil {
			return nil, fmt.Errorf("%w: state/session-preset.json version/sessions 非法",
				transport.ErrStatePersistence)
		}
		if value, ok := doc.Sessions[sessionID]; ok && strings.TrimSpace(value) != "" {
			preset = value
		}
	}
	return &preset, nil
}

// agentChildRecord 是 `subagent/spawn` 帧的只读投影（与 internal/agent.ChildRecord 同形）。
type agentChildRecord struct {
	ChildSessionID  string `json:"childSessionId"`
	ParentSessionID string `json:"parentSessionId"`
	Cwd             string `json:"cwd,omitempty"`
	SpawnedAt       int64  `json:"spawnedAt"`
}

// childRecordOfSession 从一条会话自己的日志里认它是不是某个 parent 的子会话。
//
// 判据与 internal/agent.FindChild 同源（durable `subagent/spawn`），并且要求
// childSessionId 等于**这条会话自己**的 id——fork 继承来的祖先 descriptor 会被
// 排除，不会把 fork 出来的普通会话算进目录。
func childRecordOfSession(src *sessionSource, sessionID string) (agentChildRecord, bool, error) {
	path, err := src.resolve(sessionID)
	if err != nil {
		return agentChildRecord{}, false, err
	}
	lg, err := v4.ReadFile(path)
	if err != nil {
		return agentChildRecord{}, false, err
	}
	for _, ev := range lg.Events {
		if ev.Type != "subagent/spawn" {
			continue
		}
		var rec agentChildRecord
		if json.Unmarshal(ev.Data, &rec) != nil {
			continue
		}
		if rec.ChildSessionID != sessionID || rec.ParentSessionID == "" {
			continue
		}
		return rec, true, nil
	}
	return agentChildRecord{}, false, nil
}

// SubagentCatalog 是 transport.OfficialProjectionHost 的实现。
func (h *officialSessionHost) SubagentCatalog(parentSessionID string) ([]transport.OfficialSubagentCatalogEntry, error) {
	if h == nil || strings.TrimSpace(h.home) == "" {
		return nil, nil
	}
	summaries, err := listSessions(h.home, "")
	if err != nil {
		return nil, fmt.Errorf("列会话失败: %w", err)
	}
	out := make([]transport.OfficialSubagentCatalogEntry, 0, 2)
	for _, summary := range summaries {
		rec, found, cerr := childRecordOfSession(h.src, summary.ID)
		if cerr != nil || !found {
			continue
		}
		if rec.ParentSessionID != parentSessionID {
			continue
		}
		out = append(out, transport.OfficialSubagentCatalogEntry{
			ID:        rec.ChildSessionID,
			CreatedAt: rec.SpawnedAt,
			Mode:      "continuable",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// SessionIDs 是 transport.OfficialSessionControlHost 的实现（全宿主可见会话）。
func (h *officialSessionHost) SessionIDs() ([]string, error) {
	if h == nil || strings.TrimSpace(h.home) == "" {
		return nil, nil
	}
	summaries, err := listSessions(h.home, "")
	if err != nil {
		return nil, fmt.Errorf("列会话失败: %w", err)
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	return ids, nil
}

// ResolveModelSelection 是 transport.OfficialModelSelectionHost 的实现。
//
// 校验口径（官方 requireModel 的等价物）：
//   - model 在目录里 → provider 必须等于该模型的真实路由（owned_by / gateway）；
//   - model 不在目录里 → 只有 gateway 路由能收（进程网关的 legacy 模型 id），
//     其它 provider 一律 400，不能"回显一个不存在的路由"；
//   - reasoningEffort 给了且目录声明了该模型的档位 → 必须是声明里的 key。
func (h *officialSessionHost) ResolveModelSelection(sessionID, provider, model, effort string) (transport.OfficialModelSelection, error) {
	rows, err := catalogModelRows(h.home)
	if err != nil {
		return transport.OfficialModelSelection{}, err
	}
	expected, found := providerOfModel(rows, model)
	if provider != expected {
		return transport.OfficialModelSelection{}, fmt.Errorf(
			"%w: provider %q 与模型 %q 的真实路由 %q 不一致（本宿主 provider 身份 = 目录 owned_by，缺省 %q）",
			transport.ErrOfficialBadInput, provider, model, expected, officialGatewayProviderID)
	}
	if found {
		var declared *catalogModelRow
		for i := range rows {
			if rows[i].ID == model {
				declared = &rows[i]
				break
			}
		}
		if declared != nil && effort != "" {
			levels := reasoningLevels(*declared)
			if len(levels) > 0 {
				known := false
				for _, level := range levels {
					if level == effort {
						known = true
						break
					}
				}
				if !known {
					return transport.OfficialModelSelection{}, fmt.Errorf(
						"%w: 模型 %q 没有 reasoningEffort=%q 的声明（目录只声明了 %s）",
						transport.ErrOfficialBadInput, model, effort, strings.Join(levels, ","))
				}
			}
		}
	}
	if err := h.selections.Set(sessionID, modelSelectionRecord{
		Provider: expected, Model: model, ReasoningEffort: effort,
	}); err != nil {
		return transport.OfficialModelSelection{}, err
	}
	return transport.OfficialModelSelection{Provider: expected, Model: model, ReasoningEffort: effort}, nil
}

// ClearModelSelection 是 transport.OfficialModelSelectionHost 的回滚面。
func (h *officialSessionHost) ClearModelSelection(sessionID string) error {
	return h.selections.Clear(sessionID)
}

// ModelCatalog 是 transport.OfficialModelCatalogHost 的实现。
func (h *officialSessionHost) ModelCatalog() (transport.OfficialModelCatalog, error) {
	rows, err := catalogModelRows(h.home)
	if err != nil {
		return transport.OfficialModelCatalog{}, err
	}
	overrides := newModelOverrideStore(h.home)
	if err := overrides.Load(); err != nil {
		return transport.OfficialModelCatalog{}, fmt.Errorf("读默认模型失败: %w", err)
	}
	defaultModel := overrides.Get(settingsDefaultKey)
	if strings.TrimSpace(defaultModel) == "" && len(rows) > 0 {
		defaultModel = rows[0].ID
	}
	if strings.TrimSpace(defaultModel) == "" {
		return transport.OfficialModelCatalog{}, fmt.Errorf(
			"%w: 模型目录为空且没有默认模型", transport.ErrOfficialBadInput)
	}
	defaultProvider, _ := providerOfModel(rows, defaultModel)

	groups := make([]transport.OfficialModelProvider, 0, 4)
	groupIndex := map[string]int{}
	routable := map[string]bool{}
	failures := make([]transport.OfficialModelFailure, 0)
	failureSeen := map[string]bool{}
	for _, row := range rows {
		provider, _ := providerOfModel(rows, row.ID)
		index, ok := groupIndex[provider]
		if !ok {
			index = len(groups)
			groupIndex[provider] = index
			groups = append(groups, transport.OfficialModelProvider{
				ID: provider, Name: provider, Models: []transport.OfficialModelEntry{},
			})
		}
		name := row.Name
		if strings.TrimSpace(name) == "" {
			name = row.ID
		}
		entry := transport.OfficialModelEntry{ID: row.ID, Name: name}
		if levels := reasoningLevels(row); len(levels) > 0 {
			efforts := make([]transport.OfficialReasoningEffort, 0, len(levels))
			for _, level := range levels {
				efforts = append(efforts, transport.OfficialReasoningEffort{ID: level, Name: level})
			}
			entry.Reasoning = &transport.OfficialReasoning{Efforts: efforts}
		}
		groups[index].Models = append(groups[index].Models, entry)
		// 真实可路由性：目录 provider 配置解析成功（或没有 provider 声明 = 走进程
		// 网关）都算可路由；解析失败（凭据缺失/坏配置）记 provider 级 failure。
		if _, _, rerr := resolveServeProvider(h.home, row.ID); rerr != nil {
			if !failureSeen[provider] {
				failureSeen[provider] = true
				failures = append(failures, transport.OfficialModelFailure{
					ID: provider, Name: provider, Message: redactSelectionError(rerr),
				})
			}
			continue
		}
		routable[provider] = true
	}
	routableProviders := make([]string, 0, len(routable))
	for _, group := range groups {
		if routable[group.ID] {
			routableProviders = append(routableProviders, group.ID)
		}
	}
	return transport.OfficialModelCatalog{
		Default:           transport.OfficialModelSelection{Provider: defaultProvider, Model: defaultModel},
		RoutableProviders: routableProviders,
		Groups:            groups,
		Failures:          failures,
	}, nil
}

// redactSelectionError 去掉错误里可能出现的凭据值（目录错误只应出现 ref 名）。
func redactSelectionError(err error) string {
	return llm.RedactCredential(err.Error(), os.Getenv("DSHD_LLM_KEY"))
}

// InitializeDefaultModel 是 transport.OfficialModelCatalogHost 的实现。
//
// 官方语义：provider API key 已配置 → 不动；否则把第一个可用账号模型存成默认。
// 本宿主没有 deepseek-account 登录 provider，等价物是：
//   - `DSHD_LLM_KEY` 已配置 → no-op（返回 changed=false）；
//   - 已有默认模型且能解析 → no-op；
//   - 否则取目录里第一条模型写成 settings.defaultModel（真实写盘）。
func (h *officialSessionHost) InitializeDefaultModel() (bool, error) {
	if h.settings == nil {
		return false, fmt.Errorf(
			"%w: session.initializeDefaultModel 需要 serve.go 把 settingsStore 作为 "+
				"withOfficialSessionOptions 的第 4 个参数传入（写默认模型必须走那一份共享 store；"+
				"见 tmp/GO2-TEAM/T04/WIRING.md）", transport.ErrNotImplemented)
	}
	if strings.TrimSpace(os.Getenv("DSHD_LLM_KEY")) != "" {
		return false, nil
	}
	doc, err := h.settings.Read(settingsform.NamespaceModel)
	if err != nil {
		return false, err
	}
	current := effectiveSettingsValue(doc, settingsform.FieldDefaultModel)
	if strings.TrimSpace(current) != "" {
		if _, _, rerr := resolveServeProvider(h.home, current); rerr == nil {
			return false, nil
		}
	}
	rows, err := catalogModelRows(h.home)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, fmt.Errorf(
			"%w: 模型目录为空，没有可用的账号模型（官方 session/provider-models-unavailable 同义）",
			transport.ErrOfficialBadInput)
	}
	if _, werr := h.settings.Write(settingsform.NamespaceModel,
		map[string]any{settingsform.FieldDefaultModel: rows[0].ID}, nil); werr != nil {
		return false, werr
	}
	return true, nil
}
