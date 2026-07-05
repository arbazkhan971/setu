package types

import (
	"encoding/json"
	"testing"
)

func TestChatRequestPreservesExtraFields(t *testing.T) {
	in := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}],
		"logprobs": true,
		"top_logprobs": 5,
		"metadata": {"trace": "abc"}
	}`)

	var req ChatRequest
	if err := json.Unmarshal(in, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Model != "gpt-4o" {
		t.Fatalf("model = %q", req.Model)
	}
	if len(req.Extra) != 3 {
		t.Fatalf("expected 3 extra fields, got %d: %v", len(req.Extra), req.Extra)
	}

	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	for _, k := range []string{"logprobs", "top_logprobs", "metadata"} {
		if _, ok := round[k]; !ok {
			t.Errorf("extra field %q lost on round-trip", k)
		}
	}
}

func TestMessageTextFlattensMultimodal(t *testing.T) {
	m := Message{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "hello "},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "x"}},
		map[string]any{"type": "text", "text": "world"},
	}}
	if got := m.Text(); got != "hello world" {
		t.Fatalf("Text() = %q, want %q", got, "hello world")
	}
	s := Message{Role: "user", Content: "plain"}
	if got := s.Text(); got != "plain" {
		t.Fatalf("Text() = %q, want plain", got)
	}
}
