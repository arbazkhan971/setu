# Configuration reference

Setu is configured with a single YAML file, passed via `--config`
(default `config.yaml`). It has three top-level sections.

## `model_list`

A list of deployments. Each entry maps a **client-facing model name** to a
**provider** and its **params**.

```yaml
model_list:
  - model_name: gpt-4o          # the name clients pass as "model"
    provider: openai            # a registered provider (see docs/providers.md)
    params:
      model: gpt-4o             # the upstream model id sent to the provider
      api_key: os.environ/OPENAI_API_KEY
      base_url: https://...     # optional; override the vendor endpoint
```

### `params`

| Key | Meaning |
|---|---|
| `model` | Upstream model id. Overrides the request's `model`. |
| `api_key` | Provider credential. Supports `os.environ/VAR`. |
| `base_url` | Override the vendor base URL (self-host, Azure, proxy). |

Extra keys under `params` are retained and made available to the provider.

### Load balancing

Give **the same `model_name` to multiple entries** to load-balance across
them. Setu uses smooth weighted round-robin: set an optional `weight` (default
1) to bias the split, and every backend is still covered by retries.

```yaml
model_list:
  - model_name: gpt-4o
    provider: openai
    params: { model: gpt-4o, api_key: os.environ/OPENAI_KEY_A, weight: 3 }
  - model_name: gpt-4o          # second key / region, same public name
    provider: openai
    params: { model: gpt-4o, api_key: os.environ/OPENAI_KEY_B, weight: 1 }
```

With these weights, roughly 3 of every 4 requests start on key A and 1 on
key B; if a chosen backend fails, the request retries across the others.

## `router_settings`

```yaml
router_settings:
  fallbacks:
    - gpt-4o: [claude, llama-3.1-70b]   # try these in order if gpt-4o fails
  max_retries: 2                        # attempts per model before falling back
```

- **`fallbacks`** — a list of single-key maps from a model name to an ordered
  list of fallback model names. On error, Setu transparently retries the next
  model in the chain. (For streaming, fallback only occurs before the first
  byte is sent.)
- **`max_retries`** — how many times a single model is attempted (across its
  deployments) before moving to fallbacks. Default `2`.

## `server`

```yaml
server:
  port: 4000                          # overridden by --port
  master_key: os.environ/SETU_MASTER_KEY
```

- **`port`** — listen port (default `4000`; `--port` wins).
- **`master_key`** — if set, `/v1/*` routes require
  `Authorization: Bearer <master_key>`. Leave empty to disable auth.

## Environment variable resolution

Any string value of the form `os.environ/NAME` is replaced with the value of
environment variable `NAME` at load time. This is LiteLLM-compatible, so
existing configs port directly.
