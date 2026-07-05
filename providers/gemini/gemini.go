// Package gemini implements the Provider interface for Google's Gemini
// models via the Generative Language API, translating to and from Setu's
// unified OpenAI-style schema so clients need not know the backend is
// Gemini. It registers under both "gemini" and "google".
package gemini

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/types"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com"

func init() {
	provider.Register("gemini", New)
	provider.Register("google", New)
}

// Client is a Google Generative Language (Gemini) provider.
type Client struct {
	opts    provider.Options
	baseURL string
	http    *http.Client
}

// New builds a Gemini provider.
func New(opts provider.Options) (provider.Provider, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &Client{opts: opts, baseURL: base, http: opts.Client()}, nil
}

// Name implements provider.Provider.
func (c *Client) Name() string { return "gemini" }

func (c *Client) model(req *types.ChatRequest) string {
	if c.opts.Model != "" {
		return c.opts.Model
	}
	return req.Model
}

// --- request translation (unified -> Gemini) ---

type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

func (c *Client) translate(req *types.ChatRequest) geminiRequest {
	var out geminiRequest

	var systems []string
	for _, m := range req.Messages {
		text := m.Text()
		switch m.Role {
		case "system":
			// OpenAI system messages become Gemini's systemInstruction.
			if text != "" {
				systems = append(systems, text)
			}
		case "assistant":
			appendContent(&out.Contents, "model", text)
		default: // user, tool, function -> user turn
			appendContent(&out.Contents, "user", text)
		}
	}
	if len(systems) > 0 {
		out.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: strings.Join(systems, "\n\n")}},
		}
	}

	if cfg := genConfig(req); cfg != nil {
		out.GenerationConfig = cfg
	}
	return out
}

// genConfig assembles Gemini's generationConfig from the unified params,
// returning nil when no generation params were supplied.
func genConfig(req *types.ChatRequest) *geminiGenConfig {
	cfg := geminiGenConfig{
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		MaxOutputTokens: req.MaxTokens,
		StopSequences:   stopSequences(req.Stop),
	}
	if cfg.Temperature == nil && cfg.TopP == nil && cfg.MaxOutputTokens == nil && len(cfg.StopSequences) == 0 {
		return nil
	}
	return &cfg
}

// appendContent adds a content turn, dropping empty-text turns (e.g. an
// assistant tool-call message with no text). Gemini does not require
// same-role alternation, so consecutive same-role turns are kept as-is.
func appendContent(contents *[]geminiContent, role, text string) {
	if text == "" {
		return
	}
	*contents = append(*contents, geminiContent{Role: role, Parts: []geminiPart{{Text: text}}})
}

// stopSequences normalizes OpenAI's string-or-array `stop` into Gemini's
// native stopSequences array form.
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

// mapFinishReason translates a Gemini finishReason to an OpenAI one.
func mapFinishReason(r string) string {
	switch r {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION":
		return "content_filter"
	default: // STOP, "", and anything else
		return "stop"
	}
}

// endpoint builds the request URL for the given API method. Streaming
// requests add alt=sse; the API key travels as the ?key= query param.
func (c *Client) endpoint(req *types.ChatRequest, method string, sse bool) string {
	u := fmt.Sprintf("%s/v1beta/models/%s:%s", c.baseURL, c.model(req), method)
	q := url.Values{}
	if sse {
		q.Set("alt", "sse")
	}
	if c.opts.APIKey != "" {
		q.Set("key", c.opts.APIKey)
	}
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

func (c *Client) request(ctx context.Context, endpoint string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The key also travels in the query string; the header is an accepted
	// alternative and set here for callers that strip query params.
	if c.opts.APIKey != "" {
		req.Header.Set("x-goog-api-key", c.opts.APIKey)
	}
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// --- response types (Gemini -> unified) ---

type geminiResponse struct {
	ResponseID string `json:"responseId"`
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
			Role  string       `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// candidateText concatenates every text part of the first candidate.
func (r *geminiResponse) candidateText() string {
	if len(r.Candidates) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, p := range r.Candidates[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

func (r *geminiResponse) finishReason() string {
	if len(r.Candidates) == 0 {
		return ""
	}
	return r.Candidates[0].FinishReason
}

// --- non-streaming ---

// ChatCompletion implements provider.Provider.
func (c *Client) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	body, err := json.Marshal(c.translate(req))
	if err != nil {
		return nil, err
	}
	httpReq, err := c.request(ctx, c.endpoint(req, "generateContent", false), body)
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
		return nil, &provider.APIError{Provider: "gemini", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}

	var gr geminiResponse
	if err := json.Unmarshal(data, &gr); err != nil {
		return nil, fmt.Errorf("gemini: decode response: %w", err)
	}

	id := gr.ResponseID
	if id == "" {
		id = "chatcmpl-gemini"
	}
	finish := mapFinishReason(gr.finishReason())
	return &types.ChatResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   c.model(req),
		Choices: []types.Choice{{
			Index:        0,
			Message:      &types.Message{Role: "assistant", Content: gr.candidateText()},
			FinishReason: &finish,
		}},
		Usage: &types.Usage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gr.UsageMetadata.TotalTokenCount,
		},
	}, nil
}

// --- streaming ---

// ChatCompletionStream implements provider.Provider.
func (c *Client) ChatCompletionStream(ctx context.Context, req *types.ChatRequest, emit provider.StreamFunc) error {
	body, err := json.Marshal(c.translate(req))
	if err != nil {
		return err
	}
	httpReq, err := c.request(ctx, c.endpoint(req, "streamGenerateContent", true), body)
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
		return &provider.APIError{Provider: "gemini", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}

	base := types.ChatChunk{ID: "chatcmpl-gemini", Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: c.model(req)}

	// Initial assistant role delta.
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
		var gr geminiResponse
		if err := json.Unmarshal([]byte(payload), &gr); err != nil {
			continue
		}
		if text := gr.candidateText(); text != "" {
			chunk := base
			chunk.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{Content: text}}}
			if err := emit(chunk); err != nil {
				return err
			}
		}
		if fr := gr.finishReason(); fr != "" {
			finish := mapFinishReason(fr)
			chunk := base
			chunk.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{}, FinishReason: &finish}}
			if gr.UsageMetadata.TotalTokenCount > 0 {
				chunk.Usage = &types.Usage{
					PromptTokens:     gr.UsageMetadata.PromptTokenCount,
					CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
					TotalTokens:      gr.UsageMetadata.TotalTokenCount,
				}
			}
			if err := emit(chunk); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
