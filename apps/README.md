# Apps

应用目录只负责进程或前端入口、组合依赖和传输适配。

- `web`: 移动 Web/PWA 入口。
- `admin`: 管理后台入口。
- `api`: ConnectRPC HTTP 入口。
- `realtime`: WebSocket 网关与会话进程入口。
- `worker`: 异步任务入口。
- `migrate`: 独立数据库迁移入口，不由 API 启动时调用。
- `adminctl`: 无 HTTP 的管理员灾难恢复入口。

应用可以组合 `platform`、`sdk`、`packages` 和构建注册的 `games`，并持有 HTTP、ConnectRPC、进程生命周期与配置装配，但不得承载可复用业务规则。多个服务端进程共享的安全配置加载器放在 `apps/internal`，单个进程的传输和 CLI 配置保留在自身 `internal` 目录。
