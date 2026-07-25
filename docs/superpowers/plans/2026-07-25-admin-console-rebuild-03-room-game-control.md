# 房间、牌局控制与紧急修正实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付房间/牌局查询、日常控制、owner 协调终止和带 dry-run、版本 CAS、快照审计的固定紧急修正能力。

**Architecture:** 管理查询从 PostgreSQL read model 获取房间、成员、session 和有限事件摘要，并批量补充 Redis owner lease。日常房间命令使用 `platform/room` 的显式管理入口保持聚合不变量；进行中牌局终止复用 `apps/api/internal/transport/game/remote.go` 的 realtime owner 私有协议。只有 owner 路径无法完成且命令属于固定修正枚举时，才允许 `platform/admin/room` 在 canonical lock 顺序下执行受审查的 repair repository 方法。

**Tech Stack:** Go、Connect、PostgreSQL/sqlc、Redis lease、realtime OwnerService、Vue 3、Naive UI、Vitest、Playwright。

---

## Task 1: 定义房间与牌局管理契约

**Files:**
- Create: `contracts/platform/admin/v1/admin_room.proto`
- Modify: `contracts/platform/admin/v1/admin_common.proto`
- Regenerate: `contracts/gen/go/platform/admin/v1/**`
- Regenerate: `contracts/gen/ts/platform/admin/v1/**`
- Modify: `apps/api/internal/server/surface_test.go`
- Modify: `apps/edge/internal/server/server_test.go`
- Modify: `apps/api/internal/transport/adminauth/policy_test.go`

- [ ] **Step 1: 写失败的服务挂载和 policy 覆盖测试**

断言 `AdminRoomService` 只在 admin Host 可用，查询/普通控制/强制终止/repair procedure 分别映射正确 permission 和 elevation scope。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/api/internal/server ./apps/edge/internal/server ./apps/api/internal/transport/adminauth
```

- [ ] **Step 3: 定义查询、命令和修正枚举**

契约包含：

- `ListRooms/GetRoom`：状态、game、host/player、时间、owner、异常筛选与 cursor。
- `ListGames/GetGame`：状态、game/version、room、参与者、state version、owner、最后推进时间和有限事件摘要。
- `SetRoomAdmission/RemoveRoomMember/ForceCloseRoom/ForceTerminateGame`。
- `PreviewEmergencyRepair/ExecuteEmergencyRepair/GetRepairOperation`。

紧急修正只允许：`CLEAR_STALE_OWNER_LEASE`、`TERMINATE_UNRECOVERABLE_GAME`、`REPAIR_ROOM_GAME_LINK`。不得出现任意 JSON patch、任意状态字段或完整 replay 消息。

- [ ] **Step 4: 生成并提交契约**

```powershell
pnpm generate:contracts
pnpm check:generated
git add contracts apps/api/internal/server/surface_test.go apps/edge/internal/server/server_test.go apps/api/internal/transport/adminauth/policy_test.go
git commit -m "feat(admin-room): 定义房间牌局与修正契约"
```

## Task 2: 建立管理查询和 repair 持久模型

**Files:**
- Create: `infra/migrations/00030_admin_room_control.sql`
- Create: `tooling/sqlc/queries/admin_room.sql`
- Regenerate: `platform/persistence/postgres/sqlcgen/admin_room.sql.go`
- Regenerate: `platform/persistence/postgres/sqlcgen/models.go`
- Create: `platform/persistence/postgres/admin_room_query_repository.go`
- Create: `platform/persistence/postgres/admin_repair_repository.go`
- Test: `platform/persistence/postgres/admin_room_query_repository_integration_test.go`
- Test: `platform/persistence/postgres/admin_repair_repository_integration_test.go`

- [ ] **Step 1: 写失败的查询与 repair 集成测试**

覆盖稳定 cursor、room/member/session 一致视图、有限事件上限、repair preview TTL、命令版本、expected room/membership/state/ownership 版本、同一 operation 幂等和 canonical room-first/session-second lock。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test -run "TestAdmin(RoomQuery|Repair)" ./platform/persistence/postgres
```

- [ ] **Step 3: 创建 migration**

创建 `admin_repair_operations`，保存 repair type/version、actor、target、request digest、expected versions、dry-run 摘要、前后快照摘要、状态、TTL、result/audit ID。为 room status/game ID/host/member/created/updated 和 game status/version/updated 建复合索引；事件摘要查询必须有固定 `LIMIT`。

- [ ] **Step 4: 实现 read model 与 cursor**

列表 SQL 只返回管理需要字段，不解码完整 game replay payload。owner lease 不从 PostgreSQL伪造；repository 返回 session ID/ownership epoch，应用层再批量查询 Redis。

- [ ] **Step 5: 实现 repair repository 的锁与 CAS**

repository 只暴露三个固定执行方法。每个方法在事务中重新读取与 dry-run 相同版本，计算 after aggregate，写 repair operation、命令回执、签名审计与 outbox；版本变化全部回滚。

- [ ] **Step 6: 生成、测试并提交**

```powershell
pnpm generate:sql
go test ./platform/persistence/postgres
git add infra/migrations/00030_admin_room_control.sql tooling/sqlc/queries/admin_room.sql platform/persistence/postgres
git commit -m "feat(admin-room): 建立查询与紧急修正持久层"
```

## Task 3: 为房间聚合增加显式管理命令

**Files:**
- Create: `platform/room/admin.go`
- Create: `platform/room/admin_test.go`
- Modify: `platform/room/model.go`
- Modify: `platform/room/repository.go`
- Modify: `platform/persistence/postgres/room_repository.go`
- Test: `platform/persistence/postgres/room_repository_integration_test.go`

- [ ] **Step 1: 写失败的聚合不变量测试**

覆盖管理 actor 可关闭 admission、移除非 host 成员和关闭等待房间，但不能伪装 host、不能移除 host、不能关闭 active game、不能绕过 exact room/membership version。移除 active participant 仍产生既有 participant revoked outbox fact。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/room ./platform/persistence/postgres
```

- [ ] **Step 3: 实现管理 actor 类型和固定方法**

新增与用户 host command 分离的类型，例如：

```go
type AdminActor struct { ID uuid.UUID }

func (room Room) SetAdmissionByAdmin(actor AdminActor, participant, spectator Admission, expected Version, at time.Time) (Room, error)
func (room Room) RemoveMemberByAdmin(actor AdminActor, userID uuid.UUID, expected Version, at time.Time) (Room, RemovalResult, error)
func (room Room) CloseWaitingByAdmin(actor AdminActor, expected Version, at time.Time) (Room, error)
```

`AdminActor` 只表达审计来源，不表达权限；权限在 admin service 校验。用户命令继续使用 host ID，不共享绕过分支。

- [ ] **Step 4: 让 repository 原子写 outbox**

管理移除 participant 复用 `CommitRemoval`，actor kind 新增 admin 且 payload 携带 admin ID；realtime revocation consumer 不需要知道权限，只消费事实。

- [ ] **Step 5: 测试并提交**

```powershell
go test -race ./platform/room ./platform/persistence/postgres
git add platform/room platform/persistence/postgres/room_repository.go platform/persistence/postgres/room_repository_integration_test.go
git commit -m "feat(admin-room): 增加受约束房间管理命令"
```

## Task 4: 实现房间/牌局查询与异常标记

**Files:**
- Create: `platform/admin/room/model.go`
- Create: `platform/admin/room/query.go`
- Create: `platform/admin/room/repository.go`
- Create: `platform/admin/room/owner.go`
- Create: `platform/admin/room/errors.go`
- Create: `platform/persistence/redis/admin_room_reader.go`
- Test: `platform/admin/room/query_test.go`
- Test: `platform/persistence/redis/admin_room_reader_test.go`

- [ ] **Step 1: 写失败的查询组合测试**

覆盖 PostgreSQL row + Redis lease 合并、lease 缺失/过期/epoch 不同、玩家全离线、长时间无推进、room/session 引用不一致等异常标记；Redis 不可用时返回 stale/unknown，不把数据库 owner 字段当实时 owner。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/admin/room ./platform/persistence/redis
```

- [ ] **Step 3: 实现 query service**

service 要求 `rooms.read`/`games.read`，验证 actor 当前 session。列表先按 cursor 查询 DB，再对当前页 session IDs 执行有界 Redis pipeline；响应包含 sampled_at 和 owner freshness。

- [ ] **Step 4: 限制事件摘要**

详情只读取最近固定数量的事件元数据/安全摘要，不调用 `ProjectReplay`，不返回随机数承诺、聊天全文或完整 state payload。

- [ ] **Step 5: 测试并提交**

```powershell
go test -race ./platform/admin/room ./platform/persistence/redis
git add platform/admin/room platform/persistence/redis/admin_room_reader.go platform/persistence/redis/admin_room_reader_test.go
git commit -m "feat(admin-room): 实现房间牌局查询与异常识别"
```

## Task 5: 实现日常控制和 owner 协调终止

**Files:**
- Create: `platform/admin/room/control.go`
- Create: `platform/admin/room/control_test.go`
- Modify: `apps/api/internal/transport/game/remote.go`
- Modify: `apps/api/internal/transport/game/remote_test.go`
- Modify: `apps/realtime/internal/transport/internalgame/service.go`
- Modify: `apps/realtime/internal/transport/internalgame/service_test.go`
- Modify: `platform/game-runtime/service.go`
- Modify: `platform/game-runtime/service_test.go`

- [ ] **Step 1: 写失败的命令矩阵测试**

覆盖：admission/踢人只要求 `rooms.control`；强制关闭等待房间要求 `rooms.force_close`；终止 active game 要求 `games.force_terminate`。所有命令要求 reason/operation/expected version 并返回 executed/conflict/owner unreachable/repair required。

- [ ] **Step 2: 写远程 owner 失败测试**

断言 API 使用现有 `RemoteRuntime.Cancel`/`OwnerService.CancelSession`，owner 解析失败不会回退直接更新 DB；相同 operation 可重试且 terminal 结果稳定。

- [ ] **Step 3: 运行测试确认失败**

```powershell
go test ./platform/admin/room ./apps/api/internal/transport/game ./apps/realtime/internal/transport/internalgame ./platform/game-runtime
```

- [ ] **Step 4: 实现控制 service 和 owner port**

`platform/admin/room` 依赖窄 `GameController` port。应用层用 `RemoteRuntime` 适配它；领域 service 不导入 Connect/realtime 生成类型。owner unreachable 映射稳定业务错误并审计失败结果。

- [ ] **Step 5: 为取消命令补充 operation binding**

现有 `CancelCommand` 已是管理终止入口，但需要 operation ID/request digest 以满足管理幂等。更新 realtime wire 和 repository receipt，保持 room/session 原子 terminal transaction 与 ownership epoch fencing。

- [ ] **Step 6: 测试并提交**

```powershell
go test -race ./platform/admin/room ./apps/api/internal/transport/game ./apps/realtime/internal/transport/internalgame ./platform/game-runtime
git add platform/admin/room apps/api/internal/transport/game apps/realtime/internal/transport/internalgame platform/game-runtime
git commit -m "feat(admin-room): 接入房间控制与牌局终止"
```

## Task 6: 实现三个固定紧急修正流程

**Files:**
- Create: `platform/admin/room/repair.go`
- Create: `platform/admin/room/repair_test.go`
- Modify: `platform/game-runtime/model.go`
- Modify: `platform/game-runtime/model_test.go`
- Modify: `platform/persistence/redis/coordination.go`
- Modify: `platform/persistence/redis/coordination_test.go`
- Modify: `platform/persistence/postgres/admin_repair_repository.go`
- Test: `platform/persistence/postgres/admin_repair_repository_integration_test.go`

- [ ] **Step 1: 写失败的 dry-run/execute 一致性测试**

每种 repair 覆盖：dry-run 无业务副作用；execute 要求同 repair ID/command version/expected versions/`games.emergency_repair` elevation/reason；任何资源变化拒绝；重复 operation 返回同一结果；前后快照摘要和审计 ID 存在。

- [ ] **Step 2: 写各命令边界测试**

- stale lease：只有 lease 已过期且 epoch 与 DB 状态匹配才可 compare-and-delete；活跃 lease 拒绝。
- unrecoverable terminate：只允许 non-terminal session，使用固定系统取消原因和受审查的 aggregate transition。
- link repair：只处理明确的 room active_session 与 session room/status 组合，未知组合拒绝。

- [ ] **Step 3: 运行测试确认失败**

```powershell
go test ./platform/admin/room ./platform/game-runtime ./platform/persistence/redis ./platform/persistence/postgres
```

- [ ] **Step 4: 实现修正计划和值对象**

dry-run 生成不可修改 `RepairPlan`，包含 command version、影响记录、不可逆副作用、expected versions 和到期时间。execute 不接受前端回传 after state，只接受 repair ID、operation ID、reason；服务端重新加载持久 plan。

- [ ] **Step 5: 实现 Redis compare-and-delete 与 DB 原子修正**

Redis lease 清理使用 Lua compare owner/address/epoch/value，不执行通配删除。DB 修正使用 room-first/session-second lock 和 CAS；签名审计写失败回滚 DB 修正。跨 Redis/DB 的 lease 清理记录可恢复状态，重试根据 operation receipt 收敛。

- [ ] **Step 6: 测试并提交**

```powershell
go test -race ./platform/admin/room ./platform/game-runtime ./platform/persistence/redis ./platform/persistence/postgres
git add platform/admin/room platform/game-runtime platform/persistence/redis platform/persistence/postgres/admin_repair_repository.go platform/persistence/postgres/admin_repair_repository_integration_test.go
git commit -m "feat(admin-room): 增加固定紧急修正流程"
```

## Task 7: 接入 AdminRoomService 与应用 wiring

**Files:**
- Create: `apps/api/internal/transport/adminroom/service.go`
- Create: `apps/api/internal/transport/adminroom/wire.go`
- Create: `apps/api/internal/transport/adminroom/service_test.go`
- Modify: `apps/api/internal/transport/adminauth/policy.go`
- Modify: `apps/api/internal/transport/sensitive/registry.go`
- Modify: `apps/api/internal/server/surface.go`
- Modify: `apps/api/internal/application/application.go`
- Modify: `apps/edge/internal/server/server.go`
- Modify: `apps/admin/vite.config.ts`
- Test: `apps/api/internal/application/integration_test.go`

- [ ] **Step 1: 写失败的 adapter/集成测试**

断言 wire actor/permission/elevation 被忽略，查询 permission 和命令矩阵由 policy 强制；owner unreachable、version conflict 和 repair required 具有结构化 detail；成功/拒绝/失败均有审计。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/api/internal/transport/adminroom ./apps/api/internal/server ./apps/api/internal/application
```

- [ ] **Step 3: 实现 adapter 和 wiring**

wire conversion 拒绝 unspecified enum、无效 UUID/时间/cursor/版本。应用 wiring 复用现有 room repository、game session repository、Redis coordinator 和 RemoteRuntime，不创建第二套游戏状态服务。

- [ ] **Step 4: 更新 surface、edge 和 Vite**

只开放生成的 AdminRoomService prefix；user Host 404。把所有 procedure 加入敏感字段 registry，避免原因/快照泄漏到日志。

- [ ] **Step 5: 测试并提交**

```powershell
go test -race ./apps/api/internal/transport/adminroom ./apps/api/internal/server ./apps/api/internal/application ./apps/edge/internal/server
git add apps/api apps/edge apps/admin/vite.config.ts
git commit -m "feat(admin-room): 挂载房间牌局管理服务"
```

## Task 8: 实现房间与牌局前端

**Files:**
- Create: `apps/admin/src/api/admin-room.ts`
- Create: `apps/admin/src/views/rooms/RoomGameView.vue`
- Create: `apps/admin/src/views/rooms/room-query-store.ts`
- Create: `apps/admin/src/views/rooms/room-url-state.ts`
- Create: `apps/admin/src/views/rooms/components/RoomFilters.vue`
- Create: `apps/admin/src/views/rooms/components/RoomTable.vue`
- Create: `apps/admin/src/views/rooms/components/RoomDetailsDrawer.vue`
- Create: `apps/admin/src/views/rooms/components/GameDetailsPanel.vue`
- Create: `apps/admin/src/views/rooms/components/RoomCommandDialog.vue`
- Create: `apps/admin/src/views/rooms/components/TerminateGameDialog.vue`
- Create: `apps/admin/src/views/rooms/components/RepairDialog.vue`
- Modify: `apps/admin/src/router/routes.ts`
- Modify: `apps/admin/src/constants/navigation.ts`
- Modify: `apps/admin/src/stores/navigation.ts`
- Create: `apps/admin/tests/room-query-store.test.ts`
- Create: `apps/admin/tests/room-game-view.test.ts`

- [ ] **Step 1: 写失败的 store/交互测试**

覆盖 URL 筛选/cursor、轮询 pause/resume、stale 数据、详情恢复、普通命令、step-up 后终止、repair dry-run 变化后拒绝、关闭 dialog 取消旧请求。

- [ ] **Step 2: 运行测试确认失败**

```powershell
pnpm --filter @game-night/admin test -- room-query-store.test.ts room-game-view.test.ts
```

- [ ] **Step 3: 实现高密度列表和详情**

房间与牌局使用同一页面 tabs/segmented mode，共享筛选栏+表格+详情抽屉。展示 sampled time、owner freshness、异常原因、有限事件摘要和已执行命令；不展示完整回放入口。

- [ ] **Step 4: 实现控制与 repair 对话框**

普通命令展示影响和 reason。终止/repair 先 step-up；repair 对话框展示服务端 dry-run、版本和不可逆副作用，不允许编辑 JSON。确认前若 plan 过期或资源变化则重新预演。

- [ ] **Step 5: 通过测试后加入导航**

加入“房间与牌局”，权限门禁同时接受 rooms.read/games.read 的服务端定义；无权限时不显示菜单且直接 URL 返回 403。

- [ ] **Step 6: 前端验证并提交**

```powershell
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
git add apps/admin
git commit -m "feat(admin-ui): 交付房间与牌局控制台"
```

## Task 9: 房间牌局集成与浏览器验收

**Files:**
- Modify: `apps/api/internal/application/integration_test.go`
- Create: `apps/admin/e2e/room-game-control.spec.ts`
- Create: `apps/admin/e2e/emergency-repair.spec.ts`

- [ ] **Step 1: 增加真实 owner 集成场景**

覆盖 owner 正常终止、owner 不可达不降级、版本并发、stale lease 修正、链接修正、前后快照审计和 operation 重试。

- [ ] **Step 2: 增加 Playwright 场景**

真实种子房间验证筛选、详情、禁止加入、踢人、关闭等待房间、终止牌局、dry-run/execute 和移动视口操作可达。

- [ ] **Step 3: 运行阶段门禁**

```powershell
pnpm generate
pnpm check:generated
go test -race ./platform/room ./platform/game-runtime ./platform/admin/room ./platform/persistence/postgres ./platform/persistence/redis ./apps/realtime/... ./apps/api/...
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
pnpm check:boundaries
git diff --check
```

- [ ] **Step 4: 运行 E2E 并提交测试**

```powershell
$env:ADMIN_E2E='1'; pnpm --filter @game-night/admin test:e2e -- --grep "房间|牌局|紧急修正"
git add apps/api/internal/application/integration_test.go apps/admin/e2e
git commit -m "test(admin-room): 覆盖控制与修正闭环"
```

## 完成门禁

- 房间/牌局查询和控制均使用真实领域状态与 owner 路由。
- owner 不可达绝不自动直写数据库。
- 三个固定 repair 具备 dry-run、TTL、elevation、版本 CAS、前后快照和签名审计。
- 不出现规则版本发布、完整 replay、任意 JSON 修正或任意 Redis key 删除。
- “房间与牌局”进入导航且定向/集成/E2E 测试通过。
