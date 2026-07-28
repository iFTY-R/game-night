# 1Panel Standalone 部署指南

本文档说明如何在 1Panel 中使用 `deploy/docker-compose.standalone.yml` 部署 Game Night。该模式只运行一个 `game-night` 应用容器，PostgreSQL、Redis 和 S3 兼容对象存储都由外部服务提供。

## 适用场景

- 已经有可用的 PostgreSQL、Redis 和 S3 兼容对象存储。
- 希望通过 1Panel 管理 Compose 编排、日志、重启和反向代理。
- 部署目标是单实例应用容器，不在 1Panel 编排内创建数据库、缓存或对象存储。

不适用场景：如果希望 1Panel 同时拉起 PostgreSQL、Redis 和 MinIO，请使用 `deploy/docker-compose.yml` 的完整部署模式，而不是 standalone 模式。

## 部署拓扑

```text
浏览器
  |
  | HTTPS / WebSocket
  v
1Panel 反向代理 / OpenResty
  |
  | http://127.0.0.1:40891
  v
game-night 容器 edge 网关
  |
  | 容器内部回环地址
  v
API / realtime / worker / migrate
  |
  +-- 外部 PostgreSQL
  +-- 外部 Redis
  +-- 本地 checkpoint 命名卷
```

`game-night` 容器内部 edge 网关监听 `8080`，宿主机默认发布到 `127.0.0.1:40891`。1Panel 反向代理只需要转发到这个本地端口。

## 前置资源

1. 1Panel 主机已安装 Docker，并能正常使用 1Panel 的容器编排功能。
2. 1Panel 主机能访问外部 PostgreSQL 和 Redis。
3. PostgreSQL 已创建目标数据库，推荐使用 PostgreSQL 17 或兼容版本。
4. Redis 已开启认证；受控私网可使用 `redis://`，跨不可信网络时建议使用 `rediss://`。
5. 如果镜像不是公开包，需要先在 1Panel 主机执行 `docker login ghcr.io`。

## 目录结构

建议在 1Panel 主机准备一个独立目录，例如：

```text
/opt/1panel/apps/game-night/standalone/
├── docker-compose.standalone.yml
├── .env
└── secrets/
    ├── admin-bootstrap.txt
    ├── admin-challenge.json
    ├── admin-cursor.json
    ├── admin-session.json
    ├── audit.json
    ├── device.json
    ├── pii.json
    ├── rate-limit.json
    ├── result-envelope.json
    ├── totp.json
    └── user-challenge.json
```

`secrets/` 目录不得提交到 Git，也不要放在 Web 可访问目录。standalone 编排会把它只读挂载到容器内。

## 准备 Compose 文件

把仓库中的 `deploy/docker-compose.standalone.yml` 上传到部署目录。

如果在 1Panel 中选择“路径创建”或类似入口，选择该目录下的 `docker-compose.standalone.yml`。如果选择“编辑创建”，把文件内容粘贴进去，并确保工作目录中存在 `.env` 和 `secrets/`。

## 准备环境变量

在部署目录创建 `.env`。推荐把仓库中的 `deploy/.env.standalone.example` 上传到部署目录并重命名为 `.env`；不要从 `deploy/.env.example` 复制，那个文件是完整部署的兼容模板。

`.env` 只放你在 1Panel 部署时需要决定的外部参数，例如域名、宿主机发布端口、数据库、Redis 和 secret 目录。Compose 文件里的 `GAME_NIGHT_EDGE_LISTEN_ADDRESS`、`GAME_NIGHT_API_LISTEN_ADDRESS`、`GAME_NIGHT_EDGE_API_UPSTREAM_URL`、`GAME_NIGHT_REALTIME_PUBLIC_LISTEN_ADDRESS` 这类值是容器内部固定布线，不需要也不应该放进 `.env` 手动配置。

下面是 1Panel standalone 推荐模板。生产环境请替换全部占位值。

```dotenv
# 镜像与入口
GAME_NIGHT_IMAGE=ghcr.io/ifty-r/game-night:latest
GAME_NIGHT_HTTP_BIND_ADDRESS=127.0.0.1
GAME_NIGHT_HTTP_PUBLISHED_PORT=40891
GAME_NIGHT_STOP_GRACE_PERIOD=65s

# 基础运行模式
GAME_NIGHT_ENVIRONMENT=production
GAME_NIGHT_DATABASE_SCHEMA=game_night

# 玩家端和管理端访问地址，必须与浏览器实际访问地址完全一致。
GAME_NIGHT_USER_ORIGINS=https://game.example.com
GAME_NIGHT_ADMIN_ORIGINS=https://admin-game.example.com
GAME_NIGHT_EDGE_USER_HOSTS=game.example.com
GAME_NIGHT_EDGE_ADMIN_HOSTS=admin-game.example.com
GAME_NIGHT_COOKIE_SECURE=true

# Redis；受控私网可以使用 redis://，需要链路加密时改为 rediss://。
GAME_NIGHT_REDIS_URL=redis://:change-me-redis-password@redis.example.internal:6379/0
GAME_NIGHT_REDIS_KEY_PREFIX=game-night:prod:
GAME_NIGHT_REDIS_TIMEOUT=1s

# 子进程内部通信 token，至少 32 个随机字符。
GAME_NIGHT_REALTIME_INTERNAL_TOKEN=change-me-random-internal-token-at-least-32-characters

# 密钥目录。相对路径以 compose 工作目录为基准。
GAME_NIGHT_API_SECRETS_DIR=./secrets
GAME_NIGHT_WORKER_SECRETS_DIR=./secrets
# 这是容器内路径，不是宿主机路径；首次管理员初始化后把它改成空值。
GAME_NIGHT_API_BOOTSTRAP_SECRET_FILE=/run/game-night/api-secrets/admin-bootstrap.txt

# PostgreSQL。开发环境可以先复用一个账号；生产环境推荐拆分 runtime、worker、migration 权限。
GAME_NIGHT_API_DATABASE_URL=postgresql://game_night_runtime:change-me@postgres.example.internal:5432/game_night?sslmode=disable
GAME_NIGHT_REALTIME_DATABASE_URL=postgresql://game_night_runtime:change-me@postgres.example.internal:5432/game_night?sslmode=disable
GAME_NIGHT_WORKER_DATABASE_URL=postgresql://game_night_worker:change-me@postgres.example.internal:5432/game_night?sslmode=disable
GAME_NIGHT_MIGRATION_DATABASE_URL=postgresql://game_night_migration:change-me@postgres.example.internal:5432/game_night?sslmode=disable
```

如果暂时不用 HTTPS，可以保留 `GAME_NIGHT_ENVIRONMENT=production`，将 `GAME_NIGHT_COOKIE_SECURE=false`，并把 origin 改成 `http://服务器IP:40891` 或 1Panel 配置的 HTTP 域名。正式对外访问仍建议由 1Panel 反向代理提供 HTTPS。

`GAME_NIGHT_TRUSTED_PROXY_CIDRS` 和 `GAME_NIGHT_EDGE_TRUSTED_PROXY_CIDRS` 默认不用写进 `.env`。standalone 编排已经默认信任 `127.0.0.1/32,::1/128`，适合 1Panel 反向代理转发到本机 `127.0.0.1:40891` 的场景。只有 1Panel/OpenResty 不在本机、或前面还有一层代理时，才取消注释并改成真实代理网段：

```dotenv
# GAME_NIGHT_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
# GAME_NIGHT_EDGE_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
```

## 准备密钥文件

应用需要 10 个互相独立的 keyring 文件和 1 个管理员初始化密钥文件。

| 文件 | 用途 |
| --- | --- |
| `admin-bootstrap.txt` | 首次初始化管理员的一次性密码 |
| `pii.json` | 真实姓名等个人信息加密 |
| `totp.json` | 管理员 TOTP seed 加密 |
| `result-envelope.json` | 一次性结果 envelope 加密 |
| `device.json` | 设备凭据 HMAC |
| `rate-limit.json` | 限流维度伪匿名 HMAC |
| `user-challenge.json` | 用户匿名 challenge 签名 |
| `admin-challenge.json` | 管理员 challenge 签名 |
| `admin-session.json` | 管理员 session 和 CSRF HMAC |
| `admin-cursor.json` | 管理后台列表分页游标 HMAC |
| `audit.json` | 审计事件 Ed25519 签名 |

对称 keyring 文件格式如下，`key` 必须是至少 32 字节随机值的 base64，建议固定使用 32 字节：

```json
{
  "active_version": 1,
  "keys": [
    {
      "version": 1,
      "key": "替换为32字节随机值的base64",
      "not_before": "2026-07-26T00:00:00Z"
    }
  ]
}
```

`audit.json` 使用 Ed25519 公私钥：

```json
{
  "active_version": 1,
  "keys": [
    {
      "version": 1,
      "public_key": "替换为Ed25519公钥base64",
      "private_key": "替换为Ed25519私钥base64",
      "not_before": "2026-07-26T00:00:00Z"
    }
  ]
}
```

Linux 主机上文件权限必须让容器内 UID `10001` 可读，并且 keyring 文件权限应为 `0400`。示例：

```bash
cd /opt/1panel/apps/game-night/standalone
chown -R 10001:10001 secrets
chmod 0500 secrets
chmod 0400 secrets/*.json secrets/admin-bootstrap.txt
```

首次管理员初始化完成后，应在 `.env` 中把 `GAME_NIGHT_API_BOOTSTRAP_SECRET_FILE` 设为空值，删除或移走 `secrets/admin-bootstrap.txt`，然后重启服务。管理员已经激活后仍挂载 bootstrap secret 会导致 readiness 失败。

## 在 1Panel 创建编排

1. 进入 1Panel 的容器编排页面。
2. 新建编排，推荐选择已有路径或本地 compose 文件方式。
3. 编排目录选择 `/opt/1panel/apps/game-night/standalone/`。
4. Compose 文件选择 `docker-compose.standalone.yml`。
5. 环境变量文件使用同目录 `.env`。
6. 保存前先执行配置校验或预览，确认只有一个 `game-night` 服务。

如果 1Panel 页面提供“拉取镜像”按钮，先拉取镜像再启动。私有镜像拉取失败时，回到主机执行 `docker login ghcr.io` 后重试。

## 哪些值不用改

以下变量留在 `docker-compose.standalone.yml` 里即可，不需要在 1Panel 的环境变量页面手动填写，也不要注释掉：

| 变量 | 原因 |
| --- | --- |
| `GAME_NIGHT_EDGE_LISTEN_ADDRESS=:8080` | edge 在容器内固定监听 8080，宿主机端口由 `GAME_NIGHT_HTTP_PUBLISHED_PORT` 控制 |
| `GAME_NIGHT_API_LISTEN_ADDRESS=127.0.0.1:8081` | API 只给容器内 edge 调用，不直接暴露给浏览器 |
| `GAME_NIGHT_REALTIME_PUBLIC_LISTEN_ADDRESS=127.0.0.1:8090` | realtime WebSocket 只给容器内 edge 代理，不直接暴露 |
| `GAME_NIGHT_REALTIME_INTERNAL_LISTEN_ADDRESS=127.0.0.1:8091` | 内部 owner RPC 通道，只允许容器内 API 调用 |
| `GAME_NIGHT_EDGE_API_UPSTREAM_URL=http://127.0.0.1:8081` | edge 到 API 的容器内部上游 |
| `GAME_NIGHT_EDGE_REALTIME_UPSTREAM_URL=http://127.0.0.1:8090` | edge 到 realtime 的容器内部上游 |
| `GAME_NIGHT_EDGE_USER_STATIC_DIRECTORY=/app/web` | 镜像内玩家端静态资源目录 |
| `GAME_NIGHT_EDGE_ADMIN_STATIC_DIRECTORY=/app/admin` | 镜像内管理端静态资源目录 |

1Panel 只需要把域名反代到宿主机的 `127.0.0.1:40891`。不要把上表里的内部端口改成 `40891`，否则容器内进程会互相找错地址。

## 初始化数据库

第一次启动前必须先执行迁移。可以在 1Panel 的编排终端中执行，也可以在主机目录执行：

```bash
cd /opt/1panel/apps/game-night/standalone
docker compose -f docker-compose.standalone.yml run --rm --no-deps game-night migrate up
```

迁移成功后再启动编排：

```bash
docker compose -f docker-compose.standalone.yml up -d
docker compose -f docker-compose.standalone.yml ps
```

如果 PostgreSQL 是全新数据库，`GAME_NIGHT_MIGRATION_DATABASE_URL` 对应账号需要拥有建表、建 schema、建函数和授权所需权限。生产环境建议迁移账号和运行期账号分离。

## 配置 1Panel 反向代理

推荐使用两个域名：

| 入口 | 示例域名 | 上游 |
| --- | --- | --- |
| 玩家端 | `https://game.example.com` | `http://127.0.0.1:40891` |
| 管理端 | `https://admin-game.example.com` | `http://127.0.0.1:40891` |

反向代理必须保留原始 `Host`，并支持 WebSocket Upgrade。对应环境变量应保持一致：

```dotenv
GAME_NIGHT_USER_ORIGINS=https://game.example.com
GAME_NIGHT_ADMIN_ORIGINS=https://admin-game.example.com
GAME_NIGHT_EDGE_USER_HOSTS=game.example.com
GAME_NIGHT_EDGE_ADMIN_HOSTS=admin-game.example.com
GAME_NIGHT_COOKIE_SECURE=true
```

如果暂时只用 IP 和端口直连测试：

```dotenv
GAME_NIGHT_ENVIRONMENT=production
GAME_NIGHT_HTTP_BIND_ADDRESS=0.0.0.0
GAME_NIGHT_HTTP_PUBLISHED_PORT=40891
GAME_NIGHT_USER_ORIGINS=http://服务器IP:40891
GAME_NIGHT_ADMIN_ORIGINS=http://admin.服务器IP:40891
GAME_NIGHT_EDGE_USER_HOSTS=服务器IP:40891
GAME_NIGHT_EDGE_ADMIN_HOSTS=admin.服务器IP:40891
GAME_NIGHT_COOKIE_SECURE=false
```

直接暴露 `0.0.0.0:40891` 只建议用于开发或临时验收。正式环境应改回 `127.0.0.1`，通过 1Panel HTTPS 反向代理访问。

## 启动后检查

在 1Panel 中查看 `game-night` 容器状态和日志。命令行检查：

```bash
docker compose -f docker-compose.standalone.yml ps
docker compose -f docker-compose.standalone.yml logs --tail=200 game-night
docker compose -f docker-compose.standalone.yml exec game-night game-night healthcheck
```

浏览器检查：

- 玩家端访问 `https://game.example.com`。
- 管理端访问 `https://admin-game.example.com`。
- 首次进入管理端后按页面提示使用 `admin-bootstrap.txt` 中的一次性密码完成初始化。
- 初始化完成后把 `.env` 中的 `GAME_NIGHT_API_BOOTSTRAP_SECRET_FILE` 改为空值，移除 `admin-bootstrap.txt` 并重启容器，再确认健康检查通过。

## 更新版本

更新 `.env` 中的 `GAME_NIGHT_IMAGE`，建议固定到版本 tag 或 digest，然后在 1Panel 中重新拉取并重建编排。

命令行方式：

```bash
cd /opt/1panel/apps/game-night/standalone
docker compose -f docker-compose.standalone.yml pull
docker compose -f docker-compose.standalone.yml run --rm --no-deps game-night migrate up
docker compose -f docker-compose.standalone.yml up -d
```

## 回滚

1. 把 `.env` 中 `GAME_NIGHT_IMAGE` 改回上一版 tag 或 digest。
2. 在 1Panel 中重新拉取镜像并重建编排。
3. 如果新版本已执行不可逆数据库迁移，先按发布说明确认是否支持回滚。

不要删除 PostgreSQL、Redis、S3 bucket 或 `secrets/` 目录作为回滚手段。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| 容器启动后马上退出 | 查看日志中缺失的 `GAME_NIGHT_*` 变量，补齐 `.env` 后重建 |
| `invalid keyring` | 检查 keyring JSON 格式、base64、文件权限、是否误用软链接、是否复用了同一密钥 |
| 迁移失败 | 确认 migration DSN 有 DDL 权限，PostgreSQL schema 名与 `.env` 一致 |
| 玩家端能打开但 WebSocket 失败 | 检查 1Panel 反代是否支持 Upgrade，确认上游是 `http://127.0.0.1:40891` |
| 登录或请求被 CORS/Origin 拒绝 | 检查 `GAME_NIGHT_USER_ORIGINS`、`GAME_NIGHT_ADMIN_ORIGINS` 是否与浏览器地址完全一致 |
| 管理端和玩家端串页面 | 检查 `GAME_NIGHT_EDGE_USER_HOSTS`、`GAME_NIGHT_EDGE_ADMIN_HOSTS` 是否分别写入正确域名 |
| HTTPS 下登录态异常 | 确认 `GAME_NIGHT_COOKIE_SECURE=true`，反代保留 Host 和客户端协议相关头 |
| 初始化管理员后 readiness 失败 | 把 `GAME_NIGHT_API_BOOTSTRAP_SECRET_FILE` 设为空值，移除 `admin-bootstrap.txt` 后重启容器 |

## 运行边界

- standalone 编排不会创建或备份 PostgreSQL、Redis、S3 数据。
- `.env`、`secrets/`、对象存储凭据和数据库 DSN 都属于部署 secret，不应提交到仓库。
- `GAME_NIGHT_REALTIME_INTERNAL_TOKEN` 是服务间 token，应每个环境独立生成。
- `GAME_NIGHT_REDIS_KEY_PREFIX` 应每个环境独立，避免测试环境和生产环境共用 Redis key。
- 生产环境应固定镜像版本，不要长期使用 `latest`。

## 参考

- [1Panel 容器编排官方文档](https://1panel.cn/docs/user_manual/containers/compose/)
