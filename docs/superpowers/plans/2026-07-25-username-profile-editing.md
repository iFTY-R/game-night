# 用户资料入口与房间重名改名实施计划

> **执行要求：** 按任务顺序测试先行实施；生成文件只通过仓库生成命令更新；每个后端、客户端契约和页面交互阶段形成独立提交。
>
> **状态：** 已确认，执行中

**目标：** 为设备身份提供可发现、移动端优先的用户名修改入口，并让房间码、邀请链接和公开大厅在同房间重名时引导改名后可靠重试。

**设计依据：** [用户资料入口与房间重名改名设计](../specs/2026-07-25-username-profile-editing-design.md)

**技术栈：** Go 1.26、PostgreSQL + sqlc、ConnectRPC、Vue 3、Pinia、TypeScript、Vitest、Playwright。

## 1. 验收标准

- 首次设置用户名后可以立即改名，连续改名不受时间冷却，但每次仍经过设备认证、CSRF、限流、房间内唯一性和 PostgreSQL CAS。
- Web 错误对象从 `BusinessErrorDetail.messageKey` 保留稳定业务键，并兼容没有 details 的旧错误响应。
- 首页和 `RoomView` 的 lobby/post-game 状态显示首字头像；playing、游戏会话和独立复盘页面不显示资料入口。
- 用户名弹层在移动端为底部弹层、桌面端居中，完整支持格式校验、提交防重、焦点管理、取消和错误反馈。
- 房间码、邀请链接和公开大厅发生 `room.username.taken` 时都能主动改名并只自动重试一次原动作。
- 改名成功后本地身份绝不因列表或房间快照刷新失败而回滚，失败只产生非阻断同步提示。
- 相关 Go/Vitest/Playwright、类型检查、生成漂移和生产构建全部通过。

## 2. 实施步骤

### Task 1：移除用户名时间冷却并保留并发保护

**Files:** Modify `platform/identity/model.go`, `platform/identity/errors.go`, `platform/identity/model_test.go`, `platform/identity/service_test.go`, `tooling/sqlc/queries/identity.sql`, `platform/persistence/postgres/identity_user_repository.go`, identity PostgreSQL integration tests; regenerate `platform/persistence/postgres/sqlcgen/**`; update obsolete transport mapping references if no callers remain.

- [ ] 先修改领域测试，证明 onboarding 后立即改名成功、同一规范化键仍被拒绝、旧用户名声明继续转为 reserved。
- [ ] 删除 `UsernameChangeCooldown` 和领域 cooldown 分支；保留 active 状态、输入、同名未变化和时间单调性校验。
- [ ] 从 `ChangeCurrentUsernameCAS` 删除 `cooldown_cutoff`，继续比较旧用户名、旧 key、旧 `username_changed_at` 和旧 `updated_at`。
- [ ] 运行 `pnpm run generate:sql`，不手工修改 sqlc 生成物。
- [ ] 更新内存服务与 PostgreSQL 集成测试，证明连续改名、陈旧快照冲突和房间重名整笔回滚。

**Verify:** `go test ./platform/identity ./apps/api/internal/transport/errors`；配置测试数据库后执行 `go test -p 1 ./platform/persistence/postgres -run "Username|IdentityServicePostgresChangeUsername"`；`pnpm run check:generated`。

**Commit:** `fix(identity): 允许用户即时修改用户名`

### Task 2：建立稳定业务错误与前端改名状态契约

**Files:** Modify `apps/web/src/api/client.ts`, `apps/web/src/stores/room.ts`, `apps/web/tests/api-client.test.ts`, `apps/web/tests/room-store.test.ts`.

- [ ] 先扩充客户端测试：details 优先、旧 message 回退、本地化展示三者同时成立。
- [ ] 使用仓库已有 `@bufbuild/protobuf` 和生成的 `BusinessErrorDetailSchema` 解码 Connect detail，不增加依赖；解析失败安全回退。
- [ ] 为 `ApiError` 增加只读 `businessKey`，现有 `status`、`code` 和本地化 `message` 保持兼容。
- [ ] 为 `identityClient` 增加 `ChangeUsername` 写调用，为 store 增加格式预检和权威响应落盘 action。
- [ ] 测试无效输入不发请求、成功写入 Pinia/localStorage、失败不污染身份，以及同步辅助流程的局部失败不会回滚新用户名。

**Verify:** `pnpm --filter @game-night/web test -- api-client.test.ts room-store.test.ts`、`pnpm --filter @game-night/web check`。

**Commit:** `feat(identity): 接入用户名修改状态契约`

### Task 3：实现共享资料按钮与用户名弹层

**Files:** Create `apps/web/src/components/ProfileTrigger.vue`, `apps/web/src/components/UsernameDialog.vue` and focused component tests; modify shared platform styles only if a token is genuinely reusable.

- [ ] 先写组件测试，覆盖首字、完整可访问名称、打开预填、格式校验、同名禁用、提交防重、Escape/遮罩/取消共用清理和焦点回收。
- [ ] `ProfileTrigger` 保持 40px 以上圆形命中区，不允许用户名长度改变顶栏几何。
- [ ] `UsernameDialog` 使用原生 dialog 语义和 Vue `Teleport`，移动端底部展开、桌面居中；处理安全区、内容滚动和 reduced motion。
- [ ] 弹层对外只暴露 `open(mode?)`/关闭及成功事件，页面不直接修改其内部表单状态。

**Verify:** 运行新增组件测试和 `pnpm --filter @game-night/web check`。

### Task 4：接入首页并统一三类进房重名恢复

**Files:** Modify `apps/web/src/views/HomeView.vue`; create/modify `apps/web/tests/home-view.test.ts`.

- [ ] 身份激活后用头像替换“设备已识别”，首次设置状态仍保留设备登录提示。
- [ ] 把房间码、邀请链接和公开房间加入操作表达为页面内待重试上下文；正常成功路径保持不变。
- [ ] 仅依据 `ApiError.businessKey` 识别同房间重名，显示明确冲突操作；用户取消时保留目标。
- [ ] 改名成功后只重试一次原加入动作；第二次失败停止并恢复手动操作，不能递归自动打开。
- [ ] 普通改名成功刷新两类房间列表；任一路刷新失败保留新身份并显示非阻断同步提示。

**Verify:** `pnpm --filter @game-night/web test -- home-view.test.ts api-client.test.ts room-store.test.ts`。

### Task 5：接入房间页并验证权威快照同步

**Files:** Modify `apps/web/src/views/RoomView.vue`, `apps/web/tests/room-view.test.ts`.

- [ ] lobby/post-game 顶栏显示头像并与人数组成稳定操作组；playing 状态隐藏。
- [ ] 改名成功后重新加载当前房间快照，并同步我的房间与公开列表。
- [ ] 当前房间刷新失败时保留新身份，显示同步提示；改名事务本身因其它房间重名失败时保留旧身份和旧房间快照。
- [ ] 更新 RoomView 测试 store，覆盖 lobby、post-game、playing、成功同步和失败不回滚。

**Verify:** `pnpm --filter @game-night/web test -- room-view.test.ts`、`pnpm --filter @game-night/web check`。

**Commit:** `feat(web): 增加用户名资料入口与重名重试`

### Task 6：全量验证与视觉检查

**Files:** Test/fixture changes only when verification exposes a real gap.

- [ ] 运行 Go 领域、传输、持久化相关测试以及 `go vet` 受影响包。
- [ ] 运行 Web 全量 Vitest、类型检查和生产构建，检查生成文件无漂移。
- [ ] 启动本地 API/Web；使用 Playwright 检查 `360x740`、`390x844`、`844x390` 和桌面视口的首页、弹层、房间 lobby/post-game/playing。
- [ ] 检查顶栏无重叠、头像稳定、弹层安全区和滚动正确、键盘与 reduced-motion 语义可用。
- [ ] 扫描最终 `git diff --check`、`git status --short` 和各提交内容，确认未包含管理后台计划或其它无关文件。

**Verify:** `go test ./platform/identity ./apps/api/internal/transport/errors ./platform/persistence/postgres`、`go vet ./platform/identity ./apps/api/internal/transport/errors ./platform/persistence/postgres`、`pnpm --filter @game-night/web test`、`pnpm --filter @game-night/web check`、`pnpm --filter @game-night/web build`、`pnpm run check:generated`、`git diff --check`。

## 3. 风险与缓解

- **Connect details 解码差异：** 测试真实 Connect JSON detail 形状；解码异常不抛第二个错误，退回安全的原始 message 兼容路径。
- **改名成功后刷新失败造成假回滚：** 先提交和持久化权威身份，再独立 `allSettled` 刷新派生数据；测试三个局部失败分支。
- **自动重试循环：** 待加入上下文记录是否已经自动重试，只有用户再次主动提交新名字才能创建下一次尝试。
- **跨房间重名事务：** 不做前端预判，继续由数据库触发器与同一身份事务裁决，保留现有集成测试。
- **移动端弹层遮挡：** 使用动态视口单位、安全区 padding 和内部滚动，并在指定竖/横屏视口截图验证。
