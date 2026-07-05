// Package pricing maps model ids to per-token USD prices and computes the
// cost of a request's token usage. It powers per-key budgets and spend
// tracking. Prices are approximate public list prices and can be extended.
package pricing

import "strings"

// Price is the USD cost per input and output token.
type Price struct {
	Input  float64
	Output float64
}

// Table maps model ids to prices. Lookups fall back to the longest matching
// prefix, so versioned ids (e.g. gpt-4o-2024-08-06) resolve to their family.
type Table map[string]Price

// Cost returns the USD cost of the given token counts for a model. Unknown
// models cost 0 (spend simply is not tracked for them).
func (t Table) Cost(model string, promptTokens, completionTokens int) float64 {
	p, ok := t.lookup(model)
	if !ok {
		return 0
	}
	return float64(promptTokens)*p.Input + float64(completionTokens)*p.Output
}

func (t Table) lookup(model string) (Price, bool) {
	if p, ok := t[model]; ok {
		return p, true
	}
	var best string
	for k := range t {
		if strings.HasPrefix(model, k) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return t[best], true
	}
	return Price{}, false
}

// Default returns the built-in pricing table. Helper m() takes list prices in
// USD per million tokens and converts to per-token.
func Default() Table {
	m := func(inPerM, outPerM float64) Price {
		return Price{Input: inPerM / 1e6, Output: outPerM / 1e6}
	}
	return Table{
		// OpenAI
		"gpt-4o-mini":   m(0.15, 0.60),
		"gpt-4o":        m(2.50, 10.0),
		"gpt-4-turbo":   m(10.0, 30.0),
		"gpt-4":         m(30.0, 60.0),
		"gpt-3.5-turbo": m(0.50, 1.50),
		"o1-mini":       m(1.10, 4.40),
		"o1":            m(15.0, 60.0),
		// Anthropic
		"claude-3-5-haiku":  m(0.80, 4.0),
		"claude-3-5-sonnet": m(3.0, 15.0),
		"claude-3-opus":     m(15.0, 75.0),
		"claude-3-haiku":    m(0.25, 1.25),
		"claude-3-sonnet":   m(3.0, 15.0),
		// Google
		"gemini-1.5-flash": m(0.075, 0.30),
		"gemini-1.5-pro":   m(1.25, 5.0),
		"gemini-2.0-flash": m(0.10, 0.40),
		// Cohere
		"command-r-plus": m(2.50, 10.0),
		"command-r":      m(0.15, 0.60),
		// Others
		"deepseek-chat": m(0.27, 1.10),
		"mistral-large": m(2.0, 6.0),
	}
}
