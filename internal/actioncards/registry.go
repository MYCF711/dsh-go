package actioncards

import (
	"path/filepath"
	"strings"
	"sync"
)

// 进程内按 home 复用同一份 Service：工具注册（modules/tool/actioncards）
// 与官方 actionCards.act 适配器必须看到同一张 kind 注册表；各建一份会让
// "工具说 action 已注册、act 说 unavailable"。
var services sync.Map // absHome -> *Service

// Open 返回某个 home 的进程内 Service（首次调用时创建）。
func Open(home string) *Service {
	key := strings.TrimSpace(home)
	if key == "" {
		key = "."
	}
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	if existing, ok := services.Load(key); ok {
		return existing.(*Service)
	}
	created := New(key)
	actual, _ := services.LoadOrStore(key, created)
	return actual.(*Service)
}
