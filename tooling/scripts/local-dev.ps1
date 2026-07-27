[CmdletBinding()]
param(
  [ValidateSet("migrate", "api", "edge", "worker", "realtime", "all")]
  [string]$Command = "all"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# The root-local env file is intentionally ignored; this script supplies only reproducible wiring around it.
$root = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$envFile = Join-Path $root ".env.local"

if (-not (Test-Path -LiteralPath $envFile -PathType Leaf)) {
  throw "Missing local development configuration: $envFile"
}

Get-Content -LiteralPath $envFile | ForEach-Object {
  $line = $_.Trim()
  if ($line.Length -eq 0 -or $line.StartsWith("#")) {
    return
  }
  $separator = $line.IndexOf("=")
  if ($separator -le 0) {
    throw "Invalid local environment entry."
  }
  $name = $line.Substring(0, $separator).Trim()
  $value = $line.Substring($separator + 1)
  if ($name -notmatch "^[A-Z][A-Z0-9_]*$") {
    throw "Invalid local environment variable name."
  }
  [Environment]::SetEnvironmentVariable($name, $value, "Process")
}

$secretDirectory = Join-Path $root ".local-secrets"
& node (Join-Path $root "tooling\scripts\generate-local-keyrings.mjs") $secretDirectory
if ($LASTEXITCODE -ne 0) {
  throw "Generate local keyrings failed."
}

$keyringFiles = @{
  GAME_NIGHT_PII_KEYRING_FILE = "pii-keyring.json"
  GAME_NIGHT_TOTP_KEYRING_FILE = "totp-keyring.json"
  GAME_NIGHT_RESULT_ENVELOPE_KEYRING_FILE = "result-envelope-keyring.json"
  GAME_NIGHT_DEVICE_KEYRING_FILE = "device-keyring.json"
  GAME_NIGHT_RATE_LIMIT_KEYRING_FILE = "rate-limit-keyring.json"
  GAME_NIGHT_USER_CHALLENGE_KEYRING_FILE = "user-challenge-keyring.json"
  GAME_NIGHT_ADMIN_CHALLENGE_KEYRING_FILE = "admin-challenge-keyring.json"
  GAME_NIGHT_ADMIN_SESSION_KEYRING_FILE = "admin-session-keyring.json"
  GAME_NIGHT_ADMIN_CURSOR_KEYRING_FILE = "admin-cursor-keyring.json"
  GAME_NIGHT_AUDIT_KEYRING_FILE = "audit-keyring.json"
}

foreach ($entry in $keyringFiles.GetEnumerator()) {
  [Environment]::SetEnvironmentVariable($entry.Key, (Join-Path $secretDirectory $entry.Value), "Process")
}

$checkpointDirectory = Join-Path $root "logs\audit-checkpoints"
New-Item -ItemType Directory -Force -Path $checkpointDirectory | Out-Null

$runtimeEnvironment = @{
  GAME_NIGHT_CHECKPOINT_SINK = "local"
  GAME_NIGHT_CHECKPOINT_LOCAL_DIRECTORY = $checkpointDirectory
  GAME_NIGHT_API_LISTEN_ADDRESS = "127.0.0.1:8081"
  GAME_NIGHT_API_INSTANCE_ID = "local-api"
  GAME_NIGHT_ADMIN_HEARTBEAT_URL = "http://127.0.0.1:8081/internal/admin/operations/heartbeat"
  GAME_NIGHT_ADMIN_HEARTBEAT_TOKEN = "local-admin-heartbeat-token-1234567890"
  GAME_NIGHT_API_REALTIME_BOOTSTRAP_URL = "http://127.0.0.1:8091"
  GAME_NIGHT_API_REALTIME_PEER_URLS = "http://127.0.0.1:8091"
  GAME_NIGHT_EDGE_LISTEN_ADDRESS = "127.0.0.1:8080"
  GAME_NIGHT_EDGE_API_UPSTREAM_URL = "http://127.0.0.1:8081"
  GAME_NIGHT_EDGE_REALTIME_UPSTREAM_URL = "http://127.0.0.1:8090"
  GAME_NIGHT_EDGE_USER_STATIC_DIRECTORY = (Join-Path $root "apps\web\dist")
  GAME_NIGHT_EDGE_ADMIN_STATIC_DIRECTORY = (Join-Path $root "apps\admin\dist")
  GAME_NIGHT_EDGE_USER_HOSTS = "localhost:8080,127.0.0.1:8080"
  GAME_NIGHT_EDGE_ADMIN_HOSTS = "admin.localhost:8080"
  GAME_NIGHT_EDGE_TRUSTED_PROXY_CIDRS = "127.0.0.1/32,::1/128"
  GAME_NIGHT_WORKER_INSTANCE_ID = "local-worker"
}

foreach ($entry in $runtimeEnvironment.GetEnumerator()) {
  [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, "Process")
}

$logDirectory = Join-Path $root "logs"
New-Item -ItemType Directory -Force -Path $logDirectory | Out-Null

function Start-LocalProcess {
  param(
    [Parameter(Mandatory = $true)][string]$Name,
    [Parameter(Mandatory = $true)][int]$Port,
    [Parameter(Mandatory = $true)][string[]]$Arguments
  )

  if ($Port -gt 0 -and (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)) {
    Write-Output "$Name is already listening on $Port."
    return
  }

  $stdout = Join-Path $logDirectory "$Name.local.stdout.log"
  $stderr = Join-Path $logDirectory "$Name.local.stderr.log"
  Start-Process -FilePath "go" -ArgumentList $Arguments -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput $stdout -RedirectStandardError $stderr
  Write-Output "Started $Name. Logs: $stdout"
}

if ($Command -eq "migrate" -or $Command -eq "all") {
  & go run ./apps/migrate up
  if ($LASTEXITCODE -ne 0) {
    throw "Local migration failed."
  }
}

switch ($Command) {
  "api" { Start-LocalProcess -Name "api" -Port 8081 -Arguments @("run", "./apps/api") }
  "edge" { Start-LocalProcess -Name "edge" -Port 8080 -Arguments @("run", "./apps/edge") }
  "worker" { Start-LocalProcess -Name "worker" -Port 0 -Arguments @("run", "./apps/worker") }
  "realtime" { Start-LocalProcess -Name "realtime" -Port 8090 -Arguments @("run", "./apps/realtime") }
  "all" {
    Start-LocalProcess -Name "realtime" -Port 8090 -Arguments @("run", "./apps/realtime")
    Start-LocalProcess -Name "api" -Port 8081 -Arguments @("run", "./apps/api")
    Start-LocalProcess -Name "edge" -Port 8080 -Arguments @("run", "./apps/edge")
    Start-LocalProcess -Name "worker" -Port 0 -Arguments @("run", "./apps/worker")
  }
}
