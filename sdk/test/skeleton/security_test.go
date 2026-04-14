package skeleton

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/security"
)

// ---------------------------------------------------------------------------
// MemVault — Put / Get 往返
// ---------------------------------------------------------------------------

func TestMemVaultPutGet(t *testing.T) {
	v := security.NewMemVault()
	ctx := context.Background()
	if err := v.Put(ctx, "api_key", "sk-12345"); err != nil {
		t.Fatal(err)
	}
	val, err := v.Get(ctx, "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if val != "sk-12345" {
		t.Errorf("Get = %q, want %q", val, "sk-12345")
	}
}

// ---------------------------------------------------------------------------
// MemVault — Get 缺失 key
// ---------------------------------------------------------------------------

func TestMemVaultGetMissing(t *testing.T) {
	v := security.NewMemVault()
	_, err := v.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

// ---------------------------------------------------------------------------
// MemVault — 无效 key 拒绝
// ---------------------------------------------------------------------------

func TestMemVaultInvalidKey(t *testing.T) {
	v := security.NewMemVault()
	ctx := context.Background()

	cases := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"leading space", " key"},
		{"trailing space", "key "},
		{"newline", "key\nvalue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.Put(ctx, tc.key, "val"); err == nil {
				t.Error("expected error for invalid key")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MemVault — TTL 过期
// ---------------------------------------------------------------------------

func TestMemVaultTTLExpiry(t *testing.T) {
	clock := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	v := security.NewMemVault(
		security.WithMemVaultClock(func() time.Time { return clock }),
	)
	ctx := context.Background()

	v.PutWithTTL(ctx, "temp", "value", 10*time.Second)

	// 未过期
	val, err := v.Get(ctx, "temp")
	if err != nil {
		t.Fatal(err)
	}
	if val != "value" {
		t.Errorf("Get = %q", val)
	}

	// 推进时钟超过 TTL
	clock = clock.Add(11 * time.Second)
	v = security.NewMemVault(
		security.WithMemVaultClock(func() time.Time { return clock }),
	)
	v.PutWithTTL(ctx, "temp2", "value2", 5*time.Second)
	clock = clock.Add(6 * time.Second)
	// 由于 MemVault 内部用的是构造时的 clock 函数，需要用可变时钟
	// 这里改用更正确的方式测试
	var currentTime time.Time
	currentTime = time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	v2 := security.NewMemVault(
		security.WithMemVaultClock(func() time.Time { return currentTime }),
	)
	v2.PutWithTTL(ctx, "ttl_key", "ttl_val", 5*time.Second)

	val, err = v2.Get(ctx, "ttl_key")
	if err != nil {
		t.Fatalf("should find before expiry: %v", err)
	}

	currentTime = currentTime.Add(10 * time.Second)
	_, err = v2.Get(ctx, "ttl_key")
	if err == nil {
		t.Error("should fail after TTL expiry")
	}
}

// ---------------------------------------------------------------------------
// MemVault — Delete 幂等
// ---------------------------------------------------------------------------

func TestMemVaultDeleteIdempotent(t *testing.T) {
	v := security.NewMemVault()
	ctx := context.Background()
	// delete 不存在的 key 应该不报错
	if err := v.Delete(ctx, "ghost"); err != nil {
		t.Errorf("Delete nonexistent: %v", err)
	}
	v.Put(ctx, "key1", "val1")
	if err := v.Delete(ctx, "key1"); err != nil {
		t.Fatal(err)
	}
	_, err := v.Get(ctx, "key1")
	if err == nil {
		t.Error("should not find deleted key")
	}
}

// ---------------------------------------------------------------------------
// MemVault — 并发安全
// ---------------------------------------------------------------------------

func TestMemVaultConcurrency(t *testing.T) {
	v := security.NewMemVault()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "key_" + intToStr(i)
			v.Put(ctx, key, "val")
			v.Get(ctx, key)
			v.Delete(ctx, key)
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// MemVault — 审计不泄露秘密
// ---------------------------------------------------------------------------

func TestMemVaultAuditNoSecretLeakage(t *testing.T) {
	audit := security.NewHashChainAuditLogger()
	v := security.NewMemVault(security.WithMemVaultAuditor(audit))
	ctx := context.Background()

	secret := "super_secret_api_key_12345"
	v.Put(ctx, "my_key", secret)
	v.Get(ctx, "my_key")

	events := audit.Snapshot()
	for _, ev := range events {
		// Payload 不应包含原始秘密
		for k, val := range ev.Payload {
			if s, ok := val.(string); ok && s == secret {
				t.Errorf("audit event %q leaked secret value in field %q", ev.Action, k)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Zone 常量
// ---------------------------------------------------------------------------

func TestZoneConstants(t *testing.T) {
	if security.ZoneKernel != 1 {
		t.Errorf("ZoneKernel = %d, want 1", security.ZoneKernel)
	}
	if security.ZoneLLMOutput != 5 {
		t.Errorf("ZoneLLMOutput = %d, want 5", security.ZoneLLMOutput)
	}
}

// ---------------------------------------------------------------------------
// ProxiedLLMAccess
// ---------------------------------------------------------------------------

func TestProxiedLLMAccessMode(t *testing.T) {
	p := security.NewProxiedLLMAccess()
	if p.Mode() != "proxied" {
		t.Errorf("Mode = %q, want proxied", p.Mode())
	}
}

// ---------------------------------------------------------------------------
// HashChainAuditLogger — 链完整性
// ---------------------------------------------------------------------------

func TestHashChainAuditEmitAndVerify(t *testing.T) {
	logger := security.NewHashChainAuditLogger()
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		ev := &security.AuditEvent{
			Actor:    "test",
			Action:   "op_" + intToStr(i),
			Resource: "res",
			Payload:  map[string]interface{}{"i": i},
		}
		if err := logger.Emit(ctx, ev); err != nil {
			t.Fatalf("Emit %d: %v", i, err)
		}
	}

	events := logger.Snapshot()
	if len(events) != 10 {
		t.Fatalf("events len = %d, want 10", len(events))
	}

	// 验证链接
	for i := 1; i < len(events); i++ {
		if events[i].PrevHash != events[i-1].SelfHash {
			t.Errorf("event %d PrevHash mismatch", i)
		}
	}

	// 验证所有 SelfHash 非空
	for i, ev := range events {
		if ev.SelfHash == "" {
			t.Errorf("event %d SelfHash is empty", i)
		}
	}
}
