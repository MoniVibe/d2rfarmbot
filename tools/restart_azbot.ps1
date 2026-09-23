# Restart azbot on a freshly built binary: stop, heal D2R input, swap, relaunch.
# Usage: pwsh tools\restart_azbot.ps1 -Tag f [-Seconds 7200] [-Goal campaign]
param([Parameter(Mandatory)][string]$Tag, [int]$Seconds = 7200, [string]$Goal = "campaign")
Set-Location (Split-Path $PSScriptRoot -Parent)
Get-Process azbot -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 500
.\farmbot.exe -fixinput 2>&1 | Select-String 'fixinput: done' | ForEach-Object { 'input healed' }
if (Test-Path build\azbot.exe.new) { Move-Item -Force build\azbot.exe.new build\azbot.exe }
$log = "logs\run_$(Get-Date -Format yyyy-MM-dd)$Tag.out"
Start-Process -FilePath .\build\azbot.exe -ArgumentList "-goal $Goal -seconds $Seconds" `
  -RedirectStandardOutput $log -RedirectStandardError "$log.err" -WindowStyle Hidden
"launched -> $log"
