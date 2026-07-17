# 后端身份与管理基础实施计划

> **执行要求：** 按任务顺序测试先行实施；每个任务完成验证后形成独立提交。除计划明确列出的生成命令外，不手工编辑生成文件。
>
> **状态：** 已审查，待执行

**目标：** 交付第一个可运行的正式业务后端，完整覆盖设备身份、用户入驻与恢复、管理员密码 + TOTP、真实姓名加密、审计链、WORM checkpoint、Redis 限流以及三个 ConnectRPC 服务。

**架构：** `apps/*` 只负责进程、传输和依赖装配；`platform/identity`、`platform/admin`、`platform/profile`、`platform/audit`、`platform/outbox` 与共享安全原语保存业务规则和端口；`platform/persistence/*` 实现 PostgreSQL、Redis 与 S3 兼容适配器。所有跨模块权威写入通过同一 PostgreSQL `UnitOfWork` 提交，用户认证与管理员认证保持完全独立。

**技术栈：** Go 1.26.4、Connect-Go 1.20.0、Protocol Buffers + Buf、pgx 5.10.0、sqlc 1.31.1、goose 3.27.2、go-redis 9.21.0、PostgreSQL、Redis、AWS SDK for Go v2、Argon2id、AES-256-GCM、Ed25519、Prometheus。

---

## 1. 范围与规范追踪

本计划只实现已确认的[后端身份与管理基础设计](../specs/2026-07-18-backend-identity-admin-design.md)，不实现用户端或管理端页面、房间、实时网关和任何游戏规则。阶段边界来自设计第 7-23 行；后续房间模块只依赖本阶段产出的稳定用户 ID、认证主体、状态查询和撤销事件（设计第 548-550 行）。

| 实施约束 | 规范证据 |
| --- | --- |
| 应用入口不承载业务规则，领域不直接依赖 pgx、Redis 或 HTTP | [设计第 38-69 行](../specs/2026-07-18-backend-identity-admin-design.md#3-模块和目录) |
| 用户名必须通过统一 claim 注册表和事务裁决 | [设计第 86-98 行](../specs/2026-07-18-backend-identity-admin-design.md#41-用户) |
| 设备凭证支持多设备、双期限、轮换 generation 和 previous-secret 限制 | [设计第 100-120 行](../specs/2026-07-18-backend-identity-admin-design.md#42-设备凭证) |
| 所有一次性秘密使用可重放 result envelope，擦除后保留 tombstone | [设计第 122-150 行](../specs/2026-07-18-backend-identity-admin-design.md#43-用户恢复凭证) |
| 管理员是单账号、密码 + TOTP、强制初始化且不存在 HTTP 全丢恢复 | [设计第 165-198 行](../specs/2026-07-18-backend-identity-admin-design.md#45-管理员) |
| 审计链使用受限数据库函数、Ed25519 签名和 durable checkpoint outbox | [设计第 250-256 行](../specs/2026-07-18-backend-identity-admin-design.md#5-数据库模型) |
| 用户端与管理端 Cookie、Origin、CSRF、challenge 和限流完全隔离 | [设计第 258-318 行](../specs/2026-07-18-backend-identity-admin-design.md#6-cookie来源和-csrf) |
| API 由三个独立 Connect service 和稳定业务错误构成 | [设计第 320-402 行](../specs/2026-07-18-backend-identity-admin-design.md#9-api-契约) |
| 配置不得带默认密钥，生产缺少独立 keyring 或 WORM sink 时拒绝就绪 | [设计第 416-435 行](../specs/2026-07-18-backend-identity-admin-design.md#12-配置和密钥) |
| 完整验收必须运行真实 PostgreSQL、Redis、API、race、vet、Buf 和边界检查 | [设计第 451-545 行](../specs/2026-07-18-backend-identity-admin-design.md#14-测试策略) |

## 2. 实施原则

1. **先约束再实现：** 先扩充依赖边界与协议/迁移生成门禁，后续包一旦越界立即在 CI 失败。
2. **数据库是最终裁决者：** 唯一性、CAS、凭证消费、审计 head 和 outbox 原子性不依赖进程锁或 Redis。
3. **一次性秘密可靠交付：** 提交成功与 HTTP 返回分离；重试读取原结果，确认或 TTL 只擦除密文，不重复业务动作。
4. **认证域隔离：** 用户、管理员、匿名 challenge、CSRF、HMAC keyring、Cookie 和 Redis key 都分别实现和测试。
5. **默认拒绝与最小披露：** 权限、错误、日志、指标和管理员状态机均从拒绝开始，只开放明确路径。

## 3. 依赖和生成基线

直接依赖固定为以下稳定版本；实施时由 `go get <module>@<version>` 和 `go mod tidy` 生成 `go.sum`，不得手写依赖元数据：

| 用途 | 模块 | 版本 |
| --- | --- | --- |
| Connect runtime | `connectrpc.com/connect` | `v1.20.0` |
| PostgreSQL | `github.com/jackc/pgx/v5` | `v5.10.0` |
| migration | `github.com/pressly/goose/v3` | `v3.27.2` |
| Redis | `github.com/redis/go-redis/v9` | `v9.21.0` |
| UUIDv7 | `github.com/google/uuid` | `v1.6.0` |
| TOTP | `github.com/pquerna/otp` | `v1.5.0` |
| Argon2id | `golang.org/x/crypto` | `v0.54.0` |
| Unicode NFKC | `golang.org/x/text` | `v0.40.0` |
| Protobuf runtime | `google.golang.org/protobuf` | `v1.36.11` |
| S3 config/client | `github.com/aws/aws-sdk-go-v2/config`、`github.com/aws/aws-sdk-go-v2/service/s3` | `v1.32.30`、`v1.105.2` |
| Metrics | `github.com/prometheus/client_golang` | `v1.23.2` |
| sqlc tool | `github.com/sqlc-dev/sqlc/cmd/sqlc` | `v1.31.1` |

Buf 的 Go Protobuf 和 Connect 插件同步到 `v1.36.11`、`v1.20.0`；TypeScript 插件保持仓库已验证的 `v2.6.2`。`sqlc` 使用 Go `tool` directive 固定，不要求开发机全局安装。

## 4. 目标文件责任图

| 路径 | 责任 |
| --- | --- |
| `contracts/platform/common/v1/*` | 稳定错误、分页和共享值对象 |
| `contracts/platform/identity/v1/*` | 用户 bootstrap、入驻、恢复、设备 API |
| `contracts/platform/admin/v1/*` | 管理员认证与用户管理 API |
| `contracts/platform/audit/v1/*` | 审计 canonical event 和 checkpoint 编码 |
| `contracts/gen/{go,ts}/**` | Buf 生成物，只由 `buf generate` 更新 |
| `infra/migrations/*.sql` | goose up/down migration、约束、函数与权限 |
| `infra/postgres/roles.sql` | 部署人员显式创建 migration/runtime 角色的幂等脚本 |
| `infra/nginx/**` | 用户域/管理域路由、代理头覆盖和敏感响应禁缓存配置 |
| `tooling/sqlc/sqlc.yaml`、`tooling/sqlc/queries/*.sql` | sqlc 配置和按领域分组的查询输入 |
| `platform/security` | 随机秘密、版本化 keyring、Argon2、AES-GCM、HMAC 和敏感值类型 |
| `platform/ratelimit` | 领域可见的多桶限流端口、调用顺序策略和测试 fake |
| `platform/secretresult` | 一次性结果 envelope、授权重放、确认和 tombstone 规则 |
| `platform/identity` | 用户、username claim、设备、challenge、入驻和恢复 |
| `platform/admin` | 管理员 bootstrap、密码、TOTP、会话、恢复与权限 |
| `platform/profile` | 真实姓名加密、授权访问和物化导出 |
| `platform/audit` | canonical 编码、签名、链追加与验证 |
| `platform/outbox` | durable event、claim/ack/retry 和 consumer offset |
| `platform/persistence/postgres` | pgxpool、sqlc、transaction-bound repositories |
| `platform/persistence/redis` | Lua token bucket 和版本化 HMAC key |
| `platform/persistence/objectstorage` | 本地开发 sink 与 S3 create-if-absent WORM sink |
| `apps/internal/config` | API、worker、migrate、adminctl 可共享的安全配置和 secret-file 加载器 |
| `apps/api` | Connect handler、认证拦截器、Cookie、CSRF、Origin、配置和进程生命周期 |
| `apps/migrate` | 独立 goose CLI |
| `apps/adminctl` | 无 HTTP 的管理员灾难恢复命令 |
| `apps/worker` | checkpoint dispatcher、secret/onboarding/export/claim 清理任务 |
| `internal/integrationtest` | 环境门禁、随机 schema/key prefix 和测试依赖装配 |

## 5. 可测试验收标准

- 空数据库执行 `up -> down -> up` 后创建全部表、约束、受限函数和 `bootstrap_pending` 管理员，schema 中不存在默认密码、TOTP seed 或测试密钥。
- 运行时数据库角色不能直接写审计表、锁 audit head、创建第二个管理员或执行 schema DDL，只能调用允许的函数和业务查询。
- 并发 username claim、恢复码消费、TOTP step、管理员 bootstrap 和审计 append 都只有一个有效胜者。
- bootstrap、入驻、恢复、管理员初始化/恢复在“事务提交后响应丢失”时，相同 actor + scope + operation + digest 返回同一结果；不同 digest 返回 `IDEMPOTENCY_CONFLICT`。
- secret receipt 确认、TTL 清理和延迟 retry 并发后，密文为空且 tombstone 仍阻止二次执行至少 30 天。
- 用户和管理员 Cookie、CSRF、Origin、challenge、session 和限流 key 不能跨域替代；pending token 不能访问正式管理 API。
- 设备 idle/absolute expiry、30 天轮换、两分钟 previous-secret 读取宽限和安全操作禁用均有时钟可控测试。
- 真实姓名密文不能跨用户替换；读、写、导出每页在返回明文前完成审计，审计失败时不返回数据。
- checkpoint outbox 在崩溃后可重试，S3 对象只能 create-if-absent，重复 key 内容不一致时报错且不能 ack。
- outbox 以 consumer offset/lease 独立追踪 `audit.checkpoint` 和后续消费者；API runtime 只能同事务插入事件，不能 claim、ack 或清理。
- PII/TOTP key 轮换支持可恢复批处理、并发更新保护和引用检查；仍有密文引用时移除旧 key 会使进程拒绝就绪。
- Redis 不可用时匿名创建、用户名占用、恢复、管理员认证和 PII 操作稳定 fail closed；已认证普通只读身份查询不依赖 Redis。
- 所有敏感响应设置 `no-store`；日志扫描不出现 token、Cookie、CSRF、密码、恢复码、TOTP、key 或真实姓名。
- CI 强制执行生成漂移、migration、PostgreSQL、Redis、S3 适配器、API 集成、`go test -race`、`go vet`、Buf 和依赖边界。

## 6. 逐步实施

### Task 0：锁定执行基线

**Files:** No changes expected.

- [ ] 确认本计划和设计文档已分别提交，`git status --porcelain` 为空。
- [ ] 验证 `go version`、`buf --version`、`node --version`、`pnpm --version` 与仓库基线一致。
- [ ] 记录本地 `GAME_NIGHT_TEST_DATABASE_URL`、`GAME_NIGHT_TEST_ADMIN_DATABASE_URL`、`GAME_NIGHT_TEST_REDIS_URL` 和 `GAME_NIGHT_TEST_S3_*` 是否存在；测试夹具统一解析 `GAME_NIGHT_REQUIRE_INTEGRATION=postgres,postgres-privileges,redis,object-storage,nginx`。缺少未要求的依赖时明确 `SKIPPED`，缺少已要求的 URL、权限或运行时则立即失败。
- [ ] 确认实现分支持续保留用户在主工作树中的 `.gitignore` 改动，不执行 reset/checkout 覆盖。

**Verify:** `git status --short --branch`、`go test ./...`、`go vet ./...`、`pnpm run check`。

### Task 1：先扩充后端依赖边界

**Files:** Modify `tooling/boundarycheck/policy.go`, `tooling/boundarycheck/policy_test.go`, `platform/README.md`, `apps/README.md`.

- [ ] 先写失败测试：`platform/identity|admin|profile|audit|outbox` 导入 pgx、go-redis、AWS SDK 或 `net/http` 必须违规。
- [ ] 写允许测试：相应外部依赖只允许位于 `platform/persistence/{postgres,redis,objectstorage}`；`apps/*` 只导入平台公开装配入口，不承载领域规则。
- [ ] 更新边界策略和责任文档；诊断必须显示 from、to 和具体原因。
- [ ] 回归现有 17 条禁止边和 5 条允许边，不降低游戏引擎边界。

**Verify:** `go test ./tooling/boundarycheck ./tooling/cmd/boundarycheck`、`go run ./tooling/cmd/boundarycheck`。

**Commit:** `feat(boundaries): 约束后端领域与基础设施依赖`

### Task 2：固定后端依赖与代码生成工具

**Files:** Modify `go.mod`, create `go.sum`, modify `package.json`, `buf.gen.yaml`.

- [ ] 使用第 3 节的精确版本添加运行时依赖和 `sqlc` tool directive；不引入 ORM、JWT 或第二套迁移工具。
- [ ] 添加根命令 `generate:contracts`；生产 migration 和 query 尚未创建，本任务只验证 `sqlc` tool version，不提前建立无法运行的生产 sqlc 配置。
- [ ] 同步 Buf Go 插件版本，重新生成 smoke fixture 并证明工作树稳定。

**Verify:** `go mod verify`、`go tool sqlc version`、`pnpm run generate:contracts`、`git diff --check`。

**Commit:** `build(backend): 固定服务端依赖与生成工具`

### Task 3：定义正式 Proto 契约和稳定错误

**Files:** Create `contracts/platform/common/v1/common.proto`, `error.proto`; `contracts/platform/identity/v1/identity.proto`; `contracts/platform/admin/v1/admin_auth.proto`, `admin_identity.proto`; `contracts/platform/audit/v1/audit.proto`; generate `contracts/gen/{go,ts}/platform/**`.

- [ ] 先写契约检查脚本/测试，断言三个 service 的完整 RPC 集合、枚举零值 `UNSPECIFIED`、禁用 `Struct`/JSON payload、ID/string 和 timestamp 约束。
- [ ] 为设计第 324-367 行的每个 RPC 建立明确 request/response；一次性秘密响应携带 result metadata，但不把 Cookie token 作为普通可缓存字段。
- [ ] 定义 `BusinessErrorDetail` 和设计第 377-400 行全部稳定 code；认证/恢复内部原因不进入公开枚举差异。
- [ ] 审计 canonical message 固定 version、sequence、previous hash、actor、target、action、reason、request ID、timestamp 和 key version；禁止 map 和浮点字段以保持确定性编码。
- [ ] 首次生成后暂存 `contracts/gen`，再次生成并检查该目录没有 unstaged 或 untracked 漂移；不得用会被本任务预期 diff 触发的全局 `git diff --exit-code` 伪装幂等验证。
- [ ] 精确执行 production breaking 检查：先验证 `origin/master` 存在；若 base tree 已有 production Proto，运行 `buf breaking --against ".git#branch=origin/master"`；若确实没有，输出 `SKIPPED: no production Proto baseline` 并由首次协议提交建立后续基线。

**Verify:** `buf format --diff --exit-code`、`buf lint`、上述 breaking 命令、第二次 `buf generate` 后 `git diff --exit-code -- contracts/gen` 且 `git ls-files --others --exclude-standard -- contracts/gen` 为空、`go test ./contracts/gen/go/...`。

**Commit:** `feat(contracts): 定义身份与管理服务协议`

### Task 4：建立 migration、数据库权限和真实集成测试夹具

**Files:** Create `infra/migrations/00001_identity.sql`, `00002_admin.sql`, `00003_audit_outbox.sql`, `00004_profile_export.sql`, `00005_operations.sql`, `infra/postgres/roles.sql`, `apps/migrate/main.go`, `internal/integrationtest/{requirements,postgres}.go`, migration tests.

- [ ] 先写真实 PostgreSQL 测试：普通 DSN 在随机 schema 执行 `up/down/up`；管理员 DSN 每次运行创建随机数据库以及带随机后缀的 owner/migration/runtime/worker login role，验证设计的 19 张领域表及内部 `outbox_consumers`、`key_rotation_jobs`，以及索引、FK、partial unique、deferred trigger、timestamptz 和 singleton constraint。
- [ ] migration 使用 Goose annotations；创建安全函数时把经过标识符引用的当前 trusted schema 写入 DDL，函数 `search_path` 显式固定为 `pg_catalog, <trusted_schema>, pg_temp`，函数体内业务对象全部 schema-qualified，不捕获 `$user`/`public`。
- [ ] `roles.sql` 显式创建无登录权限 owner group 并最小化 grant；`SECURITY DEFINER` 由 non-login owner 持有，每个函数先撤销 `PUBLIC EXECUTE` 再按用途单独授权；migration/runtime/worker 登录账号由部署人员创建，不在应用启动时提权。
- [ ] `read_audit_head`、`append_audit_event` 只授权无登录 `audit_writer` 组，API runtime 与 worker 角色按需加入该组；离线 admin reset 函数只授权 migration/adminctl 角色，runtime/worker 显式无 `EXECUTE`。同时撤销二者对审计底表和 public schema 的默认权限。
- [ ] API runtime 只能执行业务 query 和同事务插入 outbox；worker 角色只能 claim/ack outbox、运行限定清理/重加密 query 和读取 checkpoint health，不能调用业务管理 query、重置管理员或执行 DDL。
- [ ] `apps/migrate` 只接受显式 DSN 和 `up|down|status`，生产 down 需要额外 destructive flag；API 启动绝不自动迁移。
- [ ] privilege harness 使用唯一数据库/角色名支持并行包，测试结束强制断开并删除临时数据库和角色；用独立 runtime/worker DSN 而非只用 `SET ROLE` 验证允许操作和禁止 admin reset/DDL/底表写入/outbox 越权，并增加 public/temp shadow 对象攻击测试。
- [ ] `requirements.go` 解析逗号分隔 require 集合；要求 `postgres-privileges` 时缺少管理员 DSN、`CREATEROLE`/`CREATEDB` 权限或发生清理失败都必须失败，不能 skip。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres,postgres-privileges'; go test ./apps/migrate ./internal/integrationtest/...`。

**Commit:** `feat(database): 建立身份管理数据模型与权限`

### Task 5：生成 sqlc 查询并建立 PostgreSQL 事务底座

**Files:** Create `tooling/sqlc/sqlc.yaml`, `tooling/sqlc/queries/{identity,secret_result,admin,profile,audit,outbox,key_rotation}.sql`; modify `package.json`; generate `platform/persistence/postgres/sqlcgen/**`; create pool/config and internal transaction runner tests under `platform/persistence/postgres`.

- [ ] 先写集成测试，证明 transaction runner 的 query handle 绑定同一 `pgx.Tx`，回调错误或 panic 会整体回滚，commit/context 错误不会被吞掉。
- [ ] 查询使用 CAS/`RETURNING`、明确列清单和稳定排序，不在 Go 中实现先查后写唯一裁决。
- [ ] 为 username claim、credential generation、grant/result consumption、TOTP step、audit head、outbox consumer offset/lease 和 key-rotation cursor 建立并发查询。
- [ ] 配置 sqlc `pgx/v5` 输出，schema 来源为 `infra/migrations`；添加根命令 `generate:sql`、组合 `generate` 和 `check:generated`，检查 tracked diff 与 untracked 新文件。
- [ ] 本任务不创建尚无领域接口可实现的 repository；`sqlcgen` 类型不得进入 `platform/identity|admin|profile|audit|outbox`，各领域 adapter 随 Task 7-12 的端口一起落地。
- [ ] 首轮 sqlc 产物暂存后再次生成，断言 `platform/persistence/postgres/sqlcgen` 没有 unstaged 或 untracked 漂移。

**Verify:** `go tool sqlc generate -f tooling/sqlc/sqlc.yaml`；随后 `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres'; go test -race ./platform/persistence/postgres/...`。

**Commit:** `feat(postgres): 建立查询生成与事务底座`

### Task 6：实现共享安全原语、配置和资源保护

**Files:** Create `platform/security/**`, `platform/clock/**`, `platform/identifier/**`, `platform/ratelimit/**`, `apps/internal/config/**`, `apps/api/internal/config/**`, unit tests and test key fixtures generated at runtime.

- [ ] 先写单元测试覆盖随机长度、token parser、常量时间比较、Argon2 参数升级、worker/queue 上限、AES-GCM AAD、key version 轮换、HMAC keyring 和错误 key。
- [ ] keyring 只从只读 JSON secret file 加载，校验 active/not-before/retire-after、重复 version/key 和至少 256-bit；生产无默认值或测试回退。
- [ ] 共享加载器放在 `apps/internal/config`，供 API、worker、migrate、adminctl 导入；各进程私有 listener/worker/CLI 选项留在自身 `internal/config`，平台包不反向导入 apps 配置。
- [ ] 为 PII、TOTP、result envelope、device、rate limit、user challenge、admin challenge、audit 分别声明强类型配置，防止传错 keyring。
- [ ] Argon2 服务使用固定并发 semaphore 和有界队列；selector 不存在走同参数 dummy hash。
- [ ] 定义领域可见的 `RateLimiter` port、bucket dimension/value object、方法级调用顺序和 fail-closed error；提供记录消费顺序、注入拒绝/依赖故障的测试 fake，领域服务从首次实现起必须使用，Redis adapter 在 Task 13 落地。
- [ ] 配置加载校验 PostgreSQL、Redis、Origin、trusted proxy、Cookie secure、checkpoint 阈值和 bootstrap secret file；所有错误只报告字段名，不回显 secret。
- [ ] 导出函数、关键常量、状态与异步清理按仓库注释规则说明意图、单位、所有权和失败影响。

**Verify:** `go test -race ./platform/security/... ./platform/ratelimit/... ./apps/internal/config/... ./apps/api/internal/config/...`。

**Commit:** `feat(security): 建立密钥哈希与配置基础`

### Task 7：实现 challenge 与一次性 result envelope

**Files:** Create `platform/secretresult/**`, shared challenge primitives under `platform/identity` and `platform/admin`, PostgreSQL repository implementations and tests.

- [ ] 先写状态机测试：new/available/confirmed/expired tombstone；相同 operation + digest 重放；不同 digest 冲突；跨 actor/scope/type 拒绝。
- [ ] AAD 包含 scope、actor/challenge、operation ID、digest、result type/version、key version、expiry；operation ID 从不单独构成授权。
- [ ] challenge Cookie secret 与 body proof 分别验证并绑定 purpose、audience、Origin、flow ID、TTL、尝试次数；用户和管理员使用不同 keyring/表中 purpose。
- [ ] 首次完成以条件更新消费 challenge；仅 exact replay authorization 延长到秘密 TTL，其他重放统一失败。
- [ ] confirm、TTL cleanup 和 retry 通过行锁/CAS 互斥；擦除 ciphertext、nonce、wrapped key 后保留 30 天 tombstone。
- [ ] 在本任务定义 `secretresult` 和 challenge repository/UnitOfWork 端口并实现对应 PostgreSQL adapter；adapter 只返回领域类型和领域错误，不向上泄漏 sqlc/pgx。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres'; go test -race ./platform/secretresult/... ./platform/identity/... ./platform/admin/...`。

**Commit:** `feat(auth): 实现挑战与一次性结果重放`

### Task 8：实现审计链、outbox 和 WORM checkpoint

**Files:** Create `platform/audit/**`, `platform/outbox/**`, `platform/persistence/objectstorage/{local,s3}/**`, PostgreSQL adapters and tests.

- [ ] 先写 canonical deterministic Protobuf、Ed25519 签名/验证、previous-hash、tamper detection 和 key rotation 测试。
- [ ] 审计 append 读取 head、构造/签名事件并调用受限函数；expected-head 冲突重试整个业务事务，不单独补写审计。
- [ ] outbox 使用 `outbox_consumers` 保存每个 consumer 的 last-acked sequence、subscription、lease owner/expiry 和 retry state；按 consumer 独立 claim/ack，禁止在 event 行写全局 ack。`audit.checkpoint` 注册生产 consumer，身份事件无 consumer 时保持 durable。
- [ ] 达到事件数/时间阈值时同事务写 `audit.checkpoint.pending`；敏感操作读取 durable checkpoint health 并在 5 分钟或 100 事件阈值后 fail closed。
- [ ] S3 sink 使用 `If-None-Match: *` 和确定性 key，上传后重新读取 metadata/hash；已存在同内容视为幂等，不同内容为完整性错误且不 ack。
- [ ] 生产配置验证 Object Lock/retention；本地 sink 使用 exclusive create 并明确标记 non-production。
- [ ] 在本任务定义 audit/outbox ports 和 UnitOfWork 参与者并实现 PostgreSQL adapter，证明业务 mutation、audit 与 outbox 使用同一 transaction-bound query handle。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres'; go test -race ./platform/audit/... ./platform/outbox/... ./platform/persistence/objectstorage/...`；S3 client contract 和 local sink 测试不得依赖外部服务或 skip。

**Commit:** `feat(audit): 实现签名审计链与检查点投递`

### Task 9：实现用户、用户名与设备核心

**Files:** Create `platform/identity/{model,username,device,service,ports,errors}*.go`, PostgreSQL adapter additions and tests.

- [ ] 先写 username 表驱动测试：NFKC、trim、Latin case fold、汉字/数字/下划线、2-20 code points、控制/格式/emoji/路径符、连续下划线、保留词和敏感词。
- [ ] 先写设备测试：token entropy/format、HMAC version、idle 180 天、absolute 365 天、30 天轮换、previous secret 两分钟以及旧 generation 不能安全写/覆盖 Cookie。
- [ ] 实现 `BeginIdentityBootstrap`、`BootstrapIdentity`、`CompleteOnboarding`、`GetCurrentIdentity`、`ChangeUsername` 的领域服务和端口。
- [ ] 实现 identity repository/UnitOfWork 的 PostgreSQL adapter；service 测试使用 fake port，adapter 集成测试使用真实 PostgreSQL，二者都不得依赖 sqlc 生成类型作为领域模型。
- [ ] 每个匿名创建、username 检查/占用流程按方法策略调用 `RateLimiter`；fake 测试断言 IP/device/username 桶顺序、拒绝时无数据库写入和依赖故障 fail closed。
- [ ] username claim 的占用、保留 90 天、30 天改名冷却、暂停保留和删除释放全部由数据库事务裁决。
- [ ] revoked credential 只有 secret 验证成功且原因为暂停/删除时返回对应状态指令；未知 selector 和其他撤销原因统一无效。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres'; go test -race ./platform/identity/... ./platform/persistence/postgres/...`，包含并发 claim、response-loss 和用户状态矩阵。

**Commit:** `feat(identity): 实现用户入驻与设备凭证`

### Task 10：实现用户恢复、设备管理与辅助 grant

**Files:** Extend `platform/identity/**`, PostgreSQL queries/adapters/tests.

- [ ] 先写恢复码 selector + Argon2 dummy 路径、grant entropy/Origin/challenge/version 绑定、attempt 上限和并发消费测试。
- [ ] 实现 `BeginRecoveryChallenge`、`BeginRecovery`、`CompleteRecovery`、`ConfirmSecretReceipt`、`RotateRecoveryCode`、`ListDevices`、`RevokeDevice`。
- [ ] 恢复先消费 IP + selector 桶，selector 命中后再消费 user 桶；该顺序位于领域服务并由 fake 测试证明，不能只依赖 transport interceptor。
- [ ] Complete 事务同时消费旧恢复凭证、创建设备、生成新恢复码、按选择撤销设备、撤销辅助 grant、写审计/outbox/result envelope。
- [ ] 辅助 grant 每用户最多一个 active；创建、暂停、删除或任一恢复成功时撤销旧 grant；Begin 中断不消费 grant，Complete 才变为 consumed(result_id)。
- [ ] suspended/deleted 禁止普通恢复，刚解除暂停和 active 用户才可使用辅助 grant。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres'; go test -race ./platform/identity/...`，包含并发消费、Begin/Complete 中断、response-loss、跨 actor operation 和 grant 替换。

**Commit:** `feat(identity): 实现身份恢复与设备管理`

### Task 11：实现管理员 bootstrap、密码、TOTP、会话与恢复

**Files:** Create `platform/admin/{model,bootstrap,password,totp,session,recovery,authorization,service,ports,errors}*.go`, PostgreSQL adapters/tests.

- [ ] 先按设计第 184-198 行写完整状态表测试，证明 setup/MFA/recovery token 的 purpose、audience、admin/password version、TTL 和消费约束。
- [ ] 完整实现并逐项测试 15 个 AdminAuth RPC：`GetSetupState`、`BeginAdminLogin`、`LoginPassword`、`VerifyTotp`、`ChangeInitialPassword`、`BeginTotpEnrollment`、`CompleteTotpEnrollment`、`ConfirmAdminSecretReceipt`、`RecoverAdmin`、`ChangeAdminPassword`、`BeginTotpRebind`、`CompleteTotpRebind`、`RegenerateAdminRecoveryCodes`、`LogoutAdmin`、`LogoutAllAdminSessions`。
- [ ] 多实例 bootstrap 使用 CAS；输家重新验证同一 secret，配置不一致 readiness 失败；管理员激活后仍挂载 secret readiness 失败。
- [ ] 密码至少 12 字符，拒绝 username 和内置泄漏密码集合；hash 保存算法/参数并支持登录时升级。
- [ ] TOTP 使用 6 位/30 秒/±1 window；`last_accepted_step` SQL CAS 与 challenge 消费、会话签发同事务，禁止并发重放。
- [ ] 初始化在首码成功事务中启用 seed、生成 10 个恢复码、保存 result envelope 并激活；envelope 失败不得激活。
- [ ] 管理员恢复码只能在密码产生的 MFA challenge 中替代第二因素；进入 recovery pending 后必须改密、重绑 TOTP 并生成新恢复码才恢复 active。
- [ ] permission-based `AdminAuthorizer` 默认拒绝；任何 pending token 不得访问管理业务 API。
- [ ] 管理员密码、TOTP 和恢复流程从首次实现起通过 limiter port 消费 IP + account + fixed-purpose 桶，并测试任一桶拒绝或 Redis 故障时不推进 challenge/session 状态。
- [ ] 实现 admin repository/UnitOfWork 的 PostgreSQL adapter，保持状态机类型与 sqlc/pgx 隔离。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres'; go test -race ./platform/admin/...`，并发 TOTP/bootstrap/enrollment/recovery 与 session revocation matrix 全部执行。

**Commit:** `feat(admin): 实现管理员多因素认证`

### Task 12：实现真实姓名、资料导出与管理员用户操作

**Files:** Create `platform/profile/**`, admin identity orchestration under `platform/admin`, PostgreSQL adapters/tests.

- [ ] 先写 PII AAD、wrong-user/wrong-field substitution、key rotation、审计失败不返回明文测试。
- [ ] 实现 `GetUser`、`GetRealName`、`UpdateRealName`，读取和修改在返回/提交前完成权限、限流和审计。
- [ ] PII 读取、修改、导出通过 limiter port 消费 admin session + target user 桶；失败时不解密、不创建 export context、不追加披露审计。
- [ ] 创建导出 context 时按 user ID/ordinal 物化加密快照；分页使用稳定 keyset cursor，每页先提交 target digest/profile/schema version checkpoint 审计后解密返回。
- [ ] 实现 complete/abort/expired terminal state，关闭后拒绝分页；响应/日志不缓存或记录明文。
- [ ] 实现强制改名、暂停、解除、删除、设备撤销、辅助恢复 grant 和审计列表；全部提交 reason、permission、审计与 outbox。
- [ ] 删除事务先把 username claim reserved，再清空 current FK；解除暂停不恢复旧凭证。
- [ ] 实现 profile/admin-identity repository 与跨模块 UnitOfWork PostgreSQL adapter，真实测试证明 profile、identity、audit、outbox 同事务提交。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres'; go test -race ./platform/profile/... ./platform/admin/...`，覆盖物化导出、target digest、状态矩阵和 audit recursion guard。

**Commit:** `feat(admin): 实现资料管理与治理操作`

### Task 13：实现 Redis 限流、可信代理和可观测性

**Files:** Create `platform/persistence/redis/**`, `apps/api/internal/transport/{ratelimit,proxy,metrics,logging}/**`, tests.

- [ ] 先写 Lua token bucket 原子性、refill、TTL、并发和随机 key-prefix 集成测试。
- [ ] 实现 Task 6 已冻结的 limiter port，不在本任务改变领域调用顺序；用户、设备、username、selector、管理员账号、flow purpose、session 和 target user 分桶独立消费，Redis key 只保存版本化 HMAC 标识。
- [ ] 用 Task 6 的策略契约回放设计第 307-316 行全部 API，多桶顺序和 fail-closed 矩阵必须与 fake 测试一致；普通已认证身份只读不查 Redis。
- [ ] trusted proxy 从 socket peer 由右向左剥离连续可信 CIDR；重复/冲突 Forwarded/XFF、超限和不可信上游忽略头并增加安全指标。
- [ ] Prometheus label 只允许 operation/result/dimension；slog redactor 和 Connect interceptor 禁止 body、cookie、secret、PII。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='redis'; go test -race ./platform/persistence/redis/... ./apps/api/internal/transport/...`。

**Commit:** `feat(api): 实现限流代理解析与安全观测`

### Task 14：装配 Connect API、Cookie/CSRF 和进程入口

**Files:** Create `apps/api/main.go`, `apps/api/internal/server/**`, `apps/api/internal/transport/{identity,adminauth,adminidentity,errors,cookies,csrf,origin}/**`, generated handler adapters and tests.

- [ ] 先用 Connect clients + `httptest` 写三个 service 的传输测试，覆盖 RPC 到领域命令、稳定错误 detail 和 header/cookie 行为。
- [ ] 遍历三个生成 service descriptor，对每个 RPC 发送满足传输解析的最小请求；允许返回业务/认证错误，但任何方法都不得返回 Connect `Unimplemented`，防止嵌入 generated stub 假完成。
- [ ] 用户、管理和匿名 challenge 分别使用设计第 260-285 行的 `__Host-` Cookie 属性；禁止 Domain，生产禁止关闭 Secure。
- [ ] 所有认证写请求依次验证 Origin、double-submit CSRF 和服务端绑定 hash；anonymous Begin/Complete 验证 challenge Cookie + proof。
- [ ] sensitive method registry 统一设置 `Cache-Control: no-store`、`Pragma: no-cache` 并关闭 body trace/error sampling；新增敏感 RPC 必须显式注册，否则测试失败。
- [ ] 管理域只注册 admin paths，用户域只注册 identity paths；即使同一进程监听，interceptor、cookie、origin、CSRF、rate key 和 auth context 不复用。
- [ ] 公开 readiness 分别报告 PostgreSQL、Redis、bootstrap secret、keyring 和 checkpoint health；敏感写降级与普通读取就绪状态分开。
- [ ] 实现优雅关闭、连接 drain、后台 goroutine ownership 和清理注释，退出时不丢已 claim 但未 ack 的 outbox。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres,redis'; go test -race ./apps/api/...`，API matrix 覆盖 cross-origin/cookie、pending-token privilege、no-store 和全部 RPC 非 `Unimplemented`。

**Commit:** `feat(api): 提供身份与管理 Connect 服务`

### Task 15：实现 worker、迁移命令和离线 adminctl

**Files:** Create `apps/worker/**`, `apps/adminctl/**` and process-specific config; extend `apps/migrate/**`, PostgreSQL key-rotation queries/adapters and operations tests.

- [ ] worker 只注册 `audit.checkpoint` consumer；身份事件保持 durable 未 ack，不因无消费者被清理。
- [ ] 添加 secret ciphertext/tombstone、onboarding、username reservation、export context/item 和 lease 恢复清理任务；每项使用数据库时间边界/CAS 并可重复执行。
- [ ] 覆盖全部 TTL 状态：anonymous/admin challenge 过期 5 分钟且 terminal metadata 保留 24 小时；admin session 按 idle/absolute expiry 失效且 terminal metadata 保留 30 天；user recovery attempt/grant、admin recovery token、assisted grant 的 terminal metadata 保留 30 天；过期 TOTP enrollment 立即擦除 seed 密文并保留 30 天 metadata。创建新 active/pending 状态的事务必须先原子淘汰已过期行，避免 partial unique 被陈旧记录占用。
- [ ] checkpoint dispatcher 使用 worker DSN 和独立 consumer offset；claim 后 create-if-absent、read-back verify、ack，崩溃或网络错只释放/过期 lease 后重试。
- [ ] worker 根据 active PII/TOTP key version 创建或恢复 `key_rotation_jobs`，按稳定游标小批量重加密；每行使用 version/source-key 条件更新避免覆盖并发资料/TOTP 变化，job start/batch/complete 与数据批次在同一 UnitOfWork 追加签名审计，只记录 ID/count/digest，不记录明文。
- [ ] API/worker 启动查询仍被密文引用的 key version；任一引用 version 不在对应 keyring 时拒绝启动，仍有引用时 retire/remove 旧 key 的测试必须失败。result envelope 等 TTL 密文等待安全擦除，不做长期重加密。
- [ ] `adminctl reset` 要求 migration DSN + 一次性 secret file + audit signing keyring + 明确确认参数；先读取 audit head、构造 canonical event 并 Ed25519 签名，再由专用函数在同一事务进入 `setup_required`、撤销认证材料、追加签名审计并按阈值写 checkpoint outbox，不增加 HTTP 等价接口。
- [ ] CLI 禁止在参数或错误中打印 secret，支持结构化退出码和 dry-run 状态检查但不展示 hash/seed。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres,postgres-privileges'; go test -race ./apps/worker/... ./apps/adminctl/...`，覆盖 worker 权限、清理重入、rotation resume/concurrent update/old-key rejection，以及 reset 后整条审计签名链验证。

**Commit:** `feat(operations): 实现后台任务与离线恢复命令`

### Task 16：交付 Nginx 安全入口配置

**Files:** Create `infra/nginx/nginx.conf`, `infra/nginx/templates/game-night.conf.template`, `infra/nginx/test/**`.

- [ ] 用户域只路由 IdentityService，管理域只路由 AdminAuthService/AdminIdentityService；错误 Host、跨域 service path 和未列出的 upstream path 返回拒绝，不回退到默认 backend。
- [ ] Nginx 覆盖而非追加客户端 `Forwarded`/`X-Forwarded-For`，只把 socket peer 作为最右入口交给应用；测试伪造左端、重复和冲突头不能穿透。
- [ ] 身份/管理 API 禁用 proxy cache，保留或强化应用 `Cache-Control: no-store` 与 `Pragma: no-cache`；TLS 配置保证 `__Host-` Cookie 的 Secure 前提。
- [ ] 容器测试使用两个可区分的 stub upstream、临时证书和固定 digest Nginx image，执行 `nginx -t` 并实际请求验证 Host/path 路由、代理头和缓存响应头。
- [ ] 集成夹具在 `nginx` 被 require 但 Docker/runtime/image 不可用时失败，未 require 时明确 skip。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='nginx'; go test -race ./infra/nginx/test/...`。

**Commit:** `feat(infra): 配置身份管理安全入口`

### Task 17：补齐全链路集成测试与 CI 服务

**Files:** Create/extend integration tests under `apps/api`, `platform/**`, `internal/integrationtest`; modify `.github/workflows/ci.yml`, `package.json`.

- [ ] 新增 `backend-integration` job，启动固定 major 的 PostgreSQL 与 Redis service containers，设置 `GAME_NIGHT_REQUIRE_INTEGRATION=postgres,postgres-privileges,redis,object-storage,nginx`；管理员 DSN 负责创建随机 database/roles，真实 runtime/worker DSN 执行允许与禁止权限测试。
- [ ] 在 Linux CI 启动固定 digest 的 MinIO，创建启用 Object Lock 的审计 bucket 和最小权限应用账号；真实验证 create-if-absent、read-back、retention 下 overwrite/delete 失败。
- [ ] CI 向测试夹具提供完整 `GAME_NIGHT_TEST_S3_ENDPOINT/REGION/BUCKET/ACCESS_KEY/SECRET_KEY`；`object-storage` 被 require 时任一配置缺失、Object Lock 未启用或 overwrite/delete 意外成功都必须失败。
- [ ] 加入从全新浏览器到入驻、恢复、管理员 15 个 Auth RPC、PII 写读导出、暂停/解除/辅助恢复和设备撤销的 Connect client 场景；service descriptor 中任何 RPC 返回 `Unimplemented` 直接失败。
- [ ] 注入“数据库 commit 成功但响应被丢弃”“checkpoint 上传前崩溃”“Redis 断连”“审计 append 冲突”“key version 轮换”故障。
- [ ] 在 Linux 执行 `go test -race ./platform/... ./apps/...`；race 与普通集成测试均不得 skip 必需依赖。
- [ ] 生成后检查 tracked diff 和 untracked files；Proto 和 sqlc 任一漂移使 CI 失败。
- [ ] 扫描捕获日志和响应，断言测试 secret/token/password/TOTP/real name 均未泄漏。

**Verify:** `$env:GAME_NIGHT_REQUIRE_INTEGRATION='postgres,postgres-privileges,redis,object-storage,nginx'; go test -race ./platform/... ./apps/... ./internal/integrationtest/... ./infra/nginx/test/...`；随后运行 `go vet ./...`、`pnpm run check:generated`、`pnpm run check`、`git diff --check`。

**Commit:** `ci(backend): 验证身份管理真实依赖链路`

### Task 18：更新运维文档并执行最终验收

**Files:** Create `docs/operations/backend-identity.md`; modify `README.md`, `docs/operations/development.md`; no generated/manual secret files.

- [ ] 文档化 PostgreSQL 角色、migration、API/worker/adminctl 启动顺序、secret-file 格式、key rotation、管理员首次初始化和移除 bootstrap secret。
- [ ] 文档化 API runtime/worker/migration-adminctl 的独立 DSN 和 grant，Nginx 用户域/管理域部署，以及 outbox consumer 注册/offset 恢复。
- [ ] 文档化开发环境明确 non-production local checkpoint sink；生产必须 Object Lock/WORM，并说明 readiness/fail-closed 阈值。
- [ ] 文档化所有测试环境变量、随机 schema/prefix 行为和 `SKIPPED` 与完整验收的区别。
- [ ] README 当前阶段更新为后端身份与管理已交付，但不宣称前端或房间已完成。
- [ ] 运行最终命令并保存结果摘要；任何失败继续修复，不以“已知失败”结束。

**Verify:**

```powershell
$env:GAME_NIGHT_REQUIRE_INTEGRATION = 'postgres,postgres-privileges,redis,object-storage,nginx'
pnpm install --frozen-lockfile
pnpm run generate
pnpm run check:generated
buf format --diff --exit-code
buf lint
go test ./...
go vet ./...
pnpm run check
pnpm test
pnpm build
git diff --check
git status --short
```

Linux CI 另运行 `go test -race`；上述 require 集合确保 PostgreSQL 权限、Redis、Object Storage 和 Nginx 真实测试均不能跳过。

**Commit:** `docs(backend): 补充身份管理部署与验收说明`

## 7. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 大范围状态机一次落地造成难以审查的提交 | 19 个有依赖顺序的单一目的任务；每个任务先测试、独立提交、独立验证 |
| response-loss 导致重复发凭证或恢复码 | 所有秘密操作共用 actor/scope/operation/digest 唯一键、原子 result envelope 和 tombstone |
| Argon2 成为 CPU DoS 放大器 | Redis 多维桶 + 本地固定 worker 数 + 有界队列 + dummy hash 同成本 |
| 领域包被 pgx/Redis/HTTP 污染 | Task 1 先扩边界检查，CI 通过 import edge 强制 |
| SECURITY DEFINER 被 search path 或直接表权限绕过 | DDL 写入显式 trusted schema、对象全限定、non-login owner、revoke public/runtime 底表权限和 shadow 攻击测试 |
| 审计 checkpoint 上传成功但 ack 丢失 | 确定性 key + create-if-absent + read-back hash，重复同内容幂等 |
| bootstrap secret 长期留在部署中 | 激活后 readiness fail，运维步骤强制移除并重启 |
| 本地没有 Docker 导致误判集成测试通过 | 统一逗号分隔 require 集合；CI 要求 object-storage/nginx 时缺少 Docker 或配置直接失败 |
| 生成工具升级导致大量不可复现漂移 | Go tool directive、Buf remote plugin pin、两次生成零漂移门禁 |

## 8. 执行交接

实现按 Task 0-18 顺序推进；Task 4 之后允许在不修改同一文件的前提下并行准备安全原语、审计和领域单元测试，但 migration/schema、Proto 和共享端口变更必须由单一负责人串行合并。每次提交前必须检查 `git status --short`、`git diff --staged --name-only`、`git diff --staged --check` 和暂存补丁，只提交当前任务文件。

计划完成的定义不是“代码已写完”，而是设计第 532-546 行全部验收标准均有真实测试证据、工作树干净且不存在已知错误。计划执行期间若必须改变已确认的外部 API、安全状态机、密钥边界或数据库权威语义，应先修改设计并重新审查；普通文件拆分和内部命名不需要重新询问用户。
