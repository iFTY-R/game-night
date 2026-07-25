# 房间暂停与房主转移实施计划

> **执行要求：** 按任务顺序测试先行实施；暂停/恢复的房间治理与会话生命周期必须同事务提交；生成文件只通过仓库生成命令更新；每个阶段形成独立、单一意图提交。
>
> **状态：** 已确认，执行中

**目标：** 为进行中的多人游戏提供可恢复、冻结计时的房主暂停与成员申请暂停能力，并允许房主在持续房间中把治理权安全转移给当前参与玩家。

**设计依据：** [房间暂停与房主转移设计](../specs/2026-07-26-room-pause-host-transfer-design.md)

**技术栈：** Go 1.26、PostgreSQL + sqlc、ConnectRPC + Buf、Vue 3、Pinia、TypeScript、Vitest、Playwright。

## 1. 验收标准

- 房主可直接暂停/恢复活动会话，参与玩家可申请暂停，房主可批准或拒绝精确的当前申请，旁观者不能申请。
- 暂停后服务端拒绝玩家动作、系统动作和计时器推进；恢复时所有计时器保留暂停前的剩余时长，多次暂停可正确累计。
- 房间治理状态与游戏会话状态始终原子一致，刷新、重连和漏通知后可从 PostgreSQL 权威快照恢复。
- 房主可把身份转移给当前参与玩家；旧房主立即失权，新房主立即接管，未完成的 `pending_start` 被取消。
- 公共投影在暂停期间不暴露可执行动作，Web 共享实时桌再次屏蔽动作，所有游戏自动获得一致暂停行为。
- 移动端暂停控件融入现有游戏操作托盘，暂停层不遮挡关键桌面；房主转移位于玩家信息弹层并有二次确认。
- 领域、事务、传输、客户端和视觉测试覆盖权限、竞态、恢复、横竖屏与 reduced-motion；生成漂移、类型检查和构建通过。

## 2. 实施步骤

### Task 1：扩展游戏会话暂停时钟模型

**Files:** Modify `platform/game-runtime/model.go`, `platform/game-runtime/action.go`, `platform/game-runtime/errors.go`, `platform/game-runtime/model_test.go`, `platform/game-runtime/action_test.go`, `platform/game-runtime/service.go`, `platform/game-runtime/service_test.go`.

- [ ] 先增加测试，覆盖 `Suspend` 写入 `SuspendedAt`、`Resume` 按暂停持续时间统一平移每个计时器和 `NextDeadlineAt`、多次暂停累计、无计时器恢复和非法时间/状态拒绝。
- [ ] 在 `SessionSnapshot` 增加可选 `SuspendedAt`，恢复校验保证只有 `suspended` 状态携带该时间。
- [ ] 修改 `Session.Suspend`/`Resume`，保持游戏状态版本不变；恢复使用溢出安全的时间平移并清空 `SuspendedAt`。
- [ ] 修改 `NewLifecycleCommit`：仅允许 `suspended -> active` 发生严格一致的计时平移，其它生命周期转换继续禁止修改计时器。
- [ ] 为公开服务增加明确的 `SuspendCommand`，保留现有模块不可用自动挂起语义并使人工暂停/恢复共用生命周期提交。

**Verify:** `go test ./platform/game-runtime`、`go vet ./platform/game-runtime`。

**Commit:** `feat(runtime): 冻结暂停期间的游戏计时`

### Task 2：建立房间暂停治理与房主转移领域规则

**Files:** Modify `platform/room/model.go`, `platform/room/errors.go`, `platform/room/service.go`, `platform/room/repository.go`, `platform/room/model_test.go`, `platform/room/service_test.go`; update focused lobby/rules tests where the ownership fence is asserted.

- [ ] 先增加聚合测试，覆盖申请、重复申请、旁观者/房主拒绝、精确批准/拒绝、直接暂停、恢复和会话结束清理。
- [ ] 在 `RoomSnapshot` 增加 `PendingPauseRequest` 与 `ActivePause`，恢复校验约束它们只对应当前 `playing` 会话。
- [ ] 增加 `RequestPause`、`RejectPause`、`Pause`、`Resume`、`TransferHost` 聚合转换，所有权限与状态判断集中在领域层。
- [ ] 转移房主时递增 `OwnershipEpoch`、保持成员版本不变、取消 `PendingStart`；目标为当前参与玩家且不能是自己。
- [ ] 增加服务命令与仓储端口，使用房间版本/成员版本 CAS；申请和拒绝可以单聚合提交，暂停/恢复交给跨聚合事务端口。

**Verify:** `go test ./platform/room`、`go vet ./platform/room`。

**Commit:** `feat(room): 增加暂停治理与房主转移规则`

### Task 3：实现 PostgreSQL 生命周期迁移与原子事务

**Files:** Create next migration after current highest migration; modify `tooling/sqlc/queries/game_session.sql`, `tooling/sqlc/queries/room.sql`, `platform/persistence/postgres/game_session_repository.go`, `platform/persistence/postgres/room_repository.go`, `platform/persistence/postgres/room_game_session_repository.go`, mapping/transition helpers and focused integration tests; regenerate `platform/persistence/postgres/sqlcgen/**`.

- [ ] 迁移为游戏会话增加 `suspended_at`，为房间增加待申请和活动暂停字段及状态约束；约束关闭/结束房间不能残留暂停治理状态。
- [ ] 为恢复增加锁定活动计时器并统一平移 `due_at` 的 sqlc 查询；在同一事务更新计时器、会话 `next_deadline_at`、`suspended_at` 和状态。
- [ ] 扩展生命周期持久化校验，保持 `next_deadline_at = min(timer.due_at)`，同时覆盖无计时器和多计时器。
- [ ] 为申请、拒绝和转移扩展房间 CAS 映射；为直接暂停、批准暂停和恢复增加房间 + 会话原子提交方法。
- [ ] 增加集成测试，证明回滚不会留下半状态、并发旧版本失败、旧房主栅栏失败、`pending_start` 清理和多次暂停计时正确。
- [ ] 运行 `pnpm run generate:sql`，审查共享生成文件只包含当前 schema/query 与工作区已有变更的合并结果。

**Verify:** `go test ./platform/persistence/postgres`（使用开发测试数据库）、`pnpm run check:generated`、`git diff --check`。

**Commit:** `feat(postgres): 持久化房间暂停与计时冻结`

### Task 4：扩展 Connect 公共契约与错误键

**Files:** Modify `contracts/platform/room/v1/room.proto`, `contracts/platform/game/v1/game.proto`, `contracts/platform/common/v1/error.proto`, `tooling/contracttest/contracts_test.go`; regenerate `contracts/gen/go/**` and `contracts/gen/ts/**` through `buf generate`.

- [ ] 为房间快照增加待暂停申请、活动暂停及来源枚举，为会话摘要增加 `suspended_at`。
- [ ] 新增 `RequestRoomPause`、`RejectRoomPauseRequest`、`PauseRoomGame`、`ResumeRoomGame`、`TransferRoomHost` RPC 和精确请求/响应。
- [ ] 请求携带 `expected_version`；暂停/恢复携带活动会话 ID，批准携带申请 ID，转移携带目标用户 ID 与 `ownership_epoch`。
- [ ] 增加精确业务错误码与 message key，保留现有通用房间错误兼容语义。
- [ ] 更新契约测试并运行 `buf lint`、`buf generate`，不手改生成代码。

**Verify:** `pnpm run generate:contracts`、`pnpm run check:generated`、`go test ./tooling/contracttest`。

**Commit:** `feat(contracts): 定义房间暂停与房主转移接口`

### Task 5：接入 API 编排、投影与实时通知

**Files:** Modify `apps/api/internal/transport/room/service.go`, `apps/api/internal/transport/room/game_lifecycle.go` or add focused governance file, `apps/api/internal/transport/room/auth.go`, `apps/api/internal/transport/room/service_test.go`, `apps/api/internal/transport/game/wire.go`, `apps/api/internal/transport/game/service.go`, `apps/api/internal/transport/game/service_test.go`, `apps/api/internal/transport/errors/mapper.go`, API server wiring tests as needed.

- [ ] 实现五个房间 RPC，复用身份、Origin、CSRF 和版本校验；所有命令返回权威最新快照。
- [ ] 直接暂停、批准暂停与恢复通过同一跨聚合事务执行，提交后发布房间和会话更新；发布失败不回滚已提交事务。
- [ ] `roomWire` 投影治理字段；`sessionWire` 投影 `suspended_at`；暂停会话的 `projectionWire` 强制清空 `allowed_actions`。
- [ ] 错误映射输出稳定 `BusinessErrorDetail`；旧房主、旧申请、旧版本和重复命令均映射为可恢复业务错误。
- [ ] 增加服务测试覆盖认证、权限、事务失败、漏通知后的读取收敛、房主转移和 `pending_start` 取消。

**Verify:** `go test ./apps/api/internal/transport/room ./apps/api/internal/transport/game ./apps/api/internal/transport/errors`、`go vet` 同范围。

**Commit:** `feat(api): 接入房间暂停与房主转移流程`

### Task 6：扩展 Web 客户端与共享实时桌状态

**Files:** Modify `apps/web/src/api/client.ts`, `apps/web/src/api/game-projection.ts`, `apps/web/src/stores/room.ts`, `apps/web/src/composables/use-live-game-table.ts`, `apps/web/tests/api-client.test.ts`, `apps/web/tests/room-store.test.ts`, `apps/web/tests/live-game-table.test.ts`.

- [ ] 客户端解析并保留会话 `status` 与 `suspendedAt`，房间 store 保留待申请/活动暂停，并提供五个治理 action。
- [ ] 共享实时桌在暂停状态统一返回空的可执行动作，终止进行中的本地提交，并对暂停错误执行权威刷新而非静默重放。
- [ ] 计时展示使用 `suspendedAt` 冻结剩余时长；恢复投影到达后切换到服务端平移的新截止时间。
- [ ] 房间轮询与实时订阅任一先到都能幂等收敛，不因暂时只收到一侧更新而退出游戏路由或重新初始化桌面。
- [ ] 测试初始暂停加载、运行中暂停、恢复、断线重连、无动作防线和版本冲突刷新。

**Verify:** `pnpm --filter @game-night/web test -- api-client.test.ts room-store.test.ts live-game-table.test.ts`、`pnpm --filter @game-night/web check`。

**Commit:** `feat(web): 接入暂停治理状态与动作防线`

### Task 7：实现移动端暂停与房主转移交互

**Files:** Modify `apps/web/src/views/RoomView.vue`, shared game-table shell/UI components and their tests; modify game client tables only where existing action trays need a standard slot; add focused dialog/components under `apps/web/src/components/` if no existing shared surface fits.

- [ ] 在操作托盘内增加房主暂停/恢复、参与玩家申请暂停和房主审批提示，不新增占据半屏的固定面板。
- [ ] 暂停层保留桌面状态和座位布局，禁用所有游戏动作；房主显示恢复主操作，其他成员显示等待状态。
- [ ] 玩家信息弹层显示完整座位信息；当前房主查看其他参与玩家时显示“转移房主”，使用二次确认并明确立即失权。
- [ ] 所有弹层遵循项目 `AppDialog`、表单校验、清理和陈旧异步响应约定；高影响按钮防重复提交。
- [ ] 覆盖 `360x740`、`390x844`、`844x390` 与桌面视口，验证操作托盘展开/折叠、横竖屏、安全区、长状态轮播和 reduced-motion。

**Verify:** `pnpm --filter @game-night/web test -- room-view.test.ts live-game-table.test.ts`、相关共享组件/游戏客户端测试、`pnpm --filter @game-night/web check`、`pnpm --filter @game-night/web build`。

**Commit:** `feat(web): 增加游戏暂停与房主转移交互`

### Task 8：全链路验证与运行检查

**Files:** 只在验证暴露真实缺口时修改测试或 fixture；不提交截图、临时状态或本地服务数据。

- [ ] 运行所有受影响 Go 单元/集成测试、`go vet`、契约生成/漂移检查、Web Vitest、类型检查和生产构建。
- [ ] 使用开发 PostgreSQL/Redis 启动 API、Realtime 和 Web，验证两台浏览器身份的申请、审批、暂停、恢复、转移和旧房主失权。
- [ ] 验证暂停前剩余时间、暂停持续时间和恢复后剩余时间；验证多次暂停、刷新、断网重连、漏通知与并发操作。
- [ ] 使用 Playwright 对指定移动/横屏/桌面视口截图并执行视觉检查，确认无重叠、操作区不过高、暂停状态清晰且不遮挡游戏。
- [ ] 检查 `git status --short`、每个 staged diff 和提交边界，确保未混入管理后台、部署、玩家卡片或其它并行改动。

**Verify:** `go test ./platform/game-runtime ./platform/room ./platform/persistence/postgres ./apps/api/internal/transport/room ./apps/api/internal/transport/game ./apps/api/internal/transport/errors ./tooling/contracttest`；对应 `go vet`；`pnpm --filter @game-night/web test`；`pnpm --filter @game-night/web check`；`pnpm --filter @game-night/web build`；`pnpm run check:generated`；`git diff --check`。

## 3. 风险与缓解

- **跨聚合半提交：** 暂停、批准和恢复只暴露一个事务端口，仓储同时锁定房间、会话和计时器；故障注入测试验证任一步失败都整笔回滚。
- **恢复后计时器立即到期：** 领域层生成统一平移，持久化层逐行验证差值并维护最小截止时间数据库约束；用确定时钟测试多计时器和多次暂停。
- **房主转移竞态：** 房间 `ownership_epoch` 与版本同时递增并清除 `pending_start`；所有旧房主治理请求使用旧栅栏或旧版本失败。
- **房间与会话通知乱序：** PostgreSQL 是唯一权威，客户端同时保留两个摘要并按会话 ID 收敛；短暂不一致只禁用操作，不退出路由。
- **每个游戏重复暂停逻辑：** 服务端投影和 `useLiveGameTable` 提供两层统一动作屏蔽，游戏组件只消费共享暂停上下文和标准操作槽。
- **共享生成文件已有并行修改：** 生成前记录工作区状态，生成后按契约源审查差异；提交时只暂存本功能源文件及其必需生成物，不覆盖其它任务。
- **移动端误触和遮挡：** 高影响命令二次确认、40px 命中区、提交防重；在操作托盘展开/折叠和指定横竖屏视口执行 Playwright 视觉验收。
