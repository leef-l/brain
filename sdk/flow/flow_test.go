package flow

import (
	"context"
	"io"
	"sort"
	"strings"
	"testing"
)

// ==================== CAS Tests ====================

func TestComputeRefConsistency(t *testing.T) {
	data := []byte("hello world")
	r1 := ComputeRef(data)
	r2 := ComputeRef(data)
	if r1 != r2 {
		t.Fatalf("ComputeRef not consistent: %s != %s", r1, r2)
	}
	if !strings.HasPrefix(string(r1), "sha256:") {
		t.Fatalf("Ref should start with sha256: got %s", r1)
	}
}

func TestComputeRefDifferentData(t *testing.T) {
	r1 := ComputeRef([]byte("aaa"))
	r2 := ComputeRef([]byte("bbb"))
	if r1 == r2 {
		t.Fatal("different data should produce different refs")
	}
}

func TestMemStorePutGet(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	data := []byte("test data")
	ref, err := s.Put(ctx, data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Get returned %q, want %q", got, data)
	}
}

func TestMemStoreIdempotentPut(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	data := []byte("duplicate")
	r1, _ := s.Put(ctx, data)
	r2, _ := s.Put(ctx, data)
	if r1 != r2 {
		t.Fatalf("idempotent put: refs differ %s != %s", r1, r2)
	}

	refs, _ := s.List(ctx)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref after duplicate put, got %d", len(refs))
	}
}

func TestMemStoreGetNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	_, err := s.Get(ctx, Ref("sha256:0000"))
	if err != ErrRefNotFound {
		t.Fatalf("expected ErrRefNotFound, got %v", err)
	}
}

func TestMemStoreHas(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	if s.Has(ctx, Ref("sha256:nope")) {
		t.Fatal("Has should return false for missing ref")
	}

	ref, _ := s.Put(ctx, []byte("exists"))
	if !s.Has(ctx, ref) {
		t.Fatal("Has should return true after Put")
	}
}

func TestMemStoreDelete(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	ref, _ := s.Put(ctx, []byte("to delete"))
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.Has(ctx, ref) {
		t.Fatal("Has should return false after Delete")
	}
}

func TestMemStoreList(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	s.Put(ctx, []byte("a"))
	s.Put(ctx, []byte("b"))
	s.Put(ctx, []byte("c"))

	refs, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d", len(refs))
	}
}

func TestMemStoreDataIsolation(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	data := []byte("original")
	ref, _ := s.Put(ctx, data)

	// mutate original slice
	data[0] = 'X'

	got, _ := s.Get(ctx, ref)
	if got[0] == 'X' {
		t.Fatal("store should copy data on Put")
	}

	// mutate returned slice
	got[0] = 'Y'
	got2, _ := s.Get(ctx, ref)
	if got2[0] == 'Y' {
		t.Fatal("store should copy data on Get")
	}
}

// ==================== Stream Tests ====================

func TestPipeBackendWriteRead(t *testing.T) {
	ctx := context.Background()
	p := NewPipeBackend(8)

	if err := p.Write(ctx, []byte("frame1")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := p.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "frame1" {
		t.Fatalf("Read returned %q, want %q", got, "frame1")
	}
}

func TestPipeBackendMultiFrame(t *testing.T) {
	ctx := context.Background()
	p := NewPipeBackend(8)

	frames := []string{"a", "b", "c"}
	for _, f := range frames {
		if err := p.Write(ctx, []byte(f)); err != nil {
			t.Fatalf("Write(%s): %v", f, err)
		}
	}

	for _, want := range frames {
		got, err := p.Read(ctx)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(got) != want {
			t.Fatalf("Read: got %q, want %q", got, want)
		}
	}
}

func TestPipeBackendCloseRead(t *testing.T) {
	p := NewPipeBackend(8)
	p.Close()

	ctx := context.Background()
	_, err := p.Read(ctx)
	if err != io.EOF {
		t.Fatalf("Read after Close: expected io.EOF, got %v", err)
	}
}

func TestPipeBackendCloseWrite(t *testing.T) {
	p := NewPipeBackend(8)
	p.Close()

	ctx := context.Background()
	err := p.Write(ctx, []byte("data"))
	if err != ErrPipeClosed {
		t.Fatalf("Write after Close: expected ErrPipeClosed, got %v", err)
	}
}

func TestPipeBackendReadDrainsAfterClose(t *testing.T) {
	ctx := context.Background()
	p := NewPipeBackend(8)

	p.Write(ctx, []byte("buffered"))
	p.Close()

	got, err := p.Read(ctx)
	if err != nil {
		t.Fatalf("Read buffered after Close: %v", err)
	}
	if string(got) != "buffered" {
		t.Fatalf("got %q, want %q", got, "buffered")
	}
}

func TestPipeBackendContextCancel(t *testing.T) {
	p := NewPipeBackend(0) // unbuffered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.Write(ctx, []byte("data"))
	if err != context.Canceled {
		t.Fatalf("Write with cancelled ctx: expected context.Canceled, got %v", err)
	}

	_, err = p.Read(ctx)
	if err != context.Canceled {
		t.Fatalf("Read with cancelled ctx: expected context.Canceled, got %v", err)
	}
	p.Close()
}

// ==================== PipeRegistry Tests ====================

func TestPipeRegistryCreateGet(t *testing.T) {
	r := NewPipeRegistry()

	p, err := r.Create("test", 4)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p == nil {
		t.Fatal("Create returned nil")
	}

	got, ok := r.Get("test")
	if !ok || got != p {
		t.Fatal("Get should return the created pipe")
	}
}

func TestPipeRegistryDuplicate(t *testing.T) {
	r := NewPipeRegistry()
	r.Create("dup", 4)
	_, err := r.Create("dup", 4)
	if err != ErrPipeExists {
		t.Fatalf("expected ErrPipeExists, got %v", err)
	}
}

func TestPipeRegistryClose(t *testing.T) {
	r := NewPipeRegistry()
	r.Create("x", 4)

	if err := r.Close("x"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, ok := r.Get("x")
	if ok {
		t.Fatal("Get should return false after Close")
	}
}

func TestPipeRegistryCloseNotFound(t *testing.T) {
	r := NewPipeRegistry()
	err := r.Close("nope")
	if err != ErrPipeNotFound {
		t.Fatalf("expected ErrPipeNotFound, got %v", err)
	}
}

func TestPipeRegistryCloseAll(t *testing.T) {
	r := NewPipeRegistry()
	r.Create("a", 4)
	r.Create("b", 4)

	if err := r.CloseAll(); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
	if len(r.Names()) != 0 {
		t.Fatal("Names should be empty after CloseAll")
	}
}

func TestPipeRegistryNames(t *testing.T) {
	r := NewPipeRegistry()
	r.Create("z", 1)
	r.Create("a", 1)

	names := r.Names()
	sort.Strings(names)
	if len(names) != 2 || names[0] != "a" || names[1] != "z" {
		t.Fatalf("Names: got %v, want [a z]", names)
	}
}

// ==================== EdgeRegistry Tests ====================

func TestEdgeRegistryRegisterGet(t *testing.T) {
	r := NewEdgeRegistry()
	desc := EdgeDescriptor{
		Name:      "signal-flow",
		Type:      EdgeMaterialized,
		FromBrain: "quant",
		ToBrain:   "risk",
	}

	if err := r.Register(desc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok := r.Get("signal-flow")
	if !ok {
		t.Fatal("Get should find registered edge")
	}
	if got.Name != "signal-flow" || got.FromBrain != "quant" {
		t.Fatalf("Get returned wrong descriptor: %+v", got)
	}
}

func TestEdgeRegistryDuplicate(t *testing.T) {
	r := NewEdgeRegistry()
	desc := EdgeDescriptor{Name: "dup", Type: EdgeStreaming}
	r.Register(desc)
	err := r.Register(desc)
	if err != ErrEdgeExists {
		t.Fatalf("expected ErrEdgeExists, got %v", err)
	}
}

func TestEdgeRegistryGetNotFound(t *testing.T) {
	r := NewEdgeRegistry()
	_, ok := r.Get("nope")
	if ok {
		t.Fatal("Get should return false for missing edge")
	}
}

func TestEdgeRegistryFindByBrain(t *testing.T) {
	r := NewEdgeRegistry()
	r.Register(EdgeDescriptor{Name: "e1", FromBrain: "A", ToBrain: "B"})
	r.Register(EdgeDescriptor{Name: "e2", FromBrain: "B", ToBrain: "C"})
	r.Register(EdgeDescriptor{Name: "e3", FromBrain: "D", ToBrain: "E"})

	found := r.FindByBrain("B")
	if len(found) != 2 {
		t.Fatalf("FindByBrain(B): expected 2, got %d", len(found))
	}

	found = r.FindByBrain("D")
	if len(found) != 1 || found[0].Name != "e3" {
		t.Fatalf("FindByBrain(D): got %+v", found)
	}

	found = r.FindByBrain("Z")
	if len(found) != 0 {
		t.Fatalf("FindByBrain(Z): expected 0, got %d", len(found))
	}
}

func TestEdgeRegistryList(t *testing.T) {
	r := NewEdgeRegistry()
	r.Register(EdgeDescriptor{Name: "x"})
	r.Register(EdgeDescriptor{Name: "y"})

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List: expected 2, got %d", len(list))
	}
}

func TestEdgeRegistryRemove(t *testing.T) {
	r := NewEdgeRegistry()
	r.Register(EdgeDescriptor{Name: "rm"})
	r.Remove("rm")

	_, ok := r.Get("rm")
	if ok {
		t.Fatal("Get should return false after Remove")
	}

	list := r.List()
	if len(list) != 0 {
		t.Fatalf("List: expected 0 after Remove, got %d", len(list))
	}
}
