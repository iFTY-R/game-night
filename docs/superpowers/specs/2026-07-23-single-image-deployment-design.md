# 单镜像双编排单入口部署设计规范

> 日期：2026-07-23
>
> 状态：单服务与多服务 Compose 编排、Dockerfile、edge 和 GHCR workflow 已落地
>
> 目标：将 Game Night 的应用程序打包为一个可发布到 GHCR 的单镜像，并同时提供单应用容器和多应用容器两份 Docker Compose 编排；两种模式对外都只提供一个应用入口端口。

## 1. 背景与问题

当前仓库的运行职责已经拆分为多个 Go 进程：

- `apps/api`：ConnectRPC API，同时承载用户和管理端 API。
- `apps/realtime`：游戏 WebSocket 和 realtime owner RPC。
- `apps/worker`：异步 durable work、checkpoint 和清理任务。
- `apps/migrate`：数据库 migration 一次性命令。
- `apps/web`：Vue/Vite 前端，构建产物为静态文件。

这种内部拆分是合理的，但如果把每个进程的端口直接暴露给部署者，就会产生以下问题：

- 外部 Nginx 需要维护多个 upstream 和多组域名路由。
- Compose 的 `ports` 暴露多个内部端口，端口职责容易被误配。
- 前端 API 和 WebSocket 需要额外的跨域或构建时地址配置。
- 单镜像的交付边界被内部进程实现细节泄露。

本设计调整的是部署边界，不合并 API、realtime 和 worker 的业务职责。

## 2. 目标与非目标

### 2.1 目标

- 应用相关程序构建为一个镜像。
- 容器对外只监听一个应用端口，默认使用 `:8080`。
- 同时提供两种彼此独立的 Compose 模式：默认 `docker-compose.yaml` 只定义一个 `app` 容器，依赖外部 PostgreSQL、Redis 和 S3；`docker-compose.multi.yaml` 额外提供完整依赖栈，并将 `edge`、`api`、`realtime` 和 `worker` 拆成独立应用服务。
- 前端静态文件、ConnectRPC API 和游戏 WebSocket 使用同源入口。
- API、realtime 和 worker 继续作为独立进程运行，便于保持现有代码边界；多服务模式还保留独立容器边界。
- 同一个镜像支持默认服务模式和一次性 migration 模式。
- Docker Compose 只编排应用及其依赖，不要求外部部署者理解内部端口。
- GitHub Actions 构建并发布 GHCR 镜像，支持不可变 SHA 标签和发布标签。
- 外部 Nginx 可选；使用时只反代一个应用上游。

### 2.2 非目标

- 不把 PostgreSQL、Redis 或 S3/MinIO 作为应用镜像内的进程。
- 不在应用镜像内默认运行 Nginx、TLS 终止或域名证书管理。
- 不把 API、realtime 和 worker 合并成一个 Go 业务程序。
- 不在本阶段解决多节点高可用、独立水平扩缩容或 Kubernetes 部署。
- 不通过自动 migration 隐式改变生产数据库。

## 3. 核心决策

### 3.1 一个镜像，两种 Compose 模式

镜像包含以下程序或产物：

```text
/app/bin/edge          # 唯一公网入口
/app/bin/api           # ConnectRPC API
/app/bin/realtime      # WebSocket 和 owner RPC
/app/bin/worker        # 异步任务进程
/app/bin/migrate       # 一次性 migration 命令
/app/web               # apps/web 的构建产物
/app/infra/migrations  # migration SQL
```

额外的多服务模式使用同一个镜像分别启动 `edge`、`api`、`realtime` 和 `worker` 服务。每个服务只有一个前台进程，因此数据库角色、信号处理、重启策略和资源边界不会互相污染。`migrate up` 是同一镜像的独立一次性命令。

默认单服务模式由 `docker-compose.yaml` 提供，只有一个应用服务 `app`，没有 `extends`，也不定义 PostgreSQL、Redis、MinIO、init 或独立 migration 服务。它使用镜像内的 `serve-all` 入口在同一容器内启动上述四个进程。`serve-all` 是一个真正的进程主管，负责转发信号、回收子进程、在关键进程退出时结束容器，并为 API、realtime 和 worker 注入各自的数据库与 keyring 配置；不能用简单的 shell 后台命令替代。

两种模式共享同一个镜像和 `8080` 公网入口契约，但文件和服务定义互不依赖。单服务模式适合只想运行一个应用容器、由外部系统提供 PostgreSQL、Redis 和 S3 的部署；多服务模式适合需要 Compose 同时创建依赖服务、独立重启、资源限制和后续独立扩容的部署。两份编排应择一运行，不要求同时启动。

### 3.2 一个公网入口端口

对外只保留 edge 的端口：

```text
容器公网入口：:8080
Docker 映射：  8080:8080
```

内部服务端口不写入宿主机 `ports`，目标配置如下：

| 进程 | 默认单服务模式 | 额外多服务模式 | 是否发布到宿主机 | 用途 |
| --- | --- | --- | --- | --- |
| edge | `:8080` | `:8080`（服务名 `edge`） | 是，唯一端口 | 静态文件、API 代理、WebSocket 代理、聚合健康检查 |
| api | `127.0.0.1:8081` | `:8080`（服务名 `api`） | 否 | ConnectRPC API |
| realtime 公网服务 | `127.0.0.1:8090` | `:8090`（服务名 `realtime`） | 否 | WebSocket 上游 |
| realtime owner RPC | `127.0.0.1:8091` | `:8091`（服务名 `realtime`） | 否 | API 与 realtime 的内部协调 |
| worker | 无 | 无 | 否 | 后台任务 |

默认模式中 API、realtime 和 owner RPC 只能由同一容器内的 edge 访问；额外多服务模式中这些端口仅存在于 Compose 私有网络。两种模式都不会把内部端口变成外部部署契约。

### 3.3 使用 Go 应用入口，不内置 Nginx

新增一个轻量的 Go edge/gateway 进程，职责为：

- 从镜像内提供 `apps/web/dist` 静态文件。
- 将 ConnectRPC 请求转发到本机 API。
- 将精确路径 `/realtime/game` 的 WebSocket Upgrade 转发到本机 realtime。
- 提供不泄露秘密的 liveness 和 readiness 入口。
- 统一处理超时、请求头、客户端 IP 和 WebSocket draining 行为。

这里的“静态文件服务”不是只提供 HTML 的文件服务器，而是应用入口网关。只提供静态文件无法解决 API 和 WebSocket 的多端口问题。

选择 Go edge 的原因：

- 应用镜像不增加 Nginx 配置、模板渲染和额外运行时。
- 路由、健康检查、进程配置和应用日志使用同一套语言与构建流程。
- 容器可以只暴露一个应用程序端口，外部 Nginx 不再需要理解内部服务拓扑。

Nginx 不是禁止项。若后续压测证明静态缓存、代理缓冲或连接管理需要专门代理，可以把 Nginx 作为可替换的 edge 实现，但不改变单公网端口契约。

### 3.4 前端使用同源地址

生产镜像构建前端时使用同源配置：

- `VITE_API_BASE_URL` 为空字符串。
- `VITE_REALTIME_URL` 为空字符串，运行时默认使用当前页面下的 `/realtime/game`。

浏览器最终访问形态为：

```text
https://game.example.test/                                  # web/dist
https://game.example.test/platform.room.v1.RoomService/...   # ConnectRPC
wss://game.example.test/realtime/game                       # WebSocket
```

这样外部域名切换不需要重新修改 API 和 WebSocket 的跨域地址，API、Cookie、CSRF 和 WebSocket 都维持同源边界。

### 3.5 数据依赖不放进应用镜像

“单镜像”仅指应用程序镜像。以下依赖由 Compose 或外部基础设施提供：

- PostgreSQL：权威持久化和 migration 目标。
- Redis：租约、限流、热点缓存和非权威通知。
- S3/MinIO：主题资产、归档和审计 checkpoint。

开发或单机演示可以由 Compose 启动这些依赖；生产环境可以把它们替换为外部托管服务，而不改变应用镜像。

## 4. 目标运行拓扑

两种模式都把 edge 作为唯一宿主机入口；差异只在应用进程是否跨容器分布。

### 4.1 默认单服务模式（`docker-compose.yaml`）

```text
Browser
   |
   | optional: external Nginx / TLS / domain
   | one upstream only
   v
host:8080 -> container:8080
              |
              +-- app
                    +-- edge      :8080  (only published port)
                    +-- api       127.0.0.1:8081
                    +-- realtime  127.0.0.1:8090 + 127.0.0.1:8091
                    +-- worker    no listener
              |
              +-- PostgreSQL / Redis / S3 are external dependencies
```

单服务模式的 Compose 文件只允许出现 `app` 这一个服务；内部监听地址必须绑定回环地址，避免同一容器内的 API、realtime 或 owner RPC 被意外当成额外部署入口。`app` 容器重启时四个应用进程整体重启。

### 4.2 额外多服务模式（`docker-compose.multi.yaml`）

```text
Browser
   |
   | optional: external Nginx / TLS / domain
   | one upstream only
   v
host:8080 -> container:8080
             |
              +-- edge :8080  (only published host port)
              |     +-- /, /invite/*       -> /app/web
              |     +-- /platform.*        -> http://api:8080
              |     +-- /realtime/game     -> http://realtime:8090
              |     +-- /health/*          -> aggregated checks
              |
              +-- api      :8080 (private Compose network)
              +-- realtime :8090 + :8091 (private Compose network)
              +-- worker   no listener
              +-- PostgreSQL / Redis / S3 are external or Compose services
```

内部 API 和 realtime 仍可单独测试、单独记录日志和单独执行 graceful shutdown，但不能从宿主机端口直接访问。

## 5. Edge 路由契约

### 5.1 用户端路由

| 请求 | edge 行为 | 约束 |
| --- | --- | --- |
| `/`、静态资源路径 | 从 `/app/web` 提供文件 | 静态资源按 Vite 产物处理缓存 |
| `/invite/*` 和其他 SPA 页面路径 | 文件不存在时回退到 `index.html` | 只对 HTML 导航回退，不能吞掉 API 404 |
| `/platform.identity.v1.IdentityService/*` | 代理到 API | 请求体不缓存，保留 Cookie 和 CSRF 相关头 |
| `/platform.room.v1.RoomService/*` | 代理到 API | 请求体不缓存，保留 Connect 版本头 |
| `/platform.game.v1.GameService/*` | 代理到 API | 由 API 负责 realtime 授权和业务鉴权 |
| `GET /realtime/game` | 代理到 realtime | 必须保留 Upgrade，路径必须精确匹配 |

### 5.2 管理端路由

API 当前同时注册管理端 ConnectRPC 服务：

- `/platform.admin.v1.AdminAuthService/*`
- `/platform.admin.v1.AdminIdentityService/*`

edge 可以把这两组路径转发到 `http://api:8080`，但管理域名与用户域名的隔离不能因为共用一个端口而删除。部署层仍可以让两个域名指向同一个 edge 入口，再由 edge 或外部 Nginx 按 Host 保持管理端访问边界。管理端页面本身目前没有独立 `apps/admin` 前端包，因此本设计不新增管理端静态页面。

### 5.3 健康检查

edge 提供两个不需要用户身份的入口：

- `GET /health/live`：只证明 edge 进程仍能响应，不检查数据库。
- `GET /health/ready`：聚合 API 的 `/readyz` 与 realtime 的 `/health/ready`。

readiness 未通过时返回 HTTP `503`。响应不能包含 DSN、Redis URL、密钥路径、内部地址或具体依赖错误详情。worker 没有 HTTP readiness；单服务模式由 `serve-all` 在其退出时终止 `app`，多服务模式由 Compose 负责重启 worker。现有 API 的 `/readyz` 与 `/readyz/sensitive` 保留为内部检查入口，由 edge 聚合后对外提供稳定命名。

## 6. Compose 服务生命周期

### 6.1 启动

多服务模式中的每个应用服务以自己的二进制作为 PID 1，负责：

1. 读取并校验自己的进程配置。
2. 通过 Compose `depends_on` 等待数据库、Redis、对象存储和 keyring staging 完成。
3. 将 readiness 暴露给 edge 聚合。
4. 在启动失败时以非零状态结束自己的容器。

单服务模式的 `app` 以 `serve-all` 作为 PID 1，执行相同的配置校验和依赖等待，然后管理四个子进程。`serve-all` 必须可靠转发 SIGTERM/SIGINT、回收子进程、处理部分服务提前退出，并在关键进程失败时让容器返回非零；不能用 `api & realtime & worker & edge` shell 替代。

### 6.2 退出与重启

- Compose 向多服务模式的各服务 PID 1，或单服务模式的 `serve-all` PID 1 转发 SIGTERM/SIGINT。
- realtime 先进入 draining，停止新连接并通知客户端重连。
- API、edge 和 worker 在各自关闭超时内释放资源。
- 多服务模式中单个服务异常退出时，由该服务的 restart policy 重启对应容器；单服务模式中任一关键子进程异常退出时，由 `serve-all` 终止整个 `app` 容器并由 Compose 重启。
- edge readiness 在依赖未恢复时保持失败，避免容器仅因进程存在就被视为可用。
- `app`、`api`、`realtime`、`worker` 和 `edge` 设置不小于其最长优雅退出超时的 `stop_grace_period`；默认 `65s`，避免 Docker 默认 `10s` 在 draining 或子进程回收前强制终止。
- 需要整体回滚时，Compose 使用同一个镜像 tag 或 digest 重启整套应用服务。

### 6.3 Migration 生命周期

默认 `docker compose up` 不自动执行 migration。发布流程为：

1. 使用新镜像执行一次 `migrate up`。
2. migration 成功后，在多服务模式启动或重启 `api`、`realtime`、`worker` 和 `edge`；在单服务模式启动或重启 `app`。
3. migration 失败时阻止应用发布。

Compose 可以使用同一个镜像定义一次性 migration service 或 migration profile，但不能为 migration 额外构建镜像。

## 7. Docker 镜像设计

### 7.1 多阶段构建

Dockerfile 使用多阶段构建：

1. Node/pnpm 阶段安装 workspace 依赖并构建 `@game-night/web`。
2. Go 构建阶段编译 edge、api、realtime、worker 和 migrate。
3. 最终阶段复制 Go 二进制、前端构建产物、migration SQL、CA 证书和必要的时区数据。

构建必须使用仓库固定的 `.go-version`、`.node-version` 和 `pnpm-lock.yaml`，执行 `pnpm install --frozen-lockfile`。运行时不能包含源码、`.git`、`node_modules`、pnpm store 或测试夹具。

### 7.2 运行时约束

- 最终镜像使用非 root 用户；Compose 的 `GAME_NIGHT_RUNTIME_UID/GID` 必须与镜像一致，默认约定为 `10001:10001`。
- 默认文件系统尽量只读，仅通过明确 volume 或临时目录提供写入能力。
- 应用不在镜像内保存 PostgreSQL、Redis、S3 凭据。
- 只声明 `EXPOSE 8080`。
- Dockerfile 不定义对所有子命令通用的固定 `HEALTHCHECK`，因为同一镜像还运行无 HTTP 监听的 worker 和 migrate；两份 Compose 只对 `app` 或 `edge` 使用 `/health/live`，编排层 readiness 使用 `/health/ready`。
- 镜像标签和 OCI label 包含 commit SHA、构建时间、源仓库和版本。
- 镜像命令支持 `serve-all`、`edge`、`api`、`realtime`、`worker`、`migrate up` 和必要的版本查询命令。

### 7.3 `.dockerignore`

必须排除：

- `.git`、`.omx`、`.codegraph`、编辑器配置和本地凭据。
- 各 workspace 的 `node_modules` 和 pnpm store。
- 本地 `dist`、coverage、Playwright 录制、日志和临时目录。
- 测试数据库数据、MinIO 数据和本地 checkpoint。

前端 `dist` 必须由 Docker 构建阶段生成，不能依赖开发机现有产物。

## 8. Compose 编排设计

### 8.1 默认单服务应用服务（`docker-compose.yaml`）

默认单服务编排只提供一个 `app` 服务，使用同一个镜像的 `serve-all` 命令：

```yaml
services:
  app:
    image: ghcr.io/<owner>/<repository>:<tag>
    command: ["serve-all"]
    ports:
      - "8080:8080"
```

`docker-compose.yaml` 不引用 `docker-compose.multi.yaml`，也不定义数据库、Redis、对象存储、init 或长期 migration 服务。`app` 内部由 edge 监听 `:8080`，API 监听 `127.0.0.1:8081`，realtime 监听 `127.0.0.1:8090` 和 `127.0.0.1:8091`；只有 edge 的 `8080` 映射到宿主机。单服务模式的进程主管把三个进程专用 DSN 映射为各自的 `GAME_NIGHT_DATABASE_URL`，不能让多个进程共享错误的数据库角色。

### 8.2 额外多服务应用服务（`docker-compose.multi.yaml`）

额外多服务编排使用同一个应用镜像启动多个服务，但只发布一个端口：

```yaml
services:
  edge:
    image: ghcr.io/<owner>/<repository>:<tag>
    command: ["edge"]
    ports:
      - "8080:8080"
  api:
    image: ghcr.io/<owner>/<repository>:<tag>
    command: ["api"]
  realtime:
    image: ghcr.io/<owner>/<repository>:<tag>
    command: ["realtime"]
  worker:
    image: ghcr.io/<owner>/<repository>:<tag>
    command: ["worker"]
```

不得把 API、realtime 或 owner RPC 的端口写入任何应用服务的 `ports`。如需容器内部调试，使用 Compose 网络或临时诊断配置，不把调试端口变成部署契约。

### 8.3 依赖服务

额外多服务 Compose 可以定义：

- `postgres`，带 `pg_isready` healthcheck。
- `redis`，带 `redis-cli ping` healthcheck。
- `minio`，带对象存储 readiness healthcheck。

多服务模式通过服务名访问这些依赖；默认单服务模式不定义依赖服务，必须将同名环境变量配置为外部地址。应用镜像不依赖 Compose 服务名才能启动。

额外多服务 Compose 的 PostgreSQL、Redis 和 MinIO 使用私有网络上的明文连接，因此默认环境为 `development`。默认单服务 Compose 直接使用外部依赖 URL。将 `GAME_NIGHT_ENVIRONMENT` 切换为 `production` 前，必须分别配置 PostgreSQL TLS、Redis TLS 和 HTTPS 对象存储 endpoint；应用配置会拒绝生产环境的明文 PostgreSQL、Redis 和对象存储连接。

### 8.4 Migration command

migration 使用同一镜像和同一组数据库 secret。多服务模式可以额外定义 profile 服务：

```yaml
services:
  migrate:
    image: ghcr.io/<owner>/<repository>:<tag>
    command: ["migrate", "up"]
    profiles: ["migration"]
```

`migrate` 不应与长期应用服务形成无条件的长期依赖关系。默认单服务模式用同一个 `app` 服务的临时命令执行 `migrate up`，不是第二个 Compose 服务；生产发布系统必须显式等待 migration 成功，再启动多服务模式的 `api`、`realtime`、`worker`、`edge`，或单服务模式的 `app`。

## 9. 外部 Nginx 与域名

外部 Nginx 不是应用镜像的组成部分。需要 TLS 或域名时，它只承担边缘职责：

```text
game.example.test  ->  http://127.0.0.1:8080
admin.example.test ->  http://127.0.0.1:8080
```

外部 Nginx 不再配置 API、realtime 和静态文件的多个 upstream，只需要：

- 一个应用 upstream。
- 标准 HTTP 反代头。
- WebSocket Upgrade 支持。
- TLS、域名和 Host 转发。

如果部署环境不需要 TLS 或多域名，用户可以直接通过宿主机 `8080` 访问应用，不必安装 Nginx。

管理域名仍需保留 Host 级隔离。共用一个入口端口不等于把用户端和管理端的权限、Origin 或服务路径混为一体。

## 10. 配置契约

两份编排至少需要覆盖以下模式相关配置：

| 配置 | 默认单服务模式 | 额外多服务模式 | 说明 |
| --- | --- | --- | --- |
| `GAME_NIGHT_EDGE_LISTEN_ADDRESS` | `:8080` | `:8080` | edge 唯一公网监听地址 |
| `GAME_NIGHT_API_LISTEN_ADDRESS` | `127.0.0.1:8081` | `:8080` | API 内部监听，不发布到宿主机 |
| `GAME_NIGHT_REALTIME_PUBLIC_LISTEN_ADDRESS` | `127.0.0.1:8090` | `:8090` | realtime WebSocket 内部监听 |
| `GAME_NIGHT_REALTIME_INTERNAL_LISTEN_ADDRESS` | `127.0.0.1:8091` | `:8091` | realtime owner RPC 内部监听 |
| `GAME_NIGHT_API_REALTIME_BOOTSTRAP_URL` | `http://127.0.0.1:8091` | `http://realtime:8091` | API 访问 realtime owner RPC |
| `GAME_NIGHT_REALTIME_ADVERTISED_URL` | `http://127.0.0.1:8091` | `http://realtime:8091` | realtime 内部协调地址 |
| `GAME_NIGHT_EDGE_API_UPSTREAM_URL` | `http://127.0.0.1:8081` | `http://api:8080` | edge 的 API 上游 |
| `GAME_NIGHT_EDGE_REALTIME_UPSTREAM_URL` | `http://127.0.0.1:8090` | `http://realtime:8090` | edge 的 WebSocket 上游 |

前端在两种模式都把 `VITE_API_BASE_URL` 和 `VITE_REALTIME_URL` 构建为空字符串，以使用同源 API 和 `/realtime/game`。`GAME_NIGHT_USER_ORIGINS`、`GAME_NIGHT_ADMIN_ORIGINS` 与 `GAME_NIGHT_TRUSTED_PROXY_CIDRS` 均由实际域名和代理网段决定。

数据库、Redis、对象存储、keyring、内部 token 和 checkpoint sink 继续使用现有进程配置。多服务模式直接给每个容器设置 `GAME_NIGHT_DATABASE_URL`；单服务模式通过 `GAME_NIGHT_API_DATABASE_URL`、`GAME_NIGHT_REALTIME_DATABASE_URL` 和 `GAME_NIGHT_WORKER_DATABASE_URL` 保持角色分离，再由 `serve-all` 写入各自子进程环境。单服务模式直接只读挂载 `GAME_NIGHT_API_SECRETS_DIR` 和 `GAME_NIGHT_WORKER_SECRETS_DIR`；多服务模式使用 secret staging volume，API 使用全量 keyring，worker 只挂载 PII、TOTP 和 audit 三类最小 keyring。secret 只能通过部署环境或 secret file 注入，不能进入前端 bundle、Docker layer、Compose 仓库文件或日志。

## 11. GitHub 镜像发布

新增独立的镜像发布 workflow，不把镜像发布逻辑塞进普通 PR CI：

- PR：只执行 Docker build 校验，不推送镜像。
- `master`：推送 `sha-<commit>` 和 `latest`。
- Git tag：推送版本标签和对应 `sha-<commit>`。
- 使用 `docker/metadata-action`、`docker/login-action` 和 `docker/build-push-action`。
- Job 权限至少包含 `contents: read`、`packages: write`。
- 镜像名统一为 `ghcr.io/<owner>/<repository>`，使用仓库实际小写名称。
- 推送后在 workflow summary 输出镜像 digest，部署使用 digest 或不可变 SHA 标签。
- 初始目标平台为 `linux/amd64`；Go 与前端构建稳定后扩展到 `linux/arm64`。

镜像发布前至少验证：

1. Go 二进制构建成功。
2. 前端 build 成功且没有外部 API 地址依赖。
3. 容器只监听 `8080`。
4. `/health/live` 可用，依赖未就绪时 `/health/ready` 返回 `503`。
5. ConnectRPC 请求能通过同一端口到达 API。
6. `/realtime/game` 能完成 WebSocket Upgrade、认证、draining 和重连。
7. `migrate` 子命令能使用同一镜像单独执行。

## 12. 安全与可观测性

### 12.1 安全边界

- 单服务模式中的 API、realtime public 和 owner RPC 必须绑定容器回环地址；多服务模式可以监听容器网络接口，但对应服务不得声明宿主机 `ports`。
- owner RPC `8091` 永远不能出现在 Compose `ports` 或外部 Nginx upstream。
- edge 不记录 Cookie、CSRF token、恢复码、内部 token 或完整请求体。
- 代理头只在可信代理网段内接受；直连客户端不能伪造真实来源。
- WebSocket 只允许精确 `/realtime/game` 路径，不允许通过任意路径转发到 realtime。
- SPA fallback 不能把 API、健康检查或管理路径静默返回 `index.html`。

### 12.2 日志与指标

edge 记录请求 ID、trace ID、路径类别、状态码、请求时长和上游类别，不记录敏感 payload。至少暴露或汇总：

- edge 请求总量、4xx/5xx 和上游错误。
- ConnectRPC 请求延迟与拒绝率。
- WebSocket 当前连接、Upgrade 失败、关闭原因和 draining 数量。
- API、realtime、worker readiness。
- 各 Compose 应用服务的退出次数、重启次数和关闭耗时。

## 13. 风险与取舍

### 13.1 接受的取舍

- 单服务模式配置更少，但任一关键子进程异常都会触发整个 `app` 容器重启，且四个进程共享容器级资源与权限边界。
- 多服务模式需要更多容器和 Compose 配置，但每个应用服务可以独立重启、限额、记录退出状态并扩容。
- 两种模式都使用同一个应用镜像，不引入第二套构建产物；默认单服务模式外接 PostgreSQL、Redis 和 S3，多服务模式由 Compose 创建 PostgreSQL、Redis 和 MinIO。

默认由部署者按运维需求选择一种模式。只想运行一个应用容器时使用单服务模式；需要 Compose 同时拉起依赖容器、进程级隔离或独立扩容时使用多服务模式，并保持 edge 路由契约不变。

### 13.2 主要风险

- Go edge 的 WebSocket 代理必须完整覆盖 Upgrade、读写超时、draining、连接关闭和客户端 IP 头测试。
- SPA fallback、管理端 Host 隔离和 API 404 之间容易出现路由误吞。
- Compose 的依赖条件、一次性初始化服务和 readiness 配置错误会造成假可用或启动顺序循环。
- 镜像内包含多个二进制，构建缓存和安全扫描必须覆盖所有运行路径。
- `serve-all` 如果不能正确转发信号、回收子进程或在部分失败时退出，会让单服务容器进入假可用状态。

## 14. 实施结果

1. 已实现 edge/gateway 的配置、静态文件服务、ConnectRPC 代理、WebSocket Upgrade、健康检查和代理头边界测试。
2. 已实现 `game-night` launcher：单命令转发、`serve-all` 子进程管理、信号转发、超时回收、进程专用 DSN/keyring 映射和本地 healthcheck 命令。
3. 已新增多阶段 Dockerfile、`.dockerignore`、非 root runtime、前端产物和 migration SQL 复制；Compose 只在 `app` 或 `edge` 上启用 healthcheck。
4. 已落地默认 `docker-compose.yaml` 单容器编排，以及额外 `docker-compose.multi.yaml` 多服务编排、数据库角色初始化、Redis、MinIO 和 migration profile；两份编排没有文件级 `extends` 关系。
5. 已新增 GHCR workflow：PR build 验证，`master`/`v*` 推送，输出可部署 digest。
6. 已更新开发与发布命令；旧 Nginx 配置保留为外部 TLS/域名反代参考，不再作为应用镜像运行时依赖。

## 15. 验收标准

- 使用任一编排启动后，应用服务只发布一个宿主机端口 `8080`。
- 默认 `docker-compose.yaml` 只定义一个长期应用服务 `app`，不会创建 PostgreSQL、Redis、MinIO、init 或独立 migration 服务；`docker-compose.multi.yaml` 额外提供 `edge`、`api`、`realtime` 和 `worker` 四个长期应用服务及依赖服务。
- 宿主机无法通过端口访问 API、realtime 或 owner RPC 的内部端口。
- 浏览器从同一个域名加载前端、调用 ConnectRPC 并连接 `/realtime/game`，不依赖构建时跨域地址。
- API 或 realtime 未就绪时，`/health/ready` 返回 `503`；恢复后自动变为 `204`。
- Compose 向 realtime 转发 SIGTERM 后能触发 draining，并在超时内退出服务。
- 多服务模式中任一应用服务异常退出时只重启对应容器；单服务模式中任一关键子进程异常退出时 `app` 整体返回非零并重启。
- `migrate` 可以使用同一个 GHCR 镜像一次性执行，失败时不会启动长期服务。
- 外部 Nginx 只需要一个应用 upstream 即可支持用户域名、管理域名和 WebSocket。
- GitHub Actions 能为 PR 构建镜像、为 `master` 和版本 tag 推送镜像，并输出可部署 digest。
- 不把数据库、Redis、S3/MinIO、TLS 证书或运行时 secret 打进应用镜像。

## 16. 决策摘要

- 采用一个应用镜像，同时提供单应用容器和多应用容器两份 Compose 编排。
- 默认 `docker-compose.yaml` 使用 `serve-all` 在一个 `app` 容器中管理 edge、API、realtime 和 worker；`docker-compose.multi.yaml` 额外把四个进程拆为独立服务并创建依赖容器。
- 采用一个 Go edge 作为唯一公网入口，不把 Nginx 作为镜像运行时依赖。
- 两种模式都只发布 `8080`；其他端口只存在于容器回环地址或 Compose 私有网络。
- 前端静态文件、ConnectRPC 和 WebSocket 使用同源入口。
- PostgreSQL、Redis、S3/MinIO 在单服务模式由外部基础设施提供，在额外多服务模式由 Compose 提供。
- migration 使用同一镜像的一次性命令，不在默认启动时自动执行。
- Compose 负责编排，GitHub Actions 负责构建和发布 GHCR 镜像。
- 本设计先优化单机自托管部署体验，同时保留未来拆分独立服务的路径。
