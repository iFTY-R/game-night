# 1Panel 部署指南

本指南使用 `deploy/docker-compose.yml` 在 1Panel 中运行单个 `game-night` 容器。PostgreSQL 和 Redis 由外部服务提供。

## 拓扑

```text
浏览器 -> 1Panel/OpenResty -> 127.0.0.1:40891 -> edge
                                           -> API / realtime / worker
                                           -> 外部 PostgreSQL / Redis
```

Edge 容器监听 `8080`，宿主机默认只发布 `127.0.0.1:40891`。请求路径以 `/admin` 开头时使用管理端静态资源和管理 API，其余路径使用玩家端。

## 文件

在 1Panel 主机创建目录，例如 `/opt/1panel/apps/game-night`，放入：

```text
docker-compose.yml
.env
```

不需要 `secrets/` 目录。所有密钥只通过 `.env` 中的 `GAME_NIGHT_SECRET` 注入，容器启动时派生临时 keyring；不要把真实 `.env` 提交到 Git。

## 环境变量

从模板开始：

```dotenv
GAME_NIGHT_IMAGE=ghcr.io/ifty-r/game-night:v0.0.9
GAME_NIGHT_EXTERNAL_NETWORK=1panel-network
GAME_NIGHT_HTTP_BIND_ADDRESS=127.0.0.1
GAME_NIGHT_HTTP_PUBLISHED_PORT=40891
GAME_NIGHT_ENVIRONMENT=development
GAME_NIGHT_DATABASE_SCHEMA=game_night
GAME_NIGHT_DATABASE_URL=postgresql://game_night:change-me@postgres.example.internal:5432/game_night?sslmode=disable
GAME_NIGHT_REDIS_URL=redis://:change-me@redis.example.internal:6379/0
GAME_NIGHT_REDIS_KEY_PREFIX=game-night:prod:
GAME_NIGHT_REDIS_TIMEOUT=1s
GAME_NIGHT_SECRET=replace-with-at-least-32-random-bytes
```

`GAME_NIGHT_DATABASE_URL` 是迁移、API、实时服务和 worker 共用的连接。迁移会自动将连接用户用于所有数据库角色设置，不需要额外 role 变量。

`docker-compose.yml` 会在部署时检查远端镜像。不要改回浮动的 `latest`，否则 1Panel 可能复用旧镜像并与当前环境变量格式不兼容。

直接使用 `http://IP:端口` 时保持 `GAME_NIGHT_ENVIRONMENT=development`；启用 HTTPS 后改为 `production`，Cookie 会自动使用 Secure。无需配置 Origin、Host 白名单或代理 CIDR。

## 部署命令

```bash
cd /opt/1panel/apps/game-night
docker compose -f docker-compose.yml config
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
docker compose -f docker-compose.yml ps
```

容器每次启动都会先执行幂等的数据库迁移；迁移失败时不会启动 API、realtime 和 worker，直接查看容器日志即可获得具体数据库错误。

首次管理员初始化使用 `GAME_NIGHT_SECRET` 派生的 bootstrap 密钥。初始化完成后，后续重启仍可复用同一主密钥，不需要维护或删除 secret 文件；更换主密钥会使历史加密数据无法解密，请先完成密钥迁移。

## 反向代理

1Panel 反代目标填写 `http://127.0.0.1:40891`，开启 WebSocket，并保留原始 Host、`X-Forwarded-For`、`X-Forwarded-Proto`。管理端直接访问 `https://你的域名/admin`，玩家端访问域名根路径。

## 更新与停止

```bash
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
```

`docker compose down` 只停止容器并保留 checkpoint；不要在生产环境例行使用 `down -v`。
