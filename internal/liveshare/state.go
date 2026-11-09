package liveshare

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	stateFile    = "owner-shares.json"
	stateVersion = 1
)

// StoredShare 是 owner-shares.json 里的一条分享。
//
// 文件名与官方默认同名（$DSH_HOME/liveshare/owner-shares.json）；字段是官方
// OwnerShareView + access + relay 地址。relay 地址随记录保存，避免一个 home
// 从 A relay 切到 B relay 后把 A 的 share 当成本地记录去停。
type StoredShare struct {
	ShareID            string       `json:"shareId"`
	SessionID          string       `json:"sessionId"`
	SessionTitle       string       `json:"sessionTitle"`
	Link               string       `json:"link"`
	CreatedAt          time.Time    `json:"createdAt"`
	ExpiresAt          *time.Time   `json:"expiresAt,omitempty"`
	AllowsFork         bool         `json:"allowsFork"`
	SessionMode        string       `json:"sessionMode"`
	ViewerCapabilities []Capability `json:"viewerCapabilities"`
	Access             Access       `json:"access"`
	Relay              string       `json:"relay"`
}

func (s StoredShare) view() ShareView {
	access := s.Access
	return ShareView{
		ShareID:            s.ShareID,
		SessionID:          s.SessionID,
		SessionTitle:       s.SessionTitle,
		Link:               s.Link,
		CreatedAt:          s.CreatedAt,
		ExpiresAt:          s.ExpiresAt,
		AllowsFork:         s.AllowsFork,
		SessionMode:        s.SessionMode,
		ViewerCapabilities: append([]Capability{}, s.ViewerCapabilities...),
		Access:             &access,
	}
}

// State 是 owner-shares.json 的完整内容。
type State struct {
	Version       int           `json:"version"`
	RelayOverride string        `json:"relayOverride,omitempty"`
	Shares        []StoredShare `json:"shares"`
	PendingStops  []string      `json:"pendingStops,omitempty"`
}

// statePath 返回 owner 状态文件路径（<home>/liveshare/owner-shares.json）。
func statePath(home string) string {
	return filepath.Join(home, "liveshare", stateFile)
}

func loadState(home string) (State, error) {
	path := statePath(home)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{Version: stateVersion}, nil
		}
		return State{}, fmt.Errorf("读 %s 失败: %w", path, err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	if state.Version != stateVersion {
		return State{}, fmt.Errorf("%s 的 version=%d，本二进制只认 %d", path, state.Version, stateVersion)
	}
	return state, nil
}

func (s *Service) load() (State, error) { return loadState(s.home) }

// save 原子写 owner-shares.json（0600）。
func (s *Service) save(state State) error {
	state.Version = stateVersion
	if state.Shares == nil {
		state.Shares = []StoredShare{}
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := statePath(s.home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), stateFile+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
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

// findShare 返回 shares 里 shareId 对应的下标，找不到 -1。
func findShare(shares []StoredShare, shareID string) int {
	for i := range shares {
		if shares[i].ShareID == shareID {
			return i
		}
	}
	return -1
}
