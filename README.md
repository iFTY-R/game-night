# 今晚开局

一个面向中国大陆用户、移动端优先的多人在线游戏平台。平台以朋友异地在线组局为主要场景，同时兼容现场同桌聚会。

用户可以创建持续房间，通过链接、二维码或房间码邀请朋友，并在同一房间中切换首发的骰子系列、三关定胜负和德州扑克。

## 产品范围

- 移动 Web/PWA 优先，线上异地联机优先，现场聚会兼容。
- 持续房间承载成员、座位、聊天、观众、候场、游戏切换和房主控制。
- 首批支持骰子系列、三关定胜负和德州扑克，并为后续独立游戏扩展保留契约。
- 本设计不是 MVP 缩减方案；阶段顺序只表达依赖，不代表从正式范围删除能力。

## 当前阶段

总体产品与技术设计已经确认，正在按正式交付顺序建设仓库与工具链基础。

## 技术方向

- Vue 3、Vite、TypeScript、pnpm workspace 与 Turborepo
- Go 模块化平台内核、ConnectRPC 与独立 WebSocket 实时网关
- Protocol Buffers + Buf 跨语言协议
- PostgreSQL 权威持久化、Redis 协调、S3 兼容对象存储

## 文档

- [游戏平台设计规范](docs/superpowers/specs/2026-07-17-game-night-platform-design.md)
- [仓库与工具链基础实施计划](docs/superpowers/plans/2026-07-17-repository-foundation.md)
- [本地开发](docs/operations/development.md)
- [ADR 0001：Monorepo 工具链](docs/adr/0001-monorepo-toolchain.md)

## 基础验证

```powershell
pnpm install --frozen-lockfile
go test ./...
go vet ./...
pnpm run check
pnpm test
pnpm build
buf lint tooling/testdata/proto
```

参考目录只用于理解玩法和识别体验问题，不复用其代码、架构或资产。
