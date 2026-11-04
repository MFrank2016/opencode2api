# 方案 B 实施：decodo 代理池 + 429 出口轮换 — 已完成

实施日期：2026-11-04。提交：`92844e4`（功能）、`e523e30`（稳定性修复）。
部署版本：`v1.4.6-34-ge523e30`。

## 1. 侦察 / 备份 ✅

- 备份目录 `/root/security-remediation-20261104-212132/`：
  `config.json`、`compose.yaml`、`runtime-config.json.before`（volume 内运行时配置）
- pre-change commit：`7094edc1a012c4cc4e76a97568a70558ddf75fd0`

## 2. 网关代码（TDD）✅

- `internal/config/config.go`：`PerformanceConfig.free_limit_rotation`（默认 false）
- `internal/gateway/pool.go`：`MarkFreeLimit()` + `freeLimitUntil`（与 abuse
  `blockedUntil` 分离，耗尽出口不触发全局熔断）；cursor 跳过已冷却出口
- `internal/gateway/upstream.go`：命中 `FreeUsageLimitError` 且开关开时冷却该出口
  并继续下一个出口；全部耗尽才返回 429（保留 Retry-After）
- `internal/gateway/refresh.go`：`maxCapabilityRefreshProxies=3`，避免大池吃光
  30s 目录刷新预算
- `cmd/opencode2api/main.go`：`reloadLoop` 处理 SIGHUP → `manager.Reload()`
- 测试：`free_limit_rotation_test.go`（5）、`refresh_capabilities_test.go`（1）、
  `reload_test.go`（1）。全部先 RED 再 GREEN。

## 3. 构建验证 ✅

- `gofmt -l` clean、`go vet ./...` 0 error、`go test ./...` 全绿、`go build ./...` OK
- Python `unittest` 9 个全绿

## 4. 数据面：代理同步脚本 ✅

- `scripts/sync-decodo-proxies.py`：
  - 拉 `http://127.0.0.1:18082/api/resin-proxies?type=decodo`
  - 去掉 `#decodo` 后缀、校验 scheme/凭据/端口
  - **按端口分组，每端口随机取 1 条**（10 条，覆盖 4 个出口 /24）
  - UTC 日稳定 seed（一天内不换凭据，避免每 10 分钟 SIGHUP 抖动）；
    `--rotate-now` 可强制换
  - 原子写、权限 0644（容器非 root 用户可读）、失败不动原文件
  - `--reload` 在内容变化时发 SIGHUP
- `scripts/test_sync_decodo_proxies.py`：9 个单测

## 5. 配置 / 部署 ✅

- `config.json`（seed）：`proxyfile=/run/proxies/proxies.decodo.txt`、
  `performance.free_limit_rotation=true`、`max_anonymous_exits=16`
- 运行中 volume 内的 config.json 同步更新（通过 `docker cp`，因 WebUI 账号为
  `Frank` 且密码 hash 与宿主机明文不同，未走 WebUI API）
- `compose.yaml`：`./runtime:/run/proxies:ro` 只读 bind mount
- `.gitignore`：忽略 `/runtime/`（含凭据）、`__pycache__/`
- `.env`：`OPENCODE2API_VERSION=v1.4.6-34-ge523e30`
- 镜像 `opencode2api:v1.4.6-34-ge523e30`，容器 healthy

## 6. 定时同步 ✅

- `/etc/systemd/system/opencode2api-decodo-sync.{service,timer}`
- 每 10 分钟；`systemctl enable --now` 已生效（active / enabled）
- 内容未变化时不发 SIGHUP

## 7. 端到端验证 ✅

- **关键结果**：一次请求依次轮换 11 个出口（10 decodo 端口 + 1 Resin），
  前 11 个都是 429，第 12 次拿到 **HTTP 200 `pong`**。
  日志事件 `free_usage_limit_rotate` 连续出现 11 次，证明轮换生效。
- 原行为（首个出口 429 即失败）已被替换
- `/healthz` 200、`ready:true`、42 模型、11 代理全 healthy
- 公网 `opencode.061995.xyz` / `opencodeui.061995.xyz` 均 200
- 主进程以 `opencode` 用户运行（非 root）
- 无残留测试容器；临时文件已清理

## 8. 回滚

```bash
git reset --hard 7094edc1a012c4cc4e76a97568a70558ddf75fd0
# 恢复备份的 config.json / compose.yaml / volume 内 config.json
systemctl disable --now opencode2api-decodo-sync.timer
OPENCODE2API_VERSION=v1.4.6-32-g7094edc docker compose up -d --build
```

## 9. 已知限制 / 后续

- 调研时 4 个 /24 桶已累计约 130 次请求，多数时候仍会全耗尽返回 429；
  轮换的价值是"能自动找到还有额度的桶"，不是无限扩容。
- 单出口约 20 次/天，4 桶合计约 120–160 次/天（UTC 0 点重置）。
- Retry-After 指向 UTC 0 点，冷却最长 24h，符合重置周期。
- 若 decodo 免费段被 opencode.ai 整体标记，需换出口来源。
- `proxies` 里保留 Resin `#pool` 作为最后兜底。
- 本次只提交到本地 `main`（ahead 36），未推送、未开 PR（遵守全局红线）。
