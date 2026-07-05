// Package types defines Setu's unified, OpenAI-compatible request and
// response schema. Every provider adapter translates a vendor API to and
// from these types, so clients speak one dialect regardless of backend.
package types

import (
	"encoding/json"
	"strings"
)

// knownChatFields are the top-level request fields Setu models explicitly.
// Anything else is preserved in ChatRequest.Extra for passthrough.
var knownChatFields = []string{
	"model", "messages", "stream", "temperature", "top_p", "n", "stop",
	"max_tokens", "presence_penalty", "frequency_penalty", "seed", "user",
	"tools", "tool_choice", "response_format", "stream_options",
}

// ChatRequest is the unified chat completion request. Unrecognized
// top-level fields are retained in Extra and re-emitted on marshal, so
// provider-specific and forward-compatible params pass through untouched.
type ChatRequest struct {
	Model            string    `json:"model"`
	Messages         []Message `json:"messages"`
	Stream           bool      `json:"stream,omitempty"`
	Temperature      *float64  `json:"temperature,omitempty"`
	TopP             *float64  `json:"top_p,omitempty"`
	N                *int      `json:"n,omitempty"`
	Stop             any       `json:"stop,omitempty"`
	MaxTokens        *int      `json:"max_tokens,omitempty"`
	PresencePenalty  *float64  `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64  `json:"frequency_penalty,omitempty"`
	Seed             *int      `json:"seed,omitempty"`
	User             string    `json:"user,omitempty"`
	Tools            []Tool    `json:"tools,omitempty"`
	ToolChoice       any       `json:"tool_choice,omitempty"`
	ResponseFormat   any       `json:"response_format,omitempty"`
	StreamOptions    any       `json:"stream_options,omitempty"`

	// Extra holds unrecognized top-level fields for passthrough.
	Extra map[string]json.RawMessage `json:"-"`
}

// MarshalJSON emits the known fields plus any preserved Extra fields.
func (r ChatRequest) MarshalJSON() ([]byte, error) {
	type alias ChatRequest
	b, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return b, nil
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON decodes the known fields and captures the rest in Extra.
func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	type alias ChatRequest
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*r = ChatRequest(a)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, k := range knownChatFields {
		delete(raw, k)
	}
	if len(raw) > 0 {
		r.Extra = raw
	}
	return nil
}

// Message is a single chat message. Content is either a string or an
// array of content parts (multimodal), matching the OpenAI schema.
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Text returns the message content as plain text, flattening multimodal
// content parts down to their text segments.
func (m Message) Text() string {
	switch v := m.Content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, part := range v {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := pm["type"].(string); t == "text" {
				if s, ok := pm["text"].(string); ok {
					sb.WriteString(s)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// Tool describes a function tool available to the model.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef is a tool's function schema.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
	Index    *int         `json:"index,omitempty"`
}

// FunctionCall carries a tool call's function name and JSON arguments.
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ChatResponse is a non-streaming chat completion result.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice is one completion candidate.
type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"`
	FinishReason *string  `json:"finish_reason"`
}

// Usage reports token accounting for a request.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatChunk is a single streamed delta (OpenAI chat.completion.chunk).
type ChatChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

// ChunkChoice is one streamed choice delta.
type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

// Delta is the incremental message content in a stream chunk.
type Delta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}
