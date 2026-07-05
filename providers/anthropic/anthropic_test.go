package anthropic

import (
	"testing"

	"github.com/arbazkhan971/setu/types"
)

func f64(v float64) *float64 { return &v }

func TestTranslateClampsTemperature(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:       "m",
		Temperature: f64(1.7), // valid for OpenAI, out of range for Anthropic
		Messages:    []types.Message{{Role: "user", Content: "hi"}},
	}, false)
	if out.Temperature == nil || *out.Temperature != 1 {
		t.Fatalf("temperature not clamped to 1: %v", out.Temperature)
	}
}

func TestTranslateStopArray(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:    "m",
		Stop:     []any{"\n\n", "END"},
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	}, false)
	if len(out.StopSequences) != 2 || out.StopSequences[0] != "\n\n" || out.StopSequences[1] != "END" {
		t.Fatalf("array-form stop not mapped: %v", out.StopSequences)
	}
}

func TestTranslateStopString(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:    "m",
		Stop:     "STOP",
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	}, false)
	if len(out.StopSequences) != 1 || out.StopSequences[0] != "STOP" {
		t.Fatalf("string-form stop not mapped: %v", out.StopSequences)
	}
}

func TestTranslateExtractsSystemAndMergesConsecutiveRoles(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model: "m",
		Messages: []types.Message{
			{Role: "system", Content: "you are helpful"},
			{Role: "user", Content: "context"},
			{Role: "user", Content: "question"}, // consecutive user turns
		},
	}, false)
	if out.System != "you are helpful" {
		t.Fatalf("system = %q", out.System)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("consecutive user messages not merged: %+v", out.Messages)
	}
	if out.Messages[0].Role != "user" || out.Messages[0].Content != "context\n\nquestion" {
		t.Fatalf("merged message = %+v", out.Messages[0])
	}
}

func TestTranslateSkipsEmptyMessages(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model: "m",
		Messages: []types.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: ""}, // e.g. tool-call-only assistant turn
			{Role: "user", Content: "again"},
		},
	}, false)
	// The empty assistant turn is dropped; the surrounding user turns merge.
	if len(out.Messages) != 1 || out.Messages[0].Content != "hi\n\nagain" {
		t.Fatalf("empty message not skipped/merged: %+v", out.Messages)
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"end_turn":      "stop",
		"stop_sequence": "stop",
		"max_tokens":    "length",
		"tool_use":      "tool_calls",
		"":              "stop",
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Errorf("mapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}
