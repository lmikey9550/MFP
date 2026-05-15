# MFP — Model Failover Proxy

MFP is a lightweight Go service that exposes an OpenAI-compatible proxy API and routes each frontend virtual model to one or more backend provider models. It is designed for local teams, coding agents, and model-heavy applications that need simple failover, sticky routing, health tracking, and an admin console without deploying a large gateway stack.

中文文档：[`README.zh-CN.md`](README.zh-CN.md)  
User manual: [`docs/user-manual.en.md`](docs/user-manual.en.md) / [`docs/user-manual.zh-CN.md`](docs/user-manual.zh-CN.md)

## What MFP does

- Accepts client requests under `POST /v1/*`.
- Reads the request `model` value as a frontend virtual model ID.
- Selects a backend candidate model from the virtual model configuration.
- Rewrites only the top-level `model` value to the selected backend model ID.
- Forwards the original request path, body shape, and non-hop-by-hop headers to the backend provider.
- Fails over to the next candidate when configured error rules mark an attempt as retryable/failover-worthy.

This keeps MFP intentionally simple: the backend provider decides which `/v1/*` endpoints it supports. MFP does not try to maintain a hardcoded model-capability matrix.

## Key features

- **Transparent OpenAI-style forwarding**: `POST /v1/*` is proxied, including chat, responses, embeddings, audio, image, rerank, and future provider-specific endpoints.
- **JSON and multipart support**: rewrites `model` in JSON request bodies and multipart form fields.
- **Virtual models**: expose stable frontend names such as `smart`, backed by ordered provider/model candidates.
- **Failover and retry rules**: classify upstream errors and retry, fail over, or reject.
- **Sticky routing**: optionally keep the same agent/session/global scope on the last successful backend model.
- **Basic congestion routing**: route away from overloaded first candidates when enabled.
- **Health and request logs**: track model health, recent requests, attempts, failover counts, latency, and cooldown state.
- **Admin console**: browser UI for providers, frontend models, rules, settings, health, logs, and testing.
- **Mock provider**: bundled mock OpenAI-compatible provider for local demos and smoke tests.
- **No database dependency**: JSON config plus file-backed runtime state.

## Quick start with Docker Compose

```bash
docker compose up --build
```

Then open:

- Proxy API: `http://127.0.0.1:18320`
- Admin console: `http://127.0.0.1:18321`

Default demo admin account:

- Username: `admin`
- Password: `change-me`

The demo starts MFP plus two mock providers. The default frontend model is `smart`.

### Smoke test

```bash
curl -s http://127.0.0.1:18320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

Expected response includes the backend model, for example:

```json
{
  "model": "provider-model-a",
  "provider": "mock-primary"
}
```

Test failover by including the mock failure keyword:

```bash
curl -s http://127.0.0.1:18320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "please [failover]"}]
  }'
```

The response should come from the next available backend candidate.

## Run locally without Docker

Start a mock provider:

```bash
go run ./cmd/mock-provider
```

In another terminal, start MFP:

```bash
MFP_CONFIG=configs/dev.json go run ./cmd/mfp
```

Run tests:

```bash
go test ./...
go vet ./...
```

Build binaries:

```bash
go build -o build/mfp ./cmd/mfp
go build -o build/mock-provider ./cmd/mock-provider
```

## Configuration overview

MFP reads JSON config from:

1. `MFP_CONFIG`, if set.
2. The built-in/default path used by the application.
3. Example files under `configs/`.

Important top-level fields:

```json
{
  "api_listen_addr": ":18320",
  "admin_listen_addr": ":18321",
  "data_dir": "data",
  "proxy": {
    "request_timeout_ms": 120000,
    "max_body_bytes": 67108864,
    "trust_authorization_header": false
  },
  "admin": { "accounts": [] },
  "providers": [],
  "virtual_models": [],
  "error_rules": []
}
```

### Providers

A provider describes one backend API service:

```json
{
  "id": "example-provider",
  "type": "openai_compatible",
  "base_url": "https://provider.example.com/v1",
  "credential_env": "PROVIDER_API_KEY",
  "headers_template": {},
  "timeout_ms": 120000,
  "enabled": true,
  "models": [
    { "id": "provider-model-id", "label": "Provider Model" }
  ]
}
```

`base_url` may include `/v1`. MFP avoids duplicating `/v1` when forwarding request paths.

### Virtual models

A virtual model is what clients send in the `model` field:

```json
{
  "id": "smart",
  "display_name": "Smart Model",
  "candidates": [
    { "provider_id": "example-provider", "model_id": "provider-model-id", "priority": 1, "max_retry": 1, "enabled": true }
  ],
  "sticky": true,
  "sticky_scope": "agent",
  "failover_strategy": "sequential",
  "max_attempts": 3
}
```

Clients call MFP with `"model": "smart"`; MFP forwards to the chosen backend with `"model": "provider-model-id"`.

## API behavior

### Supported proxy requests

MFP proxies `POST /v1/*`. Examples:

- `/v1/chat/completions`
- `/v1/responses`
- `/v1/completions`
- `/v1/embeddings`
- `/v1/audio/transcriptions`
- `/v1/audio/speech`
- `/v1/images/generations`
- `/v1/images/edits`
- `/v1/moderations`
- `/v1/rerank`
- `/v1/messages`
- provider-specific future `/v1/*` endpoints

Whether an endpoint works depends on the selected backend provider/model. MFP forwards; the backend implements.

### Headers and credentials

- MFP strips hop-by-hop headers before forwarding.
- Client `Authorization` is dropped by default.
- Provider credentials are supplied from `api_key` or `credential_env`.
- `trust_authorization_header` exists for advanced/manual deployments but should normally remain `false`.
- `headers_template` can add fixed upstream headers.

## Admin console

The admin console runs on `admin_listen_addr` and supports:

- Login/logout.
- Provider management.
- Provider model discovery from `/models`.
- Frontend virtual model management.
- Error rules and default action.
- Platform settings.
- Model health and recovery.
- Recent request/attempt logs.
- Sticky route inspection.
- Config export/reload.

## Security notes

- Change the default admin password before exposing the admin console.
- Do not commit real API keys in config files.
- Prefer `credential_env` over `api_key` for production secrets.
- Keep `trust_authorization_header` disabled unless you explicitly want client authorization forwarded upstream.
- Put MFP behind TLS and network access controls if it is not purely local.
- Treat `configs/config.json` as environment-specific if it contains credentials.

## Development

Common commands:

```bash
go run ./cmd/mfp
go run ./cmd/mock-provider
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
docker compose build
```

Project layout:

```text
cmd/                 executables
  mfp/               main MFP service
  mock-provider/     local mock provider
internal/            private Go packages
  app/               application startup
  auth/              admin auth/session helpers
  config/            config loading, defaults, validation
  core/              shared data types
  orchestrator/      routing plan builder
  provider/          upstream provider adapter
  rules/             error normalization and rules
  server/            HTTP API, admin UI, proxy handler
  state/             health, logs, sticky state
configs/             example and demo configs
docs/                user manuals and additional docs
```

## License

No license file is currently included. Add one before distributing this project publicly.
