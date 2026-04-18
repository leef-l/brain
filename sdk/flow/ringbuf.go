package flow

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

var (
	ErrRingBufClosed = errors.New("ring buffer is closed")
	ErrFrameTooLarge = errors.New("frame too large for ring buffer")
)

// SpillFunc 是 ring buffer 满时的溢出处理函数。
// 返回 error 则 Write 返回该 error。
type SpillFunc func(data []byte) error

// RingBufConfig 配置
type RingBufConfig struct {
	Size      int       // 缓冲区大小（字节），必须是 2 的幂，不是则向上取整
	SpillFunc SpillFunc // 缓冲区满时的降级回调，nil 则 Write 阻塞等待
}

// RingBufStats 返回环形缓冲区的运行统计
type RingBufStats struct {
	TotalWritten int64 // 累计写入帧数
	TotalRead    int64 // 累计读取帧数
	SpillCount   int64 // 降级触发次数
	BufferUsed   int   // 当前缓冲区已用字节
	BufferSize   int   // 缓冲区总大小
}

// RingBufBackend 是基于内存环形缓冲区的高性能 StreamBackend 实现。
// 用于同进程内的跨脑高速数据传递（替代 channel 的性能上限场景）。
//
// 特性：
//   - 固定大小的环形缓冲区（预分配，零 GC 压力）
//   - 单生产者单消费者（SPSC）设计
//   - 帧格式：[4 bytes len][payload]
//   - 写满时触发 SpillFunc 降级回调（如果配置了的话）
type RingBufBackend struct {
	buf      []byte
	size     int // 缓冲区大小，2 的幂
	mask     int // size - 1，用于取模
	writePos int
	readPos  int

	mu       sync.Mutex
	notEmpty *sync.Cond // 通知消费者有新数据
	notFull  *sync.Cond // 通知生产者有空间

	closed    bool
	spillFunc SpillFunc

	totalWritten atomic.Int64
	totalRead    atomic.Int64
	spillCount   atomic.Int64
}

// nextPowerOf2 将 n 向上取整到 2 的幂
func nextPowerOf2(n int) int {
	if n <= 0 {
		return 1
	}
	// 已经是 2 的幂
	if n&(n-1) == 0 {
		return n
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	n++
	return n
}

// NewRingBufBackend 创建一个新的 RingBufBackend
func NewRingBufBackend(cfg RingBufConfig) (*RingBufBackend, error) {
	size := cfg.Size
	if size <= 0 {
		size = 4096
	}
	size = nextPowerOf2(size)

	r := &RingBufBackend{
		buf:       make([]byte, size),
		size:      size,
		mask:      size - 1,
		spillFunc: cfg.SpillFunc,
	}
	r.notEmpty = sync.NewCond(&r.mu)
	r.notFull = sync.NewCond(&r.mu)
	return r, nil
}

// used 返回当前已使用字节数（调用者须持有锁）
func (r *RingBufBackend) used() int {
	return r.writePos - r.readPos
}

// free 返回当前可用字节数（调用者须持有锁）
func (r *RingBufBackend) free() int {
	return r.size - r.used()
}

// writeBytes 将 data 写入环形缓冲区（调用者须持有锁且已确认空间足够）
func (r *RingBufBackend) writeBytes(data []byte) {
	for len(data) > 0 {
		pos := r.writePos & r.mask
		end := r.size
		if pos+len(data) < end {
			end = pos + len(data)
		}
		n := copy(r.buf[pos:end], data)
		r.writePos += n
		data = data[n:]
	}
}

// readBytes 从环形缓冲区读取 n 字节（调用者须持有锁且已确认数据足够）
func (r *RingBufBackend) readBytes(n int) []byte {
	out := make([]byte, n)
	dst := out
	for len(dst) > 0 {
		pos := r.readPos & r.mask
		end := r.size
		if pos+len(dst) < end {
			end = pos + len(dst)
		}
		copied := copy(dst, r.buf[pos:end])
		r.readPos += copied
		dst = dst[copied:]
	}
	return out
}

const frameHeaderSize = 4

// Write 写入一帧数据
func (r *RingBufBackend) Write(ctx context.Context, data []byte) error {
	frameSize := frameHeaderSize + len(data)

	if frameSize > r.size {
		return ErrFrameTooLarge
	}

	r.mu.Lock()

	if r.closed {
		r.mu.Unlock()
		return ErrRingBufClosed
	}

	// 等待足够空间
	for r.free() < frameSize {
		if r.closed {
			r.mu.Unlock()
			return ErrRingBufClosed
		}

		// 检查 context
		if err := ctx.Err(); err != nil {
			r.mu.Unlock()
			return err
		}

		if r.spillFunc != nil {
			sf := r.spillFunc
			r.mu.Unlock()
			r.spillCount.Add(1)
			if err := sf(data); err != nil {
				return err
			}
			r.totalWritten.Add(1)
			return nil
		}

		// 没有 spillFunc，阻塞等待空间
		// 使用 goroutine 来监听 context 取消
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				r.mu.Lock()
				r.notFull.Broadcast()
				r.mu.Unlock()
			case <-done:
			}
		}()

		r.notFull.Wait()
		close(done)

		if err := ctx.Err(); err != nil {
			r.mu.Unlock()
			return err
		}
	}

	// 写入帧头
	var header [frameHeaderSize]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(data)))
	r.writeBytes(header[:])
	// 写入 payload
	r.writeBytes(data)

	r.mu.Unlock()
	r.totalWritten.Add(1)
	r.notEmpty.Broadcast()
	return nil
}

// Read 读取一帧数据（阻塞直到有数据或 ctx 取消）
func (r *RingBufBackend) Read(ctx context.Context) ([]byte, error) {
	r.mu.Lock()

	for r.used() < frameHeaderSize {
		if r.closed && r.used() == 0 {
			r.mu.Unlock()
			return nil, io.EOF
		}

		if err := ctx.Err(); err != nil {
			r.mu.Unlock()
			return nil, err
		}

		if r.closed {
			// closed but not enough data for a frame
			r.mu.Unlock()
			return nil, io.EOF
		}

		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				r.mu.Lock()
				r.notEmpty.Broadcast()
				r.mu.Unlock()
			case <-done:
			}
		}()

		r.notEmpty.Wait()
		close(done)

		if err := ctx.Err(); err != nil {
			r.mu.Unlock()
			return nil, err
		}
	}

	// 读取帧头
	headerBytes := r.readBytes(frameHeaderSize)
	payloadLen := int(binary.LittleEndian.Uint32(headerBytes))

	// 等待完整 payload（正常情况下 writer 保证原子写入，但为安全起见）
	for r.used() < payloadLen {
		if r.closed && r.used() < payloadLen {
			r.mu.Unlock()
			return nil, io.EOF
		}
		r.notEmpty.Wait()
	}

	payload := r.readBytes(payloadLen)
	r.mu.Unlock()

	r.totalRead.Add(1)
	r.notFull.Broadcast()
	return payload, nil
}

// Close 关闭 backend
func (r *RingBufBackend) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.notEmpty.Broadcast()
	r.notFull.Broadcast()
	return nil
}

// Stats 返回环形缓冲区的运行统计
func (r *RingBufBackend) Stats() RingBufStats {
	r.mu.Lock()
	used := r.used()
	size := r.size
	r.mu.Unlock()

	return RingBufStats{
		TotalWritten: r.totalWritten.Load(),
		TotalRead:    r.totalRead.Load(),
		SpillCount:   r.spillCount.Load(),
		BufferUsed:   used,
		BufferSize:   size,
	}
}
