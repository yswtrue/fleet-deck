package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestRedisCacheLifecycle pins the Cache contract against a real (in-process)
// Redis: miss semantics, JSON round trip, TTL expiry, delete, and the
// corrupt-entry-is-a-miss behavior.
func TestRedisCacheLifecycle(t *testing.T) {
	mr := miniredis.RunT(t)
	c, err := NewRedis("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("new redis cache: %v", err)
	}
	ctx := context.Background()

	var dest map[string]int
	if ok, err := c.Get(ctx, "k", &dest); err != nil || ok {
		t.Fatalf("empty get = (%v, %v), want (false, nil)", ok, err)
	}

	if err := c.Set(ctx, "k", map[string]int{"a": 1}, time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if ok, err := c.Get(ctx, "k", &dest); err != nil || !ok || dest["a"] != 1 {
		t.Fatalf("get = (%v, %v, %v), want (true, nil, {a:1})", ok, err, dest)
	}

	mr.FastForward(2 * time.Minute)
	if ok, err := c.Get(ctx, "k", &dest); err != nil || ok {
		t.Fatalf("expired get = (%v, %v), want (false, nil)", ok, err)
	}

	if err := c.Set(ctx, "k", map[string]int{"a": 2}, time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := c.Delete(ctx, "k", "never-existed"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, _ := c.Get(ctx, "k", &dest); ok {
		t.Fatal("deleted key still present")
	}

	// A corrupt entry is treated as a miss and removed, not an error.
	if err := mr.Set("bad", "{"); err != nil {
		t.Fatalf("seed corrupt entry: %v", err)
	}
	if ok, err := c.Get(ctx, "bad", &dest); err != nil || ok {
		t.Fatalf("corrupt get = (%v, %v), want (false, nil)", ok, err)
	}
	if mr.Exists("bad") {
		t.Fatal("corrupt entry not removed")
	}
}

// TestNoopCache: the default cache stores nothing and never errors.
func TestNoopCache(t *testing.T) {
	ctx := context.Background()
	var dest struct{}
	if ok, err := (Noop{}).Get(ctx, "k", &dest); err != nil || ok {
		t.Fatalf("noop get = (%v, %v), want (false, nil)", ok, err)
	}
	if err := (Noop{}).Set(ctx, "k", dest, time.Second); err != nil {
		t.Fatalf("noop set: %v", err)
	}
	if err := (Noop{}).Delete(ctx, "k"); err != nil {
		t.Fatalf("noop delete: %v", err)
	}
	// A nil *Redis is a valid no-op as well (treats "not configured" uniformly).
	var r *Redis
	if ok, err := r.Get(ctx, "k", &dest); err != nil || ok {
		t.Fatalf("nil redis get = (%v, %v), want (false, nil)", ok, err)
	}
}
