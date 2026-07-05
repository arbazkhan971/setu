// Package anthropic implements the Provider interface for Anthropic's
// Messages API, translating to and from Setu's unified OpenAI-style
// schema so clients need not know the backend is Claude.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/types"
)

const (
	defaultBaseURL = "https://api.anthropic.com"
	apiVersion     = "2023-06-01"
	defaultMaxTok  = 4096
)

func init() { provider.Register("anthropic", New) }

// Client is an Anthropic Messages API provider.
type Client struct {
	opts    provider.Options
	baseURL string
	http    *http.Client
}

// New builds an Anthropic provider.
func New(opts provider.Options) (provider.Provider, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &Client{opts: opts, baseURL: base, http: opts.Client()}, nil
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "anthropic" }

func (c *Client) model(req *types.ChatRequest) string {
	if c.opts.Model != "" {
		return c.opts.Model
	}
	return req.Model
}

// --- request translation (unified -> Anthropic) ---

type anthRequest struct {
	Model         string        `json:"model"`
	MaxTokens     int           `json:"max_tokens"`
	Messages      []anthMessage `json:"messages"`
	System        string        `json:"system,omitempty"`
	Temperature   *float64      `json:"temperature,omitempty"`
	TopP          *float64      `json:"top_p,omitempty"`
	StopSequences []string      `json:"stop_sequences,omitempty"`
	Stream        bool          `json:"stream,omitempty"`
}

type anthMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *Client) translate(req *types.ChatRequest, stream bool) anthRequest {
	out := anthRequest{
		Model:     c.model(req),
		MaxTokens: defaultMaxTok,
		TopP:      req.TopP,
		Stream:    stream,
	}
	if req.MaxTokens != nil {
		out.MaxTokens = *req.MaxTokens
	}
	// Anthropic accepts temperature in [0,1]; OpenAI allows [0,2]. Clamp so a
	// valid OpenAI request does not 400 against Anthropic.
	if req.Temperature != nil {
		t := *req.Temperature
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		out.Temperature = &t
	}
	out.StopSequences = stopSequences(req.Stop)

	var systems []string
	for _, m := range req.Messages {
		text := m.Text()
		switch m.Role {
		case "system":
			if text != "" {
				systems = append(systems, text)
			}
		case "assistant":
			appendMessage(&out.Messages, "assistant", text)
		default: // user, tool, function -> user turn
			appendMessage(&out.Messages, "user", text)
		}
	}
	out.System = strings.Join(systems, "\n\n")
	return out
}

// stopSequences normalizes OpenAI's string-or-array `stop` into Anthropic's
// native array form.
func stopSequences(stop any) []string {
	switch v := stop.(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []string:
		return v
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// appendMessage enforces Anthropic's alternation rule: it drops empty turns
// (e.g. assistant tool-call messages with no text) and merges consecutive
// same-role messages, which the Messages API would otherwise reject.
func appendMessage(msgs *[]anthMessage, role, text string) {
	if text == "" {
		return
	}
	if n := len(*msgs); n > 0 && (*msgs)[n-1].Role == role {
		(*msgs)[n-1].Content += "\n\n" + text
		return
	}
	*msgs = append(*msgs, anthMessage{Role: role, Content: text})
}

func mapStopReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default: // end_turn, stop_sequence, ""
		return "stop"
	}
}

func (c *Client) request(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", apiVersion)
	if c.opts.APIKey != "" {
		req.Header.Set("x-api-key", c.opts.APIKey)
	}
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// --- non-streaming ---

type anthResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// ChatCompletion implements provider.Provider.
func (c *Client) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	body, err := json.Marshal(c.translate(req, false))
	if err != nil {
		return nil, err
	}
	httpReq, err := c.request(ctx, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &provider.APIError{Provider: "anthropic", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}

	var ar anthResponse
	if err := json.Unmarshal(data, &ar); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	var sb strings.Builder
	for _, block := range ar.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	finish := mapStopReason(ar.StopReason)
	return &types.ChatResponse{
		ID:      ar.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   ar.Model,
		Choices: []types.Choice{{
			Index:        0,
			Message:      &types.Message{Role: "assistant", Content: sb.String()},
			FinishReason: &finish,
		}},
		Usage: &types.Usage{
			PromptTokens:     ar.Usage.InputTokens,
			CompletionTokens: ar.Usage.OutputTokens,
			TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
	}, nil
}

// --- streaming ---

type anthStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
}

// ChatCompletionStream implements provider.Provider.
func (c *Client) ChatCompletionStream(ctx context.Context, req *types.ChatRequest, emit provider.StreamFunc) error {
	body, err := json.Marshal(c.translate(req, true))
	if err != nil {
		return err
	}
	httpReq, err := c.request(ctx, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return &provider.APIError{Provider: "anthropic", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}

	base := types.ChatChunk{ID: "chatcmpl-anthropic", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: c.model(req)}
	role := base
	role.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{Role: "assistant"}}}
	if err := emit(role); err != nil {
		return err
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var evt anthStreamEvent
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue
		}
		switch evt.Type {
		case "content_block_delta":
			if evt.Delta.Text == "" {
				continue
			}
			c := base
			c.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{Content: evt.Delta.Text}}}
			if err := emit(c); err != nil {
				return err
			}
		case "message_delta":
			if evt.Delta.StopReason == "" {
				continue
			}
			finish := mapStopReason(evt.Delta.StopReason)
			c := base
			c.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{}, FinishReason: &finish}}
			if err := emit(c); err != nil {
				return err
			}
		case "message_stop":
			return nil
		}
	}
	return sc.Err()
}
