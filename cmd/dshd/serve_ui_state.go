// serve_ui_state.go —— 界面状态（`uiState.get/set/delete`）的宿主实现与生产接线。
//
// # 这份状态是什么
//
// 官方 GUI 把一批**纯前端记忆**放在 Electron 的 `Local Storage`（leveldb）里，
// 键名逐字是 `dsh.sidebar-browser.v1.session-<uuid>`、`dsh.user-questions.drafts.v1`
// 这一类。本宿主没有 Electron 容器，所以给它一个同语义的落点：
//
//	<home>/state/ui-state/<key>.json
//
// 一个 key 一个文件，值是原样字节。与 `internal/account/account.go:350`、
// `internal/agent/child_agents.go:59` 的 `state/*.json` 同一层。
//
// # 为什么不是 settings
//
// settings.mutate 带 revision 栅栏：界面每点一次"已读"就会推进 revision，
// 用户这时保存设置就 revision_conflict。这是**正确性**问题，不是风格问题。
//
// # 为什么不是 ui-config（config.get/set）
//
// `serve_session_ops.go` 的 `serveConfigGet` 是**一份**扁平 key→string 表
// （`state/ui-config.json`），读一次全量、写是合并。界面状态是"一 key 一文件、
// 可单独删除、可单读"，用它会被迫把整份表搬进搬出，也会丢掉 delete 语义。
//
// # 边界
//
// key 只做**文件名安全**校验（不是语义校验）：字符集、长度、不许 "."/".."。
// 宿主不认识任何一个具体的 key，也不做迁移、版本或结构校验——那是界面的事。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dshgo/internal/transport"
)

const (
	// uiStateDirName 是 `<home>/state/` 下的子目录名。
	uiStateDirName = "ui-state"
	// maxUIStateKeyLen 是 key 的**宿主安全上界**（不是官方数值：官方 localStorage
	// 的键长上限由浏览器给，与本仓的目录布局无关）。200 覆盖官方那批键
	// （最长的一个带 uuid 也才四十来个字符），超出的一律拒。
	maxUIStateKeyLen = 200
	// maxUIStateValueBytes 是单份状态的**宿主安全上界**（同样不是官方数值）。
	// 官方那几份状态都是几十 KB 级；1 MiB 足够，同时挡住"一个插件把盘写满"。
	maxUIStateValueBytes = 1 << 20
)

// uiStateDir 是界面状态的根。不同 home 之间不共享任何对象。
func uiStateDir(home string) string {
	return filepath.Join(home, "state", uiStateDirName)
}

// validateUIStateKey 只回答一个问题：这个 key 能不能安全地当一个文件名。
//
// 允许的字符集是 [A-Za-z0-9._-]：官方那批键（`dsh.sidebar-browser.v1.session-<uuid>`、
// `dsh.user-questions.drafts.v1`）都在里面；路径分隔符、Windows 保留字符
// （`\ / : * ? " < > |`）、控制字符与空白全部拒掉。首字符不许是 "."：
// 那是隐藏文件/临时文件的地盘（写入用的 `.ui-state-*.tmp` 就在这里）。
func validateUIStateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: key 不能为空", transport.ErrUIStateBadKey)
	}
	if len(key) > maxUIStateKeyLen {
		return fmt.Errorf("%w: key 长度 %d 超过上界 %d", transport.ErrUIStateBadKey, len(key), maxUIStateKeyLen)
	}
	if key == "." || key == ".." || strings.HasPrefix(key, ".") {
		return fmt.Errorf("%w: key %q 不能是隐藏名或相对路径", transport.ErrUIStateBadKey, key)
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("%w: key %q 含非法字符 %q（只许字母数字与 . _ -）",
				transport.ErrUIStateBadKey, key, string(r))
		}
	}
	return nil
}

// uiStateFile 把 key 映射成文件路径。调用前必须已过 validateUIStateKey。
func uiStateFile(home, key string) string {
	return filepath.Join(uiStateDir(home), key+".json")
}

// uiStateStore 是 transport.UIStateStore 的生产实现。
//
// 无内存缓存：每次读写都落盘。理由是这份状态量小（官方那几份是几十 KB 级）、
// 写频率低（用户点击），缓存只会多一处可能与磁盘不一致的副本。
type uiStateStore struct {
	home string
}

func newUIStateStore(home string) *uiStateStore { return &uiStateStore{home: home} }

func (s *uiStateStore) Get(key string) (transport.UIState, bool, error) {
	if err := validateUIStateKey(key); err != nil {
		return transport.UIState{}, false, err
	}
	raw, err := os.ReadFile(uiStateFile(s.home, key))
	if err != nil {
		if os.IsNotExist(err) {
			return transport.UIState{}, false, nil
		}
		return transport.UIState{}, false, fmt.Errorf("读界面状态 %q: %w", key, err)
	}
	// 0 字节说明这份文件写坏了或被清空：按"没有"处理，不假装有一份空状态
	// （空字节也不是合法 JSON，界面拿回去只会解析失败）。
	if len(raw) == 0 {
		return transport.UIState{}, false, nil
	}
	return transport.UIState{Key: key, Value: raw}, true, nil
}

func (s *uiStateStore) Set(key string, value []byte) error {
	if err := validateUIStateKey(key); err != nil {
		return err
	}
	if len(value) == 0 {
		return fmt.Errorf("%w: %q 的值为空（清状态请用 uiState.delete）", transport.ErrUIStateBadKey, key)
	}
	if len(value) > maxUIStateValueBytes {
		return fmt.Errorf("%w: %q 的值 %d 字节超过上界 %d",
			transport.ErrUIStateBadKey, key, len(value), maxUIStateValueBytes)
	}
	dir := uiStateDir(s.home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("建界面状态目录 %s: %w", dir, err)
	}
	return writeUIStateAtomic(uiStateFile(s.home, key), value)
}

func (s *uiStateStore) Delete(key string) (bool, error) {
	if err := validateUIStateKey(key); err != nil {
		return false, err
	}
	if err := os.Remove(uiStateFile(s.home, key)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("删界面状态 %q: %w", key, err)
	}
	return true, nil
}

// writeUIStateAtomic 用同目录临时文件 + fsync + rename 原子替换。
//
// 形状照 `internal/account/account.go:496-520`（同目录临时文件 + Chmod 0600 +
// Write + Sync + Close + Rename）：rename 在 POSIX 与 Windows 上都是原子替换，
// 状态文件只有一个写者、last-write-wins 是正确语义。
func writeUIStateAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ui-state-*.tmp")
	if err != nil {
		return fmt.Errorf("建临时文件失败（%s）: %w", dir, err)
	}
	name := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	// Chmod 失败不算致命（Windows 上基本是空操作）；但 Write/Sync 失败必须报出来。
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync 临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("发布 %s 失败: %w", path, err)
	}
	cleanup = false
	return nil
}

// withUIStateOptions 是 `uiState.*` 的生产接线，serve 与真实 HTTP 测试共用同一个函数。
//
// 为什么不在 `cmdServe` 里内联：那份 Options 装配在超大函数里，测试拿不到。
// 测试另写一份闭包就是"测试绿、生产没接"的经典来源（本仓踩过，
// 见 `serve_terminal_resize.go` 的同一段理由）。
func withUIStateOptions(home string, opt transport.Options) transport.Options {
	store := newUIStateStore(home)
	opt.UIState = store
	return opt
}
