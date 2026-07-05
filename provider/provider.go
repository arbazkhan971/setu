// Package provider defines the Provider interface every upstream LLM
// backend implements, along with a name-based registry used by config to
// construct providers at startup.
package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"github.com/arbazkhan971/setu/types"
)

// StreamFunc receives each unified streaming chunk. Returning a non-nil
// error aborts the stream and is propagated to the caller.
type StreamFunc func(types.ChatChunk) error

// Provider is implemented by every upstream LLM backend. A Provider
// translates Setu's unified schema to and from a specific vendor API.
type Provider interface {
	// Name returns the provider identifier, e.g. "openai".
	Name() string
	// ChatCompletion performs a non-streaming chat completion.
	ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
	// ChatCompletionStream performs a streaming chat completion, invoking
	// emit for each chunk in unified (OpenAI) chunk format.
	ChatCompletionStream(ctx context.Context, req *types.ChatRequest, emit StreamFunc) error
}

// Options configures a single provider instance.
type Options struct {
	APIKey     string            // upstream credential
	BaseURL    string            // override vendor base URL (self-host, proxy, Azure)
	Model      string            // upstream model id; overrides the request model
	Headers    map[string]string // extra HTTP headers
	Params     map[string]any    // raw params block from config (default params)
	HTTPClient *http.Client
}

// Client returns the configured HTTP client or a sane default.
func (o Options) Client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return http.DefaultClient
}

// APIError is returned by a provider for a non-2xx upstream response. It
// carries the HTTP status so the gateway can decide whether an error is
// worth retrying or falling back on.
type APIError struct {
	Provider   string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: upstream status %d: %s", e.Provider, e.StatusCode, e.Body)
}

// Retryable reports whether the error warrants a retry or fallback. Network
// errors (StatusCode 0), 429 Too Many Requests, and 5xx are retryable; other
// 4xx responses are client errors that will not succeed on retry.
func (e *APIError) Retryable() bool {
	return e.StatusCode == 0 ||
		e.StatusCode == http.StatusTooManyRequests ||
		e.StatusCode >= http.StatusInternalServerError
}

// Factory builds a Provider from Options.
type Factory func(Options) (Provider, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a provider factory available by name. It is intended to
// be called from a provider package's init function.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	factories[name] = f
}

// New constructs a registered provider by name.
func New(name string, opts Options) (Provider, error) {
	mu.RLock()
	f, ok := factories[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("setu: unknown provider %q (registered: %v)", name, Registered())
	}
	return f(opts)
}

// Registered returns the sorted names of all registered providers.
func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
