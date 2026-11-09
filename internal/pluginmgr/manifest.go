package pluginmgr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// jsEntryExtensions 是本仓**明确不支持**的入口后缀。
//
// 为什么要在 manifest 层就拒：宿主不嵌 JS 运行时（AGENTS R6），一个 .js/.ts
// 入口永远不可能被装载。今天它会被原样复制、install 返回成功——"装上了"被当成
// 成功，而它一次都跑不起来。这类静默成功比报错难查得多（docs/architect-
// official-sync-taskbooks.md HS-07 先红第 3 条）。
var jsEntryExtensions = []string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".mts", ".cts"}

func manifestError(format string, args ...any) *Error {
	return errorf("invalid_manifest", http.StatusUnprocessableEntity, false,
		"修正私有 plugin.json、相对路径与 SHA-256 后重试", format, args...)
}

// unsupportedPluginError 是"合法的插件形态，但本仓不支持"的四字段错误。
//
// 为什么与 invalid_manifest 分开：坏 manifest 是**你写错了**（改文件就能过），
// 而 JS/Cordis/process 插件是**宿主没有对应运行时**（改文件没用，要么换插件，
// 要么等宿主支持）。两件事的下一步完全不同，错误码不能塌成一个。
func unsupportedPluginError(hint, format string, args ...any) *Error {
	return errorf("unsupported_plugin", http.StatusUnprocessableEntity, false, hint, format, args...)
}

// isJSEntry 判断入口是不是 JS/TS 文件（大小写不敏感：Windows 上 .JS 一样是 JS）。
func isJSEntry(entry string) bool {
	lower := strings.ToLower(strings.TrimSpace(entry))
	for _, ext := range jsEntryExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// looksLikeCordisPackage 判断目录是不是官方 Cordis/npm 插件形态。
//
// 判据只有两条，都来自官方包结构本身：根上有 package.json（npm 包），
// 或者有 cordis.patch.yml（Cordis 热重载补丁）。本仓的私有包唯一 manifest 是
// plugin.json，所以"没有 plugin.json 但有这两样"就一定是另一种生态的插件。
func looksLikeCordisPackage(dir string) bool {
	for _, name := range []string{"package.json", "cordis.patch.yml", "cordis.yml"} {
		if info, err := os.Lstat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// decodeStrict rejects duplicate keys, unknown fields, trailing values and wrong shapes.
func decodeStrict(raw []byte, target any) error {
	duplicateCheck := json.NewDecoder(bytes.NewReader(raw))
	duplicateCheck.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := duplicateCheck.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for duplicateCheck.More() {
				keyToken, err := duplicateCheck.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("对象键不是字符串")
				}
				if seen[key] {
					return fmt.Errorf("重复 JSON 键 %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = duplicateCheck.Token()
			return err
		case '[':
			for duplicateCheck.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = duplicateCheck.Token()
			return err
		default:
			return fmt.Errorf("非法 JSON 分隔符 %q", delim)
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := duplicateCheck.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON 后有多余值")
		}
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON 后有多余值")
		}
		return err
	}
	return nil
}

func safePackagePath(name string) error {
	if name == "" || name == "." || strings.Contains(name, `\`) || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("非法相对路径 %q", name)
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) || strings.Contains(name, ":") {
		return fmt.Errorf("绝对路径/盘符越界 %q", name)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if clean != name || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("路径逃逸 %q", name)
	}
	return nil
}

func validateID(id string) error {
	if !pluginIDPattern.MatchString(id) || strings.Contains(id, "..") {
		return fmt.Errorf("非法插件 id %q", id)
	}
	return nil
}

func readAndValidatePackage(source string) (Manifest, error) {
	var manifest Manifest
	if source == "" {
		return manifest, badRequest("缺 path：要本地私有包目录")
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return manifest, manifestError("解析包目录失败: %v", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return manifest, manifestError("读取包目录 %s 失败: %v", absolute, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return manifest, manifestError("包根必须是真实目录且不能是 symlink: %s", absolute)
	}
	manifestPath := filepath.Join(absolute, "plugin.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		// 另一套生态的插件目录（Cordis/npm）：本仓不嵌 JS 运行时（R6）。
		// 必须点名"不支持 JS 插件"，而不是报"读不到 plugin.json"——
		// 后者读起来像文件放错了，会让人一直去找那个不存在的文件。
		if os.IsNotExist(err) && looksLikeCordisPackage(absolute) {
			return manifest, unsupportedPluginError(
				"本仓不嵌 JS 运行时（AGENTS R6）：受支持的是 Go/WASM 工具插件"+
					"（GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared）；"+
					"要跑 Cordis/JS 插件请用官方桌面端，或把插件改写成 wasm 工具",
				"包 %s 是 Cordis/npm 形态（package.json 或 cordis.patch.yml + JS 入口），"+
					"不是本仓的 dsh.local-plugin@1 私有包；JS/Cordis 插件明确不支持", absolute)
		}
		return manifest, manifestError("读取 plugin.json 失败: %v", err)
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > 1<<20 {
		return manifest, manifestError("plugin.json 必须是 ≤1MiB 的常规文件")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return manifest, manifestError("读取 plugin.json 失败: %v", err)
	}
	if err := decodeStrict(raw, &manifest); err != nil {
		return manifest, manifestError("坏 manifest: %v", err)
	}
	if manifest.Schema != ManifestSchema {
		return manifest, manifestError("未知 manifest schema %q；只接受 %s", manifest.Schema, ManifestSchema)
	}
	if err := validateID(manifest.ID); err != nil {
		return manifest, manifestError("%v", err)
	}
	if _, err := parseSemver(manifest.Version); err != nil {
		return manifest, manifestError("非法版本 %q: %v", manifest.Version, err)
	}
	if manifest.Kind != "wasm" && manifest.Kind != "process" {
		return manifest, manifestError("非法 kind %q；只接受 wasm/process", manifest.Kind)
	}
	// process 后端没有实现（internal/kernel/module.go 的 implementedBackends 只有
	// static/wasm）。装上一个永远跑不起来的包就是静默成功，必须在安装边界上炸。
	if manifest.Kind == "process" {
		return manifest, unsupportedPluginError(
			"本仓没有 process 插件后端（internal/kernel/module.go 的 implementedBackends 只有 static/wasm）；"+
				"受支持的是 wasm 工具插件：GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared",
			"插件 %s 声明 kind = \"process\"：process 后端未实现，装上也永远跑不起来", manifest.ID)
	}
	if err := safePackagePath(manifest.Entry); err != nil {
		return manifest, manifestError("entry 越界: %v", err)
	}
	// 入口是 JS/TS：合法路径，但本仓没有 JS 运行时。原样收下就是"装上了"
	// 却一次都跑不起来——静默成功，必须显式拒绝。
	if isJSEntry(manifest.Entry) {
		return manifest, unsupportedPluginError(
			"本仓不嵌 JS 运行时（AGENTS R6）：受支持的是 Go/WASM 工具插件"+
				"（GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared）；"+
				"JS/Cordis 插件请用官方桌面端，或改写成 wasm 工具",
			"插件 %s 的入口 %q 是 JavaScript/TypeScript 文件：JS/Cordis 插件明确不支持", manifest.ID, manifest.Entry)
	}
	if err := validateToolDeclarations(manifest.Tools); err != nil {
		return manifest, manifestError("工具声明非法: %v", err)
	}
	if len(manifest.Files) == 0 {
		return manifest, manifestError("files 不能为空，且必须包含 entry")
	}
	if _, ok := manifest.Files[manifest.Entry]; !ok {
		return manifest, manifestError("entry %q 没有文件哈希", manifest.Entry)
	}
	for name, wantHash := range manifest.Files {
		if err := safePackagePath(name); err != nil {
			return manifest, manifestError("files 路径越界: %v", err)
		}
		if name == "plugin.json" {
			return manifest, manifestError("plugin.json 自身不应列入 files")
		}
		if len(wantHash) != 64 {
			return manifest, manifestError("文件 %s 的 SHA-256 必须是 64 位小写 hex", name)
		}
		if _, err := hex.DecodeString(wantHash); err != nil || wantHash != strings.ToLower(wantHash) {
			return manifest, manifestError("文件 %s 的 SHA-256 非法", name)
		}
		path := filepath.Join(absolute, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			return manifest, manifestError("登记文件 %s 不可读: %v", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return manifest, manifestError("登记文件 %s 必须是常规非 symlink 文件", name)
		}
		if err := verifyFileHash(path, wantHash); err != nil {
			return manifest, err
		}
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absolute {
			return nil
		}
		rel, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("包内禁止 symlink: %s", name)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("包内只允许常规文件: %s", name)
		}
		if name != "plugin.json" {
			if _, ok := manifest.Files[name]; !ok {
				return fmt.Errorf("存在未登记哈希的文件 %s", name)
			}
			seen[name] = true
		}
		return nil
	})
	if err != nil {
		return manifest, manifestError("包目录校验失败: %v", err)
	}
	if len(seen) != len(manifest.Files) {
		return manifest, manifestError("files 与包目录实际文件不一致")
	}
	return manifest, nil
}

func verifyFileHash(path, want string) error {
	file, err := os.Open(path)
	if err != nil {
		return manifestError("打开文件 %s 失败: %v", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return manifestError("哈希文件 %s 失败: %v", path, err)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if got != want {
		return errorf("hash_mismatch", http.StatusUnprocessableEntity, false,
			"重新生成私有包及 plugin.json 中的 SHA-256", "文件 %s SHA-256 不符：got=%s want=%s", path, got, want)
	}
	return nil
}

type semVersion struct {
	core [3]uint64
	pre  []string
}

func validIdentifiers(value string) bool {
	if value == "" {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func parseSemver(value string) (semVersion, error) {
	var out semVersion
	main := value
	if index := strings.IndexByte(main, '+'); index >= 0 {
		build := main[index+1:]
		main = main[:index]
		if !validIdentifiers(build) || strings.Contains(build, "+") {
			return out, fmt.Errorf("非法 build metadata")
		}
	}
	if index := strings.IndexByte(main, '-'); index >= 0 {
		pre := main[index+1:]
		main = main[:index]
		if !validIdentifiers(pre) {
			return out, fmt.Errorf("非法 prerelease")
		}
		out.pre = strings.Split(pre, ".")
		for _, part := range out.pre {
			if isDigits(part) && len(part) > 1 && part[0] == '0' {
				return out, fmt.Errorf("prerelease 数字有前导零")
			}
		}
	}
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("必须是 major.minor.patch")
	}
	for i, part := range parts {
		if !isDigits(part) || len(part) > 1 && part[0] == '0' {
			return out, fmt.Errorf("核心版本数字非法")
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return out, err
		}
		out.core[i] = n
	}
	return out, nil
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func compareSemver(a, b semVersion) int {
	for i := range a.core {
		if a.core[i] < b.core[i] {
			return -1
		}
		if a.core[i] > b.core[i] {
			return 1
		}
	}
	if len(a.pre) == 0 && len(b.pre) != 0 {
		return 1
	}
	if len(a.pre) != 0 && len(b.pre) == 0 {
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		xn, yn := isDigits(x), isDigits(y)
		switch {
		case xn && yn:
			if len(x) != len(y) {
				if len(x) < len(y) {
					return -1
				}
				return 1
			}
		case xn != yn:
			if xn {
				return -1
			}
			return 1
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}

func sortedFileNames(files map[string]string) []string {
	out := make([]string, 0, len(files))
	for name := range files {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
