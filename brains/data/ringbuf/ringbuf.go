package ringbuf

import (
	"sort"
	"sync"
)

// RingBuffer -- 单品种的环形缓冲区
// 一写多读，RWMutex 保护并发访问
type RingBuffer struct {
	slots    []MarketSnapshot
	size     int
	writeSeq uint64
	mu       sync.RWMutex
}

// New 创建指定槽位数的 RingBuffer，默认 1024
func New(size int) *RingBuffer {
	if size <= 0 {
		size = 1024
	}
	return &RingBuffer{
		slots: make([]MarketSnapshot, size),
		size:  size,
	}
}

// Write 写入一个 snapshot，线程安全
func (rb *RingBuffer) Write(snap MarketSnapshot) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.writeSeq++
	snap.SeqNum = rb.writeSeq
	idx := rb.writeSeq % uint64(rb.size)
	rb.slots[idx] = snap
}

// Latest 读最新的 snapshot
// 如果无数据返回 false
func (rb *RingBuffer) Latest() (MarketSnapshot, bool) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	if rb.writeSeq == 0 {
		return MarketSnapshot{}, false
	}
	idx := rb.writeSeq % uint64(rb.size)
	return rb.slots[idx], true
}

// WriteSeq 返回当前写序号
func (rb *RingBuffer) WriteSeq() uint64 {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.writeSeq
}

// readSlots 内部方法，在持有读锁时读取指定范围的 slots
// 调用者必须持有 mu.RLock
func (rb *RingBuffer) readSlots(startSeq, endSeq uint64) []MarketSnapshot {
	size := uint64(rb.size)
	result := make([]MarketSnapshot, 0, endSeq-startSeq+1)
	for seq := startSeq; seq <= endSeq; seq++ {
		idx := seq % size
		snap := rb.slots[idx]
		if snap.SeqNum == seq {
			result = append(result, snap)
		}
	}
	return result
}

// Size 返回槽位数
func (rb *RingBuffer) Size() int {
	return rb.size
}

// BufferManager -- 管理多品种的 Ring Buffer
type BufferManager struct {
	buffers map[string]*RingBuffer
	size    int
	mu      sync.RWMutex
}

// NewBufferManager 创建 BufferManager，slotCount 为每个 buffer 的槽位数
func NewBufferManager(slotCount int) *BufferManager {
	if slotCount <= 0 {
		slotCount = 1024
	}
	return &BufferManager{
		buffers: make(map[string]*RingBuffer),
		size:    slotCount,
	}
}

// GetOrCreate 获取或创建品种对应的 RingBuffer
func (m *BufferManager) GetOrCreate(instID string) *RingBuffer {
	m.mu.RLock()
	buf, ok := m.buffers[instID]
	m.mu.RUnlock()
	if ok {
		return buf
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// double check
	if buf, ok = m.buffers[instID]; ok {
		return buf
	}
	buf = New(m.size)
	m.buffers[instID] = buf
	return buf
}

// Write 写入指定品种的 snapshot
func (m *BufferManager) Write(instID string, snap MarketSnapshot) {
	buf := m.GetOrCreate(instID)
	snap.InstID = instID
	buf.Write(snap)
}

// Latest 读取指定品种的最新 snapshot
func (m *BufferManager) Latest(instID string) (MarketSnapshot, bool) {
	m.mu.RLock()
	buf, ok := m.buffers[instID]
	m.mu.RUnlock()
	if !ok {
		return MarketSnapshot{}, false
	}
	return buf.Latest()
}

// Instruments 返回所有已注册品种列表（排序）
func (m *BufferManager) Instruments() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.buffers))
	for id := range m.buffers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Count 返回已注册品种数量
func (m *BufferManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.buffers)
}
