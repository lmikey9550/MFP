# MFP — 模型故障转移代理

MFP 是一个轻量级 Go 服务，对外提供 OpenAI 兼容风格的代理 API，并把每个前端虚拟模型路由到一个或多个后端供应商模型。它适合本地团队、代码 Agent、模型密集型应用，在不引入大型网关系统的情况下获得简单的故障转移、粘性路由、健康状态和管理台能力。

English: [`README.md`](README.md)  
用户手册：[`docs/user-manual.zh-CN.md`](docs/user-manual.zh-CN.md) / [`docs/user-manual.en.md`](docs/user-manual.en.md)

## MFP 做什么

- 接收 `POST /v1/*` 客户端请求。
- 从请求中的 `model` 字段读取前端虚拟模型 ID。
- 根据虚拟模型配置选择一个后端候选模型。
- 只把顶层 `model` 值替换成被选中的后端模型 ID。
- 将原始请求路径、请求体结构和非 hop-by-hop 请求头转发给后端供应商。
- 当错误规则判定某次上游调用需要重试或故障转移时，自动切换到下一个候选模型。

MFP 刻意保持简单：后端供应商负责决定它支持哪些 `/v1/*` endpoint。MFP 不维护复杂的模型能力矩阵。

## 核心功能

- **透明 OpenAI 风格转发**：代理 `POST /v1/*`，包括 chat、responses、embeddings、audio、image、rerank 以及未来供应商自定义 endpoint。
- **JSON 与 multipart 支持**：支持替换 JSON 请求体和 multipart 表单中的 `model` 字段。
- **虚拟模型**：客户端使用稳定的前端模型名，例如 `smart`，后端可配置多个供应商候选模型。
- **故障转移与重试规则**：根据上游错误执行 retry、failover 或 reject。
- **粘性路由**：可按 agent/session/global 作用域优先沿用上次成功的后端模型。
- **基础拥塞分流**：当首选候选模型并发过高时可切换到其他候选。
- **健康与请求日志**：记录模型健康、最近请求、尝试链路、故障转移次数、延迟和冷却状态。
- **管理台**：浏览器 UI 管理供应商、前端模型、规则、设置、健康、日志和测试。
- **Mock provider**：内置本地 mock OpenAI 兼容供应商，方便 demo 和 smoke test。
- **无需数据库**：JSON 配置加文件化运行时状态。

## 使用 Docker Compose 快速开始

```bash
docker compose up --build
```

然后打开：

- 代理 API：`http://127.0.0.1:18320`
- 管理台：`http://127.0.0.1:18321`

默认 demo 管理员账号：

- 用户名：`admin`
- 密码：`change-me`

Demo 会启动 MFP 和两个 mock provider。默认前端模型是 `smart`。

### Smoke test

```bash
curl -s http://127.0.0.1:18320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

响应里应包含实际后端模型，例如：

```json
{
  "model": "provider-model-a",
  "provider": "mock-primary"
}
```

测试故障转移：

```bash
curl -s http://127.0.0.1:18320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "please [failover]"}]
  }'
```

响应应来自下一个可用后端候选模型。

## 不使用 Docker 本地运行

启动 mock provider：

```bash
go run ./cmd/mock-provider
```

另开一个终端启动 MFP：

```bash
MFP_CONFIG=configs/dev.json go run ./cmd/mfp
```

运行测试：

```bash
go test ./...
go vet ./...
```

构建二进制：

```bash
go build -o build/mfp ./cmd/mfp
go build -o build/mock-provider ./cmd/mock-provider
```

## 配置概览

MFP 从 JSON 配置文件读取配置：

1. 优先读取环境变量 `MFP_CONFIG` 指定的路径。
2. 否则使用应用默认路径。
3. 示例配置位于 `configs/`。

重要顶层字段：

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

Provider 描述一个后端 API 服务：

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

`base_url` 可以包含 `/v1`。MFP 转发时会避免产生重复的 `/v1/v1/...`。

### 虚拟模型

虚拟模型是客户端请求中传入的 `model` 值：

```json
{
  "id": "smart",
  "display_name": "智能模型",
  "candidates": [
    { "provider_id": "example-provider", "model_id": "provider-model-id", "priority": 1, "max_retry": 1, "enabled": true }
  ],
  "sticky": true,
  "sticky_scope": "agent",
  "failover_strategy": "sequential",
  "max_attempts": 3
}
```

客户端调用 MFP 时传 `"model": "smart"`；MFP 转发给后端时改为 `"model": "provider-model-id"`。

## API 行为

### 支持的代理请求

MFP 代理 `POST /v1/*`。例如：

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
- 供应商未来新增的 `/v1/*` endpoint

某个 endpoint 是否真正可用取决于被选中的后端供应商和模型。MFP 负责转发，后端负责实现。

### 请求头与凭据

- MFP 转发前会移除 hop-by-hop 请求头。
- 默认丢弃客户端传入的 `Authorization`。
- 后端 provider 凭据来自 `api_key` 或 `credential_env`。
- `trust_authorization_header` 仅用于高级/手动部署，通常应保持 `false`。
- `headers_template` 可添加固定上游请求头。

## 管理台

管理台运行在 `admin_listen_addr`，支持：

- 登录/退出。
- 管理 Provider。
- 从 `/models` 发现上游模型列表。
- 管理前端虚拟模型。
- 管理错误规则和默认动作。
- 管理平台设置。
- 查看模型健康并手动恢复。
- 查看最近请求和尝试链路。
- 查看粘性路由。
- 导出/重载配置。

## 安全建议

- 对外暴露管理台前必须修改默认管理员密码。
- 不要把真实 API Key 提交到配置文件。
- 生产环境优先使用 `credential_env`，不要使用明文 `api_key`。
- 除非明确需要，否则不要启用 `trust_authorization_header`。
- 如果不是纯本地使用，请给 MFP 加 TLS 和网络访问控制。
- 如果 `configs/config.json` 包含凭据，应视为环境本地文件管理。

## 开发

常用命令：

```bash
go run ./cmd/mfp
go run ./cmd/mock-provider
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
docker compose build
```

项目结构：

```text
cmd/                 可执行入口
  mfp/               MFP 主服务
  mock-provider/     本地 mock provider
internal/            私有 Go 包
  app/               应用启动
  auth/              管理员认证和会话
  config/            配置加载、默认值、校验
  core/              共享数据类型
  orchestrator/      路由计划构建
  provider/          上游供应商适配器
  rules/             错误归一化和规则引擎
  server/            HTTP API、管理台、代理 handler
  state/             健康、日志、粘性状态
configs/             示例和 demo 配置
docs/                用户手册和其他文档
```

## License

当前项目尚未包含 license 文件。公开分发前请补充 license。
