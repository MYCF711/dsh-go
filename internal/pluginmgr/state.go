package pluginmgr

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Manager is safe for concurrent callers in one process. Cross-process writers
// are deliberately outside this minimal slice; transactions make crashes loud.
type Manager struct {
	mu         sync.Mutex
	root       string
	state      *persistentState
	afterPhase func(string) error

	// jobs 是**在飞**安装作业的登记表（官方 requestId 语义）。
	// 它用独立的锁：安装作业持着 mu 干活时，wait/cancel 仍然必须能被观察到，
	// 否则"取消一次安装"会先卡在自己的互斥量上。
	jobsMu sync.Mutex
	jobs   map[string]*InstallJob

	// compatMu 守卫 compatibility.json（版本豁免）的读-改-写。它与 mu 分开：
	// 安装闸门在持 mu 时读豁免，而 setVersionExemption 只碰文件，不需要等安装。
	compatMu sync.Mutex

	// runtime 是**可选**的运行时装载器（见 loader.go）。它由 NewLoader 挂上，
	// 之后每次状态变更都会同步装载集合：enable 先装载再提交、disable/uninstall
	// 提交后卸载。没有挂载器时（纯存储用法）行为与原来完全一致。
	//
	// 为什么把它做成管理器的钩子而不是让调用方自己记得调：状态与运行时一旦能
	// 各走各的，就会出现"enabled=true 但没跑起来"和"disabled 但还能调"两种
	// 静默不一致，而这两种都只能靠人记得去对账。
	runtime *Loader
}

// Open loads only the explicitly supplied root and reconciles interrupted work.
// It never consults a user-home default, environment variable or remote source.
func Open(root string) (*Manager, error) {
	if strings.TrimSpace(root) == "" {
		return nil, badRequest("管理器 root 不能为空")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, internalError(err)
	}
	for _, dir := range []string{absolute, filepath.Join(absolute, "packages"), filepath.Join(absolute, "transactions")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, internalError(fmt.Errorf("创建管理器目录 %s: %w", dir, err))
		}
	}
	m := &Manager{root: absolute, jobs: map[string]*InstallJob{}}
	m.mu.Lock()
	err = m.loadStateLocked()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if _, err := m.Reconcile(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validateCurrentLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) loadStateLocked() error {
	raw, err := os.ReadFile(filepath.Join(m.root, "state.json"))
	if os.IsNotExist(err) {
		m.state = emptyState()
		return nil
	}
	if err != nil {
		return internalError(fmt.Errorf("读取插件状态失败: %w", err))
	}
	state := emptyState()
	if err := decodeStrict(raw, state); err != nil {
		return errorf("state_corrupt", 422, false,
			"修复 state.json；不要删除事务目录，先调用 plugin.reconcile 检查", "插件状态损坏: %v", err)
	}
	if state.Schema != stateSchema || state.Plugins == nil {
		return errorf("state_corrupt", 422, false, "恢复备份或修复 state.json", "未知/空插件状态 schema %q", state.Schema)
	}
	for id, record := range state.Plugins {
		if err := validateRecord(id, record); err != nil {
			return errorf("state_corrupt", 422, false, "修复 state.json 后重启 reconcile", "%v", err)
		}
	}
	m.state = state
	return nil
}

func validateRecord(id string, record *pluginRecord) error {
	if record == nil || id == "" || record.ID != id {
		return fmt.Errorf("插件记录 id 不一致: map=%q record=%+v", id, record)
	}
	if err := validateID(id); err != nil {
		return err
	}
	if _, err := parseSemver(record.Version); err != nil {
		return fmt.Errorf("插件 %s 当前版本非法: %w", id, err)
	}
	if len(record.Versions) == 0 {
		return fmt.Errorf("插件 %s 没有版本来源", id)
	}
	for version, source := range record.Versions {
		if _, err := parseSemver(version); err != nil || source.Source == "" {
			return fmt.Errorf("插件 %s 版本来源非法: %q %+v", id, version, source)
		}
	}
	if _, ok := record.Versions[record.Version]; !ok {
		return fmt.Errorf("插件 %s 当前版本 %s 没有来源", id, record.Version)
	}
	if record.PreviousVersion != "" {
		if _, ok := record.Versions[record.PreviousVersion]; !ok {
			return fmt.Errorf("插件 %s previousVersion %s 不在保留版本里", id, record.PreviousVersion)
		}
	}
	return nil
}

func (m *Manager) saveStateLocked(next *persistentState) error {
	if err := validateRecordSet(next); err != nil {
		return internalError(err)
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return internalError(err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(m.root, ".state-*.tmp")
	if err != nil {
		return internalError(fmt.Errorf("创建状态临时文件失败: %w", err))
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return internalError(fmt.Errorf("写入插件状态失败: %w", err))
	}
	if err := os.Rename(tmpName, filepath.Join(m.root, "state.json")); err != nil {
		return internalError(fmt.Errorf("原子替换插件状态失败: %w", err))
	}
	return nil
}

func validateRecordSet(state *persistentState) error {
	if state == nil || state.Schema != stateSchema || state.Plugins == nil {
		return fmt.Errorf("拒绝写入空/未知状态")
	}
	for id, record := range state.Plugins {
		if err := validateRecord(id, record); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) validateCurrentLocked() error {
	for id, record := range m.state.Plugins {
		if _, err := m.recordInfoLocked(id, record); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) recordInfoLocked(id string, record *pluginRecord) (PluginInfo, error) {
	info := PluginInfo{}
	manifest, err := readAndValidatePackage(m.packagePath(id, record.Version))
	if err != nil {
		return info, errorf("package_corrupt", 422, false,
			"保留现场并检查 packages 与 transactions；不要静默删除插件", "已安装插件 %s@%s 校验失败: %v", id, record.Version, err)
	}
	if manifest.ID != id || manifest.Version != record.Version {
		return info, errorf("package_corrupt", 422, false, "修复包目录身份后重试", "已安装包身份不符：state=%s@%s manifest=%s@%s", id, record.Version, manifest.ID, manifest.Version)
	}
	return PluginInfo{
		ID: id, Version: record.Version, Kind: manifest.Kind, Entry: manifest.Entry,
		Enabled: record.Enabled, Source: record.Versions[record.Version].Source,
		PreviousVersion: record.PreviousVersion,
	}, nil
}

func (m *Manager) packagePath(id, version string) string {
	return filepath.Join(m.root, "packages", id, version)
}

func (m *Manager) pluginInfoLocked(id string) (PluginInfo, error) {
	record := m.state.Plugins[id]
	if record == nil {
		return PluginInfo{}, notFound(id)
	}
	return m.recordInfoLocked(id, record)
}

// ToolsMounted 报告插件包的工具当前是否挂进模型工具表。
//
// 三级来源，前一级赢：
//  1. 环境变量 `DSHD_PLUGIN_TOOLS_MOUNTED`（部署期下线开关，见 official_config.go）；
//  2. `state.json` 里的显式开关（`SetToolsMounted` 落盘）；
//  3. 都没有 → **挂载**（= 宿主既有行为；不设即零影响）。
//
// 为什么第 3 级是挂载而不是官方 `tool-plugin-manager` 行的 `disabled: true`：
// 官方那一行禁掉的是**插件管理器自己的管理工具**（模型侧的 plugin_manager），
// 宿主根本没有这个工具（`grep -rn plugin_manager --include=*.go` 在工具注册面 0 命中），
// 该语义在宿主侧已经等价成立。本开关管的是"插件**包**提供的工具"，那些包存在的
// 唯一目的就是提供能力；默认不挂等于"装了等于没装"，违反"能力不得比官方更少"。
func (m *Manager) ToolsMounted() bool { return m.ToolsMountedWith(os.Getenv) }

// ToolsMountedWith 是 ToolsMounted 的可注入环境版（测试用）。
func (m *Manager) ToolsMountedWith(getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(getenv(EnvToolsMounted))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil || m.state.ToolsMounted == nil {
		return true
	}
	return *m.state.ToolsMounted
}

// SetToolsMounted 显式落盘"插件工具是否挂进模型工具表"。
//
// 只改这一个位，不动任何 enabled/装载状态：关掉挂载**不是**卸载插件——插件仍然
// 装载着（句柄、实例、线性内存都在），只是模型看不到它的工具；打开就立刻回来，
// 不需要重新安装或重启。
func (m *Manager) SetToolsMounted(mounted bool) error {
	m.mu.Lock()
	next := cloneState(m.state)
	value := mounted
	next.ToolsMounted = &value
	if err := m.saveStateLocked(next); err != nil {
		m.mu.Unlock()
		return err
	}
	m.state = next
	m.mu.Unlock()
	return nil
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func sortedPluginIDs(state *persistentState) []string {
	ids := make([]string, 0, len(state.Plugins))
	for id := range state.Plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
