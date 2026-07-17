# ONE koolo test-cycle, run elevated by the scheduled task "koolo_autorun".
# Claude triggers this via: schtasks /run /tn koolo_autorun  (no elevation needed to trigger).
$ErrorActionPreference = 'SilentlyContinue'
$install = "E:\Games\Diablo II - Resurrected"
$err = "$install\logs\koolo_stderr.txt"
$out = "$install\logs\koolo_stdout.txt"

# clean slate (elevated task can kill an elevated koolo/D2R)
Get-Process koolo,D2R,koolo-map,handle64 -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep 2
Remove-Item $err,$out -ErrorAction SilentlyContinue

# launch koolo elevated (inherits the task's admin token), capture stderr
Start-Process -FilePath "$install\koolo.exe" -WorkingDirectory $install -RedirectStandardError $err -RedirectStandardOutput $out
Start-Sleep 8

# start the Main supervisor and let it attempt game-create + run
try { Invoke-WebRequest "http://localhost:8087/start?characterName=Main" -UseBasicParsing -TimeoutSec 8 | Out-Null } catch {}
Start-Sleep 75
try { Invoke-WebRequest "http://localhost:8087/stop?characterName=Main" -UseBasicParsing -TimeoutSec 8 | Out-Null } catch {}
Start-Sleep 3

# tear down so the next cycle starts clean
Get-Process koolo,D2R,koolo-map,handle64 -ErrorAction SilentlyContinue | Stop-Process -Force

# marker so Claude knows the cycle finished
Set-Content -Path "$install\logs\autorun_done.txt" -Value (Get-Date -Format o)
