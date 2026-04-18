package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
)

// Ref 是内容的唯一标识符，格式 "sha256:<hex-digest>"
type Ref string

// Store 是 CAS 存储层的核心接口
type Store interface {
	// Put 写入内容，返回内容的 Ref
	Put(ctx context.Context, data []byte) (Ref, error)
	// Get 根据 Ref 读取内容
	Get(ctx context.Context, ref Ref) ([]byte, error)
	// Has 检查 Ref 是否存在
	Has(ctx context.Context, ref Ref) bool
	// Delete 删除指定 Ref 的内容
	Delete(ctx context.Context, ref Ref) error
	// List 列出所有 Ref
	List(ctx context.Context) ([]Ref, error)
}

// ComputeRef 计算数据的 sha256 Ref
func ComputeRef(data []byte) Ref {
	h := sha256.Sum256(data)
	return Ref(fmt.Sprintf("sha256:%s", hex.EncodeToString(h[:])))
}

var (
	ErrRefNotFound = errors.New("ref not found")
)

// MemStore 是 Store 的内存实现
type MemStore struct {
	mu   sync.RWMutex
	data map[Ref][]byte
}

// NewMemStore 创建一个新的内存 CAS 存储
func NewMemStore() *MemStore {
	return &MemStore{
		data: make(map[Ref][]byte),
	}
}

func (s *MemStore) Put(ctx context.Context, data []byte) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ref := ComputeRef(data)
	cp := make([]byte, len(data))
	copy(cp, data)

	s.mu.Lock()
	s.data[ref] = cp
	s.mu.Unlock()
	return ref, nil
}

func (s *MemStore) Get(ctx context.Context, ref Ref) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	d, ok := s.data[ref]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrRefNotFound
	}
	cp := make([]byte, len(d))
	copy(cp, d)
	return cp, nil
}

func (s *MemStore) Has(ctx context.Context, ref Ref) bool {
	s.mu.RLock()
	_, ok := s.data[ref]
	s.mu.RUnlock()
	return ok
}

func (s *MemStore) Delete(ctx context.Context, ref Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.data, ref)
	s.mu.Unlock()
	return nil
}

func (s *MemStore) List(ctx context.Context) ([]Ref, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	refs := make([]Ref, 0, len(s.data))
	for r := range s.data {
		refs = append(refs, r)
	}
	s.mu.RUnlock()
	return refs, nil
}
