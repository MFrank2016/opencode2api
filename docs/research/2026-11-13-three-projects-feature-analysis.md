# 三个 OpenCode 反代项目的功能点拆解与本项目对比

日期：2026-11-13
分析对象（按 star + 近 30 天提交量选取，排除本项目上游 `jasonxu114514/opencode2api`）：

| 项目                       | star | 近30天提交 | 分析基线提交           | 许可                              |
| -------------------------- | ---: | ---------: | ---------------------- | --------------------------------- |
| `TiaraBasori/OpenCode2API` |  434 |         22 | `6baf0f0` (2026-11-07) | MIT                               |
| `6Kmfi6HP/opencode2api`    |  156 |         60 | `20021bd` (2026-11-10) | **All rights reserved（非开源）** |
| `12errh/zen-proxy`         |  136 |         77 | `977771c` (2026-11-10) | MIT                               |

本项目基线：`5abdd38`（`v1.4.7-36-g5abdd38`），GPL-3.0。

证据来源：三个仓库均浅克隆到 `/tmp/analysis/`，逐文件阅读；下文引用格式为 `文件:行号`。

---

## 0. 代码规模基线

| 项目        |                             非测试代码 |              测试代码 | 依赖                                 |
| ----------- | -------------------------------------: | --------------------: | ------------------------------------ |
| 本项目      |                24,383 行 Go（75 文件） | 1,095 行 Go（7 文件） | utls / brotli / x/crypto / wireguard |
| zen-proxy   | 4,282 行 JS（单文件）+ 412 行工具 JSON |    6,278 行 node:test | **零依赖**                           |
| TiaraBasori |          6,323 行 JS（proxy.js 5,689） |         5,241 行 jest | express / axios / `@opencode-ai/sdk` |
| 6Kmfi6HP    |               约 2 万行 Go（161 文件） |         94 个测试文件 | utls / brotli / x/sync               |

包级分布（本项目）：gateway 7,891 / protocol 5,594 / models 2,164 / warp 1,965 / admin 1,796 / config 1,478 / telemetry 1,230 / httpx 1,030 / identity 512 / rotation 371。

---

## F1 协议面与端点

### 三项目做法

- **zen-proxy**：手写四类端点 `zen-proxy.mjs:4101-4119`
  - `/v1/models`、`/v1/chat/completions`、`/v1/responses`、`/v1/messages`
  - 额外实现 `/v1/messages/count_tokens`（`:4119`）
  - Anthropic↔Chat 全手写转换（`anthropicToChatBody` `:808`, `anthropicToolsToChat` `:791`）
- **TiaraBasori**：**双 lane**
  - `/v1/chat/completions`（provider=opencode）走 `zen-direct` 直连（`src/proxy.js:2597-2608`）
  - `/v1/messages`、`/v1/responses` 走本地 `opencode serve`（SDK）
  - Responses 支持 `previous_response_id` 会话续接、30 分钟 TTL（README/`src/proxy.js`）
- **6Kmfi6HP**：Chat/Responses/Anthropic 全支持，另有：
  - **原生 Responses 注册表**：某些模型 chat 通道 500、但 `/responses` 正常，探测成功后记忆（`internal/app/native_responses_registry.go:1-40`）
  - `protocol_rules` 按模型覆盖协议

### 本项目现状

- `internal/gateway/gateway.go:167-172`：Chat / Responses / Anthropic / **SystemOne** / `/healthz`
- 协议翻译在 `internal/protocol/`（5,594 行），比三者都完整
- `previous_response_id` 仅作为身份信号使用（`internal/identity/request.go:69-71`），**没有服务端响应存储/续接**

### 可借鉴

1. **`/v1/messages/count_tokens`**（zen-proxy）：成本极低，Claude Code 会调用。
2. **原生 Responses 自动探测 + 记忆**（6Kmfi）：我们只有 `models.protocols` 静态覆盖，没有“chat 失败后自动回退 responses 并记住”的能力。对 `muse-spark-*` 这类模型有实际价值。
3. **`previous_response_id` 服务端续接**（Tiara）：我们没有，属于功能缺口；但实现成本高（要存会话状态），优先级低。

---

## F2 免费层请求体伪装（prompt / tools）

### 三项目做法

- **zen-proxy**
  - `agent-preamble.txt`（184 行官方 harness preamble，逐字）`zen-proxy.mjs:193-244`
  - `agent-tools.json` 中 6 件可映射工具，**manifest 前置、调用方工具在后、同名丢弃**（`:158-176`）
  - 回复侧 `remapAgentTool`（`:245-270`）把 manifest 工具名映射回调用方
  - **`keepableFor`（`:169-176`）**：只保留“调用方确实提供过”的工具调用，**模型自造的工具名会被丢弃**
- **TiaraBasori**
  - `zen-direct.js:14-45` 注释明确写：session/UA/project/官方 prompt/`clint_`/强制 stream 的组合“mirroring the disguise proven out by the **jasonxu114514/opencode2api** gateway”
  - 13 件官方工具、`clint_` 前缀、`tool_choice`/历史工具调用一起重写
- **6Kmfi6HP**
  - `internal/app/fingerprint_manifest.go:14-80`：embed preamble + manifest，注释写“按 zen-proxy 的逻辑重做”
  - `agent_tool_reply.go` 做回复侧映射
  - 无工具调用方强制 `tool_choice=none`

### 本项目现状

- `internal/gateway/toolshape.go` + `opencode_prompt.go`：
  - 2.x/1.x 两套官方工具集（`opencode_tools.json` / `opencode_tools_v1.json`）
  - 调用方工具改名 `clint_<digest>`（`internal/protocol/tools.go:239-266`）
  - 官方工具别名绑定 `bindOfficialToolAliases`（`toolshape.go:284-297`）：**调用方声明了 `Read` 时，官方 `read` 会映射回 `Read`**
- 差距：`RenameResponseTool` 对“映射不到的名字原样保留”（`internal/protocol/response.go:527`）。也就是说，**调用方没有提供等价工具时，模型调用的官方工具（如 `read`）会原样透传给客户端，不会丢弃**。zen-proxy 会丢弃。

### 可借鉴

1. **回复侧丢弃不可执行调用**（zen-proxy `keepableFor`）：我们有别名绑定但没有“无等价则丢弃”。这是最值得补的一处，防止客户端收到无法执行的 `read`/`grep` 调用。
2. **`tool_choice=none` 强制**（6Kmfi，无工具调用方）：本仓库未发现“调用方无 tools 时强制 none”的逻辑（只找到对已有 `tool_choice` 的重写，`toolshape.go:482,612-672`）；而我们会追加官方工具集，模型有被诱导调用官方工具的可能。可作为可选改进。
3. 官方 prompt 我们已逐字注入，与三者同源，无需改。

---

## F3 TLS / 传输指纹

### 三项目做法

- **zen-proxy**：不做 uTLS，**依赖 Bun 运行时自身的 BoringSSL ClientHello**（README 要求 Bun 1.2+；Node 模式会丢指纹）
- **TiaraBasori**：`zen-direct.js` 用 `fetch()`（Node/undici），**没有 uTLS**，只靠头部伪装
- **6Kmfi6HP**：`internal/app/fp_bun_hello.go` 用 uTLS `HelloCustom` 复刻 Bun/BoringSSL hello（ALPN 仅 `http/1.1`，`X25519MLKEM768` 优先），并有 `fp_bun_transport.go`

### 本项目现状

- `internal/httpx/bun.go` + `bun_dial.go`：uTLS 复刻 Bun 1.4.3 ClientHello（JA4 `t13d1713h1`），每个代理独立 transport
- 与 6Kmfi 同一路线，且我们额外做了 `BunTransport` 的连接池/idle watcher

### 可借鉴

- 指纹本身无需再改（我们与 6Kmfi 都是 uTLS，zen-proxy 靠运行时，Tiara 没有）。
- 唯一可借鉴：6Kmfi 的 **`fpTLSSettings` 允许每条出口单独配置 CA / skip-verify**（`egress_proxy.go:95-120`），适合企业 HTTPS 代理。我们目前没有 per-proxy TLS 信任配置。

---

## F4 身份与会话（session / project / identity pool）

### 三项目做法

- **zen-proxy（最强）**
  - 身份池 `identityPool`，上限 2000，LRU（`zen-proxy.mjs:540-580`）
  - 分桶键 = `客户端IP + UA家族`（UA 版本号归一为 `v`）
  - session 30 分钟过期重铸（`SESSION_MAX_AGE_MS` `:515`）
  - project = `sha1("directory:"+18字节随机)`，每身份独立（`:556-558`）
  - 调用方自带 `x-opencode-session` 时**原样透传**（`:580-598`）
  - `ses_` 生成：`~((ms<<12)+ctr)` 的 12 位 hex + 14 位 base62（`:517-527`）
- **TiaraBasori**：`ses_` 按会话信号稳定；`x-opencode-project` **每请求随机 40-hex**（`zen-direct.js:391-402`）
- **6Kmfi6HP**：全局 session + project；**429 时同时换 sessionID 和 projectID**（`internal/app/opencode.go:1008-1070`，注释：上游按 session+project 组合归因）

### 本项目现状

- `internal/identity/request.go:49-103`：
  - session 从 `x-opencode-session`/`x-session-affinity`/conversation seed 派生，官方 `ses_` 原样保留
  - **project 每请求随机**（`:82-87`，注释：免费层按 project+egress 记账）
  - 没有身份池；匿名游标按 session 哈希选起点（`pool.go:197`）

### 可借鉴

1. **身份池**（zen-proxy）：按 `IP+UA家族` 稳定身份、30 分钟重铸。收益：同一客户端多轮对话不因 project 每请求随机而打散缓存/记账；也避免“同一 IP 短时间内大量不同 project”显得异常。成本：改身份派生 + A/B 验证。**需实测，不能假设收益。**
2. **调用方 session 透传**：我们已对官方 `ses_` 原样保留，基本等价。
3. **session+project 双换代**（6Kmfi）：**本项目已实测过**——20 次 project-ID + fresh session 轮换，0/20 成功（见 2026-11-04 记录）。因此不作为优先项，只保留“最后手段”选项。

---

## F5 免费额度与 429 策略

### 三项目做法

- **zen-proxy**
  - 两类 429 严格区分：`retryableUpstream`（429/5xx 瞬态）vs `exhaustedFreeVerdict`（`FreeUsageLimitError`）（`zen-proxy.mjs:624-668`）
  - 瞬态重试，`Retry-After` **上限 3 秒**（`:1784`）
  - **额度耗尽 → 用 `ZEN_KEY` 以 BYOK 身份重试同一次请求一次**（`:1767-1781`），无 key 才 429
- **TiaraBasori**：把 401/402/429 都当瞬态重试（`src/proxy.js:40-62`），走 SDK/后端；无出口/身份轮换
- **6Kmfi6HP**：三层
  1. key 池 failover
  2. session+project 换代
  3. 出口重选（429/5xx 解除粘性）
  4. 429 退避 `Retry-After` **上限 5 秒**

### 本项目现状

- `internal/gateway/abuse.go:139-182`：`peekFreeUsageLimit` 识别 `FreeUsageLimitError`/`GoUsageLimitError`
- 默认 **fail-fast**（`upstream.go`：命中即停，返回 429 + Retry-After）
- 本地新增 **`free_limit_rotation`**（默认关）：命中后冷却该出口到 Retry-After 并继续下一个出口，不触发全局熔断
- 有全局匿名熔断、`free_tier_block_seconds`（IP abuse 封禁 TTL）
- 三项目都**没有**按出口桶轮换配额的能力（6Kmfi 是换 session，不是换出口桶）

### 可借鉴

1. **BYOK 兜底**（zen-proxy）：额度耗尽时若配置了有余额的 Zen key，用 key 重试一次。可作为 `free_limit_byok_fallback` 选项，与现有 fail-fast / rotation 三者互斥或分级。
2. **瞬态 429 的短退避**（zen-proxy 3s / 6Kmfi 5s）：我们目前对普通 429 是走 cooldown（15s 起），可以考虑对“非额度耗尽”的 429 用更短的退避，减少延迟。
3. 我们的 `free_limit_rotation` 是三者都没有的，保留为主路径。

---

## F6 出口 / 代理池

### 三项目做法

- **zen-proxy**：**没有**出口能力（`proxyKey` 是入站鉴权，不是上游代理，`zen-proxy.mjs:1482`）
- **TiaraBasori**：**没有**出口能力（grep 无 proxy/egress）
- **6Kmfi6HP**
  - SOCKS5 池 + 环境 `HTTP(S)_PROXY`，`internal/app/egress_proxy.go`
  - 支持 **CONNECT 隧道 + TLS-to-proxy + 自定义 CA**（`:95-120`）
  - **sticky / round-robin**；429/5xx/连接失败时 `invalidateUpstreamTarget` 解除粘性（`httpclient.go:553,832`）
  - Tor / WARP 通过 compose 外挂（`deploy/compose/compose.warp.yml`）

### 本项目现状

- `internal/gateway/pool.go`：`direct` / http / https / socks5 / socks5h、`proxyfile`、`#pool` 轮换、健康检查、冷却、`blockedUntil`、`freeLimitUntil`
- **userspace WARP**（`internal/warp/`，1,965 行）——三者都没有
- 每 key 可绑定代理（`nodePool` bindings `pool.go:732-773`）
- `proxyfile` + SIGHUP 热更新（本地新增）

### 可借鉴

1. **环境变量代理支持**（6Kmfi `egress_proxy.go`）：用 `x/net/http/httpproxy` 而非 `ProxyFromEnvironment`（后者进程级缓存，热更新读旧值），并对坏值降级为直连。我们有显式代理，但没有 `HTTP(S)_PROXY` 约定；可加一个可选开关。
2. **per-proxy TLS 信任配置**（custom CA / skip-verify）：企业代理场景。
3. sticky/RR 我们已有（游标亲和 + 冷却），无需改。
4. Tor 模板：我们有 userspace WARP，更强，不需要。

---

## F7 上游 Key 池与路由

### 三项目做法

- **zen-proxy**：单一 `ZEN_KEY`（BYOK 兜底用），无池
- **TiaraBasori**：入站多用户 `API_KEYS`（`name:key`），不是上游 key 池
- **6Kmfi6HP（最强）**：`internal/app/keypool.go`
  - 策略 `round_robin / weighted / sticky`（`:95-98`, `:226-310`）
  - `cooldown_secs`、`blacklist_after`（连续失败进长冷却）
  - `retry_on` 状态码列表、`max_retries`
  - 按 key 的 `/api/key_status`、`/api/key_parse`

### 本项目现状

- `internal/gateway/pool.go` 的 `upstreamNodes`：Zen/Go 双池、失败换 key、冷却、恢复
- `internal/rotation/`（371 行）是**模型组轮换**（alias → model），不是 key 策略
- 无 weight、无 blacklist_after、无 per-key 分组

### 可借鉴

1. **weighted / blacklist_after**：如果有多把 Zen/Go key，按权重分配 + 连续失败拉黑更稳。
2. **`/api/key_status` + `/api/key_parse`（粘贴多行 key 自动解析）**：运维体验好，成本低。

---

## F8 模型目录与元数据

### 三项目做法

- **zen-proxy**：`models.json` 由 GitHub Action 定时同步（当前 12 个免费模型）；`/v1/models` 缓存 30s；每小时自动 sync；**按模型探测分桶** `working/rateLimited/flaky/gated/dead`（`zen-proxy.mjs:1279`）
- **TiaraBasori**：模型由本地 OpenCode 运行时提供
- **6Kmfi6HP**：`model_alias`、`max_tokens_cap_per_model`、`protocol_rules`、原生 responses 注册表

### 本项目现状

- `internal/models/`（2,164 行）：动态发现 + `models.opencode.ai` capabilities + `models.context`/`max_output` 覆盖 + 协议映射 + 磁盘缓存
- 上游 #171 已加被动可用性追踪 + 可选探测
- 无简单 `model_alias`（只有 rotation group alias）

### 可借鉴

1. **per-model 健康分桶**（zen-proxy）：我们的 availability 已有部分，可对齐成 `working/rateLimited/flaky/gated/dead` 五档并在 WebUI 展示。
2. **`model_alias`**（6Kmfi）：把 `deepseek-v4-flash` 直接映射到 `deepseek-v4-flash-free`，比 rotation group 更轻。
3. `max_tokens_cap_per_model` 我们已有等价物（`models.max_output`）。

---

## F9 流式健壮性

### 三项目做法

- **zen-proxy**：手写 SSE 解析；检测 `finish_reason`/`[DONE]`；**上游无 finish 就断流时写错误帧而不是假装成功**（`zen-proxy.mjs:2599-2634`）
- **TiaraBasori**：`EVENT_IDLE_TIMEOUT_MS` / `EVENT_FIRST_DELTA_TIMEOUT_MS`；空流重试
- **6Kmfi6HP（最强）**：`PeekFirstFrame` + **`StreamEmptyRetryMax`** + 首字节看门狗 30s；空流/读错/上游错误帧时换 key 重试（`internal/app/config.go:190-194`，`chat_to_anthropic.go:1173-1195`）；按协议定制 hooks

### 本项目现状

- `internal/gateway/first_event.go`：首事件超时、attempt 超时、非流客户端折叠
- **没有空流重试**：200 后无任何 SSE 数据，我们视为失败返回，不会自动换 key/出口重试

### 可借鉴

1. **空流重试**（6Kmfi/Tiara）：首字节超时或 EOF 且未 commit 时，换 key/出口重试 N 次。这是三者共识，收益明确（上游偶发空流），成本中等。
2. **断流写错误帧**（zen-proxy）：**我们已覆盖**——`internal/protocol/stream.go:148` 会在“上游 SSE 流在终止事件前结束”时报错，不会把半截流当成功。

---

## F10 工具调用与外部工具桥

### 三项目做法

- **TiaraBasori（独有）**：`src/tool-runtime/`（1,118 行）实现 **文本契约 `<function_calls>` 桥**
  - 适用于“后端无法原生携带工具”的场景
  - 820 行 parser、registry/router/validator，`EXTERNAL_TOOL_PREFIX='external__'`
- **zen-proxy / 6Kmfi**：manifest + 回复侧映射 + 丢弃不可执行调用

### 本项目现状

- 原生工具直通（客户端执行），`clint_` 重命名 + 官方别名绑定
- 无文本契约桥（我们不需要，因为我们直连上游、工具能原生携带）

### 可借鉴

- **不需要** Tiara 的文本桥（架构不同）。
- 只借鉴 zen-proxy 的 **回复侧丢弃**（见 F2）。

---

## F11 可观测性

### 三项目做法

- **zen-proxy**：`/health`、`/api/status`、`/api/logs`、`/api/test`、`/api/sync`、`/api/reset`
- **TiaraBasori**：
  - **`/metrics` Prometheus**（`src/proxy.js:4513-4559`，按 key 维度）
  - `/health/details` 结构化诊断（`:4455`）
  - **usage 原子持久化**（`src/usage-persistence.js`：临时文件 + rename，损坏当“无快照”）
  - 面板按 key 作用域隔离
- **6Kmfi6HP**：管理面板、`/api/stats`、`/api/key_status`

### 本项目现状

- `/healthz` 字段丰富（models/keys/proxies/warp/issues）
- WebUI：`/api/monitor`、`/api/logs`（SSE 流）、`/api/model-quotas`、`/api/debug/*`、Token 统计
- **没有 Prometheus `/metrics`**
- **统计不持久化**：`internal/telemetry/metrics.go:281-592` 是进程内原子计数（`total/success/errors/active`），未发现磁盘快照逻辑，重启即丢。

### 可借鉴

1. **`/metrics`（Prometheus）**：高价值、低风险，便于接 Grafana/告警。
2. **usage 原子持久化**（Tiara）：我们确实只在内存，重启丢统计；可借其“临时文件 + rename + 损坏降级”模式（`src/usage-persistence.js:44-60`）。
3. **按 key 作用域隔离**（Tiara）：非管理员只能看自己的 usage/健康；我们目前所有 server key 共享视图。

---

## F12 管理与热更新

### 三项目做法

- **zen-proxy**：`/api/config` 读写 + Web UI；无鉴权（除非设 `proxyKey`）
- **TiaraBasori**：`/panel` 只读面板；配置靠 env/config.json
- **6Kmfi6HP**：管理面板 + `/api/config`、`/api/reload`、CSRF、`admin_password`

### 本项目现状

- 完整 WebUI：配置读写、Playground、诊断、Token 统计、实时日志、ETag 静态资源
- **SIGHUP + `POST /api/config/reload`**，校验后切换 runtime
- CSRF、登录限流、密码哈希、密钥脱敏

### 可借鉴

1. **key 粘贴解析 UI**（6Kmfi `/api/key_parse`）：多行 key 自动识别 `sk-`/`oc_sk-` 并分组。
2. **只读公共诊断开关**（Tiara `PUBLIC_DIAGNOSTICS`）：把 `/health/details` 暴露给非管理员但脱敏。
3. 热更新我们已领先，无需改。

---

## F13 安全

### 三项目做法

- **zen-proxy**：可选 `proxyKey`、`safeEqual` 常量时间比较、`/api/config` 脱敏、入站 `RATE_LIMIT_MAX`/窗口
- **TiaraBasori**：入站多 key（`API_KEYS`），`/health/details` 与 `/metrics` 按 key 作用域
- **6Kmfi6HP**：`admin_password`、CSRF、`/api/*` 鉴权

### 本项目现状

- `server_keys` 鉴权、WebUI 密码 argon2id、CSRF、登录限流、`SecretRedactor`、常量时间比较
- 无入站速率限制（依赖 Caddy）
- 无 per-key usage 隔离

### 可借鉴

1. **per-key usage 隔离**（Tiara）：多租户场景必要。
2. **入站速率限制**（zen-proxy）：如果 Caddy 没配，可在网关内加一层。
3. 其余已对齐。

---

## F14 测试策略

### 三项目做法

- **zen-proxy**：6,278 行 node:test（`test/zen-proxy.test.mjs` 单文件 5,912 行，含大量 wire 级夹具）
- **TiaraBasori**：5,241 行 jest，按功能分文件（anthropic、multiuser、panel、tool-lock、zen-direct、parser）
- **6Kmfi6HP**：94 个 Go 测试文件，覆盖协议/429/限流重置/截断修复

### 本项目现状

- 7 个 Go 测试文件 / 1,095 行（上游曾删除 `_test.go`，本 fork 保留并补了若干）
- 覆盖：匿名轮换、free-limit 轮换、能力刷新上限、SIGHUP、WebUI 资产

### 可借鉴

1. **wire 级金样本测试**（zen-proxy）：把请求/响应体逐字节固定，防止伪装字段回归。
2. **协议矩阵测试**（Tiara/6Kmfi）：3 入站 × 3 上游 × 流/非流。
3. **429 分类测试**（6Kmfi `ratelimit_reset_test.go`）：区分瞬态 429 与额度 429。

---

## F15 部署与运行形态

### 三项目做法

- **zen-proxy**：Node/Bun 单文件 + install 脚本；`ZEN_PROXY_CONFIG` 指定配置
- **TiaraBasori**：Node + `opencode` 二进制；`PUID/PGID` 重映射 + `gosu` 降权；`MANAGE_BACKEND` 托管后端
- **6Kmfi6HP**：Go 单二进制；`FROM scratch`；文档要求 `--user 65532:65532`（非 root 靠启动参数）；compose 提供 standalone/Tor/WARP 三套

### 本项目现状

- Go 单二进制 + 内嵌 WebUI；`read_only` rootfs；entrypoint `su-exec` 降权；命名卷；healthcheck；compose 单套
- **userspace WARP**（无需外挂容器/权限）

### 可借鉴

1. **PUID/PGID 映射**（Tiara）：bind mount 场景下避免权限问题；我们现在用命名卷所以不需要，但加一个可选 env 成本低。
2. **多架构发布 + GHCR**（6Kmfi）：我们目前本地构建；若要分发可加。
3. Tor 模板：我们有 WARP，不需要。

---

## 汇总：三项目“有而本项目没有”的能力

| 能力                                 | 来源        | 价值             | 成本 | 风险/许可                              |
| ------------------------------------ | ----------- | ---------------- | ---- | -------------------------------------- |
| 回复侧丢弃不可执行工具调用           | zen-proxy   | 高               | 低   | MIT，可移植                            |
| 空流重试（首字节超时/EOF 后换 key）  | 6Kmfi/Tiara | 高               | 中   | 6Kmfi 非开源，需自研；Tiara MIT 可参考 |
| Prometheus `/metrics`                | Tiara       | 高               | 低   | MIT                                    |
| BYOK 兜底（额度耗尽用 key 重试一次） | zen-proxy   | 中高             | 低   | MIT                                    |
| 身份池（IP+UA 家族，30min 重铸）     | zen-proxy   | 中               | 中   | MIT，需 A/B                            |
| 原生 Responses 自动探测 + 记忆       | 6Kmfi       | 中               | 中   | 非开源，需自研                         |
| 上游 key 池 weight/blacklist         | 6Kmfi       | 中               | 中   | 非开源，需自研                         |
| usage 原子持久化                     | Tiara       | 中               | 低   | MIT                                    |
| per-key usage 隔离                   | Tiara       | 中               | 中   | MIT                                    |
| `count_tokens` 端点                  | zen-proxy   | 低               | 低   | MIT                                    |
| `model_alias`                        | 6Kmfi       | 低               | 低   | 非开源，需自研                         |
| 入站速率限制                         | zen-proxy   | 低               | 低   | MIT                                    |
| per-proxy TLS 信任（CA/skip-verify） | 6Kmfi       | 低               | 中   | 非开源，需自研                         |
| 环境变量代理（`HTTP(S)_PROXY`）      | 6Kmfi       | 低               | 低   | 非开源，需自研                         |
| 文本契约工具桥 `<function_calls>`    | Tiara       | 低（我们不适用） | 高   | 不建议                                 |

## 汇总：本项目“有而三者都没有”的能力

| 能力                                        | 说明                                          |
| ------------------------------------------- | --------------------------------------------- |
| `free_limit_rotation`                       | 按出口桶冷却 + 轮换（三者都没有）             |
| userspace WARP                              | 无需 TUN/root（三者都没有，6Kmfi 靠外挂容器） |
| `proxyfile` + SIGHUP 热更新                 | 代理列表原子替换 + 无凭据重载                 |
| SystemOne 端点                              | 决策请求专用 lane                             |
| 完整 WebUI（Playground/诊断/实时日志/ETag） | 三者只有简版面板                              |
| 协议面最全                                  | Chat/Responses/Anthropic/SystemOne 全翻译     |

---

## 建议的实施优先级

### P0（先做，收益/风险比最高）

1. **回复侧丢弃不可执行工具调用**（zen-proxy `keepableFor`）
   - 改动点：`internal/protocol` 响应重写 + `gateway` 侧传入“调用方声明的工具集合”
   - 测试：模型只返回 manifest 工具、调用方未声明 → 客户端不应收到该调用
2. **空流重试**（6Kmfi/Tiara 共识）
   - 改动点：`internal/gateway` 流式路径，首字节超时/EOF 且未向客户端写字节时，换 key/出口重试 N 次（可配置，默认 1）
   - 测试：fake upstream 返回 200 + 立即 EOF → 第二次成功
3. **Prometheus `/metrics`**（Tiara）
   - 改动点：`internal/admin` 新增只读端点，暴露请求数/延迟/token/出口健康
   - 无行为风险

### P1（需要 A/B 或设计）

4. **BYOK 兜底**（zen-proxy）：新增 `free_limit_byok_fallback`，与 fail-fast / rotation 分级
5. **身份池**（zen-proxy）：先做离线对比（同一客户端多轮对话，pool vs 随机 project），有正收益再上
6. **原生 Responses 自动探测**（6Kmfi 思路，自研）：`models.protocols` 静态覆盖之外的动态回退
7. **usage 原子持久化 + per-key 隔离**（Tiara）

### P2（锦上添花）

8. `count_tokens`、`model_alias`、key 池 weight/blacklist、入站限流、per-proxy TLS 配置、PUID/PGID

---

## 明确不建议借鉴

1. **6Kmfi6HP 的任何代码**：LICENSE 为 “All rights reserved. No license is granted to copy, modify, distribute…”，GitHub 标 `NOASSERTION`。只能作为思路参考，**不能复制代码**。
2. **session+project 双换代**：本项目已实测 0/20，收益不成立（除非作为最后的兜底开关）。
3. **Tiara 的 `opencode serve` 托管架构**：我们是独立网关，引入 Node + OpenCode 运行时会显著增加部署复杂度和故障面。
4. **zen-proxy 的单文件架构**：Go 项目的包边界是我们的优势，不应为了“像它”而合并。
5. **Tiara 的 `<function_calls>` 文本桥**：仅在后端无法原生携带工具时需要，我们不适用。

---

## 与现有方案 B 的关系

- 方案 B（`free_limit_rotation` + decodo 池）解决的是**出口级配额**问题，三者都没有同类能力，保持为主路径。
- 本次分析找到的 P0 项（工具调用丢弃、空流重试、`/metrics`）与方案 B **正交**，可以独立实施、独立验证。
- P1 的 BYOK 兜底与方案 B 是**互补**：rotation 找不到活出口时，若有余额 key，可再兜一层。

---

## 下一步

建议按 P0 顺序，沿用方案 B 的方式（先写失败测试 → 最小实现 → 本地验证 → 部署）逐项落地；每项单独提交、单独验证，避免一次性大改。需要我先做哪一项，或先针对某一项给出 TDD 级别的详细改动清单？
