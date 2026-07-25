# 用户中心、批量任务与导出实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付支持组合筛选、详情、PII、标签备注、设备会话、治理、注销擦除、批量任务和异步导出的完整用户中心。

**Architecture:** 管理查询使用 PostgreSQL 专用 query repository 和绑定筛选摘要的 opaque cursor；管理写操作在 `platform/admin/user` 编排身份聚合、签名审计与 outbox。批量、擦除和导出由 worker 持久执行，浏览器只创建/查询任务；导出结果使用独立 keyring 加密并通过 session 绑定、单次使用的下载 grant 从固定管理 Host 端点下载。

**Tech Stack:** Go、Connect、Protobuf、PostgreSQL/sqlc、worker lease、对象存储、AES envelope、Vue 3、Naive UI、Vitest、Playwright。

---

## Task 1: 定义用户、批量和导出契约

**Files:**
- Create: `contracts/platform/admin/v1/admin_user.proto`
- Modify: `contracts/platform/admin/v1/admin_common.proto`
- Regenerate: `contracts/gen/go/platform/admin/v1/**`
- Regenerate: `contracts/gen/ts/platform/admin/v1/**`
- Modify: `apps/api/internal/server/surface_test.go`
- Modify: `apps/edge/internal/server/server_test.go`

- [ ] **Step 1: 写失败的 handler 挂载测试**

测试 `AdminUserService` 可从 admin surface 访问、从 user Host 返回 404，且每个 RPC 都出现在 adminauth procedure policy 中。`GetUser` 只要求 `users.read`；`GetUserPII` 单独要求 `users.read_pii`、reason 和访问审计；不能把 PII 作为 `GetUser` 的条件响应字段。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/api/internal/server ./apps/edge/internal/server ./apps/api/internal/transport/adminauth
```

- [ ] **Step 3: 定义查询与命令消息**

契约至少包含：

- `ListUsers`：ID/用户名/状态/标签/创建时间/最近活动/在线组合筛选，稳定 sort enum、page size/token、sampled_at。
- `GetUser`：只返回基础身份、PII 可用性/字段名、标签、备注、设备、房间/牌局摘要和最近治理摘要，不返回任何 PII 值。
- `GetUserPII`：独立请求，要求 `users.read_pii`、reason 和目标用户 ID；响应只返回授权字段并写访问审计。
- `ListUserTags/CreateUserTag/UpdateUserTag/SetUserTags/AppendUserNote`。
- `PreviewUserCommand/ExecuteUserCommand`：封禁、解封、强制退出全部设备、踢出房间和注销。
- `PreviewBatchUserOperation/StartBatchUserOperation/GetBatchUserOperation/ListBatchUserOperationItems/Cancel/Retry`。
- `CreateUserExport/GetUserExport/CreateExportDownloadGrant/DeleteExportResult`。

所有写请求包含 `operation_id`、reason 和 expected version。标签定义/关联、备注和用户命令使用不同消息，避免自由 JSON patch。`GetUserPII` 不作为 `GetUser` 的权限条件字段展开；两个 RPC 不返回重叠的 PII 值。

- [ ] **Step 4: 生成并验证契约**

```powershell
pnpm generate:contracts
pnpm check:generated
```

- [ ] **Step 5: 提交**

```powershell
git add contracts apps/api/internal/server/surface_test.go apps/edge/internal/server/server_test.go
git commit -m "feat(admin-user): 定义用户中心与任务契约"
```

## Task 2: 建立用户标注、任务和导出持久模型

**Files:**
- Create: `infra/migrations/00029_admin_user_center.sql`
- Create: `tooling/sqlc/queries/admin_user.sql`
- Create: `tooling/sqlc/queries/admin_jobs.sql`
- Create: `tooling/sqlc/queries/admin_export.sql`
- Regenerate: `platform/persistence/postgres/sqlcgen/admin_user.sql.go`
- Regenerate: `platform/persistence/postgres/sqlcgen/admin_jobs.sql.go`
- Regenerate: `platform/persistence/postgres/sqlcgen/admin_export.sql.go`
- Regenerate: `platform/persistence/postgres/sqlcgen/models.go`
- Create: `platform/persistence/postgres/admin_user_repository.go`
- Create: `platform/persistence/postgres/admin_job_repository.go`
- Create: `platform/persistence/postgres/admin_export_repository.go`
- Test: `platform/persistence/postgres/admin_user_repository_integration_test.go`
- Test: `platform/persistence/postgres/admin_job_repository_integration_test.go`
- Test: `platform/persistence/postgres/admin_export_repository_integration_test.go`

- [ ] **Step 1: 写失败的 schema/repository 集成测试**

覆盖标签名称唯一/CAS、备注只追加、稳定用户 cursor、batch preview TTL、逐项 lease/幂等、注销擦除状态机、export 状态机、单次 download grant、结果 TTL 和并发消费。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test -run "TestAdmin(User|Job|Export)Repository" ./platform/persistence/postgres
```

- [ ] **Step 3: 创建表和索引**

迁移创建规格中的：

```text
admin_user_tags
admin_user_tag_links
admin_user_notes
admin_batch_jobs
admin_batch_job_items
admin_user_erasure_jobs
admin_export_jobs
admin_export_download_grants
```

同时为 `users(status, created_at, user_id)`、规范化用户名、`device_credentials(last_seen_at)`、room membership 和 game participant 摘要增加管理查询索引。禁止用宽泛 JSONB 替代任务状态；仅允许版本化的筛选快照/结果摘要 JSONB，并由领域 decoder 验证 schema version。

- [ ] **Step 4: 实现 opaque cursor 和稳定查询**

cursor 由版本、规范化 filter/sort digest、最后排序值和 user ID 组成并使用管理 keyring HMAC。不同筛选、排序或 schema version 的 token 返回 invalid argument。

- [ ] **Step 5: 生成 SQL 并运行集成测试**

```powershell
pnpm generate:sql
go test ./platform/persistence/postgres
```

- [ ] **Step 6: 提交**

```powershell
git add infra/migrations/00029_admin_user_center.sql tooling/sqlc/queries platform/persistence/postgres
git commit -m "feat(admin-user): 建立用户任务与导出持久层"
```

## Task 3: 实现用户列表、详情、PII、标签和备注

**Files:**
- Create: `platform/admin/user/model.go`
- Create: `platform/admin/user/query.go`
- Create: `platform/admin/user/annotation.go`
- Create: `platform/admin/user/repository.go`
- Create: `platform/admin/user/service.go`
- Create: `platform/admin/user/errors.go`
- Modify: `platform/profile/pii.go`
- Test: `platform/admin/user/query_test.go`
- Test: `platform/admin/user/annotation_test.go`
- Test: `platform/admin/user/service_test.go`

- [ ] **Step 1: 写失败的查询与标注测试**

覆盖：筛选规范化、cursor 绑定、稳定排序、PII permission、PII 读取原因、标签 CAS、标签关联原因、备注只追加、分页 sample time 和旧异步结果不可覆盖新查询。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/admin/user
```

- [ ] **Step 3: 定义窄 repository ports**

查询 port 返回管理 read model，不返回 sqlc row；写 port 暴露 `Run` UOW 内的用户、profile、tag、note、audit 和 outbox repository。PII protector 只在具有 `users.read_pii` 且审计健康的请求中调用。

- [ ] **Step 4: 实现查询与标注命令**

`ListUsers` 不写逐行 PII 审计且不返回 PII。`GetUserPII` 单独调用、要求 reason 并写访问审计。标签变更需要 expected tag/link version；备注保存原文的受控长度内容，但审计只记录不可逆摘要和 note ID。

- [ ] **Step 5: 运行领域测试并提交**

```powershell
go test -race ./platform/admin/user
git add platform/admin/user platform/profile/pii.go
git commit -m "feat(admin-user): 实现用户查询标签与备注"
```

## Task 4: 实现设备会话、单用户治理和注销预览

**Files:**
- Modify: `platform/identity/model.go`
- Modify: `platform/identity/device_management.go`
- Create: `platform/identity/governance.go`
- Create: `platform/admin/user/governance.go`
- Modify: `platform/admin/user/service.go`
- Test: `platform/identity/device_management_test.go`
- Create: `platform/identity/governance_test.go`
- Create: `platform/admin/user/governance_test.go`

- [ ] **Step 1: 写失败的治理矩阵测试**

覆盖：封禁/解封无需 elevation 但要求 reason/operation/version；退出全部设备要求 `users.revoke_devices`；踢房间遇到进行中牌局可拒绝；注销要求 `users.delete`，进行中牌局/待处理敏感导出等阻塞项必须出现在 preview。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/identity ./platform/admin/user
```

- [ ] **Step 3: 提取明确身份治理入口**

不让 admin service 直接拼 SQL。`platform/identity/governance.go` 接受已经授权的系统 actor 和 expected user version，使用 `User.TransitionForGovernance`、device revoke、recovery revoke 与 durable outbox，返回影响摘要。

- [ ] **Step 4: 实现 preview/execute 一致性**

preview 返回短 TTL、actor/filter/target/version 绑定的 token。execute 重新读取资源并验证 preview digest；版本变化返回冲突，不沿用旧影响摘要。

- [ ] **Step 5: 实现注销状态机的第一阶段**

事务内标记 user deleted、撤销设备/恢复凭据/辅助 grant、保留 username claim 90 天并创建 `admin_user_erasure_jobs`；房间移除通过 outbox。重复 operation 返回同一任务，不创建第二个擦除任务。

- [ ] **Step 6: 运行测试并提交**

```powershell
go test -race ./platform/identity ./platform/admin/user
git add platform/identity platform/admin/user
git commit -m "feat(admin-user): 完成用户治理与注销预览"
```

## Task 5: 实现批量任务与注销擦除 worker

**Files:**
- Create: `platform/admin/user/batch.go`
- Create: `platform/admin/user/erasure.go`
- Create: `apps/worker/internal/adminjobs/dispatcher.go`
- Create: `apps/worker/internal/adminjobs/dispatcher_test.go`
- Modify: `apps/worker/internal/runtime/runtime.go`
- Modify: `apps/worker/internal/runtime/runtime_test.go`
- Modify: `apps/worker/internal/application/application.go`
- Modify: `apps/worker/internal/config/config.go`
- Modify: `apps/worker/internal/config/config_test.go`
- Test: `platform/admin/user/batch_test.go`
- Test: `platform/admin/user/erasure_test.go`

- [ ] **Step 1: 写失败的任务状态机测试**

覆盖固化筛选/显式目标、逐项 claim、部分失败、取消只阻止未 claim 项、失败项安全重试、worker 崩溃后 lease 回收、重复 operation 不重复副作用，以及擦除 PII/tag/note 后历史业务只保留稳定 user ID。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/admin/user ./apps/worker/internal/adminjobs ./apps/worker/internal/runtime
```

- [ ] **Step 3: 实现 preview/start/job/item 协议**

preview 保存规范化筛选、可见数据上界、目标版本集合和短 TTL。start 要求有效 preview 与 `users.bulk_governance` elevation。worker 每次 claim 有界批次，单 item 使用稳定 operation ID 调用与单用户相同的治理服务。

- [ ] **Step 4: 实现 PII 擦除**

擦除 worker 清除 `user_profiles` 密文、标签关联、备注正文和可恢复认证材料；审计/游戏/房间保留 ID 与删除遮蔽语义。擦除步骤逐项幂等并在审计中记录字段类别，不记录原值。

- [ ] **Step 5: 串行接入现有 worker runtime**

现有 runtime 已串行 checkpoint/rotation/cleanup；将 admin job dispatcher 加入有界 pass，防止一种任务长期饿死其他任务。配置常量说明 poll/lease/batch 单位和上限。

- [ ] **Step 6: 运行测试并提交**

```powershell
go test -race ./platform/admin/user ./apps/worker/internal/adminjobs ./apps/worker/internal/runtime ./apps/worker/internal/application
git add platform/admin/user apps/worker
git commit -m "feat(admin-user): 增加批量治理与注销擦除任务"
```

## Task 6: 建立加密导出结果存储和单次下载 grant

**Files:**
- Create: `platform/admin/export/model.go`
- Create: `platform/admin/export/service.go`
- Create: `platform/admin/export/repository.go`
- Create: `platform/admin/export/cipher.go`
- Create: `platform/persistence/objectstorage/resultstore/store.go`
- Create: `platform/persistence/objectstorage/resultstore/local.go`
- Create: `platform/persistence/objectstorage/resultstore/s3.go`
- Create: `apps/internal/exportstorage/config.go`
- Create: `apps/internal/exportstorage/config_test.go`
- Modify: `apps/internal/config/types.go`
- Modify: `apps/internal/config/config.go`
- Modify: `platform/security/bundle.go`
- Modify: `deploy/.env.example`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/docker-compose.standalone.yml`
- Test: `platform/admin/export/service_test.go`
- Test: `platform/admin/export/cipher_test.go`
- Test: `platform/persistence/objectstorage/resultstore/local_test.go`
- Test: `platform/persistence/objectstorage/resultstore/s3_test.go`

- [ ] **Step 1: 写失败的导出安全测试**

覆盖：筛选/字段/遮蔽/数据上界固化；envelope AAD 包含 export/admin/schema/filter；对象读取后校验 digest；grant 绑定 session/export/TTL 且一次消费；删除和 24 小时过期；下载失败不延长 TTL。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/admin/export ./platform/persistence/objectstorage/resultstore ./apps/internal/exportstorage
```

- [ ] **Step 3: 实现可删除结果存储，不复用 WORM checkpoint sink**

现有 `objectstorage.Sink` 是 append-only/Object Lock 语义。新 `resultstore.Store` 只用于短期加密导出，提供 `Put/Get/Delete/CheckReady`，使用独立 bucket/prefix/credentials 配置，不降低审计 checkpoint WORM 约束。

- [ ] **Step 4: 实现导出 envelope 与 grant**

数据库只保存对象 key、密文 digest、wrapped key/version 和状态。grant 只以摘要持久化，不放 URL；成功消费与 replay 拒绝都写审计。

- [ ] **Step 5: 运行测试并提交**

```powershell
go test -race ./platform/admin/export ./platform/persistence/objectstorage/resultstore ./apps/internal/exportstorage
git add platform/admin/export platform/persistence/objectstorage/resultstore apps/internal/exportstorage apps/internal/config platform/security deploy
git commit -m "feat(admin-user): 建立加密导出与单次下载授权"
```

## Task 7: 实现用户导出 worker 与固定下载端点

**Files:**
- Create: `apps/worker/internal/adminexport/dispatcher.go`
- Create: `apps/worker/internal/adminexport/dispatcher_test.go`
- Modify: `apps/worker/internal/application/application.go`
- Modify: `apps/worker/internal/runtime/runtime.go`
- Create: `apps/api/internal/transport/adminuser/service.go`
- Create: `apps/api/internal/transport/adminuser/service_test.go`
- Create: `apps/api/internal/transport/adminexport/download.go`
- Create: `apps/api/internal/transport/adminexport/download_test.go`
- Modify: `apps/api/internal/server/surface.go`
- Modify: `apps/api/internal/server/surface_test.go`
- Modify: `apps/api/internal/application/application.go`
- Modify: `apps/edge/internal/server/server.go`
- Modify: `apps/edge/internal/server/server_test.go`
- Modify: `apps/admin/vite.config.ts`

- [ ] **Step 1: 写失败的 worker/transport 测试**

断言 worker 按固化上界生成 CSV、PII 遮蔽正确、结果加密后再写对象；Connect handler 不返回公开对象 URL；固定 POST 下载端点要求 session+header grant、设置 no-store/content-disposition 并原子消费 grant。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/worker/internal/adminexport ./apps/api/internal/transport/adminuser ./apps/api/internal/transport/adminexport ./apps/api/internal/server ./apps/edge/internal/server
```

- [ ] **Step 3: 实现用户导出 dispatcher**

使用游标分批读取固化用户 ID，流式写临时加密结果，成功后原子更新 job。失败留下稳定错误键；明文临时文件在 defer/进程恢复清理路径中删除。

- [ ] **Step 4: 实现 Connect adapter 和下载 handler**

adapter 从 `ActorContext` 取身份，不接受 wire actor。下载 endpoint 使用固定路径 `/admin/exports/download`，grant 放 `X-Admin-Download-Grant` header，edge 只允许该精确路径和 POST。

- [ ] **Step 5: 运行测试并提交**

```powershell
go test -race ./apps/worker/internal/adminexport ./apps/api/internal/transport/adminuser ./apps/api/internal/transport/adminexport ./apps/api/internal/server ./apps/edge/internal/server
git add apps/worker apps/api apps/edge apps/admin/vite.config.ts
git commit -m "feat(admin-user): 接入异步用户导出与受控下载"
```

## Task 8: 重建用户中心前端

**Files:**
- Create: `apps/admin/src/api/admin-user.ts`
- Create: `apps/admin/src/views/users/UserCenterView.vue`
- Create: `apps/admin/src/views/users/user-query-store.ts`
- Create: `apps/admin/src/views/users/user-url-state.ts`
- Create: `apps/admin/src/views/users/components/UserFilters.vue`
- Create: `apps/admin/src/views/users/components/UserTable.vue`
- Create: `apps/admin/src/views/users/components/UserDetailsDrawer.vue`
- Create: `apps/admin/src/views/users/components/UserAnnotations.vue`
- Create: `apps/admin/src/views/users/components/UserDevices.vue`
- Create: `apps/admin/src/views/users/components/UserCommandDialog.vue`
- Create: `apps/admin/src/views/users/components/BatchOperationPanel.vue`
- Create: `apps/admin/src/views/users/components/ExportJobsPanel.vue`
- Delete: `apps/admin/src/views/users/UserWorkbenchView.vue`
- Delete: `apps/admin/src/components/users/RealNameDialog.vue`
- Delete: `apps/admin/src/components/users/UserDetails.vue`
- Delete: `apps/admin/src/components/users/UserLookupForm.vue`
- Delete: `apps/admin/src/components/users/UserStatusDialog.vue`
- Create: `apps/admin/src/components/CommandResult.vue`
- Create: `apps/admin/src/composables/use-request-generation.ts`
- Modify: `apps/admin/src/router/routes.ts`
- Modify: `apps/admin/src/constants/navigation.ts`
- Modify: `apps/admin/src/stores/navigation.ts`
- Create: `apps/admin/tests/user-query-store.test.ts`
- Replace: `apps/admin/tests/user-workbench.test.ts`

- [ ] **Step 1: 写失败的 store/组件测试**

覆盖 URL 筛选/排序/cursor、AbortController 竞态、详情按 ID 恢复、基础详情不含 PII、通过独立 `GetUserPII` 携带 reason 明确请求、标签备注、命令 preview、elevation 回来后重校验、批量/导出刷新恢复和错误状态。

- [ ] **Step 2: 运行测试确认失败**

```powershell
pnpm --filter @game-night/admin test -- user-query-store.test.ts user-workbench.test.ts
```

- [ ] **Step 3: 实现高密度列表与详情抽屉**

页面采用筛选栏+表格+详情抽屉。表格固定操作列和状态尺寸；窄屏折叠筛选、详情单列但不隐藏关键命令。打开详情只调用 `GetUser`；管理员点击 PII 区域并填写 reason 后才调用 `GetUserPII`。PII 不在列表/详情预取，详情关闭、切换用户或权限变化时立即清空。

- [ ] **Step 4: 实现治理、批量与导出工作流**

命令先 preview，再按矩阵请求 elevation，最后 execute。业务参数在 step-up 前保留于组件内存，但关闭/路由变化后清理。批量工具栏只在选择/筛选 preview 后出现；任务/导出面板可在刷新后恢复。

- [ ] **Step 5: 完成后加入导航**

只有本任务测试通过后，才把“用户中心”加入侧栏和 layout tab allowlist；权限使用生成的 `USERS_READ`。

- [ ] **Step 6: 运行前端验证并提交**

```powershell
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
git add apps/admin
git commit -m "feat(admin-ui): 交付完整用户中心"
```

## Task 9: 用户中心集成与浏览器验收

**Files:**
- Modify: `apps/api/internal/application/integration_test.go`
- Create: `apps/admin/e2e/user-center.spec.ts`
- Create: `apps/admin/e2e/user-export.spec.ts`

- [ ] **Step 1: 增加真实后端集成测试**

覆盖分页 token 不能跨筛选复用、PII 访问审计、标签 CAS、备注追加、治理幂等、批量部分失败、注销阻塞/擦除、导出 grant 单次与过期。

- [ ] **Step 2: 增加 Playwright 流程**

使用真实数据库种子验证列表筛选、详情、PII、标注、设备撤销、封禁/解封、批量、导出下载和页面刷新恢复。

- [ ] **Step 3: 运行阶段门禁**

```powershell
pnpm generate
pnpm check:generated
go test -race ./platform/admin/user ./platform/admin/export ./platform/identity ./platform/persistence/postgres ./apps/worker/... ./apps/api/...
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
pnpm check:boundaries
git diff --check
```

- [ ] **Step 4: 运行 E2E 并提交测试**

```powershell
$env:ADMIN_E2E='1'; pnpm --filter @game-night/admin test:e2e -- --grep "用户中心|用户导出"
git add apps/api/internal/application/integration_test.go apps/admin/e2e
git commit -m "test(admin-user): 覆盖治理批量与导出闭环"
```

## 完成门禁

- 用户中心的列表、详情、PII、标签、备注、设备、治理、注销、批量和导出全部连接真实后端；`GetUser` 永不返回 PII，PII 只经带 reason/访问审计的 `GetUserPII` 返回。
- 所有写命令具有 operation/version/reason，矩阵要求的命令强制 elevation。
- 注销不硬删历史业务，PII 擦除可恢复、可审计且幂等。
- 下载无公开 URL，grant 单次/session 绑定，结果按 TTL 删除。
- 用户中心进入导航且无旧精确查询工作台残留。
