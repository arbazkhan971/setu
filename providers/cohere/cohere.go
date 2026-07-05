// Package cohere implements the Provider interface for Cohere's Chat API
// v2, translating to and from Setu's unified OpenAI-style schema so clients
// need not know the backend is Cohere.
package cohere

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

const defaultBaseURL = "https://api.cohere.com"

func init() { provider.Register("cohere", New) }

// Client is a Cohere Chat API v2 provider.
type Client struct {
	opts    provider.Options
	baseURL string
	http    *http.Client
}

// New builds a Cohere provider.
func New(opts provider.Options) (provider.Provider, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &Client{opts: opts, baseURL: base, http: opts.Client()}, nil
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "cohere" }

func (c *Client) model(req *types.ChatRequest) string {
	if c.opts.Model != "" {
		return c.opts.Model
	}
	return req.Model
}

// --- request translation (unified -> Cohere) ---

type cohereRequest struct {
	Model         string          `json:"model"`
	Messages      []cohereMessage `json:"messages"`
	Temperature   *float64        `json:"temperature,omitempty"`
	MaxTokens     *int            `json:"max_tokens,omitempty"`
	P             *float64        `json:"p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
}

type cohereMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *Client) translate(req *types.ChatRequest, stream bool) cohereRequest {
	// Cohere Chat v2 carries the system prompt inline as a role="system"
	// message, so unlike some vendors there is no separate system field to
	// extract; roles map through directly.
	out := cohereRequest{
		Model:         c.model(req),
		Temperature:   req.Temperature,
		MaxTokens:     req.MaxTokens,
		P:             req.TopP, // OpenAI top_p -> Cohere nucleus sampling "p"
		StopSequences: stopSequences(req.Stop),
		Stream:        stream,
	}
	for _, m := range req.Messages {
		text := m.Text()
		if text == "" {
			// Skip empty turns (e.g. an assistant tool-call-only message);
			// Cohere requires non-empty content on each message.
			continue
		}
		out.Messages = append(out.Messages, cohereMessage{Role: mapRole(m.Role), Content: text})
	}
	return out
}

// mapRole normalizes unified roles to the set Cohere Chat v2 accepts.
func mapRole(role string) string {
	switch role {
	case "system":
		return "system"
	case "assistant":
		return "assistant"
	default: // user, tool, function -> user turn
		return "user"
	}
}

// stopSequences normalizes OpenAI's string-or-array `stop` into Cohere's
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

// mapFinishReason translates Cohere's finish reasons to OpenAI's.
func mapFinishReason(r string) string {
	switch r {
	case "MAX_TOKENS":
		return "length"
	default: // COMPLETE, STOP_SEQUENCE, ERROR, ...
		return "stop"
	}
}

func (c *Client) request(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v2/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	}
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// --- non-streaming ---

type cohereResponse struct {
	ID      string `json:"id"`
	Message struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
	Usage        struct {
		Tokens struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"tokens"`
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
		return nil, &provider.APIError{Provider: "cohere", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}

	var cr cohereResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return nil, fmt.Errorf("cohere: decode response: %w", err)
	}

	var sb strings.Builder
	for _, block := range cr.Message.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	finish := mapFinishReason(cr.FinishReason)
	return &types.ChatResponse{
		ID:      cr.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   c.model(req),
		Choices: []types.Choice{{
			Index:        0,
			Message:      &types.Message{Role: "assistant", Content: sb.String()},
			FinishReason: &finish,
		}},
		Usage: &types.Usage{
			PromptTokens:     cr.Usage.Tokens.InputTokens,
			CompletionTokens: cr.Usage.Tokens.OutputTokens,
			TotalTokens:      cr.Usage.Tokens.InputTokens + cr.Usage.Tokens.OutputTokens,
		},
	}, nil
}

// --- streaming ---

type cohereStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Message struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
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
		return &provider.APIError{Provider: "cohere", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}

	base := types.ChatChunk{ID: "chatcmpl-cohere", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: c.model(req)}
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
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var evt cohereStreamEvent
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue
		}
		switch evt.Type {
		case "content-delta":
			if evt.Delta.Message.Content.Text == "" {
				continue
			}
			ch := base
			ch.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{Content: evt.Delta.Message.Content.Text}}}
			if err := emit(ch); err != nil {
				return err
			}
		case "message-end":
			finish := mapFinishReason(evt.Delta.FinishReason)
			ch := base
			ch.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{}, FinishReason: &finish}}
			if err := emit(ch); err != nil {
				return err
			}
			return nil
		}
		// Ignore message-start, content-start, content-end and any others.
	}
	return sc.Err()
}
