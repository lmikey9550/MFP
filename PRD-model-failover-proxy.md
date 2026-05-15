# PRD: Model Failover Proxy (MFP) — 模型轮换代理

> 版本: 1.0 | 日期: 2026-05-13 | 作者: ops team

---

## 1. 背景与问题

当前 Agent 通过模型中转站（如 an existing model gateway、OpenRouter、各种 API 代理等）调用多个上游模型，但存在以下痛点：

1. **额度耗尽导致模型不可用** — 不同模型有不同的调用额度限制，隔一段时间就会有模型报错
2. **模型切换需手动改配置** — agent 绑定单一模型，模型不可用时需人工修改 openclaw.json 并重启
3. **缺乏自动恢复** — 模型恢复可用后，也无法自动切回
4. **无拥塞感知** — 多 agent 同时请求同一模型，无法自动分流

## 2. 产品目标

在 Agent 与模型中转站之间增加一个**模型轮换代理层 (Model Failover Proxy, MFP)**，对外暴露虚拟模型，对内管理实际模型列表，实现：

- **自动故障转移** — 模型报错时自动尝试下一个，无需人工干预
- **粘性路由** — 同一任务优先使用上次成功的模型，避免中途跳变
- **拥塞分流** — 检测到某模型请求激增时自动分流
- **用户可配置** — 所有规则提供推荐默认值，同时允许用户自由调整

## 3. 系统架构

```
┌──────────┐     ┌───────────────────┐     ┌─────────────┐     ┌──────────┐
│  Agent   │────▶│  MFP (本产品)      │────▶│  模型中转站  │────▶│ 上游模型  │
│ (an agent client)│     │                   │     │ (已有)      │     │ provider-model-a     │
└──────────┘     │  - 虚拟模型路由     │     └─────────────┘     │ provider-model-c     │
                 │  - 粘性会话         │            │            │ provider-model-d    │
                 │  - 错误分类 & 轮换  │            │            │ ...      │
                 │  - 拥塞检测 & 分流  │     ┌──────┴──────┐     └──────────┘
                 │  - 管理页面         │     │ 也可直连上游  │
                 │  - 规则引擎         │     │ (OpenAI/     │
                 └───────────────────┘     │  Anthropic)  │
                                           └─────────────┘
```

**关键设计决策：**
- MFP 是独立服务，部署在模型中转站前端（Agent 配置的 baseUrl 指向 MFP）
- MFP 转发请求到模型中转站，中转站再转发到上游模型；也支持直连上游 API（如 OpenAI、Anthropic 官方）
- MFP 不替代模型中转站，只负责模型选择和故障转移逻辑
- MFP 可对接任意兼容 OpenAI API 格式的服务（自部署中转站、OpenRouter、官方 API 等）

## 4. 核心概念

### 4.1 虚拟模型 (Virtual Model)

用户在 Agent 配置中使用的模型名，映射到一个实际模型列表：

```yaml
virtual_models:
  - id: "smart"                    # 虚拟模型名，agent 配置中填写 virtual/smart
    display_name: "智能模型"
    description: "高质量推理模型，优先使用 provider-model-a"
    models:                         # 有序候选列表
      - "provider-model-a"
      - "provider-model-b"
      - "provider-model-c"
    sticky: true                    # 粘性开关（默认 true）
    failover_strategy: "sequential" # 轮换策略

  - id: "fast"                     # 快速模型
    display_name: "快速模型"
    description: "低延迟模型，适合简单任务"
    models:
      - "provider-model-c"
      - "provider-model-a"
    sticky: true
    failover_strategy: "sequential"
```

Agent 配置示例：
```json
{
  "agents": {
    "list": [{
      "id": "ops",
      "model": "virtual/smart"    // 使用虚拟模型
    }]
  }
}
```

### 4.2 粘性会话 (Sticky Session)

**核心规则：同一任务优先使用上次成功的模型。**

```
首次请求 → 按模型列表顺序尝试 model[0]
  ├─ model[0] 成功 → 记录 sticky_model = model[0]，后续请求优先用 model[0]
  └─ model[0] 失败 → 尝试 model[1]
      ├─ model[1] 成功 → 记录 sticky_model = model[1]
      └─ model[1] 失败 → 尝试 model[2] ...
```

**粘性作用域**（可配置）：

| 作用域 | 说明 | 适用场景 |
|--------|------|----------|
| `global` | 所有请求共享一个 sticky model | 最简单，适合单 agent |
| `agent` | 同一 agent 共享 sticky model | 多 agent 各自独立 |
| `session` | 同一会话共享 sticky model | 会话内上下文一致性最强 |
| `off` | 每次请求都按列表顺序 | 无状态场景 |

**粘性过期**：可配置超时时间（默认 30min），过期后下次请求重新从列表首位开始。

### 4.3 错误分类与轮换规则

#### 内置错误分类

| 类别 | HTTP 状态码 / 错误特征 | 是否轮换 | 推荐行为 |
|------|----------------------|----------|----------|
| `rate_limit` | 429, `rate_limit_exceeded` | ✅ | 轮换下一个模型 |
| `auth_failed` | 401, 403, `invalid_api_key` | ✅ | 轮换下一个模型 |
| `quota_exhausted` | 402, 429 + `insufficient_quota` | ✅ | 轮换下一个模型 |
| `upstream_error` | 500, 502, 503, `server_error` | ✅ | 轮换下一个模型 |
| `timeout` | 请求超时，连接中断 | ✅ | 轮换下一个模型 |
| `bad_request` | 400, `invalid_request` | ❌ | 直接返回错误给客户端 |
| `context_too_long` | 400 + `context_length_exceeded` | ❌ | 直接返回错误 |
| `content_filter` | 400 + `content_policy_violation` | ⚠️ | 可配置（默认不轮换） |
| `model_not_found` | 404, `model_not_found` | ✅ | 轮换下一个模型 |

#### 用户自定义规则

```yaml
error_rules:
  # 修改内置规则
  - match:
      status_code: 400
      body_contains: "content_policy_violation"
    action: failover          # 覆盖默认的不轮换
    cooldown: 300             # 标记该模型 5 分钟内不重试

  # 添加自定义规则
  - match:
      body_regex: "overloaded"
    action: failover
    cooldown: 60

  # 永不轮换的规则
  - match:
      status_code: 400
      body_contains: "invalid_model"
    action: reject            # 直接拒绝，不轮换
```

### 4.4 拥塞检测与分流

**检测机制：**

```yaml
congestion:
  enabled: true
  # 滑动窗口配置
  window_seconds: 10          # 统计窗口（10秒内）
  threshold: 5                # 同一模型在窗口内超过5个并发请求则触发分流
  # 分流策略
  strategy: "round_robin"     # round_robin | random | least_loaded
  # 分流冷却
  cooldown_seconds: 30        # 分流后30秒内不再对该模型触发分流
```

**工作流程：**
1. 请求到达 → 检查目标模型的当前并发数
2. 如果并发数 > threshold → 按 strategy 选择其他模型
3. 记录分流事件，cooldown 期间不重复触发
4. 并发下降后自动恢复

**拥塞分流的优先级低于故障轮换**：只有模型健康时才做拥塞分流，模型已标记不健康则直接跳过。

### 4.5 健康检测

```yaml
health_check:
  enabled: true
  # 被动检测（基于实际请求结果）
  passive: true
  failure_threshold: 3        # 连续失败3次标记不健康
  success_threshold: 1        # 成功1次恢复健康
  
  # 主动检测（定期探测）
  active: false               # 默认关闭，避免浪费额度
  interval_seconds: 300       # 每5分钟探测一次
  timeout_seconds: 10
  
  # 不健康模型的自动恢复
  auto_recover: true
  recover_interval_seconds: 300  # 不健康模型每5分钟允许重试一次
```

## 5. API 协议

### 5.1 对外接口（Agent 侧）

MFP 完全兼容 OpenAI API 格式，Agent 无需任何修改，只需把 `baseUrl` 指向 MFP：

```
POST /v1/chat/completions
POST /v1/responses
```

请求中的 `model` 字段使用虚拟模型名，MFP 内部替换为实际模型名后转发。

**响应头扩展（调试用）：**

```http
X-MFP-Virtual-Model: smart
X-MFP-Actual-Model: provider-model-a
X-MFP-Failover-Count: 0
X-MFP-Sticky-Hit: true
```

### 5.2 管理接口（Admin API）

```
GET    /admin/v1/virtual-models          # 列出所有虚拟模型
POST   /admin/v1/virtual-models          # 创建虚拟模型
PUT    /admin/v1/virtual-models/:id       # 更新虚拟模型
DELETE /admin/v1/virtual-models/:id       # 删除虚拟模型

GET    /admin/v1/models/health            # 查看所有实际模型健康状态
POST   /admin/v1/models/:id/recover       # 手动恢复模型健康状态

GET    /admin/v1/rules                    # 查看轮换规则
PUT    /admin/v1/rules                    # 更新轮换规则

GET    /admin/v1/stats                    # 请求统计（成功率、轮换次数等）
GET    /admin/v1/stats/live               # 实时请求流（SSE）
```

## 6. 管理页面设计

### 6.1 页面结构

```
┌─────────────────────────────────────────────────┐
│  MFP 管理面板                    [状态灯: 🟢 运行中] │
├─────────┬───────────────────────────────────────┤
│         │                                       │
│ 导航     │  主内容区                               │
│         │                                       │
│ 📡 模型  │  ┌─ 虚拟模型: smart ─────────────────┐  │
│   列表   │  │ 候选模型（拖拽排序）:                 │  │
│         │  │  1. provider-model-a    [🟢 健康] [↑↓] [✕]  │  │
│ ⚙️ 规则  │  │  2. provider-model-b      [🟢 健康] [↑↓] [✕]  │  │
│         │  │  3. provider-model-c        [🟡 拥塞] [↑↓] [✕]  │  │
│ 📊 统计  │  │                    [+ 添加模型]      │  │
│         │  ├─────────────────────────────────────┤  │
│ 🔧 设置  │  │ 粘性会话: [● 开启]  作用域: [Agent ▾] │  │
│         │  │ 粘性超时: [30] 分钟                    │  │
│         │  │ 轮换策略: [顺序 ▾]                     │  │
│         │  │ 拥塞分流: [● 开启]  阈值: [5] 并发     │  │
│         │  └─────────────────────────────────────┘  │
│         │                                       │
│         │  ┌─ 模型健康状态 ──────────────────────┐  │
│         │  │ provider-model-a   🟢 健康  成功率 99.2%     │  │
│         │  │ provider-model-b     🟢 健康  成功率 97.8%     │  │
│         │  │ provider-model-c       🟡 拥塞  成功率 94.1%     │  │
│         │  │ provider-model-d      🔴 故障  连续失败 3次      │  │
│         │  │                    [🔄 手动恢复]      │  │
│         │  └─────────────────────────────────────┘  │
└─────────┴───────────────────────────────────────┘
```

### 6.2 核心交互

1. **虚拟模型管理**
   - 创建/编辑/删除虚拟模型
   - 候选模型列表支持**拖拽排序**（决定优先级）
   - 一键从模型中转站同步可用模型列表

2. **健康状态面板**
   - 实时显示所有模型的健康状态（🟢健康/🟡拥塞/🔴故障）
   - 显示成功率、平均延迟、连续失败次数
   - 支持手动恢复/手动标记故障

3. **规则编辑器**
   - 错误轮换规则的可视化编辑
   - 内置规则高亮显示，自定义规则可区分
   - 提供规则测试功能（输入错误响应，预览匹配结果）

4. **实时统计**
   - 请求量/成功率/轮换次数的时间曲线
   - 各模型的使用占比饼图
   - 实时请求流（SSE 推送）

5. **设置页**
   - 拥塞检测参数
   - 健康检测参数
   - 超时配置
   - 日志级别

## 7. 数据模型

### 7.1 核心实体

```typescript
// 虚拟模型
interface VirtualModel {
  id: string;                    // 唯一标识，如 "smart"
  display_name: string;          // 显示名称
  description?: string;
  models: string[];              // 候选模型列表（有序）
  sticky: boolean;               // 是否启用粘性
  sticky_scope: 'global' | 'agent' | 'session' | 'off';
  sticky_timeout_minutes: number;
  failover_strategy: 'sequential' | 'random' | 'least_loaded';
  congestion: CongestionConfig;
  created_at: string;
  updated_at: string;
}

// 模型健康状态
interface ModelHealth {
  model_id: string;
  status: 'healthy' | 'congested' | 'unhealthy' | 'unknown';
  consecutive_failures: number;
  last_failure_at?: string;
  last_failure_reason?: string;
  last_success_at?: string;
  success_rate_24h: number;      // 24小时成功率
  avg_latency_ms: number;
  active_requests: number;       // 当前并发请求数
  marked_unhealthy_at?: string;
}

// 粘性记录
interface StickyRecord {
  virtual_model: string;
  scope_key: string;             // global | agent:{id} | session:{key}
  actual_model: string;
  last_used_at: string;
}

// 请求日志
interface RequestLog {
  id: string;
  virtual_model: string;
  actual_model: string;
  scope_key: string;
  status: 'success' | 'failover' | 'all_failed';
  failover_count: number;
  models_tried: string[];        // 尝试过的模型列表
  error_type?: string;
  latency_ms: number;
  created_at: string;
}

// 错误轮换规则
interface ErrorRule {
  id: string;
  name: string;
  match: ErrorMatch;
  action: 'failover' | 'reject' | 'retry';
  cooldown_seconds: number;
  is_builtin: boolean;           // 内置规则不可删除，可禁用
  enabled: boolean;
  priority: number;              // 规则优先级，数字越小越先匹配
}

interface ErrorMatch {
  status_code?: number;
  status_code_range?: [number, number]; // 如 [500, 599]
  body_contains?: string;
  body_regex?: string;
  error_code?: string;           // OpenAI 格式的 error.code
}
```

### 7.2 存储方案

| 数据 | 存储方式 | 说明 |
|------|----------|------|
| 虚拟模型配置 | YAML/JSON 文件 | 持久化，支持版本控制 |
| 模型健康状态 | 内存 + 定期持久化 | 快速读写，重启后从日志恢复 |
| 粘性记录 | 内存 | 重启后自动重建（下次请求重新选择） |
| 请求日志 | SQLite | 轻量持久化，支持统计查询 |
| 错误规则 | YAML/JSON 文件 | 与虚拟模型配置同目录 |

## 8. 技术规格

### 8.1 推荐技术栈

| 组件 | 推荐方案 | 备选 |
|------|----------|------|
| 后端语言 | Go | Python (FastAPI) |
| API 框架 | Gin / Chi | FastAPI |
| 管理页面前端 | React + Ant Design | Vue + Element Plus |
| 数据存储 | SQLite (日志) + YAML (配置) | PostgreSQL |
| 进程管理 | systemd | Docker |
| 配置热加载 | 文件 watch + SIGHUP | API 触发 |

**选择 Go 的理由**：
- 代理层核心是 IO 密集的转发，Go 的 goroutine 天然适合
- 单二进制部署，无运行时依赖
- 内存占用极低（< 50MB），适合跑在 201 这种内存紧张的机器上

### 8.2 性能要求

| 指标 | 目标 |
|------|------|
| 请求转发延迟增加 | < 5ms（不含上游响应时间） |
| 内存占用 | < 50MB（1万条日志内） |
| 并发处理 | 100+ 并发请求 |
| 故障转移延迟 | < 2s（含重试） |

### 8.3 部署方案

```yaml
# docker-compose.yml
services:
  mfp:
    image: mfp:latest
    ports:
      - "18320:18320"     # API 代理端口
      - "18321:18321"     # 管理页面端口
    volumes:
      - ./mfp-config:/app/config
      - ./mfp-data:/app/data
    environment:
      - UPSTREAM_BASE_URL=http://provider.example.com  # 模型中转站地址
      - ADMIN_PASSWORD=${MFP_ADMIN_PASSWORD}
      - UPSTREAM_API_KEY=${UPSTREAM_API_KEY}           # 中转站/上游 API Key
    restart: unless-stopped
```

Agent 配置变更：
```json
{
  "models": {
    "providers": {
      "mfp": {
        "api": "openai-completions",
        "apiKey": "${UPSTREAM_API_KEY}",
        "baseUrl": "http://10.147.19.201:18320/v1",
        "models": [
          { "id": "smart", "name": "Smart (Failover)", "contextWindow": 200000, "maxTokens": 8192 }
        ]
      }
    }
  }
}
```

## 9. 开发阶段

### Phase 1 — MVP（核心功能）
- [ ] 请求代理转发（兼容 OpenAI API）
- [ ] 虚拟模型 → 候选模型列表
- [ ] 顺序故障转移
- [ ] 粘性会话（global + agent 作用域）
- [ ] 内置错误分类规则
- [ ] 健康状态被动检测
- [ ] Admin API（CRUD）
- [ ] 简易管理页面（模型列表 + 健康状态）

### Phase 2 — 进阶
- [ ] 拥塞检测与分流
- [ ] 用户自定义错误规则
- [ ] session 作用域粘性
- [ ] 请求统计面板
- [ ] 规则测试功能
- [ ] 配置热加载

### Phase 3 — 优化
- [ ] 主动健康检测
- [ ] 自动恢复策略优化
- [ ] 多模型中转站实例支持
- [ ] Prometheus metrics 导出
- [ ] 实时请求流（SSE）

## 10. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| MFP 自身单点故障 | 所有 agent 不可用 | 1. 进程级自动重启 (systemd/docker) 2. MFP 极简架构降低故障概率 3. 降级模式：MFP 挂了可临时直连模型中转站或上游 API |
| 轮换导致 token 浪费 | 多个模型都失败时白白消耗 | 设置最大重试次数（默认=候选模型数），全部失败立即返回 |
| 粘性导致新模型不被使用 | 一直用旧模型 | 粘性超时 + 管理页面手动切换 |
| 中转站侧的模型名与 MFP 映射不一致 | 转发失败 | 从中转站同步模型列表功能 + 启动时校验 |

## 11. 开放问题

1. **多上游混合路由** — 同一个虚拟模型的候选列表中，是否允许混合不同上游来源（如中转站的 provider-model-a + 官方 OpenAI 的 provider-model-c）？需要不同 baseUrl/apiKey 的模型如何管理？
2. **粘性记录的持久化** — 当前设计为纯内存，MFP 重启后粘性丢失。是否需要持久化？
3. **管理页面认证** — 简单密码 vs OAuth vs 与中转站共享认证？
4. **与模型中转站的合并可能性** — 如果是自部署的中转站（如 an existing model gateway），长期看这些功能是否更适合直接集成到中转站内部？但对于第三方中转站（OpenRouter 等），独立 MFP 仍是唯一选择

---

*文档结束。交给开发 agent 时，建议同时提供模型中转站的 API 文档和 an agent client 的模型配置文档作为参考。*
