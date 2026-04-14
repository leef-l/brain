package ringbuf

import (
	"fmt"
	"sync"
	"testing"
)

func TestBasicReadWrite(t *testing.T) {
	rb := New(1024)
	snap := MarketSnapshot{InstID: "BTC-USDT", CurrentPrice: 50000.0, Timestamp: 1000}
	rb.Write(snap)

	got, ok := rb.Latest()
	if !ok {
		t.Fatal("expected ok=true after write")
	}
	if got.SeqNum != 1 {
		t.Fatalf("expected SeqNum=1, got %d", got.SeqNum)
	}
	if got.CurrentPrice != 50000.0 {
		t.Fatalf("expected CurrentPrice=50000, got %f", got.CurrentPrice)
	}
	if got.InstID != "BTC-USDT" {
		t.Fatalf("expected InstID=BTC-USDT, got %s", got.InstID)
	}
}

func TestOverwrite(t *testing.T) {
	size := 64
	rb := New(size)

	// 写入超过 size 个
	total := size * 3
	for i := 1; i <= total; i++ {
		rb.Write(MarketSnapshot{CurrentPrice: float64(i), Timestamp: int64(i)})
	}

	got, ok := rb.Latest()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.CurrentPrice != float64(total) {
		t.Fatalf("expected CurrentPrice=%d, got %f", total, got.CurrentPrice)
	}
	if got.SeqNum != uint64(total) {
		t.Fatalf("expected SeqNum=%d, got %d", total, got.SeqNum)
	}
}

func TestEmptyBuffer(t *testing.T) {
	rb := New(1024)
	_, ok := rb.Latest()
	if ok {
		t.Fatal("expected ok=false for empty buffer")
	}
	if rb.WriteSeq() != 0 {
		t.Fatalf("expected WriteSeq=0, got %d", rb.WriteSeq())
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	rb := New(1024)
	const numWrites = 10000
	const numReaders = 4

	var wg sync.WaitGroup

	// writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numWrites; i++ {
			rb.Write(MarketSnapshot{
				CurrentPrice: float64(i + 1),
				Timestamp:    int64(i + 1),
			})
		}
	}()

	// readers
	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < numWrites; i++ {
				snap, ok := rb.Latest()
				if ok {
					if snap.SeqNum == 0 {
						t.Error("got SeqNum=0 with ok=true")
						return
					}
					if snap.CurrentPrice <= 0 {
						t.Error("got non-positive CurrentPrice with ok=true")
						return
					}
				}
			}
		}()
	}

	wg.Wait()

	// 最终状态检查
	got, ok := rb.Latest()
	if !ok {
		t.Fatal("expected data after writes")
	}
	if got.SeqNum != numWrites {
		t.Fatalf("expected SeqNum=%d, got %d", numWrites, got.SeqNum)
	}
}

func TestReaderHasNew(t *testing.T) {
	rb := New(1024)
	rd := NewReader(rb)

	if rd.HasNew() {
		t.Fatal("expected HasNew=false for empty buffer")
	}

	rb.Write(MarketSnapshot{CurrentPrice: 100})
	if !rd.HasNew() {
		t.Fatal("expected HasNew=true after write")
	}

	rd.Latest()
	if rd.HasNew() {
		t.Fatal("expected HasNew=false after read")
	}

	rb.Write(MarketSnapshot{CurrentPrice: 200})
	if !rd.HasNew() {
		t.Fatal("expected HasNew=true after second write")
	}
}

func TestReaderReadSince(t *testing.T) {
	rb := New(1024)
	rd := NewReader(rb)

	// 写 5 个
	for i := 1; i <= 5; i++ {
		rb.Write(MarketSnapshot{CurrentPrice: float64(i * 100)})
	}

	snaps, ok := rd.ReadSince()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(snaps) != 5 {
		t.Fatalf("expected 5 snapshots, got %d", len(snaps))
	}
	for i, s := range snaps {
		expected := float64((i + 1) * 100)
		if s.CurrentPrice != expected {
			t.Fatalf("snap[%d]: expected CurrentPrice=%f, got %f", i, expected, s.CurrentPrice)
		}
		if s.SeqNum != uint64(i+1) {
			t.Fatalf("snap[%d]: expected SeqNum=%d, got %d", i, i+1, s.SeqNum)
		}
	}

	// 再次调用应无新数据
	snaps2, ok2 := rd.ReadSince()
	if ok2 || len(snaps2) > 0 {
		t.Fatal("expected no new data after full read")
	}
}

func TestReaderReadSinceLagBehind(t *testing.T) {
	size := 1024
	rb := New(size)
	rd := NewReader(rb)

	// 写 2000 个（超过 size）
	total := 2000
	for i := 1; i <= total; i++ {
		rb.Write(MarketSnapshot{CurrentPrice: float64(i)})
	}

	snaps, ok := rd.ReadSince()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(snaps) != size {
		t.Fatalf("expected %d snapshots (capped by size), got %d", size, len(snaps))
	}
	// 应该是最新的 1024 个，序号从 977 到 2000
	firstExpectedSeq := uint64(total - size + 1)
	if snaps[0].SeqNum != firstExpectedSeq {
		t.Fatalf("expected first SeqNum=%d, got %d", firstExpectedSeq, snaps[0].SeqNum)
	}
	if snaps[len(snaps)-1].SeqNum != uint64(total) {
		t.Fatalf("expected last SeqNum=%d, got %d", total, snaps[len(snaps)-1].SeqNum)
	}
}

func TestBufferManager(t *testing.T) {
	m := NewBufferManager(128)

	// GetOrCreate
	buf := m.GetOrCreate("BTC-USDT")
	if buf == nil {
		t.Fatal("expected non-nil buffer")
	}
	buf2 := m.GetOrCreate("BTC-USDT")
	if buf != buf2 {
		t.Fatal("expected same buffer for same instID")
	}

	// Write/Latest
	m.Write("BTC-USDT", MarketSnapshot{CurrentPrice: 50000})
	m.Write("ETH-USDT", MarketSnapshot{CurrentPrice: 3000})

	snap, ok := m.Latest("BTC-USDT")
	if !ok || snap.CurrentPrice != 50000 {
		t.Fatalf("expected BTC price=50000, got %f ok=%v", snap.CurrentPrice, ok)
	}
	if snap.InstID != "BTC-USDT" {
		t.Fatalf("expected InstID=BTC-USDT, got %s", snap.InstID)
	}

	snap, ok = m.Latest("ETH-USDT")
	if !ok || snap.CurrentPrice != 3000 {
		t.Fatalf("expected ETH price=3000, got %f ok=%v", snap.CurrentPrice, ok)
	}

	// 不存在的品种
	_, ok = m.Latest("DOGE-USDT")
	if ok {
		t.Fatal("expected ok=false for non-existent instrument")
	}

	// Instruments
	ids := m.Instruments()
	if len(ids) != 2 {
		t.Fatalf("expected 2 instruments, got %d", len(ids))
	}
	if ids[0] != "BTC-USDT" || ids[1] != "ETH-USDT" {
		t.Fatalf("unexpected instrument order: %v", ids)
	}

	// Count
	if m.Count() != 2 {
		t.Fatalf("expected count=2, got %d", m.Count())
	}
}

func TestMultiReaderLatestAll(t *testing.T) {
	m := NewBufferManager(128)
	m.Write("BTC-USDT", MarketSnapshot{CurrentPrice: 50000})
	m.Write("ETH-USDT", MarketSnapshot{CurrentPrice: 3000})
	m.Write("SOL-USDT", MarketSnapshot{CurrentPrice: 100})

	mr := NewMultiReader(m)
	all := mr.LatestAll()
	if len(all) != 3 {
		t.Fatalf("expected 3 instruments, got %d", len(all))
	}
	if all["BTC-USDT"].CurrentPrice != 50000 {
		t.Fatalf("expected BTC=50000, got %f", all["BTC-USDT"].CurrentPrice)
	}
	if all["ETH-USDT"].CurrentPrice != 3000 {
		t.Fatalf("expected ETH=3000, got %f", all["ETH-USDT"].CurrentPrice)
	}
	if all["SOL-USDT"].CurrentPrice != 100 {
		t.Fatalf("expected SOL=100, got %f", all["SOL-USDT"].CurrentPrice)
	}
}

func TestMultiReaderHasNew(t *testing.T) {
	m := NewBufferManager(128)
	mr := NewMultiReader(m)

	// 写入前
	if mr.HasNew("BTC-USDT") {
		t.Fatal("expected HasNew=false before write")
	}

	m.Write("BTC-USDT", MarketSnapshot{CurrentPrice: 50000})
	if !mr.HasNew("BTC-USDT") {
		t.Fatal("expected HasNew=true after write")
	}

	mr.Latest("BTC-USDT")
	if mr.HasNew("BTC-USDT") {
		t.Fatal("expected HasNew=false after read")
	}
}

func TestSize(t *testing.T) {
	rb := New(256)
	if rb.Size() != 256 {
		t.Fatalf("expected size=256, got %d", rb.Size())
	}

	rb2 := New(0)
	if rb2.Size() != 1024 {
		t.Fatalf("expected default size=1024, got %d", rb2.Size())
	}
}

func BenchmarkWrite(b *testing.B) {
	rb := New(1024)
	snap := MarketSnapshot{InstID: "BTC-USDT", CurrentPrice: 50000}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.Write(snap)
	}
}

func BenchmarkLatest(b *testing.B) {
	rb := New(1024)
	rb.Write(MarketSnapshot{InstID: "BTC-USDT", CurrentPrice: 50000})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.Latest()
	}
}

func BenchmarkConcurrentReadWrite(b *testing.B) {
	rb := New(1024)
	snap := MarketSnapshot{InstID: "BTC-USDT", CurrentPrice: 50000}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rb.Write(snap)
			rb.Latest()
		}
	})
}

func BenchmarkBufferManagerWrite(b *testing.B) {
	m := NewBufferManager(1024)
	instruments := []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "DOGE-USDT"}
	snap := MarketSnapshot{CurrentPrice: 50000}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Write(instruments[i%len(instruments)], snap)
	}
}

func ExampleRingBuffer() {
	rb := New(1024)

	rb.Write(MarketSnapshot{
		InstID:       "BTC-USDT",
		CurrentPrice: 50000.0,
		Timestamp:    1700000000000,
	})

	snap, ok := rb.Latest()
	if ok {
		fmt.Printf("Price: %.0f\n", snap.CurrentPrice)
	}
	// Output: Price: 50000
}
