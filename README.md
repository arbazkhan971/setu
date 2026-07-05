<div align="center">

# 🌉 Setu

### The open-source LLM gateway — one OpenAI-compatible API for every provider, in a single fast Go binary.

[![CI](https://github.com/arbazkhan971/setu/actions/workflows/ci.yml/badge.svg)](https://github.com/arbazkhan971/setu/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/arbazkhan971/setu)](https://goreportcard.com/report/github.com/arbazkhan971/setu)
[![Go Reference](https://pkg.go.dev/badge/github.com/arbazkhan971/setu.svg)](https://pkg.go.dev/github.com/arbazkhan971/setu)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.22%2B-00ADD8.svg)](https://go.dev)

*Setu (सेतु) is Sanskrit for “bridge.” Point your OpenAI SDK at Setu and reach OpenAI, Anthropic, Groq, Together, OpenRouter, Azure, Ollama, and any OpenAI-compatible endpoint — with routing, fallbacks, and load balancing built in.*

</div>

---

## Why Setu?

The Python LLM gateways are powerful but heavy: a Python runtime, dozens of dependencies, slow cold starts, and real memory pressure under load. Setu is a **single statically-linked binary** — drop it on a box, in a container, or in a Lambda, and it starts in milliseconds.

- 🔌 **Drop-in OpenAI API** — `/v1/chat/completions`, `/v1/models`. Change one base URL; keep your existing SDK.
- 🌍 **20+ providers, one dialect** — **OpenAI, Anthropic, Gemini & Cohere** with native translation; **Groq, Mistral, DeepSeek, Together, OpenRouter, Fireworks, xAI, Perplexity, NVIDIA, Ollama, LM Studio** and more as first-class named providers; plus *any* OpenAI-compatible endpoint via `base_url`.
- ⚖️ **Weighted load balancing** — split traffic across keys, regions, or models by weight (smooth weighted round-robin), with automatic retry coverage of every backend.
- 🔁 **Reliability built in** — automatic **retries**, cross-model **fallbacks**, and **round-robin load balancing** across keys/regions.
- 🌊 **Real streaming** — server-sent events passthrough, unified into OpenAI chunk format across every backend.
- 🔑 **Passthrough-safe** — unknown request fields (`tools`, `response_format`, `logprobs`, …) are preserved verbatim, so new provider features work before Setu even knows about them.
- 🪶 **Tiny & fast** — ~9 MB binary, no runtime dependencies, ~millisecond startup, low idle memory.
- 📋 **LiteLLM-shaped config** — `model_list` + `router_settings`. Migrating is mostly copy-paste.

## 60-second quickstart

```bash
# 1. Install (Go 1.22+)
go install github.com/arbazkhan971/setu/cmd/setu@latest

# 2. Configure
cp config.example.yaml config.yaml
export OPENAI_API_KEY=sk-...

# 3. Run
setu --config config.yaml     # listening on :4000
```

Now talk to it with **any OpenAI client**:

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Say hi from Setu"}]
  }'
```

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:4000/v1", api_key="unused")
print(client.chat.completions.create(
    model="gpt-4o",                      # or "claude", "llama-3.1-70b", ...
    messages=[{"role": "user", "content": "Say hi from Setu"}],
).choices[0].message.content)
```

No keys handy? Setu ships a zero-credential `mock` provider so you can try the whole flow offline:

```bash
echo 'model_list: [{model_name: mock, provider: mock, params: {}}]' > config.yaml
setu --config config.yaml
curl -s localhost:4000/v1/chat/completions -d '{"model":"mock","messages":[{"role":"user","content":"hello"}]}'
```

## Configuration

Setu reads a YAML file whose shape mirrors LiteLLM's proxy config:

```yaml
model_list:
  - model_name: gpt-4o                    # what clients request
    provider: openai
    params:
      model: gpt-4o                       # upstream model id
      api_key: os.environ/OPENAI_API_KEY  # os.environ/VAR is resolved at load

  - model_name: claude
    provider: anthropic
    params:
      model: claude-3-5-sonnet-20241022
      api_key: os.environ/ANTHROPIC_API_KEY

  - model_name: llama-3.1-70b             # any OpenAI-compatible endpoint
    provider: openai
    params:
      model: llama-3.1-70b-versatile
      base_url: https://api.groq.com/openai/v1
      api_key: os.environ/GROQ_API_KEY

router_settings:
  fallbacks:
    - gpt-4o: [claude]                    # if gpt-4o fails, transparently use claude
  max_retries: 2

server:
  port: 4000
  master_key: os.environ/SETU_MASTER_KEY  # require this bearer token; empty = open
```

## How it works

```
                         ┌───────────────────────────────────────────┐
   OpenAI SDK / curl     │                   Setu                     │
  ──────────────────►    │  HTTP server ─► auth ─► gateway            │
   POST /v1/chat/...     │                         │                  │
                         │            ┌────────────┼────────────┐     │
                         │      load-balance    retries      fallback │
                         │            │            │            │     │
                         │        ┌───▼───┐   ┌────▼────┐  ┌────▼────┐ │
                         │        │OpenAI │   │Anthropic│  │ Groq /  │ │
                         │        │adapter│   │ adapter │  │ compat  │ │
                         │        └───────┘   └─────────┘  └─────────┘ │
                         └───────────────────────────────────────────┘
```

Each **provider adapter** translates the unified OpenAI-style schema to and from a vendor API. The **gateway** maps a requested model name to one or more **deployments** and applies load balancing, retries, and fallbacks. The **server** exposes it all as an OpenAI-compatible HTTP API.

## Where Setu fits

| | **Setu** | LiteLLM | one-api | Portkey Gateway |
|---|:---:|:---:|:---:|:---:|
| Language | **Go** | Python | Go | TypeScript |
| Ships as | **single binary** | pip + runtime | binary | Node runtime |
| OpenAI-compatible API | ✅ | ✅ | ✅ | ✅ |
| Retries / fallbacks / LB | ✅ | ✅ | partial | ✅ |
| Streaming | ✅ | ✅ | ✅ | ✅ |
| Unknown-field passthrough | ✅ | ✅ | partial | ✅ |
| Cold start | **~ms** | seconds | ~ms | ~100s ms |

> Setu is young and moving fast. See the [roadmap](#roadmap) for what's landing next.

## Roadmap

**Shipping now (M1):** OpenAI + Anthropic + OpenAI-compatible providers · streaming · retries · fallbacks · round-robin load balancing · master-key auth · field passthrough.

- [ ] **M2 — breadth & reliability:** native Gemini, Bedrock, Vertex, Cohere, Mistral; weighted & least-latency load balancing; per-deployment timeouts & health checks.
- [ ] **M3 — gateway features:** virtual API keys, per-key budgets & spend tracking, response caching, rate limiting, `/v1/embeddings`.
- [ ] **M4 — DX & ops:** Prometheus metrics, structured request logs, Grafana dashboard, admin UI, Helm chart, benchmarks vs LiteLLM.

Want something bumped up? [Open an issue](https://github.com/arbazkhan971/setu/issues).

## Contributing

Contributions are very welcome — a new provider adapter is often <150 lines. See [CONTRIBUTING.md](CONTRIBUTING.md). Good first issues are labeled [`good first issue`](https://github.com/arbazkhan971/setu/labels/good%20first%20issue).

```bash
git clone https://github.com/arbazkhan971/setu && cd setu
make test      # race-enabled test suite
make run       # build + run with config.yaml
```

## License

[MIT](LICENSE) © arbazkhan971

<div align="center">

**If Setu saves you a dependency headache, drop a ⭐ — it genuinely helps.**

</div>
