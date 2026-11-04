# 方案 B 实施计划：decodo 代理池 + 429 出口轮换

目标：把 reg-platform 注册的 decodo 免费 DC 代理接入 opencode2api，并在匿名免费层遇到
`FreeUsageLimitError` 时按出口轮换，而不是首个出口 429 就整体失败。

## 1. 侦察 / 备份

- [ ] 备份 config.json、compose.yaml 到 /root/security-remediation-<时间戳>/
- [ ] 记录 pre-change commit：`git rev-parse HEAD`
- [ ] 记录 git status

## 2. 网关代码（TDD：先写测试）

- [ ] `internal/config/config.go`：`PerformanceConfig` 新增 `free_limit_rotation`（默认 false）
- [ ] 新增 `internal/gateway/pool.go`：`MarkFreeLimit(node, resp)` —— 把该出口冷却到
      `Retry-After`（上限 24h），并置 `blockedUntil` 使 `#pool` 节点也被跳过
- [ ] `doAnonymousUpstream`：429 时轮换而非 fail-fast；不计入全局熔断；
      全部出口耗尽才返回 429（保留 Retry-After）
- [ ] `refreshProtocolCapabilities`：限制尝试的代理数量（避免 30s 预算被吃光导致目录只剩 2 模型）
- [ ] `cmd/opencode2api/main.go`：SIGHUP 触发 `manager.Reload()`（脚本无需凭据即可热更新）
- [ ] 单测：开关开→429 后换出口成功；全部 429→返回 429；开关关→保持原行为；
      Retry-After 冷却生效

## 3. 构建验证

- [ ] gofmt / go vet ./... / go test ./... / go build
- [ ] 本地 mock 上游集成测试（可选，验证真实轮换链路）

## 4. 数据面：代理同步脚本

- [ ] `scripts/sync-decodo-proxies.sh`：拉取 reg-platform `/api/resin-proxies?type=decodo`，
      按端口→/24 去重（4 组），每组随机取 1-2 条，原子写 `proxies.decodo.txt`
- [ ] 校验格式、失败不动现有文件、记录日志

## 5. 配置 / 部署

- [ ] `config.json`：`proxyfile` 指向 proxies.decodo.txt，`performance.free_limit_rotation=true`
- [ ] `compose.yaml`：bind 挂载 proxies.decodo.txt 到 /var/lib/opencode2api/（只读）
- [ ] `.gitignore` 忽略 proxies.decodo.txt（含凭据，禁止入库）
- [ ] 构建镜像 `OPENCODE2API_VERSION=$(git describe --always --dirty)`
- [ ] `docker compose up -d --build`

## 6. 定时同步

- [ ] systemd timer 每 10 分钟跑同步脚本，文件变化时 `docker compose kill -s HUP opencode2api`

## 7. 验证与清理

- [ ] 容器 healthy、/healthz 200、模型目录 45+、代理数 4-8
- [ ] Caddy 路由 200、非 root 运行
- [ ] 清理临时容器 / 文件 / 构建残留
- [ ] 回滚点记录

## 已知限制

- 调研时 4 个 /24 桶已全部耗尽（累计约 130 次请求后），因此无法端到端验证
  「轮换后拿到 200」；只能靠单测 + mock 验证逻辑，真实恢复需等 UTC 00:00 重置。
