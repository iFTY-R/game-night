# 本地开发

## 前置工具

- Go 1.26.4
- Node.js 24.18.0
- pnpm 11.13.1（通过 Corepack）
- Buf 1.66.0（协议变更需要）

Docker 在仓库基础阶段不是必需项。后续数据库、Redis、Testcontainers 和部署验证会使用 Docker。

### Windows 安装 Buf

Windows 环境缺少 Buf 时，使用固定版本安装，避免本地生成结果与 CI 基线不一致：

```powershell
$previousGoProxy = $env:GOPROXY
try {
  $env:GOPROXY = 'https://goproxy.cn,direct'
  go install github.com/bufbuild/buf/cmd/buf@v1.66.0
  if ($LASTEXITCODE -ne 0) { throw 'Buf 1.66.0 installation failed' }
} finally {
  $env:GOPROXY = $previousGoProxy
}

$goBin = (go env GOBIN).Trim()
if ($LASTEXITCODE -ne 0) { throw 'go env GOBIN failed' }
if ([string]::IsNullOrWhiteSpace($goBin)) {
  $goPath = (go env GOPATH).Trim()
  if ($LASTEXITCODE -ne 0) { throw 'go env GOPATH failed' }
  $goPathRoot = ($goPath -split [IO.Path]::PathSeparator)[0]
  if ([string]::IsNullOrWhiteSpace($goPathRoot)) { throw 'Go returned an empty GOPATH' }
  $goBin = Join-Path $goPathRoot 'bin'
}
if (-not (Test-Path -LiteralPath $goBin -PathType Container)) {
  throw "Go binary directory does not exist: $goBin"
}

$bufExe = Join-Path $goBin 'buf.exe'
if (-not (Test-Path -LiteralPath $bufExe -PathType Leaf)) {
  throw "Buf executable does not exist: $bufExe"
}
$bufVersion = (& $bufExe --version).Trim()
if ($LASTEXITCODE -ne 0 -or $bufVersion -ne '1.66.0') {
  throw "unexpected Buf version: $bufVersion"
}

$seenPathEntries = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
$pathEntries = foreach ($entry in @($goBin) + @($env:Path -split ';')) {
  $trimmed = $entry.Trim()
  if (-not [string]::IsNullOrWhiteSpace($trimmed) -and $seenPathEntries.Add($trimmed.TrimEnd('\', '/'))) {
    $trimmed
  }
}
$env:Path = $pathEntries -join ';'
$resolvedBuf = (Get-Command buf -CommandType Application -ErrorAction Stop).Source
if (-not [string]::Equals($resolvedBuf, $bufExe, [StringComparison]::OrdinalIgnoreCase)) {
  throw "PATH resolved an unexpected Buf executable: $resolvedBuf"
}
```

`GOBIN` 非空时是 `go install` 的显式输出目录；为空时 Go 使用首个 `GOPATH` 条目的 `bin` 子目录。上面的 PATH 修改会移除重复项并把实际安装目录放在当前 PowerShell 的首位，避免旧版 `buf` 抢先解析；需要跨终端使用时，将该目录加入用户 PATH。不要由仓库脚本静默安装或升级全局工具。

## 初始化

```powershell
corepack enable
pnpm install --frozen-lockfile
if ($LASTEXITCODE -ne 0) { throw 'frozen pnpm install failed' }
```

## Docker Compose 本地部署

默认 `deploy/docker-compose.yml` 使用一个 `game-night` 应用容器，并创建 PostgreSQL、Redis、MinIO 和必要的初始化容器。镜像内的 `serve-all` 在同一应用容器管理 edge、API、realtime 和 worker，对宿主机只发布 `8080`。

```powershell
Copy-Item deploy/.env.example deploy/.env
# 编辑 deploy/.env，替换所有 change-me 值并准备 deploy/secrets
Set-Location deploy

docker compose run --rm game-night migrate up
docker compose up -d
```

只运行一个应用容器并连接外部 PostgreSQL、Redis 和 S3 时，改用 `deploy/docker-compose.standalone.yml`。两种模式的应用拓扑相同，区别仅是依赖由 Compose 创建还是由外部提供；完整命令和变量说明见 [`deploy/README.md`](../../deploy/README.md)。

## 仓库验证

以下命令与根 `package.json` scripts 和 CI repository job 保持一致：

```powershell
$trackedGoFiles = @(git ls-files -- '*.go')
if ($LASTEXITCODE -ne 0) { throw 'listing tracked Go files failed' }
$formattingIssues = @()
foreach ($file in $trackedGoFiles) {
  $fileIssues = @(gofmt -l -- $file)
  if ($LASTEXITCODE -ne 0) { throw "gofmt failed for tracked file: $file" }
  $formattingIssues += $fileIssues
}
if ($formattingIssues.Count -ne 0) {
  $formattingIssues
  throw 'tracked Go formatting drift detected'
}

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
@(
  '@game-night/workspace-smoke#check',
  '@game-night/workspace-smoke#test',
  '@game-night/workspace-smoke#build'
) | ForEach-Object {
  if ($_ -notin $taskIds) { throw "missing Turbo task: $_" }
}
```

`pnpm run check` 会先运行仓库依赖边界检查，再运行 workspace check 任务；`pnpm test` 同时运行根 Go 测试和 workspace test 任务。

## Realtime 开发进程

Realtime 只加载 PostgreSQL、Redis、Origin/代理策略和独立内部凭据，不加载设备、管理员、PII 或审计密钥。开发环境使用占位凭据启动：

```powershell
$env:GAME_NIGHT_ENVIRONMENT = 'development'
$env:GAME_NIGHT_DATABASE_URL = 'postgresql://runtime:replace-me@127.0.0.1:5432/game_night?sslmode=disable'
$env:GAME_NIGHT_DATABASE_SCHEMA = 'public'
$env:GAME_NIGHT_REDIS_URL = 'redis://:replace-me@127.0.0.1:6379/0'
$env:GAME_NIGHT_REDIS_KEY_PREFIX = 'game-night:dev:'
$env:GAME_NIGHT_USER_ORIGINS = 'http://127.0.0.1:5173'
$env:GAME_NIGHT_ADMIN_ORIGINS = 'http://127.0.0.1:5174'
$env:GAME_NIGHT_TRUSTED_PROXY_CIDRS = '127.0.0.1/32,::1/128'
$env:GAME_NIGHT_REALTIME_INTERNAL_TOKEN = ('t' * 32)
$env:GAME_NIGHT_REALTIME_INSTANCE_ID = 'realtime-local'
$env:GAME_NIGHT_REALTIME_ADVERTISED_URL = 'http://127.0.0.1:8091'
go run ./apps/realtime
```

公网监听默认 `:8090`，私网 owner RPC 默认 `:8091`。部署编排中由 edge 将精确 `/realtime/game` 路径转发给 realtime；外部 Nginx 只反代应用入口 `127.0.0.1:8080`，不直接配置 realtime upstream，`:8091` 绝不能接入公网代理。

权威 timer 默认每 `250ms` 扫描 128 条候选、单条超时 `5s`，可通过 `GAME_NIGHT_REALTIME_TIMER_SCAN_INTERVAL`、`GAME_NIGHT_REALTIME_TIMER_BATCH_SIZE`、`GAME_NIGHT_REALTIME_TIMER_OPERATION_TIMEOUT` 调整。durable fanout consumer 默认使用 `15s` lease、每 `250ms` 读取 128 条，可通过 `GAME_NIGHT_REALTIME_OUTBOX_LEASE_DURATION`、`GAME_NIGHT_REALTIME_OUTBOX_POLL_INTERVAL`、`GAME_NIGHT_REALTIME_OUTBOX_BATCH_SIZE` 调整；所有值均受进程配置硬上限约束。

## 协议验证

smoke 模块用于证明固定的 Go、Connect-Go 和 TypeScript 插件可以重复生成并编译：

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

buf lint tooling/testdata/proto-breaking
if ($LASTEXITCODE -ne 0) { throw 'breaking fixture lint failed' }
buf build tooling/testdata/proto-breaking -o NUL
if ($LASTEXITCODE -ne 0) { throw 'breaking fixture build failed' }
$breakingOutput = (buf breaking tooling/testdata/proto-breaking --against tooling/testdata/proto-baseline 2>&1 | Out-String).Trim()
$breakingStatus = $LASTEXITCODE
if ($breakingStatus -eq 0) { throw 'breaking fixture unexpectedly passed' }
if (-not $breakingOutput.Contains('changed type from "string" to "int64"')) {
  throw 'breaking fixture failed without the expected field type diagnostic'
}

git diff --exit-code -- tooling/testdata/proto/gen
if ($LASTEXITCODE -ne 0) { throw 'tracked generated smoke drift detected' }
if (git status --porcelain -- tooling/testdata/proto/gen) {
  throw 'generated smoke drift detected'
}
```

生产协议出现后，breaking check 优先使用 GitHub PR 提供的 base ref，本地回退到 `origin/master`。base ref 不存在或 Git 树读取失败必须报错；只有有效 base ref 确实没有生产 Proto 时，才能明确报告 `SKIPPED: no production Proto baseline`：

```powershell
$baseRef = if ([string]::IsNullOrWhiteSpace($env:GITHUB_BASE_REF)) {
  'origin/master'
} else {
  "origin/$($env:GITHUB_BASE_REF)"
}
git rev-parse --verify "${baseRef}^{commit}" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "production base ref does not exist: $baseRef" }
$baseTree = @(git ls-tree -r --name-only $baseRef -- contracts games)
if ($LASTEXITCODE -ne 0) { throw "reading production base tree failed: $baseRef" }
$baseProto = @($baseTree | Where-Object { $_ -like '*.proto' })
if ($baseProto.Count -eq 0) {
  Write-Output 'SKIPPED: no production Proto baseline'
} else {
  buf breaking --against ".git#branch=$baseRef"
  if ($LASTEXITCODE -ne 0) { throw 'production breaking check failed' }
}
```

## 约束

- 不从 `.tmp/` 或外部参考仓库复制代码、架构或资产。
- 不手工修改 `pnpm-lock.yaml` 或 Buf 生成文件。
- 新包必须遵守各责任根 README 和边界检查器规则。
## 密钥文件权限

Keyring JSON 必须以只读普通文件挂载，不能使用符号链接。Unix 部署必须使用 `0400` 权限；Windows 部署除设置只读属性外，还必须通过 ACL 仅向服务身份授予读取权限，因为 Go 的跨平台文件模式接口无法区分 Windows ACL 的所有者和用户组。
