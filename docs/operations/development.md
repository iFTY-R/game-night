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
