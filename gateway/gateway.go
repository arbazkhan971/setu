// Package gateway routes unified chat requests across one or more
// deployments per model, applying weighted load balancing, retries, and
// cross-model fallbacks.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/types"
)

// ErrModelNotFound is returned when neither the requested model nor any of
// its fallbacks has a configured deployment. Callers can map it to an HTTP
// 404 with error type invalid_request_error, matching OpenAI's behavior.
var ErrModelNotFound = errors.New("setu: model not found")

// Deployment is one backend serving a client-facing model name. Multiple
// deployments may share a model name to load-balance across keys/regions.
// Weight biases how often a deployment is chosen as the primary target
// (default 1); it does not affect retry coverage.
type Deployment struct {
	ModelName string
	Provider  provider.Provider
	Weight    int
}

func (d *Deployment) weight() int {
	if d.Weight <= 0 {
		return 1
	}
	return d.Weight
}

// pool holds the deployments for one model name plus the smooth weighted
// round-robin (SWRR) state used to pick a primary target.
type pool struct {
	deployments []*Deployment
	total       int
	mu          sync.Mutex
	current     []int // SWRR current weights, one per deployment
}

func newPool(ds []*Deployment) *pool {
	p := &pool{deployments: ds, current: make([]int, len(ds))}
	for _, d := range ds {
		p.total += d.weight()
	}
	return p
}

// startIndex returns the next primary deployment index using SWRR, so over
// many requests each deployment is chosen in proportion to its weight while
// the sequence stays smooth. It is safe for concurrent use.
func (p *pool) startIndex() int {
	if len(p.deployments) == 1 {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	best := 0
	for i, d := range p.deployments {
		p.current[i] += d.weight()
		if p.current[i] > p.current[best] {
			best = i
		}
	}
	p.current[best] -= p.total
	return best
}

// ordered returns the deployments starting at the SWRR-chosen primary,
// followed by the rest in order. A single request therefore covers every
// distinct deployment on retry, regardless of concurrency.
func (p *pool) ordered() []*Deployment {
	ds := p.deployments
	if len(ds) <= 1 {
		return ds
	}
	start := p.startIndex()
	out := make([]*Deployment, len(ds))
	for i := range ds {
		out[i] = ds[(start+i)%len(ds)]
	}
	return out
}

// Gateway routes requests to deployments with weighted load balancing,
// retries, and fallbacks. It is safe for concurrent use.
type Gateway struct {
	pools      map[string]*pool
	order      []string
	fallbacks  map[string][]string
	maxRetries int
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
	byModel := map[string][]*Deployment{}
	for _, d := range deployments {
		byModel[d.ModelName] = append(byModel[d.ModelName], d)
	}
	g := &Gateway{
		pools:      make(map[string]*pool, len(byModel)),
		fallbacks:  map[string][]string{},
		maxRetries: 2,
	}
	for name, ds := range byModel {
		g.pools[name] = newPool(ds)
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
		if p := g.pools[m]; p != nil && len(p.deployments) > 0 {
			return true
		}
	}
	return false
}

// ordered returns the deployments for a model in weighted, retry-covering
// order, or nil if the model has no pool.
func (g *Gateway) ordered(model string) []*Deployment {
	if p := g.pools[model]; p != nil {
		return p.ordered()
	}
	return nil
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

// attempts is how many times a single model is tried before its fallbacks:
// maxRetries+1, but never fewer than the number of deployments so every
// distinct backend gets a shot.
func (g *Gateway) attempts(n int) int {
	a := g.maxRetries + 1
	if n > a {
		a = n
	}
	return a
}

// ChatCompletion routes a non-streaming request, retrying and falling back
// on retryable errors.
func (g *Gateway) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	var lastErr error
	for _, model := range g.chain(req.Model) {
		ds := g.ordered(model)
		if len(ds) == 0 {
			continue
		}
		for i := 0; i < g.attempts(len(ds)); i++ {
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
// attempted while no output has been emitted; once real tokens stream to the
// client, an upstream failure is surfaced rather than retried.
func (g *Gateway) ChatCompletionStream(ctx context.Context, req *types.ChatRequest, emit provider.StreamFunc) error {
	var lastErr error
	for _, model := range g.chain(req.Model) {
		ds := g.ordered(model)
		if len(ds) == 0 {
			continue
		}
		for i := 0; i < g.attempts(len(ds)); i++ {
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
