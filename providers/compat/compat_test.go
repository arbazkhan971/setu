package compat

import (
	"testing"

	"github.com/arbazkhan971/setu/provider"
)

func TestAllEndpointsRegisterAndConstruct(t *testing.T) {
	for _, e := range Endpoints {
		p, err := provider.New(e.name, provider.Options{APIKey: "test"})
		if err != nil {
			t.Fatalf("provider %q not registered/constructable: %v", e.name, err)
		}
		if p.Name() != e.name {
			t.Errorf("provider %q Name() = %q, want %q", e.name, p.Name(), e.name)
		}
	}
}

func TestExplicitBaseURLOverridesDefault(t *testing.T) {
	// A user-supplied base_url must win over the built-in default.
	p, err := provider.New("groq", provider.Options{BaseURL: "http://localhost:9999/v1", APIKey: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "groq" {
		t.Fatalf("Name() = %q", p.Name())
	}
}
