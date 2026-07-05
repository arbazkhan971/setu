// Package compat registers named OpenAI-compatible providers. Many hosted
// inference APIs speak the OpenAI wire format and differ only by base URL, so
// this lets users write `provider: groq` instead of repeating base_url. Each
// is a thin wrapper over the openai adapter with a sensible default endpoint.
package compat

import (
	"github.com/arbazkhan971/setu/provider"
	"github.com/arbazkhan971/setu/providers/openai"
)

// endpoint describes a named OpenAI-compatible backend.
type endpoint struct {
	name    string
	baseURL string
	keyEnv  string // for documentation / examples
}

// Endpoints is the built-in set of named OpenAI-compatible providers.
var Endpoints = []endpoint{
	{"groq", "https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	{"mistral", "https://api.mistral.ai/v1", "MISTRAL_API_KEY"},
	{"deepseek", "https://api.deepseek.com/v1", "DEEPSEEK_API_KEY"},
	{"together", "https://api.together.xyz/v1", "TOGETHER_API_KEY"},
	{"openrouter", "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
	{"fireworks", "https://api.fireworks.ai/inference/v1", "FIREWORKS_API_KEY"},
	{"xai", "https://api.x.ai/v1", "XAI_API_KEY"},
	{"perplexity", "https://api.perplexity.ai", "PERPLEXITY_API_KEY"},
	{"deepinfra", "https://api.deepinfra.com/v1/openai", "DEEPINFRA_API_KEY"},
	{"anyscale", "https://api.endpoints.anyscale.com/v1", "ANYSCALE_API_KEY"},
	{"nvidia", "https://integrate.api.nvidia.com/v1", "NVIDIA_API_KEY"},
	{"novita", "https://api.novita.ai/v3/openai", "NOVITA_API_KEY"},
	{"ollama", "http://localhost:11434/v1", "OLLAMA_API_KEY"},
	{"lmstudio", "http://localhost:1234/v1", "LMSTUDIO_API_KEY"},
}

func init() {
	for _, e := range Endpoints {
		e := e
		provider.Register(e.name, func(opts provider.Options) (provider.Provider, error) {
			if opts.BaseURL == "" {
				opts.BaseURL = e.baseURL
			}
			inner, err := openai.New(opts)
			if err != nil {
				return nil, err
			}
			return &named{Provider: inner, name: e.name}, nil
		})
	}
}

// named overrides the wrapped provider's Name so logs and errors attribute the
// request to the configured backend (e.g. "groq") rather than "openai". The
// embedded Provider promotes ChatCompletion and ChatCompletionStream.
type named struct {
	provider.Provider
	name string
}

func (n *named) Name() string { return n.name }
