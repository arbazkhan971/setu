package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/types"
)

func f64(v float64) *float64 { return &v }
func iptr(v int) *int        { return &v }

func TestTranslateExtractsSystemInstruction(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model: "m",
		Messages: []types.Message{
			{Role: "system", Content: "you are helpful"},
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "hi"},
		},
	})
	if out.SystemInstruction == nil {
		t.Fatalf("systemInstruction not set")
	}
	if len(out.SystemInstruction.Parts) != 1 || out.SystemInstruction.Parts[0].Text != "you are helpful\n\nbe concise" {
		t.Fatalf("joined system text = %+v", out.SystemInstruction.Parts)
	}
	// System messages must not leak into contents.
	if len(out.Contents) != 1 || out.Contents[0].Role != "user" {
		t.Fatalf("contents = %+v", out.Contents)
	}
}

func TestTranslateOmitsSystemInstructionWhenAbsent(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:    "m",
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	})
	if out.SystemInstruction != nil {
		t.Fatalf("systemInstruction should be nil, got %+v", out.SystemInstruction)
	}
}

func TestTranslateRoleMapping(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model: "m",
		Messages: []types.Message{
			{Role: "user", Content: "u1"},
			{Role: "assistant", Content: "a1"},
			{Role: "tool", Content: "t1"},
		},
	})
	if len(out.Contents) != 3 {
		t.Fatalf("want 3 contents, got %d: %+v", len(out.Contents), out.Contents)
	}
	wantRole := []string{"user", "model", "user"} // assistant->model, tool->user
	wantText := []string{"u1", "a1", "t1"}
	for i, ct := range out.Contents {
		if ct.Role != wantRole[i] {
			t.Errorf("contents[%d].Role = %q, want %q", i, ct.Role, wantRole[i])
		}
		if len(ct.Parts) != 1 || ct.Parts[0].Text != wantText[i] {
			t.Errorf("contents[%d].Parts = %+v, want text %q", i, ct.Parts, wantText[i])
		}
	}
}

func TestTranslateSkipsEmptyTurns(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model: "m",
		Messages: []types.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: ""}, // e.g. tool-call-only assistant turn
			{Role: "user", Content: "again"},
		},
	})
	if len(out.Contents) != 2 {
		t.Fatalf("empty turn not skipped: %+v", out.Contents)
	}
	if out.Contents[0].Parts[0].Text != "hi" || out.Contents[1].Parts[0].Text != "again" {
		t.Fatalf("unexpected contents: %+v", out.Contents)
	}
}

func TestTranslateGenerationConfig(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:       "m",
		Temperature: f64(0.7),
		TopP:        f64(0.9),
		MaxTokens:   iptr(256),
		Messages:    []types.Message{{Role: "user", Content: "hi"}},
	})
	cfg := out.GenerationConfig
	if cfg == nil {
		t.Fatalf("generationConfig not set")
	}
	if cfg.Temperature == nil || *cfg.Temperature != 0.7 {
		t.Errorf("temperature = %v", cfg.Temperature)
	}
	if cfg.TopP == nil || *cfg.TopP != 0.9 {
		t.Errorf("topP = %v", cfg.TopP)
	}
	if cfg.MaxOutputTokens == nil || *cfg.MaxOutputTokens != 256 {
		t.Errorf("maxOutputTokens = %v", cfg.MaxOutputTokens)
	}
}

func TestTranslateOmitsGenerationConfigWhenEmpty(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:    "m",
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	})
	if out.GenerationConfig != nil {
		t.Fatalf("generationConfig should be nil, got %+v", out.GenerationConfig)
	}
}

func TestTranslateStopString(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:    "m",
		Stop:     "STOP",
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	})
	if out.GenerationConfig == nil {
		t.Fatalf("generationConfig not set")
	}
	got := out.GenerationConfig.StopSequences
	if len(got) != 1 || got[0] != "STOP" {
		t.Fatalf("string-form stop not mapped: %v", got)
	}
}

func TestTranslateStopArray(t *testing.T) {
	c := &Client{}
	out := c.translate(&types.ChatRequest{
		Model:    "m",
		Stop:     []any{"\n\n", "END"},
		Messages: []types.Message{{Role: "user", Content: "hi"}},
	})
	if out.GenerationConfig == nil {
		t.Fatalf("generationConfig not set")
	}
	got := out.GenerationConfig.StopSequences
	if len(got) != 2 || got[0] != "\n\n" || got[1] != "END" {
		t.Fatalf("array-form stop not mapped: %v", got)
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := map[string]string{
		"STOP":       "stop",
		"MAX_TOKENS": "length",
		"SAFETY":     "content_filter",
		"RECITATION": "content_filter",
		"":           "stop",
		"OTHER":      "stop",
	}
	for in, want := range cases {
		if got := mapFinishReason(in); got != want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelPrefersOptionOverride(t *testing.T) {
	c := &Client{opts: provider.Options{Model: "override-model"}}
	if got := c.model(&types.ChatRequest{Model: "req-model"}); got != "override-model" {
		t.Fatalf("model() = %q, want override-model", got)
	}
	c2 := &Client{}
	if got := c2.model(&types.ChatRequest{Model: "req-model"}); got != "req-model" {
		t.Fatalf("model() = %q, want req-model", got)
	}
}

func TestEndpointURL(t *testing.T) {
	c, err := New(provider.Options{APIKey: "SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	cl := c.(*Client)
	req := &types.ChatRequest{Model: "gemini-1.5-pro"}

	nonStream := cl.endpoint(req, "generateContent", false)
	if !strings.HasPrefix(nonStream, defaultBaseURL+"/v1beta/models/gemini-1.5-pro:generateContent?") {
		t.Fatalf("non-stream endpoint = %q", nonStream)
	}
	if !strings.Contains(nonStream, "key=SECRET") {
		t.Fatalf("api key not in query: %q", nonStream)
	}
	if strings.Contains(nonStream, "alt=sse") {
		t.Fatalf("non-stream should not set alt=sse: %q", nonStream)
	}

	stream := cl.endpoint(req, "streamGenerateContent", true)
	if !strings.Contains(stream, ":streamGenerateContent?") {
		t.Fatalf("stream endpoint = %q", stream)
	}
	if !strings.Contains(stream, "alt=sse") || !strings.Contains(stream, "key=SECRET") {
		t.Fatalf("stream endpoint missing params: %q", stream)
	}
}

func TestParsesResponseText(t *testing.T) {
	var gr geminiResponse
	body := `{"candidates":[{"content":{"parts":[{"text":"Hello "},{"text":"world"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`
	if err := json.Unmarshal([]byte(body), &gr); err != nil {
		t.Fatal(err)
	}
	if got := gr.candidateText(); got != "Hello world" {
		t.Fatalf("candidateText = %q", got)
	}
	if got := mapFinishReason(gr.finishReason()); got != "stop" {
		t.Fatalf("finishReason = %q", got)
	}
	if gr.UsageMetadata.TotalTokenCount != 5 {
		t.Fatalf("totalTokenCount = %d", gr.UsageMetadata.TotalTokenCount)
	}
}
