# MFP

Model Failover Proxy 的一个纯 Go 实现，提供：

- OpenAI 兼容代理接口：`/v1/chat/completions`、`/v1/responses`
- 虚拟模型到多上游候选模型的编排
- 故障转移、粘性会话、基础拥塞分流、健康状态管理
- 管理接口、本地登录、审计日志、SSE 实时请求流
- 文件化配置与请求/审计日志持久化

## 运行

先设置环境变量：

```powershell
$env:MFP_ADMIN_PASSWORD="change-me"
$env:MFP_ADMIN_SECRET="change-me-too"
$env:UPSTREAM_API_KEY="your-upstream-key"
$env:PROVIDER_API_KEY="your-openai-key"
```

然后启动：

```powershell
go run ./cmd/mfp
```

默认端口：

- 代理 API: `http://127.0.0.1:18320`
- 管理台: `http://127.0.0.1:18321`

默认配置文件位置：

- `configs/example.json`
- `configs/dev.json` 可直接配合本地 mock provider 使用
- 可通过 `MFP_CONFIG` 指向其他 JSON 配置文件

## 已实现模块

- 配置加载与原子保存
- Provider / Virtual Model / Error Rule 数据模型
- OpenAI-compatible 上游调用适配
- 粘性路由、故障轮换、基础冷却与 provider/credential 级影响
- 健康统计、请求日志、审计日志
- Provider 列表、模型目录同步、配置导出、配置重载
- Admin 登录、Cookie 会话、SSE 请求流

## 当前实现说明

- 为了保持零外部依赖，配置格式当前使用 JSON；YAML 和 SQLite 可在后续引入第三方库后替换
- 流式响应支持“未开始输出前的 failover”；一旦上游已经开始成功输出，就不会中途切换
- `session` 粘性默认依赖 `X-MFP-Session-Id`，缺失时自动退化为 `agent` 再退化为 `global`
