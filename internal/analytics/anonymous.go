package analytics

// 匿名用户身份（官方 `telemetry.anonymousUserId`，dsh-base/cordis.patch.yml:210）。
//
// 官方原文：
//
//	Exports carry the harness home's anonymous user id ($DSH_HOME/.anonymous-user-id,
//	random UUID; delete the file to reset the identity) as the Resource's user.id.
//
// 本仓的红线（AGENTS W0 裁定）：productAnalytics 默认**本地零出网**。所以这条能力
// 只在本地落地——文件在 `$DSHD_HOME/.anonymous-user-id`，内容是一个随机 UUIDv4，
// **生成过程不产生任何网络请求**（crypto/rand + 本地文件），只在用户显式开了
// `local+export` 时才会随脱敏记录一起离开本机。
//
// 为什么它必须存在（而不是"反正是本地，随便"）：没有稳定身份，本地事件无法回答
// "这是同一个用户的两天，还是两个用户各一天"——那正是产品分析唯一要回答的问题。

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// anonymousUserIDFileName 是官方路径里的文件名（在 DSH home 根下）。
const anonymousUserIDFileName = ".anonymous-user-id"

// uuidV4Pattern 是 RFC 4122 v4 的形态（version 位 4、variant 位 8/9/a/b）。
var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// isUUIDv4 报告一个字符串是不是规范的小写 UUIDv4。
func isUUIDv4(value string) bool { return uuidV4Pattern.MatchString(value) }

// ensureAnonymousUserID 读取或生成 home 下的匿名身份。
//
// 语义与官方一致：
//   - 文件存在且内容是合法 UUIDv4 → 原样复用（跨进程/跨会话稳定）；
//   - 文件不存在 → 生成一个并原子写入（0600）；
//   - 文件存在但内容坏了/为空 → 按"删文件即重置"的等价语义**重写**一份新的
//     （不是静默返回空 id：空 id 意味着所有事件都没有身份，而且是不可见的降级）。
func ensureAnonymousUserID(home string) (string, error) {
	path := filepath.Join(home, anonymousUserIDFileName)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if existing := strings.TrimSpace(string(raw)); isUUIDv4(existing) {
			return existing, nil
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return "", fmt.Errorf("analytics: 读 %s: %w", path, err)
	}
	id, err := newUUIDv4()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("analytics: 创建 %s: %w", home, err)
	}
	if err := writeFileAtomic0600(path, []byte(id+"\n")); err != nil {
		return "", fmt.Errorf("analytics: 写 %s: %w", path, err)
	}
	return id, nil
}

// newUUIDv4 生成一个 UUIDv4（crypto/rand，零网络）。
func newUUIDv4() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("analytics: 生成匿名 id: %w", err)
	}
	raw[6] = raw[6]&0x0f | 0x40 // version 4
	raw[8] = raw[8]&0x3f | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

// writeFileAtomic0600 用"同目录临时文件 + rename"写，权限 0600：
// 身份文件可以被用户看见/删除，但不该默认对其他账户可读。
func writeFileAtomic0600(path string, raw []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".anonymous-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	// Chmod 失败不算致命（Windows 上基本是空操作），所以只忽略它；
	// 但 Write/Sync 失败**必须报出来**——旧写法把 `err :=` 写进了 if 的作用域，
	// 内层那次赋值根本没碰到外层 err，写盘失败会被静默吞掉（SA4006 抓的就是它）。
	_ = tmp.Chmod(0o600)
	if _, err = tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
