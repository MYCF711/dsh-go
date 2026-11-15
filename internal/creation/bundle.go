package creation

// 生成包校验与复制：官方 `src/generated-bundle.ts` 的 Go 落地。
//
// 官方 `validateGeneratedBundle(directory, name)` 在**装进 profile 之前**和
// **复制进 profile 之后**各跑一次（src/index.ts 的 generate()），判据逐条是：
//
//  1. `package.json`：`name` **字面等于**保留名；`version` 非空；
//     `dsh.bundle.patch` 必须是 `'./cordis.patch.yml'`；`dsh.client` 可选，
//     但声明了就必须有已构建的 `./client` 入口。
//  2. `cordis.patch.yml`：只允许 `{insert:[...]}` 行（`.strict()`：op 层不许
//     其它键），每个 `insert` 至少一行；每行 `id` 必须以 `<name>:` 开头且唯一，
//     `id` 不许被两个插件抢占；`name` 必须是本包或本包的导出子路径；`group: true`
//     明确拒绝。
//  3. 每个插入行的目标入口必须解析到 `.js/.mjs/.cjs` 的**已构建真实文件**
//     （不许 `.ts`，不许指向目录）。
//  4. 声明 `dsh.client` 时 `./client` 入口同样必须是已构建真实文件。
//  5. 路径必须留在包内；符号链接必须相对且不逃出包。
//
// # 与官方的两处差异（都是**更严**，不是更松）
//
//   - **补丁解析**：宿主没有 YAML 解析器（仓内 go.mod 不引入 yaml），所以只接受
//     **JSON 数组**形式的 `cordis.patch.yml`——JSON 是 YAML 的子集，官方 Creator
//     的提示词本来就要求 "plain YAML or JSON array"。YAML 专有语法（锚点/别名/
//     自定义标签/分组）会被明确拒绝并说明原因，不会被"猜着解析"。
//   - **符号链接**：官方允许包内相对 symlink；本仓 pluginmgr 完全禁止包内
//     symlink（internal/pluginmgr/manifest.go 的 WalkDir 判据），装进去也过不了。
//     所以这里直接拒绝并说明，而不是让它在安装时才以一条难懂的错失败。
//
// 校验**不执行**任何包内代码（与官方一致）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	manifestFileName = "package.json"
	patchFileName    = "cordis.patch.yml"
	patchRelPath     = "./" + patchFileName
	// maxManifestBytes 是单个文档的读取上限（1 MiB）：生成的 manifest 与补丁
	// 都是小文件，超过就是写错了，不该被整个读进内存。
	maxManifestBytes = 1 << 20
)

// runtimeExtensions 是官方允许的运行时入口后缀（generated-bundle.ts 的 RUNTIME_EXTENSIONS）。
var runtimeExtensions = []string{".js", ".mjs", ".cjs"}

// patchOperation 是补丁里的一条操作（官方只允许 `insert`，且 op 层是 strict）。
type patchOperation struct {
	Insert []patchRow `json:"insert"`
}

// patchRow 是一条插入行（官方用 `z.looseObject`：未知键允许）。
type patchRow struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group *bool  `json:"group,omitempty"`
}

// ValidateBundle 校验一个生成包目录是否满足官方判据。
//
// 返回 nil 表示：包身份正确、补丁只加自己的行、每行入口都是已构建真实文件、
// 声明了 `dsh.client` 时 `./client` 也已构建、包内没有 symlink。
// 任何一条不满足都返回带**具体原因**的错误（不静默通过）。
func ValidateBundle(directory, name string) error {
	dir, err := filepath.Abs(strings.TrimSpace(directory))
	if err != nil {
		return fmt.Errorf("creation: 解析包目录失败: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("creation: 读不到生成包目录 %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("creation: 生成包根 %s 不是目录", dir)
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("creation: 校验生成包时必须给宿主保留名")
	}
	manifest, err := readManifest(dir)
	if err != nil {
		return err
	}
	if manifest.Name != name {
		return fmt.Errorf("creation: package.json 的 name 必须是宿主保留名 %q，实际是 %q", name, manifest.Name)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return errors.New("creation: package.json 的 version 不能为空")
	}
	if strings.TrimSpace(manifest.Dsh.Bundle.Patch) != patchRelPath {
		return fmt.Errorf("creation: package.json 的 dsh.bundle.patch 必须是 %q，实际是 %q",
			patchRelPath, manifest.Dsh.Bundle.Patch)
	}
	patchPath, err := containedFile(dir, patchRelPath)
	if err != nil {
		return err
	}
	raw, err := readCapped(patchPath)
	if err != nil {
		return err
	}
	operations, err := parsePatch(raw)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, operation := range operations {
		for _, row := range operation.Insert {
			id := strings.TrimSpace(row.ID)
			rowName := strings.TrimSpace(row.Name)
			if !strings.HasPrefix(id, name+":") {
				return fmt.Errorf("creation: 插入行 id %q 必须以 %q 开头（生成包只能加自己的行）", row.ID, name+":")
			}
			if seen[id] {
				return fmt.Errorf("creation: 插入行 id %q 重复", id)
			}
			seen[id] = true
			if rowName != name && !strings.HasPrefix(rowName, name+"/") {
				return fmt.Errorf("creation: 插入行 %q 的 name %q 必须是本包 %q 或它的导出子路径", id, row.Name, name)
			}
			if row.Group != nil && *row.Group {
				return errors.New("creation: 生成包不允许 group: true（官方只接受平铺插入行）")
			}
			target, ok := exportedEntry(manifest, rowName)
			if !ok {
				return fmt.Errorf("creation: 插入行 %q 的 name %q 在 package.json 的 exports/main 里解析不到入口", id, rowName)
			}
			if err := requireBuiltEntry(dir, rowName, target); err != nil {
				return err
			}
		}
	}
	if declaresClient(manifest) {
		target, ok := exportedEntry(manifest, name+"/client")
		if !ok {
			return fmt.Errorf("creation: 声明了 dsh.client 就必须导出 %q 的已构建入口", name+"/client")
		}
		if err := requireBuiltEntry(dir, name+"/client", target); err != nil {
			return err
		}
	}
	return validateTree(dir)
}

// declaresClient 判断包是否声明了 dsh.client（`{}` 也算声明，null 不算）。
func declaresClient(manifest Manifest) bool {
	raw := bytes.TrimSpace(manifest.Dsh.Client)
	return len(raw) > 0 && string(raw) != "null"
}

// readManifest 读并解析 package.json（未知字段允许，与官方 `z.looseObject` 一致）。
func readManifest(dir string) (Manifest, error) {
	var manifest Manifest
	path, err := containedFile(dir, manifestFileName)
	if err != nil {
		return manifest, err
	}
	raw, err := readCapped(path)
	if err != nil {
		return manifest, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return manifest, errors.New("creation: package.json 是空文件")
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("creation: package.json 不是合法 JSON 对象: %w", err)
	}
	return manifest, nil
}

// parsePatch 解析 cordis.patch.yml。
//
// 只接受 JSON 数组（YAML 子集）：宿主没有 YAML 解析器，宁可明确拒绝也不猜。
// op 层用 DisallowUnknownFields（对应官方 `.strict()`）；行层允许未知键
// （对应 `z.looseObject`）。
func parsePatch(raw []byte) ([]patchOperation, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("creation: %s 是空文件（至少要有一条 insert 操作）", patchFileName)
	}
	if trimmed[0] != '[' {
		return nil, fmt.Errorf(
			"creation: %s 必须是 JSON 数组（本宿主没有 YAML 解析器；JSON 是 YAML 的子集，"+
				"官方的补丁本来也允许写成 JSON 数组）。YAML 专有语法（锚点/别名/自定义标签/分组）明确不支持",
			patchFileName)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var operations []patchOperation
	if err := decoder.Decode(&operations); err != nil {
		return nil, fmt.Errorf("creation: %s 不是合法的补丁数组: %w", patchFileName, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("creation: %s 在补丁数组之后还有多余内容", patchFileName)
	}
	if len(operations) == 0 {
		return nil, fmt.Errorf("creation: %s 至少要有一条 insert 操作", patchFileName)
	}
	for index, operation := range operations {
		if len(operation.Insert) == 0 {
			return nil, fmt.Errorf("creation: %s 第 %d 条操作没有 insert 行", patchFileName, index+1)
		}
	}
	return operations, nil
}

// exportedEntry 按 package.json 的 main/exports 解析一个包内说明符的入口相对路径。
//
// 这是 `resolve.exports` 的**文档化子集**：没有 exports 时用 main（缺省
// `./index.js`），子路径按 `./<后缀>`；有 exports 时支持字符串、条件表
// （default/import/require/node，按此顺序取第一个可用的）、以及数组形式的
// fallback。解析不到返回 ok=false（调用方报"没构建入口"，不猜）。
func exportedEntry(manifest Manifest, specifier string) (string, bool) {
	if manifest.Exports == nil {
		if specifier == manifest.Name {
			if main := strings.TrimSpace(manifest.Main); main != "" {
				return main, true
			}
			return "./index.js", true
		}
		if !strings.HasPrefix(specifier, manifest.Name+"/") {
			return "", false
		}
		return "./" + strings.TrimPrefix(specifier, manifest.Name+"/"), true
	}
	switch table := manifest.Exports.(type) {
	case string:
		if specifier == manifest.Name {
			return table, table != ""
		}
		return "", false
	case map[string]any:
		key := "."
		if specifier != manifest.Name {
			if !strings.HasPrefix(specifier, manifest.Name+"/") {
				return "", false
			}
			key = "./" + strings.TrimPrefix(specifier, manifest.Name+"/")
		}
		if value, ok := table[key]; ok {
			return resolveExportTarget(value)
		}
		// 顶层直接是条件表（`"exports": {"default": "./index.js"}`）时只服务包根。
		if specifier == manifest.Name {
			return resolveConditions(table)
		}
	}
	return "", false
}

// exportConditions 是条件表的取值顺序（官方依赖 `resolve.exports` 的默认条件，
// 这里只声明本宿主认的四个；不在表里的条件不会被误当成入口）。
var exportConditions = []string{"default", "import", "require", "node"}

func resolveExportTarget(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, strings.TrimSpace(typed) != ""
	case map[string]any:
		return resolveConditions(typed)
	case []any:
		for _, item := range typed {
			if target, ok := resolveExportTarget(item); ok {
				return target, true
			}
		}
	}
	return "", false
}

func resolveConditions(table map[string]any) (string, bool) {
	for _, condition := range exportConditions {
		value, ok := table[condition]
		if !ok {
			continue
		}
		if target, ok := resolveExportTarget(value); ok {
			return target, true
		}
	}
	return "", false
}

// requireBuiltEntry 要求入口指向包内一个**已构建**的 .js/.mjs/.cjs 常规文件。
func requireBuiltEntry(dir, specifier, target string) error {
	if !hasRuntimeExtension(target) {
		return fmt.Errorf("creation: %q 的入口 %q 不是 .js/.mjs/.cjs（生成包必须先构建再安装）", specifier, target)
	}
	file, err := containedFile(dir, target)
	if err != nil {
		return fmt.Errorf("creation: %q 的入口 %q 不可用: %w", specifier, target, err)
	}
	info, err := os.Lstat(file)
	if err != nil {
		return fmt.Errorf("creation: %q 的入口 %q 读不到: %w", specifier, target, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("creation: %q 的入口 %q 必须是常规文件", specifier, target)
	}
	return nil
}

func hasRuntimeExtension(target string) bool {
	lower := strings.ToLower(strings.TrimSpace(target))
	for _, extension := range runtimeExtensions {
		if strings.HasSuffix(lower, extension) {
			return true
		}
	}
	return false
}

// containedFile 把一个包内相对路径解析成真实路径，并保证它留在包内且不是 symlink。
func containedFile(dir, relative string) (string, error) {
	trimmed := strings.TrimSpace(relative)
	if trimmed == "" {
		return "", errors.New("creation: 生成包里的路径不能为空")
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) {
		return "", fmt.Errorf("creation: 生成包里的路径 %q 不能是绝对路径", trimmed)
	}
	full := filepath.Clean(filepath.Join(dir, filepath.FromSlash(trimmed)))
	rel, err := filepath.Rel(dir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("creation: 生成包里的路径 %q 逃出了包目录", trimmed)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", fmt.Errorf("creation: 生成包里的 %q 不可读: %w", trimmed, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("creation: 生成包里不允许 symlink（%q；本仓 pluginmgr 也会拒绝包内 symlink）", trimmed)
	}
	return full, nil
}

// readCapped 读一个 ≤1 MiB 的常规文件。
func readCapped(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("creation: 打开 %s 失败: %w", path, err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("creation: 读 %s 失败: %w", path, err)
	}
	if len(raw) > maxManifestBytes {
		return nil, fmt.Errorf("creation: %s 超过 %d 字节上限", path, maxManifestBytes)
	}
	return raw, nil
}

// validateTree 走一遍生成包：只允许常规文件与目录，任何 symlink 都拒绝。
func validateTree(dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("creation: 遍历生成包失败: %w", walkErr)
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("creation: 生成包里不允许 symlink（%q）", filepath.ToSlash(rel))
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("creation: 生成包里只允许常规文件（%q）", filepath.ToSlash(rel))
		}
		return nil
	})
}

// CopyBundle 把暂存目录整棵复制到 profile 的保留目录（官方 `cp(source, directory,
// {recursive:true, force:false, errorOnExist:true, verbatimSymlinks:true})`）。
//
// 目标**必须不存在**（官方 errorOnExist: true）：已存在的保留目录可能属于另一张卡，
// 覆盖它等于悄悄换掉了别人装的内容。
func CopyBundle(source, destination string) error {
	src, err := filepath.Abs(strings.TrimSpace(source))
	if err != nil {
		return fmt.Errorf("creation: 解析暂存目录失败: %w", err)
	}
	dst, err := filepath.Abs(strings.TrimSpace(destination))
	if err != nil {
		return fmt.Errorf("creation: 解析 profile 目录失败: %w", err)
	}
	if src == dst {
		return errors.New("creation: 暂存目录与 profile 目录相同，拒绝原地复制")
	}
	if _, statErr := os.Lstat(dst); statErr == nil {
		return fmt.Errorf("creation: profile 保留目录 %s 已存在，拒绝覆盖", dst)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("creation: 检查 profile 目录失败: %w", statErr)
	}
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("creation: 遍历暂存目录失败: %w", walkErr)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("creation: 生成包里不允许 symlink（%q）", filepath.ToSlash(rel))
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("creation: 生成包里只允许常规文件（%q）", filepath.ToSlash(rel))
		}
		return copyFile(path, target)
	})
}

func copyFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("creation: 建 profile 目录失败: %w", err)
	}
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("creation: 打开 %s 失败: %w", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creation: 创建 %s 失败: %w", destination, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("creation: 复制 %s 失败: %w", source, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("creation: 关闭 %s 失败: %w", destination, err)
	}
	return nil
}
