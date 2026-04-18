package flow

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewRingBufBackendPowerOf2(t *testing.T) {
	tests := []struct {
		input    int
		expected int
	}{
		{0, 4096},   // 默认值
		{-1, 4096},  // 负值用默认
		{1, 1},      // 已经是 2 的幂
		{2, 2},      // 已经是 2 的幂
		{3, 4},      // 向上取整
		{5, 8},      // 向上取整
		{100, 128},  // 向上取整
		{1024, 1024}, // 已经是 2 的幂
		{1025, 2048}, // 向上取整
	}

	for _, tt := range tests {
		r, err := NewRingBufBackend(RingBufConfig{Size: tt.input})
		if err != nil {
			t.Fatalf("NewRingBufBackend(Size=%d): %v", tt.input, err)
		}
		stats := r.Stats()
		if stats.BufferSize != tt.expected {
			t.Errorf("Size=%d: got BufferSize=%d, want %d", tt.input, stats.BufferSize, tt.expected)
		}
		r.Close()
	}
}

func TestRingBufWriteReadSingleFrame(t *testing.T) {
	ctx := context.Background()
	r, _ := NewRingBufBackend(RingBufConfig{Size: 256})
	defer r.Close()

	data := []byte("hello ring buffer")
	if err := r.Write(ctx, data); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := r.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Read got %q, want %q", got, data)
	}
}

func TestRingBufWriteReadMultiFrame(t *testing.T) {
	ctx := context.Background()
	r, _ := NewRingBufBackend(RingBufConfig{Size: 1024})
	defer r.Close()

	frames := []string{"frame-0", "frame-1", "frame-2", "frame-3", "frame-4"}
	for _, f := range frames {
		if err := r.Write(ctx, []byte(f)); err != nil {
			t.Fatalf("Write(%s): %v", f, err)
		}
	}

	for _, want := range frames {
		got, err := r.Read(ctx)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(got) != want {
			t.Fatalf("Read: got %q, want %q", got, want)
		}
	}
}

func TestRingBufWrapAround(t *testing.T) {
	ctx := context.Background()
	// 小缓冲区，强制 wrap around
	// 每帧 = 4(header) + 8(payload) = 12 bytes，缓冲区 32 bytes 可容纳 2 帧
	r, _ := NewRingBufBackend(RingBufConfig{Size: 32})
	defer r.Close()

	for i := 0; i < 10; i++ {
		data := fmt.Sprintf("wrap-%03d", i)
		if err := r.Write(ctx, []byte(data)); err != nil {
			t.Fatalf("Write(%d): %v", i, err)
		}
		got, err := r.Read(ctx)
		if err != nil {
			t.Fatalf("Read(%d): %v", i, err)
		}
		if string(got) != data {
			t.Fatalf("iteration %d: got %q, want %q", i, got, data)
		}
	}
}

func TestRingBufSpillFunc(t *testing.T) {
	var spilledData [][]byte
	var mu sync.Mutex

	spill := func(data []byte) error {
		mu.Lock()
		cp := make([]byte, len(data))
		copy(cp, data)
		spilledData = append(spilledData, cp)
		mu.Unlock()
		return nil
	}

	// 16 字节缓冲区，只够放 1 帧（4+8=12）
	r, _ := NewRingBufBackend(RingBufConfig{Size: 16, SpillFunc: spill})
	defer r.Close()

	ctx := context.Background()

	// 第一帧写入缓冲区
	if err := r.Write(ctx, []byte("12345678")); err != nil {
		t.Fatalf("Write 1: %v", err)
	}

	// 第二帧应触发 spill（缓冲区满）
	if err := r.Write(ctx, []byte("overflow")); err != nil {
		t.Fatalf("Write 2: %v", err)
	}

	stats := r.Stats()
	if stats.SpillCount != 1 {
		t.Fatalf("SpillCount: got %d, want 1", stats.SpillCount)
	}

	mu.Lock()
	if len(spilledData) != 1 || string(spilledData[0]) != "overflow" {
		t.Fatalf("spilledData: got %v", spilledData)
	}
	mu.Unlock()

	// 仍然能从缓冲区读到第一帧
	got, err := r.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "12345678" {
		t.Fatalf("Read: got %q, want %q", got, "12345678")
	}
}

func TestRingBufSpillFuncError(t *testing.T) {
	spillErr := fmt.Errorf("spill failed")
	spill := func(data []byte) error {
		return spillErr
	}

	r, _ := NewRingBufBackend(RingBufConfig{Size: 16, SpillFunc: spill})
	defer r.Close()

	ctx := context.Background()
	// 填满缓冲区
	r.Write(ctx, []byte("12345678"))
	// 第二次写应该返回 spill error
	err := r.Write(ctx, []byte("overflow"))
	if err != spillErr {
		t.Fatalf("Write: got %v, want %v", err, spillErr)
	}
}

func TestRingBufCloseReadRemaining(t *testing.T) {
	ctx := context.Background()
	r, _ := NewRingBufBackend(RingBufConfig{Size: 256})

	// 写入两帧后关闭
	r.Write(ctx, []byte("remain-1"))
	r.Write(ctx, []byte("remain-2"))
	r.Close()

	// 应该能读出剩余数据
	got1, err := r.Read(ctx)
	if err != nil {
		t.Fatalf("Read 1 after Close: %v", err)
	}
	if string(got1) != "remain-1" {
		t.Fatalf("Read 1: got %q", got1)
	}

	got2, err := r.Read(ctx)
	if err != nil {
		t.Fatalf("Read 2 after Close: %v", err)
	}
	if string(got2) != "remain-2" {
		t.Fatalf("Read 2: got %q", got2)
	}

	// 下一次读应返回 EOF
	_, err = r.Read(ctx)
	if err != io.EOF {
		t.Fatalf("Read 3 after Close: expected io.EOF, got %v", err)
	}
}

func TestRingBufCloseWriteError(t *testing.T) {
	r, _ := NewRingBufBackend(RingBufConfig{Size: 256})
	r.Close()

	ctx := context.Background()
	err := r.Write(ctx, []byte("after close"))
	if err != ErrRingBufClosed {
		t.Fatalf("Write after Close: expected ErrRingBufClosed, got %v", err)
	}
}

func TestRingBufStatsAccuracy(t *testing.T) {
	ctx := context.Background()
	r, _ := NewRingBufBackend(RingBufConfig{Size: 1024})
	defer r.Close()

	// 初始状态
	stats := r.Stats()
	if stats.TotalWritten != 0 || stats.TotalRead != 0 || stats.SpillCount != 0 {
		t.Fatalf("initial stats should be zero: %+v", stats)
	}
	if stats.BufferSize != 1024 {
		t.Fatalf("BufferSize: got %d, want 1024", stats.BufferSize)
	}

	// 写入 3 帧
	for i := 0; i < 3; i++ {
		r.Write(ctx, []byte(fmt.Sprintf("s%d", i)))
	}
	stats = r.Stats()
	if stats.TotalWritten != 3 {
		t.Fatalf("TotalWritten: got %d, want 3", stats.TotalWritten)
	}
	if stats.BufferUsed == 0 {
		t.Fatal("BufferUsed should be > 0 after writes")
	}

	// 读取 2 帧
	r.Read(ctx)
	r.Read(ctx)
	stats = r.Stats()
	if stats.TotalRead != 2 {
		t.Fatalf("TotalRead: got %d, want 2", stats.TotalRead)
	}

	// 读最后一帧
	r.Read(ctx)
	stats = r.Stats()
	if stats.TotalRead != 3 {
		t.Fatalf("TotalRead: got %d, want 3", stats.TotalRead)
	}
	if stats.BufferUsed != 0 {
		t.Fatalf("BufferUsed: got %d, want 0", stats.BufferUsed)
	}
}

func TestRingBufConcurrentProducerConsumer(t *testing.T) {
	ctx := context.Background()
	r, _ := NewRingBufBackend(RingBufConfig{Size: 1024})

	const numFrames = 500
	var wg sync.WaitGroup

	// 生产者
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numFrames; i++ {
			data := fmt.Sprintf("msg-%04d", i)
			if err := r.Write(ctx, []byte(data)); err != nil {
				t.Errorf("Write(%d): %v", i, err)
				return
			}
		}
		r.Close()
	}()

	// 消费者
	received := make([]string, 0, numFrames)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			data, err := r.Read(ctx)
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Errorf("Read: %v", err)
				return
			}
			received = append(received, string(data))
		}
	}()

	wg.Wait()

	if len(received) != numFrames {
		t.Fatalf("received %d frames, want %d", len(received), numFrames)
	}
	for i, got := range received {
		want := fmt.Sprintf("msg-%04d", i)
		if got != want {
			t.Fatalf("frame %d: got %q, want %q", i, got, want)
		}
	}
}

func TestRingBufContextCancel(t *testing.T) {
	r, _ := NewRingBufBackend(RingBufConfig{Size: 16})
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// 填满缓冲区
	r.Write(ctx, []byte("12345678")) // 4+8=12 字节

	// 取消 context
	cancel()

	// Write 应该返回 context error
	err := r.Write(ctx, []byte("blocked"))
	if err != context.Canceled {
		t.Fatalf("Write with cancelled ctx: expected context.Canceled, got %v", err)
	}

	// 用新的已取消 context 测试 Read
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()

	// 先读掉缓冲区里的数据
	bgCtx := context.Background()
	r.Read(bgCtx)

	// 缓冲区为空时用取消的 context 读
	_, err = r.Read(ctx2)
	if err != context.Canceled {
		t.Fatalf("Read with cancelled ctx: expected context.Canceled, got %v", err)
	}
}

func TestRingBufFrameTooLarge(t *testing.T) {
	r, _ := NewRingBufBackend(RingBufConfig{Size: 16})
	defer r.Close()

	ctx := context.Background()
	// 帧 = 4(header) + 20(payload) = 24 > 16
	err := r.Write(ctx, make([]byte, 20))
	if err != ErrFrameTooLarge {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestRingBufImplementsStreamBackend(t *testing.T) {
	r, _ := NewRingBufBackend(RingBufConfig{Size: 64})
	var _ StreamBackend = r // 编译时检查接口实现
	r.Close()
}

func TestRingBufReadBlocksUntilWrite(t *testing.T) {
	r, _ := NewRingBufBackend(RingBufConfig{Size: 256})
	defer r.Close()

	var got atomic.Value

	go func() {
		time.Sleep(50 * time.Millisecond)
		r.Write(context.Background(), []byte("delayed"))
	}()

	data, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got.Store(string(data))
	if got.Load().(string) != "delayed" {
		t.Fatalf("got %q, want %q", got.Load(), "delayed")
	}
}

func TestRingBufWriteBlocksUntilRead(t *testing.T) {
	// 小缓冲区，写两帧就满了
	r, _ := NewRingBufBackend(RingBufConfig{Size: 16})
	defer r.Close()

	ctx := context.Background()
	// 第一帧占 4+4=8 字节
	r.Write(ctx, []byte("aaaa"))

	done := make(chan error, 1)
	go func() {
		// 这个写入应该阻塞（缓冲区不够放第二个帧 4+4=8，只剩8但刚好够）
		// 写入第二帧
		done <- r.Write(ctx, []byte("bbbb"))
	}()

	// 读出第一帧以腾出空间
	time.Sleep(50 * time.Millisecond)
	r.Read(ctx)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked Write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not unblock after Read")
	}
}

func TestRingBufCloseUnblocksRead(t *testing.T) {
	r, _ := NewRingBufBackend(RingBufConfig{Size: 256})

	done := make(chan error, 1)
	go func() {
		_, err := r.Read(context.Background())
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	r.Close()

	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("Read after Close: expected io.EOF, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not unblock after Close")
	}
}

func TestRingBufEmptyFrame(t *testing.T) {
	ctx := context.Background()
	r, _ := NewRingBufBackend(RingBufConfig{Size: 64})
	defer r.Close()

	// 写入空帧
	if err := r.Write(ctx, []byte{}); err != nil {
		t.Fatalf("Write empty: %v", err)
	}

	got, err := r.Read(ctx)
	if err != nil {
		t.Fatalf("Read empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty frame, got %d bytes", len(got))
	}
}
