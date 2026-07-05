// Package metrics implements a minimal, dependency-free Prometheus
// text-format registry for Setu. It tracks request counts, token counts,
// and request-duration histograms, and exposes them over HTTP in the
// Prometheus text exposition format (version 0.0.4).
//
// A Registry is safe for concurrent use by multiple goroutines: every
// mutating method and the scrape handler take an internal mutex.
package metrics

import (
	"bytes"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// contentType is the Prometheus text exposition format media type.
const contentType = "text/plain; version=0.0.4; charset=utf-8"

// defaultBuckets are the upper bounds (in seconds) for the
// setu_request_duration_seconds histogram. They are sorted ascending and
// must not be mutated. The implicit +Inf bucket is emitted separately and
// always equals the observation count.
var defaultBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// requestKey identifies a single setu_requests_total series.
type requestKey struct {
	model    string
	provider string
	status   string
}

// histogram holds cumulative bucket counts, the running sum of observed
// values, and the total observation count for one label set. Each entry of
// buckets[i] is already cumulative: it counts every observation whose value
// is <= defaultBuckets[i], which guarantees the exposed series is
// monotonically non-decreasing by construction.
type histogram struct {
	buckets []uint64
	sum     float64
	count   uint64
}

// observe records a single duration (in seconds) into the histogram.
func (h *histogram) observe(v float64) {
	// SearchFloat64s returns the smallest index i with defaultBuckets[i] >= v,
	// i.e. the first bucket the value falls into. Every bucket from there to
	// the end is cumulative, so increment the whole suffix.
	for i := sort.SearchFloat64s(defaultBuckets, v); i < len(h.buckets); i++ {
		h.buckets[i]++
	}
	h.sum += v
	h.count++
}

// Registry is a concurrent-safe collection of Setu metrics.
type Registry struct {
	mu sync.Mutex

	requests         map[requestKey]uint64
	promptTokens     map[string]uint64
	completionTokens map[string]uint64
	latency          map[string]*histogram
}

// New returns a ready-to-use, empty Registry.
func New() *Registry {
	return &Registry{
		requests:         make(map[requestKey]uint64),
		promptTokens:     make(map[string]uint64),
		completionTokens: make(map[string]uint64),
		latency:          make(map[string]*histogram),
	}
}

// IncRequest increments the setu_requests_total counter for the given
// model, provider, and status labels.
func (r *Registry) IncRequest(model, provider, status string) {
	r.mu.Lock()
	r.requests[requestKey{model: model, provider: provider, status: status}]++
	r.mu.Unlock()
}

// AddTokens adds prompt and completion token counts to the
// setu_prompt_tokens_total and setu_completion_tokens_total counters for
// the given model. Negative arguments are ignored (counters never decrease).
func (r *Registry) AddTokens(model string, prompt, completion int) {
	r.mu.Lock()
	if prompt > 0 {
		r.promptTokens[model] += uint64(prompt)
	} else if _, ok := r.promptTokens[model]; !ok {
		r.promptTokens[model] = 0
	}
	if completion > 0 {
		r.completionTokens[model] += uint64(completion)
	} else if _, ok := r.completionTokens[model]; !ok {
		r.completionTokens[model] = 0
	}
	r.mu.Unlock()
}

// ObserveLatency records a request duration (in seconds) into the
// setu_request_duration_seconds histogram for the given model.
func (r *Registry) ObserveLatency(model string, seconds float64) {
	r.mu.Lock()
	h := r.latency[model]
	if h == nil {
		h = &histogram{buckets: make([]uint64, len(defaultBuckets))}
		r.latency[model] = h
	}
	h.observe(seconds)
	r.mu.Unlock()
}

// Handler returns an http.Handler that serves the current metrics in
// Prometheus text exposition format. It responds to GET (and HEAD)
// requests and returns 405 Method Not Allowed for any other method.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body := r.gather()
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		if req.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}

// gather renders the full exposition payload. It snapshots and formats
// everything under the lock so the returned bytes are a consistent view.
func (r *Registry) gather() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	var b bytes.Buffer

	// setu_requests_total
	writeHelp(&b, "setu_requests_total", "Total number of requests processed by Setu, labeled by model, provider, and status.")
	writeType(&b, "setu_requests_total", "counter")
	reqKeys := make([]requestKey, 0, len(r.requests))
	for k := range r.requests {
		reqKeys = append(reqKeys, k)
	}
	sort.Slice(reqKeys, func(i, j int) bool {
		a, c := reqKeys[i], reqKeys[j]
		if a.model != c.model {
			return a.model < c.model
		}
		if a.provider != c.provider {
			return a.provider < c.provider
		}
		return a.status < c.status
	})
	for _, k := range reqKeys {
		b.WriteString("setu_requests_total")
		writeLabels(&b, "model", k.model, "provider", k.provider, "status", k.status)
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(r.requests[k], 10))
		b.WriteByte('\n')
	}

	// setu_prompt_tokens_total
	writeHelp(&b, "setu_prompt_tokens_total", "Total number of prompt tokens processed, labeled by model.")
	writeType(&b, "setu_prompt_tokens_total", "counter")
	writeTokenCounter(&b, "setu_prompt_tokens_total", r.promptTokens)

	// setu_completion_tokens_total
	writeHelp(&b, "setu_completion_tokens_total", "Total number of completion tokens generated, labeled by model.")
	writeType(&b, "setu_completion_tokens_total", "counter")
	writeTokenCounter(&b, "setu_completion_tokens_total", r.completionTokens)

	// setu_request_duration_seconds
	writeHelp(&b, "setu_request_duration_seconds", "Request duration in seconds, labeled by model.")
	writeType(&b, "setu_request_duration_seconds", "histogram")
	models := make([]string, 0, len(r.latency))
	for m := range r.latency {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		h := r.latency[m]
		for i, bound := range defaultBuckets {
			b.WriteString("setu_request_duration_seconds_bucket")
			writeLabels(&b, "model", m, "le", formatFloat(bound))
			b.WriteByte(' ')
			b.WriteString(strconv.FormatUint(h.buckets[i], 10))
			b.WriteByte('\n')
		}
		// +Inf bucket always equals the total count.
		b.WriteString("setu_request_duration_seconds_bucket")
		writeLabels(&b, "model", m, "le", "+Inf")
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(h.count, 10))
		b.WriteByte('\n')

		b.WriteString("setu_request_duration_seconds_sum")
		writeLabels(&b, "model", m)
		b.WriteByte(' ')
		b.WriteString(formatFloat(h.sum))
		b.WriteByte('\n')

		b.WriteString("setu_request_duration_seconds_count")
		writeLabels(&b, "model", m)
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(h.count, 10))
		b.WriteByte('\n')
	}

	return b.Bytes()
}

// writeTokenCounter emits one series per model for a single-label counter.
func writeTokenCounter(b *bytes.Buffer, name string, m map[string]uint64) {
	models := make([]string, 0, len(m))
	for k := range m {
		models = append(models, k)
	}
	sort.Strings(models)
	for _, model := range models {
		b.WriteString(name)
		writeLabels(b, "model", model)
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(m[model], 10))
		b.WriteByte('\n')
	}
}

// writeHelp writes a "# HELP" line. The help text is escaped per the
// exposition format (backslash and newline).
func writeHelp(b *bytes.Buffer, name, help string) {
	b.WriteString("# HELP ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(escapeHelp(help))
	b.WriteByte('\n')
}

// writeType writes a "# TYPE" line.
func writeType(b *bytes.Buffer, name, typ string) {
	b.WriteString("# TYPE ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(typ)
	b.WriteByte('\n')
}

// writeLabels writes a label set as {k="v",...}. Pairs are supplied as
// consecutive name,value arguments and are emitted in the given order.
// Nothing is written when there are no pairs.
func writeLabels(b *bytes.Buffer, pairs ...string) {
	if len(pairs) == 0 {
		return
	}
	b.WriteByte('{')
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(pairs[i])
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(pairs[i+1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
}

// escapeLabelValue escapes a label value per the Prometheus exposition
// format: backslash, double-quote, and newline.
func escapeLabelValue(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			sb.WriteString(`\\`)
		case '"':
			sb.WriteString(`\"`)
		case '\n':
			sb.WriteString(`\n`)
		default:
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}

// escapeHelp escapes help text per the exposition format: backslash and
// newline (double-quotes need not be escaped in HELP lines).
func escapeHelp(s string) string {
	if !strings.ContainsAny(s, "\\\n") {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		default:
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}

// formatFloat renders a float in the minimal round-trippable form used by
// Prometheus (e.g. 0.005, 1, 2.5, 10).
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
