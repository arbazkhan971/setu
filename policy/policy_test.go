package policy

import (
	"errors"
	"testing"

	"github.com/arbazkhan971/setu/pricing"
	"github.com/arbazkhan971/setu/types"
)

func newEnforcer() *Enforcer {
	return New([]*Key{
		{Secret: "sk-a", Name: "team-a", Models: []string{"gpt-4o"}, MaxBudget: 0.01},
		{Secret: "sk-b", Name: "team-b"}, // unrestricted
	}, pricing.Default())
}

func TestResolve(t *testing.T) {
	e := newEnforcer()
	if k := e.Resolve("sk-a"); k == nil || k.Name != "team-a" {
		t.Fatalf("resolve sk-a failed: %v", k)
	}
	if k := e.Resolve("nope"); k != nil {
		t.Fatalf("resolve of bad secret returned %v", k)
	}
}

func TestAuthorizeModelAllowlist(t *testing.T) {
	e := newEnforcer()
	a := e.Resolve("sk-a")
	if err := e.Authorize(a, "gpt-4o"); err != nil {
		t.Fatalf("allowed model rejected: %v", err)
	}
	if err := e.Authorize(a, "claude"); !errors.Is(err, ErrModelNotAllowed) {
		t.Fatalf("expected ErrModelNotAllowed, got %v", err)
	}
	// team-b is unrestricted.
	if err := e.Authorize(e.Resolve("sk-b"), "anything"); err != nil {
		t.Fatalf("unrestricted key rejected: %v", err)
	}
}

func TestBudgetEnforcement(t *testing.T) {
	e := newEnforcer()
	a := e.Resolve("sk-a") // MaxBudget 0.01 USD
	if err := e.Authorize(a, "gpt-4o"); err != nil {
		t.Fatalf("fresh key over budget: %v", err)
	}
	// Record enough spend to exceed 0.01: gpt-4o out is $10/1M -> 2000 out = $0.02.
	e.Record(a, "gpt-4o", &types.Usage{PromptTokens: 0, CompletionTokens: 2000})
	if err := e.Authorize(a, "gpt-4o"); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded after spend, got %v", err)
	}
	info := e.Info(a)
	if info.Spend <= 0.01 {
		t.Fatalf("spend not tracked: %v", info.Spend)
	}
	if info.CompletionTokens != 2000 || info.Requests != 1 {
		t.Fatalf("usage counters wrong: %+v", info)
	}
}

func TestDisabledEnforcerHasNoKeys(t *testing.T) {
	var e *Enforcer
	if e.Enabled() {
		t.Fatal("nil enforcer should not be enabled")
	}
	if New(nil, pricing.Default()).Enabled() {
		t.Fatal("empty enforcer should not be enabled")
	}
}
