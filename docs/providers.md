# Providers

Setu talks to backends through **provider adapters**. Each adapter translates
Setu's unified OpenAI-style schema to and from a specific vendor API.

## Built-in providers

| `provider:` | Backend | Notes |
|---|---|---|
| `openai` | OpenAI **and any OpenAI-compatible API** | Set `base_url` to reach Groq, Together, OpenRouter, Azure OpenAI, Ollama, vLLM, DeepSeek, Mistral, Fireworks, and more. |
| `anthropic` | Anthropic Messages API (Claude) | Full request/response + streaming translation. |
| `mock` | Offline echo backend | No credentials; great for local dev, CI, and demos. |

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
