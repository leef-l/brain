package flow

import (
	"errors"
	"sync"
)

// EdgeType 标识 edge 类型
type EdgeType string

const (
	EdgeMaterialized EdgeType = "materialized"
	EdgeStreaming    EdgeType = "streaming"
)

var (
	ErrEdgeExists   = errors.New("edge already registered")
	ErrEdgeNotFound = errors.New("edge not found")
)

// EdgeDescriptor 描述一条 flow edge
type EdgeDescriptor struct {
	Name       string   // 唯一名称
	Type       EdgeType
	FromBrain  string   // 源 brain kind
	ToBrain    string   // 目标 brain kind
	DataSchema string   // 数据 schema 描述（可选）
}

// EdgeRegistry 管理所有已注册的 flow edge
type EdgeRegistry struct {
	mu    sync.RWMutex
	edges map[string]EdgeDescriptor
}

// NewEdgeRegistry 创建新的 EdgeRegistry
func NewEdgeRegistry() *EdgeRegistry {
	return &EdgeRegistry{
		edges: make(map[string]EdgeDescriptor),
	}
}

// Register 注册一条 edge
func (r *EdgeRegistry) Register(desc EdgeDescriptor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.edges[desc.Name]; exists {
		return ErrEdgeExists
	}
	r.edges[desc.Name] = desc
	return nil
}

// Get 获取一条 edge 的描述
func (r *EdgeRegistry) Get(name string) (*EdgeDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.edges[name]
	if !ok {
		return nil, false
	}
	return &d, true
}

// FindByBrain 查找连接到某 brain 的所有 edge（from 或 to）
func (r *EdgeRegistry) FindByBrain(brainKind string) []EdgeDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []EdgeDescriptor
	for _, d := range r.edges {
		if d.FromBrain == brainKind || d.ToBrain == brainKind {
			result = append(result, d)
		}
	}
	return result
}

// List 列出所有 edge
func (r *EdgeRegistry) List() []EdgeDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]EdgeDescriptor, 0, len(r.edges))
	for _, d := range r.edges {
		result = append(result, d)
	}
	return result
}

// Remove 移除一条 edge
func (r *EdgeRegistry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.edges, name)
}
