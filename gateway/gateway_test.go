package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/types"
)

// stubProvider is a controllable provider for tests.
type stubProvider struct {
	name  string
	err   error
	calls *int
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) ChatCompletion(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
	if s.calls != nil {
		*s.calls++
	}
	if s.err != nil {
		return nil, s.err
	}
	fr := "stop"
	return &types.ChatResponse{Model: s.name, Choices: []types.Choice{{FinishReason: &fr,
		Message: &types.Message{Role: "assistant", Content: s.name}}}}, nil
}

func (s *stubProvider) ChatCompletionStream(_ context.Context, _ *types.ChatRequest, emit provider.StreamFunc) error {
	if s.calls != nil {
		*s.calls++
	}
	if s.err != nil {
		return s.err
	}
	return emit(types.ChatChunk{Model: s.name, Choices: []types.ChunkChoice{{Delta: types.Delta{Content: s.name}}}})
}

func req(model string) *types.ChatRequest {
	return &types.ChatRequest{Model: model, Messages: []types.Message{{Role: "user", Content: "hi"}}}
}

func TestChatCompletionSuccess(t *testing.T) {
	g := New([]*Deployment{{ModelName: "m", Provider: &stubProvider{name: "ok"}}})
	resp, err := g.ChatCompletion(context.Background(), req("m"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("unexpected content %v", resp.Choices[0].Message.Content)
	}
}

func TestUnknownModel(t *testing.T) {
	g := New([]*Deployment{{ModelName: "m", Provider: &stubProvider{name: "ok"}}})
	if _, err := g.ChatCompletion(context.Background(), req("nope")); err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestFallback(t *testing.T) {
	primaryCalls, backupCalls := 0, 0
	g := New(
		[]*Deployment{
			{ModelName: "primary", Provider: &stubProvider{name: "primary", err: errors.New("boom"), calls: &primaryCalls}},
			{ModelName: "backup", Provider: &stubProvider{name: "backup", calls: &backupCalls}},
		},
		WithFallbacks(map[string][]string{"primary": {"backup"}}),
		WithMaxRetries(1),
	)
	resp, err := g.ChatCompletion(context.Background(), req("primary"))
	if err != nil {
		t.Fatalf("expected fallback success, got %v", err)
	}
	if resp.Choices[0].Message.Content != "backup" {
		t.Fatalf("expected backup, got %v", resp.Choices[0].Message.Content)
	}
	if primaryCalls != 2 { // maxRetries+1 attempts on primary
		t.Errorf("primary attempts = %d, want 2", primaryCalls)
	}
	if backupCalls != 1 {
		t.Errorf("backup calls = %d, want 1", backupCalls)
	}
}

func TestLoadBalanceRoundRobin(t *testing.T) {
	a, b := 0, 0
	g := New([]*Deployment{
		{ModelName: "m", Provider: &stubProvider{name: "a", calls: &a}},
		{ModelName: "m", Provider: &stubProvider{name: "b", calls: &b}},
	})
	for i := 0; i < 4; i++ {
		if _, err := g.ChatCompletion(context.Background(), req("m")); err != nil {
			t.Fatalf("err: %v", err)
		}
	}
	if a != 2 || b != 2 {
		t.Fatalf("round-robin imbalance: a=%d b=%d", a, b)
	}
}

func TestNonRetryableErrorSkipsRetryAndFallback(t *testing.T) {
	pc, bc := 0, 0
	g := New(
		[]*Deployment{
			{ModelName: "primary", Provider: &stubProvider{name: "p", err: &provider.APIError{StatusCode: 400, Body: "bad request"}, calls: &pc}},
			{ModelName: "backup", Provider: &stubProvider{name: "b", calls: &bc}},
		},
		WithFallbacks(map[string][]string{"primary": {"backup"}}),
		WithMaxRetries(3),
	)
	if _, err := g.ChatCompletion(context.Background(), req("primary")); err == nil {
		t.Fatal("expected the 400 to be surfaced")
	}
	if pc != 1 {
		t.Errorf("primary calls = %d, want 1 (a 400 must not be retried)", pc)
	}
	if bc != 0 {
		t.Errorf("backup calls = %d, want 0 (a client error must not fan out to fallbacks)", bc)
	}
}

func TestRetryableErrorRetriesAndFallsBack(t *testing.T) {
	pc, bc := 0, 0
	g := New(
		[]*Deployment{
			{ModelName: "primary", Provider: &stubProvider{name: "p", err: &provider.APIError{StatusCode: 503}, calls: &pc}},
			{ModelName: "backup", Provider: &stubProvider{name: "b", calls: &bc}},
		},
		WithFallbacks(map[string][]string{"primary": {"backup"}}),
		WithMaxRetries(1),
	)
	resp, err := g.ChatCompletion(context.Background(), req("primary"))
	if err != nil {
		t.Fatalf("expected fallback success on 503, got %v", err)
	}
	if resp.Choices[0].Message.Content != "b" {
		t.Fatalf("want backup response, got %v", resp.Choices[0].Message.Content)
	}
	if pc != 2 || bc != 1 {
		t.Errorf("attempts primary=%d backup=%d, want 2/1", pc, bc)
	}
}

// TestConcurrentCoverageFindsHealthyDeployment locks in the fix for the
// shared-round-robin-counter bug: with attempts == number of deployments, a
// single request must probe every deployment and therefore always reach the
// one healthy backend, regardless of how many other requests advance the
// shared counter concurrently.
func TestConcurrentCoverageFindsHealthyDeployment(t *testing.T) {
	mk := func(fail bool) *Deployment {
		var e error
		if fail {
			e = &provider.APIError{StatusCode: 503}
		}
		return &Deployment{ModelName: "m", Provider: &stubProvider{name: "d", err: e}}
	}
	// 3 unhealthy + 1 healthy, no fallback, maxRetries=0 => attempts=4=len.
	g := New([]*Deployment{mk(true), mk(true), mk(false), mk(true)}, WithMaxRetries(0))

	var wg sync.WaitGroup
	var fails int64
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.ChatCompletion(context.Background(), req("m")); err != nil {
				atomic.AddInt64(&fails, 1)
			}
		}()
	}
	wg.Wait()
	if fails != 0 {
		t.Fatalf("%d/500 requests failed despite a permanently-healthy deployment", fails)
	}
}

func TestWeightedLoadBalancing(t *testing.T) {
	a, b := 0, 0
	// Deployment "a" has weight 3, "b" has weight 1 -> ~3:1 primary split.
	g := New([]*Deployment{
		{ModelName: "m", Weight: 3, Provider: &stubProvider{name: "a", calls: &a}},
		{ModelName: "m", Weight: 1, Provider: &stubProvider{name: "b", calls: &b}},
	})
	for i := 0; i < 40; i++ {
		if _, err := g.ChatCompletion(context.Background(), req("m")); err != nil {
			t.Fatalf("err: %v", err)
		}
	}
	// Each request succeeds on its primary (both healthy), so calls reflect
	// the weighted primary distribution: a ~= 30, b ~= 10.
	if a != 30 || b != 10 {
		t.Fatalf("weighted split off: a=%d b=%d (want 30/10)", a, b)
	}
}

func TestModelsSorted(t *testing.T) {
	g := New([]*Deployment{
		{ModelName: "zeta", Provider: &stubProvider{name: "z"}},
		{ModelName: "alpha", Provider: &stubProvider{name: "a"}},
	})
	got := g.Models()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("Models() = %v", got)
	}
}
