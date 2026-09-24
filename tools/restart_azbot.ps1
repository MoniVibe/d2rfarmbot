# Restart azbot on a freshly built binary: stop, heal D2R input, swap, relaunch.
# Usage: pwsh tools\restart_azbot.ps1 -Tag f [-Seconds 7200] [-Goal campaign] [-Classic] [-Janitor]
param([Parameter(Mandatory)][string]$Tag, [int]$Seconds = 7200, [string]$Goal = "campaign", [switch]$Force, [switch]$Classic, [switch]$Janitor)
Set-Location (Split-Path $PSScriptRoot -Parent)
# NEVER stop the bot outside town (2026-09-23: a restart mid-fight left Fableboi
# standing undriven in a pack; he died). Allowed: in town, or NO living monster within
# 40 tiles (a restart takes ~15s). -Force only when the owner says so.
$look = .\build\look.exe -r 0 -n 0 2>$null
$where = $look | Select-Object -First 1
$quiet = ($look -join "`n") -match "monsters within 40: 0 alive"
if (-not $Force -and (Get-Process azbot -ErrorAction SilentlyContinue) -and ($where -notmatch "town=true") -and -not $quiet) {
  "REFUSED: not in town and monsters are near ($where). Wait for town / a quiet field, or pass -Force."
  exit 1
}
Get-Process azbot -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 500
.\farmbot.exe -fixinput 2>&1 | Select-String 'fixinput: done' | ForEach-Object { 'input healed' }
if (Test-Path build\azbot.exe.new) { Move-Item -Force build\azbot.exe.new build\azbot.exe }
# Deliberate navigation (route intent + seam hysteresis + escalation ladder) is ON
# unless -Classic. The child process inherits this.
if ($Classic) { Remove-Item Env:AZBOT_DELIBERATE -ErrorAction SilentlyContinue } else { $env:AZBOT_DELIBERATE = "1" }
# The v2 janitor + gate (docs/AZBOT_V2.md step 6) is OFF unless -Janitor: the
# executive gates every Step on the screen oracle and closes foreign panels by sight.
if ($Janitor) { $env:AZBOT_JANITOR = "1" } else { Remove-Item Env:AZBOT_JANITOR -ErrorAction SilentlyContinue }
$log = "logs\run_$(Get-Date -Format yyyy-MM-dd)$Tag.out"
Start-Process -FilePath .\build\azbot.exe -ArgumentList "-goal $Goal -seconds $Seconds" `
  -RedirectStandardOutput $log -RedirectStandardError "$log.err" -WindowStyle Hidden
"launched -> $log"
