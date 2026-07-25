# 系统运维、运营概览与最终集成实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付可验证的服务/依赖健康、积压、维护模式和固定维护命令，提供使用真实指标、趋势和异常摘要的运营概览，并完成六模块导航、旧后台删除与全仓验收。

**Architecture:** PostgreSQL 保存维护状态、服务心跳、指标 bucket 和维护命令回执，Redis 只承载 realtime presence 与可重建的管理投影缓存。API 聚合有界健康探针、持久任务积压和 realtime presence，所有响应携带统计窗口、采样时间与新鲜度；用户写请求在 API procedure policy 和 realtime action 入口读取同一维护状态并 fail closed。缓存刷新与失败任务重试使用固定枚举、预览、版本 CAS、`operations.maintenance` elevation 和签名审计，不提供任意键、Shell 或进程控制。

**Tech Stack:** Go、Connect RPC、Protocol Buffers、PostgreSQL/sqlc、Redis、签名审计、worker runtime、Vue 3、Pinia、Naive UI、Lucide、Vitest、Playwright。

---

## Task 1: 定义运维与概览契约

**Files:**
- Create: `contracts/platform/admin/v1/admin_operations.proto`
- Create: `contracts/platform/admin/v1/admin_overview.proto`
- Modify: `contracts/platform/admin/v1/admin_common.proto`
- Modify: `contracts/platform/common/v1/error.proto`
- Regenerate: `contracts/gen/go/platform/admin/v1/**`
- Regenerate: `contracts/gen/ts/platform/admin/v1/**`
- Regenerate: `contracts/gen/go/platform/common/v1/**`
- Regenerate: `contracts/gen/ts/platform/common/v1/**`
- Modify: `apps/api/internal/server/surface_test.go`
- Modify: `apps/api/internal/transport/adminauth/policy_test.go`

- [ ] **Step 1: 写失败的服务 surface 与 procedure policy 测试**

断言 AdminOperationsService 与 AdminOverviewService 只存在于 admin Host。概览要求 `overview.read`；运维查询要求 `operations.read`；维护模式预览/应用、缓存刷新预览/应用和失败任务重试预览/应用同时要求 `operations.maintain` 与 `operations.maintenance` elevation。未知 procedure 默认拒绝。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/api/internal/server ./apps/api/internal/transport/adminauth
```

- [ ] **Step 3: 定义 AdminOperationsService wire model**

契约至少包含：

```text
GetOperationsSnapshot
GetMaintenanceState
PreviewMaintenanceChange
ApplyMaintenanceChange
PreviewCacheRefresh
ApplyCacheRefresh
PreviewTaskRetry
ApplyTaskRetry
```

运维快照包含服务实例/版本/启动时间/最后心跳、依赖健康、realtime peer、限流策略名称与配置版本、队列积压、维护状态和刷新时间。健康状态只使用固定枚举 `healthy/degraded/unavailable/stale`，不得携带 DSN、对象键、Redis key、内部错误或密钥版本材料。

维护影响范围当前只定义 `USER_MUTATIONS`；缓存 namespace 当前只定义 `ADMIN_OVERVIEW_PROJECTION`、`ADMIN_OPERATIONS_PROBES` 和 `REALTIME_PRESENCE_PROJECTION`；可重试任务只定义 `USER_BATCH`、`USER_ERASURE`、`USER_EXPORT` 和 `AUDIT_EXPORT`。这些枚举不是任意字符串入口。

- [ ] **Step 4: 定义 AdminOverviewService wire model**

`GetOverview` 请求接受有界时间窗口和 `hour/day` granularity。响应返回当前在线用户、活跃房间、进行中牌局、最近 24 小时新增/封禁/解封/异常终止/紧急修正、活跃趋势、异常房间/牌局摘要、依赖健康摘要、最近高危操作和失败后台任务。每组数据都包含 `window_start/window_end/sampled_at/fresh_until` 或明确的 unavailable reason enum。

- [ ] **Step 5: 生成、验证并提交**

```powershell
pnpm generate:contracts
pnpm check:generated
go test ./apps/api/internal/server ./apps/api/internal/transport/adminauth
git add contracts apps/api/internal/server/surface_test.go apps/api/internal/transport/adminauth/policy_test.go
git commit -m "feat(admin-ops): 定义运维与运营概览契约"
```

## Task 2: 建立运维状态、心跳和指标持久模型

**Files:**
- Create: `infra/migrations/00032_admin_operations.sql`
- Create: `tooling/sqlc/queries/admin_operations.sql`
- Create: `tooling/sqlc/queries/admin_overview.sql`
- Regenerate: `platform/persistence/postgres/sqlcgen/admin_operations.sql.go`
- Regenerate: `platform/persistence/postgres/sqlcgen/admin_overview.sql.go`
- Regenerate: `platform/persistence/postgres/sqlcgen/models.go`
- Regenerate: `platform/persistence/postgres/sqlcgen/querier.go`
- Create: `platform/admin/operations/model.go`
- Create: `platform/admin/operations/repository.go`
- Create: `platform/admin/operations/errors.go`
- Create: `platform/persistence/postgres/admin_operations_repository.go`
- Create: `platform/persistence/postgres/admin_operations_repository_integration_test.go`
- Create: `platform/persistence/postgres/admin_overview_query_integration_test.go`

- [ ] **Step 1: 写失败的 migration/repository 集成测试**

覆盖维护单例初始化、maintenance version CAS、计划结束时间约束、实例心跳 upsert/过期、metric bucket 唯一键、重试回执幂等、积压摘要、概览时间边界和稳定异常摘要上限。测试证明迁移不删除用户、房间、牌局、审计或前四阶段后台数据。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/persistence/postgres -run "AdminOperations|AdminOverview"
```

- [ ] **Step 3: 编写 `00032_admin_operations.sql`**

创建并约束：

```text
maintenance_state
admin_service_instances
admin_metric_buckets
admin_cache_generations
admin_operations_retry_receipts
```

`maintenance_state` 是固定主键单例，保存 enabled、scope、reason、planned_end_at、version、changed_by 和 changed_at。`admin_service_instances` 以 service kind + instance ID 为键，保存 build version、started_at、last_heartbeat_at、bounded status 和当前 maintenance version；超过固定 TTL 的记录只显示 stale，由清理任务延迟删除。

`admin_metric_buckets` 使用 metric name + bucket width + bucket start 唯一键，保存 value、sampled_at 和 source watermark；只接受固定 metric 名称与 `hour/day` bucket。`admin_cache_generations` 为三个固定 projection 保存单调 generation，使刷新命令、签名审计和 outbox 可以在同一 PostgreSQL 事务提交；重试回执绑定 operation ID、任务 kind/ID、request digest、expected version、结果和审计 ID，防止重放改变第二个任务。

- [ ] **Step 4: 编写有界 SQL 查询与 repository 映射**

积压查询在 SQL 中计算 pending/running/failed 数量和最老等待时间；outbox 使用 consumer sequence/next attempt，timer 使用 due time，用户 batch/erasure/export 和 audit export 使用各自状态列。概览查询只扫描明确时间范围、索引和 `LIMIT`，不在 Go 中下载全表聚合。

- [ ] **Step 5: 生成、测试并提交**

```powershell
pnpm generate:sql
pnpm check:generated
go test -race ./platform/admin/operations ./platform/persistence/postgres
git add infra/migrations/00032_admin_operations.sql tooling/sqlc/queries/admin_operations.sql tooling/sqlc/queries/admin_overview.sql platform/admin/operations platform/persistence/postgres
git commit -m "feat(admin-ops): 建立运维状态与指标持久模型"
```

## Task 3: 发布四类服务实例心跳与运行状态

**Files:**
- Create: `apps/internal/runtimeinfo/info.go`
- Create: `apps/internal/runtimeinfo/info_test.go`
- Create: `apps/internal/serviceheartbeat/reporter.go`
- Create: `apps/internal/serviceheartbeat/client.go`
- Create: `apps/internal/serviceheartbeat/reporter_test.go`
- Create: `apps/api/internal/transport/adminoperations/heartbeat.go`
- Create: `apps/api/internal/transport/adminoperations/heartbeat_test.go`
- Modify: `apps/api/internal/server/surface.go`
- Modify: `apps/api/internal/server/surface_test.go`
- Modify: `apps/api/internal/config/config.go`
- Modify: `apps/api/internal/config/config_test.go`
- Modify: `apps/api/internal/application/application.go`
- Modify: `apps/api/main.go`
- Modify: `apps/edge/internal/config/config.go`
- Modify: `apps/edge/internal/config/config_test.go`
- Modify: `apps/edge/internal/server/server.go`
- Modify: `apps/edge/internal/server/server_test.go`
- Modify: `apps/edge/main.go`
- Modify: `apps/realtime/internal/config/config.go`
- Modify: `apps/realtime/internal/config/config_test.go`
- Modify: `apps/realtime/internal/application/application.go`
- Modify: `apps/realtime/internal/application/application_test.go`
- Modify: `apps/realtime/main.go`
- Modify: `apps/worker/internal/config/config.go`
- Modify: `apps/worker/internal/config/config_test.go`
- Modify: `apps/worker/internal/runtime/runtime.go`
- Modify: `apps/worker/internal/runtime/runtime_test.go`
- Modify: `apps/worker/internal/application/application.go`
- Modify: `apps/worker/main.go`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/.env.example`

- [ ] **Step 1: 写失败的 runtime info 与心跳认证测试**

覆盖 api/edge/realtime/worker 固定 service kind、合法 instance ID、build version fallback、进程级 started_at 不漂移、服务端 heartbeat 时间、重复 upsert 和超时 stale。内部 heartbeat endpoint 只接受精确路径、POST、固定长度 bearer credential 和有界 JSON；edge 明确拒绝把 `/internal/` 从公网代理到 API。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/internal/runtimeinfo ./apps/internal/serviceheartbeat ./apps/api/internal/transport/adminoperations ./apps/edge/internal/server ./apps/realtime/internal/application ./apps/worker/internal/runtime
```

- [ ] **Step 3: 实现共享 runtime info 与 reporter**

`runtimeinfo.Info` 在进程启动时一次性固化 kind、instance ID、build version 和 started_at。build version 优先使用构建注入值，再读取 Go build info，开发回退为 `development`；不得把 commit dirty diff、主机环境或完整进程参数发到后台。

API 直接写 heartbeat repository。edge、realtime 和 worker 使用内部 client 向 API 精确 heartbeat endpoint 报告；该 handler 作为 exact path 挂在现有 API listener、位于两个 Connect surface 之外，只接受内部 bearer credential，edge/Vite 明确不代理该 path。认证比较使用 constant-time。heartbeat interval、request timeout 和 stale threshold 有固定上限并说明单位。

- [ ] **Step 4: 接入各进程的 bounded status**

API 状态来自现有 readiness；edge 报告 API/realtime upstream 是否可达；realtime 报告 PostgreSQL、Redis、owner renewal、timer/fanout/revocation loop；worker 报告 PostgreSQL、checkpoint、admin job/export pass 的最近成功时间。只上传固定 component code 与状态，不上传原始 error 文本。

- [ ] **Step 5: 更新配置与部署清单**

为 api/edge 增加显式 instance ID，为四个服务配置内部 heartbeat URL/credential；开发默认只在 compose 网络生效，生产缺少 credential 时启动失败。关闭进程时停止 reporter 并执行一次有界 final heartbeat，不延长 shutdown 超时。

- [ ] **Step 6: 测试并提交**

```powershell
go test -race ./apps/internal/runtimeinfo ./apps/internal/serviceheartbeat ./apps/api/... ./apps/edge/... ./apps/realtime/... ./apps/worker/...
git add apps deploy
git commit -m "feat(admin-ops): 发布服务实例心跳与运行状态"
```

## Task 4: 聚合依赖健康、realtime presence 与任务积压

**Files:**
- Create: `platform/admin/operations/health.go`
- Create: `platform/admin/operations/backlog.go`
- Create: `platform/admin/operations/service.go`
- Create: `platform/admin/operations/service_test.go`
- Create: `platform/persistence/redis/admin_presence.go`
- Create: `platform/persistence/redis/admin_presence_test.go`
- Create: `platform/persistence/redis/admin_presence_integration_test.go`
- Modify: `platform/persistence/redis/coordination.go`
- Modify: `platform/persistence/redis/coordination_test.go`
- Modify: `platform/persistence/redis/rules.go`
- Modify: `apps/internal/checkpointstorage/readiness.go`
- Modify: `apps/internal/checkpointstorage/config_test.go`
- Modify: `apps/realtime/internal/subscription/hub.go`
- Modify: `apps/realtime/internal/subscription/hub_test.go`

- [ ] **Step 1: 写失败的健康与 presence 聚合测试**

覆盖 PostgreSQL、Redis、export result store、checkpoint Object Lock sink、checkpoint progress、realtime peer 和 worker heartbeat 的 healthy/degraded/unavailable/stale 映射；单个探针超时不拖住整个响应；错误内容不进入结果。presence 测试覆盖连接/断开/TTL、同一用户多设备去重、同一连接重复 heartbeat、实例崩溃后自然过期和 Redis 不可用时 unknown。

- [ ] **Step 2: 写失败的积压与限流摘要测试**

覆盖 outbox、realtime timer、用户 batch、擦除、用户导出和审计导出的 pending/running/failed、最老等待时间、采样时间与上限。限流只返回规则名称、固定配置 digest/version 和 Redis dependency 状态，不返回 HMAC digest、bucket key 或用户维度。

- [ ] **Step 3: 运行测试确认失败**

```powershell
go test ./platform/admin/operations ./platform/persistence/redis ./apps/internal/checkpointstorage ./apps/realtime/internal/subscription
```

- [ ] **Step 4: 实现 realtime presence 投影**

presence 使用独立 generation/version 和 TTL key，不复用或扫描 ticket、owner lease、fanout、rate-limit key。realtime hub 在认证连接建立、心跳和断开时更新投影；异常退出由 TTL 回收。管理查询通过有界 Redis pipeline 返回在线用户数和当前页用户 presence，不把 presence 当用户身份事实来源。

- [ ] **Step 5: 实现并发有界 health/backlog service**

每个依赖探针有独立 deadline，总请求有更短的整体 deadline；结果按固定 component 顺序返回。外部对象存储探针复用现有缓存 readiness，不因后台刷新形成高频 S3 请求。service heartbeat、依赖探针和 backlog 各自携带 sampled_at/fresh_until，不能用旧健康掩盖当前 unavailable。

- [ ] **Step 6: 集成测试并提交**

```powershell
go test -race ./platform/admin/operations ./platform/persistence/redis ./apps/internal/checkpointstorage ./apps/realtime/internal/subscription
git add platform/admin/operations platform/persistence/redis apps/internal/checkpointstorage apps/realtime/internal/subscription
git commit -m "feat(admin-ops): 聚合依赖健康与任务积压"
```

## Task 5: 实现维护模式和用户写请求拦截

**Files:**
- Create: `platform/admin/operations/maintenance.go`
- Create: `platform/admin/operations/maintenance_test.go`
- Modify: `platform/persistence/postgres/admin_operations_repository.go`
- Modify: `platform/persistence/postgres/admin_operations_repository_integration_test.go`
- Create: `apps/api/internal/transport/maintenance/interceptor.go`
- Create: `apps/api/internal/transport/maintenance/policy.go`
- Create: `apps/api/internal/transport/maintenance/interceptor_test.go`
- Create: `apps/api/internal/transport/maintenance/policy_test.go`
- Modify: `apps/api/internal/application/application.go`
- Modify: `apps/api/internal/transport/errors/mapper.go`
- Modify: `apps/api/internal/transport/errors/mapper_test.go`
- Modify: `platform/game-runtime/service.go`
- Modify: `platform/game-runtime/service_test.go`
- Modify: `apps/realtime/internal/transport/gamewebsocket/handler.go`
- Modify: `apps/realtime/internal/transport/gamewebsocket/handler_test.go`
- Modify: `apps/realtime/internal/application/application.go`
- Test: `apps/api/internal/application/integration_test.go`

- [ ] **Step 1: 写失败的 maintenance domain/CAS 测试**

覆盖启用/停用预览、reason、planned end、expected version、operation ID 幂等、过期计划不自动静默关闭、签名审计成功/拒绝/失败和审计不可用 fail closed。重复 operation 返回原结果；版本变化返回结构化 conflict。

- [ ] **Step 2: 写全部用户 procedure 分类测试**

从生成 descriptor 枚举 user surface 的所有 unary procedure，要求每个 procedure 唯一分类为 read、authentication recovery 或 mutation。maintenance 只拒绝 mutation；readyz、登录/会话恢复、纯读和 admin surface 继续可用；未知 procedure 默认按 mutation 拒绝。测试不得依赖方法名字符串猜测 HTTP verb。

- [ ] **Step 3: 写 realtime action gate 测试**

已建立 WebSocket 在维护启用后继续接收系统/状态消息，但用户 game action、房间 mutation 和新 admission 被稳定拒绝；owner 内部取消、timer、revocation、fanout 和优雅 shutdown 继续运行，避免维护模式阻止系统收敛。维护 repository 不可用时用户 mutation fail closed。

- [ ] **Step 4: 运行测试确认失败**

```powershell
go test ./platform/admin/operations ./apps/api/internal/transport/maintenance ./apps/api/internal/transport/errors ./platform/game-runtime ./apps/realtime/internal/transport/gamewebsocket ./apps/api/internal/application
```

- [ ] **Step 5: 实现权威 maintenance gate**

PostgreSQL 单例是写入权威。API mutation interceptor 与 realtime `MutationGate` 在执行领域命令前读取同一 versioned state；不使用可能长期 stale 的进程布尔值。若后续为性能增加短缓存，最大 stale 时间必须小于协议常量且缓存过期/读取失败拒绝 mutation，本任务不以 Redis availability 改写维护语义。

- [ ] **Step 6: 实现维护变更 service 与审计**

preview 返回当前/目标状态、影响范围、当前活跃房间/牌局和预计被拒请求类型；apply 要求 `operations.maintenance` elevation、reason、operation ID 和 expected version。审计记录 before/after、版本、planned end 与结果，不记录用户请求内容。

- [ ] **Step 7: 测试并提交**

```powershell
go test -race ./platform/admin/operations ./apps/api/internal/transport/maintenance ./apps/api/internal/transport/errors ./platform/game-runtime ./apps/realtime/internal/transport/gamewebsocket ./apps/api/internal/application
git add platform/admin/operations platform/persistence/postgres/admin_operations_repository.go platform/persistence/postgres/admin_operations_repository_integration_test.go apps/api platform/game-runtime apps/realtime
git commit -m "feat(admin-ops): 增加维护模式与用户写入门禁"
```

## Task 6: 实现固定缓存刷新和失败任务重试

**Files:**
- Create: `platform/admin/operations/commands.go`
- Create: `platform/admin/operations/commands_test.go`
- Create: `platform/persistence/redis/admin_cache.go`
- Create: `platform/persistence/redis/admin_cache_test.go`
- Create: `apps/worker/internal/admincache/dispatcher.go`
- Create: `apps/worker/internal/admincache/dispatcher_test.go`
- Modify: `platform/admin/user/batch.go`
- Modify: `platform/admin/user/erasure.go`
- Modify: `platform/admin/export/service.go`
- Modify: `platform/admin/audit/export.go`
- Modify: `platform/persistence/postgres/admin_operations_repository.go`
- Modify: `platform/persistence/postgres/admin_operations_repository_integration_test.go`
- Modify: `apps/worker/internal/adminjobs/dispatcher.go`
- Modify: `apps/worker/internal/adminjobs/dispatcher_test.go`
- Modify: `apps/worker/internal/adminexport/dispatcher.go`
- Modify: `apps/worker/internal/adminexport/dispatcher_test.go`
- Modify: `apps/worker/internal/adminauditexport/dispatcher.go`
- Modify: `apps/worker/internal/adminauditexport/dispatcher_test.go`
- Modify: `apps/worker/internal/runtime/runtime.go`
- Modify: `apps/worker/internal/runtime/runtime_test.go`
- Modify: `apps/worker/internal/application/application.go`

- [ ] **Step 1: 写失败的缓存刷新安全测试**

覆盖三个固定 namespace、preview 影响摘要、PostgreSQL generation CAS、重复 operation、并发 refresh、审计失败回滚和 outbox 重放。Redis adapter 只能应用已提交的已知 generation 并删除上一 generation 下由后台创建的精确 key；禁止接收 key/prefix/glob，禁止 `KEYS`/`SCAN` 和任意 Lua 输入。

- [ ] **Step 2: 写失败的任务重试状态机测试**

四类任务分别覆盖 failed 可重试、pending/running/succeeded/cancelled/expired 不可重试、expected version、最大手动重试次数、原错误 code 保留、逐项结果不丢失、相同 operation 幂等和 worker claim 恢复。重试不创建第二份逻辑导出或第二个擦除任务。

- [ ] **Step 3: 运行测试确认失败**

```powershell
go test ./platform/admin/operations ./platform/admin/user ./platform/admin/export ./platform/admin/audit ./platform/persistence/redis ./apps/worker/internal/admincache ./apps/worker/internal/adminjobs ./apps/worker/internal/adminexport ./apps/worker/internal/adminauditexport
```

- [ ] **Step 4: 实现 preview/apply 命令**

preview 和 apply 都要求 permission/elevation；apply 额外要求 reason、operation ID、preview version/expected task version。缓存刷新在 PostgreSQL 事务中推进 generation、写命令回执、签名审计与 outbox，返回旧/新 generation 和受影响 projection，不直接修改 Redis；审计失败时事务回滚且不会产生可消费事件。admincache worker 在提交后按 generation 幂等更新 Redis 投影。任务重试同样在事务内写 retry receipt、更新原任务状态、追加签名审计与 outbox；任何一步失败全部回滚。

- [ ] **Step 5: 保持自动重试与人工重试边界**

outbox、timer、checkpoint 和 realtime owner 本身已有自动恢复机制，不进入人工 RetryTask 枚举。人工重试只唤醒前述四类 durable admin job；worker 继续使用 lease/CAS，不因管理员操作绕过 claim、幂等或结果加密。

- [ ] **Step 6: 测试并提交**

```powershell
go test -race ./platform/admin/operations ./platform/admin/user ./platform/admin/export ./platform/admin/audit ./platform/persistence/redis ./apps/worker/internal/admincache ./apps/worker/internal/adminjobs ./apps/worker/internal/adminexport ./apps/worker/internal/adminauditexport ./apps/worker/internal/runtime ./apps/worker/internal/application
git add platform/admin platform/persistence/redis platform/persistence/postgres/admin_operations_repository.go platform/persistence/postgres/admin_operations_repository_integration_test.go apps/worker
git commit -m "feat(admin-ops): 增加受控缓存刷新与任务重试"
```

## Task 7: 生成真实运营指标、趋势和异常摘要

**Files:**
- Create: `platform/admin/operations/overview.go`
- Create: `platform/admin/operations/overview_test.go`
- Create: `apps/worker/internal/adminmetrics/collector.go`
- Create: `apps/worker/internal/adminmetrics/collector_test.go`
- Modify: `apps/worker/internal/runtime/runtime.go`
- Modify: `apps/worker/internal/runtime/runtime_test.go`
- Modify: `apps/worker/internal/application/application.go`
- Modify: `tooling/sqlc/queries/admin_overview.sql`
- Regenerate: `platform/persistence/postgres/sqlcgen/admin_overview.sql.go`
- Modify: `platform/persistence/postgres/admin_operations_repository.go`
- Modify: `platform/persistence/postgres/admin_overview_query_integration_test.go`
- Modify: `platform/admin/room/query.go`
- Modify: `platform/admin/room/query_test.go`

- [ ] **Step 1: 写失败的 metric bucket collector 测试**

覆盖 hour/day UTC bucket 边界、当前 bucket 重算、迟到事件回看窗口、source watermark、重复 pass 幂等、停机恢复和部分依赖不可用。collector 不把当前在线/活跃 snapshot 伪装成完整时间段唯一用户数。

- [ ] **Step 2: 写失败的 overview 聚合测试**

固定时钟下验证在线用户来自 realtime presence，活跃房间/进行中牌局来自权威查询，24 小时新增/治理/异常终止/紧急修正来自索引数据，趋势来自 metric buckets，异常摘要复用房间阶段的 owner/推进/在线标记。最近高危操作来自签名审计投影，失败任务来自 durable job 状态。

- [ ] **Step 3: 运行测试确认失败**

```powershell
go test ./platform/admin/operations ./platform/admin/room ./apps/worker/internal/adminmetrics ./apps/worker/internal/runtime ./platform/persistence/postgres
```

- [ ] **Step 4: 实现可恢复 collector**

worker 每次只处理有界 bucket 数并重算当前 bucket 与固定迟到窗口；完成 bucket 保存 source watermark，重复执行 upsert 同值。collector 加入现有公平的有界 runtime pass，不能让 checkpoint、admin jobs 或 exports 饥饿。常量说明 bucket 宽度、回看窗口、poll 单位和查询上限。

- [ ] **Step 5: 实现 overview service**

service 并发读取当前 snapshot、趋势、异常、健康和最近操作，使用整体 deadline 与每源状态。结果区分 zero 和 unavailable；所有列表固定 `LIMIT`；采样时间使用数据源时间而不是响应发送时间。异常房间/牌局只提供详情入口需要的 ID/状态/原因，不提供完整回放或聊天。

- [ ] **Step 6: 生成、测试并提交**

```powershell
pnpm generate:sql
pnpm check:generated
go test -race ./platform/admin/operations ./platform/admin/room ./apps/worker/internal/adminmetrics ./apps/worker/internal/runtime ./apps/worker/internal/application ./platform/persistence/postgres
git add platform/admin/operations platform/admin/room apps/worker tooling/sqlc/queries/admin_overview.sql platform/persistence/postgres
git commit -m "feat(admin-overview): 聚合真实运营指标与异常摘要"
```

## Task 8: 挂载 Operations/Overview 服务与应用 wiring

**Files:**
- Create: `apps/api/internal/transport/adminoperations/service.go`
- Create: `apps/api/internal/transport/adminoperations/wire.go`
- Create: `apps/api/internal/transport/adminoperations/service_test.go`
- Create: `apps/api/internal/transport/adminoverview/service.go`
- Create: `apps/api/internal/transport/adminoverview/wire.go`
- Create: `apps/api/internal/transport/adminoverview/service_test.go`
- Modify: `apps/api/internal/transport/adminauth/policy.go`
- Modify: `apps/api/internal/transport/adminauth/policy_test.go`
- Modify: `apps/api/internal/transport/sensitive/registry.go`
- Modify: `apps/api/internal/transport/sensitive/registry_test.go`
- Modify: `apps/api/internal/server/surface.go`
- Modify: `apps/api/internal/server/surface_test.go`
- Modify: `apps/api/internal/application/application.go`
- Modify: `apps/api/internal/application/integration_test.go`
- Modify: `apps/edge/internal/server/server.go`
- Modify: `apps/edge/internal/server/server_test.go`
- Modify: `apps/admin/vite.config.ts`

- [ ] **Step 1: 写失败的 adapter、policy 与敏感字段测试**

断言 wire actor/permission/elevation 被忽略，所有查询/命令使用 server ActorContext；preview/apply policy、expected version、operation ID、reason、错误 detail 与审计结果稳定。reason、维护影响、实例 ID、任务 ID 和异常摘要进入 sensitive registry；日志只保留 procedure/request ID/status/duration。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/api/internal/transport/adminoperations ./apps/api/internal/transport/adminoverview ./apps/api/internal/transport/adminauth ./apps/api/internal/transport/sensitive ./apps/api/internal/server ./apps/api/internal/application ./apps/edge/internal/server
```

- [ ] **Step 3: 实现 wire/domain 转换与 bounded validation**

所有 duration/time/granularity/enum/page limit 在 transport 边界验证；domain service 不导入生成类型。健康与概览响应明确传递 sampled/freshness，不根据 HTTP 成功码伪造 healthy。维护和重试冲突返回最新 version 摘要，不能包含原任务错误文本。

- [ ] **Step 4: 更新 surface、edge 和 Vite**

两个公开管理 service prefix 只在 admin Host；内部 heartbeat path 不由 edge/Vite 对浏览器开放。Admin operations/overview handler 使用与其他管理模块相同的 Cookie/CSRF/context/policy/metrics/error interceptor 顺序。

- [ ] **Step 5: 测试并提交**

```powershell
go test -race ./apps/api/internal/transport/adminoperations ./apps/api/internal/transport/adminoverview ./apps/api/internal/transport/adminauth ./apps/api/internal/transport/sensitive ./apps/api/internal/server ./apps/api/internal/application ./apps/edge/internal/server
git add apps/api apps/edge apps/admin/vite.config.ts
git commit -m "feat(admin-ops): 挂载运维与概览管理服务"
```

## Task 9: 实现系统运维、运营概览与六模块导航

**Files:**
- Create: `apps/admin/src/api/admin-operations.ts`
- Create: `apps/admin/src/api/admin-overview.ts`
- Delete: `apps/admin/src/views/dashboard/OverviewView.vue`
- Delete: `apps/admin/src/api/readiness.ts`
- Delete: `apps/admin/src/components/session/ReadinessStatus.vue`
- Delete: `apps/admin/tests/readiness-status.test.ts`
- Create: `apps/admin/src/views/overview/OverviewView.vue`
- Create: `apps/admin/src/views/overview/overview-store.ts`
- Create: `apps/admin/src/views/overview/components/MetricStrip.vue`
- Create: `apps/admin/src/views/overview/components/ActivityTrend.vue`
- Create: `apps/admin/src/views/overview/components/AttentionList.vue`
- Create: `apps/admin/src/views/overview/components/OperationalSummary.vue`
- Create: `apps/admin/src/views/operations/OperationsView.vue`
- Create: `apps/admin/src/views/operations/operations-store.ts`
- Create: `apps/admin/src/views/operations/components/ServiceInstancesTable.vue`
- Create: `apps/admin/src/views/operations/components/DependencyHealth.vue`
- Create: `apps/admin/src/views/operations/components/BacklogTable.vue`
- Create: `apps/admin/src/views/operations/components/MaintenancePanel.vue`
- Create: `apps/admin/src/views/operations/components/MaintenanceDialog.vue`
- Create: `apps/admin/src/views/operations/components/CacheRefreshDialog.vue`
- Create: `apps/admin/src/views/operations/components/TaskRetryDialog.vue`
- Modify: `apps/admin/src/router/routes.ts`
- Modify: `apps/admin/src/constants/navigation.ts`
- Modify: `apps/admin/src/stores/navigation.ts`
- Modify: `apps/admin/src/layouts/AdminLayout.vue`
- Modify: `apps/admin/src/layouts/components/MobileNavigation.vue`
- Create: `apps/admin/tests/overview-store.test.ts`
- Create: `apps/admin/tests/overview-view.test.ts`
- Create: `apps/admin/tests/operations-store.test.ts`
- Create: `apps/admin/tests/operations-view.test.ts`
- Modify: `apps/admin/tests/navigation-store.test.ts`
- Create: `apps/admin/tests/router.test.ts`

- [ ] **Step 1: 写失败的 overview/operations store 测试**

覆盖采样时间与 stale 状态、hour/day 趋势、partial dependency failure、请求竞态取消、visibility pause/resume、手动刷新、维护/缓存/重试 preview、step-up 后恢复原命令、版本冲突重载和刷新后任务状态恢复。概览不得使用静态 fallback 数字。

- [ ] **Step 2: 写失败的组件、路由与权限测试**

覆盖紧凑指标带、趋势、异常/高危操作列表、服务实例、依赖、积压、维护状态和固定命令。缺少 `OVERVIEW_READ`/`OPERATIONS_READ` 时菜单隐藏且直接 URL 403；缺少 maintain permission 时命令不可见；缺少 elevation 时打开统一 step-up，不丢失已完成 preview。

- [ ] **Step 3: 运行测试确认失败**

```powershell
pnpm --filter @game-night/admin test -- overview-store.test.ts overview-view.test.ts operations-store.test.ts operations-view.test.ts navigation-store.test.ts router.test.ts
```

- [ ] **Step 4: 实现运营概览**

首屏使用紧凑 metric strip、可读趋势、需要关注列表与健康/失败任务摘要，不使用装饰性卡片墙。每个区域展示统计窗口/采样时间/新鲜度；zero、loading、empty、stale、partial 和 unavailable 有不同状态。异常项链接到房间/牌局详情，失败任务链接到运维任务行。

- [ ] **Step 5: 实现系统运维与固定命令对话框**

运维页按“实例与依赖、积压、维护控制”分区，不嵌套卡片。所有写操作使用 `AppDialog.toggleDialog`，表单使用 Naive UI 既有校验模式；close 时 abort、清理 reason/preview/elevation 关联和 validation。缓存 namespace 和 task kind 使用固定选项，不提供自由 key、命令或 JSON 编辑器。

- [ ] **Step 6: 完成六模块导航**

测试通过后按固定顺序加入：运营概览、用户中心、房间与牌局、审计中心、安全设置、系统运维。桌面 sider 与移动 drawer 使用同一 `navigationItems`；tab restore 会移除旧 route name 和无权限 tab。导航不显示规则发布、完整回放、告警、禁用项或占位模块。

- [ ] **Step 7: 前端验证并提交**

```powershell
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
git add apps/admin
git commit -m "feat(admin-ui): 交付运维概览与六模块导航"
```

## Task 10: 锁定旧后台清零约束

**Files:**
- Modify: `apps/api/internal/server/surface_test.go`
- Modify: `apps/edge/internal/server/server_test.go`
- Create: `apps/admin/tests/legacy-surface.test.ts`
- Modify: `apps/admin/src/router/routes.ts`
- Modify: `apps/admin/src/constants/navigation.ts`
- Modify: `apps/admin/src/stores/preferences.ts`

- [ ] **Step 1: 写失败的旧 surface/route 清零测试**

后端测试断言旧 AdminIdentity service prefix 在 API 和 edge 均为 404，新 handler registry 不含旧 service name。前端测试导入 routes/navigation 后断言只存在六个正式模块，不含旧 dashboard/user-workbench/audit route component、旧 auth step route 或 readiness 伪概览。

- [ ] **Step 2: 对照精确文件清单确认前置删除完成**

以下路径必须全部不存在；任何一项仍存在都回到拥有它的阶段修复，不在本任务使用目录通配盲删：

```text
contracts/platform/admin/v1/admin_identity.proto
contracts/gen/go/platform/admin/v1/admin_identity.pb.go
contracts/gen/go/platform/admin/v1/adminv1connect/admin_identity.connect.go
contracts/gen/ts/platform/admin/v1/admin_identity_pb.ts
apps/api/internal/transport/adminidentity/handler.go
platform/admin/connect_service.go
platform/admin/identity_connect_service.go
platform/admin/identity_service.go
platform/admin/identity_service_test.go
apps/admin/src/api/admin-identity.ts
apps/admin/src/api/readiness.ts
apps/admin/src/components/auth/BootstrapPendingState.vue
apps/admin/src/components/auth/ChangePasswordStep.vue
apps/admin/src/components/auth/LoginPasswordStep.vue
apps/admin/src/components/auth/MfaVerificationStep.vue
apps/admin/src/components/auth/SecretReceiptStep.vue
apps/admin/src/components/auth/TotpEnrollmentStep.vue
apps/admin/src/components/users/RealNameDialog.vue
apps/admin/src/components/users/UserDetails.vue
apps/admin/src/components/users/UserLookupForm.vue
apps/admin/src/components/users/UserStatusDialog.vue
apps/admin/src/components/audit/AuditDetails.vue
apps/admin/src/components/audit/AuditFilters.vue
apps/admin/src/components/audit/AuditTable.vue
apps/admin/src/components/session/ReadinessStatus.vue
apps/admin/src/views/users/UserWorkbenchView.vue
apps/admin/src/views/audit/AuditView.vue
apps/admin/src/views/dashboard/OverviewView.vue
apps/admin/tests/readiness-status.test.ts
```

- [ ] **Step 3: 清理持久 route/tab 名称**

preferences 恢复只接受当前六模块 route name；遇到旧 tab 直接丢弃，不创建旧路由跳转。仓库搜索证明 `ConnectAdminService`、`WithAdminTransportContext`、`next_step`、`recovery_pending`、`GAME_NIGHT_API_ADMIN_MFA_REQUIRED`、旧 service prefix 和 `ReadinessStatus` 在生产代码为零；migration 历史或负向测试若保留字面量，必须是精确断言而非兼容逻辑。

- [ ] **Step 4: 运行清零测试并提交**

```powershell
go test ./apps/api/internal/server ./apps/edge/internal/server
pnpm --filter @game-night/admin test -- legacy-surface.test.ts navigation-store.test.ts
pnpm --filter @game-night/admin check
git add apps/api/internal/server/surface_test.go apps/edge/internal/server/server_test.go apps/admin/tests/legacy-surface.test.ts apps/admin/src/router/routes.ts apps/admin/src/constants/navigation.ts apps/admin/src/stores/preferences.ts
git commit -m "test(admin): 锁定旧后台清零约束"
```

## Task 11: 完成跨服务后端集成验收

**Files:**
- Modify: `apps/api/internal/application/integration_test.go`
- Create: `apps/realtime/internal/application/maintenance_integration_test.go`
- Create: `apps/worker/internal/application/admin_operations_integration_test.go`
- Modify: `platform/persistence/postgres/admin_operations_repository_integration_test.go`
- Modify: `platform/persistence/postgres/admin_overview_query_integration_test.go`

- [ ] **Step 1: 增加真实依赖集成场景**

覆盖四类实例 heartbeat/stale、依赖 partial health、真实 backlog、maintenance version CAS、API user mutation 拦截、已有 WebSocket action 拦截、纯读/admin/内部收敛继续、缓存 generation/outbox 消费、四类失败任务重试、metric bucket 恢复和 overview freshness。每个高危操作验证成功/拒绝/失败签名审计。

- [ ] **Step 2: 运行定向集成测试**

```powershell
go test -race ./platform/admin/operations ./platform/persistence/postgres ./apps/api/internal/application ./apps/realtime/internal/application ./apps/worker/internal/application
pnpm test:backend:integration
```

预期：全部 PASS；若暴露实现缺口，回到对应 Task 修复并使用该 Task 的 scope 提交，不能把功能修复混入本测试提交。

- [ ] **Step 3: 提交集成测试**

```powershell
git add apps/api/internal/application/integration_test.go apps/realtime/internal/application/maintenance_integration_test.go apps/worker/internal/application/admin_operations_integration_test.go platform/persistence/postgres/admin_operations_repository_integration_test.go platform/persistence/postgres/admin_overview_query_integration_test.go
git commit -m "test(admin-ops): 覆盖运维概览跨服务集成"
```

## Task 12: 完成六模块浏览器与响应式验收

**Files:**
- Create: `apps/admin/e2e/operations.spec.ts`
- Create: `apps/admin/e2e/overview.spec.ts`
- Create: `apps/admin/e2e/admin-console-responsive.spec.ts`
- Create: `apps/admin/e2e/admin-console-security.spec.ts`

- [ ] **Step 1: 增加完整浏览器业务流程**

使用真实本地 PostgreSQL、Redis、对象存储、API、edge、realtime 和 worker。覆盖概览数据/趋势/异常跳转、运维实例/健康/积压、启停维护、被拦截的用户写请求、固定缓存刷新、失败任务预览/重试、权限隐藏、elevation 过期、撤销单个其他管理员会话，以及预览并提权撤销其他全部会话。

- [ ] **Step 2: 增加桌面/移动与敏感信息断言**

Playwright 至少验证 `1440x900`、`1024x768`、`390x844` 和 `360x800`：导航、指标带、表格、drawer/dialog、长实例 ID、长 reason、stale 标签和按钮无重叠/溢出；键盘可完成筛选、详情与维护预览。检查 DOM、URL、local/session storage、console 和请求日志不残留 reason、elevation、PII、secret 或 download grant。

- [ ] **Step 3: 启动本地栈并运行新增 spec**

```powershell
docker compose -f deploy/docker-compose.yml up -d
$env:ADMIN_E2E='1'; pnpm --filter @game-night/admin test:e2e -- --grep "运维|概览|响应式|安全"
```

逐一阅读 Playwright trace/screenshot；对桌面和移动关键页面做截图与像素非空检查。浏览器 console error、请求 5xx、文本重叠、水平溢出或不可达命令均视为失败，修复后重跑对应 owning Task 与新增 spec。

- [ ] **Step 4: 提交浏览器测试**

```powershell
git add apps/admin/e2e/operations.spec.ts apps/admin/e2e/overview.spec.ts apps/admin/e2e/admin-console-responsive.spec.ts apps/admin/e2e/admin-console-security.spec.ts
git commit -m "test(admin-ui): 覆盖六模块浏览器流程"
```

## Task 13: 更新开发说明并运行最终门禁

**Files:**
- Modify: `README.md`
- Modify: `docs/operations/development.md`

- [ ] **Step 1: 更新开发与运行说明**

记录开发环境执行 `00028` 至 `00032` 的重置影响、初始管理员改密、2FA 默认关闭、四服务启动顺序、内部 heartbeat 配置、后台/前台地址和 E2E 前置条件。明确不包含规则版本发布、完整牌局回放和告警中心。

- [ ] **Step 2: 运行全仓静态与测试门禁**

```powershell
pnpm generate
pnpm check:generated
go test -race ./...
pnpm check
pnpm test
pnpm build
pnpm check:boundaries
pnpm test:backend:integration
$env:ADMIN_E2E='1'; pnpm --filter @game-night/admin test:e2e
git diff --check
```

预期：全部退出码为 `0`；生成物无漂移；边界检查无新增跨层依赖；没有旧管理 service/route 被注册。失败时返回 owning Task 修复并重跑完整门禁，不在文档提交中夹带功能修复。

- [ ] **Step 3: 只提交文档**

```powershell
git add README.md docs/operations/development.md
git diff --cached --name-only
git diff --cached --check
git commit -m "docs(admin): 更新后台开发与验证说明"
```

## 完成门禁

- API、edge、realtime、worker 的版本、实例、启动时间和心跳均来自真实运行状态，过期实例显示 stale。
- PostgreSQL、Redis、对象存储、checkpoint、realtime peer、限流依赖和所有 durable job 积压具有采样时间与明确状态。
- 维护模式在 API 与已有 realtime 连接上阻止用户 mutation，同时不阻止管理恢复、读取、内部终止和 worker 收敛。
- 缓存刷新只作用于三个固定可重建 projection；任务重试只作用于四类 failed durable admin job。
- 概览所有指标、趋势、异常、高危操作和失败任务来自真实后端，zero 与 unavailable 不混淆。
- 导航按固定顺序只含六个模块；旧页面、旧 RPC、旧认证状态机、readiness 伪概览和死代码已删除。
- 规则版本发布、完整牌局回放和告警中心没有页面、菜单、RPC 或占位入口。
- 全仓 generate/check/test/build、集成测试和真实 Playwright 桌面/移动流程全部通过。
