package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbazkhan971/setu/cache"
	"github.com/arbazkhan971/setu/gateway"
	"github.com/arbazkhan971/setu/metrics"
	"github.com/arbazkhan971/setu/policy"
	"github.com/arbazkhan971/setu/pricing"
	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/providers/mock"
	"github.com/arbazkhan971/setu/types"
)

// countingProvider counts upstream calls, for cache tests.
type countingProvider struct{ n *int32 }

func (c countingProvider) Name() string { return "count" }

func (c countingProvider) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	atomic.AddInt32(c.n, 1)
	fr := "stop"
	return &types.ChatResponse{
		Model:   req.Model,
		Choices: []types.Choice{{Message: &types.Message{Role: "assistant", Content: "hi"}, FinishReason: &fr}},
		Usage:   &types.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

func (c countingProvider) ChatCompletionStream(_ context.Context, req *types.ChatRequest, emit provider.StreamFunc) error {
	atomic.AddInt32(c.n, 1)
	return emit(types.ChatChunk{Model: req.Model, Choices: []types.ChunkChoice{{Delta: types.Delta{Content: "hi"}}}})
}

func newTestServer(t *testing.T, masterKey string) http.Handler {
	t.Helper()
	p, err := mock.New(provider.Options{})
	if err != nil {
		t.Fatalf("mock: %v", err)
	}
	gw := gateway.New([]*gateway.Deployment{{ModelName: "test", Provider: p}})
	return New(gw, masterKey).Handler()
}

func post(t *testing.T, h http.Handler, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func chatBody(stream bool) types.ChatRequest {
	return types.ChatRequest{
		Model:    "test",
		Stream:   stream,
		Messages: []types.Message{{Role: "user", Content: "ping"}},
	}
}

func TestHealth(t *testing.T) {
	h := newTestServer(t, "")
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d", w.Code)
	}
}

func TestChatNonStreaming(t *testing.T) {
	h := newTestServer(t, "")
	w := post(t, h, "/v1/chat/completions", "", chatBody(false))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.ChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := resp.Choices[0].Message.Content.(string); got != "echo: ping" {
		t.Fatalf("content = %q", got)
	}
}

func TestChatStreaming(t *testing.T) {
	h := newTestServer(t, "")
	w := post(t, h, "/v1/chat/completions", "", chatBody(true))
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("no SSE data frames: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream not terminated with [DONE]: %s", body)
	}
	if !strings.Contains(body, "echo") {
		t.Fatalf("stream missing content: %s", body)
	}
}

func TestModels(t *testing.T) {
	h := newTestServer(t, "")
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "test") {
		t.Fatalf("models missing 'test': %s", w.Body.String())
	}
}

func TestAuthRequired(t *testing.T) {
	h := newTestServer(t, "secret")

	// missing key -> 401
	w := post(t, h, "/v1/chat/completions", "", chatBody(false))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", w.Code)
	}
	// wrong key -> 401
	w = post(t, h, "/v1/chat/completions", "wrong", chatBody(false))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong key, got %d", w.Code)
	}
	// correct key -> 200
	w = post(t, h, "/v1/chat/completions", "secret", chatBody(false))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct key, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestUnknownModelReturns404(t *testing.T) {
	h := newTestServer(t, "")
	w := post(t, h, "/v1/chat/completions", "", types.ChatRequest{
		Model:    "ghost",
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown model, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_request_error") {
		t.Fatalf("expected invalid_request_error, got %s", w.Body.String())
	}
}

func TestUnknownModelStreamReturns404NotSSE(t *testing.T) {
	h := newTestServer(t, "")
	w := post(t, h, "/v1/chat/completions", "", types.ChatRequest{
		Model:    "ghost",
		Stream:   true,
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown streaming model, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("unknown model must not open an event-stream, got content-type %q", ct)
	}
}

func newPolicyServer(t *testing.T) http.Handler {
	t.Helper()
	p, err := mock.New(provider.Options{})
	if err != nil {
		t.Fatal(err)
	}
	gw := gateway.New([]*gateway.Deployment{
		{ModelName: "test", Provider: p},
		{ModelName: "secret", Provider: p},
	})
	enf := policy.New([]*policy.Key{
		{Secret: "sk-scoped", Name: "scoped", Models: []string{"test"}},
	}, pricing.Default())
	return New(gw, "master").WithPolicy(enf).Handler()
}

func TestVirtualKeyAuth(t *testing.T) {
	h := newPolicyServer(t)

	// Unknown key -> 401.
	if w := post(t, h, "/v1/chat/completions", "bogus", chatBody(false)); w.Code != http.StatusUnauthorized {
		t.Fatalf("bogus key: got %d", w.Code)
	}
	// Valid scoped key on allowed model -> 200.
	if w := post(t, h, "/v1/chat/completions", "sk-scoped", chatBody(false)); w.Code != http.StatusOK {
		t.Fatalf("scoped key allowed model: got %d body=%s", w.Code, w.Body.String())
	}
	// Master key acts as admin -> 200 on any model.
	body := types.ChatRequest{Model: "secret", Messages: []types.Message{{Role: "user", Content: "hi"}}}
	if w := post(t, h, "/v1/chat/completions", "master", body); w.Code != http.StatusOK {
		t.Fatalf("master admin: got %d", w.Code)
	}
}

func TestVirtualKeyModelNotAllowed(t *testing.T) {
	h := newPolicyServer(t)
	body := types.ChatRequest{Model: "secret", Messages: []types.Message{{Role: "user", Content: "hi"}}}
	w := post(t, h, "/v1/chat/completions", "sk-scoped", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed model, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestKeyInfoTracksSpend(t *testing.T) {
	h := newPolicyServer(t)
	// Make a request, then read /v1/key/info.
	post(t, h, "/v1/chat/completions", "sk-scoped", chatBody(false))
	r := httptest.NewRequest(http.MethodGet, "/v1/key/info", nil)
	r.Header.Set("Authorization", "Bearer sk-scoped")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("key/info status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "\"requests\":1") {
		t.Fatalf("key/info did not record the request: %s", w.Body.String())
	}
}

func TestResponseCacheAvoidsUpstream(t *testing.T) {
	var n int32
	gw := gateway.New([]*gateway.Deployment{{ModelName: "test", Provider: countingProvider{&n}}})
	h := New(gw, "").WithCache(cache.New(10, time.Minute)).Handler()

	body := chatBody(false)
	if w := post(t, h, "/v1/chat/completions", "", body); w.Code != http.StatusOK {
		t.Fatalf("first request: %d", w.Code)
	}
	if w := post(t, h, "/v1/chat/completions", "", body); w.Code != http.StatusOK {
		t.Fatalf("second request: %d", w.Code)
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("expected exactly 1 upstream call with caching, got %d", got)
	}
}

func TestRateLimitPerKey(t *testing.T) {
	p, _ := mock.New(provider.Options{})
	gw := gateway.New([]*gateway.Deployment{{ModelName: "test", Provider: p}})
	enf := policy.New([]*policy.Key{{Secret: "sk-rl", Name: "rl", RPM: 1}}, pricing.Default())
	h := New(gw, "").WithPolicy(enf).Handler()

	if w := post(t, h, "/v1/chat/completions", "sk-rl", chatBody(false)); w.Code != http.StatusOK {
		t.Fatalf("first request within limit: %d", w.Code)
	}
	if w := post(t, h, "/v1/chat/completions", "sk-rl", chatBody(false)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request should be 429, got %d", w.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	p, _ := mock.New(provider.Options{})
	gw := gateway.New([]*gateway.Deployment{{ModelName: "test", Provider: p}})
	h := New(gw, "").WithMetrics(metrics.New()).Handler()

	post(t, h, "/v1/chat/completions", "", chatBody(false))

	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "setu_requests_total") {
		t.Fatalf("metrics missing setu_requests_total:\n%s", w.Body.String())
	}
}

func TestMissingModel(t *testing.T) {
	h := newTestServer(t, "")
	w := post(t, h, "/v1/chat/completions", "", types.ChatRequest{
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing model, got %d", w.Code)
	}
}
