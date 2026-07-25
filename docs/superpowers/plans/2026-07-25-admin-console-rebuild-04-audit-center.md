# 审计检索与敏感导出实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付可按操作者、目标、动作、结果、风险、请求 ID 和时间检索的签名审计中心，以及异步、加密、单次授权下载的审计导出。

**Architecture:** 签名 canonical event 仍是事实来源；PostgreSQL 仅增加由同一 canonical event 派生的可索引投影列，repository 读取时重新解码并比对投影，任何不一致都视为完整性错误。列表在开始时固定最大 chain sequence，然后在同一授权流程中记录访问审计，因此新读取事件不进入本次结果；导出复用上一阶段的通用 export job/result store/download grant。

**Tech Stack:** Go、签名审计链、Protobuf canonical encoding、PostgreSQL/sqlc、worker、加密对象存储、Vue 3、Naive UI、Vitest、Playwright。

---

## Task 1: 定义审计查询、详情和导出契约

**Files:**
- Create: `contracts/platform/admin/v1/admin_audit.proto`
- Modify: `contracts/platform/admin/v1/admin_common.proto`
- Modify: `contracts/platform/audit/v1/audit.proto`
- Regenerate: `contracts/gen/go/platform/admin/v1/**`
- Regenerate: `contracts/gen/ts/platform/admin/v1/**`
- Regenerate: `contracts/gen/go/platform/audit/v1/**`
- Regenerate: `contracts/gen/ts/platform/audit/v1/**`
- Modify: `apps/api/internal/server/surface_test.go`
- Modify: `apps/api/internal/transport/adminauth/policy_test.go`

- [ ] **Step 1: 写失败的服务与 policy 测试**

断言 AdminAuditService 只在 admin Host；list/get 要求 `audit.read`；普通脱敏导出要求 `audit.export`；敏感导出创建和下载额外要求 `audit.export_sensitive` elevation。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/api/internal/server ./apps/api/internal/transport/adminauth
```

- [ ] **Step 3: 定义审计 wire model**

列表响应只返回扫描字段和验证状态；详情返回 actor/target/action/result/risk/reason/request context 摘要、permission/elevation、operation/related job 和结构化 before/after diff。敏感值使用字段名、版本或 digest，不返回 canonical 原始秘密。

契约包含 `ListAuditEvents/GetAuditEvent/CreateAuditExport/GetAuditExport/CreateAuditExportDownloadGrant/DeleteAuditExportResult`。page token 绑定 filter/sort 和固定 `max_chain_sequence`。

- [ ] **Step 4: 生成、验证并提交**

```powershell
pnpm generate:contracts
pnpm check:generated
git add contracts apps/api/internal/server/surface_test.go apps/api/internal/transport/adminauth/policy_test.go
git commit -m "feat(admin-audit): 定义审计中心与导出契约"
```

## Task 2: 为签名事件增加可验证检索投影

**Files:**
- Create: `infra/migrations/00031_admin_audit_index.sql`
- Modify: `tooling/sqlc/queries/audit.sql`
- Regenerate: `platform/persistence/postgres/sqlcgen/audit.sql.go`
- Regenerate: `platform/persistence/postgres/sqlcgen/models.go`
- Modify: `platform/audit/model.go`
- Modify: `platform/audit/canonical.go`
- Modify: `platform/audit/service.go`
- Modify: `platform/audit/repository.go`
- Modify: `platform/persistence/postgres/audit_repository.go`
- Modify: `platform/persistence/postgres/audit_checkpoint_repository.go`
- Test: `platform/audit/audit_test.go`
- Test: `platform/persistence/postgres/audit_repository_test.go`
- Create: `platform/persistence/postgres/audit_query_integration_test.go`

- [ ] **Step 1: 写失败的投影完整性与快照分页测试**

覆盖：append 同时写 canonical 与 actor/target/action/result/risk/request/time 投影；读取重新解码并比对；篡改任一投影失败；list 只返回 `sequence <= max_sequence`；复杂筛选在 SQL 层执行；cursor 不能跨 max/filter 复用。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/audit ./platform/persistence/postgres
```

- [ ] **Step 3: 编写 migration 和数据库函数变更**

为 `audit_events` 增加有界投影列及索引。替换 `append_audit_event` SECURITY DEFINER 签名，使 repository 同时传入投影；函数仍在 chain head row lock 下更新 sequence/hash。迁移从现有 canonical event 回填投影，任何无法验证/解码的行使迁移失败，而不是填默认值。

- [ ] **Step 4: 更新 canonical event model**

统一所有后台事件的 result/risk/permission/elevation/operation 结构；旧业务调用通过类型化 builder 构造，不允许 transport 自由组装 canonical bytes。

- [ ] **Step 5: 更新 repository 验证**

`List`/`Get` 对每行执行 signature/hash/canonical/projection 四重校验。查询 repository 接受结构化 filter、max sequence、cursor 和 limit，不在 Go 中按四倍页大小扫描过滤。

- [ ] **Step 6: 生成、测试并提交**

```powershell
pnpm generate:sql
go test -race ./platform/audit ./platform/persistence/postgres
git add infra/migrations/00031_admin_audit_index.sql tooling/sqlc/queries/audit.sql platform/audit platform/persistence/postgres
git commit -m "feat(admin-audit): 增加可验证审计检索投影"
```

## Task 3: 实现无递归列表、详情和访问审计

**Files:**
- Create: `platform/admin/audit/model.go`
- Create: `platform/admin/audit/query.go`
- Create: `platform/admin/audit/repository.go`
- Create: `platform/admin/audit/service.go`
- Create: `platform/admin/audit/errors.go`
- Test: `platform/admin/audit/query_test.go`
- Test: `platform/admin/audit/service_test.go`

- [ ] **Step 1: 写失败的读取事件时序测试**

测试精确顺序：验证 actor/session -> 读取 chain head 得到 max -> 查询 `<= max` -> 验证结果 -> 追加 `audit.events.listed`；返回 page 的 max 保持不变。Get 写 `audit.event.read`，目标为被读取 sequence。内部 append 不触发读取审计。

- [ ] **Step 2: 写重试去重测试**

相同 request ID、相同 filter/cursor 的自动重试只追加一个访问事件；主动换筛选/翻页即使 request ID 新值也写新事件。访问审计 append 失败使读取失败并不返回敏感数据。

- [ ] **Step 3: 运行测试确认失败**

```powershell
go test ./platform/admin/audit
```

- [ ] **Step 4: 实现 query service**

page token 内容：schema version、filter/sort digest、max sequence、last sort value/sequence，使用管理 HMAC keyring。默认按 sequence desc；page size 有上限。详情 diff 由版本化 audit detail decoder 生成，未知 detail version 显示“不可解析版本”且不伪造差异。

- [ ] **Step 5: 测试并提交**

```powershell
go test -race ./platform/admin/audit
git add platform/admin/audit
git commit -m "feat(admin-audit): 实现审计检索与访问审计"
```

## Task 4: 实现异步审计导出

**Files:**
- Create: `platform/admin/audit/export.go`
- Create: `platform/admin/audit/export_test.go`
- Create: `apps/worker/internal/adminauditexport/dispatcher.go`
- Create: `apps/worker/internal/adminauditexport/dispatcher_test.go`
- Modify: `apps/worker/internal/application/application.go`
- Modify: `apps/worker/internal/runtime/runtime.go`
- Modify: `platform/admin/export/service.go`
- Test: `platform/admin/export/service_test.go`

- [ ] **Step 1: 写失败的导出授权/固化测试**

覆盖 filter、字段集合、max sequence、遮蔽策略和 schema version 固化；敏感字段创建要求 elevation；worker 不导出 max 之后事件；partial/failed 状态精确；结果 TTL 和删除复用通用 export 语义。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/admin/audit ./platform/admin/export ./apps/worker/internal/adminauditexport
```

- [ ] **Step 3: 实现审计 export adapter**

导出 service 只负责审计筛选与记录序列化，通用 export service 负责 job、加密对象和 grant。CSV/JSONL 中的 PII、IP、User-Agent 和 detail 按字段策略遮蔽；签名/hash/sequence 可以导出用于离线验证。

- [ ] **Step 4: 接入 worker runtime**

dispatcher 使用固定 max sequence 的 cursor 分批生成，不因为读取访问事件增加任务范围。任务状态/进度可恢复，相同 export ID 不重复写第二个结果对象。

- [ ] **Step 5: 测试并提交**

```powershell
go test -race ./platform/admin/audit ./platform/admin/export ./apps/worker/internal/adminauditexport ./apps/worker/internal/runtime
git add platform/admin/audit platform/admin/export apps/worker
git commit -m "feat(admin-audit): 增加异步审计导出"
```

## Task 5: 挂载 AdminAuditService 和下载授权

**Files:**
- Create: `apps/api/internal/transport/adminaudit/service.go`
- Create: `apps/api/internal/transport/adminaudit/wire.go`
- Create: `apps/api/internal/transport/adminaudit/service_test.go`
- Modify: `apps/api/internal/transport/adminexport/download.go`
- Modify: `apps/api/internal/transport/adminexport/download_test.go`
- Modify: `apps/api/internal/transport/adminauth/policy.go`
- Modify: `apps/api/internal/transport/sensitive/registry.go`
- Modify: `apps/api/internal/server/surface.go`
- Modify: `apps/api/internal/application/application.go`
- Modify: `apps/edge/internal/server/server.go`
- Modify: `apps/admin/vite.config.ts`
- Test: `apps/api/internal/application/integration_test.go`

- [ ] **Step 1: 写失败的 transport 和 elevation 测试**

断言 list/get 权限、敏感导出创建/下载 elevation、grant session binding、wire filter 校验、读取审计和稳定错误 detail。普通脱敏导出不错误要求 elevation。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/api/internal/transport/adminaudit ./apps/api/internal/transport/adminexport ./apps/api/internal/application
```

- [ ] **Step 3: 实现 adapter 与下载二次校验**

敏感 grant 创建时要求 elevation；实际下载时再次读取当前 actor/elevation 和 export classification。grant 被消费后即使 elevation 仍有效也不能重放。

- [ ] **Step 4: 更新 surface/edge/Vite/sensitive registry**

AdminAuditService prefix 只在 admin Host。审计 filter/reason/diff/export 字段加入敏感 registry，日志只保留 procedure、request ID、状态和耗时。

- [ ] **Step 5: 测试并提交**

```powershell
go test -race ./apps/api/internal/transport/adminaudit ./apps/api/internal/transport/adminexport ./apps/api/internal/server ./apps/api/internal/application ./apps/edge/internal/server
git add apps/api apps/edge apps/admin/vite.config.ts
git commit -m "feat(admin-audit): 挂载审计查询与导出服务"
```

## Task 6: 实现审计中心前端

**Files:**
- Create: `apps/admin/src/api/admin-audit.ts`
- Delete: `apps/admin/src/views/audit/AuditView.vue`
- Create: `apps/admin/src/views/audit/AuditCenterView.vue`
- Create: `apps/admin/src/views/audit/audit-query-store.ts`
- Create: `apps/admin/src/views/audit/audit-url-state.ts`
- Create: `apps/admin/src/views/audit/components/AuditFilters.vue`
- Create: `apps/admin/src/views/audit/components/AuditTable.vue`
- Create: `apps/admin/src/views/audit/components/AuditDetailsDrawer.vue`
- Create: `apps/admin/src/views/audit/components/AuditDiff.vue`
- Create: `apps/admin/src/views/audit/components/AuditExportDialog.vue`
- Create: `apps/admin/src/views/audit/components/AuditExportJobs.vue`
- Delete: `apps/admin/src/components/audit/AuditDetails.vue`
- Delete: `apps/admin/src/components/audit/AuditFilters.vue`
- Delete: `apps/admin/src/components/audit/AuditTable.vue`
- Modify: `apps/admin/src/router/routes.ts`
- Modify: `apps/admin/src/constants/navigation.ts`
- Modify: `apps/admin/src/stores/navigation.ts`
- Create: `apps/admin/tests/audit-query-store.test.ts`
- Create: `apps/admin/tests/audit-center.test.ts`

- [ ] **Step 1: 写失败的 store/组件测试**

覆盖 URL filter/cursor、固定 max sequence 翻页、详情竞态、签名验证状态、结构化 diff、普通/敏感导出、step-up、任务恢复和单次下载错误。

- [ ] **Step 2: 运行测试确认失败**

```powershell
pnpm --filter @game-night/admin test -- audit-query-store.test.ts audit-center.test.ts
```

- [ ] **Step 3: 实现列表、详情和导出 UI**

表格显示时间、actor、target、action、result、risk、request ID 与验证状态。详情抽屉显示 reason、permission/elevation、operation/job 和遮蔽 diff；不显示 canonical bytes 作为主要内容。审计列表不自动轮询。

- [ ] **Step 4: 完成后加入导航**

测试通过后加入“审计中心”；缺少 `AUDIT_READ` 时菜单隐藏且直接 URL 403。导出按钮按 `AUDIT_EXPORT` 显示，敏感选项触发 elevation。

- [ ] **Step 5: 前端验证并提交**

```powershell
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
git add apps/admin
git commit -m "feat(admin-ui): 交付审计中心"
```

## Task 7: 审计中心集成与浏览器验收

**Files:**
- Modify: `apps/api/internal/application/integration_test.go`
- Create: `apps/admin/e2e/audit-center.spec.ts`
- Create: `apps/admin/e2e/audit-export.spec.ts`

- [ ] **Step 1: 增加固定 sequence 和读取审计集成测试**

创建已知 chain，执行 list，断言响应 max 为调用前 head、`audit.events.listed` sequence 更大且不在结果；翻页沿用 max；Get 目标 sequence 正确；内部 append 无递归读取事件。

- [ ] **Step 2: 增加导出/grant 集成测试**

断言导出只到固定 max、敏感创建/下载都要求 elevation、grant 单次、过期/删除后拒绝、结果自动清理有审计。

- [ ] **Step 3: 运行阶段门禁**

```powershell
pnpm generate
pnpm check:generated
go test -race ./platform/audit ./platform/admin/audit ./platform/admin/export ./platform/persistence/postgres ./apps/worker/... ./apps/api/...
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
pnpm check:boundaries
git diff --check
```

- [ ] **Step 4: 运行 E2E 并提交测试**

```powershell
$env:ADMIN_E2E='1'; pnpm --filter @game-night/admin test:e2e -- --grep "审计"
git add apps/api/internal/application/integration_test.go apps/admin/e2e
git commit -m "test(admin-audit): 覆盖检索读取与敏感导出"
```

## 完成门禁

- 审计筛选在数据库执行且每行重新验证 canonical/signature/hash/projection。
- 列表固定 max sequence，读取事件不进入同次分页，无自增长循环。
- 普通与敏感导出权限/elevation 区分正确；下载 grant 单次且 session 绑定。
- 审计中心进入导航，无旧审计组件或高频自动轮询残留。
