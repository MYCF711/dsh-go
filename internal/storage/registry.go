package storage

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Hub 是 `ctx.storage` 的 Go 等价物：一张 backend 名字表 + 一张 form 名字表。
//
// 它**不做 IO**，也不决定"全局用哪个后端"：多个后端并排挂着，哪个 domain
// 走哪个介质是 domain 层的 route 配置。注册与挂载都是 effect：返回的
// disposer 只撤掉这一次注册的贡献，不关闭后端（关闭由拥有它的插件负责）。
type Hub struct {
	mu       sync.RWMutex
	backends map[string]Backend
	forms    map[string]any
}

// NewHub 建一个空注册表。
func NewHub() *Hub {
	return &Hub{backends: map[string]Backend{}, forms: map[string]any{}}
}

// RegisterBackend 注册一个具名后端。同名注册是装配 bug，稳定码
// `duplicate-backend`。
func (h *Hub) RegisterBackend(name string, backend Backend) (func(), error) {
	if strings.TrimSpace(name) == "" {
		return nil, Errorf(CodeInvalidName, "storage backend 名字不能为空")
	}
	if backend == nil {
		return nil, Errorf(CodeInvalidSpec, "storage backend %q 是 nil", name)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.backends[name]; exists {
		return nil, Errorf(CodeDuplicateBackend, "storage backend %q 已经注册过了", name)
	}
	h.backends[name] = backend
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.backends[name] == backend {
			delete(h.backends, name)
		}
	}, nil
}

// Backend 按名字解析后端。未注册时稳定码 `backend-not-found`，错误信息里
// 列出已注册的名字（否则装配问题只能靠猜）。
func (h *Hub) Backend(name string) (Backend, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	backend, ok := h.backends[name]
	if !ok {
		return nil, Errorf(CodeBackendNotFound, "storage backend %q 没有注册（已注册: %s）",
			name, registeredNames(h.backends))
	}
	return backend, nil
}

// BackendNames 返回已注册后端名字（按字典序，便于断言与日志）。
func (h *Hub) BackendNames() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	names := make([]string, 0, len(h.backends))
	for name := range h.backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Mount 把一个数据表单挂到 hub 上。重复挂载稳定码 `duplicate-mount`。
func (h *Hub) Mount(form string, facility any) (func(), error) {
	if strings.TrimSpace(form) == "" {
		return nil, Errorf(CodeInvalidName, "storage form 名字不能为空")
	}
	if facility == nil {
		return nil, Errorf(CodeInvalidSpec, "storage form %q 是 nil", form)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.forms[form]; exists {
		return nil, Errorf(CodeDuplicateMount, "storage form %q 已经挂载过了", form)
	}
	h.forms[form] = facility
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.forms[form] == facility {
			delete(h.forms, form)
		}
	}, nil
}

// Form 按名字解析已挂载的数据表单。未挂载时稳定码 `form-not-mounted`。
func (h *Hub) Form(form string) (any, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	facility, ok := h.forms[form]
	if !ok {
		return nil, Errorf(CodeFormNotMounted, "storage form %q 没有挂载（装配顺序里先挂 domain 层）", form)
	}
	return facility, nil
}

// FormDomain 是 domain 数据表单的固定名字（官方 `ctx.storage.domain`）。
const FormDomain = "domain"

// Domain 解析已挂载的 domain 数据表单（即 *Facility）。
func (h *Hub) Domain() (*Facility, error) {
	raw, err := h.Form(FormDomain)
	if err != nil {
		return nil, err
	}
	facility, ok := raw.(*Facility)
	if !ok {
		return nil, Errorf(CodeInvalidSpec, "storage form %q 不是 *Facility，而是 %T", FormDomain, raw)
	}
	return facility, nil
}

// BackendServiceKey 是官方 `storageBackendServiceKey`：每个后端插件同时发布
// 一个只用于生命周期的服务键，数据表单注入它，从而"后端注册"先于"表单激活"。
func BackendServiceKey(name string) string { return "storage.backend." + name }

func registeredNames(backends map[string]Backend) string {
	if len(backends) == 0 {
		return "none"
	}
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("%v", names)
}
