# Apps

应用目录只负责进程或前端入口、组合依赖和传输适配。

- `web`: 移动 Web/PWA 入口。
- `admin`: 管理后台入口。
- `api`: ConnectRPC HTTP 入口。
- `realtime`: WebSocket 网关与会话进程入口。
- `worker`: 异步任务入口。

应用可以组合 `platform`、`sdk`、`packages` 和构建注册的 `games`，但不得承载可复用业务规则。
