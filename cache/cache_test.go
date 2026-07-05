package cache

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbazkhan971/setu/types"
)

// resp builds a small ChatResponse whose assistant message content is text.
func resp(id, text string) *types.ChatResponse {
	fr := "stop"
	return &types.ChatResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "gpt-4o",
		Choices: []types.Choice{{
			Index:        0,
			Message:      &types.Message{Role: "assistant", Content: text},
			FinishReason: &fr,
		}},
		Usage: &types.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}
}

func strptr(s string) *string { return &s }
func fptr(f float64) *float64 { return &f }
func iptr(i int) *int         { return &i }

func TestGetMiss(t *testing.T) {
	c := New(4, 0)
	if got, ok := c.Get("absent"); ok || got != nil {
		t.Fatalf("Get(absent) = %v, %v; want nil, false", got, ok)
	}
}

func TestSetGetHit(t *testing.T) {
	c := New(4, 0)
	c.Set("k", resp("id-1", "hello"))

	got, ok := c.Get("k")
	if !ok {
		t.Fatal("Get(k) miss after Set")
	}
	if got.ID != "id-1" || got.Choices[0].Message.Content != "hello" {
		t.Fatalf("Get(k) = %+v; unexpected contents", got)
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d; want 1", c.Len())
	}
}

func TestSetUpdatesInPlace(t *testing.T) {
	c := New(4, 0)
	c.Set("k", resp("old", "old text"))
	c.Set("k", resp("new", "new text"))

	if c.Len() != 1 {
		t.Fatalf("Len = %d; want 1 after updating same key", c.Len())
	}
	got, ok := c.Get("k")
	if !ok || got.ID != "new" || got.Choices[0].Message.Content != "new text" {
		t.Fatalf("Get(k) = %+v, %v; want updated value", got, ok)
	}
}

func TestLRUEvictionOrder(t *testing.T) {
	tests := []struct {
		name    string
		touch   func(c *Cache) // extra access after inserting a, b
		wantHit map[string]bool
	}{
		{
			name:    "evicts_least_recently_inserted",
			touch:   func(c *Cache) {},
			wantHit: map[string]bool{"a": false, "b": true, "c": true},
		},
		{
			name:    "get_promotes_to_mru",
			touch:   func(c *Cache) { c.Get("a") }, // a becomes MRU, so b is LRU
			wantHit: map[string]bool{"a": true, "b": false, "c": true},
		},
		{
			name:    "reset_promotes_to_mru",
			touch:   func(c *Cache) { c.Set("a", resp("a2", "a2")) }, // a becomes MRU
			wantHit: map[string]bool{"a": true, "b": false, "c": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(2, 0)
			c.Set("a", resp("a", "a"))
			c.Set("b", resp("b", "b"))
			tt.touch(c)
			c.Set("c", resp("c", "c")) // forces one eviction

			if c.Len() != 2 {
				t.Fatalf("Len = %d; want 2", c.Len())
			}
			for k, want := range tt.wantHit {
				if _, ok := c.Get(k); ok != want {
					t.Errorf("Get(%q) present = %v; want %v", k, ok, want)
				}
			}
		})
	}
}

func TestUnbounded(t *testing.T) {
	for _, max := range []int{0, -1} {
		c := New(max, 0)
		for i := 0; i < 1000; i++ {
			c.Set(fmt.Sprintf("k%d", i), resp(fmt.Sprintf("id%d", i), "x"))
		}
		if c.Len() != 1000 {
			t.Fatalf("max=%d: Len = %d; want 1000 (no eviction)", max, c.Len())
		}
		if _, ok := c.Get("k0"); !ok {
			t.Fatalf("max=%d: earliest key evicted from unbounded cache", max)
		}
	}
}

func TestTTLExpiryWithClock(t *testing.T) {
	c := New(8, 100*time.Millisecond)
	base := time.Unix(1000, 0)
	current := base
	var mu sync.Mutex
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}

	c.Set("k", resp("id", "v"))
	if _, ok := c.Get("k"); !ok {
		t.Fatal("Get(k) miss before expiry")
	}

	// Advance just short of the TTL: still present.
	current = base.Add(99 * time.Millisecond)
	if _, ok := c.Get("k"); !ok {
		t.Fatal("Get(k) miss at 99ms; entry expired too early")
	}

	// Advance past the TTL: absent and lazily pruned.
	current = base.Add(150 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("Get(k) hit after expiry")
	}
	if c.Len() != 0 {
		t.Fatalf("Len = %d after expired Get; want 0 (lazy prune)", c.Len())
	}

	// Re-Set restarts the TTL from the current clock.
	c.Set("k", resp("id2", "v2"))
	current = base.Add(200 * time.Millisecond) // 50ms into new TTL
	if _, ok := c.Get("k"); !ok {
		t.Fatal("Get(k) miss; Set should have restarted TTL")
	}
}

func TestTTLExpiryRealClock(t *testing.T) {
	c := New(8, 15*time.Millisecond)
	c.Set("k", resp("id", "v"))
	if _, ok := c.Get("k"); !ok {
		t.Fatal("Get(k) miss immediately after Set")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("Get(k) hit after real-clock expiry")
	}
}

func TestNoExpiryWhenTTLZero(t *testing.T) {
	c := New(8, 0)
	base := time.Unix(1000, 0)
	c.now = func() time.Time { return base.Add(1000 * time.Hour) }
	c.Set("k", resp("id", "v"))
	if _, ok := c.Get("k"); !ok {
		t.Fatal("Get(k) miss with ttl<=0; entries should never expire")
	}
}

func TestDeepCopyIsolation(t *testing.T) {
	c := New(4, 0)

	orig := resp("id", "hello")
	orig.Choices[0].Message.Content = []any{
		map[string]any{"type": "text", "text": "hi"},
	}
	c.Set("k", orig)

	// Mutating the original after Set must not change what is cached.
	orig.ID = "mutated"
	orig.Choices[0].Message.Content = "mutated"
	orig.Usage.TotalTokens = 999
	orig.Choices[0].FinishReason = strptr("length")

	got, ok := c.Get("k")
	if !ok {
		t.Fatal("Get(k) miss")
	}
	if got.ID != "id" {
		t.Errorf("cached ID = %q; caller mutation leaked in", got.ID)
	}
	if got.Usage.TotalTokens != 3 {
		t.Errorf("cached Usage.TotalTokens = %d; want 3", got.Usage.TotalTokens)
	}
	if *got.Choices[0].FinishReason != "stop" {
		t.Errorf("cached FinishReason = %q; want stop", *got.Choices[0].FinishReason)
	}
	parts, ok := got.Choices[0].Message.Content.([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("cached Content = %#v; want 1-element []any", got.Choices[0].Message.Content)
	}

	// Mutating the returned copy must not corrupt the cache either.
	parts[0].(map[string]any)["text"] = "corrupted"
	got.Usage.TotalTokens = -1

	again, _ := c.Get("k")
	if again.Choices[0].Message.Content.([]any)[0].(map[string]any)["text"] != "hi" {
		t.Error("mutation of returned Content leaked back into cache")
	}
	if again.Usage.TotalTokens != 3 {
		t.Errorf("mutation of returned Usage leaked back into cache: %d", again.Usage.TotalTokens)
	}
}

func TestKeyStability(t *testing.T) {
	a := &types.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []types.Message{{Role: "user", Content: "hi"}},
		Temperature: fptr(0.7),
		MaxTokens:   iptr(64),
		Extra: map[string]json.RawMessage{
			"logprobs": json.RawMessage(`true`),
			"metadata": json.RawMessage(`{"a":1,"b":2}`),
		},
	}
	// Identical logical request, Extra map built in a different order and
	// with differently-ordered nested keys in the raw passthrough value.
	b := &types.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []types.Message{{Role: "user", Content: "hi"}},
		Temperature: fptr(0.7),
		MaxTokens:   iptr(64),
		Extra: map[string]json.RawMessage{
			"metadata": json.RawMessage(`{"b":2,"a":1}`),
			"logprobs": json.RawMessage(`true`),
		},
	}
	if Key(a) != Key(b) {
		t.Fatalf("Key not stable across equal requests:\n a=%s\n b=%s", Key(a), Key(b))
	}
	// Idempotent.
	if Key(a) != Key(a) {
		t.Fatal("Key not idempotent for the same request")
	}
}

func TestKeyDistinguishes(t *testing.T) {
	base := func() *types.ChatRequest {
		return &types.ChatRequest{
			Model:       "gpt-4o",
			Messages:    []types.Message{{Role: "user", Content: "hi"}},
			Temperature: fptr(0.7),
		}
	}
	baseKey := Key(base())

	tests := []struct {
		name   string
		mutate func(r *types.ChatRequest)
	}{
		{"model", func(r *types.ChatRequest) { r.Model = "gpt-4o-mini" }},
		{"message_content", func(r *types.ChatRequest) { r.Messages[0].Content = "hello" }},
		{"message_role", func(r *types.ChatRequest) { r.Messages[0].Role = "system" }},
		{"extra_message", func(r *types.ChatRequest) {
			r.Messages = append(r.Messages, types.Message{Role: "user", Content: "again"})
		}},
		{"temperature", func(r *types.ChatRequest) { r.Temperature = fptr(0.1) }},
		{"temperature_nil", func(r *types.ChatRequest) { r.Temperature = nil }},
		{"seed", func(r *types.ChatRequest) { r.Seed = iptr(7) }},
		{"max_tokens", func(r *types.ChatRequest) { r.MaxTokens = iptr(1) }},
		{"stop", func(r *types.ChatRequest) { r.Stop = []any{"\n"} }},
		{"extra_param", func(r *types.ChatRequest) {
			r.Extra = map[string]json.RawMessage{"logprobs": json.RawMessage(`true`)}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base()
			tt.mutate(r)
			if Key(r) == baseKey {
				t.Errorf("Key unchanged after mutating %s", tt.name)
			}
		})
	}
}

func TestKeyIgnoresTransportFields(t *testing.T) {
	base := &types.ChatRequest{
		Model:    "gpt-4o",
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	}
	variant := &types.ChatRequest{
		Model:         "gpt-4o",
		Messages:      []types.Message{{Role: "user", Content: "hi"}},
		Stream:        true,
		User:          "user-123",
		StreamOptions: map[string]any{"include_usage": true},
	}
	if Key(base) != Key(variant) {
		t.Fatal("Key must ignore stream/user/stream_options")
	}
}

func TestKeyIsSHA256Hex(t *testing.T) {
	k := Key(&types.ChatRequest{Model: "m"})
	if len(k) != 64 {
		t.Fatalf("Key length = %d; want 64 hex chars", len(k))
	}
	for _, r := range k {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("Key contains non-hex char %q: %s", r, k)
		}
	}
	// Nil request must not panic and must be stable.
	if Key(nil) != Key(nil) {
		t.Fatal("Key(nil) not stable")
	}
}

func TestConcurrentGetSet(t *testing.T) {
	c := New(64, 50*time.Millisecond)
	const goroutines = 32
	const iterations = 500

	var wg sync.WaitGroup
	var hits int64
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := fmt.Sprintf("k%d", (g+i)%128)
				if i%2 == 0 {
					c.Set(key, resp(fmt.Sprintf("id-%d-%d", g, i), "v"))
				} else {
					if _, ok := c.Get(key); ok {
						atomic.AddInt64(&hits, 1)
					}
				}
				_ = c.Len()
			}
		}(g)
	}
	wg.Wait()

	if l := c.Len(); l < 0 || l > 64 {
		t.Fatalf("Len = %d after concurrent load; want within [0,64]", l)
	}
}
