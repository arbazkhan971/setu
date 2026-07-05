// Package gateway routes unified chat requests across one or more
// deployments per model, applying round-robin load balancing, retries,
// and cross-model fallbacks.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/types"
)

// ErrModelNotFound is returned when neither the requested model nor any of
// its fallbacks has a configured deployment. Callers can map it to an HTTP
// 404 with error type invalid_request_error, matching OpenAI's behavior.
var ErrModelNotFound = errors.New("setu: model not found")

// Deployment is one backend serving a client-facing model name. Multiple
// deployments may share a model name to load-balance across keys/regions.
type Deployment struct {
	ModelName string
	Provider  provider.Provider
	Weight    int
}

// Gateway routes requests to deployments with load balancing, retries,
// and fallbacks. It is safe for concurrent use.
type Gateway struct {
	deployments map[string][]*Deployment
	order       []string
	fallbacks   map[string][]string
	maxRetries  int
	rr          map[string]*uint64
}

// Option configures a Gateway.
type Option func(*Gateway)

// WithFallbacks sets the per-model fallback chains.
func WithFallbacks(f map[string][]string) Option {
	return func(g *Gateway) {
		if f != nil {
			g.fallbacks = f
		}
	}
}

// WithMaxRetries sets how many times a single model is retried before
// moving to its fallbacks.
func WithMaxRetries(n int) Option {
	return func(g *Gateway) {
		if n >= 0 {
			g.maxRetries = n
		}
	}
}

// New builds a Gateway from a set of deployments.
func New(deployments []*Deployment, opts ...Option) *Gateway {
	g := &Gateway{
		deployments: map[string][]*Deployment{},
		fallbacks:   map[string][]string{},
		maxRetries:  2,
		rr:          map[string]*uint64{},
	}
	for _, d := range deployments {
		g.deployments[d.ModelName] = append(g.deployments[d.ModelName], d)
	}
	for name := range g.deployments {
		var c uint64
		g.rr[name] = &c
		g.order = append(g.order, name)
	}
	sort.Strings(g.order)
	for _, o := range opts {
		o(g)
	}
	return g
}

// Models returns the sorted list of served model names.
func (g *Gateway) Models() []string {
	out := make([]string, len(g.order))
	copy(out, g.order)
	return out
}

// CanServe reports whether the model, or one of its fallbacks, has at least
// one deployment. It lets the server reject unknown models before it commits
// to a streaming response.
func (g *Gateway) CanServe(model string) bool {
	for _, m := range g.chain(model) {
		if len(g.deployments[m]) > 0 {
			return true
		}
	}
	return false
}

// rotated returns the model's deployments ordered from a per-request starting
// offset. Taking the round-robin offset once per request (rather than once per
// attempt) load-balances across requests while guaranteeing that a single
// request's retries cover every distinct deployment, even under concurrency.
func (g *Gateway) rotated(model string) []*Deployment {
	ds := g.deployments[model]
	if len(ds) <= 1 {
		return ds
	}
	start := int((atomic.AddUint64(g.rr[model], 1) - 1) % uint64(len(ds)))
	out := make([]*Deployment, len(ds))
	for i := range ds {
		out[i] = ds[(start+i)%len(ds)]
	}
	return out
}

// retryable reports whether an error is worth another attempt or a fallback.
// A typed provider.APIError decides from its HTTP status; any other error
// (network failure, decode error) is treated as retryable.
func retryable(err error) bool {
	var apiErr *provider.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	return true
}

// chunkHasPayload reports whether a stream chunk carries real output — content,
// tool calls, or a finish reason — as opposed to a content-free role delta.
// The streaming router uses this so an eager role delta does not defeat
// pre-first-token fallback.
func chunkHasPayload(c types.ChatChunk) bool {
	for _, ch := range c.Choices {
		if ch.Delta.Content != "" || ch.FinishReason != nil || len(ch.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// chain returns the ordered list of models to try: the requested model
// first, then its fallbacks, de-duplicated.
func (g *Gateway) chain(model string) []string {
	seen := map[string]bool{model: true}
	chain := []string{model}
	for _, fb := range g.fallbacks[model] {
		if !seen[fb] {
			seen[fb] = true
			chain = append(chain, fb)
		}
	}
	return chain
}

func (g *Gateway) attempts(model string) int {
	n := g.maxRetries + 1
	if ds := len(g.deployments[model]); ds > n {
		n = ds // ensure every deployment gets a shot
	}
	return n
}

// ChatCompletion routes a non-streaming request, retrying and falling
// back on error.
func (g *Gateway) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	var lastErr error
	for _, model := range g.chain(req.Model) {
		ds := g.rotated(model)
		if len(ds) == 0 {
			continue
		}
		for i := 0; i < g.attempts(model); i++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			resp, err := ds[i%len(ds)].Provider.ChatCompletion(ctx, req)
			if err == nil {
				return resp, nil
			}
			lastErr = err
			if !retryable(err) {
				return nil, err // client error: retry/fallback would not help
			}
		}
	}
	if lastErr == nil {
		return nil, fmt.Errorf("%w: %q", ErrModelNotFound, req.Model)
	}
	return nil, lastErr
}

// ChatCompletionStream routes a streaming request. A fallback is only
// attempted while no bytes have been emitted; once streaming begins an
// upstream failure is surfaced to the client.
func (g *Gateway) ChatCompletionStream(ctx context.Context, req *types.ChatRequest, emit provider.StreamFunc) error {
	var lastErr error
	for _, model := range g.chain(req.Model) {
		ds := g.rotated(model)
		if len(ds) == 0 {
			continue
		}
		for i := 0; i < g.attempts(model); i++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sent := false
			wrapped := func(c types.ChatChunk) error {
				if chunkHasPayload(c) {
					sent = true
				}
				return emit(c)
			}
			err := ds[i%len(ds)].Provider.ChatCompletionStream(ctx, req, wrapped)
			if err == nil {
				return nil
			}
			lastErr = err
			if sent {
				return err // real output already delivered; cannot safely re-stream
			}
			if !retryable(err) {
				return err // client error: retry/fallback would not help
			}
		}
	}
	if lastErr == nil {
		return fmt.Errorf("%w: %q", ErrModelNotFound, req.Model)
	}
	return lastErr
}
