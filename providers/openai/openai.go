// Package openai implements the Provider interface for the OpenAI API
// and any OpenAI-compatible endpoint (Azure OpenAI, Groq, Together,
// OpenRouter, Ollama, vLLM, and others) via a configurable base URL.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/types"
)

const defaultBaseURL = "https://api.openai.com/v1"

func init() { provider.Register("openai", New) }

// Client is an OpenAI-compatible chat provider.
type Client struct {
	opts    provider.Options
	baseURL string
	http    *http.Client
}

// New builds an OpenAI-compatible provider.
func New(opts provider.Options) (provider.Provider, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &Client{opts: opts, baseURL: base, http: opts.Client()}, nil
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "openai" }

func (c *Client) body(req *types.ChatRequest, stream bool) ([]byte, error) {
	r := *req // shallow copy; we only override scalar fields
	if c.opts.Model != "" {
		r.Model = c.opts.Model
	}
	r.Stream = stream
	return json.Marshal(&r)
}

func (c *Client) request(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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

// ChatCompletion implements provider.Provider.
func (c *Client) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	body, err := c.body(req, false)
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
		return nil, &provider.APIError{Provider: "openai", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	var out types.ChatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	return &out, nil
}

// ChatCompletionStream implements provider.Provider.
func (c *Client) ChatCompletionStream(ctx context.Context, req *types.ChatRequest, emit provider.StreamFunc) error {
	body, err := c.body(req, true)
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
		return &provider.APIError{Provider: "openai", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
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
		if payload == "[DONE]" {
			break
		}
		var chunk types.ChatChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // tolerate keep-alive / non-JSON lines
		}
		if err := emit(chunk); err != nil {
			return err
		}
	}
	return sc.Err()
}
