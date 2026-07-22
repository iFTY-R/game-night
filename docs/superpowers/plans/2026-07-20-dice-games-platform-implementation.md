# 三款骰子游戏平台完整实施计划

> **状态：** 实施中；Task 13 已完成，Task 14 的负载/故障演练仍待完成
>
> **执行要求：** 按任务依赖顺序实施；每个任务先写测试、完成验证后独立提交。三款游戏均实现其正式规则规范，任务顺序只表示依赖，不代表 MVP 取舍。

**目标：** 交付吹牛骰子、789、喜相逢三款可持续联机的完整游戏模块，并接入现有 PartyRoom、GameSession、PostgreSQL 原子持久化、Redis 协调、移动端 Web/PWA、观战、复盘、断线恢复、主题和房主赛后重新开放进房许可。

**架构：** 游戏规则保持在 `games/<game-id>/engine` 的纯 Go 包中；每款游戏有独立协议、配置、状态机、投影、客户端和主题。低层骰子生成、罚酒 ticks 和确定性编解码辅助能力放在服务端 SDK；平台运行时负责版本注册、随机种子注入、动作幂等、计时器、租约、持久化和投影路由；应用层只负责认证、连接和依赖装配。

**技术栈：** 现有 Go 1.26.4、PostgreSQL、Redis、Buf、ConnectRPC、pgx/sqlc；补齐 `coder/websocket`、Vue 3/Vite/TypeScript/Vue Router/Pinia、PWA、Vitest、Playwright、axe-core 和现有主题/UI 包。依赖版本必须写入仓库锁文件，不手写生成元数据。

---

## 1. 范围与规范追踪

本计划执行以下已确认规范：

- [平台总设计](../specs/2026-07-17-game-night-platform-design.md)：持续房间、赛后重新开放进房许可、Game SDK、服务端裁决、移动端布局、主题、观战、复盘和仓库边界。
- [吹牛骰子规则](../specs/2026-07-20-liars-dice-rules-design.md)：私密骰子、叫斋/飞斋、上不封顶叫数、开骰、超时和私密投影。
- [789 规则](../specs/2026-07-20-dice-789-rules-design.md)：公共池、7/8/9、对子优先级、叠杯、掉骰待确认和加注边界。
- [喜相逢规则](../specs/2026-07-20-meet-by-chance-rules-design.md)：豹子/顺子/对子/单骰、`235`、同牌/同大/同小、靶子本轮重摇上限和公开投影。

规范追踪：

| 计划约束 | 当前依据 |
| --- | --- |
| 三款游戏是独立模块，不能合并成一个模式大分支 | 平台总设计第 199-207 行；各游戏规范的 `Game ID` 与独立状态机 |
| 规则引擎无 IO，时间和随机种子由运行时注入 | `sdk/go/game/contract.go:20-37,39-75,282-290` |
| 精确版本恢复，禁止隐式回退 | `sdk/go/game/manifest.go:123-203`、`sdk/go/game/registry.go:20-120` |
| GameSession 事件、计时器、动作回执原子提交 | `platform/game-runtime/model.go:47-242`、`platform/game-runtime/action.go:36-250` |
| PostgreSQL 是权威，旧租约不能写入新版本 | `platform/persistence/postgres/game_session_repository.go:185-280` |
| PartyRoom 与首个 GameSession 必须同事务创建 | `platform/persistence/postgres/room_game_session_repository.go:16-125` |
| 游戏协议放在各自 `games/<game>/proto`，不合并进平台 `oneof` | `contracts/README.md:3-5`、平台总设计第 457-485 行 |
| 引擎只能依赖自身规则包和 SDK，禁止平台/数据库/网络/时间/随机 IO | `tooling/boundarycheck/policy.go:35-79,110-147` |
| 竖屏优先、横屏适配、操作托盘可收起、主题不改变规则 | 平台总设计第 233-303、770-795 行 |

本计划不复制 `.tmp` 或 `E:/WorkPro/Project_20260305/nuxt-games` 的代码、资源、页面结构或架构，也不把任何现实饮酒量写入数据库。

## 2. 当前基线与缺口

- `sdk/go/game` 已有 `Message`、确定性上下文、`Transition`、投影契约和精确注册表，但没有骰子 PRNG、ticks 工具、游戏协议编解码器或运行时服务层。
- `platform/game-runtime` 已有不可变 `Session`、状态版本、ownership epoch、动作回执和完整计时器集，但 `Store` 仍只有创建、读取、租约、回执和动作提交接口，没有 timer/system commit 和 viewer-scoped service。
- `platform/persistence/postgres/game_session_repository.go` 已实现底层 CAS/幂等提交；`room_game_session_repository.go` 已实现跨 PartyRoom/GameSession 原子开局适配器，但尚未接入 `platform/room.Service`。
- `platform/room.Service.StartGame` 仍单独更新 PartyRoom；`apps/api/internal/application/application.go:145-147` 仍注入 `NewDisabledGameCatalog()`，当前 `StartGame` 预期 fail closed。
- `games` 只有 README，没有具体游戏目录；`apps/realtime`、`apps/web`、`sdk/ts/game-client`、`packages/game-ui-kit` 和 `packages/theme-system` 尚未创建。
- `buf.yaml` 目前只扫描 `contracts`，生成脚本也只生成平台协议；`pnpm-workspace.yaml` 已预留 `games/*/client` 和 `games/*/themes`。

## 3. 设计决策与备选方案

### 3.1 推荐方案：独立模块 + SDK 低层骰子能力

创建 `games/liars-dice`、`games/dice-789`、`games/meet-by-chance` 三个顶层模块，每个模块维护自己的 `proto`、`engine`、`projection`、`module`、`client`、`themes` 和 `tests`。`sdk/go/game/dice` 只提供确定性骰点、ticks、座位顺序和边界校验，不包含任何一款游戏的牌型或回合规则。

优点是规则、引擎版本、协议版本、客户端版本和主题可以独立发布与恢复，符合当前边界检查器的 `games/<id>/engine` 假设，也能让三款游戏并行实现。代价是需要为三款游戏各自维护协议和注册元数据；通过统一 envelope、测试夹具和生成脚本消除重复。

### 3.2 被拒绝方案

- **单一 `games/dice` 大引擎 + mode 字段：** 会把三个规则状态机、配置和版本耦合到一个模块，破坏独立恢复、投影和主题边界，拒绝。
- **客户端或 Redis 主导骰点/状态：** 无法满足服务端裁决、断线恢复和审计要求，且会泄露吹牛骰子秘密，拒绝。

### 3.3 ADR

- **Decision：** 采用三顶层独立游戏模块，SDK 只承载通用骰子基础能力；平台协议使用 opaque game envelope，具体 payload 由游戏注册表解析。
- **Drivers：** 规则隔离、精确版本恢复、私密投影安全、移动端客户端独立演进。
- **Alternatives considered：** 单一骰子模式引擎、客户端权威或 Redis 权威。
- **Why chosen：** 独立模块满足设计规范和已有 boundarycheck，纯引擎便于属性测试与重放，opaque envelope 避免平台 `oneof` 无限增长。
- **Consequences：** 需要扩展 Buf/代码生成、运行时 registry、边界检查器和三套客户端模块；换来的好处是单款游戏可以独立回滚和灰度。
- **Follow-ups：** 三关定胜负和德州扑克沿用同一模块模板，不在本计划内实现。

## 4. 可测试验收标准

- 三个 `GameID` 均有有效 Manifest、独立默认版本、精确版本恢复和签名/构建注册记录；缺失旧版本时会话进入 `suspended`，不会回退到新版本。
- 三款规则引擎均能从固定初始状态、配置、随机种子和命令重放出字节一致的 snapshot、event 和 timer；fuzz 输入不会 panic、越权或产生非法状态。
- 吹牛骰子覆盖上不封顶叫数、斋/飞斋换算、万能开关、开骰、超时认输、ParticipantRevoked 和玩家/观众/复盘秘密隔离。
- 789 覆盖 7/8/9、特殊对子优先级、两人反转例外、叠杯、奇数半池、最后不足步长加注、掉骰 `result_pending`、强制模式超时和撤销时已提交效果保留。
- 喜相逢覆盖四类牌型、顺子开关、百搭、`235` 全豹子/存在非豹子两种情境、`fullKey/tieKey`、解析批次上限、靶子累计重摇上限、换靶和公开投影。
- GameSession 创建通过真实 `RoomGameSessionRepository.Start` 同事务完成 PartyRoom + Session + participants + timers + event batch + outbox；任一写入失败全部回滚。
- 正常结束通过 `RoomGameSessionRepository.Finish` 同事务提交游戏终态和 PartyRoom 赛后状态；成员移除通过 durable room outbox + runtime inbox 最终可靠触发 ParticipantRevoked，重复 source event 不会重复罚酒。
- 房间移除事务提交后立即形成 participant action fence；即使 ParticipantRevoked inbox 尚未消费，被移除玩家的 GameSession 动作也必须在数据库提交前拒绝。
- 动作重试按 `(session, actor, action_id, request_digest)` 幂等；不同 digest 冲突；旧 ownership epoch、旧 state version 和已撤销玩家不能写入新版本。
- timer/system 使用独立 cause 和持久幂等键，不伪造 actor action；房主起始座位来自可信 PartyRoom 快照，不接受客户端 config 覆盖。
- WebSocket 订阅始终按 viewer 重新生成投影；吹牛骰子对手秘密、未公开骰子、随机种子和权威快照不会进入客户端或日志。
- 小差距补发只发送 module 生成的 viewer-safe delta；任何无法安全投影的历史差距回退到最新 viewer-scoped snapshot，raw events 永不下发。
- 移动端 `390x844` 竖屏完整可玩，`844x390` 横屏自适应，操作托盘可收起/展开，危险动作有确认，旋转和重连不重置局面。
- Playwright 覆盖身份后进入房间、房主开局、三款游戏完整回合、观战、断线恢复、赛后房主重新开放进房许可；axe-core、Vitest、Go race、fuzz、Buf 和 boundarycheck 全部通过。
- 性能验收达到平台目标：国内同区域动作确认 p95 < 250ms（不含表现动画），恢复到最新可见状态 < 5 秒；1,000 在线玩家压测有结果记录。

## 5. 文件责任图

| 路径 | 责任 |
| --- | --- |
| `sdk/go/game/dice/**` | 确定性骰点、无浮点 ticks、稳定座位顺序、通用边界和纯测试夹具 |
| `sdk/go/game/contract.go` | 补充可信开局上下文、system 命令和 viewer-safe delta 投影契约 |
| `sdk/ts/game-client/**` | `GameView`、`AllowedActions`、dispatch、重连和主题端口 |
| `games/*/proto/**` | 各游戏配置、命令、事件、状态和投影协议；禁止平台 oneof |
| `games/*/engine/**` | 纯规则状态机和不变量，不导入 time、rand、网络、数据库或平台 |
| `games/*/projection/**` | 玩家、观众、复盘投影和秘密字段扫描 |
| `games/*/module/**` | SDK `ServerGameModule` 适配、protobuf codec、timer envelope、manifest |
| `games/*/client/**` | Vue 游戏桌面、共同桌面、私密区域和操作托盘 |
| `games/*/themes/**` | 版本化主题 token、资源清单、回退主题和视觉夹具 |
| `packages/game-ui-kit/**` | 座位层、共同桌面、安全区、折叠托盘、方向布局和无障碍原语 |
| `packages/theme-system/**` | 主题加载、哈希校验、回退、动态切换和资源隔离 |
| `platform/game-runtime/service.go` | registry 解析、创建/动作/timer/system 编排、viewer 投影和 request digest |
| `platform/game-runtime/system_inbox.go` | ParticipantRevoked 等外部 system 事件的持久幂等消费和结果记录 |
| `platform/game-runtime/repository.go` | 扩展 timer/system commit、事件读取和复盘查询端口 |
| `platform/persistence/postgres/**` | runtime 新端口的事务 CAS、事件读取、viewer/replay 索引和 outbox |
| `platform/room/**` | 通过运行时 start port 接入三款游戏，保留房间/游戏边界和赛后开放许可 |
| `contracts/platform/game/v1/**` | Connect/实时通用 envelope、动作回执、订阅游标和稳定错误 |
| `apps/api/**` | GameService Connect RPC、认证、错误、no-store 和依赖装配 |
| `apps/realtime/**` | WebSocket 会话、Redis 非权威 fanout、订阅、重连、draining |
| `apps/web/**` | 发现、持续房间、游戏路由、PWA、旋转、安全区和断线恢复 |
| `tooling/scripts/generate-games.*` | 每款游戏 proto/TS/Go 生成、registry 检查和零漂移门禁 |
| `tooling/boundarycheck/**` | 放行各游戏自己的 generated/module 包和 SDK 通用 dice 包，继续拒绝跨游戏和平台越界 |

## 6. 逐步实施

### Task 0：执行交接基线

**Files:** 无文件改动。

- [ ] 确认计划已独立提交，工作树干净，当前分支没有未认领的用户改动。
- [ ] 运行 `go test ./sdk/go/game ./platform/game-runtime ./platform/room` 和 `go test ./platform/persistence/postgres/...`（真实 PostgreSQL 按环境门禁执行）。
- [ ] 确认 `buf --version`、`pnpm --version`、`go version`；缺少工具时按仓库规则请求安装，不自动安装。

**Verify:** `git status --short` 为空；基础包测试通过。

**Commit:** 无。

### Task 1：建立游戏模块拓扑与通用骰子 SDK

**Files:** `sdk/go/game/contract.go`、`sdk/go/game/dice/**`、`games/{liars-dice,dice-789,meet-by-chance}/{engine,projection,module,proto,tests}/`、`tooling/boundarycheck/**`、`pnpm-workspace.yaml`、`games/README.md`。

- [ ] 先写确定性骰点测试：固定 32-byte seed + ordinal 产生字节一致的 1-6，拒绝零/短 seed、越界骰面和超过配置数量；使用无偏 rejection 逻辑，不导入 `math/rand`、`crypto/rand` 或 `time`。
- [ ] 实现 `PenaltyTicks`/容量算术、`uint32` 溢出检查、稳定座位顺/逆时针寻址和跨模块测试夹具；所有规则使用整数 ticks。
- [ ] 扩展服务端 SDK：`CreateRequest` 增加运行时生成的 `SessionStartContext{HostUserID, StartingSeat}` 并验证属于冻结 participants；增加带 `system_operation_id`、expected version 和 opaque message 的 `SystemRequest`/`HandleSystem`；增加只返回 viewer-safe 消息的 `ProjectEvents` delta 契约。ParticipantRevoked/host finish 进入模块 system handler，缺失模块导致的 suspend 和管理员强制 cancel 保持运行时原生状态转换。
- [ ] 为每款游戏创建顶层模块目录和空的责任 README，不放业务占位实现；补齐引擎/模块/客户端/主题的依赖方向。
- [ ] 更新 boundarycheck：引擎只允许自身 `engine` 和 `sdk/go/game`（含 `sdk/go/game/dice`）；protobuf codec 留在 `module`，不得进入纯引擎；客户端只允许自身目录、`sdk/ts/game-client`、`packages/game-ui-kit`、自身生成协议；主题继续禁止引擎/投影/权威状态依赖。
- [ ] 扩展 pnpm workspace 与静态检查，使三款 `client`/`themes` 被发现；不得把模块合并为 `games/dice` 单包。

**Verify:** `go test ./sdk/go/game/... ./tooling/boundarycheck/...`；边界夹具验证跨游戏、`platform -> games`、引擎 IO 和客户端跨模块导入均失败。

**Commit:** `feat(game): 建立三款骰子模块拓扑与通用骰子 SDK`

### Task 2：冻结游戏协议、编解码和 Buf 生成链

**Files:** `contracts/platform/game/v1/**`、`games/*/proto/**`、`games/*/module/codec.go`、`tooling/scripts/generate-games.*`、`buf.yaml`、`buf.gen.yaml`、generated outputs、`contracts/README.md`。

- [ ] 定义平台通用 envelope：`session_id`、`game_id`、精确 version tuple、`action_id`、`expected_version`、schema version、message type、payload、request digest 和 stable error。
- [ ] 定义 GameSession 创建配置、动作确认、timer/system 命令、ParticipantRevoked、finish、订阅游标和 viewer projection envelope；平台不出现三款游戏的 `oneof`。
- [ ] `SessionStartContext` 由服务端从 PartyRoom 冻结快照生成，客户端 game config 不得覆盖 host/starting seat；测试房主不是 0 号座位和房主失活后的最小活跃座位 fallback。
- [ ] system envelope 固定 `system_operation_id/source_event_id`、source kind、expected version 和 request digest；相同 source+digest 幂等，不同 digest 冲突，乱序 system 通过 expected version 拒绝或重算。
- [ ] viewer delta 只允许 module `ProjectEvents` 输出；raw EventBatch、权威 snapshot 和未经过 viewer 投影的 payload 不得进入 API/WebSocket。delta 无法安全投影或差距过大时返回最新 viewer-scoped snapshot。
- [ ] 为三款游戏分别定义冻结 config/state/command/event/view/projection message；字段必须覆盖各规则规范的配置边界、秘密边界和复盘字段。
- [ ] 选择可重复的 per-game Buf generation：每个 `games/<id>/proto` 生成自己的 Go/TS 代码，生成文件不手改；根 `buf` lint/breaking 和脚本零漂移检查覆盖所有游戏协议。
- [ ] module codec 使用 deterministic protobuf marshal，拒绝未知 schema、payload 超过 1 MiB、非法 enum、非法 ticks、malformed action 和跨 game message type。

**Verify:** `buf format --diff --exit-code`、`buf lint`、`pnpm run generate` 后 `git diff --exit-code`；每款 codec 的 round-trip、unknown field、malformed payload 和 digest tests 通过。

**Commit:** `feat(protocol): 定义游戏 envelope 与三款骰子协议`

### Task 3：实现 GameSession 运行时服务与原子开局

**Files:** `platform/game-runtime/service.go`、`platform/game-runtime/repository.go`、`platform/game-runtime/service_test.go`、`platform/persistence/postgres/game_session_repository.go`、`platform/persistence/postgres/room_game_session_repository.go`、runtime migrations/queries、`platform/room` start/finish ports and tests。

- [ ] 先用 fake registry/module/store 写创建、动作、timer、system、finish、replay 和错误矩阵测试；测试 actor、viewer、version、epoch、digest、terminal 和配置冻结。
- [ ] 实现 runtime service：精确 registry resolve；创建时从已授权 PartyRoom 快照生成可信 `SessionStartContext`，调用 module.Create、生成确定性上下文、构造 `gameruntime.NewSession` 和 `CreationCommit`；通过 `RoomGameSessionRepository.Start` 同事务写房间与会话。
- [ ] 为领域模型新增 `ApplyTimer`、`ApplySystem`、`Suspend`、`Cancel` 和 host finish 的合法状态转换，并扩展 `Store`/PostgreSQL adapter 的 `CommitTimer`/`CommitSystem`、事件读取和 replay 输入；timer/system batch 保持 actor/action ID 为空，不能伪装成玩家动作。外部 system 以 `(session_id, system_operation_id/source_event_id)` + digest 持久去重并保留原 result/version。
- [ ] 实现 action service：认证 actor 后检查当前会话/ownership/receipt，显式转换 SDK `ActionID` 与平台 `OperationID`；以 actor、session、expected version、game/version tuple、message type/schema/payload 生成 canonical request digest，调用 module.HandleCommand，应用 Transition，提交 receipt/outbox，并在提交后生成 viewer projection。
- [ ] `CommitAction` 的 PostgreSQL 事务按统一锁顺序先锁 matching PartyRoom、校验 actor 当前仍是该房间 participant 且 active session 匹配，再锁/更新 GameSession；移除/封禁事务提交后，即使 revoke inbox 未消费，旧玩家动作也必须 fail closed。测试动作与移除并发时只有符合提交顺序的一方生效，禁止 check-then-commit 窗口。
- [ ] 实现 timer service：由持久 timer 的 expected version 唤醒，调用 module.HandleTimer；超时不能用本地重置时间，重复 timer 必须幂等。
- [ ] 实现平台 system command：ParticipantRevoked/host finish 调用 module.HandleSystem；重复、乱序、旧 epoch 和不同 digest 全部测试。模块缺失时 session suspend；管理员强制 cancellation 为运行时终态，不能伪造正常胜负事件；游戏状态冻结配置不可更新。
- [ ] `session.finish` 是平台生命周期动作：module 保存初始 host/starting seat 只用于首轮规则，当前 PartyRoom host 可能转移；API/runtime 在 viewer projection 外层按当前房间权限附加或移除 finish action，不能依赖游戏 state 中的旧 host 身份授权。
- [ ] 为正常结束新增 `RoomGameSessionRepository.Finish`：锁定 matching room/session，把 module finish transition/event/outbox 和 PartyRoom active pointer 清理同事务提交；任一写点失败全部回滚。Task 3 只提供 runtime/room ports、PostgreSQL adapter 和 fake/integration tests，生产仍保留 `NewDisabledGameCatalog()` fail closed，直到 Task 9 注册真实模块。

**Verify:** 真实 PostgreSQL 集成测试覆盖 start/finish 每个写点 rollback、重复 action/system、不同 digest、ownership fencing、timer CAS、revoke、suspend/cancel、可信起始座位和精确版本缺失；`go test -race ./platform/game-runtime/... ./platform/room/... ./platform/persistence/postgres/...`。

**Commit:** `feat(game): 接入权威会话运行时与原子开局`

### Task 4：实现 API/GameService 与实时网关

**Files:** `contracts/platform/game/v1/**` service definitions、`apps/api/internal/transport/game/**`、`apps/api/internal/server/surface.go`、`apps/api/internal/transport/{sensitive,metrics,errors}/**`、`apps/realtime/**`、Redis adapter、`infra/nginx/**`。

- [ ] Connect API 提供创建/动作/当前投影/复盘访问/房主 finish 和订阅握手；所有写操作要求设备会话、CSRF、Origin、expected version 和 action ID。
- [ ] WebSocket 使用 Protobuf binary envelope，握手后按身份、房间角色和 viewer kind 建立订阅；服务端只推送投影和动作回执，不发送权威 snapshot。
- [ ] Redis 只做非权威 fanout、在线状态和 ownership lease；PostgreSQL commit 成功后才 publish，Redis 丢失时停止新租约并按安全策略暂停/恢复。
- [ ] 实现订阅游标：小差距只通过 module `ProjectEvents(currentSnapshot, versionedEvents, viewer)` 生成 viewer-safe delta；大差距、角色变化或模块拒绝 delta 时返回最新 viewer-scoped snapshot。不同玩家/观众、开骰前后和历史游标不得共享缓存；raw event 永不下发。
- [x] 实现断线恢复、客户端 pending action、draining 和旧连接关闭；不同 viewer 的 snapshot/delta 必须独立生成。
- [ ] 连接 `UserSurface`/`AdminSurface`、敏感 operation registry、no-store、稳定错误码和 metrics；Nginx 路由新增 game API/WebSocket path 并覆盖转发头。

**Verify:** `httptest`/WebSocket integration 覆盖未认证、跨房间、观众、断线、重复 action、Redis publish 丢失、旧租约和 projection leak；`go test -race ./apps/api/... ./apps/realtime/...`。

**Commit:** `feat(realtime): 提供游戏 API 与 WebSocket 实时网关`

### Task 5：建立客户端 SDK、共同桌面和主题系统

**Files:** `sdk/ts/game-client/**`、`packages/game-ui-kit/**`、`packages/theme-system/**`、`packages/test-kit/**`、`apps/web/**` 基础 shell、pnpm manifests。

- [ ] 定义 `GameView`、`AllowedActions`、`dispatch`、stateVersion、pending/retry、projection envelope、viewer role 和 reconnect adapter；禁止导入 Go authoritative state。
- [ ] 实现共同桌面原语：移动端 portrait-first 共同桌面、稳定座位层、圆桌/椭圆布局、私密区域、连接状态、安全区、可滑动折叠操作托盘和危险动作确认。
- [ ] 实现主题加载、版本 pin、内容哈希校验、fallback、动态 token 和资源清单；主题不能控制动作合法性或读取隐藏状态。
- [ ] 创建 `apps/web` PWA shell、路由、设备身份/房间上下文恢复和游戏 session route；实现横竖屏 responsive，不强制依赖浏览器 orientation lock。
- [ ] 提供 Storybook/视觉夹具或等价固定视口夹具，覆盖 `390x844`、`844x390` 和小屏安卓尺寸。

**Verify:** `pnpm run check`、Vitest、axe-core 和 Playwright shell tests；旋转、折叠托盘、焦点顺序、pending 防重复点击、主题失败回退通过。

**Commit:** `feat(web): 建立游戏客户端 SDK 与移动端共同桌面`

### Task 6：实现吹牛骰子完整服务端模块

**Files:** `games/liars-dice/{proto,engine,projection,module,tests}/**`、registry entry、generated outputs。

- [ ] 先写配置/状态构造、每人 3-6 骰、2-8 人、万能/斋/飞斋开关、首叫下限、罚酒 ticks、timer 和 `uint32` 叫数边界测试。
- [ ] 实现叫数比较：同模式数量/点数严格递增；斋→飞 `2n`、飞→斋 `floor(n/2)+1`；叫数超过总骰数合法；转换乘法溢出和 `face=1` 飞斋拒绝。
- [ ] 实现固定 seed 生成私密骰子、`round.bid`、`round.open`、恰好命中/不足/超出、输家首发、超时认输和 ParticipantRevoked。
- [ ] 实现 deterministic snapshot/events/timers 和 module codec；所有私密骰子只进入本人 projection，开骰后才进入公开事件/复盘。
- [ ] 实现 player/spectator/replay projection、合法动作集合、schema migration 和 fuzz/property tests：叫数序列不回退、无飞斋循环、每轮唯一输家、无秘密泄漏。

**Verify:** `go test -race ./games/liars-dice/...`、`go test -fuzz=Fuzz -run=^$ ./games/liars-dice/...`、固定 seed replay 和 projection scanner。

**Commit:** `feat(game): 实现吹牛骰子服务端规则与投影`

### Task 7：实现 789 完整服务端模块

**Files:** `games/dice-789/{proto,engine,projection,module,tests}/**`、registry entry、generated outputs。

- [ ] 先写配置边界：2-12 人、initial/layer/add ticks、最大层数、sum match、特殊对子、后续模式、掉骰窗口和 timer。
- [ ] 实现 `result_pending`、掉骰/正常确认互斥、7 加注（包括不足一步时填满）、8 `ceil/floor` 半池、9 全池、叠杯跨层扣减。
- [ ] 实现特殊对子优先级：双 1 目标喝光接力、双 6 目标加注、双 4 半池强制重摇、普通对子方向反转和两人重摇。
- [ ] 实现 `awaiting_continue` optional/forced_reroll/forced_pass 及对应超时动作；来源撤销按效果应用前后区分，目标撤销返回选人；池和罚酒不能回滚已提交事件。
- [ ] 实现公开 player/spectator/replay projection、房主手动掉骰审计、事件重放和属性测试：池容量守恒、方向指向活跃玩家、优先级唯一命中。

**Verify:** `go test -race ./games/dice-789/...`、fuzz malformed payload/command、奇数 ticks、addStep=2 的余量场景、timer/revoke/replay matrix。

**Commit:** `feat(game): 实现 789 服务端规则与公共池`

### Task 8：实现喜相逢完整服务端模块

**Files:** `games/meet-by-chance/{proto,engine,projection,module,tests}/**`、registry entry、generated outputs。

- [ ] 先写牌型规范化和 `fullKey/tieKey` 测试：豹子、开启顺子、对子、单骰、百搭、稳定排序和 `235` sentinel。
- [ ] 实现 `235` 全场其他均豹子时克制、存在非豹子时最低单骰；多人局只做一次情境判定，禁止非传递两两比较。
- [ ] 实现完全同牌优先、粗粒度同大/同小批次、同小最弱额外罚、`matchResolutionCount` 上限、组拆分/合并和 cap 终态。
- [ ] 实现靶子首次罚 2 ticks、整轮累计重摇上限、换靶不重置、当前靶子展示计数、重摇后再次解析、stand/timeout 和 revoke。
- [ ] 所有骰子按 revealing 后公开；实现 player/spectator/replay projection、事件复盘、schema migration、属性/fuzz 和投影泄漏测试。

**Verify:** `go test -race ./games/meet-by-chance/...`、全豹子/非豹子 `235`、tieKey 跨牌型隔离、批次 cap、换靶循环防护和固定 seed replay。

**Commit:** `feat(game): 实现喜相逢服务端规则与靶子流程`

### Task 9：注册三款游戏并接入房间目录与开局配置

**Files:** `tooling/game-registry/**`、`apps/api/internal/application/application.go`、`platform/room/catalog.go`、`platform/room/service.go`、`contracts/platform/room/v1/room.proto`、generated outputs、`platform/persistence/postgres/room_game_session_repository.go` tests。

- [ ] 构建时生成三个模块的 Go registry、TS manifest index 和精确 version map；每个 game ID 恰有一个 new-session default。
- [ ] 冻结 Manifest：吹牛骰子 2-8 人、789 2-12 人、喜相逢 3-12 人；三者 timers/spectating/replay 均开启且 reveal 为 `rule_controlled`。竖屏优先和操作托盘偏好按各客户端冻结，默认/回退主题必须都在 variants 中。
- [ ] 只有在三款默认 module 和生成 registry 都可构造且启动自检通过后，才把 `RegisteredGameCatalog`/runtime start port 接入应用装配并移除 Disabled catalog；公开大厅从 Manifest 得到人数、方向、观战、复盘和主题元数据。
- [ ] 扩展 `StartGameRequest` 传递 opaque frozen game config；创建时 module.Validate/config codec 校验并把 config 写入初始 state，之后禁止修改。
- [ ] host finish 必须通过 PartyRoom 当前 host + matching active session 授权，调用 Task 3 的 `RoomGameSessionRepository.Finish` 原子提交游戏 finish transition 与房间赛后状态；普通参赛者不能利用游戏 payload 结束整局。
- [ ] 使用跨聚合 `RoomGameSessionRepository.Start`，覆盖房间版本冲突、session 版本 key、座位快照、创建事件、timer 和 outbox 同事务。
- [ ] 实现单局结束后 PartyRoom 赛后大厅：现有成员保留；新玩家只有房主重新开放后才能 participant/waiting；新玩家不能写入已结束 GameSession。

**Verify:** API/room/PostgreSQL integration 覆盖三 game IDs、非法 config、开局回滚、赛后开放/关闭、候场提升、精确版本恢复和公开大厅展示。

**Commit:** `feat(room): 接入三款游戏目录与原子开局配置`

### Task 10：实现吹牛骰子客户端与主题

**Files:** `games/liars-dice/client/**`、`games/liars-dice/themes/**`、`packages/game-ui-kit/**` fixtures、`apps/web` route wiring。

- [ ] 共同桌面展示围绕桌面的玩家座位，自己骰子在安全私密区，对手显示连接/叫数状态而不显示骰子；竖屏优先，横屏扩展桌面。
- [ ] 操作托盘提供数量、点数、斋/飞斋 segmented controls、开骰确认、倒计时、非法叫数禁用原因和提交 pending；超过总骰数合法但显示明确风险提示。
- [ ] 断线时保留未确认草稿但不自动重放；重连后按 server projection/stateVersion 恢复；复盘在开骰后显示完整叫数链和骰子。
- [ ] 主题 token/音效/动效可替换，不改变秘密字段、动作权限或规则说明；实现 reduced-motion 和无障碍标签。

**Verify:** Playwright 竖/横屏、私密投影、开骰确认、超时、断线恢复、复盘和 axe-core；固定截图夹具无秘密泄漏。

**Commit:** `feat(web): 接入吹牛骰子移动端桌面`

### Task 11：实现 789 客户端与主题

**Files:** `games/dice-789/client/**`、`games/dice-789/themes/**`、`apps/web` route wiring。

- [ ] 共同桌面显示公开当前骰点、方向箭头、公共池/叠杯层、当前操作者和效果状态；操作托盘按阶段只展示 roll/add/target/reroll/pass。
- [ ] 7 加注使用 step/remaining 约束控件；8/9 半池/全池效果用明确可撤销前确认；掉骰只给房主显示二次确认，结果待确认阶段不可误触其他动作。
- [ ] 双 1/双 6 目标选择、双 4 强制重摇、两人对子例外、forced timeout 和公共池动画不能改变服务端状态。
- [ ] 叠杯主题、容量上限、非酒精替代显示、复盘和观战投影全部来自 config/view，不在客户端推导规则。

**Verify:** Playwright 覆盖所有优先级、奇数 ticks、addStep 余量、目标撤销、掉骰窗口、旋转和重连；axe-core 和视觉夹具通过。

**Commit:** `feat(web): 接入 789 移动端桌面`

### Task 12：实现喜相逢客户端与主题

**Files:** `games/meet-by-chance/client/**`、`games/meet-by-chance/themes/**`、`apps/web` route wiring。

- [ ] 共同桌面同时展示所有公开骰子/牌型；将靶子视觉固定在座位环内，避免控制区与桌面割裂；靶子操作区贴近靶子但不遮挡桌面。
- [ ] 同牌/同大/同小批次显示共同重摇、额外最弱罚酒、解析上限和靶子换位；没有“挑战”按钮。
- [ ] `235` 情境说明、顺子开关、百搭状态、当前靶子 reroll/stand、剩余本轮上限和 timeout 由服务端 view 驱动。
- [ ] 主题资产、骰子/牌型动效、横竖屏布局、reduced-motion、观战和复盘统一走主题系统。

**Verify:** Playwright 覆盖全豹子/非豹子 `235`、tieKey 同大/同小、靶子换位/上限、观战/复盘、旋转和 axe-core。

**Commit:** `feat(web): 接入喜相逢移动端桌面`

### Task 13：持续房间、观战、复盘与赛后再入场闭环

**Files:** `apps/web` room/game routes、`apps/api/internal/transport/game/**`、`apps/realtime/**`、`platform/replay/**`、`platform/room/**`、`platform/game-runtime/system_inbox.go`、PostgreSQL outbox/inbox migrations and adapters。

- [x] 房间页区分 lobby/playing/post-game；playing 时 participant admission 强制 closed，旧参赛者可重连，新用户只能观战/候场；一局结束后房主可直接开放/需批准/保持关闭。
- [x] 观众 projection 不含私密骰子；复盘只通过 `ProjectReplay` 和授权策略读取，未结算当前轮不生成复盘；资源地址不能绕过授权。
- [x] 房主/管理员移除参赛者后，runtime 发送 `ParticipantRevoked`；座位本局不交给新玩家；审计/复盘摘要记录原因和规则影响。
- [x] 成员移除/封禁的房间事务同时写 durable `room.participant.revoked.v1` outbox，事件 ID 作为 runtime `source_event_id`；runtime inbox 以 `(session_id, source_event_id, digest)` 幂等消费并重试 `HandleSystem(ParticipantRevoked)`。房间权限立即撤销，游戏规则效果最终可靠提交；进程在任一写点退出不能丢撤销或重复罚酒。
- [x] 移除提交与 action commit 使用 Task 3 的统一 PartyRoom→GameSession 锁顺序和 participant fence；测试“移除已提交、inbox 未消费”窗口中的旧玩家动作必定拒绝，同时其他未撤销参赛者仍可正常行动。
- [x] 公开大厅状态卡片、房间码/邀请深链、身份完成后回房、赛后重新加入/候场/晋升和 roomVersion/membershipVersion 冲突全部闭环；游戏路由使用一次性票据建立 Protobuf WebSocket，并按 viewer cursor 自动恢复。

**Verify:** Playwright 从邀请深链开始覆盖身份恢复、观战、赛后开放、候场提升、踢人/封禁、复盘授权和跨 session 房间连续使用；PostgreSQL 故障/并发注入覆盖 finish 原子回滚、remove-vs-action 锁序、移除后即时 fence、revoke outbox 提交后崩溃、inbox 重投、不同 digest 冲突和最终收敛。

**Commit:** `feat(room): 完成游戏观战复盘与赛后再入场`

### Task 14：完整质量门禁、压测和发布准备

**Files:** `.github/workflows/ci.yml`、`tooling/**`、`docs/operations/**`、`infra/monitoring/**`、`apps/realtime` load fixtures、game tests。

- [x] Go：`go test -race ./...`、原生 fuzz、`go vet ./...`、确定性重放、snapshot migration、timer recovery、projection secret scan。
- [x] Protocol/生成：`buf format/lint/breaking`、所有 games proto 生成零漂移、module registry 与 version artifact 覆盖检查。
- [x] TypeScript/Vue：`pnpm run check`、Vitest、Playwright 固定视口、axe-core、主题 fallback/reduced-motion、客户端 bundle 版本 pin。
- [x] Integration：真实 PostgreSQL/Redis 测试跨聚合 start、action receipt、ownership epoch、Redis publish loss、恢复、replay 和 outbox；不允许把必需依赖记为 skip。
- [ ] 负载与故障：1,000 在线玩家、热点观战房、WebSocket reconnect、lease 转移、蓝绿 draining、PostgreSQL/Redis 故障、对象存储主题回退；记录 p95 和恢复目标。
- [x] 更新运维文档：独立 DSN/Redis、migration、registry artifact 保留、旧版本清理、主题资源哈希、秘密不进入日志/配置提交；运行 `git diff --check` 和 `git status --short`。

**Verify:** 完整 CI matrix 全绿，固定输出证据归档，工作树干净；任何已知失败继续修复，不能以“已知失败”收尾。

**Commit:** `ci(game): 完成三款骰子端到端质量门禁`

## 7. 并行边界与执行交接

执行顺序：`Task 0 -> Task 1 -> Task 2 -> Task 3 -> Task 4/5`；Task 6/7/8 在 Task 1-2 完成后可由三个独立负责人并行；Task 9 依赖三款 module manifest；Task 10/11/12 分别依赖对应协议和 Task 5；Task 13 依赖 Task 3-12；Task 14 最后执行。

共享文件规则：`buf.yaml`、生成脚本、`tooling/boundarycheck`、`sdk/go/game`、`sdk/ts/game-client`、`packages/game-ui-kit`、`platform/game-runtime`、根 package manifest 和 registry 只能由单一负责人串行修改；三款游戏目录内部可以并行。每个任务提交前必须检查 `git status --short`、`git diff --staged --name-only`、`git diff --staged --check` 和 staged patch，只提交当前任务文件。

推荐执行角色：

- 运行时/API：`executor`（高推理），负责 Task 3、4、9、13；`security-reviewer` 复核权限、ownership、投影。
- 吹牛骰子：`executor`（高推理）+ `test-engineer`，负责 Task 6/10。
- 789：`executor`（高推理）+ `test-engineer`，负责 Task 7/11。
- 喜相逢：`executor`（高推理）+ `test-engineer`，负责 Task 8/12。
- 客户端基础：`designer` + `executor`，负责 Task 5；视觉阶段使用 `visual-verdict`。
- 最终验收：`verifier` + `performance-reviewer` + `security-reviewer`，负责 Task 14。

任何实现过程中如果必须改变已确认规则、真实饮酒数据边界、私密投影、版本恢复、房间赛后许可或数据库权威语义，先停在该任务，修改对应规范并重新审查；普通内部文件拆分、错误码命名和组件布局不需要重新确认。

## 8. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 三款游戏并行修改共享协议/SDK 造成生成或边界冲突 | Task 1-2 串行冻结 envelope、SDK 和生成脚本；共享文件单一负责人；每个游戏只拥有自身 proto/module/client |
| 规则引擎因时间/随机/Protobuf 泄漏而不可重放 | engine 禁止 IO/time/rand；module 注入固定 context；deterministic marshal 和 seed replay 作为 CI 门禁 |
| 私密骰子经实时广播、复盘或日志泄漏 | projection 只由 module 生成；WebSocket 只推 view；secret scanner、viewer matrix 和日志 redaction 测试强制执行 |
| PartyRoom 与 GameSession 半成功 | 只通过 `RoomGameSessionRepository.Start`，禁止恢复单独 UpdateCAS 开局；真实数据库故障测试覆盖每个写点 |
| 789/喜相逢自动重摇循环 | 789 结果待确认和配置硬边界；喜相逢解析批次/靶子均为整轮累计上限；属性测试证明终止 |
| 房主撤销/Redis 丢失造成双主或非法动作 | PostgreSQL epoch fencing 为权威；Redis 仅 lease/fanout；失租约旧实例拒绝 commit |
| 移动端操作区遮挡桌面或误触 | 共同 UI kit 统一安全区/折叠托盘；高风险动作确认；固定视口 Playwright + 视觉审查 |
| 规则完成但客户端/协议未接通 | 每款游戏必须同时通过 module、codec、projection、client、e2e 验收；Task 9/13 不接受只注册 Manifest 的假完成 |

## 9. 完成定义

计划只有在三款游戏所有规则规范条款有真实代码和测试证据、实时投影与重连安全、赛后重新开放进房闭环、移动端横竖屏和主题回退通过、全量 CI/压测/故障验证完成、工作树干净且无已知错误时才算完成。单独完成任一引擎不代表平台完成，也不代表其他两款进入 MVP 取舍。
