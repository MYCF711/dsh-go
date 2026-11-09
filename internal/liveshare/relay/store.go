package relay

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ownerTokenFile = "owner-token.json"
	stateFile      = "relay-state.json"
	auditFile      = "audit.jsonl"
	stateVersion   = 1
)

// ownerTokenDoc 是 owner-token.json 的形状。token 只落 0600 文件，不写日志。
type ownerTokenDoc struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"createdAt"`
}

// persistedShare 是 relay-state.json 里的一条 share（不含运行时连接状态）。
type persistedShare struct {
	ShareID            string       `json:"shareId"`
	SessionID          string       `json:"sessionId"`
	SessionTitle       string       `json:"sessionTitle"`
	Link               string       `json:"link"`
	SessionMode        string       `json:"sessionMode"`
	AccessMode         string       `json:"accessMode"`
	AllowedSubjectIDs  []string     `json:"allowedSubjectIds,omitempty"`
	AllowsFork         bool         `json:"allowsFork"`
	CreatedAt          time.Time    `json:"createdAt"`
	ExpiresAt          *time.Time   `json:"expiresAt,omitempty"`
	ViewerCapabilities []Capability `json:"viewerCapabilities,omitempty"`
}

type persistedState struct {
	Version int              `json:"version"`
	Shares  []persistedShare `json:"shares"`
}

// loadOrCreateOwnerToken 读现有的 owner token；没有就用 cfgToken（非空）或
// 新生成的随机 token 落盘。返回值同时用于鉴权与 CLI 提示。
func loadOrCreateOwnerToken(home, cfgToken string) (string, string, error) {
	path := filepath.Join(home, ownerTokenFile)
	raw, err := os.ReadFile(path)
	if err == nil {
		var doc ownerTokenDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			return "", path, fmt.Errorf("解析 %s 失败: %w", path, err)
		}
		if strings.TrimSpace(doc.Token) == "" {
			return "", path, fmt.Errorf("%s 里的 token 是空值", path)
		}
		return doc.Token, path, nil
	}
	if !os.IsNotExist(err) {
		return "", path, fmt.Errorf("读 %s 失败: %w", path, err)
	}

	token := strings.TrimSpace(cfgToken)
	if token == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return "", path, fmt.Errorf("生成 owner token 失败: %w", err)
		}
		token = hex.EncodeToString(buf)
	}
	doc := ownerTokenDoc{Token: token, CreatedAt: time.Now().UTC()}
	body, err := json.Marshal(doc)
	if err != nil {
		return "", path, err
	}
	if err := writeFileAtomic(path, body, 0o600); err != nil {
		return "", path, fmt.Errorf("写 %s 失败: %w", path, err)
	}
	return token, path, nil
}

// loadPersistedShares 读上次运行的 share 记录。没有文件就是空的。
func loadPersistedShares(home string) ([]persistedShare, error) {
	raw, err := os.ReadFile(filepath.Join(home, stateFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state persistedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", stateFile, err)
	}
	if state.Version != stateVersion {
		return nil, fmt.Errorf("%s 的 version=%d，本二进制只认 %d", stateFile, state.Version, stateVersion)
	}
	return state.Shares, nil
}

// savePersistedShares 原子写 share 记录（调用方持 mu.RLock 之外的快照）。
func savePersistedShares(home string, shares []persistedShare) error {
	state := persistedState{Version: stateVersion, Shares: shares}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(home, stateFile), append(body, '\n'), 0o600)
}

// appendAudit 把一条事件追加到 audit.jsonl（O_APPEND + fsync）。
func appendAudit(home string, event AuditEvent) error {
	body, err := marshalEvent(event)
	if err != nil {
		return err
	}
	path := filepath.Join(home, auditFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(body, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// loadAudit 读审计文件（坏行跳过但不会静默：调用方拿到 err 时保留已解析部分）。
func loadAudit(home string) ([]AuditEvent, error) {
	path := filepath.Join(home, auditFile)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var events []AuditEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var bad int
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var event AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			bad++
			continue
		}
		events = append(events, event)
	}
	if err := sc.Err(); err != nil {
		return events, err
	}
	if bad > 0 {
		return events, fmt.Errorf("%s 有 %d 行无法解析（已跳过）", auditFile, bad)
	}
	return events, nil
}

// writeFileAtomic 先写同目录临时文件再 rename，避免进程崩在写一半。
func writeFileAtomic(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// normalizeSubjectIDs 去空白、去重、排序；空串一律拒（允许列表里放空串
// 等于把"谁都不许"写成"某个无名的人也许"）。
func normalizeSubjectIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, errors.New("allowedSubjectIds 里有空串")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// newShareID 生成 relay 侧的真实 share id。前缀便于肉眼识别，随机体来自
// crypto/rand——**绝不由 host 侧本地编**（官方口径：编 shareId 就是假数据）。
func newShareID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "share-" + hex.EncodeToString(buf), nil
}

// newAuditID 生成审计事件 id（relay 内唯一）。
func newAuditID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "audit-" + hex.EncodeToString(buf), nil
}
