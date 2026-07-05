package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually advanced, concurrency-safe clock for deterministic
// refill testing.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newFixed returns a Limiter whose clock never advances, isolating burst/
// capacity behavior from refill.
func newFixed() *Limiter {
	l := New()
	fixed := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return fixed }
	return l
}

func TestAllowUnlimited(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		perMinute int
	}{
		{"zero", 0},
		{"negative", -5},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := newFixed()
			for i := 0; i < 1000; i++ {
				if !l.Allow("k", tt.perMinute) {
					t.Fatalf("perMinute=%d call %d: got false, want true (unlimited)", tt.perMinute, i)
				}
			}
			// Unlimited must not allocate a bucket.
			l.mu.Lock()
			n := len(l.buckets)
			l.mu.Unlock()
			if n != 0 {
				t.Fatalf("unlimited created %d buckets, want 0", n)
			}
		})
	}
}

func TestBurstThenDeny(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		perMinute int
	}{
		{"cap1", 1},
		{"cap5", 5},
		{"cap100", 100},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := newFixed() // no refill
			// A fresh bucket starts full: exactly perMinute allows succeed.
			for i := 0; i < tt.perMinute; i++ {
				if !l.Allow("k", tt.perMinute) {
					t.Fatalf("call %d: got false, want true (within burst)", i)
				}
			}
			// The next call must be denied (bucket empty, no refill).
			if l.Allow("k", tt.perMinute) {
				t.Fatalf("call %d: got true, want false (over burst)", tt.perMinute)
			}
		})
	}
}

func TestAllowN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		perMinute int
		n         int
		want      bool
	}{
		{"consume half", 10, 5, true},
		{"consume exactly capacity", 10, 10, true},
		{"consume over capacity", 10, 11, false},
		{"n zero always ok", 10, 0, true},
		{"n negative always ok", 10, -3, true},
		{"n zero unlimited", 0, 0, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := newFixed()
			if got := l.AllowN("k", tt.perMinute, tt.n); got != tt.want {
				t.Fatalf("AllowN(k, %d, %d) = %v, want %v", tt.perMinute, tt.n, got, tt.want)
			}
		})
	}
}

func TestAllowNDrainsExactly(t *testing.T) {
	t.Parallel()
	l := newFixed()
	// Capacity 10: consume 4 + 4 leaves 2; a request for 3 fails, for 2 passes.
	if !l.AllowN("k", 10, 4) {
		t.Fatal("first AllowN(4) = false, want true")
	}
	if !l.AllowN("k", 10, 4) {
		t.Fatal("second AllowN(4) = false, want true")
	}
	if l.AllowN("k", 10, 3) {
		t.Fatal("AllowN(3) with 2 left = true, want false")
	}
	if !l.AllowN("k", 10, 2) {
		t.Fatal("AllowN(2) with 2 left = false, want true")
	}
	if l.AllowN("k", 10, 1) {
		t.Fatal("AllowN(1) with 0 left = true, want false")
	}
}

func TestRefillOverTime(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	l := New()
	l.now = clk.now

	const perMinute = 60 // rate = 1 token/sec, capacity 60
	// Drain the full bucket.
	for i := 0; i < perMinute; i++ {
		if !l.Allow("k", perMinute) {
			t.Fatalf("drain call %d: got false, want true", i)
		}
	}
	if l.Allow("k", perMinute) {
		t.Fatal("post-drain: got true, want false")
	}

	// After 3 seconds, 3 tokens have refilled: 3 allows, then denial.
	clk.advance(3 * time.Second)
	for i := 0; i < 3; i++ {
		if !l.Allow("k", perMinute) {
			t.Fatalf("refill call %d: got false, want true", i)
		}
	}
	if l.Allow("k", perMinute) {
		t.Fatal("after consuming 3 refilled tokens: got true, want false")
	}
}

func TestRefillCapsAtCapacity(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	l := New()
	l.now = clk.now

	const perMinute = 30
	// Drain.
	for i := 0; i < perMinute; i++ {
		if !l.Allow("k", perMinute) {
			t.Fatalf("drain call %d: got false, want true", i)
		}
	}
	// Idle for an hour: refill must saturate at capacity, not overflow.
	clk.advance(time.Hour)
	for i := 0; i < perMinute; i++ {
		if !l.Allow("k", perMinute) {
			t.Fatalf("post-idle burst call %d: got false, want true", i)
		}
	}
	if l.Allow("k", perMinute) {
		t.Fatal("bucket held more than capacity after long idle")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	t.Parallel()
	l := newFixed()
	const perMinute = 2
	// Exhaust key "a".
	if !l.Allow("a", perMinute) || !l.Allow("a", perMinute) {
		t.Fatal("draining a: got false, want true")
	}
	if l.Allow("a", perMinute) {
		t.Fatal("a over budget: got true, want false")
	}
	// Key "b" must be unaffected.
	if !l.Allow("b", perMinute) || !l.Allow("b", perMinute) {
		t.Fatal("b should have its own full budget")
	}
	if l.Allow("b", perMinute) {
		t.Fatal("b over budget: got true, want false")
	}
}

func TestChangingPerMinuteShrinks(t *testing.T) {
	t.Parallel()
	l := newFixed()
	// Create with capacity 100 and leave it full.
	if !l.Allow("k", 100) {
		t.Fatal("initial allow failed")
	}
	// tokens now 99. Re-call with a smaller limit of 3: capacity clamps to 3,
	// so only 3 allows may succeed before denial (no refill under fixed clock).
	for i := 0; i < 3; i++ {
		if !l.Allow("k", 3) {
			t.Fatalf("shrunk-limit call %d: got false, want true", i)
		}
	}
	if l.Allow("k", 3) {
		t.Fatal("after shrink budget exhausted: got true, want false")
	}
}

// TestConcurrentAllow exercises the limiter under many goroutines with a fixed
// clock (no refill), so the number of granted requests must equal capacity
// exactly. Run with -race.
func TestConcurrentAllow(t *testing.T) {
	t.Parallel()
	l := newFixed()

	const (
		perMinute  = 500
		goroutines = 50
		perG       = 100 // total attempts = 5000 >> capacity
	)

	var granted int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				if l.Allow("shared", perMinute) {
					atomic.AddInt64(&granted, 1)
				}
			}
		}()
	}
	wg.Wait()

	if granted != perMinute {
		t.Fatalf("granted = %d, want exactly %d (capacity)", granted, perMinute)
	}
}

// TestConcurrentDistinctKeys stresses lazy per-key bucket creation from many
// goroutines to surface map races under -race.
func TestConcurrentDistinctKeys(t *testing.T) {
	t.Parallel()
	l := newFixed()

	const (
		keys       = 200
		perKeyG    = 8
		perMinute  = 3
		iterations = 20
	)

	var wg sync.WaitGroup
	for k := 0; k < keys; k++ {
		key := string(rune('A'+k%26)) + "-" + time.Duration(k).String()
		for g := 0; g < perKeyG; g++ {
			wg.Add(1)
			go func(key string) {
				defer wg.Done()
				for i := 0; i < iterations; i++ {
					l.Allow(key, perMinute)
				}
			}(key)
		}
	}
	wg.Wait()

	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n == 0 {
		t.Fatal("expected buckets to be created")
	}
}

// TestRealTimeRefill is a small non-deterministic sanity check that the real
// clock actually drives refill, complementing the injected-clock tests.
func TestRealTimeRefill(t *testing.T) {
	t.Parallel()
	l := New()             // real time.Now
	const perMinute = 6000 // rate = 100 tokens/sec

	// Drain fully.
	for l.Allow("k", perMinute) {
	}
	// Immediately after draining, it should be denied.
	if l.Allow("k", perMinute) {
		t.Fatal("expected denial immediately after drain")
	}
	// After ~50ms, ~5 tokens (100/sec) should have refilled; at least 1 allow
	// must now succeed. Loose bound keeps the test non-flaky.
	time.Sleep(50 * time.Millisecond)
	if !l.Allow("k", perMinute) {
		t.Fatal("expected at least one allow after real-time refill")
	}
}
