# ADR 0001: 采用 Go、pnpm 与 Buf 的单仓库工具链

- 状态：Accepted
- 日期：2026-07-17

## Context

平台需要同时维护 Go 服务端、Vue/TypeScript 客户端、跨语言协议和可独立扩展的游戏模块。首套生产部署是单机，但代码边界必须支持后续横向扩展。

## Decision

采用一个 Git monorepo：根 Go module 管理服务端代码，pnpm workspace + Turborepo 管理前端与 TypeScript 包，Buf 管理 Protocol Buffers lint、breaking 和生成。依赖方向由仓库边界检查器与 CI 强制。

## Consequences

- 一个变更可以原子更新协议、服务端与客户端。
- Go 与 TypeScript 工具链必须同时维护，CI 成本高于单语言仓库。
- 生成插件、工具链和 lockfile 必须固定版本。
- 具体游戏保持自包含，平台不能反向依赖游戏实现。
