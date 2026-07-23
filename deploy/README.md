# Docker 部署

`deploy` 提供两种互相独立的部署方式。两种方式都从 GHCR 拉取同一个应用镜像，并且只运行一个 `game-night` 应用容器、发布一个 `8080` 端口。

| 文件 | 应用容器 | 依赖服务 |
| --- | --- | --- |
| `docker-compose.yml` | 一个 `game-night` 容器 | Compose 创建 PostgreSQL、Redis、MinIO 和初始化容器 |
| `docker-compose.standalone.yml` | 一个 `game-night` 容器 | 使用外部 PostgreSQL、Redis 和 S3 |

## 准备配置

在仓库根目录执行：

```powershell
Copy-Item deploy/.env.example deploy/.env
```

编辑 `deploy/.env`，替换所有 `change-me` 值。默认镜像为 `ghcr.io/ifty-r/game-night:latest`；生产发布建议把 `GAME_NIGHT_IMAGE` 固定为 workflow 输出的版本标签或 digest。私有 GHCR 包需要先执行 `docker login ghcr.io`。

在 `deploy/secrets` 中准备以下文件：

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

这些文件不能提交到 Git。应用镜像已在 Dockerfile 中使用固定的非 root 用户 `10001:10001`，Compose 不再重复配置 `user:`。standalone 模式直接挂载文件时，宿主机权限必须允许 UID `10001` 读取；完整模式由 `secrets-init` 设置所有权和 `0400` 权限。

## 完整部署

默认 `docker-compose.yml` 创建一个应用容器以及 PostgreSQL、Redis 和 MinIO。数据库角色、对象存储 bucket 和 keyring staging 由一次性初始化容器完成。

```powershell
Set-Location deploy

docker compose config
docker compose pull
docker compose run --rm game-night migrate up
docker compose up -d
docker compose ps
```

完整部署中的依赖连接使用 Compose 私有网络明文通信，因此示例环境保持 `GAME_NIGHT_ENVIRONMENT=development`。生产环境应使用 standalone 编排连接启用 TLS 的托管依赖。

## Standalone 部署

`docker-compose.standalone.yml` 只定义一个 `game-night` 服务。启动前必须配置外部 PostgreSQL、Redis、S3 URL 和对应凭据。

```powershell
Set-Location deploy

docker compose -f docker-compose.standalone.yml config
docker compose -f docker-compose.standalone.yml pull
docker compose -f docker-compose.standalone.yml run --rm --no-deps game-night migrate up
docker compose -f docker-compose.standalone.yml up -d
docker compose -f docker-compose.standalone.yml ps
```

## 对外入口

两种方式都只发布 `${GAME_NIGHT_HTTP_BIND_ADDRESS}:${GAME_NIGHT_HTTP_PUBLISHED_PORT}`，默认是 `127.0.0.1:8080`。外部 Nginx 或其他 TLS 终止层只需反代这个地址，并转发原始 Host、客户端地址以及 WebSocket Upgrade 头。

## 更新与清理

更新应用镜像：

```powershell
docker compose pull game-night
docker compose up -d game-night
```

停止完整部署但保留数据：

```powershell
docker compose down
```

删除完整部署的数据库、Redis、MinIO 和 secret staging volume：

```powershell
docker compose down -v
```

`down -v` 会永久删除 Compose 管理的数据，不能作为生产环境的常规更新命令。
