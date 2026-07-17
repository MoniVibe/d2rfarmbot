# crashwatch.ps1 -- the D2R "black box" reader.
#
# Run this AFTER D2R dies. It answers the one question that splits the suspects:
# did the game FAULT, or did it CHOOSE to quit?
#
#   Exit 0xC0000005 -> access violation (bad memory access: injector race, overlay hook, driver)
#   Exit 0xC0000409 -> stack buffer overrun / __fastfail (anti-tamper often uses this)
#   Exit 0x0        -> clean, deliberate exit (NOT a crash -- game decided to quit)
#   no 4689 at all  -> killed externally, or audit policy got reset
#
# Sources, cheapest first. None of these attach a debugger (Arxan kills D2R on attach).
param([int]$Minutes = 30)

$since = (Get-Date).AddMinutes(-$Minutes)
Write-Host "=== D2R process exits (Security 4689) — last $Minutes min ===" -ForegroundColor Cyan
Get-WinEvent -FilterHashtable @{LogName='Security'; Id=4689; StartTime=$since} -ErrorAction SilentlyContinue |
  ForEach-Object {
    $d = ([xml]$_.ToXml()).Event.EventData.Data
    [pscustomobject]@{ Time=$_.TimeCreated; Proc=Split-Path $d[6].'#text' -Leaf; Status=$d[4].'#text' }
  } | Where-Object { $_.Proc -match 'D2R' } | Format-Table -AutoSize

Write-Host "=== Windows fault records (Application Error / WER) ===" -ForegroundColor Cyan
Get-WinEvent -FilterHashtable @{LogName='Application'; ProviderName=@('Application Error','Windows Error Reporting','Application Hang'); StartTime=$since} -ErrorAction SilentlyContinue |
  Where-Object { $_.Message -match 'D2R|Diablo' } |
  ForEach-Object { "--- $($_.TimeCreated) [$($_.ProviderName)]"; ($_.Message -split "`n" | Select-Object -First 6) -join "`n" }

Write-Host "=== Crash dumps captured (WER LocalDumps) ===" -ForegroundColor Cyan
Get-ChildItem "C:\dev\koolo-build\crashdumps" -ErrorAction SilentlyContinue |
  Select-Object Name, @{n='MB';e={[math]::Round($_.Length/1MB)}}, LastWriteTime | Format-Table -AutoSize

Write-Host "=== Display driver resets (TDR) ===" -ForegroundColor Cyan
Get-WinEvent -FilterHashtable @{LogName='System'; StartTime=$since} -ErrorAction SilentlyContinue |
  Where-Object { $_.Id -in @(4101,141,117) } |
  Select-Object TimeCreated, Id, @{n='Msg';e={($_.Message -split "`n")[0]}} | Format-Table -AutoSize -Wrap

Write-Host "=== blz-log: last non-texture lines (game's own trail) ===" -ForegroundColor Cyan
# An ORDERLY death leaves a long texture-teardown tail; a HARD death just stops mid-line.
$log = "E:\Games\Diablo II - Resurrected\blz-log.txt"
if (Test-Path $log) {
  $all = Get-Content $log
  Write-Host ("last line written: " + $all[-1])
  $all | Where-Object { $_ -notmatch '\[Texture/' } | Select-Object -Last 12
  $tail = ($all | Select-Object -Last 30 | Where-Object { $_ -match '\[Texture/' }).Count
  Write-Host ("texture-teardown lines in last 30: $tail  -> " + $(if ($tail -gt 10) { "ORDERLY shutdown (game chose to exit)" } else { "HARD death (no teardown)" })) -ForegroundColor Yellow
}
