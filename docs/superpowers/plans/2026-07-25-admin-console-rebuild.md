# Game Night 管理后台完整重建实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不保留旧后台兼容层的前提下，交付连接真实后端的运营概览、用户中心、房间与牌局、安全设置、审计中心和系统运维六个完整模块。

**Architecture:** 继续保留管理 Host、Cookie/CSRF、Connect、签名审计链、领域聚合和 realtime owner 等已验证基础，重建管理员认证状态机、细粒度权限、短时提权和全部管理查询/命令服务。实施按安全基础、用户中心、房间牌局、审计中心、运维与概览五个依赖阶段推进；每个阶段同时完成契约、持久化、领域/应用服务、传输、前端和测试，未闭环模块不进入导航。

**Tech Stack:** Go 1.26、Connect RPC、Protocol Buffers、PostgreSQL/pgx/sqlc/goose、Redis、Vue 3、TypeScript、Pinia、Vue Router、Naive UI、Vitest、Playwright、pnpm/Turborepo。

---

## 计划套件

按以下顺序执行，后一个计划不得绕过前一个计划的完成门禁：

1. [安全基础与后台壳层](./2026-07-25-admin-console-rebuild-01-security-foundation.md)
2. [用户中心、批量任务与导出](./2026-07-25-admin-console-rebuild-02-user-center.md)
3. [房间、牌局控制与紧急修正](./2026-07-25-admin-console-rebuild-03-room-game-control.md)
4. [审计检索与敏感导出](./2026-07-25-admin-console-rebuild-04-audit-center.md)
5. [系统运维、运营概览与最终集成](./2026-07-25-admin-console-rebuild-05-operations-overview.md)

设计基准是 [`docs/superpowers/specs/2026-07-25-admin-console-rebuild-design.md`](../specs/2026-07-25-admin-console-rebuild-design.md)。实现发现规格歧义时先更新规格和当前阶段计划，不在代码里静默改变安全语义。

## 全局执行规则

- [ ] 每个任务先写可观察失败的单测、契约测试或集成测试，再写实现。
- [ ] 每个任务只提交计划列出的文件；生成文件与其源契约/SQL 放在同一提交。
- [ ] 不创建旧 RPC adapter、旧路由跳转、旧认证状态兼容分支或双写表。
- [ ] 不在导航中放禁用项、Mock 数据、静态统计、占位页面或“敬请期待”。
- [ ] 管理查询使用服务端筛选、稳定排序和 opaque cursor；页面不得下载全集后筛选。
- [ ] 管理写操作包含 `operation_id`、原因和目标版本；高危操作同时校验 elevation scope。
- [ ] 所有敏感读取、拒绝、失败和成功写入签名审计；审计不可用时敏感动作 fail closed。
- [ ] 管理命令调用领域服务或明确的管理领域入口，不从 Connect handler 直接执行 SQL。
- [ ] 房间与进行中牌局命令不得伪装成房主；日常牌局终止通过 realtime owner 私有协议。
- [ ] 密码、TOTP seed/code、恢复码、PII、下载 grant 不进入日志、指标、URL、本地存储或错误文本。
- [ ] 所有 Vue 对话框使用 `AppDialog` 和 `toggleDialog(open, payload?)`；关闭时取消旧请求并清理秘密/校验状态。
- [ ] 所有新导出、批量任务和修正流程必须可在刷新后恢复，不依赖浏览器请求持续在线。
- [ ] 新增或修改的常量、重要状态、公开方法、关键分支、异步取消和跨组件联动按仓库注释规范说明意图与约束。

## 目标文件结构

### 契约

最终 `contracts/platform/admin/v1` 只保留新服务契约：

```text
contracts/platform/admin/v1/
  admin_common.proto       # 权限、elevation、命令结果、查询通用类型
  admin_auth.proto         # 登录、密码、2FA、恢复码、会话与提权
  admin_overview.proto     # 运营指标、趋势、异常摘要
  admin_user.proto         # 用户查询、详情、标注、治理、批量任务与导出
  admin_room.proto         # 房间/牌局查询、控制与紧急修正
  admin_audit.proto        # 审计检索、详情与导出
  admin_operations.proto   # 健康、积压、维护模式与固定维护命令
```

删除 `contracts/platform/admin/v1/admin_identity.proto`，运行 `pnpm generate:contracts` 后提交相应 `contracts/gen/go` 与 `contracts/gen/ts` 生成物。

### 后端

`platform/admin` 只负责管理员账户安全、权限、actor context 和 elevation。业务模块放在有明确依赖方向的子包：

```text
platform/admin/             # 认证聚合、会话、2FA、权限、elevation
platform/admin/user/        # 用户查询、标注、治理、批量编排
platform/admin/export/      # 通用导出任务、加密结果、下载 grant
platform/admin/room/        # 房间/牌局管理编排和修正计划
platform/admin/audit/       # 审计查询与导出编排
platform/admin/operations/  # 健康、维护、积压与概览聚合
```

Connect adapter 留在应用传输层：

```text
apps/api/internal/transport/adminauth/
apps/api/internal/transport/adminuser/
apps/api/internal/transport/adminroom/
apps/api/internal/transport/adminaudit/
apps/api/internal/transport/adminoperations/
apps/api/internal/transport/adminoverview/
```

PostgreSQL adapter 继续位于 `platform/persistence/postgres`；SQL 源位于 `tooling/sqlc/queries`。不得把 pgx/sqlc 类型泄漏到领域包。

### 前端

保留 `apps/admin/src/api/connect.ts`、Cookie/错误解析、布局骨架、主题、`AppDialog`、`AsyncState` 与 Lucide 接入；删除依赖旧 `next_step`、旧身份工作台和旧 readiness 展示的页面。最终结构：

```text
apps/admin/src/views/
  auth/          # 登录、首次改密、MFA 验证
  overview/      # 运营概览
  users/         # 用户中心及本域 query store/components
  rooms/         # 房间与牌局及本域 query store/components
  audit/         # 审计中心及本域 query store/components
  security/      # 密码、2FA、恢复码、会话与 elevation
  operations/    # 系统健康、积压和维护命令
```

列表 URL 只保存非敏感筛选、排序、cursor 和资源 ID。PII、秘密结果、命令原因与 elevation 不进入 URL 或持久化 Pinia state。

## 数据迁移顺序

- [ ] `00028_admin_console_security.sql`：重置旧管理员认证数据，建立新会话、2FA、恢复码、elevation 和命令回执约束。
- [ ] `00029_admin_user_center.sql`：标签、备注、批量/擦除任务、通用导出任务和下载 grant。
- [ ] `00030_admin_room_control.sql`：管理查询索引、修正 dry-run/execute 持久状态和快照摘要。
- [ ] `00031_admin_audit_index.sql`：为签名事件增加可校验的检索投影、固定 sequence 上界查询和导出索引。
- [ ] `00032_admin_operations.sql`：维护状态、服务心跳、指标 bucket 和受控任务重试所需状态。

不改写 `00001` 至 `00027` 历史迁移。开发环境通过新增破坏性迁移撤销旧管理认证状态；用户、房间和游戏历史数据不得因后台重建被清空。

## 跨阶段质量门禁

每个阶段结束执行：

```powershell
pnpm generate
pnpm check:generated
go test ./platform/admin/... ./platform/persistence/postgres/... ./apps/api/...
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
pnpm check:boundaries
git diff --check
```

预期：所有命令退出码为 `0`，`check:generated` 无漂移，`git diff --check` 无输出。需要 PostgreSQL/Redis/对象存储的集成测试按阶段计划给出的命令在本地开发栈中执行。

最终阶段追加：

```powershell
go test ./...
pnpm check
pnpm test
pnpm build
pnpm test:backend:integration
$env:ADMIN_E2E='1'; pnpm --filter @game-night/admin test:e2e
```

## 最终验收矩阵

- [ ] 首次修改初始密码后直接得到 full session，2FA 仍为关闭。
- [ ] 2FA 启用、TOTP 登录、恢复码登录、恢复码重新生成、关闭 2FA 和仅密码再次登录全部通过。
- [ ] 修改密码撤销其他会话且不改变 active TOTP enrollment。
- [ ] elevation 按 scope、管理员版本、密码版本、session 和五分钟 TTL 校验，并在安全状态变化时失效。
- [ ] 用户列表、组合筛选、详情、PII、标签、备注、设备、治理、注销、批量任务和导出均使用真实数据。
- [ ] 房间/牌局列表与详情、禁止加入、踢人、解散、owner 终止、固定紧急修正、dry-run/version/snapshot 全部闭环。
- [ ] 审计列表固定最大 chain sequence，读取事件不进入同次分页；详情、差异、敏感导出与单次下载 grant 可验证。
- [ ] 运维展示真实依赖/实例/积压，维护模式和固定任务重试受 elevation、预览、版本和审计保护。
- [ ] 概览展示带统计窗口、采样时间和新鲜度的真实指标、趋势和异常摘要。
- [ ] 最终导航只含六个正式模块；旧页面、旧 RPC、旧 `next_step`/`recovery_pending` 和死代码全部删除。
- [ ] 桌面与移动视口无重叠/溢出，键盘可完成核心流程，敏感值不残留在 DOM、URL、storage 或日志。

## 提交策略

每个详细计划中的任务独立提交。推荐 scope：

```text
feat(admin-auth): ...
feat(admin-user): ...
feat(admin-room): ...
feat(admin-audit): ...
feat(admin-ops): ...
test(admin): ...
docs(admin): ...
```

生成文件、迁移和对应 adapter 不拆成无法构建的半提交。中间提交必须通过该任务列出的定向测试；每个阶段的最后一个提交通过跨阶段质量门禁。
