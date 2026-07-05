# Setu — Iteration Index

Goal: build a Go implementation of an LLM gateway (LiteLLM-class) that is a
genuinely better alternative — single fast binary, full OSS gateway features —
and grow it to 10k+ GitHub stars. Keep building, testing, and shipping.

Repo: https://github.com/arbazkhan971/setu · Positioning: **the open-source LLM gateway**

## Milestones

- **M1 — Core + drop-in proxy** — _in progress_
  Unified OpenAI-compatible schema, OpenAI + Anthropic + OpenAI-compatible +
  mock providers, streaming, gateway (round-robin LB, retries, fallbacks),
  master-key auth, YAML config, HTTP server, tests, README, CI, Docker.
- **M2 — Breadth & reliability** — _planned_
  Native Gemini/Bedrock/Vertex/Cohere/Mistral; weighted + least-latency LB;
  per-deployment timeouts & health checks; `/v1/embeddings`.
- **M3 — Gateway features** — _planned_
  Virtual keys, budgets & spend tracking, response caching, rate limiting.
- **M4 — DX & ops** — _planned_
  Prometheus metrics, structured logs, dashboards, admin UI, Helm, benchmarks
  vs LiteLLM, launch (Show HN / r/golang / r/LocalLLaMA).

## Iteration log

### iter 1 — 2026-07-05 — M1 scaffold
- Scaffolded module `github.com/arbazkhan971/setu` (Go 1.22, one dep: yaml.v3).
- Packages: `types`, `provider`, `providers/{openai,anthropic,mock}`,
  `gateway`, `server`, `config`, `cmd/setu`.
- Unified OpenAI-compatible schema with unknown-field passthrough (`Extra`).
- OpenAI adapter (native + all OpenAI-compatible endpoints via base_url),
  Anthropic adapter (full Messages API translation + SSE), mock adapter.
- Gateway: round-robin load balancing, retries, cross-model fallbacks
  (streaming fallback guarded to pre-first-byte).
- Server: `/v1/chat/completions` (stream + non-stream), `/v1/models`,
  `/health`, master-key auth, request logging.
- Tests: types round-trip, gateway routing/fallback/LB, server end-to-end
  (incl. SSE + auth). `go test -race ./...` green; `go vet` clean.
- Verified end-to-end with a live server (curl: health, models, chat,
  streaming SSE with `[DONE]`, extra-field passthrough).
- Docs: README (Show-HN grade), CONTRIBUTING, docs/{configuration,providers},
  examples, Dockerfile (distroless), GitHub Actions CI.
- Ran an adversarial multi-agent correctness review of the core.

### iter 2 — 2026-07-05 — M1 hardening (applied adversarial review)
Ran a 3-lens adversarial review + per-finding verification workflow (14 agents,
11 findings → 8 confirmed). Applied all confirmed fixes + 3 server-layer fixes:
- **gateway**: per-request deployment rotation (fixes shared round-robin counter
  that, under concurrency, could starve a healthy deployment → spurious 502s /
  wrong-model fallbacks); retryable-error classification via typed
  `provider.APIError` (4xx no longer retried/fanned out); streaming fallback no
  longer defeated by a content-free role delta.
- **anthropic**: array-form `stop` supported; consecutive same-role messages
  merged + empty turns dropped (avoids Anthropic 400s); temperature clamped to
  [0,1]; `created` timestamp set.
- **server**: unknown model → 404 `invalid_request_error` (not 502; streaming
  rejects before opening the event-stream); constant-time master-key compare;
  accurate Flusher detection via unwrap chain (removed always-true assertion).
- Added regression tests incl. a 500-goroutine concurrency-coverage test that
  fails on the old router and passes on the new one. 24 tests green under -race.

_Next: push public repo, then start M2 provider breadth (Gemini/Bedrock/Vertex/
Cohere/Mistral) + `/v1/embeddings` + weighted load balancing._
