<#
.SYNOPSIS
  Starts Online Boutique with BranchBox attached and builds the CLI.

.EXAMPLE
  .\scripts\up.ps1                 # baseline + BranchBox
  .\scripts\up.ps1 -BaselineOnly   # plain Online Boutique, no BranchBox
#>
param([switch]$BaselineOnly)
# Docker writes progress to stderr; rely on exit codes, not error records.
$ErrorActionPreference = 'Continue'

$root = Split-Path -Parent $PSScriptRoot
$project = Join-Path $root 'projects\online-boutique'
$files = @('-f', (Join-Path $project 'docker-compose.yml'))
if (-not $BaselineOnly) { $files += @('-f', (Join-Path $project 'docker-compose.branchbox.yml')) }

# Refuse to expose the controller (which drives Docker) without a token.
$envFile = Join-Path $project '.env'
if (Test-Path $envFile) {
    $settings = Get-Content $envFile
    if (($settings -match '^BB_BIND_ADDRESS=0\.0\.0\.0') -and -not ($settings -match '^BB_API_TOKEN=..')) {
        throw "BB_BIND_ADDRESS=0.0.0.0 requires BB_API_TOKEN in $envFile"
    }
}

docker compose @files up -d --build
if ($LASTEXITCODE -ne 0) { throw 'docker compose up failed' }

if ($BaselineOnly) {
    Write-Host "`nOnline Boutique baseline: http://localhost:8081"
    return
}

Push-Location $root
try {
    go build -o bin\branchbox.exe .\cmd\branchbox
    if ($LASTEXITCODE -ne 0) { throw 'building the CLI failed' }
} finally { Pop-Location }

Write-Host @"

BranchBox is running.
  Shop (baseline, via edge)   http://localhost:8080
  Shop (bypassing the edge)   http://localhost:8081
  Controller API              http://localhost:8090
  CLI                         $root\bin\branchbox.exe status

Create a sandbox:
  .\bin\branchbox.exe sandbox create --service productcatalogservice --branch feature/diwali-sale --id diwali
"@
