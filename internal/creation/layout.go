package creation

// 落盘布局与官方链路上的"保留名/两个目录"。
//
// 官方（src/index.ts 的 decide()）：
//
//	id := randomUUID()
//	directory         := join(profileContext.dir, 'created-plugins', id)   // 装进 profile 的复制件
//	sourceDirectory   := join(agent.session.header.cwd, '.dsh-created-plugins', id) // Creator 写代码的地方
//	bundleName        := RESERVED_PREFIX + id
//
// 三步顺序在本宿主里一一对应（见 service.go 的 generate）；本文件只负责
// 算路径与造 id，不做任何 I/O（读路径不写盘）。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// CreatedPluginsDir 是 profile 里"用户生成的插件"目录名（官方硬编码 'created-plugins'）。
const CreatedPluginsDir = "created-plugins"

// StagingDirName 是会话工作目录下的暂存目录名（官方硬编码 '.dsh-created-plugins'）。
const StagingDirName = ".dsh-created-plugins"

// NewID 造一个官方同形状的随机 id（uuid v4 文本）。
//
// 只用 crypto/rand：id 会进包名与目录名，不能让两次调用撞车。
func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("creation: 生成 id 失败: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // variant
	buf := make([]byte, 0, 36)
	buf = append(buf, hex.EncodeToString(raw[0:4])...)
	buf = append(buf, '-')
	buf = append(buf, hex.EncodeToString(raw[4:6])...)
	buf = append(buf, '-')
	buf = append(buf, hex.EncodeToString(raw[6:8])...)
	buf = append(buf, '-')
	buf = append(buf, hex.EncodeToString(raw[8:10])...)
	buf = append(buf, '-')
	buf = append(buf, hex.EncodeToString(raw[10:16])...)
	return string(buf), nil
}

// BundleName 是宿主保留名：官方 RESERVED_PREFIX + id 的宿主拼写。
//
// 官方名字（`@dsh-local/view-<id>`）装不进本仓 pluginmgr（id 不许 `@`/`/`），
// 所以这里用 HostBundlePrefix。语义完全相同：这个名字**由宿主分配**，
// 模型不能用它命名别的东西。
func BundleName(id string) string { return HostBundlePrefix + id }

// IDOfBundleName 从宿主保留名里取回 id。
func IDOfBundleName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if !strings.HasPrefix(trimmed, HostBundlePrefix) {
		return "", fmt.Errorf("creation: %q 不是宿主保留名（应以 %s 开头）", name, HostBundlePrefix)
	}
	id := strings.TrimPrefix(trimmed, HostBundlePrefix)
	if strings.TrimSpace(id) == "" {
		return "", errors.New("creation: 宿主保留名缺 id")
	}
	return id, nil
}

// ProfileCopyDir 是装进 profile 的复制件目录（官方 join(profileContext.dir, 'created-plugins', id)）。
//
// 它在安装前由宿主创建、安装失败后**保留**——官方 restore 就是从这份副本重装的
// （src/index.ts 的 restore()：`join(profileContext.dir, 'created-plugins',
// previous.bundleName.slice(RESERVED_PREFIX.length))`）。
func ProfileCopyDir(home, id string) string {
	return filepath.Join(home, CreatedPluginsDir, id)
}

// StagingDir 是 Creator 写代码的暂存目录（官方 join(session.cwd, '.dsh-created-plugins', id)）。
//
// cwd 取会话真实头部的 cwd；为空时退回 home（官方 `header.cwd ?? profileContext.cwd`）。
func StagingDir(cwd, home, id string) string {
	base := strings.TrimSpace(cwd)
	if base == "" {
		base = home
	}
	return filepath.Join(base, StagingDirName, id)
}

// Manifest 是生成包里 package.json 的**必要字段**（官方 validateGeneratedBundle
// 用 zod 校验的同一组字段；未知字段允许，与 `z.looseObject` 一致）。
type Manifest struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Main    string      `json:"main,omitempty"`
	Exports any         `json:"exports,omitempty"`
	Dsh     ManifestDsh `json:"dsh"`
}

// ManifestDsh 是 package.json 的 `dsh` 段。
type ManifestDsh struct {
	Bundle struct {
		Patch string `json:"patch"`
	} `json:"bundle"`
	// Client 是原始 JSON：官方判据是"**声明了** dsh.client 就必须有已构建的
	// ./client 入口"，空对象 `{}` 也算声明，所以这里不能用 map（`{}` 解出来
	// len==0，会被误判成没声明）。
	Client json.RawMessage `json:"client,omitempty"`
}
