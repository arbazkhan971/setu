// Package mock provides an offline provider that echoes the last user
// message. It powers tests and lets Setu run with zero credentials.
package mock

import (
	"context"
	"strings"

	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/types"
)

func init() { provider.Register("mock", New) }

// Mock is a deterministic, offline provider.
type Mock struct{ opts provider.Options }

// New builds a mock provider.
func New(opts provider.Options) (provider.Provider, error) {
	return &Mock{opts: opts}, nil
}

// Name implements provider.Provider.
func (m *Mock) Name() string { return "mock" }

func (m *Mock) reply(req *types.ChatRequest) string {
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Text()
	}
	return "echo: " + last
}

// ChatCompletion returns a single echoed completion.
func (m *Mock) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	content := m.reply(req)
	stop := "stop"
	return &types.ChatResponse{
		ID:     "chatcmpl-mock",
		Object: "chat.completion",
		Model:  req.Model,
		Choices: []types.Choice{{
			Index:        0,
			Message:      &types.Message{Role: "assistant", Content: content},
			FinishReason: &stop,
		}},
		Usage: &types.Usage{PromptTokens: 1, CompletionTokens: len(strings.Fields(content)), TotalTokens: 1 + len(strings.Fields(content))},
	}, nil
}

// ChatCompletionStream streams the echoed reply one word at a time.
func (m *Mock) ChatCompletionStream(ctx context.Context, req *types.ChatRequest, emit provider.StreamFunc) error {
	base := types.ChatChunk{ID: "chatcmpl-mock", Object: "chat.completion.chunk", Model: req.Model}

	role := base
	role.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{Role: "assistant"}}}
	if err := emit(role); err != nil {
		return err
	}

	words := strings.Fields(m.reply(req))
	for i, w := range words {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		token := w
		if i < len(words)-1 {
			token += " "
		}
		c := base
		c.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{Content: token}}}
		if err := emit(c); err != nil {
			return err
		}
	}

	stop := "stop"
	final := base
	final.Choices = []types.ChunkChoice{{Index: 0, Delta: types.Delta{}, FinishReason: &stop}}
	return emit(final)
}
