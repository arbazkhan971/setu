// Package ratelimit provides per-key token-bucket limiters for enforcing
// requests-per-minute (RPM) and tokens-per-minute (TPM) quotas on a Setu
// gateway. Each distinct key (typically an API key, user, or model) gets an
// independent bucket with capacity equal to the caller-supplied per-minute
// limit, refilling continuously at limit/60 tokens per second and starting
// full so a fresh key may burst up to its whole minute of budget at once.
//
// A single Limiter is safe for concurrent use by multiple goroutines.
//
// Memory: buckets are created lazily on first use and retained for the
// lifetime of the Limiter, one per distinct key ever seen. Callers that mint
// unbounded, short-lived keys should therefore use a fresh Limiter per epoch
// (or scope keys to a bounded set) to avoid unbounded growth; there is no
// automatic eviction.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter holds one token bucket per key. The zero value is not usable; call
// New. It is safe for concurrent use by multiple goroutines.
type Limiter struct {
	// now returns the current time. It is a field so tests can inject a
	// deterministic clock; production code leaves it as time.Now. It is set
	// once by New and treated as read-only thereafter.
	now func() time.Time

	mu      sync.Mutex // guards buckets
	buckets map[string]*bucket
}

// bucket is a single token bucket. Its own mutex serializes refill-and-consume
// so the Limiter's map lock is held only briefly for lookup/creation.
type bucket struct {
	mu     sync.Mutex
	tokens float64   // current available tokens
	last   time.Time // last time tokens was refilled
}

// New returns a ready Limiter backed by the real clock.
func New() *Limiter {
	return &Limiter{
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}
}

// Allow consumes a single token from key's bucket, returning true if the
// request is within the limit. A perMinute of zero or less means "unlimited"
// and always returns true (no bucket is created). It is shorthand for
// AllowN(key, perMinute, 1).
func (l *Limiter) Allow(key string, perMinute int) bool {
	return l.AllowN(key, perMinute, 1)
}

// AllowN consumes n tokens from key's bucket, returning true only if at least n
// tokens are currently available (after refilling for elapsed time). The bucket
// has capacity perMinute and refills at perMinute/60 tokens per second.
//
// A perMinute of zero or less means "unlimited" and always returns true; no
// bucket is created. An n of zero or less consumes nothing and always returns
// true. Because tokens are capped at capacity, an n greater than perMinute can
// never be satisfied and always returns false.
func (l *Limiter) AllowN(key string, perMinute, n int) bool {
	if perMinute <= 0 {
		return true // unlimited
	}
	if n <= 0 {
		return true // nothing to consume
	}

	now := l.now()
	b := l.getBucket(key, perMinute, now)
	return b.take(now, perMinute, n)
}

// getBucket returns the bucket for key, creating it full at capacity perMinute
// on first use.
func (l *Limiter) getBucket(key string, perMinute int, now time.Time) *bucket {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(perMinute), last: now}
		l.buckets[key] = b
	}
	return b
}

// take refills the bucket for the time elapsed since its last update, caps it at
// capacity, then consumes n tokens if available. perMinute sets both the refill
// rate (perMinute/60 per second) and the capacity, so a changed perMinute for an
// existing key takes effect on the next call.
func (b *bucket) take(now time.Time, perMinute, n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	capacity := float64(perMinute)
	// Refill for elapsed time. Guard against a non-monotonic clock (elapsed
	// <= 0), which would otherwise drain tokens or leave last in the future.
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * (float64(perMinute) / 60.0)
		if b.tokens > capacity {
			b.tokens = capacity
		}
		b.last = now
	} else if b.tokens > capacity {
		// perMinute may have shrunk since creation; clamp down.
		b.tokens = capacity
	}

	if b.tokens >= float64(n) {
		b.tokens -= float64(n)
		return true
	}
	return false
}
