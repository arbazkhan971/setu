# Contributing to Setu

Thanks for helping build the open-source LLM gateway! Contributions of all
sizes are welcome — bug reports, docs, and especially new provider adapters.

## Getting started

```bash
git clone https://github.com/arbazkhan971/setu && cd setu
make test      # race-enabled test suite
make vet fmt   # static checks + formatting
make run       # build + run with ./config.yaml
```

Setu targets **Go 1.22+** and has a single third-party dependency (`yaml.v3`).
Keep it lean.

## Project layout

| Package | Responsibility |
|---|---|
| `types/` | Unified, OpenAI-compatible request/response schema |
| `provider/` | The `Provider` interface + a name-based registry |
| `providers/*` | One adapter per backend (openai, anthropic, mock, …) |
| `gateway/` | Model routing: load balancing, retries, fallbacks |
| `server/` | OpenAI-compatible HTTP API + middleware |
| `config/` | YAML loader → gateway builder |
| `cmd/setu/` | The CLI entrypoint |

## Adding a provider

A provider is usually **under 150 lines**. Implement the interface:

```go
type Provider interface {
    Name() string
    ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
    ChatCompletionStream(ctx context.Context, req *types.ChatRequest, emit provider.StreamFunc) error
}
```

1. Create `providers/<name>/<name>.go`.
2. Register it in `init()`:
   ```go
   func init() { provider.Register("<name>", New) }
   ```
3. Translate the unified request to the vendor API, and translate the
   response/stream back into `types.ChatResponse` / `types.ChatChunk`.
4. Add a blank import in `cmd/setu/main.go` so it's linked in.
5. Add a table-driven test. Mirror `providers/anthropic` for a translating
   provider or `providers/openai` for an OpenAI-compatible one.

If the backend already speaks the OpenAI wire format, you often only need a
config entry with a `base_url` — no new adapter required.

## Pull requests

- Keep PRs focused; one provider or one fix per PR.
- `make test vet fmt` must pass. CI runs the race detector.
- Add or update tests for behavior changes.
- Describe the change and how you verified it.

By contributing you agree your work is licensed under the project's
[MIT license](LICENSE).
