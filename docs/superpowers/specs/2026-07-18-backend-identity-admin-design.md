# 后端身份与管理基础设计

> 日期：2026-07-18
>
> 状态：已获方向批准，审查修订中
>
> 范围：实现正式架构第二阶段“身份与后台”的后端纵向链路，不包含用户端和管理端前端。

## 1. 目标与边界

本阶段交付第一个可运行的业务后端。用户首次访问时由服务端创建匿名待完善身份并签发设备凭证；用户设置唯一用户名后完成入驻并获得一次性展示的恢复码。用户可以在多个设备上恢复身份、查看设备和撤销设备。

后台使用独立的管理员认证上下文。系统只维护一个 `super_admin`，管理员使用密码和 TOTP MFA 登录，可以读取或修改加密保存的真实姓名、重置用户恢复凭证、撤销设备并查询审计记录。

本阶段明确不包含：

- 用户端和管理端页面。
- 房间、大厅、聊天、实时网关和游戏运行时。
- 好友、支付、内置语音或第三方账号登录。
- 多管理员、自定义角色和完整动态 RBAC。
- 邮件、短信找回和浏览器特征指纹。

这些能力没有从正式范围删除，只按总体设计的依赖顺序在后续阶段实现。

## 2. 技术选择

- Go 根 module 承载服务端代码。
- ConnectRPC + Protocol Buffers 定义和实现 API。
- `pgxpool` 管理 PostgreSQL 连接，`sqlc` 生成类型安全查询。
- `goose` 管理显式 SQL migration，由独立命令执行，不允许 API 服务在启动时自动变更 schema。
- `go-redis` 连接 Redis，仅用于限流和短期非权威状态。
- Argon2id 处理管理员密码、用户恢复秘密和管理员恢复码。
- 标准库 AES-256-GCM 加密真实姓名和 TOTP seed。
- 标准 `slog` 输出结构化日志，敏感字段不得进入日志。

具体依赖版本在实施计划中固定，并由 lockfile、生成物和 CI 共同约束。

## 3. 模块和目录

```text
apps/
├─ api/                         # 用户和管理 ConnectRPC 入口、拦截器、配置装配
├─ migrate/                     # goose migration CLI
└─ adminctl/                    # 无 HTTP 的管理员灾难恢复命令
platform/
├─ identity/                    # 用户、用户名、设备凭证、恢复流程
├─ profile/                     # 真实姓名加密和资料访问边界
├─ admin/                       # super_admin 密码、TOTP、会话和引导流程
├─ audit/                       # 仅追加审计事件和完整性链
├─ outbox/                      # 同事务领域事件和后续投递
└─ persistence/
   ├─ postgres/                 # pgxpool、sqlc 和事务适配
   └─ redis/                    # 限流存储适配
contracts/
├─ platform/identity/v1/
└─ platform/admin/v1/
infra/migrations/              # SQL migration
tooling/sqlc/                  # sqlc 配置和查询输入
```

依赖方向：

- `apps/api` 只负责配置、传输适配、Cookie 和依赖装配。
- `identity`、`profile`、`admin` 依赖各自端口，不直接导入 pgx、Redis 或 HTTP。
- `persistence` 实现领域端口，不反向依赖应用入口。
- 跨模块原子操作通过领域定义的 `UnitOfWork` 端口执行；事务回调只能使用绑定到同一事务的 repository，禁止在领域层传递 `pgx.Tx`。
- 用户认证和管理员认证不共享 Cookie、会话表、CSRF token 或限流 key。
- `super_admin` 授权通过按 permission 默认拒绝的 `AdminAuthorizer` 完成；本阶段仅把该账号映射到全部已定义权限，不把单账号或布尔型 `isAdmin` 固化进领域/API 契约。

总体设计原目录把审计列在 moderation 下。本阶段明确把跨域、不可变的审计能力提升为独立 `platform/audit`，后续 moderation 复用该模块，不再建立第二套审计存储。

## 4. 领域模型

### 4.1 用户

`User` 表示平台身份，主要字段为：

- `user_id`：UUIDv7，服务端生成。
- `status`：`onboarding`、`active`、`suspended`、`deleted`。
- `username`：用户公开看到的规范展示值，入驻前为空。
- `current_username_key`：指向统一 username claim 的外键，入驻前为空。
- `username_changed_at`：控制改名冷却期。
- `created_at`、`updated_at`。

用户名规则：

- 支持汉字、拉丁字母、十进制数字和下划线。
- Unicode NFKC 规范化并去除首尾空白。
- 拉丁字母大小写不敏感。
- 规范化后长度为 2-20 个 Unicode code point。
- 禁止控制字符、不可见格式字符、emoji、路径符号和连续下划线。
- 命中系统保留名或敏感词时拒绝。
- 所有当前用户名和历史保留名都写入同一 `username_claims` 注册表，以 `username_key` 主键裁决，并发占用只能有一个请求成功。

`username_claims` 使用 `active`、`reserved` 两种状态并始终保留 owner。`active` claim 由 `users.current_username_key` 引用；改名时在同一事务中插入新 claim、把旧 claim 改为保留 90 天并更新用户。`suspended` 用户继续持有 active claim，禁止他人占用；`deleted` 用户的 claim 进入保留期，清理任务到期后才释放。

用户主动改名最多每 30 天一次。管理员强制改名必须提供原因并写入审计。所有入驻、改名、封禁、解封、删除和保留期清理都通过同一注册表事务完成，不允许“先查两张表再写入”的流程。

### 4.2 设备凭证

每个用户可以拥有多个 `DeviceCredential`：

- `credential_id`：公开 selector，不承担保密性。
- `secret_hash`：设备秘密的 HMAC-SHA-256 摘要。
- `previous_secret_hash` 和 `previous_valid_until`：支持轮换时短暂并发宽限。
- `csrf_hash`：绑定该设备会话的 CSRF 秘密摘要。
- `label`：由客户端提交、服务端清理后的设备名称。
- `created_at`、`last_seen_at`、`rotated_at`。
- `idle_expires_at`：最后活动后 180 天。
- `absolute_expires_at`：创建后 365 天。
- `revoked_at`、`revoke_reason`。

设备 token 使用 `v1.<credential_id>.<secret>` 格式。`secret` 至少 256 bit，由密码学安全随机源生成。数据库不保存原 token 或可逆设备秘密。

凭证每 30 天轮换，身份恢复和敏感安全事件也会触发轮换。旧 secret 只保留两分钟宽限，以允许同一页面的并发请求完成。使用 previous secret 的请求只能完成当前读取或已开始的幂等操作，不能触发再次轮换、修改安全设置或覆盖新 Cookie。

`onboarding` 身份 24 小时未完成用户名设置时进入清理队列；清理必须撤销设备、删除未激活身份并写入聚合计数，不为无业务价值的匿名垃圾记录创建长期审计。`suspended` 用户只能读取封禁状态和执行退出，不能恢复、改名或管理设备；`deleted` 用户的全部设备和恢复凭证立即撤销。

### 4.3 用户恢复凭证

用户完成入驻时生成一个恢复码，只在响应中展示一次。恢复码 selector 至少 128 bit、secret 至少 128 bit。服务端按 selector 定位记录，再使用 Argon2id 校验 secret；selector 不存在时执行参数相同的 dummy Argon2id，外部错误、响应结构和可观察成本保持一致。

恢复使用 prepare/complete 两阶段协议：

1. `BeginRecovery` 校验匿名 challenge、独立限流桶和恢复码，但不消费恢复码，返回 5 分钟有效的一次性 recovery grant。
2. `CompleteRecovery` 携带 grant、客户端生成的 `operation_id` 和请求摘要，原子消费恢复码并创建新凭证。
3. 新设备 token、新恢复码和 Cookie 元数据使用独立 result-envelope key 加密，按 `operation_id` 保存最多 10 分钟。
4. 相同 grant、operation ID 和请求摘要的重试返回同一结果并重新设置同一 Cookie；不同请求摘要返回 idempotency conflict。
5. 客户端确认收妥后调用 `ConfirmSecretReceipt` 删除 envelope；未确认时由 TTL 清理。

首次设备 bootstrap、入驻完成、用户恢复、用户恢复码主动轮换、管理员 TOTP seed/初始化恢复码和管理员辅助恢复 grant 都复用该“一次性结果 envelope”机制。所有产生一次性秘密的方法都要求至少 128 bit 随机 operation ID。数据库只短期保存可逆密文，envelope key 与 PII、TOTP 和长期凭证 key 完全隔离。

首次执行 `CompleteRecovery` 必须在同一事务中：

1. 锁定并消费旧恢复记录。
2. 创建新设备凭证。
3. 生成并保存新的恢复码哈希。
4. 根据用户选择保留或撤销其他设备。
5. 写入安全审计事件。

事务提交后才解密并返回结果 envelope。旧恢复码永远不能用于新 operation；仅允许原 operation 在 TTL 内幂等重放已经提交的相同结果。必须测试“数据库提交成功但 HTTP 响应丢失”。

### 4.4 真实姓名

`UserProfile` 保存：

- `real_name_ciphertext`。
- `real_name_nonce`。
- `real_name_key_version`。
- `real_name_updated_at`、`real_name_updated_by`。

真实姓名使用 AES-256-GCM 加密，associated data 固定包含 `user_id`、字段名和 schema version，防止密文跨用户或跨字段替换。PII 独立 keyring 从只读 secret file 加载，包含 active key version 和历史解密 key；数据库不保存密钥。TOTP、一次性结果 envelope、设备 HMAC、challenge、限流和审计分别使用独立 keyring，禁止仅靠 AAD 共用一个主密钥。

读取、修改和导出真实姓名都必须先完成管理员授权。单条读取和修改在同一事务中写入审计；审计提交失败时不返回明文。导出采用短期 export context 和分页 unary API：每页先在短事务中读取、解密并提交包含 export ID、查询范围、字段集合、页游标和记录数的 checkpoint 审计，提交成功后才返回该页；不使用无法撤回的服务端明文流。导出结束或过期时记录 `completed`、`aborted` 或 `expired`。日志、指标和错误信息只允许使用 `user_id` 和脱敏值。

### 4.5 管理员

本阶段只有一个固定 username 为 `admin` 的 `super_admin`。数据库使用固定 singleton 主键和 CHECK constraint，runtime 数据库角色没有 INSERT 第二个管理员的权限：

- migration 创建 `bootstrap_pending` 管理员记录，不写入默认密码或 TOTP secret。
- 首次启动从只读、一次性挂载的 secret file 读取 bootstrap password，不允许把固定密码写入 migration、镜像或普通环境变量。
- 每个实例执行条件更新 `bootstrap_pending -> setup_required`；只有一个实例可以写入 Argon2id 哈希。未获胜实例必须重新读取并验证同一 bootstrap secret，配置不一致时 readiness 失败。
- 管理员完成初始化后，任何实例仍挂载 bootstrap secret 都必须 readiness 失败，迫使部署移除 secret 并重启。
- `setup_required` 会话只能修改初始密码、绑定 TOTP 和生成管理员恢复码。
- 完成密码修改与 TOTP 验证后，管理员状态变为 `active`。

管理员密码最低 12 个字符，拒绝常见泄漏密码和与 username 相同的密码。密码哈希记录算法和参数，便于未来升级。

TOTP 使用 6 位数字、30 秒周期和前后各一个时间窗口。TOTP seed 使用独立版本化 keyring 加密。验证码成功必须在 PostgreSQL 中执行 `UPDATE ... WHERE last_accepted_step < candidate_step RETURNING`，并在同一事务中消费 challenge、更新 step 和签发会话，保证并发请求只有一个成功。

管理员初始化完成时生成 10 个一次性恢复码，每个独立 Argon2id 哈希，并通过 result envelope 支持响应丢失后的同 operation 重放。使用恢复码时必须在已有密码验证产生的 MFA challenge 内提交；成功后在同一事务中消费恢复码、禁用旧 TOTP、撤销全部管理员会话和旧恢复码，并签发 `recovery_pending` 受限会话。该会话只能修改密码、重绑 TOTP 和生成新恢复码，不能访问任何用户或 PII API。

管理员恢复码只替代第二因素，不绕过密码。管理员同时丢失密码和 TOTP 时，部署人员必须使用离线 `apps/adminctl reset`：该命令要求 migration 级数据库身份和一次性 secret file，条件更新管理员为 `setup_required`、撤销所有会话/TOTP/恢复码并通过受限数据库函数写入审计。不存在 HTTP 密码重置接口。

管理员状态和受限 token 如下：

| 当前状态 | 输入 | 新状态/凭证 | 允许操作 |
| --- | --- | --- | --- |
| `bootstrap_pending` | 一次性 secret file，CAS 成功 | `setup_required` | 无 HTTP 业务权限 |
| `setup_required` | bootstrap password | 10 分钟 `setup_password_pending` | 仅修改初始密码 |
| `setup_required` | 已改密码的 setup token | 10 分钟 `totp_enrollment_pending` | 仅创建/读取当前 enrollment seed |
| `setup_required` | 首个 TOTP code，原子 CAS | `active` + full session | 全部显式 permission |
| `active` | 密码正确 | 5 分钟 `mfa_pending` | 仅 TOTP 或恢复码验证 |
| `active` | TOTP 原子 CAS | full session | 全部显式 permission |
| `active` | MFA challenge + 恢复码 | `recovery_pending` | 仅改密、重绑 TOTP、生成恢复码 |
| `recovery_pending` | 新 TOTP 首码 CAS | `active` + full session | 全部显式 permission |

所有 setup/MFA/recovery token 都保存摘要、purpose、audience、admin version、password version、尝试次数、创建时间、到期时间和 consumed time。状态或密码版本变化后旧 token 全部失效。TOTP enrollment 通过管理员行版本和 active enrollment 唯一约束保证并发页面只能共享同一 pending seed，不能覆盖。

### 4.6 用户状态转换

| 当前状态 | 事件 | 新状态 | 安全副作用 |
| --- | --- | --- | --- |
| 不存在 | bootstrap challenge 成功 | `onboarding` | 创建首个设备和 CSRF，写限流计数 |
| `onboarding` | 合法 username claim | `active` | 创建长期恢复凭证和 result envelope |
| `onboarding` | 24 小时超时 | 删除 | 撤销设备并释放无用户名记录 |
| `active` | 管理员暂停 | `suspended` | 撤销设备/恢复凭证，保留 username claim，写 outbox/audit |
| `suspended` | 管理员解除 | `active` | 不自动恢复旧凭证，用户必须使用管理员辅助恢复 grant |
| `active/suspended` | 管理员删除 | `deleted` | 撤销全部凭证，username claim 保留 90 天，写 outbox/audit |

`suspended` 和 `deleted` 状态不能调用恢复、改名、设备管理或普通身份业务 API。认证拦截器只返回稳定状态码和退出指令。管理员辅助恢复 grant 只能发给 `active` 或刚解除暂停的用户。

## 5. 数据库模型

首批 migration 创建：

- `users`
- `username_claims`
- `device_credentials`
- `user_recovery_credentials`
- `user_recovery_attempts`
- `anonymous_challenges`
- `secret_operation_results`
- `user_profiles`
- `admin_accounts`
- `admin_challenges`
- `admin_totp_enrollments`
- `admin_sessions`
- `admin_recovery_codes`
- `admin_assisted_recovery_grants`
- `profile_export_contexts`
- `audit_chain_head`
- `audit_events`
- `outbox_events`

关键约束：

- `username_claims.username_key` 为主键，统一保存 active 和 reserved claim；用户状态变化不能绕过该主键。
- `users.current_username_key` 外键指向 owner 为自身且状态 active 的 claim；该一致性通过受限数据库函数或 deferred constraint trigger 校验。
- 一个用户同一时刻只能有一个 active 恢复凭证。
- selector、token hash 和恢复 selector 均唯一。
- `secret_operation_results` 以 `(operation_scope, actor_or_challenge_id, operation_id)` 唯一，并保存请求摘要、密文、key version、到期和确认时间；不同身份不能通过预占全局 operation ID 相互干扰。
- 被撤销或过期凭证不能通过查询条件回到 active 集合。
- 所有时间由应用注入的 UTC clock 产生，数据库使用 `timestamptz`。
- migration 必须同时提供向上和向下脚本；破坏性回滚仅允许在无生产数据环境执行，并在文档中标记。

管理员审计量低，使用单行 `audit_chain_head` 加锁串行追加。事务内的 audit repository 先锁定并读取 chain head，使用版本化 canonical Protobuf 编码构造包含 previous hash 的事件，再由应用 Ed25519 私钥签名。数据库通过 `SECURITY DEFINER` 的受限 `append_audit_event(expected_previous_hash, event, signature)` 函数校验 expected head 未变化、原子更新 chain head 并插入事件；并发变化时整个业务事务重试。runtime 角色只能 SELECT chain head、EXECUTE 该函数和 SELECT 脱敏视图，不能直接 INSERT/UPDATE/DELETE 审计表。

签名公钥和历史 key version 可公开验证，私钥从独立 secret file 加载。链头按固定事件数或时间间隔签名并写入 `AuditCheckpointSink`；生产 sink 必须是启用 Object Lock/WORM 的外部对象存储，开发环境可以使用明确标记为非生产的本地 sink。缺少生产 checkpoint sink 时 API readiness 失败，避免把同库链误报为不可抵赖。

需要通知后续房间/实时模块的设备撤销、用户暂停和删除事件与权威状态变更在同一 `UnitOfWork` 中写入 `outbox_events`。本阶段实现 outbox claim/ack 和测试适配，但消费者在后续模块接入；消费方仍需在执行敏感命令时查询权威身份状态，不能只依赖事件缓存。

## 6. Cookie、来源和 CSRF

用户端 Cookie：

- `__Host-gn_device`：`Secure`、`HttpOnly`、`SameSite=Lax`、`Path=/`，不设置 Domain。
- `__Host-gn_csrf`：`Secure`、非 HttpOnly、`SameSite=Lax`、`Path=/`。

管理端 Cookie：

- `__Host-gn_admin`：`Secure`、`HttpOnly`、`SameSite=Strict`、`Path=/`。
- `__Host-gn_admin_csrf`：`Secure`、非 HttpOnly、`SameSite=Strict`、`Path=/`。

所有带认证状态的写请求必须同时满足：

1. `Origin` 属于对应用户端或管理端 allowlist。
2. CSRF Cookie 与 `X-CSRF-Token` 请求头相同。
3. token 摘要与当前服务端会话绑定值相同。

首次身份创建、恢复和管理员登录先创建短期匿名 challenge Cookie，再由完成请求提交 challenge，防止 login CSRF。用户端和管理端使用不同 challenge 名称、签名 key 和来源 allowlist。

匿名 challenge 必须通过对应的 `Begin*` API 创建：

- 用户 challenge Cookie：`__Host-gn_user_challenge`，`Secure`、`HttpOnly`、`SameSite=Lax`、`Path=/`。
- 管理 challenge Cookie：`__Host-gn_admin_challenge`，`Secure`、`HttpOnly`、`SameSite=Strict`、`Path=/`。
- challenge secret 至少 256 bit，数据库只保存摘要；响应 body 另返回同 challenge 绑定的 proof，完成请求必须同时提交 Cookie 和 proof。
- 记录 `purpose`、`audience`、Origin hash、request flow ID、创建时间、5 分钟 TTL、最多尝试次数和 consumed time。
- 首次完成请求以条件更新原子消费 challenge；已消费 challenge 只能在 operation ID、purpose、audience、Origin 和请求摘要全部相同时读取既有 result envelope，不能再次执行状态转换。其他过期、重放或绑定不匹配统一失败。
- 用户身份创建、用户恢复、管理员登录、管理员 setup 和管理员恢复使用不同 purpose；用户端和管理端使用独立版本化 signing key，禁止跨流程接受。

## 7. 管理员会话

active 管理员密码验证成功后只创建 `mfa_pending` challenge，不创建完整会话。TOTP 验证成功后才创建 opaque full session；恢复码验证成功只创建 `recovery_pending` 受限会话：

- 30 分钟无操作过期。
- 12 小时绝对过期。
- active 管理员修改密码、重绑 TOTP、使用恢复码或执行 logout-all 时撤销全部会话；普通 logout 只撤销当前会话。
- 管理员 Cookie 每次权限提升后轮换。
- 数据库只保存 session token 摘要。

管理 API 与用户 API 可以由同一个 `apps/api` 进程承载，但必须使用独立 Connect service、拦截器、Cookie、CSRF、限流配置和允许来源。Nginx 将管理域名只路由到管理服务路径。

`AdminAuthorizer` 只接受 `active` 管理员的 full session，并按 API 所需 permission 判定。setup、MFA 和 recovery pending token 即使携带合法 Cookie，也不能调用 `AdminIdentityService` 或审计查询。

## 8. Redis 限流

Redis 不保存权威身份数据。限流器使用原子 Lua token bucket，并以独立、版本化 HMAC key 表示 IP、设备、恢复 selector 和管理员账号，避免在 Redis key 中泄漏原始值。

必须限流的操作：

- 创建待完善身份：IP。
- 用户名检查与占用：独立消费 IP、device 和 username key 三个桶。
- 身份恢复：独立消费 IP 与 selector 桶；成功定位后再消费 user 桶。
- 管理员密码登录：独立消费 IP 与 admin account 桶。
- TOTP 和管理员恢复码：独立消费 IP、admin account 与固定 flow-purpose 桶；challenge 只用于关联，不创建可通过刷新绕过的容量。
- 真实姓名读取、修改和导出：独立消费 admin session 与 target user 桶。

匿名身份创建、用户名检查/占用、恢复、管理员认证和真实姓名操作在 Redis 不可用时全部 fail closed，返回稳定的暂时不可用错误。进程内还必须限制 Argon2 worker 并发和全局队列长度，避免攻击者通过大量不同 selector、username 或 challenge 耗尽 CPU。普通已认证只读身份查询不依赖 Redis 可用性。

Nginx 必须覆盖而不是追加客户端传入的 `Forwarded`/`X-Forwarded-For`。应用只信任配置中的直接上游代理 CIDR；从 socket peer 开始对代理链由右向左剥离连续可信代理，首个不可信地址才是客户端 IP。直接上游不可信、头格式重复/冲突、同时出现语义不一致的 `Forwarded` 与 XFF、地址数量超限时忽略转发头并记录安全指标。

## 9. API 契约

### 9.1 IdentityService

- `BeginIdentityBootstrap`：校验用户 Origin 并创建短期匿名 identity-bootstrap challenge。
- `BootstrapIdentity`：消费 challenge 和 operation ID；无 Cookie 时创建待完善身份、设备和 result envelope，有有效 Cookie 时返回当前入驻状态和 CSRF token。响应丢失时相同 operation 重新设置同一设备 Cookie。携带无效、撤销或过期 Cookie 时只清除 Cookie并返回恢复/重新开始选择，不静默创建新身份。
- `CompleteOnboarding`：携带 operation ID 设置用户名、激活用户并通过 result envelope 返回只展示一次的恢复码。
- `GetCurrentIdentity`：返回当前用户和设备摘要。
- `ChangeUsername`：执行规范化、冷却期和并发唯一占用。
- `RotateRecoveryCode`：校验当前设备后轮换恢复码。
- `BeginRecoveryChallenge`：校验用户 Origin 并创建短期匿名 recovery challenge。
- `BeginRecovery`：消费 challenge、校验恢复码但不消费，创建一次性 recovery grant。
- `CompleteRecovery`：按 operation ID 原子消费恢复码、签发设备、轮换恢复码并创建可幂等重放的结果 envelope。
- `ConfirmSecretReceipt`：确认一次性结果已保存并删除短期 envelope。
- `ListDevices`：列出 active/revoked 设备摘要，不返回秘密。
- `RevokeDevice`：撤销指定设备；撤销当前设备会立即清除 Cookie。

### 9.2 AdminAuthService

- `GetSetupState`：仅返回是否需要初始设置，不暴露内部凭据状态。
- `BeginAdminLogin`：校验管理 Origin 并创建短期 admin-login challenge。
- `LoginPassword`：消费 login challenge；setup 状态创建受限 setup token，active 状态创建 MFA challenge。
- `VerifyTotp`：完成 MFA 并签发管理员会话。
- `ChangeInitialPassword`：只允许 `setup_required` challenge。
- `BeginTotpEnrollment`：只展示一次 TOTP seed/otpauth URI。
- `CompleteTotpEnrollment`：验证首个 code 并返回一次性管理员恢复码集合。
- `ConfirmAdminSecretReceipt`：确认管理员恢复码集合已保存并删除短期 result envelope。
- `RecoverAdmin`：消费管理员恢复码并进入强制重绑流程。
- `ChangeAdminPassword`：active/recovery pending 管理员改密，并按状态撤销会话。
- `BeginTotpRebind`、`CompleteTotpRebind`：要求近期密码/MFA 或 recovery pending token，替换 TOTP 并撤销旧 seed。
- `RegenerateAdminRecoveryCodes`：要求近期 TOTP，撤销旧集合并通过 result envelope 返回新集合。
- `LogoutAdmin`：只撤销当前会话并清除 Cookie。
- `LogoutAllAdminSessions`：撤销全部管理员会话和 pending challenge。

### 9.3 AdminIdentityService

- `GetUser`：按 `user_id` 或规范 username 查找用户。
- `GetRealName`：解密真实姓名并写入访问审计。
- `UpdateRealName`：加密写入并记录原因与审计。
- `CreateUserProfileExport`：创建短期 export context，记录筛选范围、字段和开始审计。
- `GetUserProfileExportPage`：每页审计提交成功后返回该页授权字段。
- `CompleteUserProfileExport`：关闭 context 并记录完成状态；过期 context 由清理任务记录 expired。
- `CreateAssistedRecoveryGrant`：撤销旧恢复凭证并通过幂等 result envelope 创建 15 分钟有效的管理员交付 grant；管理员不能读取旧恢复码或用户新的长期恢复码。
- `ForceChangeUsername`：强制改名或释放违规名称，必须提交原因并通过统一 claim registry。
- `RevokeUserDevice`：撤销指定设备并写入原因。
- `ListAuditEvents`：分页读取脱敏审计事件，读取行为也写入审计。

所有 Proto 字段使用明确 message，不使用无边界 `Struct` 或 JSON payload。枚举零值必须为 `UNSPECIFIED`，时间使用 Protobuf timestamp，ID 使用 string 传输并在服务端严格校验。

管理员辅助恢复 grant 具有至少 128 bit selector 和 256 bit secret，数据库只保存 Argon2id hash、target user、purpose、15 分钟 TTL、尝试计数、创建管理员和 consumed time。用户通过独立 challenge 兑换 grant 后进入标准 recovery prepare/complete 流程；兑换与 grant 消费原子，最终长期恢复码只返回给用户。管理员重置恢复不会轮换离线设备 secret，只按明确选项撤销设备；仍 active 的设备在下次安全操作时按普通 generation 协议轮换。

## 10. 稳定错误模型

Connect error detail 返回稳定业务 code、可安全展示的 message key、可选 retry time 和 field violation。不得返回 SQL、Redis、加密或哈希内部错误。

主要业务 code：

- `IDENTITY_ONBOARDING_REQUIRED`
- `USERNAME_INVALID`
- `USERNAME_TAKEN`
- `USERNAME_CHANGE_COOLDOWN`
- `DEVICE_CREDENTIAL_INVALID`
- `DEVICE_REVOKED`
- `RECOVERY_INVALID`
- `IDEMPOTENCY_CONFLICT`
- `CSRF_INVALID`
- `ORIGIN_NOT_ALLOWED`
- `RATE_LIMITED`
- `ADMIN_SETUP_REQUIRED`
- `ADMIN_PASSWORD_CHANGE_REQUIRED`
- `MFA_REQUIRED`
- `MFA_INVALID`
- `AUTH_INVALID`
- `PII_KEY_UNAVAILABLE`
- `AUDIT_WRITE_FAILED`
- `SERVICE_TEMPORARILY_UNAVAILABLE`

认证和恢复失败对外统一使用 `AUTH_INVALID` 或 `RECOVERY_INVALID`，不得暴露“不存在、已使用、已过期、被撤销”之间的差异。未知 selector 执行 dummy Argon2id，使状态码、正文和可观察成本保持在测试阈值内。详细原因只进入不含秘密的安全日志和指标。

## 11. 原子性和并发

- 用户名占用以数据库唯一约束为最终裁决，不依赖先查后写。
- username claim、user current claim 和历史保留在同一 transaction runner 中更新。
- 恢复码消费、设备创建、新恢复码写入、结果 envelope、旧设备撤销、审计和 outbox 在同一事务完成。
- 管理员恢复码使用条件更新或 `SELECT ... FOR UPDATE` 消费，禁止并发复用；TOTP step 使用 SQL CAS。
- 真实姓名修改和审计在同一事务提交；读取审计提交失败时不返回明文。
- 设备轮换使用凭证 generation 和 previous-secret 宽限，旧 generation 不能覆盖新 generation。
- 所有写请求携带 request ID；涉及一次性秘密的操作另携带客户端持久化的 operation ID。服务端保存请求摘要和加密结果，提交后响应丢失时只能重放相同结果，不能再次推进状态。
- 审计和 outbox 与权威写入共用 `UnitOfWork`；事务提交后由 outbox dispatcher 以 claim/ack/retry 投递，消费者按 event ID 幂等。

## 12. 配置和密钥

服务启动时必须验证：

- PostgreSQL DSN 和 pool 参数。
- Redis URL、超时和 key prefix。
- 用户端和管理端 Origin allowlist。
- trusted proxy CIDR。
- Cookie secure mode；生产环境禁止关闭 `Secure`。
- 设备 token、限流 key、用户 challenge、管理 challenge 的独立版本化 HMAC/signing keyring。
- PII、TOTP 和一次性结果 envelope 的独立 AES keyring secret file 和 active key version。
- audit Ed25519 signing keyring、历史公钥和生产 checkpoint sink。
- 可选的一次性 bootstrap admin password secret file。

密钥必须至少 256 bit，不能使用默认值。生产模式缺少或重复 key 时服务拒绝启动。测试使用显式测试 key，不允许生产配置自动回退到测试值。

每个 keyring 记录 key version、active/not-before/retire-after 状态和历史验证/解密 key。轮换顺序为先部署新读取 key、再切 active 写入 key、后台重加密或等待 TTL、最后移除旧 key；任何仍被数据引用的 key 不得删除。

`bootstrap admin password` secret file 只在管理员仍为 `bootstrap_pending` 或初始化部署窗口内允许挂载；管理员激活后仍存在时服务 readiness 失败，迫使部署移除 secret。读取后清理进程变量不能替代从容器/宿主配置移除 secret。

## 13. 可观测性与数据最小化

日志允许记录：request ID、actor/target UUID、稳定业务 code、耗时、结果和限流维度类型。

日志禁止记录：

- 设备 token、Cookie 和 CSRF token。
- 用户或管理员恢复码。
- 管理员密码、TOTP seed 和验证码。
- AES key、真实姓名明文和完整导出内容。
- 原始 IP；只允许短期安全日志使用 HMAC 后的 IP 标识。

指标至少包含身份创建、入驻完成、恢复成功/失败、设备撤销、管理员登录/MFA、真实姓名访问、限流和依赖不可用计数。指标 label 禁止使用用户 ID、username 或 IP，避免高基数和隐私泄漏。

## 14. 测试策略

### 14.1 单元测试

- 用户名 NFKC、大小写、字符集、长度、保留名和相似边界。
- Argon2id 参数、恢复 selector、token 解析和常量时间比较。
- 设备滑动/绝对过期、轮换宽限和撤销。
- 管理员状态机、TOTP 时间窗口与重放保护。
- AES-GCM associated data、key rotation 和错误 key。
- Cookie、Origin、CSRF、challenge purpose/audience/TTL/单次消费。
- trusted proxy 链从右向左剥离、重复头、混合 Forwarded/XFF 和伪造左端地址。
- 独立限流桶，刷新 selector/challenge/username 不能重置 IP/account 容量。
- 稳定错误映射和敏感信息日志过滤。

### 14.2 PostgreSQL 集成测试

通过 `GAME_NIGHT_TEST_DATABASE_URL` 连接专用测试服务，每个测试包创建随机 schema：

- migration up/down/up。
- 入驻、改名、暂停/解除、删除和 reservation cleanup 交错时 username claim 始终唯一。
- 恢复码并发消费只有一个成功。
- 恢复事务提交成功但响应丢失后，相同 operation 返回相同 envelope，不同摘要冲突。
- 不同 actor/scope 使用相同 operation ID 互不干扰，不能读取彼此 envelope。
- 设备轮换 generation 防止陈旧写覆盖。
- 真实姓名与审计原子提交。
- 同一 TOTP step 并发验证只有一个成功并只签发一个 session。
- 多实例 bootstrap 只有一个 CAS 胜者，配置不一致实例 readiness 失败。
- 并发 TOTP enrollment 不替换 pending seed，旧 setup challenge 不能重放。
- 审计 canonical 编码、签名、链完整性、checkpoint 和受限数据库函数权限。
- 并发审计 append 的 expected-head 冲突会重试，业务写和审计不会分离提交。
- 权威写入、审计和 outbox 原子性，dispatcher 重试不重复事件。
- sqlc 查询和 transaction adapter 使用真实 PostgreSQL 行为。

### 14.3 Redis 集成测试

通过 `GAME_NIGHT_TEST_REDIS_URL` 连接专用测试服务，每次测试使用随机 key prefix，验证：

- Lua token bucket 原子性。
- IP/device/account 各维独立消费；selector、challenge 和 username churn 不能绕过。
- TTL 和恢复。
- Redis 不可用时匿名创建、用户名占用、恢复、管理认证和 PII 全部 fail closed。

### 14.4 API 集成测试

- 新设备创建、用户名入驻、Cookie/CSRF 属性。
- 首次设备 bootstrap 和入驻在提交后响应丢失时可以重放同一 Cookie/恢复码结果。
- challenge 过期、重放、跨 purpose、跨 audience 和 login CSRF 被拒绝。
- 用户恢复、提交成功响应丢失、恢复码轮换和可选撤销旧设备。
- 管理员多实例 bootstrap、强制改密、并发 TOTP enrollment、登录和受限恢复。
- 离线 `adminctl reset` 撤销旧认证材料并留下可验证审计，不存在 HTTP 密码找回旁路。
- setup/MFA/recovery pending token 不能访问任何管理业务 API。
- 用户 Cookie 不能调用管理 API，管理员 Cookie 不能替代用户身份。
- 真实姓名读写均产生同事务审计；分页导出每页先提交 checkpoint，断线记录 aborted/expired。
- 管理员辅助恢复 grant 的过期、尝试上限、并发消费和重放。
- `onboarding/active/suspended/deleted` 的完整接口权限矩阵。
- 跨 Origin、缺 CSRF、重放 token 和过期会话被拒绝。

本阶段明确以 CI PostgreSQL/Redis service containers 覆盖总体设计中笼统的 Testcontainers 表述，避免依赖本机 Docker。开发机使用现有专用测试服务；没有测试连接时集成测试必须明确标记 `SKIPPED`，不能报告为通过，完整验收必须提供真实服务结果。

## 15. 部署与迁移

1. 部署前运行 `apps/migrate up`。
2. 以拥有 schema 变更、bootstrap seed、受限函数和 runtime grant 管理权限的 migration 数据库角色执行迁移。
3. API 使用权限更小的 runtime 角色，只能执行明确 query、`append_audit_event` 和 outbox 操作。
4. 首次部署挂载一次性 bootstrap password secret file、独立 keyring 和 audit checkpoint sink 配置。
5. 启动 API，确认管理员进入 `setup_required`。
6. 管理员修改密码、绑定 TOTP 并保存恢复码。
7. 从部署配置移除 bootstrap secret file 并重启验证 readiness。

迁移、API 和 key rotation 都必须支持先部署兼容 schema、再切换代码、最后清理旧字段的 expand/contract 流程。禁止在同一发布中直接删除仍被上一版本读取的列。

## 16. 验收标准

- 空数据库可以完成 migration，并创建禁用的 `super_admin`，不存在固定默认密码。
- 新浏览器可获得设备凭证，设置唯一用户名并仅一次看到用户恢复码。
- 相同用户名的并发请求只有一个成功，错误稳定且不泄漏内部信息。
- 用户可用恢复码在新设备恢复，恢复码原子轮换，并按选择保留或撤销旧设备；提交后响应丢失可以按 operation ID 取回同一结果。
- 过期、撤销和旧 generation 的设备 token 均不能访问身份 API。
- 管理员必须改初始密码并绑定 TOTP 后才能使用管理 API。
- TOTP 并发重放、管理员恢复码重用、pending token 越权和跨认证域 Cookie 均被拒绝。
- 真实姓名在数据库中不可读，读取、修改和分页导出均在返回明文前提交对应审计。
- username claim、身份状态、审计和 outbox 在所有交错事务中保持一致。
- PostgreSQL/Redis 真实集成测试、API 测试、race、vet、Buf 生成和仓库边界检查全部通过。
- 服务和测试日志中不出现任何 token、恢复码、密码、TOTP seed、验证码或真实姓名明文。

## 17. 后续衔接

房间与大厅阶段只依赖本阶段提供的 `AuthenticatedUser`、设备撤销事件、封禁状态查询和稳定用户 ID，不读取设备秘密、管理员会话或真实姓名。实时网关后续复用身份验证端口，但拥有独立的连接票据和会话授权，不直接接受长期设备 Cookie 作为 WebSocket 内部凭证。
