# Docker Compose 编排

本目录配合根目录的两份编排使用：默认 `docker-compose.yaml` 是主编排，只定义一个长期应用服务 `app`；`docker-compose.multi.yaml` 是额外的多应用服务编排，才会创建 PostgreSQL、Redis 和 MinIO 等依赖容器。两种模式对外都只发布一个 `8080` 入口。

## 前置条件

1. 准备 Docker Engine 和 Docker Compose v2。
2. 准备应用镜像。默认镜像为 `ghcr.io/ifty-r/game-night:latest`，也可以在 `.env` 中改为其他 GHCR 标签。
3. 确认 `.env` 中的 `GAME_NIGHT_RUNTIME_UID/GID` 与应用镜像内的非 root 运行用户一致，默认值为 `10001:10001`。
4. 单服务模式需要提前准备外部 PostgreSQL、Redis 和 S3 兼容对象存储，并配置 `GAME_NIGHT_API_DATABASE_URL`、`GAME_NIGHT_REALTIME_DATABASE_URL`、`GAME_NIGHT_WORKER_DATABASE_URL`、`GAME_NIGHT_REDIS_URL` 与 S3/AWS 变量。
5. 准备以下只读 keyring 文件和 bootstrap 文件：

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

默认示例使用 `./.tmp/game-night-dev-runtime/secrets`。单服务模式通过 `GAME_NIGHT_API_SECRETS_DIR` 和 `GAME_NIGHT_WORKER_SECRETS_DIR` 直接只读挂载；多服务模式通过 `GAME_NIGHT_SECRETS_DIR` 先 staging 到命名 volume。这些文件不能提交到 Git；生产环境应由 secret manager 或受限文件挂载提供。

## 单服务模式

单服务模式只有一个 Compose 服务和一个长期容器：`app`。它不创建 PostgreSQL、Redis、MinIO、初始化容器或独立 migration 服务；镜像的 `serve-all` 命令负责在同一容器内启动 edge、api、realtime 和 worker。应用内部使用 `8080`、`8081`、`8090`、`8091`，只有 `8080` 发布到宿主机。

```powershell
Copy-Item infra/compose/.env.example infra/compose/.env
# 编辑 infra/compose/.env，替换所有 change-me 值，并指向外部 PostgreSQL、Redis、S3 和 keyring 目录

docker compose --env-file infra/compose/.env config

# migration 是同一个 app 服务的一次性命令，不是第二个 Compose 服务
docker compose --env-file infra/compose/.env run --rm --no-deps app migrate up

docker compose --env-file infra/compose/.env up -d app
```

访问入口为 `http://127.0.0.1:8080`。如果外部 Nginx 负责 TLS 和域名，保持 `GAME_NIGHT_HTTP_BIND_ADDRESS=127.0.0.1`，让 Nginx 只反代 `127.0.0.1:8080`。

默认单服务编排用于已有外部依赖的部署环境。生产环境必须先配置 PostgreSQL TLS、Redis TLS 和 HTTPS 对象存储 endpoint，再将 `GAME_NIGHT_ENVIRONMENT` 改为 `production`；应用会拒绝生产环境的明文依赖连接。

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

单服务模式牺牲进程级重启和容器级权限隔离，换取只有一个应用容器；`serve-all` 必须为 API、realtime 和 worker 分别设置各自的数据库 DSN。多服务模式则由 Compose 分别启动 `api`、`realtime`、`worker` 和 `edge`，并额外创建 PostgreSQL、Redis、MinIO、secret staging 和 init 容器。

## 多服务模式

`docker-compose.multi.yaml` 是额外的多服务编排。完成依赖和 migration 后，分别启动四个应用服务：

```powershell
docker compose -f docker-compose.multi.yaml --env-file infra/compose/.env config
docker compose -f docker-compose.multi.yaml --env-file infra/compose/.env up -d postgres redis minio secrets-init minio-init postgres-init
docker compose -f docker-compose.multi.yaml --env-file infra/compose/.env --profile migration run --rm migrate
docker compose -f docker-compose.multi.yaml --env-file infra/compose/.env up -d api realtime worker edge
```

## 数据与重置

单服务模式没有数据库、Redis、MinIO 或 keyring staging 命名 volume，`down -v` 只清理应用容器相关的 Compose 资源：

```powershell
docker compose --env-file infra/compose/.env down -v
```

额外多服务模式使用 `docker compose -f docker-compose.multi.yaml --env-file infra/compose/.env down -v`。该命令会删除本地数据库、Redis、对象存储和 secret staging volume，不能用于生产恢复。PostgreSQL 角色初始化只在 `postgres-init` 成功后允许应用和 migration 启动；已存在的数据库会重复执行幂等角色授权。

## 当前状态

两份编排现在都引用已落地的 Dockerfile、`edge` 二进制、`serve-all` 进程主管和 GHCR workflow。默认主编排只定义 `app` 一个服务；额外多服务编排才定义依赖服务。Docker CLI 若不可用，仍无法在本机执行 `docker compose config` 或实际容器启动；Go 入口和前端/镜像构建链应在 CI 或具备 Docker Engine 的环境中验证。
