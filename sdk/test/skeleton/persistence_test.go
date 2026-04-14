package skeleton

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
)

// ---------------------------------------------------------------------------
// CAS — 已知向量
// ---------------------------------------------------------------------------

func TestSha256HexKnownVector(t *testing.T) {
	// FIPS 180-4 known answer
	emptyHash := persistence.Sha256Hex([]byte(""))
	if emptyHash != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("SHA-256('') = %q", emptyHash)
	}

	abcHash := persistence.Sha256Hex([]byte("abc"))
	if abcHash != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("SHA-256('abc') = %q", abcHash)
	}
}

// ---------------------------------------------------------------------------
// ComputeKey 格式
// ---------------------------------------------------------------------------

func TestComputeKeyFormat(t *testing.T) {
	ref := persistence.ComputeKey([]byte("hello"))
	algo, hex, err := persistence.ParseRef(string(ref))
	if err != nil {
		t.Fatal(err)
	}
	if algo != "sha256" {
		t.Errorf("algo = %q", algo)
	}
	if len(hex) != 64 {
		t.Errorf("hex len = %d", len(hex))
	}
}

// ---------------------------------------------------------------------------
// ComputeKey 确定性 & 去重
// ---------------------------------------------------------------------------

func TestComputeKeyDeterministic(t *testing.T) {
	r1 := persistence.ComputeKey([]byte("same"))
	r2 := persistence.ComputeKey([]byte("same"))
	if r1 != r2 {
		t.Errorf("same input → different refs: %s vs %s", r1, r2)
	}
}

func TestComputeKeyUnique(t *testing.T) {
	r1 := persistence.ComputeKey([]byte("alpha"))
	r2 := persistence.ComputeKey([]byte("beta"))
	if r1 == r2 {
		t.Error("different inputs should produce different refs")
	}
}

// ---------------------------------------------------------------------------
// ParseRef 验证
// ---------------------------------------------------------------------------

func TestParseRefValidation(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid", "sha256/" + persistence.Sha256Hex([]byte("x")), false},
		{"no slash", "sha256abcdef", true},
		{"wrong algo", "md5/" + persistence.Sha256Hex([]byte("x")), true},
		{"short digest", "sha256/abcdef", true},
		{"uppercase hex", "sha256/" + "ABCDEF" + persistence.Sha256Hex([]byte("x"))[6:], true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := persistence.ParseRef(tc.input)
			if (err != nil) != tc.wantErr {
				t.Errorf("ParseRef(%q) err = %v, wantErr = %v", tc.input, err, tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MemPlanStore — Create / Get / Archive
// ---------------------------------------------------------------------------

func TestMemPlanStoreRoundTrip(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC) }
	store := persistence.NewMemPlanStore(now)
	ctx := context.Background()

	plan := &persistence.BrainPlan{
		RunID:   1,
		BrainID: "central",
		Version: 1,
	}
	planID, err := store.Create(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != 1 {
		t.Errorf("RunID = %d", got.RunID)
	}

	// Archive
	if err := store.Archive(ctx, planID); err != nil {
		t.Fatal(err)
	}
	archived, err := store.Get(ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Archived {
		t.Error("plan should be archived")
	}
}

// ---------------------------------------------------------------------------
// MemPlanStore — 归档后拒绝更新
// ---------------------------------------------------------------------------

func TestMemPlanStoreArchivedRejectsUpdate(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC) }
	store := persistence.NewMemPlanStore(now)
	ctx := context.Background()

	plan := &persistence.BrainPlan{RunID: 1, Version: 1}
	planID, _ := store.Create(ctx, plan)
	store.Archive(ctx, planID)

	delta := &persistence.BrainPlanDelta{
		PlanID:  planID,
		Version: 2,
		OpType:  "update",
	}
	err := store.Update(ctx, planID, delta)
	if err == nil {
		t.Error("update on archived plan should fail")
	}
}

// ---------------------------------------------------------------------------
// MemArtifactStore — CAS 往返
// ---------------------------------------------------------------------------

func TestMemArtifactStorePutGetExists(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC) }
	meta := persistence.NewMemArtifactMetaStore(now)
	store := persistence.NewMemArtifactStore(meta, now)
	ctx := context.Background()

	art := persistence.Artifact{
		Kind:    "code",
		Content: []byte("package main"),
		Caption: "main.go",
	}
	ref, err := store.Put(ctx, 1, art)
	if err != nil {
		t.Fatal(err)
	}

	exists, err := store.Exists(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("Exists should be true")
	}

	reader, err := store.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, _ := io.ReadAll(reader)
	if string(data) != "package main" {
		t.Errorf("Get = %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// MemArtifactStore — 并发 Put 去重
// ---------------------------------------------------------------------------

func TestMemArtifactStoreConcurrentDedup(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC) }
	meta := persistence.NewMemArtifactMetaStore(now)
	store := persistence.NewMemArtifactStore(meta, now)
	ctx := context.Background()

	content := []byte("dedup test content")
	var wg sync.WaitGroup
	refs := make([]persistence.Ref, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ref, _ := store.Put(ctx, 1, persistence.Artifact{
				Kind:    "test",
				Content: content,
			})
			refs[i] = ref
		}(i)
	}
	wg.Wait()

	// 所有 ref 应该相同
	for i := 1; i < len(refs); i++ {
		if refs[i] != refs[0] {
			t.Errorf("ref[%d] = %s, want %s", i, refs[i], refs[0])
		}
	}
}

// ---------------------------------------------------------------------------
// MemUsageLedger — 基础操作
// ---------------------------------------------------------------------------

func TestMemUsageLedgerRecordAndSum(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC) }
	ledger := persistence.NewMemUsageLedger(now)
	ctx := context.Background()

	u1 := &persistence.UsageRecord{
		RunID:          1,
		InputTokens:   100,
		OutputTokens:  50,
		CostUSD:       0.01,
		IdempotencyKey: "key-1",
	}
	u2 := &persistence.UsageRecord{
		RunID:          1,
		InputTokens:   200,
		OutputTokens:  100,
		CostUSD:       0.02,
		IdempotencyKey: "key-2",
	}
	if err := ledger.Record(ctx, u1); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Record(ctx, u2); err != nil {
		t.Fatal(err)
	}

	summary, err := ledger.Sum(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if summary.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", summary.InputTokens)
	}
	if summary.OutputTokens != 150 {
		t.Errorf("OutputTokens = %d, want 150", summary.OutputTokens)
	}
}
