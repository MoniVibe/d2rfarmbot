# Restart azbot on a freshly built binary: stop, heal D2R input, swap, relaunch.
# Usage: pwsh tools\restart_azbot.ps1 -Tag f [-Seconds 7200] [-Goal campaign] [-Classic] [-Janitor] [-Disengaged]
#
# HOW A RUN ENDS (relay R4: -seconds 240 once exited mid-fight in the Dry Hills
# and left him undriven). When -Seconds is spent - or the owner creates
# logs\stop.now, or presses Ctrl-C once in its console - azbot does NOT exit on
# the spot: its session enters WindDown (ses=WindDown on the state line). In town,
# or with no living monster within 40 tiles for 3s, it stops the motor and exits
# normally; otherwise it keeps fighting/drinking while a town-portal ride (the
# "recall" activity, Withdraw's TP road) bids over Fight, under Survive. Not safe
# 120s after the request: it DISENGAGES and holds, logging "WINDDOWN: could not
# reach safety" every 30s - F10 resumes the wind-down, or kill the process. It
# never exits while monsters are near. To stop a live run gently, prefer
#   New-Item logs\stop.now
# over this script's hard stop below (which still refuses outside town/quiet).
#
# -Disengaged starts the bot DISENGAGED (teaching mode, azbot -disengaged):
# perception, screen shadow, trace/state lines and the flight recorder run, but
# NO input is sent - no injector stubs, no calibration keys, no startup hygiene -
# until the owner presses F10 (the kill-switch) to engage. For hands-on censuses.
param([Parameter(Mandatory)][string]$Tag, [int]$Seconds = 7200, [string]$Goal = "campaign", [switch]$Force, [switch]$Classic, [switch]$Janitor, [switch]$Disengaged)
Set-Location (Split-Path $PSScriptRoot -Parent)
# NEVER stop the bot outside town (2026-09-23: a restart mid-fight left Fableboi
# standing undriven in a pack; he died). Allowed: in town, or NO living monster within
# 40 tiles (a restart takes ~15s). -Force only when the owner says so. This is the
# same rule azbot's own WindDown applies to its timer and logs\stop.now; the kill
# below is immediate, so this script keeps its own check.
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
# A stale stop request would wind the new run down on its first tick (azbot also
# removes one at startup, and says so).
Remove-Item logs\stop.now -ErrorAction SilentlyContinue
$log = "logs\run_$(Get-Date -Format yyyy-MM-dd)$Tag.out"
$azArgs = "-goal $Goal -seconds $Seconds"
if ($Disengaged) { $azArgs += " -disengaged" }
Start-Process -FilePath .\build\azbot.exe -ArgumentList $azArgs `
  -RedirectStandardOutput $log -RedirectStandardError "$log.err" -WindowStyle Hidden
if ($Disengaged) { "launched DISENGAGED -> $log (press F10 in game to engage)" } else { "launched -> $log" }
