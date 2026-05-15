# MFP User Manual

This manual explains how to install, configure, operate, test, and troubleshoot MFP — Model Failover Proxy.

Chinese version: [`user-manual.zh-CN.md`](user-manual.zh-CN.md)

## 1. Concepts

### 1.1 Proxy API

MFP exposes a proxy API on `api_listen_addr`, usually `http://127.0.0.1:18320`.

Clients send normal model API requests to MFP instead of calling the upstream provider directly. For example:

```http
POST /v1/chat/completions
Content-Type: application/json
```

MFP accepts `POST /v1/*` requests.

### 1.2 Admin console

MFP exposes a browser admin console on `admin_listen_addr`, usually `http://127.0.0.1:18321`.

Use it to manage:

- Providers
- Backend model lists
- Frontend virtual models
- Failover rules
- Health state
- Recent requests
- Sticky routing
- Platform settings

### 1.3 Provider

A provider is an upstream API service, such as:

- An OpenAI-compatible endpoint
- A local mock provider
- A third-party model aggregator
- A self-hosted model service that accepts OpenAI-style `/v1/*` requests

Provider fields include `base_url`, credentials, timeout, headers, and model IDs.

### 1.4 Frontend virtual model

A frontend virtual model is the model name your client sends to MFP.

Example client request:

```json
{
  "model": "smart",
  "messages": [{"role": "user", "content": "hello"}]
}
```

MFP looks up the virtual model `smart`, chooses a backend candidate, rewrites `model`, and forwards the request:

```json
{
  "model": "provider-model-a",
  "messages": [{"role": "user", "content": "hello"}]
}
```

### 1.5 Candidate

A candidate is one backend provider/model pair inside a virtual model.

Example:

```json
{
  "provider_id": "example-provider",
  "model_id": "provider-model-id",
  "priority": 1,
  "max_retry": 1,
  "enabled": true
}
```

### 1.6 Failover rule

A failover rule tells MFP what to do when an upstream attempt fails.

Actions:

- `failover`: try the next candidate.
- `retry`: retry the same candidate, if retry budget remains.
- `reject`: return the error immediately.

Health impact:

- `none`: do not mark anything unhealthy.
- `model`: cool down only the attempted backend model.
- `provider`: cool down the provider.
- `credential`: cool down the provider credential group.

## 2. Installation

### 2.1 Requirements

For Docker usage:

- Docker
- Docker Compose

For local development:

- Go 1.26 or compatible Go toolchain

### 2.2 Docker Compose installation

From the project root:

```bash
docker compose up --build
```

Open:

- Proxy API: `http://127.0.0.1:18320`
- Admin console: `http://127.0.0.1:18321`

Default account:

- Username: `admin`
- Password: `change-me`

Stop services:

```bash
docker compose down
```

Rebuild after changes:

```bash
docker compose build
```

### 2.3 Local Go installation

Start mock provider:

```bash
go run ./cmd/mock-provider
```

Start MFP:

```bash
MFP_CONFIG=configs/dev.json go run ./cmd/mfp
```

Build binaries:

```bash
go build -o build/mfp ./cmd/mfp
go build -o build/mock-provider ./cmd/mock-provider
```

Run built binaries:

```bash
MFP_CONFIG=configs/config.json ./build/mfp
MOCK_PORT=4000 ./build/mock-provider
```

## 3. First login and basic setup

1. Open `http://127.0.0.1:18321`.
2. Log in with the configured admin account.
3. Change the default password in Platform settings.
4. Add or edit providers.
5. Fetch model lists from providers if supported.
6. Create or edit frontend virtual models.
7. Test selected virtual models from the admin console.
8. Send real client requests to `http://127.0.0.1:18320`.

## 4. Configuration file

### 4.1 Config path

Set config path with:

```bash
MFP_CONFIG=/path/to/config.json
```

If unset, MFP uses its default config path.

### 4.2 Top-level structure

```json
{
  "api_listen_addr": ":18320",
  "admin_listen_addr": ":18321",
  "data_dir": "data",
  "proxy": {},
  "admin": {},
  "providers": [],
  "virtual_models": [],
  "error_rules": [],
  "default_rule_action": "failover"
}
```

### 4.3 Proxy settings

```json
{
  "proxy": {
    "request_timeout_ms": 120000,
    "max_body_bytes": 67108864,
    "trust_authorization_header": false
  }
}
```

Field reference:

| Field | Meaning |
| --- | --- |
| `request_timeout_ms` | Default timeout used when a provider does not set `timeout_ms`. |
| `max_body_bytes` | Maximum client request body size. Default is 64 MiB. Increase for large audio/image uploads. |
| `trust_authorization_header` | Whether to forward client `Authorization` to upstream. Keep `false` unless you intentionally need it. |

### 4.4 Admin settings

```json
{
  "admin": {
    "session_cookie_name": "mfp_session",
    "session_ttl_minutes": 120,
    "accounts": [
      { "username": "admin", "role": "admin", "password": "change-me" }
    ]
  }
}
```

Passwords may be stored as plain `password` in local/demo config. When changed through the admin console, MFP stores password hashes.

### 4.5 Provider configuration

```json
{
  "id": "example-provider",
  "type": "openai_compatible",
  "base_url": "https://provider.example.com/v1",
  "credential_ref": "example_credential",
  "credential_env": "PROVIDER_API_KEY",
  "api_key": "",
  "headers_template": {},
  "timeout_ms": 120000,
  "enabled": true,
  "models": [
    { "id": "provider-model-id", "label": "Provider Model" }
  ]
}
```

Field reference:

| Field | Meaning |
| --- | --- |
| `id` | Unique provider ID. |
| `type` | Currently `openai_compatible`. |
| `base_url` | Upstream base URL. May include `/v1`. |
| `credential_ref` | Logical credential name for grouping health impact. |
| `credential_env` | Environment variable containing provider API key. |
| `api_key` | Inline API key. Prefer env vars in production. |
| `headers_template` | Fixed headers to add to every upstream request. |
| `timeout_ms` | Provider request timeout. |
| `enabled` | Whether provider can be used. |
| `models` | Backend model IDs available on this provider. |

### 4.6 Virtual model configuration

```json
{
  "id": "smart",
  "display_name": "Smart Model",
  "description": "Default high-quality route",
  "sticky": true,
  "sticky_scope": "agent",
  "sticky_timeout_minutes": 30,
  "failover_strategy": "sequential",
  "max_attempts": 3,
  "congestion": {
    "enabled": true,
    "window_seconds": 10,
    "threshold": 5,
    "strategy": "least_loaded",
    "cooldown_seconds": 30
  },
  "candidates": [
    {
      "provider_id": "example-provider",
      "model_id": "provider-model-id",
      "priority": 1,
      "weight": 100,
      "max_retry": 1,
      "enabled": true
    }
  ]
}
```

Field reference:

| Field | Meaning |
| --- | --- |
| `id` | Frontend model name clients send. |
| `display_name` | Admin UI label. |
| `description` | Admin UI note. |
| `sticky` | Whether to prefer last successful backend candidate for the same scope. |
| `sticky_scope` | `agent`, `session`, `global`, or `off`. |
| `sticky_timeout_minutes` | Sticky record lifetime. |
| `failover_strategy` | `sequential`, `random`, or `least_loaded`. |
| `max_attempts` | Max candidates attempted per request. |
| `congestion` | Optional basic congestion routing settings. |
| `candidates` | Ordered backend candidates. |

### 4.7 Candidate settings

| Field | Meaning |
| --- | --- |
| `provider_id` | Provider to call. |
| `model_id` | Backend model ID sent upstream. |
| `priority` | Lower priority is tried earlier in sequential mode. |
| `weight` | Higher weight is preferred in least-loaded mode tie logic. |
| `max_retry` | Retry count for this candidate. |
| `enabled` | Whether candidate can be used. |

### 4.8 Error rules

Rules can be managed through the admin console. Typical rules include:

- 429 rate limit → failover and cool down credential/provider.
- 5xx upstream error → failover.
- Invalid request → reject.

If no custom rules are configured, MFP applies built-in defaults.

## 5. Admin console guide

### 5.1 Dashboard

The dashboard shows:

- Total requests
- Successful requests
- Failovers
- Unhealthy models
- Recent request logs
- Model health
- Sticky/cooldown state

Use it to confirm whether requests are reaching the expected backend model.

### 5.2 Providers

Use Providers to:

1. Add a provider ID.
2. Set `base_url`.
3. Set API key or credential environment variable.
4. Add fixed headers if needed.
5. Fetch model list from `/models` if upstream supports it.
6. Add selected models to the provider catalog.
7. Test a selected model.

### 5.3 Frontend models

Use Frontend models to:

1. Create a frontend model ID such as `smart`.
2. Add backend candidates.
3. Order candidates by priority.
4. Set max attempts.
5. Enable sticky routing if desired.
6. Test the virtual model.

### 5.4 Rules

Use Rules to configure what happens when upstream calls fail.

For most deployments, start with defaults and add only obvious provider-specific rules.

### 5.5 Settings

Use Settings to:

- Change API/admin listen ports.
- Manage admin accounts.
- Save platform settings.

Some changes require service restart, especially listen address changes.

## 6. Client API usage

### 6.1 Chat completions

```bash
curl -s http://127.0.0.1:18320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

### 6.2 Responses API

```bash
curl -s http://127.0.0.1:18320/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "smart",
    "input": "hello"
  }'
```

### 6.3 Embeddings

```bash
curl -s http://127.0.0.1:18320/v1/embeddings \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "smart",
    "input": "hello"
  }'
```

### 6.4 Multipart audio transcription

```bash
printf 'fake audio content' > /tmp/mfp-audio.txt

curl -s http://127.0.0.1:18320/v1/audio/transcriptions \
  -F model=smart \
  -F file=@/tmp/mfp-audio.txt
```

### 6.5 Unknown future `/v1/*` endpoint

If a provider supports a future endpoint, MFP can forward it as long as it is a `POST /v1/*` request with a `model` field:

```bash
curl -s http://127.0.0.1:18320/v1/future/endpoint \
  -H 'Content-Type: application/json' \
  -d '{"model":"smart","input":"hello"}'
```

MFP forwards the path unchanged. The backend decides whether it is valid.

## 7. Testing failover

The bundled mock provider fails when the prompt contains `[failover]` and the selected backend model matches its configured fail model.

```bash
curl -s http://127.0.0.1:18320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "please [failover]"}]
  }'
```

Expected result:

- First candidate fails.
- MFP records the failure.
- MFP tries the next candidate.
- Response comes from the backup backend model.
- Admin dashboard shows a failover request log.

## 8. Runtime state and files

MFP uses `data_dir` for runtime state such as:

- Request logs
- Health state
- Sticky records
- Audit records

For Docker Compose, runtime data is stored in the `mfp-data` volume.

## 9. Security operations

### 9.1 Admin password

Change the default password immediately in any non-demo environment.

### 9.2 Provider credentials

Preferred production pattern:

```json
{
  "credential_env": "PROVIDER_API_KEY",
  "api_key": ""
}
```

Then set:

```bash
export PROVIDER_API_KEY=...
```

Avoid committing real keys.

### 9.3 Authorization forwarding

By default, MFP does not forward the client `Authorization` header upstream. This prevents accidental leakage of client-facing credentials to provider APIs.

Only enable `trust_authorization_header` if you intentionally want client-provided authorization to reach the upstream provider.

### 9.4 Network exposure

If exposing MFP beyond localhost:

- Put it behind TLS.
- Restrict admin console access.
- Use strong admin passwords.
- Protect config files and runtime state.
- Avoid storing inline API keys.

## 10. Troubleshooting

### 10.1 `model_not_found`

Cause: the client sent a `model` value that does not match any virtual model ID.

Fix:

- Check the request `model` field.
- Confirm the virtual model exists in the admin console.
- If client sends `mfp/smart`, MFP normalizes to `smart`.

### 10.2 `no_available_model`

Cause: no enabled candidates are available.

Fix:

- Ensure candidates are enabled.
- Ensure providers are enabled.
- Check cooldown/health state.
- Reduce restrictive `max_attempts` only if misconfigured.

### 10.3 Upstream 401/403

Cause: provider API key or headers are wrong.

Fix:

- Check `credential_env` exists in the MFP process environment.
- Check inline `api_key` if used.
- Check provider `base_url`.
- Check whether the backend requires custom headers.

### 10.4 Request body too large

Cause: request body exceeds `proxy.max_body_bytes`.

Fix:

- Increase `max_body_bytes` in config.
- Restart/reload config as needed.
- For large audio/image uploads, use a higher limit.

### 10.5 Multipart request fails

Cause: malformed multipart request or missing provider support.

Fix:

- Ensure the client sends `multipart/form-data`.
- Ensure there is a `model` form field.
- Confirm backend provider supports that endpoint.

### 10.6 Duplicate `/v1/v1` upstream path

MFP handles provider `base_url` ending in `/v1` and request path starting with `/v1/`. If you still see path issues, check custom reverse proxies in front of the provider.

### 10.7 Admin session expires

Log in again. Adjust `admin.session_ttl_minutes` if needed.

## 11. Maintenance

### 11.1 Validate before deployment

```bash
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
docker compose build
```

### 11.2 Backup

Back up:

- Config file
- `data_dir`
- Any deployment-specific environment variable setup

### 11.3 Updating provider model lists

Use the admin console's model fetch/sync actions if the provider supports `/models`.

### 11.4 Logs and audit

Use the dashboard and audit logs to answer:

- Which backend model served a request?
- Did failover happen?
- Which candidates were attempted?
- Which admin changed configuration?

## 12. Limitations

- Provider type dispatch is currently focused on `openai_compatible`.
- MFP forwards `/v1/*` requests but does not translate between incompatible protocols.
- Backend model capability validation is intentionally not enforced.
- There is no external database integration.
- Production TLS/access control should be handled by deployment infrastructure.
