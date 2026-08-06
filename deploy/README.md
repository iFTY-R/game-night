# Docker 部署

`deploy` 只提供一个 Compose 方案：运行一个 `game-night` 容器，连接外部 PostgreSQL 和 Redis。1Panel 操作见 [`1panel.md`](1panel.md)。

## 配置

复制环境模板：

```powershell
Copy-Item deploy/.env.example deploy/.env
```

至少填写 `GAME_NIGHT_DATABASE_URL`、`GAME_NIGHT_REDIS_URL` 和长度不小于 32 字节的 `GAME_NIGHT_SECRET`。launcher 会从主密钥在容器临时目录生成用途隔离的 keyring 和内部通信 token，不需要 `secrets/` 目录或 secret 文件。

镜像默认固定为与本配置兼容的 `v0.0.7`，Compose 每次部署都会检查远端镜像，避免复用旧的 `latest` 缓存。

## 启动

```powershell
Set-Location deploy
docker compose -f docker-compose.yml config
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml run --rm --no-deps game-night migrate up
docker compose -f docker-compose.yml up -d
docker compose -f docker-compose.yml ps
```

默认只发布 `127.0.0.1:40891`。反向代理转发该地址时保留原始 Host、客户端地址和 WebSocket Upgrade 头；`admin.` 子域自动进入管理端，其余合法 Host 进入玩家端。

`GAME_NIGHT_SECRET` 同时用于首次 bootstrap 和运行时密钥派生，初始化完成后保持不变；更换它会使历史加密数据无法解密。

## 更新

```powershell
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml run --rm --no-deps game-night migrate up
docker compose -f docker-compose.yml up -d
```

`docker compose down` 会保留 checkpoint 命名卷；只有确认不再需要数据时才使用 `down -v`。
