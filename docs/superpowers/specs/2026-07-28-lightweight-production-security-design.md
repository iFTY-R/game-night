# 轻量生产部署校验设计

## 状态

已获产品方向批准，并通过规范审查。

## 背景

Game Night 是最多约 10 名熟人使用的个人小游戏。实际部署由容器外反向代理终止公网 TLS，应用、PostgreSQL 和 Redis 位于受控私网。当前 `production` 同时强制 PostgreSQL TLS、Redis TLS、HTTPS 内部服务地址、HTTPS Origin 和 S3 Object Lock/WORM checkpoint，这套合规级门槛超出了项目规模，也导致现有 sub2api 明文私网依赖无法直接作为生产运行配置。

## 目标

- 将 `GAME_NIGHT_ENVIRONMENT=production` 保留为运行环境标识，不再把它等同于严格基础设施安全等级。
- 允许生产环境连接私网明文 PostgreSQL、Redis、内部 Realtime 服务和管理员 heartbeat 服务。
- 允许生产环境使用持久化本地目录保存 audit checkpoint，不强制 S3/WORM。
- 保留现有认证、授权、CSRF、Origin 用户/管理端隔离、可信代理、密钥文件和数据库权限边界。
- 不增加部署 profile、例外开关或新依赖，保持个人部署配置简单。

## 配置行为

### PostgreSQL 与 Redis

生产环境继续校验 URL 结构、数据库路径、Redis key prefix 和连接池边界，但不再按环境限制传输协议：

- PostgreSQL 接受未设置 `sslmode`、`sslmode=disable` 及现有 TLS 模式。
- Redis 接受 `redis://` 和 `rediss://`。
- TLS 能力保留，由部署者通过 DSN/URL 自主启用。

### 浏览器与内部网络

- 用户和管理端 Origin 在所有环境均接受 `http://` 或 `https://`，但两组 allowlist 仍必须非空、格式有效、无重复且互不重叠。
- `GAME_NIGHT_COOKIE_SECURE` 在 production 继续默认 `true`，但允许显式设置为 `false`。
- API 到 Realtime 的 bootstrap/peer URL 以及 Realtime advertised URL 在 production 可使用 HTTP；URL 结构、端口、peer allowlist 和内部 token 校验保持不变。
- API、Realtime 和 Worker 向管理员控制面发送 heartbeat 时可使用 HTTP；固定 heartbeat 路径、内部 token、实例身份和请求完整性校验保持不变。
- 推荐部署仍由容器外反向代理提供公网 HTTPS，并将浏览器 Origin 配置为真实 HTTPS 域名；代码不再把该建议作为启动硬门禁。

### Checkpoint 存储

- production 接受 `GAME_NIGHT_CHECKPOINT_SINK=local`，目录仍必须是规范化绝对路径，且不能混配 S3 字段。
- 本地 sink 的 `ErrNonProductionSink` 在所有环境均可满足 checkpoint readiness，避免 API 敏感写入和 Worker 启动被 WORM 能力阻断。
- `s3` sink、HTTPS endpoint、retention 和 Object Lock Compliance 检查继续保留为可选的高可靠部署能力；选择 S3 时仍执行现有完整校验与运行时探测。

## 保留的安全边界

本次不放宽以下能力：

- 用户与管理端 Origin 必须隔离，不能共享同一 Origin。
- CSRF、HttpOnly Cookie、管理员 MFA/提权和会话校验保持不变。
- trusted proxy CIDR 仍拒绝空集合、全网段、重复和重叠配置。
- keyring 文件仍要求绝对路径、用途隔离和有效密钥内容。
- PostgreSQL 角色、schema、migration 和最小权限模型保持不变。
- Realtime 内部 token、peer allowlist、实例标识和端口校验保持不变。
- URL 中的非法 scheme、缺失 host、凭据/路径/查询等既有结构限制保持不变。

## 实现范围

预计修改：

- `apps/internal/config/config.go` 及其测试：移除 production 专属 TLS、HTTPS Origin 和 Secure Cookie 拒绝逻辑。
- `apps/api/internal/config/config.go` 及其测试：取消 production 内部 Realtime URL 的 TLS 要求。
- `apps/realtime/internal/config/config.go` 及其测试：取消 production advertised URL 的 TLS 要求。
- `apps/internal/serviceheartbeat/heartbeat.go` 及其测试：取消 production heartbeat URL 的 TLS 要求。
- `apps/internal/checkpointstorage/config.go`、`readiness.go` 及其测试：允许 production local sink 和 readiness。
- `apps/api/main_test.go`、Worker application 测试：锁定 API 和 Worker 对 production local checkpoint 的实际启动行为。
- `deploy/docker-compose.yml`、`deploy/docker-compose.standalone.yml` 及 launcher/部署测试：确保 production 的私网 HTTP wiring 不会被配置门禁拒绝。
- `docs/operations/release.md`、`deploy/README.md`、`deploy/1panel-standalone.md` 和单镜像设计文档：将 TLS 依赖与 WORM S3 从硬要求改为可选增强项。

不修改协议合同、数据库 schema、认证流程、前端页面或第三方依赖。

## 验证

- 为每个放宽点增加或改写回归测试，证明 production 接受明文私网配置。
- 保留并运行无效 URL、Origin 重叠、Cookie 默认值、S3 配置和 readiness 缓存测试。
- 从 `.env.local` 加载现有 PostgreSQL/Redis 配置，仅将环境覆盖为 production，并使用临时绝对 checkpoint 目录验证 API、Realtime 和 Worker 配置加载成功。
- 增加 production 启动级 smoke：API 必须成功构建 checkpoint readiness，Realtime 和 Worker 必须成功完成 application 初始化；测试 heartbeat 接收端使用与单镜像一致的私网 HTTP 地址。
- 运行 `serve-all`/launcher 的 production 配置 smoke，确认共享环境映射后的 API、Realtime 和 Worker 不会因 TLS 或 WORM 策略提前退出。若本机依赖条件不足以维持完整进程，则至少通过 launcher 子进程配置测试覆盖相同环境映射，并明确记录未执行的容器级验证。
- 运行受影响 Go 测试、`go test ./...`、`go test -race` 受影响包、`go vet ./...`、`pnpm run check`、`pnpm test`、`pnpm build`、生成物检查和 `git diff --check`。

## 风险

- 私网被突破时，明文数据库和 Redis 流量可能被读取或篡改；这是本项目规模下接受的部署取舍。
- 本地 checkpoint 不具备防删除和跨主机灾难恢复能力；宿主机或 volume 丢失会同时丢失 checkpoint。
- 显式关闭 Secure Cookie 或配置 HTTP Origin 会让浏览器会话暴露于明文链路；默认值和部署文档仍推荐外部 HTTPS。
