package officekit

// 引擎发现与校验。
//
// 官方只有一条路：Node 的 `require.resolve('@deepseek-ai/libreoffice-kit-win32-x64/package.json')`
// 找到已安装包，再读 prebuilds.json 校验（官方 cli.js:1656-1700 readEngine）。
// 我们的宿主不是 Node，所以把「解析」换成两级：
//
//  1. 显式配置（设置项 EnginePath / 环境变量 DSHD_OFFICE_KIT_ENGINE）——最高优先；
//  2. 自动发现：在已知的应用解包目录里找含 `libreoffice-kit-win32-x64/prebuilds.json`
//     的 node_modules（官方 Electron 的 `resources/app.asar.unpacked/dsh/node_modules`
//     就是这一类，官方 locate.ts:86-93 也把 .asar 映射成 .asar.unpacked）。
//
// 两条都失败时**不降级、不假装**：返回 nil 引擎，调用方给出结构化 unavailable。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EngineEnvVar 是显式指定引擎目录的环境变量（指向包根、或包根里的 bin/dsoffice.exe、
// 或 node_modules/@deepseek-ai 都认）。
const EngineEnvVar = "DSHD_OFFICE_KIT_ENGINE"

// EngineRootsEnvVar 是额外的自动发现搜索根（多个用 os.PathListSeparator 分隔）。
const EngineRootsEnvVar = "DSHD_OFFICE_KIT_ROOTS"

// enginePackageDir 是官方引擎包目录名（prebuilds.json 的兄弟目录名固定）。
const enginePackageDir = "libreoffice-kit-win32-x64"

// Engine 是已验证的原生引擎安装。
//
// Root/Executable/ProgramDirectory 都是绝对路径；Verified 为 true 表示
// prebuilds.json 的结构、平台、资产三项检查都过了（不是「目录存在」就算数）。
type Engine struct {
	Root             string `json:"root"`
	Executable       string `json:"executable"`
	ProgramDirectory string `json:"programDirectory"`
	Version          string `json:"version"`
	Platform         string `json:"platform"`
	// Source 记录它是被谁发现的，便于报告与排障：explicit / env / auto:<根>。
	Source string `json:"source"`
}

// prebuilds 是官方 prebuilds.json 里我们用到的字段（官方 cli.js:1670-1690 readEngine）。
type prebuilds struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"`
	Platform      string `json:"platform"`
	Status        string `json:"status"`
	Engine        struct {
		Kind             string `json:"kind"`
		Executable       string `json:"executable"`
		ProgramDirectory string `json:"programDirectory"`
	} `json:"engine"`
}

// TargetPlatform 返回官方包名里的平台串（win32-x64 等）。
func TargetPlatform() string {
	switch runtime.GOOS {
	case "windows":
		if runtime.GOARCH == "arm64" {
			return "win32-arm64"
		}
		return "win32-x64"
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "darwin-arm64"
		}
		return "darwin-x64"
	default:
		if runtime.GOARCH == "arm64" {
			return "linux-arm64-glibc"
		}
		return "linux-x64-glibc"
	}
}

// DiscoverOptions 是引擎发现的两处输入。
type DiscoverOptions struct {
	// Explicit 是设置项给的路径（包根 / 可执行文件 / 其父目录均可），最高优先。
	Explicit string
	// SearchRoots 覆盖默认自动发现根，主要给测试注入；为空时用默认根。
	SearchRoots []string
}

// Discover 按「显式配置 → 自动发现」找引擎。找不到返回 (nil, 试过的路径列表)。
//
// 自动发现带**时间预算**（Windows 上遍历系统/临时目录可能卡几十秒），
// 并且不是"第一个能用就用"：同一台机器常并存多个安装（例如
// Desktop-0.2.2-rc.1 / Desktop-0.2.2-rc.2 / Desktop），各自带的引擎版本不同。
// 选择规则：
//  1. 显式配置（设置项 / 环境变量）——无条件优先；
//  2. 与**本进程可执行文件同属一个应用目录**的引擎（官方就是"用当前宿主自带的
//     那份 node_modules"，语义上最接近）；
//  3. 否则取**版本最高**的合法引擎（0.2.3 优于 0.1.5），避免挑到旧引擎后
//     悄悄少能力。
func Discover(opts DiscoverOptions) (*Engine, []string) {
	var tried []string
	for _, explicit := range []struct {
		path   string
		source string
	}{
		{strings.TrimSpace(opts.Explicit), "explicit"},
		{strings.TrimSpace(os.Getenv(EngineEnvVar)), "env:" + EngineEnvVar},
	} {
		if explicit.path == "" {
			continue
		}
		tried = append(tried, explicit.source+":"+explicit.path)
		if engine, err := resolveCandidate(explicit.path, explicit.source); err == nil {
			return engine, tried
		}
	}
	roots := opts.SearchRoots
	if len(roots) == 0 {
		roots = defaultSearchRoots()
	}
	deadline := time.Now().Add(discoveryBudget)
	var found []*Engine
	for _, root := range roots {
		if time.Now().After(deadline) {
			tried = append(tried, "timeout:发现预算 "+discoveryBudget.String()+" 用尽")
			break
		}
		tried = append(tried, "auto:"+root)
		if engine, err := resolveCandidate(root, "auto:"+root); err == nil {
			found = append(found, engine)
		}
	}
	if len(found) == 0 {
		return nil, tried
	}
	return chooseEngine(found), tried
}

// discoveryBudget 是自动发现的总时间预算。
const discoveryBudget = 3 * time.Second

// chooseEngine 在同一台机器上发现的多个引擎里选一个（规则见 Discover 注释）。
func chooseEngine(found []*Engine) *Engine {
	if len(found) == 1 {
		return found[0]
	}
	if appRoot := ownAppRoot(); appRoot != "" {
		var best *Engine
		bestDepth := 0
		for _, engine := range found {
			lowerRoot := strings.ToLower(filepath.Clean(engine.Root))
			lowerApp := strings.ToLower(filepath.Clean(appRoot))
			if lowerRoot != lowerApp && !strings.HasPrefix(lowerRoot, lowerApp+string(filepath.Separator)) {
				continue
			}
			if depth := len(strings.Split(lowerApp, string(filepath.Separator))); depth > bestDepth {
				best, bestDepth = engine, depth
			}
		}
		if best != nil {
			return best
		}
	}
	best := found[0]
	for _, engine := range found[1:] {
		if compareVersions(engine.Version, best.Version) > 0 {
			best = engine
		}
	}
	return best
}

// ownAppRoot 返回本进程可执行文件所在的应用目录（用于优先选同安装自带的引擎）。
func ownAppRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for depth := 0; dir != "" && depth < 6; depth++ {
		if strings.Contains(strings.ToLower(dir), "resources") {
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// compareVersions 比较 "0.2.3" 这类版本串；解析不了的一律视为最小。
func compareVersions(left, right string) int {
	leftParts, leftOK := versionParts(left)
	rightParts, rightOK := versionParts(right)
	if !leftOK || !rightOK {
		if leftOK {
			return 1
		}
		if rightOK {
			return -1
		}
		return 0
	}
	for i := 0; i < len(leftParts) || i < len(rightParts); i++ {
		var a, b int
		if i < len(leftParts) {
			a = leftParts[i]
		}
		if i < len(rightParts) {
			b = rightParts[i]
		}
		if a != b {
			if a > b {
				return 1
			}
			return -1
		}
	}
	return 0
}

func versionParts(version string) ([]int, bool) {
	text := strings.TrimSpace(strings.TrimPrefix(version, "v"))
	if text == "" {
		return nil, false
	}
	fields := strings.Split(text, ".")
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		digits := 0
		for digits < len(field) && field[digits] >= '0' && field[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			return nil, false
		}
		number, err := strconv.Atoi(field[:digits])
		if err != nil {
			return nil, false
		}
		parts = append(parts, number)
	}
	return parts, true
}

// resolveCandidate 把「用户/发现器给的一个路径」解释成引擎包根，并校验它。
//
// 接受四形态：包根（含 prebuilds.json）、node_modules 根、@deepseek-ai 目录、
// bin/dsoffice.exe 本身。这样显式配置不必要求用户记住到底该指哪一层。
func resolveCandidate(path, source string) (*Engine, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	for _, root := range candidateRoots(abs) {
		engine, err := loadEngine(root, source)
		if err == nil {
			return engine, nil
		}
	}
	return nil, fmt.Errorf("不是可用的 LibreOfficeKit 引擎：%s", abs)
}

func candidateRoots(abs string) []string {
	info, statErr := os.Stat(abs)
	isDir := statErr == nil && info.IsDir()
	var roots []string
	add := func(p string) {
		for _, exist := range roots {
			if strings.EqualFold(exist, p) {
				return
			}
		}
		roots = append(roots, p)
	}
	add(abs)
	if !isDir {
		return roots
	}
	add(filepath.Join(abs, "@deepseek-ai", enginePackageDir))
	add(filepath.Join(abs, enginePackageDir))
	add(filepath.Join(abs, "node_modules", "@deepseek-ai", enginePackageDir))
	// bin 目录：<root>/bin/dsoffice.exe → 上两级才是包根
	if strings.EqualFold(filepath.Base(abs), "bin") {
		add(filepath.Dir(abs))
	}
	return roots
}

// loadEngine 校验一个包根：prebuilds.json 存在且结构/平台/资产三项都对。
//
// 与官方 readEngine 的差异只有一条：官方把包版本钉死在 ENGINE_VERSIONS[target]
// （0.2.3），我们**不钉版本**而是把版本记下来——本仓允许用户升级引擎包，
// 但结构不合法一定拒（不合法 = 不假渲染）。
func loadEngine(root, source string) (*Engine, error) {
	manifestPath := filepath.Join(root, "prebuilds.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest prebuilds
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("prebuilds.json 不是合法 JSON：%w", err)
	}
	if manifest.SchemaVersion != 1 {
		return nil, fmt.Errorf("prebuilds.json schemaVersion=%d，只支持 1", manifest.SchemaVersion)
	}
	if manifest.Status != "built" {
		return nil, fmt.Errorf("prebuilds.json status=%q，只接受 built", manifest.Status)
	}
	if want := TargetPlatform(); !strings.EqualFold(manifest.Platform, want) {
		return nil, fmt.Errorf("prebuilds.json platform=%q，当前平台是 %q", manifest.Platform, want)
	}
	if manifest.Engine.Kind != "native" {
		return nil, fmt.Errorf("prebuilds.json engine.kind=%q，本实现只支持 native（wasm 后端不在范围）", manifest.Engine.Kind)
	}
	executable, err := engineAsset(root, manifest.Engine.Executable, false)
	if err != nil {
		return nil, err
	}
	programDirectory, err := engineAsset(root, manifest.Engine.ProgramDirectory, true)
	if err != nil {
		return nil, err
	}
	return &Engine{
		Root: root, Executable: executable, ProgramDirectory: programDirectory,
		Version: manifest.Version, Platform: manifest.Platform, Source: source,
	}, nil
}

// engineAsset 把 prebuilds.json 里的相对路径变成绝对路径并确认类型
// （官方 cli.js:1662-1668 engineAsset：越界/类型不对都拒）。
func engineAsset(root, value string, wantDir bool) (string, error) {
	if value == "" || filepath.IsAbs(value) {
		return "", fmt.Errorf("prebuilds.json 里的资产路径必须是非空相对路径：%q", value)
	}
	joined := filepath.Join(root, filepath.FromSlash(value))
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("prebuilds.json 资产路径越出包根：%q", value)
	}
	info, err := os.Stat(joined)
	if err != nil {
		return "", fmt.Errorf("引擎资产缺失 %s：%w", value, err)
	}
	if wantDir != info.IsDir() {
		kind := "文件"
		if wantDir {
			kind = "目录"
		}
		return "", fmt.Errorf("引擎资产 %s 必须是%s", value, kind)
	}
	return joined, nil
}

// defaultSearchRoots 返回自动发现的候选根（去重保序）。
//
// 设计约束（被真机卡死过）：**不遍历系统临时目录**（go-build 目录成千上万，Windows
// 下 Glob 会被实时扫描拖到几十秒），也**不从临时目录里的可执行文件往上找**。
// 只扫三类位置：
//  1. 环境变量列出的根（DSHD_OFFICE_KIT_ROOTS）；
//  2. 平台已知安装位置（%LOCALAPPDATA%\Programs\*、%PROGRAMFILES%、DeepSeek Harness）；
//  3. 工作目录的祖先目录（≤4 层）与其**同级应用目录**（官方 Electron 解包结构就藏在这一层）。
func defaultSearchRoots() []string {
	var roots []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if isSystemTempPath(p) {
			return
		}
		if seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		roots = append(roots, p)
	}
	for _, extra := range strings.Split(os.Getenv(EngineRootsEnvVar), string(os.PathListSeparator)) {
		if extra = strings.TrimSpace(extra); extra != "" {
			add(extra)
		}
	}
	for _, dir := range []string{os.Getenv("LOCALAPPDATA"), os.Getenv("PROGRAMFILES"), os.Getenv("ProgramW6432")} {
		if dir == "" {
			continue
		}
		add(filepath.Join(dir, "DeepSeek Harness", "resources", "app.asar.unpacked", "dsh", "node_modules"))
		add(filepath.Join(dir, "DeepSeek Harness", "app.asar.unpacked", "dsh", "node_modules"))
		matches, _ := filepath.Glob(filepath.Join(dir, "Programs", "*", "resources", "app.asar.unpacked", "dsh", "node_modules"))
		sort.Strings(matches)
		for _, match := range matches {
			add(match)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return roots
	}
	for dir, depth := cwd, 0; dir != "" && depth < 10; depth++ {
		addSnapshots(dir, add)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return roots
}

// addSnapshots 把一个目录下的常见布局加进候选：自身、resources 解包、同级应用目录。
func addSnapshots(dir string, add func(string)) {
	add(filepath.Join(dir, "node_modules"))
	add(filepath.Join(dir, "resources", "app.asar.unpacked", "dsh", "node_modules"))
	add(filepath.Join(dir, "dsh", "node_modules"))
	matches, err := filepath.Glob(filepath.Join(dir, "*", "resources", "app.asar.unpacked", "dsh", "node_modules"))
	if err == nil {
		sort.Strings(matches)
		for _, match := range matches {
			add(match)
		}
	}
}

// isSystemTempPath 判断路径是否落在系统临时/构建缓存里（发现器不去这些地方翻）。
func isSystemTempPath(path string) bool {
	lower := strings.ToLower(filepath.Clean(path))
	for _, prefix := range []string{
		strings.ToLower(filepath.Clean(os.TempDir())),
		`c:\windows`,
	} {
		if prefix == "" {
			continue
		}
		if lower == prefix || strings.HasPrefix(lower, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return strings.Contains(lower, "go-build") || strings.Contains(lower, string(filepath.Separator)+"temp"+string(filepath.Separator))
}
