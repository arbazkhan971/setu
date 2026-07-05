// Package server exposes an OpenAI-compatible HTTP API in front of the
// gateway: /v1/chat/completions (streaming and non-streaming),
// /v1/models, and /health.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/arbazkhan971/setu/gateway"
	"github.com/arbazkhan971/setu/types"
)

// Server wires the gateway to an HTTP mux.
type Server struct {
	gw        *gateway.Gateway
	masterKey string
	log       *slog.Logger
}

// New builds a Server. If masterKey is non-empty, /v1 routes require a
// matching Bearer token.
func New(gw *gateway.Gateway, masterKey string) *Server {
	return &Server{gw: gw, masterKey: masterKey, log: slog.Default()}
}

// Handler returns the fully-wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /v1/models", s.auth(s.models))
	mux.HandleFunc("POST /v1/chat/completions", s.auth(s.chat))
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

	if req.Stream {
		s.streamChat(w, r, &req)
		return
	}
	resp, err := s.gw.ChatCompletion(r.Context(), &req)
	if err != nil {
		if errors.Is(err, gateway.ErrModelNotFound) {
			writeError(w, http.StatusNotFound, "invalid_request_error", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req *types.ChatRequest) {
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

	emit := func(c types.ChatChunk) error {
		if c.Object == "" {
			c.Object = "chat.completion.chunk"
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
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// --- middleware ---

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.masterKey != "" {
			got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.masterKey)) != 1 {
				writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid or missing API key")
				return
			}
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
