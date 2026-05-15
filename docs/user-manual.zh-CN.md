# MFP 用户手册

本文档介绍如何安装、配置、运行、测试和排查 MFP — 模型故障转移代理。

English version: [`user-manual.en.md`](user-manual.en.md)

## 1. 核心概念

### 1.1 代理 API

MFP 在 `api_listen_addr` 暴露代理 API，默认通常是 `http://127.0.0.1:18320`。

客户端不再直接调用上游模型供应商，而是把正常模型 API 请求发给 MFP。例如：

```http
POST /v1/chat/completions
Content-Type: application/json
```

MFP 接收 `POST /v1/*` 请求。

### 1.2 管理台

MFP 在 `admin_listen_addr` 暴露浏览器管理台，默认通常是 `http://127.0.0.1:18321`。

管理台可用于：

- 管理 Provider
- 管理后端模型列表
- 管理前端虚拟模型
- 管理故障转移规则
- 查看健康状态
- 查看最近请求
- 查看粘性路由
- 管理平台设置

### 1.3 Provider

Provider 表示一个上游 API 服务，例如：

- OpenAI-compatible endpoint
- 第三方模型聚合平台
- 自托管且接受 OpenAI 风格 `/v1/*` 请求的模型服务

Provider 配置包含 `base_url`、凭据、超时、请求头和模型 ID 列表。

### 1.4 前端虚拟模型

前端虚拟模型是客户端发送给 MFP 的模型名。

客户端请求示例：

```json
{
  "model": "smart",
  "messages": [{"role": "user", "content": "hello"}]
}
```

MFP 查找虚拟模型 `smart`，选择后端候选模型，替换 `model` 后转发：

```json
{
  "model": "provider-model-id",
  "messages": [{"role": "user", "content": "hello"}]
}
```

### 1.5 候选模型

候选模型是虚拟模型下的一个后端 provider/model 组合。

示例：

```json
{
  "provider_id": "example-provider",
  "model_id": "provider-model-id",
  "priority": 1,
  "max_retry": 1,
  "enabled": true
}
```

### 1.6 故障转移规则

故障转移规则告诉 MFP 上游调用失败时该怎么办。

动作：

- `failover`：尝试下一个候选模型。
- `retry`：在重试预算允许时重试当前候选模型。
- `reject`：立即返回错误。

健康影响范围：

- `none`：不标记健康状态。
- `model`：只冷却当前后端模型。
- `provider`：冷却整个 provider。
- `credential`：冷却 provider 的凭据分组。

## 2. 安装

### 2.1 环境要求

Docker 方式需要：

- Docker
- Docker Compose

本地开发需要：

- Go 1.26 或兼容版本

### 2.2 使用 Docker Compose 安装

在项目根目录执行：

```bash
docker compose up --build
```

打开：

- 代理 API：`http://127.0.0.1:18320`
- 管理台：`http://127.0.0.1:18321`

默认账号：

- 用户名：`admin`
- 密码：`change-me`

停止服务：

```bash
docker compose down
```

变更后重建：

```bash
docker compose build
```

### 2.3 本地 Go 运行

使用生产模板启动 MFP：

```bash
MFP_CONFIG=configs/dev.json go run ./cmd/mfp
```

构建二进制：

```bash
go build -o build/mfp ./cmd/mfp
```

运行二进制：

```bash
MFP_CONFIG=configs/config.json ./build/mfp
```

## 3. 首次登录与基础设置

1. 打开 `http://127.0.0.1:18321`。
2. 使用配置中的管理员账号登录。
3. 在平台设置中修改默认密码。
4. 添加或编辑 Provider。
5. 如果上游支持 `/models`，可拉取模型列表。
6. 创建或编辑前端虚拟模型。
7. 在管理台测试虚拟模型。
8. 让真实客户端请求 `http://127.0.0.1:18320`。

## 4. 配置文件

### 4.1 配置路径

通过环境变量设置配置文件路径：

```bash
MFP_CONFIG=/path/to/config.json
```

如果未设置，MFP 使用默认配置路径。

### 4.2 顶层结构

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

### 4.3 代理设置

```json
{
  "proxy": {
    "request_timeout_ms": 120000,
    "max_body_bytes": 67108864,
    "trust_authorization_header": false
  }
}
```

字段说明：

| 字段 | 含义 |
| --- | --- |
| `request_timeout_ms` | Provider 未配置 `timeout_ms` 时使用的默认超时。 |
| `max_body_bytes` | 客户端请求体最大字节数。默认 64 MiB。音频/图片上传较大时需要调高。 |
| `trust_authorization_header` | 是否把客户端 `Authorization` 转发给上游。除非明确需要，否则保持 `false`。 |

### 4.4 管理员设置

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

本地/demo 配置可使用明文 `password`。通过管理台修改密码后，MFP 会保存密码哈希。

### 4.5 Provider 配置

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

字段说明：

| 字段 | 含义 |
| --- | --- |
| `id` | Provider 唯一 ID。 |
| `type` | 当前为 `openai_compatible`。 |
| `base_url` | 上游基础地址，可包含 `/v1`。 |
| `credential_ref` | 逻辑凭据名，用于健康影响分组。 |
| `credential_env` | 保存 provider API key 的环境变量名。 |
| `api_key` | 内联 API key。生产环境建议使用环境变量。 |
| `headers_template` | 每次上游请求都附加的固定请求头。 |
| `timeout_ms` | Provider 请求超时。 |
| `enabled` | 是否启用该 provider。 |
| `models` | 该 provider 可用的后端模型 ID。 |

### 4.6 虚拟模型配置

```json
{
  "id": "smart",
  "display_name": "智能模型",
  "description": "默认高质量链路",
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

字段说明：

| 字段 | 含义 |
| --- | --- |
| `id` | 客户端发送的前端模型名。 |
| `display_name` | 管理台展示名称。 |
| `description` | 管理台备注。 |
| `sticky` | 是否在同一作用域内优先使用上次成功的后端候选。 |
| `sticky_scope` | `agent`、`session`、`global` 或 `off`。 |
| `sticky_timeout_minutes` | 粘性记录有效期。 |
| `failover_strategy` | `sequential`、`random` 或 `least_loaded`。 |
| `max_attempts` | 单次请求最多尝试几个候选模型。 |
| `congestion` | 可选基础拥塞路由设置。 |
| `candidates` | 后端候选模型列表。 |

### 4.7 候选模型设置

| 字段 | 含义 |
| --- | --- |
| `provider_id` | 要调用的 provider。 |
| `model_id` | 发送给上游的后端模型 ID。 |
| `priority` | sequential 模式下数值越小越早尝试。 |
| `weight` | least_loaded 模式下用于排序倾向。 |
| `max_retry` | 当前候选模型的重试次数。 |
| `enabled` | 是否启用该候选。 |

### 4.8 错误规则

错误规则可通过管理台配置。常见规则：

- 429 限流 → failover，并冷却 credential/provider。
- 5xx 上游错误 → failover。
- invalid request → reject。

如果未配置自定义规则，MFP 会使用内置默认规则。

## 5. 管理台使用指南

### 5.1 Dashboard

Dashboard 显示：

- 总请求数
- 成功请求数
- 故障转移次数
- 不健康模型数量
- 最近请求日志
- 模型健康状态
- 粘性/冷却状态

可用于确认请求是否命中了预期后端模型。

### 5.2 Providers

Provider 页面可用于：

1. 添加 provider ID。
2. 设置 `base_url`。
3. 设置 API key 或凭据环境变量。
4. 按需添加固定 headers。
5. 如果上游支持 `/models`，拉取模型列表。
6. 添加选中的模型到 provider 目录。
7. 测试选中模型。

### 5.3 Frontend models

Frontend models 页面可用于：

1. 创建前端模型 ID，例如 `smart`。
2. 添加后端候选模型。
3. 按优先级排序候选。
4. 设置最大尝试次数。
5. 按需启用粘性路由。
6. 测试虚拟模型。

### 5.4 Rules

Rules 页面用于配置上游调用失败时的行为。

大多数部署可以先使用默认规则，只为明显的供应商特定错误添加规则。

### 5.5 Settings

Settings 页面可用于：

- 修改 API/admin 监听端口。
- 管理管理员账号。
- 保存平台设置。

监听地址变更通常需要重启服务。

## 6. 客户端 API 调用

### 6.1 Chat Completions

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

### 6.4 Multipart 音频转录

```bash
printf 'fake audio content' > /tmp/mfp-audio.txt

curl -s http://127.0.0.1:18320/v1/audio/transcriptions \
  -F model=smart \
  -F file=@/tmp/mfp-audio.txt
```

### 6.5 未知未来 `/v1/*` endpoint

如果供应商支持未来新增 endpoint，只要它是带 `model` 字段的 `POST /v1/*` 请求，MFP 就可以转发：

```bash
curl -s http://127.0.0.1:18320/v1/future/endpoint \
  -H 'Content-Type: application/json' \
  -d '{"model":"smart","input":"hello"}'
```

MFP 会原样转发路径。后端决定该 endpoint 是否有效。

## 7. 测试故障转移

生产环境测试故障转移需要至少配置两个后端候选。请临时禁用、限流或故意让第一个候选失败，然后发送正常请求确认 MFP 会路由到下一个可用候选。

```bash
curl -s http://127.0.0.1:18320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

预期结果：

- 第一个候选因为你的临时测试设置而失败。
- MFP 记录失败。
- MFP 尝试下一个候选。
- 响应来自备选后端模型。
- 管理台 Dashboard 显示一次 failover 请求日志。

## 8. 运行时状态与文件

MFP 使用 `data_dir` 保存运行时状态，例如：

- 请求日志
- 健康状态
- 粘性记录
- 审计记录

Docker Compose 下运行时数据保存在 `mfp-data` volume 中。

## 9. 安全运维

### 9.1 管理员密码

任何非 demo 环境都应立即修改默认密码。

### 9.2 Provider 凭据

生产环境推荐模式：

```json
{
  "credential_env": "PROVIDER_API_KEY",
  "api_key": ""
}
```

然后设置：

```bash
export PROVIDER_API_KEY=...
```

避免提交真实密钥。

### 9.3 Authorization 转发

默认情况下，MFP 不会把客户端 `Authorization` 请求头转发给上游。这可以避免客户端侧凭据被意外泄露给模型供应商。

只有在明确需要客户端提供的授权头到达上游时，才启用 `trust_authorization_header`。

### 9.4 网络暴露

如果 MFP 不只在 localhost 使用：

- 放在 TLS 后面。
- 限制管理台访问。
- 使用强管理员密码。
- 保护配置文件和运行时状态。
- 避免保存内联 API key。

## 10. 故障排查

### 10.1 `model_not_found`

原因：客户端发送的 `model` 没有匹配任何虚拟模型 ID。

处理：

- 检查请求中的 `model` 字段。
- 确认管理台存在该虚拟模型。
- 如果客户端发送 `mfp/smart`，MFP 会规范化为 `smart`。

### 10.2 `no_available_model`

原因：没有可用候选模型。

处理：

- 确认候选模型已启用。
- 确认 provider 已启用。
- 检查冷却/健康状态。
- 检查 `max_attempts` 是否配置过低。

### 10.3 上游 401/403

原因：provider API key 或 headers 错误。

处理：

- 检查 MFP 进程环境中是否存在 `credential_env` 指定的环境变量。
- 如果使用 `api_key`，检查值是否正确。
- 检查 provider `base_url`。
- 检查后端是否需要自定义 headers。

### 10.4 请求体过大

原因：请求体超过 `proxy.max_body_bytes`。

处理：

- 调高配置中的 `max_body_bytes`。
- 按需重载配置或重启服务。
- 大型音频/图片上传应设置更高上限。

### 10.5 Multipart 请求失败

原因：multipart 请求格式不正确或后端不支持。

处理：

- 确认客户端发送 `multipart/form-data`。
- 确认存在 `model` 表单字段。
- 确认后端 provider 支持该 endpoint。

### 10.6 上游路径出现重复 `/v1/v1`

MFP 已处理 provider `base_url` 以 `/v1` 结尾、请求路径也以 `/v1/` 开头的情况。如果仍然出现路径问题，请检查 provider 前面的自定义反向代理。

### 10.7 管理员会话过期

重新登录即可。必要时调整 `admin.session_ttl_minutes`。

## 11. 维护

### 11.1 部署前验证

```bash
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
docker compose build
```

### 11.2 备份

建议备份：

- 配置文件
- `data_dir`
- 部署环境变量配置

### 11.3 更新 provider 模型列表

如果 provider 支持 `/models`，可在管理台使用拉取/同步模型功能。

### 11.4 日志与审计

可通过 Dashboard 和审计日志回答：

- 某个请求由哪个后端模型服务？
- 是否发生故障转移？
- 尝试了哪些候选模型？
- 哪个管理员修改了配置？

## 12. 当前限制

- Provider 类型当前聚焦 `openai_compatible`。
- MFP 转发 `/v1/*` 请求，但不在不兼容协议之间做转换。
- MFP 不强制校验后端模型能力。
- 当前不集成外部数据库。
- 生产 TLS 和访问控制应由部署基础设施负责。
