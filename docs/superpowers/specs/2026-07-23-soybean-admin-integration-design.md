# SoybeanAdmin 管理前端集成设计

> 日期：2026-07-23
>
> 状态：已确认，待实现
>
> 上游参考：`soybeanjs/soybean-admin` `main@3d3613f20cd4add3cd20fd6cc884abead165c6d2`

## 1. 背景

仓库已经实现独立的管理员认证、用户治理和审计后端，也在 workspace、部署域名和应用目录约定中预留了 `apps/admin`，但当前没有管理端浏览器应用。管理域名只能访问管理员 ConnectRPC 路径，不能加载独立静态页面。

本设计将 SoybeanAdmin 默认 Naive UI 版本的可见应用壳层选择性集成到仓库中，同时保留现有 ConnectRPC、Cookie、CSRF、管理员状态机、单一 `super_admin` 权限模型和管理域名隔离。模板只提供经过适配的布局与视觉基础，不成为新的认证、请求或业务架构。

## 2. 目标

- 新增可独立开发、测试、构建和部署的 `@game-night/admin` 应用。
- 保留 SoybeanAdmin 的侧栏、顶栏、标签页、主题和登录页体验，并使用 Naive UI 实现后台控件。
- 接通真实的管理员初始设置、密码登录、TOTP、恢复、会话恢复和退出流程。
- 提供已有后端能力能够诚实支撑的用户治理与审计页面。
- 在开发和生产中保持用户端与管理端的 Host、Origin、Cookie、CSRF 和静态资源隔离。
- 将新增依赖控制在管理端实际使用范围内，并继续由根 pnpm catalog 固定版本。

## 3. 非目标

- 不原样搬入 SoybeanAdmin monorepo、根配置、锁文件、Mock 服务或发布脚本。
- 不采用 SoybeanAdmin 的 Axios、Token 存储、动态菜单后端或通用 RBAC 模型。
- 不把管理页面放入 `apps/web`，也不共享用户端认证状态。
- 不为尚无管理端契约的房间、对局、举报、游戏版本、主题、复盘或系统运维创建假页面。
- 不实现用户目录或设备目录；现有后端只支持精确用户查询和按已知设备 ID 操作。
- 首期不开放删除用户、资料导出、辅助恢复、恢复码重置等需要更完整秘密结果或不可逆确认体验的操作。相关后端 RPC 已经存在，但用户已明确批准先收缩首期治理范围；这里是交互安全与交付顺序决策，不表示后端不支持这些能力。

## 4. 方案选择

### 4.1 未采用：完整搬入上游

完整搬入能够最快获得全部演示功能，但会同时引入 `@sa/*` workspace、Axios、Mock、Elegant Router、UnoCSS 预设、上游状态模型和大量示例页面。它会扩大依赖与升级面，并与本仓库的 Connect JSON、静态路由、严格 catalog 和安全边界重复或冲突。

### 4.2 已采用：选择性集成

选择性集成保留上游的布局组成、交互节奏和主题表达，在 `apps/admin` 内改写为仓库原生模块。Naive UI 作为控件基础；路由、状态、请求、认证和部署使用本仓库约定。被适配的上游代码保留 MIT 来源与许可证说明。

### 4.3 未采用：仅参考视觉重新开发

完全重新开发的依赖最少，但不能复用已经选定的模板结构，且首期壳层工作量更高。选择性集成在模板一致性与长期维护之间更平衡。

## 5. 应用边界

### 5.1 目录与构建

新增 `apps/admin`，它是独立的 Vite SPA 和应用组合入口：

- 包名为 `@game-night/admin`。
- 使用根 catalog 中的 Vue、Vite、TypeScript、Pinia、Vue Router、Vitest 和 Lucide 版本。
- 新增 Naive UI 与确有使用的样式依赖时，也必须在根 catalog 中固定精确版本。
- 暴露 `dev`、`build`、`check` 和 `test` 脚本，使 Turbo 自动纳入验证图。
- 开发服务器默认绑定 `127.0.0.1:4174`，通过同源代理访问管理员 RPC。
- TypeScript 配置继承根严格配置，不复制上游 tsconfig 或格式化工具链。

应用内部按责任拆分：

- `api/`：Connect JSON 传输、错误解析、Cookie/CSRF 和管理员服务适配。
- `stores/`：只保存内存会话状态与可持久化的界面偏好。
- `router/`：静态路由、权限元数据和会话守卫。
- `layouts/`：经适配的 SoybeanAdmin 管理壳层。
- `views/auth/`：管理员状态机页面。
- `views/dashboard/`、`views/users/`、`views/audit/`：首期业务页面。
- `components/`：只放管理端实际复用的控件，不提前建立跨应用 UI 包。

### 5.2 上游代码边界

选择性移植以下可见能力：

- 固定纵向侧栏、顶部工具栏、面包屑、可关闭标签页和响应式抽屉。
- 亮色、暗色和跟随系统主题。
- 登录页结构、状态步骤呈现和后台紧凑信息层级。
- 菜单折叠、页面标题、错误页和加载状态。

不移植以下上游基础设施：

- `@sa/axios`、`@sa/alova`、Mock 和演示 API。
- 通用动态路由服务、角色管理和后端菜单协议。
- 上游本地存储认证、Token 刷新和用户模型。
- 未使用的布局模式、国际化、图表、富文本、导出和示例组件。
- 上游 lockfile、workspace 定义、提交钩子和发布脚本。

仓库增加 SoybeanAdmin MIT 许可证副本或第三方声明，记录固定参考提交。经过实质改写的代码仍保留来源说明，便于后续安全升级与差异审查。

## 6. 视觉与交互

管理端是高频操作工具，不采用营销式首页或装饰性卡片堆叠：

- 桌面端使用稳定侧栏、紧凑顶栏和可扫描的内容区；窄屏改为抽屉导航。
- 用户和审计页面以筛选、数据表、详情区和明确操作为核心。
- 颜色以中性背景、清晰文字、绿色成功、琥珀警告和红色危险为主，避免单一色相主导。
- 图标统一使用仓库已有的 Lucide Vue 图标，不复制 SVG 图标集。
- 数字、筛选栏、分页器、操作列和弹窗使用稳定尺寸，动态内容不得造成布局跳动。
- 删除、封禁、退出全部会话等危险动作必须使用明确确认，不依赖仅有颜色的提示。
- 所有表单提供标签、错误关联和键盘路径；状态变化使用文字与颜色共同表达。

只有主题、侧栏折叠和标签页等界面偏好可以写入本地存储。管理员会话、权限、TOTP 秘密、恢复码、辅助恢复 grant、PII 和审计结果不得持久化到浏览器存储。

## 7. 管理员认证

### 7.1 浏览器请求约束

管理端沿用现有 Connect JSON 协议：

- 默认使用相对 URL，同源访问管理域名；仅开发代理可以指向本地 API。
- 所有请求使用 `credentials: "include"`。
- Connect 请求发送 `Content-Type: application/json` 和 `Connect-Protocol-Version: 1`。
- 登录 challenge 使用同一个 `request_flow_id` 和 `X-Request-Flow-ID`。
- 会话写请求每次从 `__Host-gn_admin_csrf` Cookie 读取当前值并发送 `X-CSRF-Token`。
- 所有 `AdminIdentityService` 请求生成唯一 `X-Request-ID`。
- 管理端响应按敏感数据处理，不进入客户端缓存。

TypeScript 已生成消息与服务描述符，但没有生成 Connect 客户端。`apps/admin` 增加小型通用 unary transport，使用现有 protobuf-es schema 完成 JSON 编解码和稳定 Connect 错误解析，不引入上游 Axios 层。

### 7.2 会话恢复契约

当前后端缺少会话自省接口，页面刷新后无法验证 HttpOnly Cookie 对应的实时会话、权限和过期时间。实现前在 `AdminAuthService` 新增：

```proto
rpc GetCurrentAdminSession(GetCurrentAdminSessionRequest)
    returns (GetCurrentAdminSessionResponse);
```

`GetCurrentAdminSessionRequest` 是空消息，响应包含当前 `AdminSessionSummary` 和对应 `AdminNextStep`。该 RPC：

- 属于管理员会话操作，要求管理 Origin、管理员会话 Cookie 和 CSRF。
- 不属于 `AdminIdentityService`，不要求 `X-Request-ID`，也不产生用户治理审计事件。
- 不接受前端传入的管理员 ID、权限或会话类型。
- 从服务端当前会话重新读取状态，拒绝过期、撤销或不匹配的会话。
- 支持完整会话以及设置、MFA、恢复中的受限会话，以便刷新后恢复正确步骤。
- 继续使用 `no-store` 响应策略。

### 7.3 状态机

首次加载先调用 `GetCurrentAdminSession`。成功时按 `next_step` 进入完整后台或受限认证步骤；未认证时再调用 `GetSetupState` 并显示登录入口或部署初始化状态。

登录流程：

1. 生成 `request_flow_id` 并调用 `BeginAdminLogin`。
2. 用户输入密码，客户端提交 challenge proof、密码和相同 flow ID。
3. `LoginPassword` 返回 `CHANGE_PASSWORD` 时进入初始密码修改。
4. 返回 `ENROLL_TOTP` 时开始 TOTP 注册，完成首个验证码后一次性展示恢复码。
5. 返回 `VERIFY_MFA` 时输入 TOTP；也可以从该步骤使用恢复码进入 `REBIND_TOTP`。
6. 返回 `AUTHENTICATED` 时重新读取当前会话并进入后台。

TOTP 种子、恢复码和秘密结果只存在于当前组件内存。关闭、刷新或确认接收后立即清理。所有认证步骤的异步响应带请求代次保护，过期响应不得覆盖新步骤。

退出当前会话和退出全部会话复用统一清理流程：服务端撤销成功后清空 Pinia 内存状态、关闭敏感视图并跳转登录页。收到未认证、会话撤销或 CSRF 失败时执行相同本地清理，但保留非敏感主题偏好。

## 8. 路由与权限

首期使用静态路由，因为后端只有一个 `super_admin`，没有菜单管理或自定义角色契约。

每个受保护路由声明所需 `AdminPermission`。路由守卫只信任 `GetCurrentAdminSession` 返回的权限：

- 没有会话时进入认证流程。
- 受限设置、MFA 或恢复会话只能访问对应认证步骤。
- 完整会话有权限时进入页面。
- 缺少权限时显示 403，不渲染目标页面或危险操作。

菜单可见性与路由守卫使用同一权限声明，避免只隐藏菜单却仍可直接访问。前端权限只改善交互，服务端继续承担最终授权。

## 9. 首期页面

### 9.1 概览

- 展示当前会话类型、权限、空闲过期时间和绝对过期时间。
- 展示普通与敏感写入 readiness，不把敏感内部错误直接呈现给管理员。
- 展示当前已经开放的后台模块，不展示尚未实现的规划模块。

### 9.2 用户管理

现有后端没有用户列表或模糊搜索，因此页面采用精确查询工作台：

- 按规范化用户名或 UUID 查询一个用户。
- 展示用户状态和后端已经返回的非敏感资料。
- 按需读取真实姓名；必须明确触发，不能随用户详情自动加载。
- 修改真实姓名时收集审计原因并执行表单验证。
- 封禁和解封时显示目标用户、影响和原因确认。
- 不展示无后端来源的设备列表；已知设备 ID 的撤销入口首期不开放。

资料导出、辅助恢复、强制改名、删除用户和已知设备撤销虽然已有 RPC，但不进入本次用户已批准的首期界面。后续开放这些能力时，必须分别补齐不可逆确认、秘密结果接收、重试幂等和操作后状态刷新设计。

### 9.3 审计记录

- 通过 `AdminIdentityService.ListAuditEvents` 查询签名审计事件。
- 支持管理员 ID、用户 ID、动作和时间范围筛选。
- 使用后端分页 token，不推断总数。
- 表格显示时间、动作、主体、目标、请求关联和验证状态；详情按需展开。
- 审计读取本身会产生审计记录，界面避免无意义自动轮询。

### 9.4 安全设置

- 展示当前会话到期信息。
- 支持退出当前会话与退出全部会话。
- 恢复码重新生成、密码修改、TOTP 重绑和辅助恢复留待秘密结果通用交互完善后开放。

## 10. 错误与并发处理

客户端将稳定业务错误键映射为中文操作提示，并保留未知错误的通用失败文案。至少覆盖：

- `admin.auth.invalid`
- `admin.mfa.invalid`
- `admin.permission.denied`
- `request.origin.not_allowed`
- `request.csrf.invalid`
- `request.rate_limited`
- `operation.idempotency_conflict`
- `operation.concurrent_transition`
- `audit.write.unavailable`
- `service.temporarily_unavailable`

请求层保留 Connect code、业务错误键和可选重试时间，页面不解析服务端内部错误字符串。未认证和 CSRF 错误触发统一会话失效流程；限流显示可重试时间；并发冲突要求重新读取当前资源。

搜索、筛选、对话框和认证步骤使用 `AbortController` 或请求代次。组件关闭、路由切换、条件变化或新请求开始时，旧请求不能回写状态。提交按钮在单次操作期间保持稳定尺寸并显示加载状态，阻止重复提交。

## 11. 静态资源与域名隔离

### 11.1 开发

- 根脚本增加 `dev:admin`。
- Vite 管理端开发服务器绑定独立端口，并只代理管理员 RPC 和 readiness 路径。
- API 管理 Origin allowlist 必须包含开发管理端的精确 Origin。
- 浏览器请求仍使用相对路径，以模拟生产同源边界。

### 11.2 单镜像

- Docker 安装层纳入 `apps/admin/package.json` 和管理端新增依赖。
- 构建层执行管理端 build。
- 运行镜像分别保存 `/app/web` 和 `/app/admin` 静态产物。
- Edge 将原有单静态目录配置拆分为 `GAME_NIGHT_EDGE_USER_STATIC_DIRECTORY` 和 `GAME_NIGHT_EDGE_ADMIN_STATIC_DIRECTORY`，默认分别为 `/app/web` 和 `/app/admin`；旧的 `GAME_NIGHT_EDGE_STATIC_DIRECTORY` 不再作为含糊的双站点来源。
- Edge 增加 `GAME_NIGHT_EDGE_USER_HOSTS` 与 `GAME_NIGHT_EDGE_ADMIN_HOSTS` 精确 allowlist。两组 authority 必须完成规范化、互不重叠且不得为空；配置非法时进程拒绝启动。
- Edge 在任何代理或静态 fallback 前校验 `request.Host`。用户 Host 只允许用户 RPC、realtime 和用户 SPA；管理 Host 只允许管理员 RPC、readiness 和管理 SPA。
- 未知 Host 返回 `421 Misdirected Request`，不得回退到任一 SPA；已知 Host 上的跨域 RPC 路径返回 `404`。
- API、realtime 和静态 fallback 的匹配顺序保持明确，管理 Host 不得回退到用户 SPA。

### 11.3 外部 Nginx

- 管理域名继续只代理管理员 RPC，不开放用户 RPC 或 realtime。
- 管理域名的普通 GET/HEAD 转发到能够按 Host 返回管理 SPA 的 Edge。
- 用户域名继续返回用户 SPA，两个域名不得共享管理 Cookie 或 SPA fallback。
- Nginx 必须保留原始 Host；Edge 的 Host allowlist 是单镜像直连和代理误配置时的第二道边界，不能由 `X-Forwarded-Host` 覆盖。

## 12. 验证策略

### 12.1 后端

- Proto/生成物一致性检查覆盖新增 RPC。
- `GetCurrentAdminSession` 单元测试覆盖完整、设置、MFA、恢复、过期和撤销会话。
- 传输测试覆盖 Origin、Cookie、CSRF 和用户/管理服务路径隔离。
- 应用集成测试覆盖登录后自省、刷新恢复和退出后拒绝自省。
- Edge/Nginx 测试覆盖用户 Host、管理 Host、未知 Host、API 路径和双 SPA fallback。

### 12.2 前端

- Connect transport 测试覆盖 JSON 编解码、Cookie 凭证、flow ID、request ID、CSRF 和错误详情。
- Auth store 测试覆盖所有 `AdminNextStep`、刷新恢复、过期响应保护和统一清理。
- Router 测试覆盖未登录、受限会话、完整会话、缺少权限和直接 URL 访问。
- 组件测试覆盖初始密码、TOTP、恢复、用户精确查询、真实姓名修改、封禁确认和审计筛选。
- 构建验证使用根严格 TypeScript、Turbo 和生成物检查。

### 12.3 浏览器

Playwright 使用真实本地后端验证：

- 初始设置和普通 MFA 登录。
- 页面刷新后恢复当前步骤或完整会话。
- 用户查询、真实姓名按需读取/修改、封禁与解封。
- 审计筛选和分页。
- 退出当前会话与退出全部会话。
- 用户域名不能加载管理页面，管理域名不能调用用户服务。
- 桌面和窄屏无重叠、溢出或不可达操作，键盘可完成核心流程。

## 13. 风险与缓解

- **模板漂移：** 固定参考提交并记录来源；只在明确审查后同步上游安全或可访问性修复。
- **隐藏依赖：** 不复制完整 `@sa/*`；每个新增依赖都必须有实际导入和测试覆盖。
- **认证状态复杂：** 以后端 `next_step` 为唯一状态来源，用会话自省恢复刷新后的步骤。
- **秘密泄漏：** 秘密结果不持久化、不记录日志、不进入路由参数，关闭后立即清理。
- **权限假象：** 菜单、路由和按钮共用权限声明，服务端继续最终授权。
- **管理能力不完整：** 只展示有完整查询和操作契约的能力，后续模块逐项补设计与接口。
- **静态路由误吞 API：** Edge 和 Nginx 对 API、Host 与 SPA fallback 建立独立测试矩阵。
- **工作区并行修改：** 实现时限定写入 `apps/admin`、管理契约和部署文件，不改写当前无关的游戏与 Web 工作。

## 14. 完成标准

- 管理员可以从独立管理 Origin 完成初始设置、MFA 登录、恢复、刷新和退出。
- 页面刷新后由服务端重新验证会话，不依赖浏览器 Token 或伪造权限。
- 用户精确查询、真实姓名维护、封禁/解封和审计查询连接真实后端。
- 未实现能力没有可点击假入口。
- 用户与管理 Host、Origin、Cookie、RPC 和 SPA 静态目录保持隔离。
- 前端类型检查、单元测试、构建、后端测试、Edge/Nginx 测试和关键 Playwright 流程全部通过。
- SoybeanAdmin MIT 来源与固定参考提交可追溯。
