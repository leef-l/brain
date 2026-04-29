package flow

import (
	"context"
	"errors"
	"io"
	"sync"
)

var (
	ErrPipeClosed    = errors.New("pipe is closed")
	ErrPipeExists    = errors.New("pipe already exists")
	ErrPipeNotFound  = errors.New("pipe not found")
)

// StreamBackend 是 streaming edge 的统一接口
type StreamBackend interface {
	// Write 写入一帧数据
	Write(ctx context.Context, data []byte) error
	// Read 读取一帧数据（阻塞直到有数据或 ctx 取消）
	Read(ctx context.Context) ([]byte, error)
	// Close 关闭 backend
	Close() error
}

// PipeBackend 是基于 Go channel 的 StreamBackend 实现
type PipeBackend struct {
	ch     chan []byte
	done   chan struct{}
	closed bool
	mu     sync.Mutex
}

// NewPipeBackend 创建一个带缓冲的 PipeBackend
func NewPipeBackend(bufSize int) *PipeBackend {
	if bufSize < 0 {
		bufSize = 0
	}
	return &PipeBackend{
		ch:   make(chan []byte, bufSize),
		done: make(chan struct{}),
	}
}

func (p *PipeBackend) Write(ctx context.Context, data []byte) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPipeClosed
	}
	p.mu.Unlock()

	cp := make([]byte, len(data))
	copy(cp, data)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ErrPipeClosed
	case p.ch <- cp:
		return nil
	}
}

func (p *PipeBackend) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data, ok := <-p.ch:
		if !ok {
			return nil, io.EOF
		}
		return data, nil
	case <-p.done:
		// drain remaining
		select {
		case data, ok := <-p.ch:
			if ok {
				return data, nil
			}
		default:
		}
		return nil, io.EOF
	}
}

func (p *PipeBackend) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	close(p.done)
	close(p.ch)
	return nil
}

// PipeRegistry 管理所有活跃的 pipe
type PipeRegistry struct {
	mu    sync.RWMutex
	pipes map[string]*PipeBackend
}

// NewPipeRegistry 创建新的 PipeRegistry
func NewPipeRegistry() *PipeRegistry {
	return &PipeRegistry{
		pipes: make(map[string]*PipeBackend),
	}
}

// Create 创建一个命名的 pipe
func (r *PipeRegistry) Create(name string, bufSize int) (*PipeBackend, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pipes[name]; exists {
		return nil, ErrPipeExists
	}
	p := NewPipeBackend(bufSize)
	r.pipes[name] = p
	return p, nil
}

// Get 获取一个命名的 pipe
func (r *PipeRegistry) Get(name string) (*PipeBackend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.pipes[name]
	return p, ok
}

// Close 关闭并移除一个命名的 pipe
func (r *PipeRegistry) Close(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pipes[name]
	if !ok {
		return ErrPipeNotFound
	}
	err := p.Close()
	delete(r.pipes, name)
	return err
}

// CloseAll 关闭并移除所有 pipe
func (r *PipeRegistry) CloseAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for name, p := range r.pipes {
		if err := p.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(r.pipes, name)
	}
	return firstErr
}

// Write 向指定 pipe 写入数据。若 pipe 不存在返回 ErrPipeNotFound。
func (r *PipeRegistry) Write(ctx context.Context, name string, data []byte) error {
	r.mu.RLock()
	p, ok := r.pipes[name]
	r.mu.RUnlock()
	if !ok {
		return ErrPipeNotFound
	}
	return p.Write(ctx, data)
}

// SetPipe 直接设置一个已存在的 pipe（覆盖模式）。用于跨组件共享 pipe 实例。
func (r *PipeRegistry) SetPipe(name string, p *PipeBackend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pipes == nil {
		r.pipes = make(map[string]*PipeBackend)
	}
	r.pipes[name] = p
}

// Names 返回所有活跃 pipe 的名称
func (r *PipeRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.pipes))
	for n := range r.pipes {
		names = append(names, n)
	}
	return names
}
