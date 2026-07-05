package metrics

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// scrape drives the Registry's Handler with a GET request and returns the
// body and the Content-Type header.
func scrape(t *testing.T, r *Registry) (string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Handler().ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body), res.Header.Get("Content-Type")
}

// parseSeries parses the exposition body into a map keyed by the full
// series identifier ("name" or "name{labels}") to its numeric value. Lines
// beginning with '#' (HELP/TYPE) are skipped.
func parseSeries(t *testing.T, body string) map[string]float64 {
	t.Helper()
	out := make(map[string]float64)
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// The value is everything after the last space.
		i := strings.LastIndexByte(line, ' ')
		if i < 0 {
			t.Fatalf("malformed series line: %q", line)
		}
		key := line[:i]
		val, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("bad value in line %q: %v", line, err)
		}
		if _, dup := out[key]; dup {
			t.Fatalf("duplicate series %q", key)
		}
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestEscapeLabelValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "gpt-4o", "gpt-4o"},
		{"empty", "", ""},
		{"quote", `a"b`, `a\"b`},
		{"backslash", `a\b`, `a\\b`},
		{"newline", "a\nb", `a\nb`},
		{"all", "a\"\\\nz", `a\"\\\nz`},
		{"unicode", "модель", "модель"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeLabelValue(tc.in); got != tc.want {
				t.Errorf("escapeLabelValue(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatFloat(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{.005, "0.005"},
		{.01, "0.01"},
		{.025, "0.025"},
		{.05, "0.05"},
		{.1, "0.1"},
		{.25, "0.25"},
		{.5, "0.5"},
		{1, "1"},
		{2.5, "2.5"},
		{5, "5"},
		{10, "10"},
		{0, "0"},
	}
	for _, tc := range tests {
		if got := formatFloat(tc.in); got != tc.want {
			t.Errorf("formatFloat(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHelpAndTypeLines(t *testing.T) {
	r := New()
	r.IncRequest("gpt-4o", "openai", "200")
	body, ct := scrape(t, r)

	if want := contentType; ct != want {
		t.Errorf("Content-Type = %q, want %q", ct, want)
	}

	wantLines := []string{
		"# HELP setu_requests_total ",
		"# TYPE setu_requests_total counter",
		"# HELP setu_prompt_tokens_total ",
		"# TYPE setu_prompt_tokens_total counter",
		"# HELP setu_completion_tokens_total ",
		"# TYPE setu_completion_tokens_total counter",
		"# HELP setu_request_duration_seconds ",
		"# TYPE setu_request_duration_seconds histogram",
	}
	for _, w := range wantLines {
		if !strings.Contains(body, w) {
			t.Errorf("body missing line prefix %q\n---\n%s", w, body)
		}
	}
}

func TestIncRequestSeries(t *testing.T) {
	r := New()
	r.IncRequest("gpt-4o", "openai", "200")
	r.IncRequest("gpt-4o", "openai", "200")
	r.IncRequest("gpt-4o", "openai", "500")
	r.IncRequest("claude-3", "anthropic", "200")

	body, _ := scrape(t, r)
	series := parseSeries(t, body)

	tests := []struct {
		key  string
		want float64
	}{
		{`setu_requests_total{model="gpt-4o",provider="openai",status="200"}`, 2},
		{`setu_requests_total{model="gpt-4o",provider="openai",status="500"}`, 1},
		{`setu_requests_total{model="claude-3",provider="anthropic",status="200"}`, 1},
	}
	for _, tc := range tests {
		if got, ok := series[tc.key]; !ok {
			t.Errorf("missing series %s\n---\n%s", tc.key, body)
		} else if got != tc.want {
			t.Errorf("%s = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestAddTokens(t *testing.T) {
	r := New()
	r.AddTokens("gpt-4o", 100, 40)
	r.AddTokens("gpt-4o", 50, 10)
	r.AddTokens("gpt-4o", -5, -5) // negatives ignored, no decrease
	r.AddTokens("claude-3", 7, 0)

	body, _ := scrape(t, r)
	series := parseSeries(t, body)

	tests := []struct {
		key  string
		want float64
	}{
		{`setu_prompt_tokens_total{model="gpt-4o"}`, 150},
		{`setu_completion_tokens_total{model="gpt-4o"}`, 50},
		{`setu_prompt_tokens_total{model="claude-3"}`, 7},
		{`setu_completion_tokens_total{model="claude-3"}`, 0},
	}
	for _, tc := range tests {
		if got, ok := series[tc.key]; !ok {
			t.Errorf("missing series %s\n---\n%s", tc.key, body)
		} else if got != tc.want {
			t.Errorf("%s = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestHistogramExposition(t *testing.T) {
	r := New()
	// Observations chosen to land in distinct buckets.
	obs := []float64{0.003, 0.007, 0.03, 0.2, 0.75, 3.0, 42.0}
	var sum float64
	for _, v := range obs {
		r.ObserveLatency("gpt-4o", v)
		sum += v
	}

	body, _ := scrape(t, r)
	series := parseSeries(t, body)

	// _count and _sum
	if got := series[`setu_request_duration_seconds_count{model="gpt-4o"}`]; got != float64(len(obs)) {
		t.Errorf("_count = %v, want %d", got, len(obs))
	}
	if got := series[`setu_request_duration_seconds_sum{model="gpt-4o"}`]; got != sum {
		t.Errorf("_sum = %v, want %v", got, sum)
	}

	// Cumulative bucket monotonicity across the bucket boundaries plus +Inf.
	les := make([]string, 0, len(defaultBuckets)+1)
	for _, b := range defaultBuckets {
		les = append(les, formatFloat(b))
	}
	les = append(les, "+Inf")

	var prev float64 = -1
	for _, le := range les {
		key := `setu_request_duration_seconds_bucket{model="gpt-4o",le="` + le + `"}`
		got, ok := series[key]
		if !ok {
			t.Fatalf("missing bucket %s\n---\n%s", key, body)
		}
		if got < prev {
			t.Errorf("bucket le=%s value %v < previous %v (not cumulative)", le, got, prev)
		}
		prev = got
	}

	// +Inf must equal the total count.
	if got := series[`setu_request_duration_seconds_bucket{model="gpt-4o",le="+Inf"}`]; got != float64(len(obs)) {
		t.Errorf("+Inf bucket = %v, want %d", got, len(obs))
	}

	// Spot-check specific cumulative counts.
	// <= 0.005: {0.003}                          => 1
	// <= 0.01:  {0.003,0.007}                     => 2
	// <= 0.05:  {0.003,0.007,0.03}                => 3
	// <= 0.25:  + {0.2}                            => 4
	// <= 1:     + {0.75}                           => 5
	// <= 5:     + {3.0}                            => 6
	spot := map[string]float64{
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="0.005"}`: 1,
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="0.01"}`:  2,
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="0.025"}`: 2,
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="0.05"}`:  3,
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="0.25"}`:  4,
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="1"}`:     5,
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="5"}`:     6,
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="10"}`:    6,
	}
	for k, want := range spot {
		if got := series[k]; got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}
}

func TestObserveBucketBoundaryInclusive(t *testing.T) {
	// A value exactly on a boundary must be counted in that bucket (le is
	// "less than or equal").
	r := New()
	r.ObserveLatency("m", 0.05)
	body, _ := scrape(t, r)
	series := parseSeries(t, body)
	if got := series[`setu_request_duration_seconds_bucket{model="m",le="0.05"}`]; got != 1 {
		t.Errorf("le=0.05 bucket = %v, want 1", got)
	}
	if got := series[`setu_request_duration_seconds_bucket{model="m",le="0.025"}`]; got != 0 {
		t.Errorf("le=0.025 bucket = %v, want 0", got)
	}
}

func TestLabelEscapingInOutput(t *testing.T) {
	r := New()
	r.IncRequest(`weird"\model`, "openai", "200")
	body, _ := scrape(t, r)
	want := `setu_requests_total{model="weird\"\\model",provider="openai",status="200"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("body missing escaped series %q\n---\n%s", want, body)
	}
}

func TestHandlerMethodNotAllowed(t *testing.T) {
	r := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	r.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("Allow header = %q, want to contain GET", allow)
	}
}

func TestHandlerHead(t *testing.T) {
	r := New()
	r.IncRequest("m", "p", "200")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/metrics", nil)
	r.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body len = %d, want 0", rec.Body.Len())
	}
	if ct := rec.Header().Get("Content-Type"); ct != contentType {
		t.Errorf("HEAD Content-Type = %q, want %q", ct, contentType)
	}
}

func TestEmptyRegistryStillEmitsMeta(t *testing.T) {
	r := New()
	body, _ := scrape(t, r)
	// Even with no observations, HELP/TYPE metadata should be present.
	for _, name := range []string{
		"setu_requests_total",
		"setu_prompt_tokens_total",
		"setu_completion_tokens_total",
		"setu_request_duration_seconds",
	} {
		if !strings.Contains(body, "# TYPE "+name+" ") {
			t.Errorf("empty registry missing TYPE for %s\n---\n%s", name, body)
		}
	}
}

func TestConcurrentUpdatesAndScrape(t *testing.T) {
	r := New()

	const (
		workers   = 32
		perWorker = 500
	)

	var wg sync.WaitGroup

	// Concurrent writers.
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				r.IncRequest("gpt-4o", "openai", "200")
				r.AddTokens("gpt-4o", 3, 2)
				r.ObserveLatency("gpt-4o", 0.03)
			}
		}()
	}

	// Concurrent scrapers running alongside the writers (exercises the
	// mutex under the race detector).
	wg.Add(4)
	for s := 0; s < 4; s++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
				r.Handler().ServeHTTP(rec, req)
			}
		}()
	}

	wg.Wait()

	body, _ := scrape(t, r)
	series := parseSeries(t, body)

	total := float64(workers * perWorker)
	checks := map[string]float64{
		`setu_requests_total{model="gpt-4o",provider="openai",status="200"}`: total,
		`setu_prompt_tokens_total{model="gpt-4o"}`:                           total * 3,
		`setu_completion_tokens_total{model="gpt-4o"}`:                       total * 2,
		`setu_request_duration_seconds_count{model="gpt-4o"}`:                total,
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="+Inf"}`:     total,
		`setu_request_duration_seconds_bucket{model="gpt-4o",le="0.05"}`:     total, // 0.03 <= 0.05
	}
	for k, want := range checks {
		if got := series[k]; got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}
}
