package cohere

import (
	"testing"

	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/types"
)

func f64(v float64) *float64 { return &v }
func iptr(v int) *int        { return &v }

func TestTranslateMapsRolesAndKeepsSystemInline(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model: "command-r",
		Messages: []types.Message{
			{Role: "system", Content: "you are helpful"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "tool", Content: "result"}, // non-standard role -> user
		},
	}, false)

	if len(out.Messages) != 4 {
		t.Fatalf("want 4 messages, got %d: %+v", len(out.Messages), out.Messages)
	}
	want := []cohereMessage{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "user", Content: "result"},
	}
	for i, w := range want {
		if out.Messages[i] != w {
			t.Errorf("message[%d] = %+v, want %+v", i, out.Messages[i], w)
		}
	}
}

func TestTranslateSkipsEmptyMessages(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model: "command-r",
		Messages: []types.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: ""}, // tool-call-only assistant turn
			{Role: "user", Content: "again"},
		},
	}, false)
	if len(out.Messages) != 2 {
		t.Fatalf("empty message not skipped: %+v", out.Messages)
	}
	if out.Messages[0].Content != "hi" || out.Messages[1].Content != "again" {
		t.Fatalf("unexpected messages: %+v", out.Messages)
	}
}

func TestTranslateModelOverride(t *testing.T) {
	c := &Client{opts: provider.Options{Model: "command-r-plus"}}
	out := c.translate(&types.ChatRequest{Model: "ignored", Messages: []types.Message{{Role: "user", Content: "hi"}}}, false)
	if out.Model != "command-r-plus" {
		t.Fatalf("opts.Model should override req.Model, got %q", out.Model)
	}

	c2 := &Client{}
	out2 := c2.translate(&types.ChatRequest{Model: "command-r", Messages: []types.Message{{Role: "user", Content: "hi"}}}, false)
	if out2.Model != "command-r" {
		t.Fatalf("req.Model should be used when opts.Model empty, got %q", out2.Model)
	}
}

func TestTranslateParamsAndStream(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:       "command-r",
		Temperature: f64(0.7),
		TopP:        f64(0.9),
		MaxTokens:   iptr(256),
		Messages:    []types.Message{{Role: "user", Content: "hi"}},
	}, true)

	if out.Temperature == nil || *out.Temperature != 0.7 {
		t.Errorf("temperature not passed through: %v", out.Temperature)
	}
	if out.P == nil || *out.P != 0.9 {
		t.Errorf("top_p should map to p: %v", out.P)
	}
	if out.MaxTokens == nil || *out.MaxTokens != 256 {
		t.Errorf("max_tokens not passed through: %v", out.MaxTokens)
	}
	if !out.Stream {
		t.Errorf("stream flag not set")
	}
}

func TestTranslateStopString(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:    "command-r",
		Stop:     "STOP",
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	}, false)
	if len(out.StopSequences) != 1 || out.StopSequences[0] != "STOP" {
		t.Fatalf("string-form stop not mapped: %v", out.StopSequences)
	}
}

func TestTranslateStopArray(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:    "command-r",
		Stop:     []any{"\n\n", "END", ""},
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	}, false)
	if len(out.StopSequences) != 2 || out.StopSequences[0] != "\n\n" || out.StopSequences[1] != "END" {
		t.Fatalf("array-form stop not mapped (empties dropped): %v", out.StopSequences)
	}
}

func TestTranslateStopEmpty(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:    "command-r",
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	}, false)
	if out.StopSequences != nil {
		t.Fatalf("expected nil stop sequences, got %v", out.StopSequences)
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := map[string]string{
		"COMPLETE":      "stop",
		"STOP_SEQUENCE": "stop",
		"MAX_TOKENS":    "length",
		"ERROR":         "stop",
		"":              "stop",
	}
	for in, want := range cases {
		if got := mapFinishReason(in); got != want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapRole(t *testing.T) {
	cases := map[string]string{
		"system":    "system",
		"assistant": "assistant",
		"user":      "user",
		"tool":      "user",
		"function":  "user",
	}
	for in, want := range cases {
		if got := mapRole(in); got != want {
			t.Errorf("mapRole(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNameAndBaseURLDefault(t *testing.T) {
	p, err := New(provider.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != "cohere" {
		t.Errorf("Name() = %q, want cohere", p.Name())
	}
	if c, ok := p.(*Client); !ok || c.baseURL != defaultBaseURL {
		t.Errorf("default baseURL = %q, want %q", c.baseURL, defaultBaseURL)
	}
}
