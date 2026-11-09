// Package recommendations 是官方 `pluginRecommendations.describe` 的真实实现。
//
// 官方语义（@deepseek-ai/dsh-plugin-recommendations）：
//   - 推荐卡只来自 actionCards 中 action=enable_official_plugin 的 proposal；
//   - describe(agentId, callId) 从**当前插件的 catalog** 读 title/description/
//     enabled，未知卡 → refused{unknown}，无目录/无已安装包 → refused{unavailable}，
//     非 live root → refused{not-live-root}；
//   - 该 kind 的 Enable 动词通过 pluginManager 真正打开已安装包，decision 记录
//     enabled / already-enabled / failed / unconfirmed。
//
// 本包不编推荐：目录来自 pluginMarket 索引（真实 HTTP），安装/启用状态来自
// pluginManager 的真实 state.json。没有索引源就拒绝，不返回静态列表。
package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/actioncards"
	"dshgo/internal/market"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/session"
)

// Action 是官方推荐卡的 action 名。
const Action = "enable_official_plugin"

// Skill 是官方加载的 skill 名（propose_action 的 kind skill）。
const Skill = "official-plugins"

// Invitation 是官方卡片的邀请文案（模型只在卡片存在时邀请）。
const Invitation = "End with at most one short invitation in the user's language to click Enable below; do not say the plugin is already on."

// BundleManager 是本包依赖的真实 pluginManager 子集。
type BundleManager interface {
	Bundles() ([]pluginmgr.BundleState, error)
	Bundle(name string) (pluginmgr.BundleState, bool, error)
	Enable(name string) (pluginmgr.PluginInfo, error)
}

// Service 是 pluginRecommendations 的宿主实现。
type Service struct {
	home    string
	market  *market.Store
	bundles BundleManager
	cards   *actioncards.Service

	mu       sync.Mutex
	closing  bool
	disposed bool
	dispose  func()
}

// New 构造服务并注册官方 kind。kind 注册失败（例如同名 action 已注册）返回错误，
// 调用方不接线，describe 保持 501。
func New(home string, bundles BundleManager, marketStore *market.Store, cards *actioncards.Service) (*Service, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil, errors.New("recommendations: home 为空")
	}
	if bundles == nil {
		return nil, errors.New("recommendations: 缺 pluginManager")
	}
	if marketStore == nil {
		marketStore = market.Open(home)
	}
	if cards == nil {
		cards = actioncards.Open(home)
	}
	service := &Service{home: home, market: marketStore, bundles: bundles, cards: cards}
	dispose, err := cards.Register(actioncards.Kind{
		Action:     Action,
		Skill:      Skill,
		Invitation: Invitation,
		Details:    validateDetails,
		Verbs:      map[string]actioncards.VerbPolicy{"enable": {Global: true}},
		OpenPhases: []string{"failed", "unconfirmed"},
		Reconcile:  service.reconcile,
		Act:        service.enable,
	})
	if err != nil {
		return nil, fmt.Errorf("recommendations: 注册 %s kind 失败: %w", Action, err)
	}
	service.dispose = dispose
	return service, nil
}

func validateDetails(raw json.RawMessage) error {
	var details struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &details); err != nil {
		return fmt.Errorf("details 必须是 {name:string}: %w", err)
	}
	if strings.TrimSpace(details.Name) == "" {
		return errors.New("details.name 不能为空")
	}
	return nil
}

func detailsName(details map[string]any) string {
	raw, ok := details["name"]
	if !ok {
		return ""
	}
	name, _ := raw.(string)
	return strings.TrimSpace(name)
}

// Describe 是官方 describe 的真实实现。返回 map 以保持官方 union 的判别字段
// （status 为 discriminated key；available 带 title/description/enabled）。
func (s *Service) Describe(ctx context.Context, agentID, callID string) (map[string]any, error) {
	if s == nil {
		return refused("unavailable"), nil
	}
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return refused("closing"), nil
	}
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(callID) == "" {
		return refused("unknown"), nil
	}
	path, err := session.FindByID(s.home, agentID)
	if err != nil {
		return refused("not-live-root"), nil
	}
	log, err := v4.ReadFile(path)
	if err != nil || log.Header.ParentSession != "" {
		return refused("not-live-root"), nil
	}
	view, err := s.cards.View(agentID)
	if err != nil {
		return refused("unavailable"), nil
	}
	proposal, ok := view.Proposals[callID]
	if !ok || proposal.Action != Action {
		return refused("unknown"), nil
	}
	name := detailsName(proposal.Details)
	if name == "" {
		return refused("unknown"), nil
	}
	catalog, err := s.catalog(ctx)
	if err != nil {
		return refused("unavailable"), nil
	}
	plugin, ok := catalog[name]
	if !ok {
		return refused("unavailable"), nil
	}
	bundle, ok, err := s.bundles.Bundle(name)
	if err != nil || !ok {
		return refused("unavailable"), nil
	}
	if !eligible(plugin, bundle) {
		return refused("unavailable"), nil
	}
	return map[string]any{
		"status":      "available",
		"title":       localized(plugin.Title, "en", plugin.Name),
		"description": localized(plugin.Description, "en", ""),
		"enabled":     bundle.Enabled,
	}, nil
}

func refused(reason string) map[string]any {
	return map[string]any{"status": "refused", "reason": reason}
}

// catalog 从真实 pluginMarket 索引取所有条目，按 name 去重（先到先得）。
func (s *Service) catalog(ctx context.Context) (map[string]market.Plugin, error) {
	urls := s.market.ListURLs()
	if len(urls) == 0 {
		return nil, market.ErrNoIndexes
	}
	out := map[string]market.Plugin{}
	var firstErr error
	success := false
	for _, rawURL := range urls {
		result, err := s.market.Search(ctx, rawURL, market.Query{Limit: 100})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		success = true
		for _, plugin := range result.Plugins {
			if _, exists := out[plugin.Name]; !exists {
				out[plugin.Name] = plugin
			}
		}
	}
	if !success {
		return nil, firstErr
	}
	return out, nil
}

func eligible(plugin market.Plugin, bundle pluginmgr.BundleState) bool {
	if strings.TrimSpace(plugin.Name) == "" || plugin.Name != bundle.Name {
		return false
	}
	if !hasLabel(plugin.Labels, "official") && !strings.HasPrefix(plugin.Name, "@deepseek-ai/") {
		return false
	}
	available := false
	for _, version := range plugin.Versions {
		if strings.TrimSpace(version.Version) == "" {
			continue
		}
		if strings.TrimSpace(version.Deprecated) == "" {
			available = true
			break
		}
	}
	return available
}

func hasLabel(labels []string, want string) bool {
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label), want) {
			return true
		}
	}
	return false
}

func localized(values map[string]string, language, fallback string) string {
	if values != nil {
		if value := strings.TrimSpace(values[language]); value != "" {
			return value
		}
		if value := strings.TrimSpace(values["en"]); value != "" {
			return value
		}
	}
	return fallback
}

// reconcile 在 unconfirmed 卡动作前重读真实启用状态；若插件已被别处打开，
// 记录 enabled，官方不再重复启用。
func (s *Service) reconcile(proposal actioncards.Proposal, _ actioncards.Decision) (*actioncards.Decision, error) {
	name := detailsName(proposal.Details)
	if name == "" {
		return nil, nil
	}
	bundle, ok, err := s.bundles.Bundle(name)
	if err != nil {
		return nil, err
	}
	if ok && bundle.Enabled {
		return &actioncards.Decision{CallID: proposal.CallID, Action: proposal.Action, Phase: "enabled"}, nil
	}
	return nil, nil
}

func managerErrorCode(err error) string {
	var apiErr *pluginmgr.Error
	if errors.As(err, &apiErr) && strings.TrimSpace(apiErr.Code) != "" {
		return apiErr.Code
	}
	return "operation-error"
}

// enable 是官方 Enable 动词：真实调用 pluginManager.Enable，不假成功。
func (s *Service) enable(ctx context.Context, proposal actioncards.Proposal, verb string, _ map[string]json.RawMessage) (actioncards.KindResult, error) {
	if verb != "enable" {
		return actioncards.Refused("unavailable"), nil
	}
	name := detailsName(proposal.Details)
	if name == "" {
		return actioncards.Refused("unavailable"), nil
	}
	bundle, ok, err := s.bundles.Bundle(name)
	if err != nil {
		return actioncards.Settled(actioncards.Decision{
			CallID: proposal.CallID, Action: proposal.Action, Phase: "failed",
			Data: map[string]any{"errorCode": managerErrorCode(err)},
		}), nil
	}
	if !ok {
		return actioncards.Refused("unavailable"), nil
	}
	if bundle.Enabled {
		return actioncards.Settled(actioncards.Decision{
			CallID: proposal.CallID, Action: proposal.Action, Phase: "already-enabled",
		}), nil
	}
	if err := ctx.Err(); err != nil {
		return actioncards.Settled(actioncards.Decision{
			CallID: proposal.CallID, Action: proposal.Action, Phase: "unconfirmed",
		}), nil
	}
	info, err := s.bundles.Enable(name)
	if err != nil {
		return actioncards.Settled(actioncards.Decision{
			CallID: proposal.CallID, Action: proposal.Action, Phase: "failed",
			Data: map[string]any{"errorCode": managerErrorCode(err)},
		}), nil
	}
	if !info.Enabled {
		return actioncards.Settled(actioncards.Decision{
			CallID: proposal.CallID, Action: proposal.Action, Phase: "unconfirmed",
		}), nil
	}
	return actioncards.Settled(actioncards.Decision{
		CallID: proposal.CallID, Action: proposal.Action, Phase: "enabled",
	}), nil
}

// EnsureSkill 把当前真实 catalog 写成 `<home>/skills/official-plugins/SKILL.md`。
// skill 工具只读文件；没有索引源时仍写 policy，但明确说 catalog 不可用，凭空
// 编造推荐名不会成功。
func (s *Service) EnsureSkill(ctx context.Context) error {
	if s == nil {
		return errors.New("recommendations: nil service")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	catalog, catalogErr := s.catalog(ctx)
	var b strings.Builder
	b.WriteString("# Official plugin recommendations\n\n")
	b.WriteString("Use this skill when the user asks whether DSH can do something, asks how to do it, or says a capability is missing, and an official optional plugin would satisfy the need. ")
	b.WriteString("Answer first; then, if an eligible plugin matches, propose exactly one `propose_action` card with action `enable_official_plugin`, the plugin's catalog title, a one-sentence brief in the user's language, and details `{\"name\":\"<plugin name>\"}`. ")
	b.WriteString("An enabled match gets usage guidance without a card. Never invent plugin names; enabling authorizes nothing beyond that plugin.\n\n")
	b.WriteString("## Catalog\n\n")
	if catalogErr != nil {
		b.WriteString("The plugin-market catalog is unavailable right now; do not recommend a plugin. Reason: ")
		b.WriteString(catalogErr.Error())
		b.WriteString("\n")
	} else if len(catalog) == 0 {
		b.WriteString("The plugin-market catalog has no eligible official plugins.\n")
	} else {
		b.WriteString("| name | title | description | enabled |\n|---|---|---|---|\n")
		for name, plugin := range catalog {
			bundle, ok, _ := s.bundles.Bundle(name)
			if !ok || !eligible(plugin, bundle) {
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %t |\n",
				escapeCell(name), escapeCell(localized(plugin.Title, "en", name)),
				escapeCell(localized(plugin.Description, "en", "")), bundle.Enabled)
		}
	}
	path := filepath.Join(s.home, "skills", Skill, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func escapeCell(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}

// Close 停用服务并摘掉 kind（测试/关停用）。
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return
	}
	s.closing = true
	s.disposed = true
	dispose := s.dispose
	s.mu.Unlock()
	if dispose != nil {
		dispose()
	}
}
