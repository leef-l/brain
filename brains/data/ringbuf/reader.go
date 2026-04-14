package ringbuf

import "sync"

// Reader -- Ring Buffer 只读端，Quant Brain 用这个读取数据
type Reader struct {
	buffer  *RingBuffer
	lastSeq uint64
}

// NewReader 创建只读端
func NewReader(buffer *RingBuffer) *Reader {
	return &Reader{buffer: buffer}
}

// Latest 读最新 snapshot，同时更新 lastSeq
func (r *Reader) Latest() (MarketSnapshot, bool) {
	snap, ok := r.buffer.Latest()
	if ok {
		r.lastSeq = snap.SeqNum
	}
	return snap, ok
}

// HasNew 是否有新数据
func (r *Reader) HasNew() bool {
	return r.buffer.WriteSeq() > r.lastSeq
}

// ReadSince 读取自上次 Read 以来的所有新数据（按序号顺序）
// 如果落后太多（> size），只返回最新 size 个
// 更新 lastSeq
func (r *Reader) ReadSince() ([]MarketSnapshot, bool) {
	r.buffer.mu.RLock()
	currentSeq := r.buffer.writeSeq
	if currentSeq == 0 || currentSeq <= r.lastSeq {
		r.buffer.mu.RUnlock()
		return nil, false
	}

	size := uint64(r.buffer.size)
	startSeq := r.lastSeq + 1

	// 如果落后太多，只返回最新 size 个
	if currentSeq-r.lastSeq > size {
		startSeq = currentSeq - size + 1
	}

	result := r.buffer.readSlots(startSeq, currentSeq)
	r.buffer.mu.RUnlock()

	r.lastSeq = currentSeq
	return result, len(result) > 0
}

// MultiReader -- 同时读取多品种
type MultiReader struct {
	manager *BufferManager
	readers map[string]*Reader
	mu      sync.RWMutex
}

// NewMultiReader 创建多品种读取端
func NewMultiReader(manager *BufferManager) *MultiReader {
	return &MultiReader{
		manager: manager,
		readers: make(map[string]*Reader),
	}
}

// getReader 获取或创建指定品种的 Reader
func (mr *MultiReader) getReader(instID string) *Reader {
	mr.mu.RLock()
	rd, ok := mr.readers[instID]
	mr.mu.RUnlock()
	if ok {
		return rd
	}

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if rd, ok = mr.readers[instID]; ok {
		return rd
	}
	buf := mr.manager.GetOrCreate(instID)
	rd = NewReader(buf)
	mr.readers[instID] = rd
	return rd
}

// Latest 读取指定品种最新 snapshot，自动创建 Reader
func (mr *MultiReader) Latest(instID string) (MarketSnapshot, bool) {
	return mr.getReader(instID).Latest()
}

// LatestAll 读取所有品种的最新 snapshot
func (mr *MultiReader) LatestAll() map[string]MarketSnapshot {
	instruments := mr.manager.Instruments()
	result := make(map[string]MarketSnapshot, len(instruments))
	for _, id := range instruments {
		if snap, ok := mr.getReader(id).Latest(); ok {
			result[id] = snap
		}
	}
	return result
}

// HasNew 指定品种是否有新数据
func (mr *MultiReader) HasNew(instID string) bool {
	return mr.getReader(instID).HasNew()
}
