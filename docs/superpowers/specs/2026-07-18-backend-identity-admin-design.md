# 后端身份与管理基础设计

> 日期：2026-07-18
>
> 状态：已获方向批准，待文档审查
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
└─ migrate/                     # goose migration CLI
platform/
├─ identity/                    # 用户、用户名、设备凭证、恢复流程
├─ profile/                     # 真实姓名加密和资料访问边界
├─ admin/                       # super_admin 密码、TOTP、会话和引导流程
├─ audit/                       # 仅追加审计事件和完整性链
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
- 用户认证和管理员认证不共享 Cookie、会话表、CSRF token 或限流 key。
- `super_admin` 授权通过统一 `AdminAuthorizer` 完成，业务代码不散落布尔型 `isAdmin` 判断。

## 4. 领域模型

### 4.1 用户

`User` 表示平台身份，主要字段为：

- `user_id`：UUIDv7，服务端生成。
- `status`：`onboarding`、`active`、`suspended`、`deleted`。
- `username`：用户公开看到的规范展示值，入驻前为空。
- `username_key`：用于唯一索引的规范化值。
- `username_changed_at`：控制改名冷却期。
- `created_at`、`updated_at`。

用户名规则：

- 支持汉字、拉丁字母、十进制数字和下划线。
- Unicode NFKC 规范化并去除首尾空白。
- 拉丁字母大小写不敏感。
- 规范化后长度为 2-20 个 Unicode code point。
- 禁止控制字符、不可见格式字符、emoji、路径符号和连续下划线。
- 命中系统保留名或敏感词时拒绝。
- `username_key` 由数据库唯一约束裁决，并发占用只能有一个请求成功。

用户主动改名最多每 30 天一次，旧 `username_key` 保留 90 天，避免立即冒名。管理员强制改名必须提供原因并写入审计。

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

凭证每 30 天轮换，身份恢复、管理员恢复重置和敏感安全事件也会触发轮换。旧 secret 只保留两分钟宽限，以允许同一页面的并发请求完成；任何成功轮换都重新签发 Cookie。

### 4.3 用户恢复凭证

用户完成入驻时生成一个恢复码，只在响应中展示一次。恢复码格式包含公开 selector 和高熵 secret，服务端按 selector 定位记录，再使用 Argon2id 校验 secret，禁止扫描所有用户哈希。

恢复成功必须在同一事务中：

1. 锁定并消费旧恢复记录。
2. 创建新设备凭证。
3. 生成并保存新的恢复码哈希。
4. 根据用户选择保留或撤销其他设备。
5. 写入安全审计事件。

事务提交后才返回新设备 Cookie 和新恢复码。旧恢复码永远不能再次使用。

### 4.4 真实姓名

`UserProfile` 保存：

- `real_name_ciphertext`。
- `real_name_nonce`。
- `real_name_key_version`。
- `real_name_updated_at`、`real_name_updated_by`。

真实姓名使用 AES-256-GCM 加密，associated data 固定包含 `user_id`、字段名和 schema version，防止密文跨用户或跨字段替换。密钥从只读 secret file 加载，包含 active key version 和历史解密 key；数据库不保存密钥。

读取、修改和导出真实姓名都必须先完成管理员授权，并在同一事务中写入审计。审计写入失败时不返回明文。日志、指标和错误信息只允许使用 `user_id` 和脱敏值。

### 4.5 管理员

本阶段只有一个固定 username 为 `admin` 的 `super_admin`：

- migration 创建 `bootstrap_pending` 管理员记录，不写入默认密码或 TOTP secret。
- API 首次启动检测部署环境提供的一次性 bootstrap password，使用 Argon2id 写入哈希并切换为 `setup_required`。
- bootstrap password 不通过 HTTP 传输；完成写入后进程清除自身环境变量。部署必须在首次激活后移除 secret 并重启。
- `setup_required` 会话只能修改初始密码、绑定 TOTP 和生成管理员恢复码。
- 完成密码修改与 TOTP 验证后，管理员状态变为 `active`。

管理员密码最低 12 个字符，拒绝常见泄漏密码和与 username 相同的密码。密码哈希记录算法和参数，便于未来升级。

TOTP 使用 6 位数字、30 秒周期和前后各一个时间窗口。服务端记录最后接受的 time step，拒绝同一验证码重放。TOTP seed 使用与真实姓名相同的版本化 keyring、不同的 associated data 加密。

管理员初始化完成时生成 10 个一次性恢复码，每个独立 Argon2id 哈希。使用恢复码后必须重新绑定 TOTP，并生成全新恢复码集合。

## 5. 数据库模型

首批 migration 创建：

- `users`
- `username_reservations`
- `device_credentials`
- `user_recovery_credentials`
- `user_profiles`
- `admin_accounts`
- `admin_sessions`
- `admin_recovery_codes`
- `audit_chain_head`
- `audit_events`

关键约束：

- `users.username_key` 为部分唯一索引，仅 active/onboarding 用户参与。
- `username_reservations.username_key` 唯一，并记录释放时间和原因。
- 一个用户同一时刻只能有一个 active 恢复凭证。
- selector、token hash 和恢复 selector 均唯一。
- 被撤销或过期凭证不能通过查询条件回到 active 集合。
- 所有时间由应用注入的 UTC clock 产生，数据库使用 `timestamptz`。
- migration 必须同时提供向上和向下脚本；破坏性回滚仅允许在无生产数据环境执行，并在文档中标记。

管理员审计量低，使用单行 `audit_chain_head` 加锁串行追加。每个事件保存前一事件哈希和当前 HMAC，形成可验证链。应用数据库角色只拥有审计表 INSERT/SELECT 权限，不拥有 UPDATE/DELETE 权限。

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

## 7. 管理员会话

管理员密码验证成功后只创建 `mfa_pending` challenge，不创建完整会话。TOTP 或一次性恢复码验证成功后创建 opaque admin session token：

- 30 分钟无操作过期。
- 12 小时绝对过期。
- 修改密码、重绑 TOTP、使用恢复码或退出时撤销全部相关会话。
- 管理员 Cookie 每次权限提升后轮换。
- 数据库只保存 session token 摘要。

管理 API 与用户 API 可以由同一个 `apps/api` 进程承载，但必须使用独立 Connect service、拦截器、Cookie、CSRF、限流配置和允许来源。Nginx 将管理域名只路由到管理服务路径。

## 8. Redis 限流

Redis 不保存权威身份数据。限流器使用原子 Lua token bucket，并以版本化 HMAC key 表示 IP、设备、恢复 selector 和管理员账号，避免在 Redis key 中泄漏原始值。

必须限流的操作：

- 创建待完善身份：IP。
- 用户名检查与占用：IP + device + username key。
- 身份恢复：IP + recovery selector；成功定位后增加 user 维度。
- 管理员密码登录：IP + admin account。
- TOTP 和管理员恢复码：IP + challenge + admin account。
- 真实姓名读取、修改和导出：admin session + target user。

恢复、管理员认证和真实姓名操作在 Redis 不可用时 fail closed，返回稳定的暂时不可用错误。普通已认证只读身份查询不依赖 Redis 可用性。

只信任配置中的反向代理 CIDR。来自其他来源的 `X-Forwarded-For`、`Forwarded` 和类似头全部忽略。

## 9. API 契约

### 9.1 IdentityService

- `BootstrapIdentity`：创建或恢复当前设备会话，返回入驻状态和 CSRF token。
- `CompleteOnboarding`：设置用户名，激活用户，返回只展示一次的恢复码。
- `GetCurrentIdentity`：返回当前用户和设备摘要。
- `ChangeUsername`：执行规范化、冷却期和并发唯一占用。
- `RotateRecoveryCode`：校验当前设备后轮换恢复码。
- `BeginRecovery`：创建匿名恢复 challenge。
- `CompleteRecovery`：消费恢复码、签发设备并轮换恢复码。
- `ListDevices`：列出 active/revoked 设备摘要，不返回秘密。
- `RevokeDevice`：撤销指定设备；撤销当前设备会立即清除 Cookie。

### 9.2 AdminAuthService

- `GetSetupState`：仅返回是否需要初始设置，不暴露内部凭据状态。
- `LoginPassword`：密码校验后创建 MFA challenge。
- `VerifyTotp`：完成 MFA 并签发管理员会话。
- `ChangeInitialPassword`：只允许 `setup_required` challenge。
- `BeginTotpEnrollment`：只展示一次 TOTP seed/otpauth URI。
- `CompleteTotpEnrollment`：验证首个 code 并返回一次性管理员恢复码集合。
- `RecoverAdmin`：消费管理员恢复码并进入强制重绑流程。
- `LogoutAdmin`：撤销当前会话并清除 Cookie。

### 9.3 AdminIdentityService

- `GetUser`：按 `user_id` 或规范 username 查找用户。
- `GetRealName`：解密真实姓名并写入访问审计。
- `UpdateRealName`：加密写入并记录原因与审计。
- `ExportUserProfiles`：服务端流式返回授权字段，每批访问进入同一导出审计上下文。
- `ResetUserRecovery`：撤销旧恢复凭证，生成一次性管理员交付码；不允许管理员读取旧恢复码。
- `RevokeUserDevice`：撤销指定设备并写入原因。
- `ListAuditEvents`：分页读取脱敏审计事件，读取行为也写入审计。

所有 Proto 字段使用明确 message，不使用无边界 `Struct` 或 JSON payload。枚举零值必须为 `UNSPECIFIED`，时间使用 Protobuf timestamp，ID 使用 string 传输并在服务端严格校验。

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
- `RECOVERY_ALREADY_USED`
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

认证和恢复失败对外使用统一信息，避免暴露用户名、恢复 selector 或管理员状态是否存在。详细原因只进入不含秘密的安全日志和指标。

## 11. 原子性和并发

- 用户名占用以数据库唯一约束为最终裁决，不依赖先查后写。
- 恢复码消费、设备创建、新恢复码写入和旧设备撤销在同一事务完成。
- 管理员恢复码使用 `SELECT ... FOR UPDATE` 消费，禁止并发复用。
- 真实姓名修改和审计在同一事务提交；读取审计提交失败时不返回明文。
- 设备轮换使用凭证 generation 和 previous-secret 宽限，旧 generation 不能覆盖新 generation。
- 所有写请求携带 request ID；审计表对需要幂等的管理员动作保存 request ID 唯一键。

## 12. 配置和密钥

服务启动时必须验证：

- PostgreSQL DSN 和 pool 参数。
- Redis URL、超时和 key prefix。
- 用户端和管理端 Origin allowlist。
- trusted proxy CIDR。
- Cookie secure mode；生产环境禁止关闭 `Secure`。
- 设备 token HMAC key、限流 key HMAC key和匿名 challenge signing key。
- PII/TOTP AES keyring secret file 和 active key version。
- audit HMAC key。
- 可选的一次性 bootstrap admin password。

密钥必须至少 256 bit，不能使用默认值。生产模式缺少或重复 key 时服务拒绝启动。测试使用显式测试 key，不允许生产配置自动回退到测试值。

`bootstrap admin password` 只在管理员仍为 `bootstrap_pending` 时允许出现；管理员激活后配置仍包含该值时服务拒绝启动，迫使部署移除 secret。

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
- Cookie、Origin、CSRF 和 trusted proxy 处理。
- 稳定错误映射和敏感信息日志过滤。

### 14.2 PostgreSQL 集成测试

通过 `GAME_NIGHT_TEST_DATABASE_URL` 连接专用测试服务，每个测试包创建随机 schema：

- migration up/down/up。
- 并发用户名占用只有一个成功。
- 恢复码并发消费只有一个成功。
- 设备轮换 generation 防止陈旧写覆盖。
- 真实姓名与审计原子提交。
- 审计链完整性和禁止 update/delete 权限。
- sqlc 查询和 transaction adapter 使用真实 PostgreSQL 行为。

### 14.3 Redis 集成测试

通过 `GAME_NIGHT_TEST_REDIS_URL` 连接专用测试服务，每次测试使用随机 key prefix，验证：

- Lua token bucket 原子性。
- IP/device/account 多维组合。
- TTL 和恢复。
- Redis 不可用时敏感操作 fail closed。

### 14.4 API 集成测试

- 新设备创建、用户名入驻、Cookie/CSRF 属性。
- 用户恢复、恢复码轮换和可选撤销旧设备。
- 管理员 bootstrap、强制改密、TOTP enrollment、登录和恢复。
- 用户 Cookie 不能调用管理 API，管理员 Cookie 不能替代用户身份。
- 真实姓名读写和导出均产生审计。
- 跨 Origin、缺 CSRF、重放 token 和过期会话被拒绝。

CI 使用 PostgreSQL 和 Redis service containers。开发机使用现有专用测试服务；没有测试连接时集成测试必须明确标记 `SKIPPED`，不能报告为通过，完整验收必须提供真实服务结果。

## 15. 部署与迁移

1. 部署前运行 `apps/migrate up`。
2. 以只含 schema 权限的 migration 数据库角色执行迁移。
3. API 使用权限更小的 runtime 角色。
4. 首次部署注入一次性 bootstrap password 和全部 key。
5. 启动 API，确认管理员进入 `setup_required`。
6. 管理员修改密码、绑定 TOTP 并保存恢复码。
7. 从部署配置移除 bootstrap password 并重启验证。

迁移、API 和 key rotation 都必须支持先部署兼容 schema、再切换代码、最后清理旧字段的 expand/contract 流程。禁止在同一发布中直接删除仍被上一版本读取的列。

## 16. 验收标准

- 空数据库可以完成 migration，并创建禁用的 `super_admin`，不存在固定默认密码。
- 新浏览器可获得设备凭证，设置唯一用户名并仅一次看到用户恢复码。
- 相同用户名的并发请求只有一个成功，错误稳定且不泄漏内部信息。
- 用户可用恢复码在新设备恢复，恢复码原子轮换，并按选择保留或撤销旧设备。
- 过期、撤销和旧 generation 的设备 token 均不能访问身份 API。
- 管理员必须改初始密码并绑定 TOTP 后才能使用管理 API。
- TOTP 重放、管理员恢复码重用和跨认证域 Cookie 均被拒绝。
- 真实姓名在数据库中不可读，读取、修改和导出均有完整审计。
- PostgreSQL/Redis 真实集成测试、API 测试、race、vet、Buf 生成和仓库边界检查全部通过。
- 服务和测试日志中不出现任何 token、恢复码、密码、TOTP seed、验证码或真实姓名明文。

## 17. 后续衔接

房间与大厅阶段只依赖本阶段提供的 `AuthenticatedUser`、设备撤销事件、封禁状态查询和稳定用户 ID，不读取设备秘密、管理员会话或真实姓名。实时网关后续复用身份验证端口，但拥有独立的连接票据和会话授权，不直接接受长期设备 Cookie 作为 WebSocket 内部凭证。
