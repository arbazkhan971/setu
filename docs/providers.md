# Providers

Setu talks to backends through **provider adapters**. Each adapter translates
Setu's unified OpenAI-style schema to and from a specific vendor API.

## Built-in providers

| `provider:` | Backend | Notes |
|---|---|---|
| `openai` | OpenAI **and any OpenAI-compatible API** | Set `base_url` to reach Groq, Together, OpenRouter, Azure OpenAI, Ollama, vLLM, DeepSeek, Mistral, Fireworks, and more. |
| `anthropic` | Anthropic Messages API (Claude) | Full request/response + streaming translation. |
| `gemini` / `google` | Google Gemini (Generative Language API) | Native translation to `generateContent` / `streamGenerateContent`. |
| `cohere` | Cohere Chat API v2 | Native translation to `/v2/chat`. |
| `mock` | Offline echo backend | No credentials; great for local dev, CI, and demos. |

## Named OpenAI-compatible providers

These backends speak the OpenAI wire format, so Setu ships them as first-class
names with the base URL pre-filled — just set the API key. Override `base_url`
in `params` to point at a different host or region.

| `provider:` | Backend | Default base URL | Key env (convention) |
|---|---|---|---|
| `groq` | Groq | `api.groq.com/openai/v1` | `GROQ_API_KEY` |
| `mistral` | Mistral AI | `api.mistral.ai/v1` | `MISTRAL_API_KEY` |
| `deepseek` | DeepSeek | `api.deepseek.com/v1` | `DEEPSEEK_API_KEY` |
| `together` | Together AI | `api.together.xyz/v1` | `TOGETHER_API_KEY` |
| `openrouter` | OpenRouter | `openrouter.ai/api/v1` | `OPENROUTER_API_KEY` |
| `fireworks` | Fireworks AI | `api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` |
| `xai` | xAI (Grok) | `api.x.ai/v1` | `XAI_API_KEY` |
| `perplexity` | Perplexity | `api.perplexity.ai` | `PERPLEXITY_API_KEY` |
| `deepinfra` | DeepInfra | `api.deepinfra.com/v1/openai` | `DEEPINFRA_API_KEY` |
| `anyscale` | Anyscale | `api.endpoints.anyscale.com/v1` | `ANYSCALE_API_KEY` |
| `nvidia` | NVIDIA NIM | `integrate.api.nvidia.com/v1` | `NVIDIA_API_KEY` |
| `novita` | Novita AI | `api.novita.ai/v3/openai` | `NOVITA_API_KEY` |
| `ollama` | Ollama (local) | `localhost:11434/v1` | — |
| `lmstudio` | LM Studio (local) | `localhost:1234/v1` | — |

```yaml
model_list:
  - model_name: fast
    provider: groq          # base_url is filled in automatically
    params:
      model: llama-3.1-8b-instant
      api_key: os.environ/GROQ_API_KEY
```

## OpenAI-compatible endpoints

Most hosted inference APIs speak the OpenAI wire format. To use them, keep
`provider: openai` and point `base_url` at the endpoint:

```yaml
model_list:
  - model_name: llama-3.1-70b
    provider: openai
    params:
      model: llama-3.1-70b-versatile
      base_url: https://api.groq.com/openai/v1
      api_key: os.environ/GROQ_API_KEY

  - model_name: deepseek-chat
    provider: openai
    params:
      model: deepseek-chat
      base_url: https://api.deepseek.com/v1
      api_key: os.environ/DEEPSEEK_API_KEY

  - model_name: local
    provider: openai
    params:
      model: llama3.1
      base_url: http://localhost:11434/v1   # Ollama
```

## Coming soon

Native adapters for **Gemini**, **AWS Bedrock**, **Vertex AI**, **Cohere**,
and **Mistral** are on the [roadmap](../README.md#roadmap). Want one sooner?
A translating adapter is usually under 150 lines — see
[CONTRIBUTING.md](../CONTRIBUTING.md).
