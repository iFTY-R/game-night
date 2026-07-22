# 发布与恢复

本文件描述 Game Night 的生产发布边界。所有秘密只通过进程环境变量或受限的 secret manager 注入，不能写入仓库、前端 bundle、日志或 room/replay payload。

## 发布前检查

1. 固定 Go、Node、pnpm 和 Buf 版本，执行 `pnpm install --frozen-lockfile`。
2. 执行 `go test -race ./...`、`go vet ./...`、`buf format --diff --exit-code`、`buf lint`、`pnpm run check`、`pnpm test` 和 `pnpm build`。
3. 检查 `git diff --check`、`git status --short`，确认没有生成文件漂移、临时文件或配置秘密。
4. 确认 PostgreSQL migration 已在 staging 执行过一次 `up -> down -> up`，并核对 `00021` 的 system inbox、`00022` 的 replay ACL 索引和约束。
5. 主题资源必须带内容哈希，manifest 的 `game/version/theme` 组合必须在构建注册表中存在；资源下载失败只能落到内置 fallback theme。

## 配置边界

API 和 realtime 使用独立的配置集合：

- API 读取 `GAME_NIGHT_DATABASE_URL`、`GAME_NIGHT_REDIS_URL`、用户 Origin、可信代理和 identity/profile/admin keyring。
- Realtime 读取 PostgreSQL、Redis、用户 Origin、可信代理、内部 token、instance ID 和 advertised URL，不读取设备、管理员、PII 或审计密钥。
- Redis 只保存租约、限流和非权威 fanout；PostgreSQL 是 room、session、action receipt、outbox/inbox 和 replay ACL 的权威来源。
- DSN、Redis URL、内部 token 和 keyring 路径只允许在部署 secret 中出现。日志只记录配置名、实例 ID 和错误分类，不记录值。

启动前应验证连接和 readiness：

```powershell
$env:GAME_NIGHT_DATABASE_URL = '<secret-managed-postgresql-dsn>'
$env:GAME_NIGHT_REDIS_URL = '<secret-managed-redis-url>'
go run ./apps/migrate
go run ./apps/api
go run ./apps/realtime
```

迁移失败时停止发布，不启动新版本 API/realtime。迁移脚本必须保持幂等；回滚应用版本前先确认旧版本能读取新 schema，不能通过删除 migration 或手工改表恢复。

## 滚动发布

1. 先发布数据库 migration 和兼容的 API。
2. 启动 realtime 新实例，等待 PostgreSQL、Redis 和内部 owner RPC readiness 通过。
3. 将新连接路由到新实例；旧实例进入 draining，停止接受新 WebSocket，发送 draining/reconnect 事件并等待现有 session 排空。
4. 观察 action commit 错误率、ownership epoch 冲突、outbox/inbox lag、Redis lease renew 失败和 WebSocket reconnect p95。
5. 确认所有旧实例无活跃连接后再终止。不能直接杀掉持有 session lease 的实例。

## 故障处理

- **PostgreSQL 不可用：** API 写操作和 realtime action/timer/system commit fail closed；不广播未提交版本。恢复连接后由 timer/outbox/inbox 扫描器继续处理 durable work。
- **Redis 不可用：** 不取得新 lease，不允许形成双主；已持有 lease 的实例按 ownership epoch 保护继续读，写入必须经过 PostgreSQL fence。
- **realtime 实例崩溃：** 新实例取得 lease 后递增 ownership epoch；旧实例的 action、timer、revocation 和 system inbox 操作全部被拒绝。客户端通过 last state/event cursor 重连。
- **revocation inbox 堵塞：** 先确认房间成员权限已在 PostgreSQL 事务中撤销，再检查 inbox retry/backoff；重复投递必须命中相同 source event ID 和 digest，不能重复执行罚酒效果。
- **主题资源损坏：** 保留已提交的 theme manifest 哈希，客户端清除坏资源缓存并使用内置 fallback；不能从未经校验的远程 URL 继续渲染。

## 数据恢复

恢复 PostgreSQL 后按以下顺序执行：

1. 校验 migration 版本、关键唯一约束和 replay ACL 索引。
2. 启动单个 realtime recovery worker，处理 timer、outbox 和 system inbox，确认 lag 清零后再扩容。
3. 检查 active session 的 `ownership_epoch`、room active pointer 和最近 action receipt 是否一致。
4. 通过 room/game API 读取 viewer-scoped projection；禁止直接把 raw event 或 snapshot 返回给客户端。

恢复验证记录至少包含恢复时间、最老 outbox/inbox 时间、ownership epoch、replay ACL 命中结果和主题 fallback 命中次数。任何无法验证的 session 都保持 suspended，不能猜测状态继续收款或罚酒。

## 负载与故障注入入口

压测和演练不得使用生产凭据。固定场景应覆盖 1,000 个在线玩家、热点观战房、WebSocket 断线重连、lease 转移、draining、PostgreSQL/Redis 连接失败和主题对象存储失败；记录 p50/p95/p99、重连成功率、恢复时间和未提交动作数。演练完成后保留原始日志、配置摘要（脱敏）和数据库 migration 版本。

仓库内置的无凭据容量门禁使用真实 realtime Hub 和 ownership manager，默认建立 125 个房间、1,000 名玩家及热点房 500 名观众：

```powershell
pnpm run test:load
```

命令向标准输出写入 schema-versioned JSON；任一 p95 超标、依赖故障未 fail closed、恢复不完整或出现未提交更新时返回非零。CI 同时运行 WebSocket coder 重连/draining 和主题对象存储不可用回退，并将原始 JSON 保留在 `game-quality-<run-id>-<attempt>` artifact 30 天。

| 场景 | CI p95 门禁 | 生产恢复目标 |
| --- | ---: | ---: |
| 普通 fanout / 热点观战房 | 2s | 提交后 2s 内可见 |
| 全量连接重建 | 3s | 可达新实例后 5s 内恢复 |
| Redis 通知丢失 | 2s（20ms 缩放扫描） | 默认周期与投影超时内 20s |
| PostgreSQL 恢复后重新投影 | 3s | readiness 恢复后 30s |
| lease 主动转移 | 1s | 滚动发布 5s；崩溃转移 20s |
| 蓝绿 draining | 2s | 5s 内发出 draining 并关闭旧连接 |

这些 CI 数值用于发现代码和并发退化，不替代 staging 网络压测。staging 必须使用相同人口模型记录实际 p50/p95/p99，并在超出生产恢复目标时阻止发布。
