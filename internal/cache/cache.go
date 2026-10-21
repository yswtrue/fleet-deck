// Package cache is the hub's read-through cache over Redis, used to keep hot
// reads (auth token resolution, board task lists) off Postgres.
//
// The cache is strictly an accelerator: every operation degrades to a miss or
// a no-op on error, so an absent or failing Redis never fails a request — the
// hub simply reads through to the database. Entries additionally carry short
// TTLs so a missed invalidation bounds staleness instead of serving it
// forever.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache is a small JSON-orientated key/value cache. Implementations must be
// safe for concurrent use and must not panic on backend errors: Get reports
// misses as (false, nil) and Set/Delete surface errors the caller may log and
// ignore.
type Cache interface {
	// Get decodes the value stored under key into dest and reports whether it
	// was present. A missing key is (false, nil).
	Get(ctx context.Context, key string, dest any) (bool, error)
	// Set stores val as JSON under key for ttl.
	Set(ctx context.Context, key string, val any, ttl time.Duration) error
	// Delete removes the given keys (absent keys are not an error).
	Delete(ctx context.Context, keys ...string) error
}

const (
	// redisOpTimeout bounds every cache round trip. A cache is an accelerator:
	// a slow or black-holed Redis must not stall an API request for the
	// driver's multi-second defaults.
	redisOpTimeout = time.Second
	// redisRetryCooldown is how long a failed Redis is skipped before the
	// cache tries again. Without it every authenticated request pays a dial
	// (and go-redis's retry backoff) while the server is down.
	redisRetryCooldown = 5 * time.Second
)

// Redis is a Cache backed by a Redis server (go-redis). A nil *Redis is a
// valid no-op cache so callers can treat "not configured" uniformly.
type Redis struct {
	client *redis.Client

	mu        sync.Mutex
	downUntil time.Time
}

var _ Cache = (*Redis)(nil)

// NewRedis builds a Redis-backed cache from a redis:// URL. It does not dial
// eagerly: a Redis that is down at startup (or later) surfaces as per-operation
// errors, which callers treat as cache misses. Retries are disabled and every
// operation is time-boxed; after a failure the cache is skipped for
// redisRetryCooldown so an absent server costs one probe per window instead of
// adding latency to every request.
func NewRedis(url string) (*Redis, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	opts.DialTimeout = redisOpTimeout
	opts.ReadTimeout = redisOpTimeout
	opts.WriteTimeout = redisOpTimeout
	opts.MaxRetries = -1
	// go-redis defaults to five dial attempts with 100ms backoff; a cache
	// probe should fail fast instead.
	opts.DialerRetries = 1
	return &Redis{client: redis.NewClient(opts)}, nil
}

// available reports whether the cache is in its failure cooldown.
func (r *Redis) available() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return time.Now().After(r.downUntil)
}

// markFailed starts the cooldown after a backend error.
func (r *Redis) markFailed() {
	r.mu.Lock()
	r.downUntil = time.Now().Add(redisRetryCooldown)
	r.mu.Unlock()
}

// Ping checks backend connectivity. It is a startup diagnostic only: request
// paths never depend on it, and a failure starts the same cooldown as any
// other operation.
func (r *Redis) Ping(ctx context.Context) error {
	if r == nil || r.client == nil {
		return nil
	}
	if err := r.client.Ping(ctx).Err(); err != nil {
		r.markFailed()
		return err
	}
	return nil
}

// Get implements Cache.
func (r *Redis) Get(ctx context.Context, key string, dest any) (bool, error) {
	if r == nil || r.client == nil || !r.available() {
		return false, nil
	}
	raw, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		r.markFailed()
		return false, err
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		// A corrupt entry is a miss, not a failure: delete it and read through.
		_ = r.client.Del(ctx, key)
		return false, nil
	}
	return true, nil
}

// Set implements Cache.
func (r *Redis) Set(ctx context.Context, key string, val any, ttl time.Duration) error {
	if r == nil || r.client == nil || !r.available() {
		return nil
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return err
	}
	if err := r.client.Set(ctx, key, raw, ttl).Err(); err != nil {
		r.markFailed()
		return err
	}
	return nil
}

// Delete implements Cache.
func (r *Redis) Delete(ctx context.Context, keys ...string) error {
	if r == nil || r.client == nil || !r.available() || len(keys) == 0 {
		return nil
	}
	if err := r.client.Del(ctx, keys...).Err(); err != nil {
		r.markFailed()
		return err
	}
	return nil
}

// Noop is a Cache that stores nothing: every Get is a miss. It is the default
// when no Redis is configured.
type Noop struct{}

var _ Cache = Noop{}

// Get implements Cache.
func (Noop) Get(context.Context, string, any) (bool, error) { return false, nil }

// Set implements Cache.
func (Noop) Set(context.Context, string, any, time.Duration) error { return nil }

// Delete implements Cache.
func (Noop) Delete(context.Context, ...string) error { return nil }
