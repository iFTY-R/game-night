# Game Night 管理后台重建设计

> 日期：2026-07-25
>
> 状态：已确认，待实现计划
>
> 范围：直接重建管理端前后端，不兼容现有开发期后台页面、管理契约和管理员认证状态机

## 1. 背景

现有 `apps/admin` 完成了独立管理域名、Connect 请求、管理员 Cookie/CSRF、SoybeanAdmin 风格壳层和少量用户、审计页面，但它主要证明了后台可以独立运行，并没有形成可运营的管理系统。当前概览展示会话与 readiness，用户页以精确查询为主，安全页只提供退出会话，房间、牌局、批量治理和运维能力均不存在。

本次不在旧页面上继续增加占位入口，也不以 MVP 为目标。管理后台按生产级运营工具重新设计：每个正式导航模块必须连接真实查询和真实命令，形成查询、操作、结果、审计和错误恢复闭环。仓库仍处于开发环境，管理端相关契约、表结构和测试数据可以直接替换，无需为旧后台承担兼容成本。

## 2. 已确认决策

- 当前只保留一个管理员账号，但服务端权限、前端路由、菜单和审计事件按未来多管理员 RBAC 边界设计。
- 2FA 默认关闭，由管理员在安全设置中主动开启或关闭，不再由进程环境变量决定单个账号的登录流程。
- 修改密码和 2FA 状态互相独立；修改密码不强制重绑 TOTP。
- 房间和牌局同时支持日常受控命令与紧急修正命令。紧急命令必须短时提权、预演、填写原因并保存前后快照。
- 用户管理包含标签、备注、组合筛选、批量处置和导出。
- 运维模块提供监控与固定的受控维护命令，不提供任意 Shell、任意 SQL 或环境变量编辑。
- 游戏规则版本发布、完整牌局回放和告警中心暂不实现，也不出现在导航中。
- 不兼容旧管理端页面、旧管理端 RPC 和旧管理员认证状态；必要时直接重置开发数据库中的管理端数据。

## 3. 目标

1. 提供运营概览、用户中心、房间与牌局、安全设置、审计中心和系统运维六个完整模块。
2. 将后台查询与用户端热路径隔离，所有列表具备服务端分页、筛选、排序和稳定游标。
3. 将管理员写操作建模为显式领域命令，提供原因、幂等、并发保护、结果回执和审计。
4. 建立默认关闭、可自主启停的账户级 2FA，并提供完整密码、恢复码和会话管理体验。
5. 建立短时提权机制，使高危操作只能在重新验证管理员身份后执行。
6. 保持当前单管理员部署简单，同时让未来加入管理员、角色和权限时无需重写业务接口。
7. 删除展示型占位页面、开发者文案和无后端来源的数据。

## 4. 非目标

- 不实现游戏规则草稿、灰度发布、版本回滚或房间默认规则编辑。
- 不实现按操作时间轴的完整牌局回放、随机数承诺调查或调查包导出。
- 不实现站内告警、邮件、短信或 Webhook 通知中心。
- 不提供任意数据库字段编辑、任意 JSON 状态覆盖、任意 SQL、任意 Shell 或主机级服务管理。
- 不在本次引入多管理员管理页面、角色编辑器或动态菜单配置；只建立可演进的权限边界。
- 不保留旧管理端协议的弃用期、兼容适配器或旧前端路由跳转。

## 5. 总体架构

### 5.1 前端

`apps/admin` 保持独立 Vite SPA、Naive UI、Pinia、Vue Router 和 Connect JSON 技术栈，但业务页面直接重建。可以保留经验证的基础设施：

- 管理域名、静态资源和用户端域名隔离。
- Connect unary 传输、Cookie/CSRF、稳定错误解析和请求 ID。
- `AppDialog`、异步状态、布局骨架、主题变量和 Lucide 图标接入。
- 严格 TypeScript、Vitest、Playwright 和 workspace 构建配置。

旧概览、用户工作台、安全会话页和围绕旧认证 `next_step` 编排的页面不作为兼容边界。新页面按业务域组织：

- `views/overview`
- `views/users`
- `views/rooms`
- `views/security`
- `views/audit`
- `views/operations`

每个域包含 API adapter、query store、表格/筛选组件、详情抽屉和命令对话框。全局 store 只保存管理员会话、权限、短时提权摘要和界面偏好，不缓存业务列表或敏感结果。

### 5.2 管理端服务

旧的宽泛管理服务拆为明确的 Connect 服务：

| 服务 | 责任 |
| --- | --- |
| `AdminAuthService` | 初始化、登录、会话自省、改密、2FA、恢复码、会话撤销、短时提权 |
| `AdminOverviewService` | 运营指标、趋势、异常摘要和服务健康摘要 |
| `AdminUserService` | 用户列表、详情、标签备注、设备会话、游戏摘要、治理命令、批量任务、导出 |
| `AdminRoomService` | 房间/牌局列表与详情、实时状态、日常控制命令、紧急修正命令 |
| `AdminAuditService` | 审计检索、详情、变更差异和导出 |
| `AdminOperationsService` | 依赖健康、版本、任务积压、限流状态、维护模式和固定缓存刷新命令 |

查询服务只读取管理查询模型。命令服务调用现有身份、房间、游戏运行时、审计和 outbox 领域服务，不从传输层直接更新数据库。

### 5.3 请求上下文

所有受保护请求由统一拦截器建立 `AdminActorContext`：

- 管理员 ID
- 会话 ID 与会话版本
- 当前权限集合
- 当前短时提权 grant 及允许的操作范围
- 规范化 Origin、客户端 IP、User-Agent 摘要和请求 ID

传输消息不得接受前端传入的管理员 ID、权限或提权状态。服务端授权器在每个命令入口重新校验会话、账号版本、权限和提权范围。

## 6. 管理员认证与 2FA

### 6.1 账户状态

管理员账号只保留三个持久状态：

- `bootstrap_pending`：尚未写入初始密码。
- `setup_required`：已写入部署初始密码，浏览器首次登录必须修改。
- `active`：可以正常登录和使用后台。

2FA 不再作为管理员账户状态。是否开启由当前是否存在有效的 active TOTP enrollment 决定。没有 active enrollment 就是关闭，新的或重置后的管理员默认关闭。

### 6.2 登录

1. 浏览器取得一次性登录 challenge。
2. 管理员提交密码与 challenge proof。
3. `setup_required` 返回仅允许修改初始密码的短时会话。
4. `active` 且未启用 2FA 时直接签发 full session。
5. `active` 且已启用 2FA 时签发 MFA-pending 会话，验证 TOTP 或一次性恢复码后换取 full session。

前端刷新时调用会话自省接口。服务端直接返回当前会话摘要、权限、2FA 状态和提权摘要；前端不再使用通用 `next_step` 推断安全设置状态。

### 6.3 修改密码

修改密码要求 full session、当前密码和新密码：

- 新密码执行统一长度、泄漏词和 Argon2id 策略校验。
- 事务内更新密码版本，撤销除当前操作外的全部会话，并签发绑定新密码版本的 full session。
- 已有 TOTP enrollment 保持有效，不进入重绑流程。
- 审计记录目标管理员、密码版本变化和撤销会话数量，不记录密码内容或哈希。

初始密码修改只要求 setup session 和新密码。成功后账号进入 `active`，签发 full session，2FA 仍为关闭。

### 6.4 开启 2FA

开启流程只能从 full session 发起：

1. 重新验证当前密码，创建短时 enrollment operation。
2. 服务端生成 TOTP seed，使用独立 keyring 加密，只返回一次二维码 URI 与手工密钥。
3. 管理员输入首个 TOTP code。
4. 事务内激活 enrollment、推进 replay floor、生成一组恢复码并撤销旧恢复码。
5. 撤销其他管理员会话并刷新当前 full session，避免启用前已签发的仅密码会话继续存活。
6. 返回一次性恢复码结果；确认接收或超时后擦除可恢复密文。

开启后当前浏览器使用刷新后的 full session，之后的新登录要求第二因素。关闭或刷新未完成的绑定页面不能激活 pending enrollment。

### 6.5 关闭 2FA

关闭要求 full session 和 `security.disable_mfa` elevation。该 elevation 通过统一提权流程验证当前密码与当前 TOTP；使用恢复码登录后没有可用 TOTP 时，可以用另一枚未消费恢复码完成提权。关闭命令本身不重复索取密码或验证码：

- 事务内禁用 active enrollment并擦除 seed 密文。
- 撤销全部恢复码。
- 撤销其他管理员会话，刷新当前 full session。
- 写入包含 enrollment ID、会话撤销数量和原因的安全审计。

重复关闭返回稳定的“已经关闭”状态，不重新执行副作用。没有 active enrollment 时前端不显示验证码输入。

### 6.6 重新生成恢复码

重新生成只在 2FA 已开启时可用，要求 full session 和 `security.regenerate_recovery_codes` elevation。提权必须验证当前密码与 TOTP；TOTP 不可用时可以使用一枚未消费恢复码。

命令在同一事务中撤销全部旧恢复码、生成完整新集合并保存一次性结果 envelope。任何旧码在事务提交后立即失效，不能与新集合并存。响应丢失时，相同 operation 可以在秘密 TTL 内重放同一结果；确认接收或超时后擦除恢复码明文，只保留不可恢复 tombstone。关闭对话框不会重新生成第二套恢复码。

审计记录旧集合版本、新集合版本、生成数量、使用的 elevation scope 和结果，不记录恢复码。自动化测试必须覆盖旧码立即失效、并发重发只有一个成功、响应丢失重放和确认后不可再次读取。

### 6.7 恢复与离线重置

恢复码只在 2FA 开启时存在，每个码只能使用一次。密码正确后可使用恢复码替代一次 TOTP 登录，成功后签发 full session并提示恢复码余量；不会自动关闭 2FA。

同时丢失密码与第二因素时，不提供 HTTP 重置接口。开发环境和部署人员使用离线 `adminctl reset` 重建管理员认证数据、撤销全部会话并恢复为 `setup_required`。由于不要求兼容旧后台，管理端相关测试数据库可以直接清空重建。

## 7. 短时提权

高危命令不因 full session 自动获得执行资格。管理员在操作前申请 elevation：

1. 提交当前密码。
2. 已开启 2FA 时默认同时提交 TOTP；关闭 2FA 时只验证密码。只有强制矩阵明确标记“允许恢复码替代”的 scope 才能提交一枚未消费恢复码代替 TOTP，其它 scope 一律不接受恢复码。
3. 服务端签发最长 5 分钟的 elevation grant，绑定管理员、当前 session、密码版本、管理员版本和操作 scope。
4. grant 只保存摘要，不作为独立浏览器 Token 持久化。TTL 到期、退出、session 撤销、改密、2FA 变化、管理员版本变化或主动撤销都会使其失效。服务端不把“浏览器关闭”作为可可靠检测的安全边界。

scope 至少区分：

- `users.bulk_governance`
- `users.revoke_devices`
- `users.delete`
- `rooms.force_close`
- `games.force_terminate`
- `games.emergency_repair`
- `operations.maintenance`
- `security.disable_mfa`
- `security.regenerate_recovery_codes`
- `security.revoke_sessions`
- `audit.export_sensitive`

前端顶部只显示“已提权”和剩余时间，不把 grant 权限当作永久管理员权限。服务端对每条高危命令再次校验 scope。

### 7.1 强制操作分级

实现必须遵守下表，页面不得自行降低要求。取得 elevation 时完成一次身份验证；随后在 grant TTL 内执行同 scope 命令不重复输入密码或 TOTP，但服务端仍重新校验 grant。

| 操作 | 基础权限 | elevation scope | 提权时身份验证 | 强制命令约束 |
| --- | --- | --- | --- | --- |
| 列表、详情、普通审计读取 | 对应 `*.read` | 不需要 | 无 | 稳定筛选、分页和访问审计 |
| 创建/修改标签、用户标签关联、追加备注 | `users.annotate` | 不需要 | 无 | expected version；标签变更要求原因；备注只追加不静默覆盖 |
| 单用户封禁/解封 | `users.govern` | 不需要 | 无 | 原因、operation ID、expected version |
| 强制退出用户全部设备 | `users.govern` | `users.revoke_devices` | 密码；2FA 开启时验证 TOTP，不允许恢复码替代 | 影响预览、原因、operation ID、expected user version |
| 踢出用户当前房间/踢出指定玩家 | `rooms.control` | 不需要 | 无 | 原因、operation ID、expected room/player version；进行中牌局可拒绝 |
| 注销账号 | `users.govern` | `users.delete` | 密码；2FA 开启时验证 TOTP，不允许恢复码替代 | 注销预览、阻塞项、原因、operation ID、expected user version |
| 批量用户治理的预览 | `users.govern` | 不需要 | 无 | 固化筛选、目标版本和短 TTL，不产生治理副作用 |
| 启动/取消/重试批量用户治理 | `users.govern` | `users.bulk_governance` | 密码；2FA 开启时验证 TOTP，不允许恢复码替代 | 有效 preview、原因、operation ID；逐项幂等和结果 |
| 禁止/恢复房间加入 | `rooms.control` | 不需要 | 无 | 原因、operation ID、expected room version |
| 强制解散等待房间 | `rooms.control` | `rooms.force_close` | 密码；2FA 开启时验证 TOTP，不允许恢复码替代 | 影响预览、原因、operation ID、expected room version |
| 终止进行中牌局 | `games.control` | `games.force_terminate` | 密码；2FA 开启时验证 TOTP，不允许恢复码替代 | 影响预览、原因、operation ID、expected game version |
| 紧急状态修正 | `games.repair` | `games.emergency_repair` | 密码；2FA 开启时验证 TOTP，不允许恢复码替代 | 同一 dry-run、前后快照、原因、operation ID、expected versions |
| 修改密码 | `security.manage_password` | 不需要 | 命令内验证当前密码 | 新密码策略、operation ID、当前密码版本 |
| 开启 2FA | `security.manage_mfa` | 不需要 | enrollment 开始时验证当前密码 | enrollment operation、首个 TOTP、一次性恢复码确认 |
| 关闭 2FA | `security.manage_mfa` | `security.disable_mfa` | 密码 + TOTP；明确允许未消费恢复码替代 TOTP | 原因、operation ID、expected enrollment version |
| 重新生成恢复码 | `security.manage_mfa` | `security.regenerate_recovery_codes` | 密码 + TOTP；明确允许未消费恢复码替代 TOTP | operation ID、expected recovery-set version、一次性结果确认 |
| 撤销一个其他管理员会话 | `security.manage_sessions` | 不需要 | 无 | operation ID、目标 session ID、expected session version |
| 撤销其他全部管理员会话 | `security.manage_sessions` | `security.revoke_sessions` | 密码；2FA 开启时验证 TOTP，不允许恢复码替代 | 影响预览、operation ID、当前 admin/session version |
| 退出当前管理员会话/主动撤销 elevation | 已认证会话 | 不需要 | 无 | 只作用于当前 session/grant，幂等关闭 |
| 创建普通脱敏导出 | 对应 `*.export` | 不需要 | 无 | 固化筛选、字段、遮蔽策略、operation ID 和 TTL |
| 创建或下载敏感用户/审计导出 | 对应 `*.export` | `audit.export_sensitive` | 密码；2FA 开启时验证 TOTP，不允许恢复码替代 | 固化数据上界；下载使用 session 绑定的单次 grant |
| 删除自己的导出结果 | 对应 `*.export` | 不需要 | 无 | operation ID、export ID、expected export version |
| 开启/关闭维护模式、缓存刷新、任务重试 | `operations.maintain` | `operations.maintenance` | 密码；2FA 开启时验证 TOTP，不允许恢复码替代 | 影响预览、原因、operation ID、expected operation/state version |

未列入表格的新写命令必须在协议评审时明确归类，不能默认继承“不需要 elevation”。

## 8. 权限模型

当前单管理员在 active 状态拥有全部已注册权限，但接口仍声明细粒度权限。权限按资源与动作命名，例如：

- `overview.read`
- `users.read`、`users.read_pii`、`users.annotate`、`users.govern`、`users.export`
- `rooms.read`、`rooms.control`
- `games.read`、`games.control`、`games.repair`
- `security.read`、`security.manage_password`、`security.manage_mfa`、`security.manage_sessions`
- `audit.read`、`audit.export`
- `operations.read`、`operations.maintain`

菜单、路由、按钮和后端授权器引用同一份生成权限枚举。未来增加管理员和角色时，只需要增加账号/角色/授权关系与管理页面，不修改业务 RPC 的授权语义。

## 9. 正式模块

### 9.1 运营概览

概览只展示可行动的真实数据：

- 当前在线用户、活跃房间、进行中牌局。
- 最近 24 小时新增用户、封禁/解封、异常终止和紧急修正数量。
- 按小时或按天的活跃趋势。
- 需要关注的房间和牌局摘要，例如长时间无推进、玩家全部离线、运行时 owner 缺失。
- PostgreSQL、Redis、realtime、worker、审计 checkpoint 和任务队列健康摘要。
- 最近高危操作和失败批量任务。

聚合接口返回指标值、统计窗口、采样时间和数据新鲜度。页面不能把 readiness、会话 kind 或权限数量伪装成运营指标。

### 9.2 用户中心

用户列表支持：

- 用户 ID、规范化用户名、状态、标签、创建时间、最近活动时间和是否在线的组合筛选。
- 稳定排序与游标分页。
- 当前筛选条件导出。
- 多选并创建批量任务。

用户详情抽屉分区展示：

- 基础身份与状态。
- 按权限读取的真实姓名等 PII。
- 管理员标签与备注历史。
- 活跃设备、会话和最近活动。
- 房间与游戏记录摘要，不提供完整牌局回放。
- 最近治理动作和相关审计。

单用户命令包括封禁、解封、强制退出全部设备、踢出当前房间和注销账号。每个命令必须展示影响、填写原因、使用幂等键并刷新详情。批量命令支持封禁、解封、踢出房间和导出；执行前返回精确目标数与不可执行项预览。

“注销账号”是受控的逻辑删除与隐私擦除流程，不是硬删除数据库行：

1. 预览必须列出当前房间、进行中牌局、活跃设备、恢复凭据和待完成导出。用户仍在进行中牌局时拒绝注销，管理员必须先通过独立牌局终止流程解除阻塞。
2. 事务内把用户状态改为 `deleted`，撤销设备、恢复凭据和辅助恢复 grant，并写出将其移出等待房间的 durable outbox 事件。
3. username claim 保留 90 天后释放；期限内不能被其他账号重新注册。
4. PII、用户标签和管理员备注进入有状态的擦除任务。任务完成后历史房间、牌局和审计只保留稳定用户 ID及“已注销用户”显示，不保留可恢复 PII。
5. 签名审计链、游戏结果和依法/业务完整性必须保留的关联记录不可物理删除，但详情页和导出必须应用删除用户遮蔽规则。

注销预览、确认、擦除任务逐项结果和最终完成都写审计。重复 operation 返回同一注销/擦除状态，不创建第二个任务。

### 9.3 房间与牌局

房间列表支持状态、游戏类型、房主、玩家、创建时间、运行时 owner 和异常标记筛选。详情展示：

- 房间基础信息、规则摘要和绑定游戏版本。
- 玩家席位、在线状态、准备状态和加入时间。
- 当前牌局 ID、阶段、行动者、版本和最后推进时间。
- 最近有限条运行事件摘要，用于判断卡住或异常，不构成完整回放。
- 已执行控制命令与审计。

日常命令：

- 禁止或恢复新玩家加入。
- 踢出指定玩家。
- 强制解散等待房间。
- 终止异常进行中牌局。

日常命令必须经过房间/游戏领域服务和 realtime owner 执行，不直接更新数据库状态。结果明确区分已执行、目标已变化、owner 不可达和需要紧急修正。

紧急修正只提供经过评审的固定命令，例如：

- 清理失联 owner lease 并重新分配。
- 将确定无法继续的牌局终止为指定系统终态。
- 修复房间与当前牌局引用不一致。

每个修正先执行 dry-run，返回预期版本、影响记录和不可逆副作用；确认时要求 `games.emergency_repair` elevation、相同预期版本和原因。事务内保存前后快照、命令版本和结果，禁止自由编辑状态 JSON。

### 9.4 安全设置

安全页展示并操作：

- 当前管理员账号、密码最后修改时间和 2FA 开启状态。
- 修改密码表单。
- 2FA 开关；开启进入二维码/验证码/恢复码向导，关闭进入重新验证对话框。
- 恢复码剩余数量与重新生成流程。
- 当前及其他管理员会话列表，包括创建时间、最后活动、客户端摘要和到期时间。
- 撤销单个会话、撤销其他会话和退出当前会话。
- 当前短时提权状态及主动撤销。

页面不展示 `next_step`、持久化范围、内部会话枚举等开发者信息。秘密仅存在组件内存，关闭对话框、切换路由、确认接收或过期后立即清理。

### 9.5 审计中心

审计列表支持操作者、目标类型/ID、动作、结果、风险级别、请求 ID 和时间范围筛选。详情展示：

- 规范化动作与结果。
- 操作者、目标和权限/elevation scope。
- 原因、请求上下文摘要、幂等键和关联任务。
- 结构化变更前后差异，敏感字段保持遮蔽。
- 签名、链序号和 checkpoint 验证状态。

导出采用异步任务并绑定筛选条件与字段集合。PII 或高风险审计导出要求 elevation。读取和导出本身也写审计，但列表不会无意义自动轮询。

`ListAuditEvents` 在查询开始时固定可见的最大 chain sequence，再写一条 `audit.events.listed` 访问事件；该访问事件不进入本次结果，因此不会形成自增长分页。`GetAuditEvent` 写 `audit.event.read`，目标是被读取的 chain sequence。服务端内部追加审计不是审计读取，不递归生成读取事件。相同页面的自动重试使用请求 ID 去重审计，但管理员主动改变筛选或翻页会形成新的访问事件。

### 9.6 系统运维

运维页提供：

- API、edge、realtime、worker 的版本、实例和启动时间。
- PostgreSQL、Redis、对象存储、审计 checkpoint 和内部 realtime peer 健康。
- outbox、定时器、批量任务和导出任务的积压、最老等待时间和失败数量。
- 限流策略名称、当前配置版本和依赖可用性，不显示密钥或完整 bucket key。
- 全局维护模式状态、原因、计划结束时间和影响范围。
- 固定缓存命名空间刷新与任务重试命令。

维护模式、缓存刷新和批量重试要求 `operations.maintenance` elevation。后台不支持重启进程、修改环境变量、编辑密钥、执行 Shell 或任意删除缓存键。

## 10. 查询与命令协议

### 10.1 通用查询

列表请求统一包含：

- 结构化 filter message
- 排序字段和方向枚举
- `page_size`
- opaque `page_token`

响应包含 items、`next_page_token`、服务端采样时间和可选统计摘要。token 绑定规范化筛选与排序摘要，不能跨查询复用。列表不依赖前端推断总数；确有低成本计数时单独返回 approximate 或 exact 语义。

### 10.2 通用命令

每个写请求包含：

- `operation_id`
- 目标 ID
- `expected_version`
- 原因
- 命令专用参数

高危命令额外要求 elevation scope。响应返回稳定 operation ID、执行状态、目标新版本、影响摘要和关联审计 ID。相同 operation 与相同摘要可以安全重试；不同摘要返回幂等冲突。

### 10.3 批量任务

批量操作采用三步协议：

1. `PreviewBatchOperation` 固化筛选或显式目标集合，返回目标数、不可执行项和影响摘要。
2. `StartBatchOperation` 使用 preview ID、原因和 operation ID 创建任务。
3. `GetBatchOperation` / `ListBatchOperationItems` 展示进度和逐项结果。

preview 有短 TTL，并绑定管理员、筛选摘要和资源版本。任务以独立 item 幂等执行，部分失败不会回滚已成功的独立目标，但必须提供精确结果和安全重试入口。

### 10.4 导出与下载

用户和审计导出共用以下任务协议：

1. `CreateExportJob` 固化规范化筛选、字段集合、可见数据上界、遮蔽策略、operation ID 和结果 TTL。
2. `GetExportJob` 返回排队、运行、成功、部分成功、失败、过期或已删除状态，以及不含秘密的数量摘要。
3. `CreateExportDownloadGrant` 在任务成功后签发最长 5 分钟、单次使用、绑定管理员 session 与 export ID 的下载 grant。敏感导出同时要求仍有效的 `audit.export_sensitive` elevation。
4. 浏览器只从管理 Host 的固定下载端点携带 session 与 grant 下载，不接收公开对象 URL，也不能把 grant 写入查询参数、日志或本地存储。
5. `DeleteExportResult` 提前删除结果对象；后台清理器在最多 24 小时后自动删除加密结果。任务元数据和下载/删除审计按审计保留策略保存。

结果对象使用独立 keyring 加密并包含 export ID、管理员 ID、筛选摘要、字段集合和 schema version 作为关联数据。下载成功、grant 重放、grant 过期、自动删除和主动删除都有明确审计与测试。下载失败不会延长结果 TTL。

## 11. 数据模型

管理端重建至少需要以下持久模型：

- `admin_accounts`：单管理员身份、状态、密码版本、管理员版本和更新时间。
- `admin_totp_enrollments`：pending/active/disabled TOTP enrollment。
- `admin_recovery_codes`：启用 2FA 后生成的一次性恢复码。
- `admin_sessions`：full、setup、MFA pending 会话及撤销信息。
- `admin_elevation_grants`：会话绑定、scope、摘要、到期和消费/撤销状态。
- `admin_user_tags`、`admin_user_tag_links`：受约束的标签定义和用户关联。
- `admin_user_notes`：不可静默覆盖的备注历史。
- `admin_batch_jobs`、`admin_batch_job_items`：批量预览、任务和逐项结果。
- `admin_export_jobs`：筛选摘要、字段集合、状态、结果对象和过期时间。
- `admin_export_download_grants`：单次下载 grant 摘要、session 绑定、到期和消费状态。
- `admin_repair_operations`：dry-run、命令版本、预期版本、前后快照摘要和结果。
- `maintenance_state`：单例维护状态、原因、影响范围和版本。

继续复用签名审计链、outbox、用户、设备、房间、游戏会话和 runtime coordination 数据。查询性能通过明确索引、只读 view 或专用聚合表解决，不复制一套无法保持一致的后台业务数据库。

开发环境不编写旧管理员状态和旧 RPC 的兼容迁移。实现可以新增破坏性 migration 或重建管理相关表，并在开发文档中提供明确的重置命令。

## 12. 前端信息架构与交互

### 12.1 导航

固定导航顺序：

1. 运营概览
2. 用户中心
3. 房间与牌局
4. 审计中心
5. 安全设置
6. 系统运维

只有完成查询、操作、错误状态和权限测试的模块才能进入导航。延期模块不显示禁用菜单或“敬请期待”。

### 12.2 页面模式

- 概览使用紧凑指标带、趋势和异常/操作列表，不使用装饰性卡片墙。
- 用户、房间、牌局和审计使用“筛选栏 + 数据表 + 详情抽屉”。
- 常用命令放在表格行菜单和详情工具栏；图标按钮使用 Lucide 图标并提供 tooltip。
- 批量工具栏只有选择目标后出现，并持续显示已选数量、预览状态和任务进度。
- 安全设置使用分区表单、开关、会话表格和独立危险区域。
- 高危命令使用统一 step-up 对话框，提权成功后回到原命令确认，不要求管理员重新填写业务参数。

桌面端优先支持高密度操作；窄屏使用抽屉导航、折叠筛选和单列详情，但不能隐藏关键结果或让文本溢出。固定格式的表格操作列、计数器、状态标签和按钮使用稳定尺寸。

### 12.3 状态与刷新

列表筛选、分页和排序保存在 URL query，便于刷新和分享内部链接。详情抽屉通过资源 ID 路由状态恢复，但不把 PII、秘密或提权 grant 写入 URL。

房间与牌局页面使用有上限的轮询或现有 realtime 订阅获取变化。页面始终显示数据采样时间；断开、暂停和过期数据有明确状态。审计和重型聚合不自动高频刷新。

## 13. 错误、并发与恢复

- 所有业务错误使用稳定错误键和结构化 detail，前端不解析内部错误字符串。
- 未认证、CSRF、管理员版本变化统一清理本地会话并返回登录。
- elevation 缺失或过期时保留已填写的非敏感命令参数，完成提权后重新校验并提交。
- 并发冲突返回当前版本和可刷新提示，绝不静默覆盖。
- realtime owner 不可达时日常命令返回可识别失败，不降级为数据库强写；管理员可以进入独立紧急修正流程。
- 批量任务和导出任务可在页面刷新后恢复进度，不依赖单个浏览器请求持续在线。
- 对话框关闭、路由切换或筛选变化后，旧异步响应不得回写新状态。
- 秘密结果、密码、TOTP code、恢复码和 PII 不进入日志、指标、本地存储或错误文本。

## 14. 审计要求

以下操作必须写签名审计并 fail closed：

- 登录、失败登录、2FA 开启/关闭、恢复码使用和改密。
- 恢复码重新生成、一次性结果读取和确认接收。
- elevation 签发、拒绝、过期和主动撤销。
- PII 读取、用户标签备注变化和所有用户治理命令。
- 审计列表查询与单条详情读取；查询事件使用固定 sequence 上界避免递归进入同一结果。
- 房间/牌局控制、紧急修正 dry-run 与执行。
- 批量任务预览、启动、取消和重试。
- 审计/用户导出创建、下载、过期和删除。
- 维护模式、缓存刷新和任务重试。

高危操作审计必须记录原因、permission、elevation scope、预期/实际版本、前后快照摘要、影响数量和结果。敏感字段只记录字段名、版本或不可逆摘要。

## 15. 实现与交付边界

这是一个完整后台目标，不以部分功能作为长期 MVP。实现可以拆成连续、可审查的提交，但遵守以下规则：

1. 先替换管理契约、认证模型、权限枚举和安全设置，建立后续模块共同边界。
2. 再完成管理查询基础、用户中心、批量任务和导出。
3. 接入房间/牌局查询、日常控制和紧急修正。
4. 完成审计中心、系统运维和真实运营概览。
5. 最后统一执行浏览器流程、响应式与可访问性验证，并删除所有旧页面、旧 RPC 和死代码。

中间提交不能在正式导航暴露未闭环模块。最终交付必须同时包含六个模块，不能以静态统计、Mock 数据或无操作列表代替真实能力。

## 16. 验证策略

### 16.1 后端

- 领域单测覆盖无 2FA/有 2FA 登录、开启、关闭、改密不影响绑定、恢复码和离线重置。
- elevation 测试覆盖密码、可选 TOTP、scope、TTL、session/版本绑定和撤销。
- persistence 集成测试覆盖 enrollment 唯一约束、会话撤销、批量任务 claim、导出过期和 repair version CAS。
- Connect 契约测试覆盖分页 token、命令幂等、错误 detail、权限和 elevation。
- 房间/牌局集成测试覆盖 owner 成功、owner 不可达、并发变化和紧急修正 dry-run/execute 一致性。
- 审计测试证明每个敏感查询和写命令的成功、拒绝与失败路径都有正确事件，且写审计失败时业务 fail closed。

### 16.2 前端

- API adapter 测试覆盖消息编解码、CSRF、Cookie、请求 ID、幂等键和错误映射。
- auth store 测试覆盖首次改密、默认关闭 2FA、启停 2FA、恢复码、刷新恢复、改密和 elevation 生命周期。
- query store 测试覆盖 URL 筛选同步、竞态取消、分页和资源刷新。
- 组件测试覆盖筛选、详情抽屉、命令表单、批量预览、任务进度、安全设置和危险操作。
- 路由与权限测试覆盖缺少会话、缺少权限、缺少 elevation 和直接 URL 访问。

### 16.3 浏览器

使用真实本地后端和数据库验证：

- 首次改密后直接进入后台，2FA 默认关闭。
- 开启 2FA、重新登录验证 TOTP、使用恢复码、关闭 2FA和再次仅密码登录。
- 修改密码撤销其他会话但保留 2FA 状态。
- 用户筛选、详情、标签备注、单项治理、批量预览/执行和导出。
- 房间/牌局查询、日常控制、提权、紧急 dry-run 和执行。
- 审计筛选、详情差异和导出。
- 审计列表读取事件不进入同一次分页，导出下载 grant 单次使用且过期后拒绝。
- 运维健康、维护模式、缓存刷新和任务重试。
- 桌面与移动视口无重叠、溢出或不可达操作，键盘可以完成核心流程。

## 17. 完成标准

1. 管理后台六个导航模块全部连接真实后端，不含 Mock、静态指标、空操作或占位菜单。
2. 2FA 默认关闭，管理员可以在后台完整开启和关闭；登录行为与账户绑定状态一致。
3. 管理员可以修改密码，其他会话被撤销，TOTP 绑定状态不被意外改变。
4. 用户中心支持完整列表、筛选、详情、标签备注、设备会话、治理、批量任务和导出。
5. 房间与牌局支持实时查询、日常控制和带短时提权、预演、版本保护、快照审计的紧急修正。
6. 审计中心能够解释所有后台敏感操作，系统运维能够展示真实状态并执行受控维护。
7. 当前单管理员拥有全部权限，但任一业务接口都不依赖“永远只有一个管理员”的硬编码授权捷径。
8. 旧后台页面、旧管理端 RPC、旧认证分支和死代码被删除，不保留兼容层。
9. 定向测试、全仓检查、生产构建和关键 Playwright 流程全部通过。

## 18. 风险与缓解

- **范围较大：** 使用明确服务边界和连续提交，模块未闭环前不进入导航；完整交付标准不缩减。
- **高危控制破坏游戏一致性：** 日常命令必须走领域状态机；紧急修正只能使用固定命令、dry-run、版本 CAS 和快照审计。
- **查询拖慢业务库：** 管理查询使用稳定索引、只读 view、聚合表和有限轮询，禁止页面直接扫描热表。
- **未来 RBAC 返工：** 从第一条 RPC 开始声明权限并使用统一 actor context，不在服务内硬编码 super admin 绕过。
- **秘密与 PII 泄漏：** 敏感值只在短时内存和受控 envelope 中存在，日志、指标、URL 和本地存储全部禁止。
- **开发期破坏性重建影响并行工作：** 只重建管理端契约和管理专属数据；执行数据库重置前明确记录命令和影响，不改写无关游戏客户端工作。
