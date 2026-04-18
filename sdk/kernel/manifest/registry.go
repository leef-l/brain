package manifest

import (
	"fmt"
	"sync"
)

// Registry 是基于内存的 Manifest 注册表，线程安全
type Registry struct {
	mu    sync.RWMutex
	items map[string]*Manifest // key = kind
}

// NewRegistry 创建空注册表
func NewRegistry() *Registry {
	return &Registry{
		items: make(map[string]*Manifest),
	}
}

// Register 注册一个 Manifest，kind 重复则返回错误
func (r *Registry) Register(m *Manifest) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.items[m.Kind]; exists {
		return fmt.Errorf("manifest: kind %q 已注册", m.Kind)
	}
	r.items[m.Kind] = m
	return nil
}

// Get 根据 kind 获取 Manifest
func (r *Registry) Get(kind string) (*Manifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	m, ok := r.items[kind]
	return m, ok
}

// List 返回所有已注册的 Manifest
func (r *Registry) List() []*Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Manifest, 0, len(r.items))
	for _, m := range r.items {
		result = append(result, m)
	}
	return result
}

// Remove 移除指定 kind 的 Manifest
func (r *Registry) Remove(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.items, kind)
}

// FindByCapability 查找所有包含指定 capability 的 Manifest
func (r *Registry) FindByCapability(cap string) []*Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Manifest
	for _, m := range r.items {
		for _, c := range m.Capabilities {
			if c == cap {
				result = append(result, m)
				break
			}
		}
	}
	return result
}
