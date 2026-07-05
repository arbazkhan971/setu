// Package cache provides a concurrency-safe, in-memory cache of chat
// completion responses keyed by a stable content hash of the request.
//
// Entries are evicted in least-recently-used (LRU) order once the cache
// exceeds its configured capacity, and expire after a configured
// time-to-live (TTL). Expired entries are treated as absent and removed
// lazily on access. Get and Set are O(1), backed by a container/list
// doubly linked list plus a map.
//
// All exported methods are safe for concurrent use by multiple goroutines.
package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/arbazkhan971/setu/types"
)

// entry is the value stored in each list element. The key is retained so
// that an element evicted from the back of the list can be removed from
// the index map in O(1).
type entry struct {
	key      string
	resp     *types.ChatResponse
	expireAt time.Time // zero means the entry never expires
}

// Cache is an in-memory LRU cache of chat responses with per-entry TTL.
// The zero value is not usable; construct one with New.
type Cache struct {
	mu         sync.Mutex
	maxEntries int                      // <= 0 means unbounded
	ttl        time.Duration            // <= 0 means no expiry
	ll         *list.List               // front = most recently used
	items      map[string]*list.Element // key -> element in ll
	now        func() time.Time         // injectable clock for testing
}

// New returns a Cache holding at most maxEntries entries, each living for
// ttl. If maxEntries <= 0 the cache is unbounded (no LRU eviction). If
// ttl <= 0 entries never expire.
func New(maxEntries int, ttl time.Duration) *Cache {
	return &Cache{
		maxEntries: maxEntries,
		ttl:        ttl,
		ll:         list.New(),
		items:      make(map[string]*list.Element),
		now:        time.Now,
	}
}

// Get returns a deep copy of the cached response for key and true, or nil
// and false if the key is missing or its entry has expired. Expired
// entries are removed as a side effect. A successful Get marks the entry
// as most recently used.
func (c *Cache) Get(key string) (*types.ChatResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	ent := el.Value.(*entry)
	if c.expired(ent) {
		c.removeElement(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return copyResponse(ent.resp), true
}

// Set inserts or updates the response stored under key. A deep copy is
// stored, so later mutations of resp by the caller do not affect the
// cache. The entry becomes most recently used, its TTL is (re)started,
// and the least recently used entries are evicted while the cache is over
// capacity.
func (c *Cache) Set(key string, resp *types.ChatResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stored := copyResponse(resp)
	exp := c.expiry()

	if el, ok := c.items[key]; ok {
		ent := el.Value.(*entry)
		ent.resp = stored
		ent.expireAt = exp
		c.ll.MoveToFront(el)
		return
	}

	el := c.ll.PushFront(&entry{key: key, resp: stored, expireAt: exp})
	c.items[key] = el

	if c.maxEntries > 0 {
		for c.ll.Len() > c.maxEntries {
			if oldest := c.ll.Back(); oldest != nil {
				c.removeElement(oldest)
			}
		}
	}
}

// Len reports the number of entries currently stored, including any that
// have expired but not yet been accessed (expired entries are pruned
// lazily on Get). It is safe for concurrent use.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// expiry computes the absolute expiry time for a freshly written entry, or
// the zero time when the cache has no TTL. Caller must hold c.mu.
func (c *Cache) expiry() time.Time {
	if c.ttl <= 0 {
		return time.Time{}
	}
	return c.now().Add(c.ttl)
}

// expired reports whether ent has passed its expiry time. Caller must hold
// c.mu.
func (c *Cache) expired(ent *entry) bool {
	return !ent.expireAt.IsZero() && c.now().After(ent.expireAt)
}

// removeElement detaches el from both the list and the index map. Caller
// must hold c.mu.
func (c *Cache) removeElement(el *list.Element) {
	c.ll.Remove(el)
	delete(c.items, el.Value.(*entry).key)
}

// Key returns a stable sha256 hex digest of the request fields that affect
// the completion content: the model, messages, and generation parameters.
// Purely transport- or identity-oriented fields (stream, stream_options,
// user) are excluded. The digest is independent of Go map iteration order,
// so two logically identical requests always produce the same key.
func Key(req *types.ChatRequest) string {
	if req == nil {
		return hashBytes([]byte("null"))
	}

	payload := keyPayload{
		Model:            req.Model,
		Messages:         req.Messages,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		N:                req.N,
		Stop:             req.Stop,
		MaxTokens:        req.MaxTokens,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		Seed:             req.Seed,
		Tools:            req.Tools,
		ToolChoice:       req.ToolChoice,
		ResponseFormat:   req.ResponseFormat,
		Extra:            req.Extra,
	}

	b, err := json.Marshal(payload)
	if err != nil {
		// A ChatRequest built from valid JSON marshals cleanly; if it
		// somehow does not, fall back to a best-effort stable token.
		return hashBytes([]byte("setu-cache-key-error:" + req.Model))
	}
	return hashBytes(canonicalize(b))
}

// keyPayload is the projection of a ChatRequest used to compute a cache
// key. Fields are declared in a fixed order; encoding/json emits struct
// fields in declaration order and map keys in sorted order, making the
// marshaled form deterministic for equal inputs.
type keyPayload struct {
	Model            string                     `json:"model"`
	Messages         []types.Message            `json:"messages"`
	Temperature      *float64                   `json:"temperature,omitempty"`
	TopP             *float64                   `json:"top_p,omitempty"`
	N                *int                       `json:"n,omitempty"`
	Stop             any                        `json:"stop,omitempty"`
	MaxTokens        *int                       `json:"max_tokens,omitempty"`
	PresencePenalty  *float64                   `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64                   `json:"frequency_penalty,omitempty"`
	Seed             *int                       `json:"seed,omitempty"`
	Tools            []types.Tool               `json:"tools,omitempty"`
	ToolChoice       any                        `json:"tool_choice,omitempty"`
	ResponseFormat   any                        `json:"response_format,omitempty"`
	Extra            map[string]json.RawMessage `json:"extra,omitempty"`
}

// canonicalize re-encodes JSON so that every nested object has its keys in
// sorted order and all insignificant whitespace is removed. Passing raw
// bytes through encoding/json's decode/encode cycle normalizes ordering
// inside json.RawMessage passthrough values as well, which json.Marshal
// alone would leave untouched. If b is not valid JSON it is returned as-is.
func canonicalize(b []byte) []byte {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

// hashBytes returns the lowercase hex sha256 digest of b.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// copyResponse returns a deep-enough copy of r so that mutations to either
// the argument or the returned value cannot affect the other. Immutable
// scalars are shared; slices, maps, and pointer fields are cloned.
func copyResponse(r *types.ChatResponse) *types.ChatResponse {
	if r == nil {
		return nil
	}
	cp := *r // copies scalar fields (ID, Object, Created, Model)

	if r.Choices != nil {
		cp.Choices = make([]types.Choice, len(r.Choices))
		for i, ch := range r.Choices {
			cc := ch
			if ch.Message != nil {
				cc.Message = copyMessage(ch.Message)
			}
			if ch.FinishReason != nil {
				fr := *ch.FinishReason
				cc.FinishReason = &fr
			}
			cp.Choices[i] = cc
		}
	}

	if r.Usage != nil {
		u := *r.Usage
		cp.Usage = &u
	}
	return &cp
}

// copyMessage deep-copies a message, including its arbitrary Content and
// any tool calls.
func copyMessage(m *types.Message) *types.Message {
	cm := *m
	cm.Content = copyAny(m.Content)
	if m.ToolCalls != nil {
		cm.ToolCalls = make([]types.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			ct := tc // FunctionCall is scalar-only; safe to copy by value
			if tc.Index != nil {
				idx := *tc.Index
				ct.Index = &idx
			}
			cm.ToolCalls[i] = ct
		}
	}
	return &cm
}

// copyAny deep-copies the JSON-shaped value graphs that appear in a
// message's Content: nested slices and maps are cloned recursively, while
// immutable scalars (strings, numbers, bools, nil) are returned as-is.
func copyAny(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = copyAny(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = copyAny(e)
		}
		return out
	default:
		return v
	}
}
