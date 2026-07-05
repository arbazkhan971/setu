package pricing

import (
	"math"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCostExactMatch(t *testing.T) {
	tbl := Default()
	// gpt-4o: $2.50/1M in, $10/1M out. 1000 in + 500 out.
	got := tbl.Cost("gpt-4o", 1000, 500)
	want := 1000*2.50/1e6 + 500*10.0/1e6
	if !almost(got, want) {
		t.Fatalf("Cost = %v, want %v", got, want)
	}
}

func TestCostPrefixMatch(t *testing.T) {
	tbl := Default()
	// Versioned id must resolve to the gpt-4o family price.
	if !almost(tbl.Cost("gpt-4o-2024-08-06", 1000, 0), tbl.Cost("gpt-4o", 1000, 0)) {
		t.Fatalf("versioned gpt-4o did not resolve to gpt-4o price")
	}
	// gpt-4o-mini must win over gpt-4o and gpt-4 (longest prefix).
	if almost(tbl.Cost("gpt-4o-mini-2024", 1000, 0), tbl.Cost("gpt-4o", 1000, 0)) {
		t.Fatalf("gpt-4o-mini incorrectly matched gpt-4o price")
	}
}

func TestCostUnknownModelIsZero(t *testing.T) {
	if got := Default().Cost("some-unlisted-model", 1000, 1000); got != 0 {
		t.Fatalf("unknown model cost = %v, want 0", got)
	}
}
