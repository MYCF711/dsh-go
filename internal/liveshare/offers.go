package liveshare

import (
	"sort"
	"sync"
)

// Registry 是"插件声明的能力"这一条真实状态。
//
// capabilities() 必须读实时清单，setCapabilities() 只能从清单里选——所以这里
// 不是一个空数组占位，而是宿主接线时由插件加载器注册进来的注册表。没有插件
// 注册时清单为空、设置能力会被结构化拒绝（而不是假装设置成功）。
type Registry struct {
	mu     sync.RWMutex
	offers []Capability
}

// NewRegistry 造一个空注册表。
func NewRegistry() *Registry { return &Registry{} }

// Register 注册/覆盖一个插件的能力声明。
func (r *Registry) Register(capability Capability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.offers {
		if r.offers[i].Plugin == capability.Plugin {
			r.offers[i] = normalizeCapability(capability)
			return
		}
	}
	r.offers = append(r.offers, normalizeCapability(capability))
}

// Unregister 移除一个插件的声明。
func (r *Registry) Unregister(plugin string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.offers[:0]
	for _, offer := range r.offers {
		if offer.Plugin != plugin {
			out = append(out, offer)
		}
	}
	r.offers = out
}

// List 返回实时清单（按插件名排序，调用方拿到拷贝）。
func (r *Registry) List() []Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Capability, len(r.offers))
	for i, offer := range r.offers {
		out[i] = Capability{
			Plugin:   offer.Plugin,
			Readable: append([]string{}, offer.Readable...),
			Writable: append([]string{}, offer.Writable...),
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	return out
}

// ValidateSelection 校验一份 share 能力选择是否是"插件真提供了的端点"。
//
// 规则：插件必须在册；readable/writable 里的每个端点必须出现在该插件的
// 对应列表里。空选择永远合法（= 什么都不暴露）。
func (r *Registry) ValidateSelection(selected []Capability) error {
	offers := map[string]Capability{}
	for _, offer := range r.List() {
		offers[offer.Plugin] = offer
	}
	for _, want := range selected {
		offer, ok := offers[want.Plugin]
		if !ok {
			return &SelectionError{Plugin: want.Plugin}
		}
		for _, endpoint := range want.Readable {
			if !contains(offer.Readable, endpoint) {
				return &SelectionError{Plugin: want.Plugin, Endpoint: endpoint, Kind: "readable"}
			}
		}
		for _, endpoint := range want.Writable {
			if !contains(offer.Writable, endpoint) {
				return &SelectionError{Plugin: want.Plugin, Endpoint: endpoint, Kind: "writable"}
			}
		}
	}
	return nil
}

// SelectionError 表示选中了插件没声明的能力。
type SelectionError struct {
	Plugin   string
	Endpoint string
	Kind     string
}

func (e *SelectionError) Error() string {
	if e.Endpoint == "" {
		return "插件 " + e.Plugin + " 没有声明任何 liveshare 能力"
	}
	return "插件 " + e.Plugin + " 没有声明 " + e.Kind + " 端点 " + e.Endpoint
}

func normalizeCapability(in Capability) Capability {
	readable := append([]string{}, in.Readable...)
	writable := append([]string{}, in.Writable...)
	sort.Strings(readable)
	sort.Strings(writable)
	return Capability{Plugin: in.Plugin, Readable: readable, Writable: writable}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
