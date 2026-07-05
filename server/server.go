// Package server exposes an OpenAI-compatible HTTP API in front of the
// gateway: /v1/chat/completions (streaming and non-streaming),
// /v1/models, and /health.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/arbazkhan971/setu/cache"
	"github.com/arbazkhan971/setu/gateway"
	"github.com/arbazkhan971/setu/metrics"
	"github.com/arbazkhan971/setu/policy"
	"github.com/arbazkhan971/setu/ratelimit"
	"github.com/arbazkhan971/setu/types"
)

// Server wires the gateway to an HTTP mux.
type Server struct {
	gw        *gateway.Gateway
	masterKey string
	policy    *policy.Enforcer
	metrics   *metrics.Registry
	cache     *cache.Cache
	limiter   *ratelimit.Limiter
	log       *slog.Logger
}

// New builds a Server. If masterKey is non-empty, /v1 routes require a
// matching Bearer token.
func New(gw *gateway.Gateway, masterKey string) *Server {
	return &Server{gw: gw, masterKey: masterKey, limiter: ratelimit.New(), log: slog.Default()}
}

// WithMetrics enables Prometheus metrics collection and the /metrics endpoint.
func (s *Server) WithMetrics(m *metrics.Registry) *Server {
	s.metrics = m
	return s
}

// WithCache enables response caching for non-streaming requests.
func (s *Server) WithCache(c *cache.Cache) *Server {
	s.cache = c
	return s
}

// WithPolicy attaches a virtual-key enforcer. When set (and it has keys),
// /v1 routes require a valid virtual key; the master key still works as an
// unrestricted admin credential.
func (s *Server) WithPolicy(e *policy.Enforcer) *Server {
	s.policy = e
	return s
}

// ctxKeyType is the context key under which the resolved virtual key is stored.
type ctxKeyType struct{}

func withKey(ctx context.Context, k *policy.Key) context.Context {
	return context.WithValue(ctx, ctxKeyType{}, k)
}

func keyFrom(ctx context.Context) *policy.Key {
	k, _ := ctx.Value(ctxKeyType{}).(*policy.Key)
	return k
}

// adminKey is the synthetic key attributed to master-key (admin) requests.
var adminKey = &policy.Key{Name: "admin"}

// Handler returns the fully-wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /v1/models", s.auth(s.models))
	mux.HandleFunc("GET /v1/key/info", s.auth(s.keyInfo))
	mux.HandleFunc("POST /v1/chat/completions", s.auth(s.chat))
	if s.metrics != nil {
		mux.Handle("GET /metrics", s.metrics.Handler())
	}
	return s.logging(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "setu"})
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list"}
	for _, n := range s.gw.Models() {
		out.Data = append(out.Data, model{ID: n, Object: "model", OwnedBy: "setu"})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req types.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "could not parse request body: "+err.Error())
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "'model' is required")
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "'messages' is required")
		return
	}

	key, ok := s.authorize(w, r, req.Model)
	if !ok {
		return
	}
	start := time.Now()

	if req.Stream {
		s.streamChat(w, r, &req, key, start)
		return
	}

	// Response cache (non-streaming). A hit is free: no upstream call, no spend.
	var cacheKey string
	if s.cache != nil {
		cacheKey = cache.Key(&req)
		if hit, found := s.cache.Get(cacheKey); found {
			s.observe(req.Model, "cache_hit", start, nil)
			writeJSON(w, http.StatusOK, hit)
			return
		}
	}

	resp, err := s.gw.ChatCompletion(r.Context(), &req)
	if err != nil {
		s.observe(req.Model, "error", start, nil)
		if errors.Is(err, gateway.ErrModelNotFound) {
			writeError(w, http.StatusNotFound, "invalid_request_error", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	model := modelOf(resp, &req)
	s.record(key, model, resp.Usage)
	s.observe(model, "success", start, resp.Usage)
	if s.cache != nil {
		s.cache.Set(cacheKey, resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

// observe records Prometheus metrics for a completed request, if enabled.
func (s *Server) observe(model, status string, start time.Time, u *types.Usage) {
	if s.metrics == nil {
		return
	}
	s.metrics.IncRequest(model, "", status)
	s.metrics.ObserveLatency(model, time.Since(start).Seconds())
	if u != nil {
		s.metrics.AddTokens(model, u.PromptTokens, u.CompletionTokens)
	}
}

// keyInfo reports the calling key's usage and budget.
func (s *Server) keyInfo(w http.ResponseWriter, r *http.Request) {
	k := keyFrom(r.Context())
	if k == nil {
		writeError(w, http.StatusNotFound, "not_found", "virtual keys are not enabled")
		return
	}
	if s.policy == nil {
		writeJSON(w, http.StatusOK, map[string]any{"key_name": k.Name, "unlimited": true})
		return
	}
	writeJSON(w, http.StatusOK, s.policy.Info(k))
}

// authorize resolves and checks the calling key against the model. It writes
// the appropriate error and returns ok=false when the request is denied.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, model string) (*policy.Key, bool) {
	k := keyFrom(r.Context())
	if s.policy == nil || k == nil {
		return k, true
	}
	switch err := s.policy.Authorize(k, model); {
	case errors.Is(err, policy.ErrModelNotAllowed):
		writeError(w, http.StatusForbidden, "invalid_request_error",
			fmt.Sprintf("key %q is not permitted to use model %q", k.Name, model))
		return k, false
	case errors.Is(err, policy.ErrBudgetExceeded):
		writeError(w, http.StatusTooManyRequests, "insufficient_quota",
			fmt.Sprintf("budget exceeded for key %q", k.Name))
		return k, false
	}
	// Per-key requests-per-minute limit.
	if k.RPM > 0 && !s.limiter.Allow(k.Name, k.RPM) {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded",
			fmt.Sprintf("rate limit exceeded for key %q", k.Name))
		return k, false
	}
	return k, true
}

// record attributes a request's usage/cost to the calling key.
func (s *Server) record(k *policy.Key, model string, u *types.Usage) {
	if s.policy == nil || k == nil {
		return
	}
	s.policy.Record(k, model, u)
}

func modelOf(resp *types.ChatResponse, req *types.ChatRequest) string {
	if resp != nil && resp.Model != "" {
		return resp.Model
	}
	return req.Model
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req *types.ChatRequest, key *policy.Key, start time.Time) {
	// Reject unknown models before committing to a 200 event-stream, so the
	// client gets a proper 404 JSON error instead of an SSE error frame.
	if !s.gw.CanServe(req.Model) {
		writeError(w, http.StatusNotFound, "invalid_request_error",
			fmt.Sprintf("%s: %q", gateway.ErrModelNotFound.Error(), req.Model))
		return
	}
	// Locate the real underlying Flusher by walking the unwrap chain; the
	// logging middleware wraps the writer, so a direct type assertion would
	// always succeed and silently degrade streaming to a buffered no-op.
	flusher, ok := findFlusher(w)
	if !ok {
		writeError(w, http.StatusInternalServerError, "server_error", "streaming unsupported by server")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	// Capture usage and the resolved model from chunks so streaming spend can
	// be attributed to the key (present when the client sets
	// stream_options.include_usage).
	var lastUsage *types.Usage
	streamModel := req.Model
	emit := func(c types.ChatChunk) error {
		if c.Object == "" {
			c.Object = "chat.completion.chunk"
		}
		if c.Usage != nil {
			lastUsage = c.Usage
		}
		if c.Model != "" {
			streamModel = c.Model
		}
		b, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if err := s.gw.ChatCompletionStream(r.Context(), req, emit); err != nil {
		b, _ := json.Marshal(map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "upstream_error"},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		s.observe(streamModel, "error", start, nil)
	} else {
		s.record(key, streamModel, lastUsage)
		s.observe(streamModel, "success", start, lastUsage)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// --- middleware ---

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))

		// Master key, when configured, is an unrestricted admin credential.
		if s.masterKey != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.masterKey)) == 1 {
			next(w, r.WithContext(withKey(r.Context(), adminKey)))
			return
		}

		// With virtual keys configured, require a valid one.
		if s.policy.Enabled() {
			if k := s.policy.Resolve(got); k != nil {
				next(w, r.WithContext(withKey(r.Context(), k)))
				return
			}
			writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid or missing API key")
			return
		}

		// No virtual keys: fall back to master-key-only gating.
		if s.masterKey != "" {
			writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid or missing API key")
			return
		}
		next(w, r)
	}
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.Info("request",
			"method", r.Method, "path", r.URL.Path,
			"status", sw.status, "dur_ms", time.Since(start).Milliseconds())
	})
}

// statusWriter records the status code and preserves the http.Flusher
// capability needed for streaming.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap exposes the wrapped ResponseWriter so capability checks (and
// http.ResponseController) can reach its real Flusher. statusWriter
// deliberately does NOT implement http.Flusher itself, so a streaming
// handler discovers the underlying writer's true flush capability rather
// than always seeing the wrapper.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// --- helpers ---

// findFlusher walks the ResponseWriter unwrap chain to locate a real
// http.Flusher. This detects streaming capability accurately even when the
// writer is wrapped by middleware such as the logging statusWriter.
func findFlusher(w http.ResponseWriter) (http.Flusher, bool) {
	for {
		if f, ok := w.(http.Flusher); ok {
			return f, true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil, false
		}
		w = u.Unwrap()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "type": kind},
	})
}
