package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// 会话级 goal（当前这条会话在做的事）。
//
// 和 model/permission 的 store 同构，差别在**值是一段自由文本**：
// 模型是标识符、权限是枚举，而 goal 是一句话（可能是用户写的，
// 也可能是 agent 从 `update_goal` 里念出来的）。
//
// # 为什么还单独一个文件
//
// 同样是"两个界面动作不共享读-改-写路径"的理由。三个 store 各自一个 JSON，
// 出问题看一眼文件名就知道是谁的锅；合并成一个 `session-state.json`
// 会让"改权限时把 goal 覆盖掉"这种 bug 变得可能且难查。
type goalOverrideStore struct {
	mu   sync.Mutex
	path string
	m    map[string]string
}

func newGoalOverrideStore(home string) *goalOverrideStore {
	return &goalOverrideStore{
		path: filepath.Join(home, "state", "session-goal.json"),
		m:    map[string]string{},
	}
}

func (s *goalOverrideStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if m != nil {
		s.m = m
	}
	return nil
}

// Get 取一个会话的 goal（空串 = 没设过）。
func (s *goalOverrideStore) Get(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[sessionID]
}

// Set 设 goal。**空串 = 清除**（这条会话没有在做的目标）。
func (s *goalOverrideStore) Set(sessionID, goal string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if goal == "" {
		delete(s.m, sessionID)
	} else {
		s.m[sessionID] = goal
	}
	return s.flushLocked()
}

func (s *goalOverrideStore) flushLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
