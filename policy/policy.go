// Package policy implements virtual API keys with per-key model allowlists
// and budget/spend tracking. The HTTP server resolves an incoming bearer
// token to a Key, authorizes each request, and records its cost.
package policy

import (
	"crypto/subtle"
	"errors"
	"sync"

	"github.com/arbazkhan971/setu/pricing"
	"github.com/arbazkhan971/setu/types"
)

// Authorization failures.
var (
	ErrModelNotAllowed = errors.New("model not permitted for this key")
	ErrBudgetExceeded  = errors.New("budget exceeded for this key")
)

// Key is a virtual API key with optional scoping. A zero value for a limit
// (MaxBudget/RPM/TPM) means unlimited; an empty Models list means all models.
type Key struct {
	Secret    string
	Name      string
	Models    []string
	MaxBudget float64
	RPM       int
	TPM       int
}

func (k *Key) allows(model string) bool {
	if len(k.Models) == 0 {
		return true
	}
	for _, m := range k.Models {
		if m == model {
			return true
		}
	}
	return false
}

// usage accumulates per-key running totals.
type usage struct {
	spend   float64
	reqs    int
	promptT int
	compT   int
}

// Enforcer resolves virtual keys and tracks per-key spend. It is safe for
// concurrent use.
type Enforcer struct {
	keys    []*Key
	pricing pricing.Table
	mu      sync.Mutex
	usage   map[string]*usage // by key name
}

// New builds an Enforcer over the given keys and pricing table.
func New(keys []*Key, p pricing.Table) *Enforcer {
	return &Enforcer{keys: keys, pricing: p, usage: map[string]*usage{}}
}

// Enabled reports whether any virtual keys are configured.
func (e *Enforcer) Enabled() bool { return e != nil && len(e.keys) > 0 }

// Resolve returns the Key matching secret using a constant-time comparison,
// or nil if none match.
func (e *Enforcer) Resolve(secret string) *Key {
	var found *Key
	for _, k := range e.keys {
		if subtle.ConstantTimeCompare([]byte(secret), []byte(k.Secret)) == 1 {
			found = k
		}
	}
	return found
}

// Authorize checks that the key may call the model and is under budget.
func (e *Enforcer) Authorize(k *Key, model string) error {
	if k == nil {
		return nil
	}
	if !k.allows(model) {
		return ErrModelNotAllowed
	}
	if k.MaxBudget > 0 {
		e.mu.Lock()
		u := e.usage[k.Name]
		e.mu.Unlock()
		if u != nil && u.spend >= k.MaxBudget {
			return ErrBudgetExceeded
		}
	}
	return nil
}

// Record adds a request's cost and token usage to the key's totals.
func (e *Enforcer) Record(k *Key, model string, u *types.Usage) {
	if k == nil || u == nil {
		return
	}
	cost := e.pricing.Cost(model, u.PromptTokens, u.CompletionTokens)
	e.mu.Lock()
	acc := e.usage[k.Name]
	if acc == nil {
		acc = &usage{}
		e.usage[k.Name] = acc
	}
	acc.spend += cost
	acc.reqs++
	acc.promptT += u.PromptTokens
	acc.compT += u.CompletionTokens
	e.mu.Unlock()
}

// Info is a JSON snapshot of a key's usage.
type Info struct {
	Name             string   `json:"key_name"`
	Spend            float64  `json:"spend_usd"`
	MaxBudget        float64  `json:"max_budget_usd,omitempty"`
	Requests         int      `json:"requests"`
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	AllowedModels    []string `json:"allowed_models,omitempty"`
}

// Info returns a snapshot of the key's usage.
func (e *Enforcer) Info(k *Key) Info {
	e.mu.Lock()
	defer e.mu.Unlock()
	info := Info{Name: k.Name, MaxBudget: k.MaxBudget, AllowedModels: k.Models}
	if u := e.usage[k.Name]; u != nil {
		info.Spend = u.spend
		info.Requests = u.reqs
		info.PromptTokens = u.promptT
		info.CompletionTokens = u.compT
	}
	return info
}
