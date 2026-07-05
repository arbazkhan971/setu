package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arbazkhan971/setu/gateway"
	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/providers/mock"
	"github.com/arbazkhan971/setu/types"
)

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

func TestMissingModel(t *testing.T) {
	h := newTestServer(t, "")
	w := post(t, h, "/v1/chat/completions", "", types.ChatRequest{
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing model, got %d", w.Code)
	}
}
