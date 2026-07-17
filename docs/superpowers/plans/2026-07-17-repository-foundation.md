# 仓库与工具链基础实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立可复现、可验证、可持续扩展的 Go + pnpm + Buf monorepo 基础，并在任何业务代码进入仓库前启用依赖边界与 CI 门禁。

**Architecture:** 根 Go module 承载后续服务端入口、平台模块、服务端 SDK 和游戏引擎；pnpm workspace + Turborepo 编排 Web、管理后台、客户端 SDK、共享包和游戏客户端；Buf workspace 管理跨语言协议。首个可运行产物是仓库边界检查器及其测试，不创建空业务包或假协议。

**Tech Stack:** Go 1.26.4、Node.js 24.18.0、pnpm 11.13.1、Turborepo、Protocol Buffers + Buf、GitHub Actions。

---

## 1. 计划定位

本计划对应[总体设计规范](../specs/2026-07-17-game-night-platform-design.md)第 14、15、19 和 21 节中的“工具链与仓库”阶段。阶段仅表示依赖顺序，不是 MVP 范围削减。

后续正式范围按以下连续子计划交付：

1. 仓库与工具链基础（本计划）
2. 设备身份、用户资料与管理后台认证
3. 持续房间、公开大厅、文字互动与治理
4. WebSocket 网关、权威会话运行时、幂等与恢复
5. Game SDK、客户端平台端口、主题系统与契约测试套件
6. 吹牛骰子、789、喜相逢的独立规则规范与实现
7. 三关定胜负的独立规则规范与实现
8. 德州扑克的独立规则规范与实现
9. 观战、复盘、归档与后台运营
10. 压测、安全、故障演练与生产发布

本计划不实现身份、房间、游戏、数据库、Redis、对象存储、Web 页面或部署容器，也不从 `.tmp/` 或 `E:/WorkPro/Project_20260305/nuxt-games` 复制代码、资产或架构。

规范追踪：

| 本计划约束 | 规范来源 |
| --- | --- |
| 参考目录只用于理解玩法，不复用代码、架构或资产 | [设计规范第 11 行](../specs/2026-07-17-game-night-platform-design.md#1-背景与边界) |
| 阶段顺序不是 MVP 删减 | [设计规范第 17 行](../specs/2026-07-17-game-night-platform-design.md#1-背景与边界) |
| 根 Go module、pnpm workspace、Buf workspace 与目录结构 | [设计规范第 487 行](../specs/2026-07-17-game-night-platform-design.md#14-代码仓库) |
| 依赖方向必须由静态检查和 CI 强制 | [设计规范第 546 行](../specs/2026-07-17-game-night-platform-design.md#141-依赖方向) |
| Vue/TypeScript 与 Go 技术基线 | [设计规范第 566 行](../specs/2026-07-17-game-night-platform-design.md#15-技术栈) |
| Go、前端与 SDK 测试门禁 | [设计规范第 690 行](../specs/2026-07-17-game-night-platform-design.md#19-测试策略) |
| 首个依赖阶段是工具链、仓库、代码生成、边界与 CI | [设计规范第 755 行](../specs/2026-07-17-game-night-platform-design.md#21-实施阶段) |

## 2. 环境事实与前置条件

- 仓库远端为 `git@github.com:iFTY-R/game-night.git`，Go module 使用规范路径 `github.com/iFTY-R/game-night`。
- 当前已安装 Go `1.26.4`、Node.js `24.18.0`、Corepack `0.35.0`、pnpm `11.13.1`。
- 当前未安装 Buf 与 Docker CLI。Docker 不属于本计划的本地验收前置。
- 执行前若 `buf --version` 失败，必须先取得用户对 Scoop 安装的明确许可，再运行 `scoop install buf`；不得自动安装。
- 当前外部 Go/npm 代理不可达。执行 `pnpm add` 或 Buf 远程插件前必须确认网络恢复；不得手写 `pnpm-lock.yaml` 或绕过 frozen lockfile。
- 默认分支是 `master`，CI 和 Buf breaking check 不得写成 `main`。
- 本计划文件必须在执行开始前作为独立文档提交；执行者从干净工作树开始，不把计划文档混入任何实现提交。

## 3. 验收标准

- `go version` 包含 `go1.26.4`，`node --version` 等于 `v24.18.0`，`pnpm --version` 等于 `11.13.1`。
- 根目录存在 `go.mod`、`package.json`、`pnpm-lock.yaml`、`pnpm-workspace.yaml`、`turbo.json`、`buf.yaml` 和 `buf.gen.yaml`。
- `pnpm install --frozen-lockfile` 成功且不修改工作树。
- `go test ./...` 和 `go vet ./...` 成功；边界测试至少证明 17 条禁止依赖会失败、5 条允许依赖会通过。
- `go run ./tooling/cmd/boundarycheck` 对真实仓库返回成功；包含 `platform -> games`、`games -> apps` 和引擎导入 `os` 的集成夹具让完整 CLI 返回非零退出码。
- Buf smoke proto 实际调用 Go、ConnectRPC 和 TypeScript 三个固定版本插件并生成已跟踪产物；兼容基线通过、破坏性夹具必须失败。
- 生产 proto 尚不存在时 breaking check 明确报告 `SKIPPED: no production Proto baseline`，不得记为通过；首个生产 proto 合并后，PR 必须对 base ref 执行 breaking check。
- CI 对 `contracts/**/*.proto` 和 `games/*/proto/**/*.proto` 逐文件执行 `buf lint --path`；任何未加入 `buf.yaml.modules` 的生产 proto 都必须失败。
- Buf 生成后 `git diff --exit-code` 与 `git status --porcelain` 均为空，既不遗漏已跟踪漂移，也不遗漏新文件。
- `pnpm exec turbo run check test build --dry=json` 成功，并精确包含 `@game-night/workspace-smoke` 的 check、test、build 三个非空任务。
- GitHub Actions 使用完整 Git 历史执行仓库、Go、pnpm、Buf 和 breaking 门禁。
- `git diff --check` 成功，`git status --short` 为空。
- 仓库只出现本计划文件图中的基础文件，不出现业务状态、业务 API、游戏规则、数据库表或参考项目资产。

## 4. 文件责任图

| 路径 | 责任 |
| --- | --- |
| `.go-version`、`.node-version`、`.npmrc` | 固定本地工具链与 pnpm 行为 |
| `.editorconfig`、`.gitattributes` | 固定跨平台文本格式 |
| `go.mod` | 根 Go module 与 Go 版本契约 |
| `package.json`、`pnpm-lock.yaml` | 根脚本、Turborepo 版本和依赖锁 |
| `pnpm-workspace.yaml` | TypeScript/Vue workspace 边界 |
| `turbo.json`、`tsconfig.base.json` | 跨包任务图和 TypeScript 基线 |
| `buf.yaml`、`buf.gen.yaml` | Proto lint、breaking 与跨语言生成基线 |
| `apps/README.md` | 应用入口责任与禁止承载业务规则 |
| `platform/README.md` | 平台模块边界 |
| `sdk/README.md` | Go/TypeScript SDK 边界 |
| `packages/README.md` | 前端共享包边界 |
| `games/README.md` | 游戏自包含目录与依赖方向 |
| `contracts/README.md` | 平台和游戏协议所有权、生成目录 |
| `tooling/README.md` | 代码生成、边界检查和仓库工具责任 |
| `tooling/boundarycheck/policy.go` | 纯依赖边界规则 |
| `tooling/boundarycheck/policy_test.go` | 允许/禁止边界的回归测试 |
| `tooling/boundarycheck/discover.go` | 从 Go 列表和 workspace manifest 收集结构化依赖边 |
| `tooling/boundarycheck/discover_test.go` | Go JSON 流与 package manifest 发现测试 |
| `tooling/cmd/boundarycheck/main.go` | CI/本地命令入口 |
| `tooling/cmd/boundarycheck/main_test.go` | 完整 CLI 非零退出的集成测试 |
| `tooling/testdata/boundaries/forbidden/**` | 同时包含包边界和引擎 IO 违规的隔离 module |
| `tooling/testdata/proto*/**` | 非生产 Proto 生成、兼容与破坏性 smoke 夹具 |
| `tooling/workspace-smoke/**` | 证明 pnpm/Turbo check、test、build 非空执行的非生产包 |
| `.github/workflows/ci.yml` | 基础持续集成门禁 |
| `docs/adr/0001-monorepo-toolchain.md` | 仓库与工具链决策记录 |
| `docs/operations/development.md` | Windows/Linux 开发与验证说明 |
| `README.md` | 项目入口、当前阶段和文档导航 |

## 5. 逐步实施

### Task 0: 验证执行交接基线

**Files:**
- No file changes expected.

- [ ] **Step 1: 确认计划已独立提交**

```powershell
git ls-files --error-unmatch docs/superpowers/plans/2026-07-17-repository-foundation.md
if ($LASTEXITCODE -ne 0) { throw 'implementation plan is not committed' }
git log -1 --format='%h %s' -- docs/superpowers/plans/2026-07-17-repository-foundation.md
if ($LASTEXITCODE -ne 0) { throw 'implementation plan has no commit history' }
```

Expected: 输出计划路径以及独立的 `docs(plan): ...` 提交。

- [ ] **Step 2: 确认从干净工作树开始**

```powershell
if (git status --porcelain) { throw 'worktree must be clean before execution' }
```

Expected: 不抛错。若存在任何已暂存、未暂存或未跟踪内容，先停止并确认所有权，不能混入后续实现提交。

### Task 1: 固定工具链与文本格式

**Files:**
- Create: `.go-version`
- Create: `.node-version`
- Create: `.npmrc`
- Create: `.editorconfig`
- Create: `.gitattributes`
- Modify: `.gitignore`

- [ ] **Step 1: 创建版本文件**

`.go-version`：

```text
1.26.4
```

`.node-version`：

```text
24.18.0
```

`.npmrc`：

```ini
engine-strict=true
save-exact=true
shared-workspace-lockfile=true
strict-peer-dependencies=true
```

- [ ] **Step 2: 创建跨平台文本规则**

`.editorconfig`：

```ini
root = true

[*]
charset = utf-8
end_of_line = lf
insert_final_newline = true
indent_style = space
indent_size = 2
trim_trailing_whitespace = true

[*.go]
indent_style = tab

[Makefile]
indent_style = tab

[*.md]
trim_trailing_whitespace = false
```

`.gitattributes`：

```gitattributes
* text=auto eol=lf
*.bat text eol=crlf
*.cmd text eol=crlf
*.ps1 text eol=crlf
*.png binary
*.jpg binary
*.jpeg binary
*.webp binary
*.woff2 binary
```

- [ ] **Step 3: 忽略基础工具输出**

在 `.gitignore` 的构建输出区域加入：

```gitignore
.turbo/
.pnpm-store/
```

- [ ] **Step 4: 验证本机版本与格式**

Run:

```powershell
go version
node --version
pnpm --version
git diff --check
```

Expected: 依次包含 `go1.26.4`、`v24.18.0`、`11.13.1`，`git diff --check` 无输出。

- [ ] **Step 5: 提交工具链基线**

```powershell
git add .go-version .node-version .npmrc .editorconfig .gitattributes .gitignore
git status --short
git diff --staged --name-only
git diff --staged --check
git diff --staged
git commit -m "build(repo): 固定基础工具链版本"
```

### Task 2: 初始化根 Go、pnpm 与 Turborepo 工作区

**Files:**
- Create: `go.mod`
- Create: `package.json`
- Create: `pnpm-lock.yaml` (generated)
- Create: `pnpm-workspace.yaml`
- Create: `turbo.json`
- Create: `tsconfig.base.json`

- [ ] **Step 1: 创建根 Go module**

`go.mod`：

```go
module github.com/iFTY-R/game-night

go 1.26.4
```

- [ ] **Step 2: 创建 pnpm 根清单与 workspace**

`package.json`：

```json
{
  "name": "@game-night/root",
  "private": true,
  "packageManager": "pnpm@11.13.1",
  "engines": {
    "node": "24.18.0",
    "pnpm": "11.13.1"
  },
  "scripts": {
    "build": "turbo run build",
    "check": "turbo run check",
    "test": "turbo run test"
  }
}
```

`pnpm-workspace.yaml`：

```yaml
packages:
  - apps/web
  - apps/admin
  - sdk/ts/*
  - packages/*
  - games/*/client
  - games/*/themes
```

- [ ] **Step 3: 创建任务图和 TypeScript 基线**

`turbo.json`：

```json
{
  "$schema": "https://turbo.build/schema.json",
  "tasks": {
    "build": {
      "dependsOn": ["^build"],
      "outputs": ["dist/**"]
    },
    "check": {
      "dependsOn": ["^check"],
      "outputs": []
    },
    "test": {
      "dependsOn": ["^build"],
      "outputs": ["coverage/**"]
    },
    "generate": {
      "cache": false
    }
  }
}
```

`tsconfig.base.json`：

```json
{
  "compilerOptions": {
    "target": "ES2023",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "exactOptionalPropertyTypes": true,
    "useDefineForClassFields": true,
    "verbatimModuleSyntax": true,
    "resolveJsonModule": true,
    "skipLibCheck": false,
    "forceConsistentCasingInFileNames": true
  }
}
```

- [ ] **Step 4: 安装并锁定仓库本地 Turborepo**

网络恢复后运行：

```powershell
corepack enable
pnpm add --save-dev --workspace-root --save-exact turbo
pnpm install
```

Expected: `package.json` 新增精确版本的 `devDependencies.turbo`，生成 `pnpm-lock.yaml`，无 `package-lock.json` 或 `yarn.lock`。

- [ ] **Step 5: 验证配置可解析**

```powershell
go mod edit -json
pnpm install --frozen-lockfile
pnpm exec turbo run check --dry=json
```

Expected: 三条命令退出码均为 0；Turbo 输出合法 JSON，即使当前还没有 workspace package。

- [ ] **Step 6: 提交根工作区**

```powershell
git add go.mod package.json pnpm-lock.yaml pnpm-workspace.yaml turbo.json tsconfig.base.json
git status --short
git diff --staged --name-only
git diff --staged --check
git diff --staged
git commit -m "build(repo): 初始化 monorepo 工作区"
```

### Task 3: 以非生产 workspace 包证明 Turbo 任务非空

**Files:**
- Modify: `pnpm-workspace.yaml`
- Modify: `pnpm-lock.yaml` (generated)
- Create: `tooling/workspace-smoke/package.json`
- Create: `tooling/workspace-smoke/src/add.test.mjs`
- Create: `tooling/workspace-smoke/src/add.mjs`
- Create: `tooling/workspace-smoke/build.mjs`

- [ ] **Step 1: 注册 smoke workspace 并先写失败测试**

在 `pnpm-workspace.yaml` 的 `packages` 末尾加入：

```yaml
  - tooling/workspace-smoke
```

`tooling/workspace-smoke/package.json`：

```json
{
  "name": "@game-night/workspace-smoke",
  "private": true,
  "type": "module",
  "scripts": {
    "build": "node build.mjs",
    "check": "node --check src/add.mjs",
    "test": "node --test src/add.test.mjs"
  }
}
```

`tooling/workspace-smoke/src/add.test.mjs`：

```javascript
import assert from 'node:assert/strict'
import test from 'node:test'

import { add } from './add.mjs'

test('add returns the sum of two numbers', () => {
  assert.equal(add(2, 3), 5)
})
```

- [ ] **Step 2: 更新 lockfile 并确认测试先失败**

```powershell
pnpm install
if ($LASTEXITCODE -ne 0) { throw 'workspace install failed' }
pnpm --filter @game-night/workspace-smoke test
if ($LASTEXITCODE -eq 0) { throw 'workspace smoke test unexpectedly passed before implementation' }
Write-Output "expected missing implementation failure: exit $LASTEXITCODE"
```

Expected: 测试因 `src/add.mjs` 不存在而失败，lockfile 已记录 smoke workspace。

- [ ] **Step 3: 实现最小代码和真实构建任务**

`tooling/workspace-smoke/src/add.mjs`：

```javascript
// This deterministic function keeps repository task smoke tests independent from product behavior.
export function add(left, right) {
  return left + right
}
```

`tooling/workspace-smoke/build.mjs`：

```javascript
import { copyFile, mkdir } from 'node:fs/promises'

await mkdir(new URL('./dist/', import.meta.url), { recursive: true })
await copyFile(new URL('./src/add.mjs', import.meta.url), new URL('./dist/index.mjs', import.meta.url))
```

- [ ] **Step 4: 证明 check、test、build 和 Turbo 图都有真实任务**

```powershell
pnpm --filter @game-night/workspace-smoke check
if ($LASTEXITCODE -ne 0) { throw 'workspace smoke check failed' }
pnpm --filter @game-night/workspace-smoke test
if ($LASTEXITCODE -ne 0) { throw 'workspace smoke test failed' }
pnpm --filter @game-night/workspace-smoke build
if ($LASTEXITCODE -ne 0) { throw 'workspace smoke build failed' }
if (-not (Test-Path tooling/workspace-smoke/dist/index.mjs)) { throw 'workspace smoke build output missing' }
$dryRun = pnpm exec turbo run check test build --dry=json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Turbo dry-run failed' }
$taskIds = @($dryRun.tasks.taskId)
@(
  '@game-night/workspace-smoke#check',
  '@game-night/workspace-smoke#test',
  '@game-night/workspace-smoke#build'
) | ForEach-Object {
  if ($_ -notin $taskIds) { throw "missing Turbo task: $_" }
}
```

Expected: 三个任务通过，构建文件存在，dry-run 精确包含 smoke package 的三个 task ID。

- [ ] **Step 5: 提交非空任务证明**

```powershell
git add pnpm-workspace.yaml pnpm-lock.yaml tooling/workspace-smoke
git status --short
git diff --staged --check
git diff --staged --name-only
git diff --staged
git commit -m "test(repo): 添加 workspace 任务烟雾包"
```

### Task 4: 建立责任根而不创建空业务包

**Files:**
- Create: `apps/README.md`
- Create: `platform/README.md`
- Create: `sdk/README.md`
- Create: `packages/README.md`
- Create: `games/README.md`
- Create: `contracts/README.md`
- Create: `tooling/README.md`

- [ ] **Step 1: 创建应用与平台责任说明**

`apps/README.md`：

```markdown
# Apps

应用目录只负责进程或前端入口、组合依赖和传输适配。

- `web`: 移动 Web/PWA 入口。
- `admin`: 管理后台入口。
- `api`: ConnectRPC HTTP 入口。
- `realtime`: WebSocket 网关与会话进程入口。
- `worker`: 异步任务入口。

应用可以组合 `platform`、`sdk`、`packages` 和构建注册的 `games`，但不得承载可复用业务规则。
```

`platform/README.md`：

```markdown
# Platform

平台目录承载身份、资料、房间、大厅、治理、复盘、主题目录、游戏运行时和持久化适配器。

平台模块不能导入具体游戏。跨模块协作通过明确接口和领域事件完成，数据库、Redis 和对象存储依赖只允许出现在适配器边界。
```

- [ ] **Step 2: 创建 SDK 与前端共享包说明**

`sdk/README.md`：

```markdown
# SDK

- `go/game`: 服务端纯规则引擎契约。
- `ts/game-client`: 客户端游戏模块与平台端口。

SDK 只定义稳定契约，不依赖应用入口、具体游戏或持久化实现。
```

`packages/README.md`：

```markdown
# Packages

前端共享包按责任拆分为游戏 UI 原语、平台壳层、主题运行时和测试夹具。

共享包不得读取服务端权威状态；业务动作只能通过客户端 SDK 的 dispatch 端口发送。
```

- [ ] **Step 3: 创建游戏、协议和工具说明**

`games/README.md`：

```markdown
# Games

每款游戏在自己的目录维护协议、纯规则引擎、可见性投影、客户端模块、主题和测试。

游戏不能导入应用入口或 PartyRoom 内部实现。服务端引擎只能依赖服务端 Game SDK 和自身纯规则子包；客户端只能依赖客户端 SDK 与游戏 UI 原语；主题不能改变规则或安全语义。
```

`contracts/README.md`：

```markdown
# Contracts

`platform/` 保存平台级 Protocol Buffers；每款游戏的协议保存在对应 `games/<game>/proto/`。

生成代码写入 `contracts/gen/go` 和 `contracts/gen/ts`，只由 `buf generate` 更新。禁止手工编辑生成文件，也禁止将所有游戏消息合并进平台级 `oneof`。
```

`tooling/README.md`：

```markdown
# Tooling

本目录保存注册表生成、协议生成辅助、依赖边界检查和仓库验证工具。

工具必须可在本地和 CI 中以同一命令运行；失败必须返回非零退出码，不得只输出警告后继续。
```

- [ ] **Step 4: 验证责任根完整**

```powershell
@('apps','platform','sdk','packages','games','contracts','tooling') | ForEach-Object {
  if (-not (Test-Path "$($_)/README.md")) { throw "missing responsibility root: $_" }
}
git diff --check
```

Expected: 无异常且 `git diff --check` 无输出。

- [ ] **Step 5: 提交责任根**

```powershell
git add apps platform sdk packages games contracts tooling
git status --short
git diff --staged --name-only
git diff --staged --check
git diff --staged
git commit -m "docs(repo): 定义 monorepo 目录责任"
```

### Task 5: 以非生产 smoke 夹具验证 Buf 全链路

**Files:**
- Create: `buf.yaml`
- Create: `buf.gen.yaml`
- Create: `tooling/testdata/proto/buf.yaml`
- Create: `tooling/testdata/proto/buf.gen.yaml`
- Create: `tooling/testdata/proto/go.mod`
- Create: `tooling/testdata/proto/go.sum` (generated)
- Create: `tooling/testdata/proto/tooling/smoke/v1/smoke.proto`
- Create: `tooling/testdata/proto-baseline/buf.yaml`
- Create: `tooling/testdata/proto-baseline/tooling/smoke/v1/smoke.proto`
- Create: `tooling/testdata/proto-breaking/buf.yaml`
- Create: `tooling/testdata/proto-breaking/tooling/smoke/v1/smoke.proto`
- Create: `tooling/testdata/proto/gen/go/**` (generated)
- Create: `tooling/testdata/proto/gen/ts/**` (generated)

- [ ] **Step 1: 创建生产协议拓扑与固定生成模板**

`buf.yaml`：

```yaml
version: v2
modules:
  - path: contracts
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
```

当前只有 `contracts` 生产 module。每个游戏首次加入 `games/<game>/proto` 时，必须在同一提交把该路径追加到 `modules`，禁止让游戏 proto 游离在 Buf workspace 外。

`buf.gen.yaml`：

```yaml
version: v2
clean: true
plugins:
  - remote: buf.build/protocolbuffers/go:v1.36.6
    out: contracts/gen/go
    opt:
      - paths=source_relative
  - remote: buf.build/connectrpc/go:v1.18.1
    out: contracts/gen/go
    opt:
      - paths=source_relative
  - remote: buf.build/bufbuild/es:v2.6.2
    out: contracts/gen/ts
    opt:
      - target=ts
```

这些版本是仓库初始兼容基线。升级必须通过单独提交完成，并同时运行 lint、breaking、generate 和全量测试。

- [ ] **Step 2: 创建独立 smoke module 与三语言生成模板**

`tooling/testdata/proto/buf.yaml`：

```yaml
version: v2
modules:
  - path: .
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
```

`tooling/testdata/proto/buf.gen.yaml`：

```yaml
version: v2
clean: true
plugins:
  - remote: buf.build/protocolbuffers/go:v1.36.6
    out: gen/go
    opt:
      - paths=source_relative
  - remote: buf.build/connectrpc/go:v1.18.1
    out: gen/go
    opt:
      - paths=source_relative
  - remote: buf.build/bufbuild/es:v2.6.2
    out: gen/ts
    opt:
      - target=ts
```

`tooling/testdata/proto/go.mod`：

```go
module github.com/iFTY-R/game-night/tooling/testdata/proto

go 1.26.4
```

该嵌套 module 隔离生成代码依赖，避免 smoke 的 Protobuf/Connect 运行时依赖污染根 module。`go mod tidy` 会补全精确依赖并生成 `go.sum`，两者一并提交。

`tooling/testdata/proto/tooling/smoke/v1/smoke.proto`：

```proto
syntax = "proto3";

package tooling.smoke.v1;

option go_package = "github.com/iFTY-R/game-night/tooling/testdata/proto/gen/go/tooling/smoke/v1;smokev1";

message PingRequest {
  string payload = 1;
}

message PingResponse {
  string payload = 1;
}

service SmokeService {
  rpc Ping(PingRequest) returns (PingResponse);
}
```

- [ ] **Step 3: 创建兼容基线与预期失败的破坏性夹具**

`tooling/testdata/proto-baseline/buf.yaml`：

```yaml
version: v2
modules:
  - path: .
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
```

`tooling/testdata/proto-breaking/buf.yaml`：

```yaml
version: v2
modules:
  - path: .
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
```

`tooling/testdata/proto-baseline/tooling/smoke/v1/smoke.proto`：

```proto
syntax = "proto3";

package tooling.smoke.v1;

option go_package = "github.com/iFTY-R/game-night/tooling/testdata/proto/gen/go/tooling/smoke/v1;smokev1";

message PingRequest {
  string payload = 1;
}

message PingResponse {
  string payload = 1;
}

service SmokeService {
  rpc Ping(PingRequest) returns (PingResponse);
}
```

`tooling/testdata/proto-breaking/tooling/smoke/v1/smoke.proto`：

```proto
syntax = "proto3";

package tooling.smoke.v1;

option go_package = "github.com/iFTY-R/game-night/tooling/testdata/proto/gen/go/tooling/smoke/v1;smokev1";

message PingRequest {
  int64 payload = 1;
}

message PingResponse {
  string payload = 1;
}

service SmokeService {
  rpc Ping(PingRequest) returns (PingResponse);
}
```

- [ ] **Step 4: 实际调用三个插件并验证兼容/破坏结果**

若 `buf --version` 失败，停止并按前置条件取得安装许可。Buf 可用且网络恢复后运行：

```powershell
Push-Location tooling/testdata/proto
try {
  buf format --diff --exit-code
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke format failed' }
  buf lint
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke lint failed' }
  buf generate --template buf.gen.yaml
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke generate failed' }
  @(
    'gen/go/tooling/smoke/v1/smoke.pb.go',
    'gen/go/tooling/smoke/v1/smoke.connect.go',
    'gen/ts/tooling/smoke/v1/smoke_pb.ts'
  ) | ForEach-Object { if (-not (Test-Path $_)) { throw "missing generated smoke output: $_" } }
  go mod tidy
  if ($LASTEXITCODE -ne 0) { throw 'smoke go mod tidy failed' }
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw 'generated smoke Go test failed' }
} finally {
  Pop-Location
}
buf breaking tooling/testdata/proto --against tooling/testdata/proto-baseline
if ($LASTEXITCODE -ne 0) { throw 'compatible smoke breaking check failed' }
buf breaking tooling/testdata/proto-breaking --against tooling/testdata/proto-baseline
$breakingExitCode = $LASTEXITCODE
if ($breakingExitCode -eq 0) { throw 'breaking fixture unexpectedly passed' }
Write-Output "expected breaking fixture failure: exit $breakingExitCode"
```

Expected: format、lint、generate、嵌套 module 编译和兼容检查通过；最后一条 breaking 命令返回非零并被断言为预期失败。此时没有生产 proto，因此生产 breaking 基线状态是 `SKIPPED`，不能报告为 PASS。

- [ ] **Step 5: 提交 Buf 配置、夹具和真实生成产物**

```powershell
git add buf.yaml buf.gen.yaml tooling/testdata/proto tooling/testdata/proto-baseline tooling/testdata/proto-breaking
git status --short
git diff --staged --name-only
git diff --staged --check
git diff --staged
git commit -m "build(proto): 验证 Buf 跨语言生成链路"
```

### Task 6: 以 TDD 实现纯依赖边界策略

**Files:**
- Create: `tooling/boundarycheck/policy_test.go`
- Create: `tooling/boundarycheck/policy.go`

- [ ] **Step 1: 先写失败的边界规则测试**

`tooling/boundarycheck/policy_test.go`：

```go
package boundarycheck

import "testing"

func TestValidateEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		edge        Edge
		wantAllowed bool
	}{
		{name: "app may compose platform", edge: Edge{From: "apps/api", To: "platform/identity"}, wantAllowed: true},
		{name: "engine may use game sdk", edge: Edge{From: "games/dice/engine/rules", To: "sdk/go/game"}, wantAllowed: true},
		{name: "client may use ui kit", edge: Edge{From: "games/dice/client", To: "packages/game-ui-kit"}, wantAllowed: true},
		{name: "client may use client sdk", edge: Edge{From: "games/dice/client", To: "sdk/ts/game-client"}, wantAllowed: true},
		{name: "client may use own subpackage", edge: Edge{From: "games/dice/client/table", To: "games/dice/client/actions"}, wantAllowed: true},
		{name: "game cannot import app", edge: Edge{From: "games/dice/engine", To: "apps/realtime"}},
		{name: "platform cannot import game", edge: Edge{From: "platform/game-runtime", To: "games/dice/engine"}},
		{name: "engine cannot import persistence", edge: Edge{From: "games/dice/engine", To: "platform/persistence"}},
		{name: "engine cannot import infra", edge: Edge{From: "games/dice/engine", To: "infra/migrations"}},
		{name: "engine cannot read environment", edge: Edge{From: "games/dice/engine", To: "os"}},
		{name: "engine cannot use network", edge: Edge{From: "games/dice/engine", To: "net/http"}},
		{name: "engine cannot use database sql", edge: Edge{From: "games/dice/engine", To: "database/sql"}},
		{name: "engine cannot use pgx", edge: Edge{From: "games/dice/engine", To: "github.com/jackc/pgx/v5"}},
		{name: "engine cannot use redis", edge: Edge{From: "games/dice/engine", To: "github.com/redis/go-redis/v9"}},
		{name: "engine cannot own randomness", edge: Edge{From: "games/dice/engine", To: "math/rand"}},
		{name: "projection cannot import party room", edge: Edge{From: "games/dice/projection", To: "platform/room"}},
		{name: "client cannot import projection", edge: Edge{From: "games/dice/client", To: "games/dice/projection"}},
		{name: "client cannot import platform", edge: Edge{From: "games/dice/client", To: "platform/identity"}},
		{name: "client cannot import another client", edge: Edge{From: "games/dice/client", To: "games/texas-holdem/client"}},
		{name: "client cannot import arbitrary shared package", edge: Edge{From: "games/dice/client", To: "packages/platform-ui"}},
		{name: "theme cannot import rules", edge: Edge{From: "games/dice/themes/neon", To: "games/dice/engine"}},
		{name: "sdk cannot import concrete game", edge: Edge{From: "sdk/go/game", To: "games/dice/engine"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			violations := ValidateEdges([]Edge{tt.edge})
			if tt.wantAllowed && len(violations) != 0 {
				t.Fatalf("expected edge to be allowed, got %#v", violations)
			}
			if !tt.wantAllowed && len(violations) != 1 {
				t.Fatalf("expected one violation, got %#v", violations)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试并确认按预期失败**

```powershell
go test ./tooling/boundarycheck -run TestValidateEdges -v
```

Expected: FAIL，提示 `undefined: Edge` 或 `undefined: ValidateEdges`。

- [ ] **Step 3: 实现最小纯策略**

`tooling/boundarycheck/policy.go`：

```go
// Package boundarycheck enforces the repository dependency directions defined by the platform spec.
package boundarycheck

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Edge represents a direct dependency from a repository package to an internal path or external import.
type Edge struct {
	From string
	To   string
}

// Violation explains which dependency edge crossed a forbidden ownership boundary.
type Violation struct {
	Edge   Edge
	Reason string
}

// ValidateEdges returns every forbidden edge so CI can report all boundary failures in one run.
func ValidateEdges(edges []Edge) []Violation {
	violations := make([]Violation, 0)
	for _, edge := range edges {
		normalized := Edge{From: normalize(edge.From), To: normalize(edge.To)}
		if reason, forbidden := forbiddenReason(normalized); forbidden {
			violations = append(violations, Violation{Edge: normalized, Reason: reason})
		}
	}
	return violations
}

func forbiddenReason(edge Edge) (string, bool) {
	if under(edge.From, "games") && under(edge.To, "apps") {
		return "games cannot import application entrypoints", true
	}
	if under(edge.From, "platform") && under(edge.To, "games") {
		return "platform modules cannot import concrete games", true
	}
	if under(edge.From, "sdk") && (under(edge.To, "apps") || under(edge.To, "platform") || under(edge.To, "games")) {
		return "SDK packages cannot import applications, platform implementations, or concrete games", true
	}
	if under(edge.From, "games") && under(edge.To, "platform/room") {
		return "games cannot import PartyRoom internals", true
	}
	if gameArea(edge.From, "engine") {
		if reason, forbidden := forbiddenEngineImport(edge.To); forbidden {
			return reason, true
		}
		if internalEngineDependencyForbidden(edge.From, edge.To) {
			return "game engines may only import their own engine packages and sdk/go/game", true
		}
	}
	if gameArea(edge.From, "client") && internalClientDependencyForbidden(edge.From, edge.To) {
		return "game clients may only import their own client packages, sdk/ts/game-client, and packages/game-ui-kit", true
	}
	if gameArea(edge.From, "themes") && (gameArea(edge.To, "engine") || gameArea(edge.To, "projection") || under(edge.To, "platform/game-runtime")) {
		return "themes cannot import game rules or authoritative runtime state", true
	}
	return "", false
}

func forbiddenEngineImport(importPath string) (string, bool) {
	for _, prefix := range []string{
		"os",
		"io/fs",
		"net",
		"database/sql",
		"crypto/rand",
		"math/rand",
		"time",
		"github.com/jackc/pgx",
		"github.com/redis/go-redis",
		"github.com/go-redis",
		"gorm.io",
	} {
		if under(importPath, prefix) {
			return "game engines cannot own IO, clocks, randomness, database, or Redis access", true
		}
	}
	return "", false
}

func internalEngineDependencyForbidden(from, to string) bool {
	if !isRepositoryRoot(to) || under(to, "sdk/go/game") {
		return false
	}
	fromParts := strings.Split(from, "/")
	toParts := strings.Split(to, "/")
	return len(fromParts) < 2 || len(toParts) < 3 || toParts[0] != "games" || toParts[1] != fromParts[1] || toParts[2] != "engine"
}

func internalClientDependencyForbidden(from, to string) bool {
	if !isRepositoryRoot(to) || under(to, "sdk/ts/game-client") || under(to, "packages/game-ui-kit") {
		return false
	}
	fromParts := strings.Split(from, "/")
	toParts := strings.Split(to, "/")
	return len(fromParts) < 2 || len(toParts) < 3 || toParts[0] != "games" || toParts[1] != fromParts[1] || toParts[2] != "client"
}

func gameArea(value, area string) bool {
	parts := strings.Split(normalize(value), "/")
	return len(parts) >= 3 && parts[0] == "games" && parts[2] == area
}

func isRepositoryRoot(value string) bool {
	for _, root := range []string{"apps", "platform", "sdk", "packages", "games", "contracts", "infra", "tooling"} {
		if under(value, root) {
			return true
		}
	}
	return false
}

func under(value, prefix string) bool {
	return value == prefix || strings.HasPrefix(value, prefix+"/")
}

func normalize(value string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(value)), "./")
}

// String formats one violation for stable local and CI diagnostics.
func (v Violation) String() string {
	return fmt.Sprintf("%s -> %s: %s", v.Edge.From, v.Edge.To, v.Reason)
}
```

- [ ] **Step 4: 运行测试并确认通过**

```powershell
gofmt -w tooling/boundarycheck/policy.go tooling/boundarycheck/policy_test.go
go test ./tooling/boundarycheck -run TestValidateEdges -v
```

Expected: PASS，22 个子测试全部通过。

- [ ] **Step 5: 提交纯边界策略**

```powershell
git add tooling/boundarycheck/policy.go tooling/boundarycheck/policy_test.go
git status --short
git diff --staged --name-only
git diff --staged --check
git diff --staged
git commit -m "feat(tooling): 添加仓库依赖边界规则"
```

### Task 7: 发现真实 Go 与 pnpm workspace 依赖

**Files:**
- Create: `tooling/boundarycheck/discover_test.go`
- Create: `tooling/boundarycheck/discover.go`
- Create: `tooling/cmd/boundarycheck/main.go`
- Create: `tooling/cmd/boundarycheck/main_test.go`
- Create: `tooling/testdata/boundaries/forbidden/go.mod`
- Create: `tooling/testdata/boundaries/forbidden/package.json`
- Create: `tooling/testdata/boundaries/forbidden/pnpm-workspace.yaml`
- Create: `tooling/testdata/boundaries/forbidden/games/dice/engine/bad.go`
- Create: `tooling/testdata/boundaries/forbidden/games/dice/package.json`
- Create: `tooling/testdata/boundaries/forbidden/platform/room/package.json`
- Create: `tooling/testdata/boundaries/forbidden/apps/realtime/package.json`
- Modify: `package.json`

- [ ] **Step 1: 先写失败的结构化发现测试**

`tooling/boundarycheck/discover_test.go`：

```go
package boundarycheck

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDecodeGoListEdges(t *testing.T) {
	t.Parallel()

	input := strings.NewReader(`{"ImportPath":"github.com/iFTY-R/game-night/platform/room","Imports":["github.com/iFTY-R/game-night/games/dice/engine","context"]}
{"ImportPath":"github.com/iFTY-R/game-night/games/dice/engine","Imports":["github.com/iFTY-R/game-night/sdk/go/game","os"]}`)
	edges, err := decodeGoListEdges(input, "github.com/iFTY-R/game-night")
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 3 {
		t.Fatalf("expected two internal edges and one engine external edge, got %#v", edges)
	}
	if edges[2] != (Edge{From: "games/dice/engine", To: "os"}) {
		t.Fatalf("expected engine external import to be preserved, got %#v", edges)
	}
}

func TestDecodePnpmWorkspaceEdges(t *testing.T) {
	t.Parallel()

	root := filepath.Join(string(filepath.Separator), "repo")
	listed, err := json.Marshal([]pnpmListPackage{
		{Name: "@game-night/platform-ui", Path: filepath.Join(root, "packages", "platform-ui")},
		{Name: "@game-night/game-client", Path: filepath.Join(root, "sdk", "ts", "game-client")},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		"packages/platform-ui/package.json": &fstest.MapFile{Data: []byte(`{"name":"@game-night/platform-ui","dependencies":{"@game-night/game-client":"workspace:*"}}`)},
		"sdk/ts/game-client/package.json": &fstest.MapFile{Data: []byte(`{"name":"@game-night/game-client"}`)},
	}
	edges, err := decodePnpmWorkspaceEdges(bytes.NewReader(listed), files, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0] != (Edge{From: "packages/platform-ui", To: "sdk/ts/game-client"}) {
		t.Fatalf("unexpected workspace edges: %#v", edges)
	}
}

func TestForbiddenFixtureProducesViolations(t *testing.T) {
	t.Parallel()

	root, err := RepositoryRoot("../testdata/boundaries/forbidden")
	if err != nil {
		t.Fatal(err)
	}
	edges, err := DiscoverEdges(context.Background(), root, "github.com/iFTY-R/game-night")
	if err != nil {
		t.Fatal(err)
	}
	violations := ValidateEdges(edges)
	if len(violations) < 3 {
		t.Fatalf("expected package, application, and engine IO violations, got %#v", violations)
	}
}
```

- [ ] **Step 2: 运行测试并确认失败**

```powershell
go test ./tooling/boundarycheck -run "TestDecodeGoListEdges|TestDecodePnpmWorkspaceEdges|TestForbiddenFixtureProducesViolations" -v
```

Expected: FAIL，提示发现函数或集成夹具尚不存在。

- [ ] **Step 3: 创建同时违反三类规则的隔离夹具**

`tooling/testdata/boundaries/forbidden/go.mod`：

```go
module github.com/iFTY-R/game-night

go 1.26.4
```

`tooling/testdata/boundaries/forbidden/package.json`：

```json
{
  "name": "@game-night/boundary-fixture-root",
  "private": true
}
```

`tooling/testdata/boundaries/forbidden/pnpm-workspace.yaml`：

```yaml
packages:
  - apps/*
  - platform/*
  - games/*
```

`tooling/testdata/boundaries/forbidden/games/dice/engine/bad.go`：

```go
package engine

import "os"

// ReadEnvForFixture deliberately violates the pure-engine boundary for integration testing.
func ReadEnvForFixture() string {
	return os.Getenv("GAME_NIGHT_BOUNDARY_FIXTURE")
}
```

`tooling/testdata/boundaries/forbidden/games/dice/package.json`：

```json
{
  "name": "@game-night/dice",
  "dependencies": {
    "@game-night/realtime": "workspace:*"
  }
}
```

`tooling/testdata/boundaries/forbidden/platform/room/package.json`：

```json
{
  "name": "@game-night/room",
  "dependencies": {
    "@game-night/dice": "workspace:*"
  }
}
```

`tooling/testdata/boundaries/forbidden/apps/realtime/package.json`：

```json
{
  "name": "@game-night/realtime"
}
```

- [ ] **Step 4: 实现结构化依赖发现**

`tooling/boundarycheck/discover.go`：

```go
package boundarycheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type goListPackage struct {
	ImportPath string
	Imports    []string
}

type workspaceManifest struct {
	Name                 string            `json:"name"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

type pnpmListPackage struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// DiscoverEdges collects internal package edges and pure-engine external imports from structured metadata.
func DiscoverEdges(ctx context.Context, root, modulePath string) ([]Edge, error) {
	goEdges, err := discoverGoEdges(ctx, root, modulePath)
	if err != nil {
		return nil, err
	}
	workspaceEdges, err := discoverWorkspaceEdges(ctx, root)
	if err != nil {
		return nil, err
	}
	return append(goEdges, workspaceEdges...), nil
}

func discoverGoEdges(ctx context.Context, root, modulePath string) ([]Edge, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-json", "./...")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("go list failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("run go list: %w", err)
	}
	return decodeGoListEdges(strings.NewReader(string(output)), modulePath)
}

func decodeGoListEdges(reader io.Reader, modulePath string) ([]Edge, error) {
	decoder := json.NewDecoder(reader)
	edges := make([]Edge, 0)
	for {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		from, internal := moduleRelative(modulePath, pkg.ImportPath)
		if !internal {
			continue
		}
		for _, imported := range pkg.Imports {
			to, internal := moduleRelative(modulePath, imported)
			if internal {
				edges = append(edges, Edge{From: from, To: to})
				continue
			}
			// Pure engines must expose all direct external imports so IO, clock, and randomness bans are enforceable.
			if gameArea(from, "engine") {
				edges = append(edges, Edge{From: from, To: imported})
			}
		}
	}
	return edges, nil
}

func discoverWorkspaceEdges(ctx context.Context, root string) ([]Edge, error) {
	cmd := exec.CommandContext(ctx, "pnpm", "list", "--recursive", "--depth", "-1", "--json")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("pnpm list failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("run pnpm list: %w", err)
	}
	return decodePnpmWorkspaceEdges(strings.NewReader(string(output)), os.DirFS(root), root)
}

func decodePnpmWorkspaceEdges(reader io.Reader, root fs.FS, repositoryRoot string) ([]Edge, error) {
	var listed []pnpmListPackage
	if err := json.NewDecoder(reader).Decode(&listed); err != nil {
		return nil, fmt.Errorf("decode pnpm workspace list: %w", err)
	}
	manifests := make(map[string]string)
	parsed := make(map[string]workspaceManifest)
	for _, workspacePackage := range listed {
		relativeDirectory, err := filepath.Rel(repositoryRoot, workspacePackage.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace path %s: %w", workspacePackage.Path, err)
		}
		directory := normalize(relativeDirectory)
		if directory == "." {
			continue
		}
		if directory == ".." || strings.HasPrefix(directory, "../") {
			return nil, fmt.Errorf("workspace package %s is outside repository root", workspacePackage.Path)
		}
		manifestPath := path.Join(directory, "package.json")
		data, err := fs.ReadFile(root, manifestPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", manifestPath, err)
		}
		var manifest workspaceManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("decode %s: %w", manifestPath, err)
		}
		if manifest.Name == "" || manifest.Name != workspacePackage.Name {
			return nil, fmt.Errorf("workspace name mismatch in %s: pnpm=%s manifest=%s", manifestPath, workspacePackage.Name, manifest.Name)
		}
		if previous, exists := manifests[manifest.Name]; exists {
			return nil, fmt.Errorf("duplicate workspace package name %s in %s and %s", manifest.Name, previous, directory)
		}
		manifests[manifest.Name] = directory
		parsed[directory] = manifest
	}

	edges := make([]Edge, 0)
	for directory, manifest := range parsed {
		for _, dependencies := range []map[string]string{manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies, manifest.PeerDependencies} {
			for dependency := range dependencies {
				if target, internal := manifests[dependency]; internal {
					edges = append(edges, Edge{From: directory, To: target})
				}
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From == edges[j].From {
			return edges[i].To < edges[j].To
		}
		return edges[i].From < edges[j].From
	})
	return edges, nil
}

func moduleRelative(modulePath, importPath string) (string, bool) {
	if importPath == modulePath {
		return ".", true
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	return normalize(strings.TrimPrefix(importPath, prefix)), true
}

// RepositoryRoot resolves the command root once so all discovery uses the same ownership boundary.
func RepositoryRoot(value string) (string, error) {
	root, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	return root, nil
}
```

- [ ] **Step 5: 实现 CLI、非零退出测试并接入根脚本**

`tooling/cmd/boundarycheck/main.go`：

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/iFTY-R/game-night/tooling/boundarycheck"
)

// modulePath anchors Go imports to repository-relative ownership paths.
const modulePath = "github.com/iFTY-R/game-night"

func main() {
	rootFlag := flag.String("root", ".", "repository root to inspect")
	flag.Parse()

	root, err := boundarycheck.RepositoryRoot(*rootFlag)
	if err != nil {
		fail(err)
	}
	edges, err := boundarycheck.DiscoverEdges(context.Background(), root, modulePath)
	if err != nil {
		fail(err)
	}
	violations := boundarycheck.ValidateEdges(edges)
	for _, violation := range violations {
		fmt.Fprintln(os.Stderr, violation.String())
	}
	if len(violations) != 0 {
		os.Exit(1)
	}
	fmt.Printf("dependency boundaries passed (%d dependency edges)\n", len(edges))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
```

`tooling/cmd/boundarycheck/main_test.go`：

```go
package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIRejectsForbiddenFixture(t *testing.T) {
	fixture, err := filepath.Abs("../../testdata/boundaries/forbidden")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", ".", "-root", fixture)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected CLI failure, output: %s", output.String())
	}
	for _, expected := range []string{"platform modules cannot import concrete games", "games cannot import application entrypoints", "game engines cannot own IO"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in CLI output: %s", expected, output.String())
		}
	}
}
```

将 `package.json` 的 scripts 改为：

```json
"scripts": {
  "build": "turbo run build",
  "check": "pnpm run check:boundaries && turbo run check",
  "check:boundaries": "go run ./tooling/cmd/boundarycheck",
  "test": "go test ./... && turbo run test"
}
```

- [ ] **Step 6: 运行发现、策略、CLI 失败夹具和真实仓库检查**

```powershell
gofmt -w tooling/boundarycheck/discover.go tooling/boundarycheck/discover_test.go tooling/cmd/boundarycheck/main.go tooling/cmd/boundarycheck/main_test.go tooling/testdata/boundaries/forbidden/games/dice/engine/bad.go
go test ./tooling/boundarycheck -v
go test ./tooling/cmd/boundarycheck -run TestCLIRejectsForbiddenFixture -v
go run ./tooling/cmd/boundarycheck
pnpm run check:boundaries
```

Expected: 单元测试和 CLI 非零退出测试全部 PASS；真实仓库检查两次均输出 `dependency boundaries passed`。

- [ ] **Step 7: 提交依赖发现、夹具与 CLI**

```powershell
git add tooling/boundarycheck/discover.go tooling/boundarycheck/discover_test.go tooling/cmd/boundarycheck tooling/testdata/boundaries package.json
git status --short
git diff --staged --name-only
git diff --staged --check
git diff --staged
git commit -m "feat(tooling): 检查真实仓库依赖方向"
```

### Task 8: 建立 GitHub Actions 基础门禁

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: 创建仓库与协议 CI**

`.github/workflows/ci.yml`：

```yaml
name: ci

on:
  pull_request:
  push:
    branches:
      - master

permissions:
  contents: read

concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  repository:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - name: Checkout full history
        uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: .go-version
          cache: true
      - name: Set up pnpm
        uses: pnpm/action-setup@v4
        with:
          version: 11.13.1
          run_install: false
      - name: Set up Node.js
        uses: actions/setup-node@v4
        with:
          node-version-file: .node-version
          cache: pnpm
      - name: Install workspace dependencies
        run: pnpm install --frozen-lockfile
      - name: Check Go formatting
        run: test -z "$(gofmt -l tooling/boundarycheck tooling/cmd/boundarycheck tooling/testdata/boundaries/forbidden/games)"
      - name: Test Go packages
        run: go test ./...
      - name: Vet Go packages
        run: go vet ./...
      - name: Run repository checks
        run: pnpm run check
      - name: Run repository tests
        run: pnpm test
      - name: Build workspace
        run: pnpm build
      - name: Validate Turbo task graph
        run: |
          pnpm exec turbo run check test build --dry=json > "$RUNNER_TEMP/turbo-dry.json"
          node -e "const fs=require('node:fs');const data=JSON.parse(fs.readFileSync(process.env.RUNNER_TEMP+'/turbo-dry.json','utf8'));const ids=new Set(data.tasks.map((task)=>task.taskId));for(const id of ['@game-night/workspace-smoke#check','@game-night/workspace-smoke#test','@game-night/workspace-smoke#build']){if(!ids.has(id))throw new Error('missing Turbo task: '+id)}"

  contracts:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - name: Checkout full history
        uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - name: Set up Go for generated smoke code
        uses: actions/setup-go@v5
        with:
          go-version-file: .go-version
          cache-dependency-path: tooling/testdata/proto/go.sum
      - name: Set up Buf
        uses: bufbuild/buf-setup-action@v1
        with:
          version: 1.50.0
      - name: Verify Buf smoke generation
        run: |
          (cd tooling/testdata/proto && buf format --diff --exit-code && buf lint && buf generate --template buf.gen.yaml && go test ./...)
          buf breaking tooling/testdata/proto --against tooling/testdata/proto-baseline
          if buf breaking tooling/testdata/proto-breaking --against tooling/testdata/proto-baseline; then
            echo "breaking fixture unexpectedly passed"
            exit 1
          fi
          git diff --exit-code
          test -z "$(git status --porcelain)"
      - name: Verify production Proto when present
        run: |
          if find contracts games -type f -name '*.proto' -print -quit | grep -q .; then
            while IFS= read -r proto; do
              buf lint --path "$proto"
            done < <(find contracts games -type f -name '*.proto' -print)
            buf format --diff --exit-code
            buf lint
            buf generate
            git diff --exit-code
            test -z "$(git status --porcelain)"
          else
            echo "SKIPPED: no production Proto files"
          fi
      - name: Check production breaking changes
        if: github.event_name == 'pull_request'
        run: |
          git rev-parse --verify "origin/${{ github.base_ref }}^{commit}"
          if git ls-tree -r --name-only "origin/${{ github.base_ref }}" -- contracts games | grep -q '\.proto$'; then
            buf breaking --against ".git#branch=origin/${{ github.base_ref }}"
          else
            echo "SKIPPED: no production Proto baseline"
          fi
```

- [ ] **Step 2: 本地验证 CI 对应命令**

```powershell
pnpm install --frozen-lockfile
if ($LASTEXITCODE -ne 0) { throw 'frozen pnpm install failed' }
go test ./...
if ($LASTEXITCODE -ne 0) { throw 'root Go tests failed' }
go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'root Go vet failed' }
pnpm run check
if ($LASTEXITCODE -ne 0) { throw 'repository checks failed' }
pnpm test
if ($LASTEXITCODE -ne 0) { throw 'repository tests failed' }
pnpm build
if ($LASTEXITCODE -ne 0) { throw 'workspace build failed' }
$dryRun = pnpm exec turbo run check test build --dry=json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Turbo task graph validation failed' }
$taskIds = @($dryRun.tasks.taskId)
@('@game-night/workspace-smoke#check', '@game-night/workspace-smoke#test', '@game-night/workspace-smoke#build') | ForEach-Object {
  if ($_ -notin $taskIds) { throw "missing Turbo task: $_" }
}
Push-Location tooling/testdata/proto
try {
  buf format --diff --exit-code
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke format failed' }
  buf lint
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke lint failed' }
  buf generate --template buf.gen.yaml
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke generate failed' }
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw 'generated smoke Go test failed' }
} finally {
  Pop-Location
}
buf breaking tooling/testdata/proto --against tooling/testdata/proto-baseline
if ($LASTEXITCODE -ne 0) { throw 'compatible smoke breaking check failed' }
git diff --exit-code -- tooling/testdata/proto/gen
if ($LASTEXITCODE -ne 0) { throw 'tracked generated smoke drift detected' }
if (git status --porcelain -- tooling/testdata/proto/gen) { throw 'generated smoke drift detected' }
```

Expected: 所有命令退出码为 0，生成步骤不修改工作树。

- [ ] **Step 3: 提交 CI**

```powershell
git add .github/workflows/ci.yml
git status --short
git diff --staged --name-only
git diff --staged --check
git diff --staged
git commit -m "ci(repo): 添加基础质量门禁"
```

### Task 9: 记录架构决策与开发流程

**Files:**
- Create: `docs/adr/0001-monorepo-toolchain.md`
- Create: `docs/operations/development.md`
- Modify: `README.md`

- [ ] **Step 1: 创建 ADR**

`docs/adr/0001-monorepo-toolchain.md`：

```markdown
# ADR 0001: 采用 Go、pnpm 与 Buf 的单仓库工具链

- 状态：Accepted
- 日期：2026-07-17

## Context

平台需要同时维护 Go 服务端、Vue/TypeScript 客户端、跨语言协议和可独立扩展的游戏模块。首套生产部署是单机，但代码边界必须支持后续横向扩展。

## Decision

采用一个 Git monorepo：根 Go module 管理服务端代码，pnpm workspace + Turborepo 管理前端与 TypeScript 包，Buf 管理 Protocol Buffers lint、breaking 和生成。依赖方向由仓库边界检查器与 CI 强制。

## Consequences

- 一个变更可以原子更新协议、服务端与客户端。
- Go 与 TypeScript 工具链必须同时维护，CI 成本高于单语言仓库。
- 生成插件、工具链和 lockfile 必须固定版本。
- 具体游戏保持自包含，平台不能反向依赖游戏实现。
```

- [ ] **Step 2: 创建开发说明**

`docs/operations/development.md`：

```markdown
# 本地开发

## 前置工具

- Go 1.26.4
- Node.js 24.18.0
- pnpm 11.13.1（通过 Corepack）
- Buf（协议变更需要）

Docker 在仓库基础阶段不是必需项，后续数据库、Redis、Testcontainers 和部署验证会使用。

Windows 环境若缺少 Buf，先向项目所有者确认，再执行 `scoop install buf`。不要由脚本静默安装全局工具。

## 初始化

```powershell
corepack enable
pnpm install --frozen-lockfile
```

## 验证

```powershell
go test ./...
if ($LASTEXITCODE -ne 0) { throw 'root Go tests failed' }
go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'root Go vet failed' }
pnpm run check
if ($LASTEXITCODE -ne 0) { throw 'repository checks failed' }
Push-Location tooling/testdata/proto
try {
  buf format --diff --exit-code
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke format failed' }
  buf lint
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke lint failed' }
  buf generate --template buf.gen.yaml
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke generate failed' }
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw 'generated smoke Go test failed' }
} finally {
  Pop-Location
}
buf breaking tooling/testdata/proto --against tooling/testdata/proto-baseline
if ($LASTEXITCODE -ne 0) { throw 'compatible smoke breaking check failed' }
git diff --exit-code
if ($LASTEXITCODE -ne 0) { throw 'tracked file drift detected' }
```

生产协议出现后，breaking check 使用 PR 的 base ref。首个生产协议尚无基线时必须明确报告 `SKIPPED`，不能把当前分支与自身比较后报告通过：

```powershell
buf breaking --against ".git#branch=origin/master"
```

## 约束

- 不从 `.tmp/` 或外部参考仓库复制代码、架构或资产。
- 不手工修改 `pnpm-lock.yaml` 或 Buf 生成文件。
- 新包必须遵守各责任根 README 和边界检查器规则。
```

- [ ] **Step 3: 更新项目入口文档**

将 `README.md` 完整替换为：

```markdown
# 今晚开局

一个面向中国大陆用户、移动端优先的多人在线游戏平台。朋友可以创建持续房间，在同一房间中切换骰子系列、三关定胜负和德州扑克。

## 当前阶段

总体产品与技术设计已经确认，正在按正式交付顺序建设仓库与工具链基础。阶段顺序只表达依赖，不代表 MVP 缩减。

## 技术方向

- Vue 3、Vite、TypeScript、pnpm workspace 与 Turborepo
- Go 模块化平台内核、ConnectRPC 与独立 WebSocket 实时网关
- Protocol Buffers + Buf 跨语言协议
- PostgreSQL 权威持久化、Redis 协调、S3 兼容对象存储

## 文档

- [游戏平台设计规范](docs/superpowers/specs/2026-07-17-game-night-platform-design.md)
- [仓库与工具链基础实施计划](docs/superpowers/plans/2026-07-17-repository-foundation.md)
- [本地开发](docs/operations/development.md)
- [ADR 0001：Monorepo 工具链](docs/adr/0001-monorepo-toolchain.md)

## 基础验证

```powershell
pnpm install --frozen-lockfile
go test ./...
go vet ./...
pnpm run check
buf lint tooling/testdata/proto
```

参考目录只用于理解玩法和识别体验问题，不复用其代码、架构或资产。
```

- [ ] **Step 4: 验证文档链接与格式**

```powershell
@(
  'docs/superpowers/specs/2026-07-17-game-night-platform-design.md',
  'docs/superpowers/plans/2026-07-17-repository-foundation.md',
  'docs/operations/development.md',
  'docs/adr/0001-monorepo-toolchain.md'
) | ForEach-Object {
  if (-not (Test-Path $_)) { throw "missing documentation: $_" }
}
git diff --check
```

Expected: 所有路径存在且格式检查无输出。

- [ ] **Step 5: 提交文档**

```powershell
git add README.md docs/adr/0001-monorepo-toolchain.md docs/operations/development.md
git status --short
git diff --staged --name-only
git diff --staged --check
git diff --staged
git commit -m "docs(repo): 补充架构决策与开发流程"
```

### Task 10: 执行完整基础验收

**Files:**
- No file changes expected.

- [ ] **Step 1: 运行格式、单元测试和静态检查**

```powershell
git diff --check
if ($LASTEXITCODE -ne 0) { throw 'diff check failed' }
$unformatted = gofmt -l tooling/boundarycheck tooling/cmd/boundarycheck tooling/testdata/boundaries/forbidden/games
if ($unformatted) { throw "unformatted Go files: $unformatted" }
go test ./...
if ($LASTEXITCODE -ne 0) { throw 'root Go tests failed' }
go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'root Go vet failed' }
pnpm install --frozen-lockfile
if ($LASTEXITCODE -ne 0) { throw 'frozen pnpm install failed' }
pnpm run check
if ($LASTEXITCODE -ne 0) { throw 'repository checks failed' }
pnpm test
if ($LASTEXITCODE -ne 0) { throw 'repository tests failed' }
pnpm build
if ($LASTEXITCODE -ne 0) { throw 'workspace build failed' }
```

Expected: 所有命令退出码为 0，边界检查输出通过。

- [ ] **Step 2: 运行协议与任务图检查**

```powershell
Push-Location tooling/testdata/proto
try {
  buf format --diff --exit-code
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke format failed' }
  buf lint
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke lint failed' }
  buf generate --template buf.gen.yaml
  if ($LASTEXITCODE -ne 0) { throw 'buf smoke generate failed' }
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw 'generated smoke Go test failed' }
} finally {
  Pop-Location
}
buf breaking tooling/testdata/proto --against tooling/testdata/proto-baseline
if ($LASTEXITCODE -ne 0) { throw 'compatible smoke breaking check failed' }
$dryRun = pnpm exec turbo run check test build --dry=json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Turbo task graph validation failed' }
$taskIds = @($dryRun.tasks.taskId)
@('@game-night/workspace-smoke#check', '@game-night/workspace-smoke#test', '@game-night/workspace-smoke#build') | ForEach-Object {
  if ($_ -notin $taskIds) { throw "missing Turbo task: $_" }
}
```

随后执行破坏性 smoke，并断言它失败：

```powershell
buf breaking tooling/testdata/proto-breaking --against tooling/testdata/proto-baseline
$breakingExitCode = $LASTEXITCODE
if ($breakingExitCode -eq 0) { throw 'breaking fixture unexpectedly passed' }
Write-Output "expected breaking fixture failure: exit $breakingExitCode"
$productionProtos = Get-ChildItem contracts,games -Recurse -Filter *.proto
if ($productionProtos.Count -eq 0) {
  Write-Output 'SKIPPED: no production Proto baseline'
} else {
  $productionProtos | ForEach-Object {
    $relativePath = (Resolve-Path -Relative $_.FullName).TrimStart('.', '\').Replace('\', '/')
    buf lint --path $relativePath
    if ($LASTEXITCODE -ne 0) { throw "production Proto is outside configured Buf modules: $relativePath" }
  }
  buf format --diff --exit-code
  if ($LASTEXITCODE -ne 0) { throw 'production Proto format failed' }
  buf lint
  if ($LASTEXITCODE -ne 0) { throw 'production Proto lint failed' }
  buf generate
  if ($LASTEXITCODE -ne 0) { throw 'production Proto generation failed' }
}
```

Expected: smoke 的 format、lint、generate 和兼容检查通过，破坏性检查按预期失败；生产 proto 不存在时明确输出 `SKIPPED`；Turbo 输出合法 JSON。

- [ ] **Step 3: 证明生成和安装没有污染仓库**

```powershell
git diff --exit-code
if ($LASTEXITCODE -ne 0) { throw 'tracked file drift detected' }
if (git status --porcelain) { throw 'worktree is not clean' }
```

Expected: 两项断言都不抛错。`git diff` 证明已跟踪文件未漂移，`git status` 同时证明没有新增的未跟踪生成文件。

- [ ] **Step 4: 检查提交边界**

```powershell
git log --oneline -9
```

Expected: 本计划产生 9 个单一用途的 Conventional Commits，没有参考目录、构建产物、临时文件或业务实现。

## 6. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 外部代理不可达导致 pnpm 或 Buf 插件下载失败 | 执行前先恢复网络；保留精确版本和 lockfile；不得用手写产物绕过 |
| Buf/Docker 未安装 | Buf 安装需用户许可；Docker 延后到需要 Compose/Testcontainers 的子计划 |
| 空仓库让检查产生假通过 | 边界策略与发现器都有内置夹具测试；真实业务包进入后沿用同一命令 |
| 远程插件或 GitHub Action 版本漂移 | Buf 插件固定完整版本；Action 升级和 Turbo 升级走独立提交与全量验证 |
| 大量空目录制造错误抽象 | 只提交责任 README；业务目录在对应子计划产生真实代码时创建 |
| 默认分支误写为 `main` | 所有 breaking 和 CI 基线显式使用当前 `master`，PR 使用 `github.base_ref` |
| 边界检查只看包依赖而漏掉源码深层导入 | 本阶段先强制 Go import 与 workspace manifest；TypeScript 源码级 ESLint 边界在首个客户端包计划中加入 |

## 7. 验证证据清单

执行完成后必须保存或在交付报告中给出：

- Go、Node、pnpm、Buf 的实际版本
- `pnpm install --frozen-lockfile` 结果
- `go test ./...` 与 `go vet ./...` 结果
- 边界检查测试数量和真实仓库内部依赖边数量
- Buf format、lint、generate、breaking 结果
- Turbo dry-run 结果摘要
- GitHub Actions 成功链接或未运行原因
- `git status --short` 空输出

不能把“命令未安装”“网络不可用”或“CI 尚未触发”报告为通过；这些情况必须明确标记为未验证并在进入下一子计划前补齐。
