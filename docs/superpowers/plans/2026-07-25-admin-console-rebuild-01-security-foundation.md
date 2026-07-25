# 管理后台安全基础与壳层实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用默认关闭且可自主启停的账户级 2FA、细粒度权限、短时提权和完整安全设置替换旧管理员认证状态机，并让后台暂时只暴露已闭环的安全模块。

**Architecture:** `platform/admin` 保留为管理员安全域，使用不可变聚合、CAS repository、签名审计与一次性 secret result。API 的管理员 interceptor 统一解析 Host/Origin/CSRF/session，构造服务端 `ActorContext` 并按 procedure policy 校验权限/elevation；前端不再解释通用 `next_step`，而是根据明确的登录响应和 session introspection 恢复状态。

**Tech Stack:** Go、Connect、Protobuf、PostgreSQL/sqlc/goose、Argon2id、TOTP、Vue 3、Pinia、Naive UI、Vitest、Playwright。

---

## Task 1: 替换管理通用契约与认证契约

**Files:**
- Create: `contracts/platform/admin/v1/admin_common.proto`
- Modify: `contracts/platform/admin/v1/admin_auth.proto`
- Delete: `contracts/platform/admin/v1/admin_identity.proto`
- Modify: `contracts/platform/common/v1/error.proto`
- Regenerate: `contracts/gen/go/platform/admin/v1/**`
- Regenerate: `contracts/gen/ts/platform/admin/v1/**`
- Test: `apps/api/internal/server/surface_test.go`
- Test: `apps/edge/internal/server/server_test.go`

- [ ] **Step 1: 写失败的契约/路由测试**

在 `surface_test.go` 中声明新 `AdminSurfaceConfig` 只要求 `AdminAuthServiceHandler`，并断言旧 `AdminIdentityService` procedure 返回 404；在 edge 测试中断言新 Auth procedure 可代理、旧 procedure 不再代理。此时因生成 handler 和 surface API 未更新而编译失败。

- [ ] **Step 2: 运行测试确认失败**

Run:

```powershell
go test ./apps/api/internal/server ./apps/edge/internal/server
```

Expected: FAIL，缺少新消息/handler 或 `AdminSurfaceConfig` 签名不匹配。

- [ ] **Step 3: 定义稳定权限、scope 与认证响应**

`admin_common.proto` 至少定义：

```proto
enum AdminPermission {
  ADMIN_PERMISSION_UNSPECIFIED = 0;
  ADMIN_PERMISSION_OVERVIEW_READ = 1;
  ADMIN_PERMISSION_USERS_READ = 2;
  ADMIN_PERMISSION_USERS_READ_PII = 3;
  ADMIN_PERMISSION_USERS_ANNOTATE = 4;
  ADMIN_PERMISSION_USERS_GOVERN = 5;
  ADMIN_PERMISSION_USERS_EXPORT = 6;
  ADMIN_PERMISSION_ROOMS_READ = 7;
  ADMIN_PERMISSION_ROOMS_CONTROL = 8;
  ADMIN_PERMISSION_GAMES_READ = 9;
  ADMIN_PERMISSION_GAMES_CONTROL = 10;
  ADMIN_PERMISSION_GAMES_REPAIR = 11;
  ADMIN_PERMISSION_SECURITY_READ = 12;
  ADMIN_PERMISSION_SECURITY_MANAGE_PASSWORD = 13;
  ADMIN_PERMISSION_SECURITY_MANAGE_MFA = 14;
  ADMIN_PERMISSION_SECURITY_MANAGE_SESSIONS = 15;
  ADMIN_PERMISSION_AUDIT_READ = 16;
  ADMIN_PERMISSION_AUDIT_EXPORT = 17;
  ADMIN_PERMISSION_OPERATIONS_READ = 18;
  ADMIN_PERMISSION_OPERATIONS_MAINTAIN = 19;
}

enum AdminElevationScope {
  ADMIN_ELEVATION_SCOPE_UNSPECIFIED = 0;
  ADMIN_ELEVATION_SCOPE_USERS_BULK_GOVERNANCE = 1;
  ADMIN_ELEVATION_SCOPE_USERS_REVOKE_DEVICES = 2;
  ADMIN_ELEVATION_SCOPE_USERS_DELETE = 3;
  ADMIN_ELEVATION_SCOPE_ROOMS_FORCE_CLOSE = 4;
  ADMIN_ELEVATION_SCOPE_GAMES_FORCE_TERMINATE = 5;
  ADMIN_ELEVATION_SCOPE_GAMES_EMERGENCY_REPAIR = 6;
  ADMIN_ELEVATION_SCOPE_OPERATIONS_MAINTENANCE = 7;
  ADMIN_ELEVATION_SCOPE_SECURITY_DISABLE_MFA = 8;
  ADMIN_ELEVATION_SCOPE_SECURITY_REGENERATE_RECOVERY_CODES = 9;
  ADMIN_ELEVATION_SCOPE_SECURITY_REVOKE_SESSIONS = 10;
  ADMIN_ELEVATION_SCOPE_AUDIT_EXPORT_SENSITIVE = 11;
}
```

`admin_auth.proto` 只保留 `bootstrap_pending/setup_required/active` 账户状态和 `setup_password_pending/mfa_pending/full` session kind。登录密码响应使用明确的 `requires_initial_password_change`、`requires_mfa` 或 full session oneof；session summary 直接包含权限、`mfa_enabled`、恢复码余量、当前 elevation 摘要和版本，不再返回 `next_step`。

会话命令在契约层明确拆分：`RevokeAdminSession` 只撤销一个指定的其他 session，携带 operation ID、目标 session ID 和 expected session version，不要求 elevation；`PreviewRevokeOtherAdminSessions` 返回当前 session 之外的精确影响数量/摘要与当前 admin/session version；`RevokeOtherAdminSessions` 必须携带 preview version、operation ID、expected admin version、expected current session version，并要求 `security.revoke_sessions` elevation。该 scope 在 2FA 开启时只接受密码 + TOTP，不允许恢复码替代。

- [ ] **Step 4: 增加稳定错误键**

为缺少/过期 elevation、版本冲突、一次性秘密过期、恢复码耗尽、MFA 状态冲突和幂等冲突增加 `BusinessErrorCode`；不把内部错误字符串暴露给前端。

- [ ] **Step 5: 生成契约并清理旧生成物**

Run:

```powershell
pnpm generate:contracts
pnpm check:generated
```

Expected: PASS；`admin_identity_pb.*` 和对应 Connect 生成文件已删除，新 common/auth 生成文件存在。

- [ ] **Step 6: 提交**

```powershell
git add contracts apps/api/internal/server/surface_test.go apps/edge/internal/server/server_test.go
git commit -m "feat(admin-auth): 重建管理认证与权限契约"
```

## Task 2: 建立新认证数据模型和破坏性开发迁移

**Files:**
- Create: `infra/migrations/00028_admin_console_security.sql`
- Modify: `tooling/sqlc/queries/admin.sql`
- Regenerate: `platform/persistence/postgres/sqlcgen/admin.sql.go`
- Regenerate: `platform/persistence/postgres/sqlcgen/models.go`
- Modify: `platform/persistence/postgres/admin_mapping.go`
- Modify: `platform/persistence/postgres/admin_repository.go`
- Modify: `platform/persistence/postgres/admin_unit_of_work.go`
- Test: `platform/persistence/postgres/admin_repository_integration_test.go`
- Test: `platform/persistence/postgres/maintenance_integration_test.go`

- [ ] **Step 1: 写迁移后的持久化失败测试**

覆盖：账号不再接受 `recovery_pending`；同一管理员最多一个 active enrollment；新 session 保存 `session_version/client_ip/user_agent`；同一 session/scope 最多一个未过期 elevation；grant 存储 admin/password/session 版本；安全状态变化后旧 grant 读取失败。

- [ ] **Step 2: 运行集成测试确认失败**

Run:

```powershell
go test -run "TestAdmin.*(Elevation|Session|Enrollment|Recovery)" ./platform/persistence/postgres
```

Expected: FAIL，缺少表/列/query 或旧约束仍接受旧状态。

- [ ] **Step 3: 编写新增迁移，不改历史迁移**

迁移必须：

- 撤销所有现有管理员 session/challenge/恢复码并清除 pending enrollment 密文。
- 将单例账号恢复为 `setup_required`，删除 `recovery_pending` 与账号级 TOTP replay floor。
- 把 replay floor、`enrollment_version` 放到 active enrollment。
- 把 session kind 收敛为 setup/MFA/full，并增加 session version、客户端摘要字段。
- 创建 `admin_elevation_grants`，绑定 admin/session/admin version/password version/session version/scope/TTL/revoke state。
- 创建 `admin_command_receipts`，以 `(admin_id, operation_id)` 唯一约束保存 request digest、命令类型、目标、结果版本和 audit event ID。
- 删除旧 `admin_assisted_recovery_grants` 及其 recovery attempt FK；保留用户恢复凭据本身。
- 保留 `user_profiles`，只删除旧同步 `profile_export_contexts/items`，后续用异步导出替代。
- 更新 runtime/worker/audit writer 数据库权限，默认拒绝新表的 PUBLIC 权限。

- [ ] **Step 4: 更新 sqlc queries 与 repository CAS**

所有 grant/session/enrollment 更新包含旧版本条件并检查返回行。过期或版本不匹配映射为领域错误，不泄漏 pgx 诊断。

- [ ] **Step 5: 生成 SQL 并运行测试**

Run:

```powershell
pnpm generate:sql
go test ./platform/persistence/postgres
```

Expected: PASS。

- [ ] **Step 6: 提交**

```powershell
git add infra/migrations/00028_admin_console_security.sql tooling/sqlc/queries/admin.sql platform/persistence/postgres
git commit -m "feat(admin-auth): 建立会话与提权持久模型"
```

## Task 3: 收敛账户、会话、2FA 与 elevation 聚合

**Files:**
- Modify: `platform/admin/model.go`
- Modify: `platform/admin/session.go`
- Modify: `platform/admin/totp.go`
- Modify: `platform/admin/recovery.go`
- Modify: `platform/admin/authorization.go`
- Create: `platform/admin/elevation.go`
- Create: `platform/admin/actor.go`
- Modify: `platform/admin/errors.go`
- Test: `platform/admin/model_test.go`
- Test: `platform/admin/password_only_test.go`
- Create: `platform/admin/elevation_test.go`

- [ ] **Step 1: 把已确认规则写成失败的领域测试**

测试至少覆盖：

- 新/重置账号 2FA 默认关闭，active 状态可以没有 enrollment。
- 开启/关闭 2FA 只改变 enrollment 与 admin version，不改变密码。
- 修改密码保留 active enrollment。
- setup session 只能改初始密码；MFA pending 只能完成 MFA；full 才有业务权限。
- elevation 最长五分钟并绑定 scope、session/admin/password/session version。
- 只有关闭 2FA 与重新生成恢复码 scope 允许恢复码代替 TOTP。
- TTL、session 撤销、改密、2FA 变化或版本变化使 grant 无效。

- [ ] **Step 2: 运行领域测试确认失败**

Run:

```powershell
go test ./platform/admin
```

Expected: FAIL，旧 `recovery_pending`/强制 enrollment 断言与新测试冲突。

- [ ] **Step 3: 实现明确状态与默认拒绝权限**

`ActorContext` 只由服务端构造，并提供窄方法：

```go
type ActorContext struct {
    AdminID      uuid.UUID
    SessionID    uuid.UUID
    Session      Session
    Permissions  PermissionSet
    Elevations   ElevationSet
    RequestID    string
    Origin       string
    ClientIP     string
    UserAgent    string
}

func (actor ActorContext) Require(permission Permission) error
func (actor ActorContext) RequireElevation(scope ElevationScope, at time.Time) error
```

调用者不能修改 permission/elevation 集合；未知 permission/scope 一律拒绝。

- [ ] **Step 4: 删除旧环境策略语义的领域分支**

删除 `AllowPasswordOnly`、`AccountStatusRecoveryPending`、`SessionKindTOTPEnrollmentPending` 和 `SessionKindRecoveryPending`。`passwordLoginSessionState` 只根据账号状态和 active enrollment 决定 full 或 MFA pending。

- [ ] **Step 5: 运行测试并提交**

```powershell
go test ./platform/admin
git add platform/admin
git commit -m "feat(admin-auth): 实现默认关闭二次验证与短时提权"
```

## Task 4: 重写认证、安全命令和一次性恢复码流程

**Files:**
- Split/Modify: `platform/admin/service.go`
- Create: `platform/admin/login_service.go`
- Create: `platform/admin/password_service.go`
- Create: `platform/admin/mfa_service.go`
- Create: `platform/admin/session_service.go`
- Create: `platform/admin/elevation_service.go`
- Modify: `platform/admin/ports.go`
- Modify: `platform/admin/repository.go`
- Modify: `platform/admin/challenge.go`
- Test: `platform/admin/challenge_test.go`
- Test: `platform/admin/current_session_test.go`
- Replace: `platform/admin/password_only_test.go`
- Replace: `platform/admin/totp_rotation_test.go`
- Create: `platform/admin/security_service_test.go`

- [ ] **Step 1: 写端到端领域用例的失败测试**

测试以下事务结果：首次改密直接签发 full session 且 MFA 关闭；有 enrollment 的登录签发 MFA pending；TOTP/恢复码验证升级为 full；普通改密撤销其他 session 并刷新当前 session；开启/关闭 2FA 撤销其他 session；恢复码重新生成原子撤销旧集合并可在 TTL 内重放同一 secret result。

会话治理单独覆盖：撤销一个其他 session 使用目标 session version 且不撤销当前 session；preview 固化其他 session 数量、目标摘要和当前 admin/session version；撤销其他全部 session 要求同一 preview、operation ID、`security.revoke_sessions` elevation 与 expected admin/current-session version，资源变化时拒绝。2FA 开启时该 elevation 必须由密码 + TOTP 签发，恢复码明确拒绝；重复 operation 返回原结果；签名审计失败时不撤销任何 session。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./platform/admin
```

- [ ] **Step 3: 按职责拆分旧大 service**

每个 exported command 接受明确 command struct，校验 operation ID、expected version、原因和 actor。`RevokeSessionCommand` 与 `Preview/RevokeOtherSessionsCommand` 使用不同类型，后者不接受前端传入目标集合，只按服务端 preview 重新加载“除当前 session 外”的目标。安全命令在一个 UOW 中完成状态变化、session/grant 撤销、secret result 和签名审计；审计写失败必须回滚业务变化。

- [ ] **Step 4: 实现一次性结果语义**

开启 2FA 和重新生成恢复码使用现有 `platform/secretresult` envelope：相同 operation/digest 在秘密 TTL 内返回同一结果；确认或过期擦除密文；不同 digest 返回幂等冲突。日志只记录 operation/result ID。

- [ ] **Step 5: 运行竞态与领域测试**

```powershell
go test -race ./platform/admin
```

Expected: PASS。

- [ ] **Step 6: 提交**

```powershell
git add platform/admin
git commit -m "feat(admin-auth): 完成密码二次验证与会话安全流程"
```

## Task 5: 建立统一管理员 interceptor 与 procedure policy

**Files:**
- Modify: `apps/api/internal/transport/adminauth/interceptor.go`
- Modify: `apps/api/internal/transport/adminauth/effects.go`
- Modify: `apps/api/internal/transport/adminauth/handler.go`
- Modify: `apps/api/internal/transport/adminauth/readiness.go`
- Delete: `apps/api/internal/transport/adminidentity/handler.go`
- Delete: `platform/admin/connect_service.go`
- Delete: `platform/admin/identity_connect_service.go`
- Delete: `platform/admin/identity_service.go`
- Delete: `platform/admin/identity_service_test.go`
- Modify: `platform/admin/current_session_test.go`
- Create: `apps/api/internal/transport/adminauth/policy.go`
- Create: `apps/api/internal/transport/adminauth/context.go`
- Modify: `apps/api/internal/transport/sensitive/registry.go`
- Modify: `apps/api/internal/transport/errors/mapper.go`
- Modify: `apps/api/internal/server/surface.go`
- Modify: `apps/api/internal/application/application.go`
- Modify: `apps/edge/internal/server/server.go`
- Modify: `apps/admin/vite.config.ts`
- Test: `apps/api/internal/transport/adminauth/handler_test.go`
- Create: `apps/api/internal/transport/adminauth/policy_test.go`
- Modify: `apps/api/internal/server/surface_test.go`
- Modify: `apps/edge/internal/server/server_test.go`

- [ ] **Step 1: 写 procedure 覆盖与伪造上下文失败测试**

断言每个生成的 Auth procedure 都有唯一 policy；匿名 procedure 不读取 session；受保护 procedure 必须从 Cookie 认证且前端传入的 admin/permission/elevation 字段无效；写 procedure 校验 CSRF；未知 procedure 默认拒绝。`RevokeAdminSession` 与只读的 `PreviewRevokeOtherAdminSessions` 只要求 `security.manage_sessions`；真正执行 all-other revoke 额外要求 `security.revoke_sessions` elevation，不能沿用旧 `LogoutAllAdminSessions` policy。API transport 测试接管旧 `ConnectAdminService` 的 wire、Cookie 和错误映射断言，`platform/admin` 测试只保留领域 session 行为。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/api/internal/transport/adminauth ./apps/api/internal/server ./apps/edge/internal/server
```

- [ ] **Step 3: 实现 transport context 与 actor context 分层**

interceptor 先规范化 Origin、IP、User-Agent、request ID，再调用 admin service 验证 session 并附加只读 `ActorContext`。把 `platform/admin/connect_service.go` 中仍需要的 transport context、wire 转换、Cookie effects 和错误映射迁入 `apps/api/internal/transport/adminauth`；handler 不重复读取权限，`platform/admin` 不再依赖 Connect 或生成的 protobuf 类型。

- [ ] **Step 4: 更新 surface/edge/Vite 路由**

阶段内管理员 surface 只挂载 Auth service；edge 和 Vite 只代理新 Auth prefix。删除旧 `platform/admin/connect_service.go`、AdminIdentity transport/domain adapter 及其旧状态 wire 映射，旧 Identity prefix 返回 404，不加兼容转发。

- [ ] **Step 5: 更新错误映射与测试**

elevation 缺失返回稳定 failed-precondition detail；认证/CSRF/权限分别映射，不把内部状态混成统一文本。

- [ ] **Step 6: 运行测试并提交**

```powershell
go test ./platform/admin ./apps/api/internal/transport/adminauth ./apps/api/internal/server ./apps/edge/internal/server
git add apps/api apps/edge apps/admin/vite.config.ts platform/admin/connect_service.go platform/admin/current_session_test.go platform/admin/identity_connect_service.go platform/admin/identity_service.go platform/admin/identity_service_test.go
git commit -m "feat(admin-auth): 统一管理请求身份与权限校验"
```

## Task 6: 更新配置、离线重置和部署清单

**Files:**
- Modify: `apps/api/internal/config/config.go`
- Modify: `apps/api/internal/config/config_test.go`
- Modify: `apps/adminctl/main.go`
- Modify: `apps/adminctl/main_test.go`
- Modify: `platform/persistence/postgres/admin_reset.go`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/docker-compose.standalone.yml`
- Modify: `deploy/.env.example`
- Modify: `deploy/README.md`
- Test: `apps/api/internal/application/integration_test.go`

- [ ] **Step 1: 写移除环境 MFA 开关与离线重置失败测试**

配置测试断言 `GAME_NIGHT_API_ADMIN_MFA_REQUIRED` 不再被读取；integration test 断言 reset 后账号为 setup_required、MFA 关闭、所有 session/grant/recovery code 撤销。

- [ ] **Step 2: 运行测试确认失败**

```powershell
go test ./apps/api/internal/config ./apps/adminctl ./apps/api/internal/application
```

- [ ] **Step 3: 删除环境变量和旧测试分支**

从 API config、compose、env example 和文档删除 MFA-required 变量。2FA 状态只来自 active enrollment。

- [ ] **Step 4: 更新 `adminctl reset`**

离线 reset 继续使用迁移角色和签名审计链，事务内推进账号/密码/admin version、撤销认证数据、MFA 和 elevation，并输出不含秘密的结果摘要。

- [ ] **Step 5: 运行测试并提交**

```powershell
go test ./apps/api/internal/config ./apps/adminctl ./apps/api/internal/application
git add apps/api/internal/config apps/api/internal/application apps/adminctl platform/persistence/postgres/admin_reset.go deploy
git commit -m "feat(admin-auth): 改用账户级二次验证配置"
```

## Task 7: 重建前端登录、session store 与安全设置

**Files:**
- Modify: `apps/admin/src/api/connect.ts`
- Replace: `apps/admin/src/api/admin-auth.ts`
- Replace: `apps/admin/src/stores/auth.ts`
- Replace: `apps/admin/src/router/guards.ts`
- Replace: `apps/admin/src/router/routes.ts`
- Modify: `apps/admin/src/constants/navigation.ts`
- Replace: `apps/admin/src/views/auth/AdminAuthView.vue`
- Replace: `apps/admin/src/views/security/SessionSecurityView.vue`
- Create: `apps/admin/src/views/security/ChangePasswordDialog.vue`
- Create: `apps/admin/src/views/security/TotpSetupDialog.vue`
- Create: `apps/admin/src/views/security/DisableTotpDialog.vue`
- Create: `apps/admin/src/views/security/RecoveryCodesDialog.vue`
- Create: `apps/admin/src/views/security/ElevationDialog.vue`
- Create: `apps/admin/src/views/security/AdminSessionsTable.vue`
- Create: `apps/admin/src/views/security/RevokeOtherSessionsDialog.vue`
- Create: `apps/admin/src/views/auth/components/AdminLoginForm.vue`
- Create: `apps/admin/src/views/auth/components/InitialPasswordForm.vue`
- Create: `apps/admin/src/views/auth/components/MfaChallengeForm.vue`
- Delete: `apps/admin/src/components/auth/BootstrapPendingState.vue`
- Delete: `apps/admin/src/components/auth/ChangePasswordStep.vue`
- Delete: `apps/admin/src/components/auth/LoginPasswordStep.vue`
- Delete: `apps/admin/src/components/auth/MfaVerificationStep.vue`
- Delete: `apps/admin/src/components/auth/SecretReceiptStep.vue`
- Delete: `apps/admin/src/components/auth/TotpEnrollmentStep.vue`
- Delete: `apps/admin/src/api/admin-identity.ts`
- Test: `apps/admin/tests/auth-store.test.ts`
- Test: `apps/admin/tests/auth-view.test.ts`
- Create: `apps/admin/tests/security-view.test.ts`
- Modify: `apps/admin/tests/navigation-store.test.ts`

- [ ] **Step 1: 重写失败的 auth store 测试**

覆盖：无 session -> 登录；setup session -> 首次改密；MFA pending -> TOTP/恢复码；full session -> security；session introspection 直接提供权限/MFA/elevation；并发恢复只允许最新响应落地；route change/登出清理秘密。

- [ ] **Step 2: 运行 Vitest 确认失败**

```powershell
pnpm --filter @game-night/admin test -- auth-store.test.ts auth-view.test.ts security-view.test.ts
```

- [ ] **Step 3: 重建 API 与 store**

`auth.ts` 只持久存在 session、permissions、MFA/elevation 摘要；password/TOTP/recovery secret 只保存在对话框组件内存。`callUnary` 为所有受保护请求设置 request ID，并保留 AbortSignal 与稳定错误 detail。

- [ ] **Step 4: 实现登录和安全页**

登录首屏只显示密码。服务端返回 MFA required 后才显示 TOTP/恢复码。安全页完成改密、2FA 开关、恢复码余量/重生成和 session/elevation 管理。单行“撤销会话”只提交目标 ID/version；“撤销其他全部会话”必须先展示服务端影响预览，再获取 `security.revoke_sessions` elevation，并在确认时提交 operation ID 与当前 admin/session version。所有危险操作使用 `AppDialog.toggleDialog`，关闭时 abort 请求并清理表单/秘密。

- [ ] **Step 5: 阶段内只暴露安全模块**

从 routes/navigation 中移除旧空架子模块，旧用户、审计和概览文件留给各自阶段按精确清单删除。`/` 暂时重定向到 `/security`，侧栏只显示“安全设置”；后续计划在模块闭环时逐项加入最终六模块导航。

- [ ] **Step 6: 运行前端验证**

```powershell
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
```

Expected: PASS；构建产物不含旧 `AdminNextStep`、`rebindTotp`、`AdminIdentityService` 字符串。

- [ ] **Step 7: 提交**

```powershell
git add apps/admin
git commit -m "feat(admin-ui): 重建登录与安全设置"
```

## Task 8: 完成安全阶段集成验证

**Files:**
- Modify: `apps/api/internal/application/integration_test.go`
- Create: `apps/admin/e2e/security.spec.ts`
- Modify: `apps/admin/playwright.config.ts`
- Modify: `docs/superpowers/plans/2026-07-25-admin-console-rebuild-01-security-foundation.md` (勾选执行状态时)

- [ ] **Step 1: 增加真实后端集成场景**

覆盖首次改密默认无 2FA、启用 2FA、TOTP/恢复码登录、重生成恢复码、改密保留 enrollment、关闭 2FA、撤销单个其他 session、预览并撤销其他全部 session 和 elevation 失效。全量撤销场景验证 preview/admin/current-session version、`security.revoke_sessions` scope、密码 + TOTP、拒绝恢复码和当前 session 保留。

- [ ] **Step 2: 增加 Playwright 安全流程**

使用真实本地后端，不 route mock。对秘密页面断言关闭后 DOM 不再含 seed/recovery code；刷新不恢复秘密；桌面和移动视口均可完成流程。

- [ ] **Step 3: 运行阶段门禁**

```powershell
pnpm generate
pnpm check:generated
go test -race ./platform/admin ./platform/persistence/postgres ./apps/api/internal/transport/adminauth ./apps/api/internal/application
pnpm --filter @game-night/admin check
pnpm --filter @game-night/admin test
pnpm --filter @game-night/admin build
pnpm check:boundaries
git diff --check
```

Expected: 全部 PASS。

- [ ] **Step 4: 在开发栈运行 E2E**

```powershell
$env:ADMIN_E2E='1'; pnpm --filter @game-night/admin test:e2e -- --project=admin-secret-flow
```

Expected: PASS。

- [ ] **Step 5: 提交阶段测试**

```powershell
git add apps/api/internal/application/integration_test.go apps/admin/e2e apps/admin/playwright.config.ts
git commit -m "test(admin-auth): 覆盖完整安全设置流程"
```

## 完成门禁

- 旧 `next_step`、rebind 强制状态、`recovery_pending` 和环境 MFA 开关已删除。
- 2FA 默认关闭且可在后台启停；改密与 2FA 解耦。
- 单个会话撤销与全量其他会话撤销是不同命令；后者具备影响预览、独立 elevation、版本保护、幂等与签名审计。
- elevation 和其它 session 管理经后端强制校验与签名审计。
- 当前导航只暴露已完成的安全设置，不保留旧管理页面。
- 定向 Go/Vitest/build/E2E 全部通过后，才能执行用户中心计划。
