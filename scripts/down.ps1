<#
.SYNOPSIS
  Deletes every sandbox, then stops Online Boutique and BranchBox.

.EXAMPLE
  .\scripts\down.ps1          # keep controller state and the repository mirror
  .\scripts\down.ps1 -Purge   # also delete BranchBox's data volume
#>
param([switch]$Purge)
# Docker writes progress to stderr; rely on exit codes, not error records.
$ErrorActionPreference = 'Continue'

$root = Split-Path -Parent $PSScriptRoot
$project = Join-Path $root 'projects\online-boutique'
$cli = Join-Path $root 'bin\branchbox.exe'

# Sandbox containers are not part of the compose project; remove them first
# so the shared network can be torn down.
if (Test-Path $cli) {
    & $cli sandbox delete --all 2>$null
}
$leftovers = docker ps -aq --filter label=branchbox.managed=true
if ($leftovers) { docker rm -f $leftovers | Out-Null }

$composeArgs = @('-f', (Join-Path $project 'docker-compose.yml'), '-f', (Join-Path $project 'docker-compose.branchbox.yml'), 'down', '--remove-orphans')
if ($Purge) { $composeArgs += '--volumes' }
docker compose @composeArgs
