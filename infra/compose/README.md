# Docker Compose 编排

本目录配合根目录的两份编排使用：默认 `compose.yaml` 是主编排，只运行一个长期应用服务 `app`；`compose.multi.yaml` 是额外的多应用服务编排。两者都由 Compose 创建 PostgreSQL、Redis 和 MinIO，但对外只发布一个 `8080` 入口。

## 前置条件

1. 准备 Docker Engine 和 Docker Compose v2。
2. 准备应用镜像。默认镜像为 `ghcr.io/ifty-r/game-night:latest`，也可以在 `.env` 中改为其他 GHCR 标签。
3. 确认 `.env` 中的 `GAME_NIGHT_RUNTIME_UID/GID` 与应用镜像内的非 root 运行用户一致，默认值为 `10001:10001`。
4. 准备以下只读 keyring 文件和 bootstrap 文件：

```text
admin-bootstrap.txt
admin-challenge.json
admin-session.json
audit.json
device.json
pii.json
rate-limit.json
result-envelope.json
totp.json
user-challenge.json
```

默认示例使用 `./.tmp/game-night-dev-runtime/secrets`。这些文件不能提交到 Git；生产环境应由 secret manager 或受限文件挂载提供。

## 单服务模式

单服务模式只有一个应用服务 `app`。镜像的 `serve-all` 命令负责在同一容器内启动 edge、api、realtime 和 worker；应用内部使用 `8080`、`8081`、`8090`、`8091`，只有 `8080` 发布到宿主机。

```powershell
Copy-Item infra/compose/.env.example infra/compose/.env
# 编辑 infra/compose/.env，替换所有 change-me 值

docker compose --env-file infra/compose/.env config
docker compose --env-file infra/compose/.env up -d postgres redis minio secrets-init minio-init postgres-init

# migration 是显式一次性步骤，不由默认 app 启动隐式执行
docker compose --env-file infra/compose/.env --profile migration run --rm migrate

docker compose --env-file infra/compose/.env up -d app
```

访问入口为 `http://127.0.0.1:8080`。如果外部 Nginx 负责 TLS 和域名，保持 `GAME_NIGHT_HTTP_BIND_ADDRESS=127.0.0.1`，让 Nginx 只反代 `127.0.0.1:8080`。

默认示例用于本地开发或预发布环境，内部 PostgreSQL、Redis 和 MinIO 使用明文私有网络连接。生产环境必须先配置 PostgreSQL TLS、Redis TLS 和 HTTPS 对象存储 endpoint，再将 `GAME_NIGHT_ENVIRONMENT` 改为 `production`；应用会拒绝生产环境的明文依赖连接。

## 服务边界

| 服务 | 镜像 | 宿主机端口 | 作用 |
| --- | --- | --- | --- |
| `app` | Game Night 应用镜像 | `8080` | 默认模式，同一容器内运行 edge、API、realtime 和 worker |
| `edge` | Game Night 应用镜像 | `8080` | 额外多服务模式的静态文件、API 和 WebSocket 入口 |
| `api` | Game Night 应用镜像 | 无 | ConnectRPC API |
| `realtime` | Game Night 应用镜像 | 无 | WebSocket 和 owner RPC |
| `worker` | Game Night 应用镜像 | 无 | 异步任务和 checkpoint |
| `postgres` | `postgres:17-alpine` | 无 | 权威数据库 |
| `redis` | `redis:8-alpine` | 无 | 租约、限流和非权威协调 |
| `minio` | MinIO server | 无 | 本地 S3 兼容 checkpoint 存储 |

单服务模式牺牲进程级重启和容器级权限隔离，换取只有一个应用服务配置；`serve-all` 必须为 API、realtime 和 worker 分别设置各自的数据库 DSN。多服务模式则由 Compose 分别启动 `api`、`realtime`、`worker` 和 `edge`，保留独立重启边界。

## 多服务模式

`compose.multi.yaml` 是额外的多服务编排。完成依赖和 migration 后，分别启动四个应用服务：

```powershell
docker compose -f compose.multi.yaml --env-file infra/compose/.env config
docker compose -f compose.multi.yaml --env-file infra/compose/.env up -d postgres redis minio secrets-init minio-init postgres-init
docker compose -f compose.multi.yaml --env-file infra/compose/.env --profile migration run --rm migrate
docker compose -f compose.multi.yaml --env-file infra/compose/.env up -d api realtime worker edge
```

## 数据与重置

数据库、Redis、MinIO、API keyring staging 和 worker 最小 keyring staging 使用命名 volume。删除所有本地数据时：

```powershell
docker compose --env-file infra/compose/.env down -v
```

这会删除本地数据库和对象存储，不能用于生产恢复。额外多服务模式使用 `docker compose -f compose.multi.yaml --env-file infra/compose/.env down -v`。PostgreSQL 角色初始化只在 `postgres-init` 成功后允许应用和 migration 启动；已存在的数据库会重复执行幂等角色授权。

## 当前状态

两份编排现在都引用已落地的 Dockerfile、`edge` 二进制、`serve-all` 进程主管和 GHCR workflow。Docker CLI 若不可用，仍无法在本机执行 `docker compose config` 或实际容器启动；Go 入口和前端/镜像构建链应在 CI 或具备 Docker Engine 的环境中验证。
