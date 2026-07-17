# farmbot — D2R (modded, OFFLINE) autoplay — HANDOFF

_Rewritten 2026-07-17 ~06:20. Branch `offline-3.0.91636`. The previous version of this file was
substantially WRONG and self-contradicting; it has been replaced, not edited. Do not restore it._

---

## 0. READ THIS FIRST — the pathology that matters more than any fact below

This project has repeatedly written **"PROVEN" / "SOLVED"** on top of experiments that **could not
have proven anything**, then built on the words instead of the evidence. Three separate instances
were confirmed in one night:

1. **`-inputtest`** "proved 4/4" that D2R reads `GetPhysicalCursorPos` and not `GetCursorPos`. Those
   two exports are **THE SAME ADDRESS** on this build, so its "control" patched the same function as
   its experiment. The only variable that actually differed was a ×1.5 DPI scale.
2. **Waypoint travel** was declared SOLVED with a "verified live round-trip". The click formula was
   82px (exactly 2 rows) off, so every destination clicked **town's own row**. The one "verified"
   trip only ever exercised that accidental row. A `if row == 0 { clickY = 130 }` clamp was the
   tell — a formula force-fit to a single data point.
3. **Absolute cursor aim** was wrong by a factor of the DPI scale for the entire life of the project.
   Nothing caught it because **nothing here tests absolute aim**: force-move needs only a DIRECTION,
   and `hoverPickClick` SWEEPS ±80px until the game reports a hover, so it succeeds THROUGH any
   mapping error.

**The operative test, apply it to everything:** _what would this experiment have shown if the
hypothesis were FALSE?_ If there's no answer, it proved nothing. That is cheap and it would have
caught all three in five minutes.

Corollary: **claims verified by OBSERVING THE GAME survive; claims that are inferences do not.**
Movement, map alignment (SIZE_MATCH), the live room graph, Rabies combat — all measured against live
behaviour, all still good. The *explanations* attached to them were often wrong.

**SAFETY LOCK:** this targets a **single-player, OFFLINE, network-disconnected, modded** install only.
NEVER point it at a Battle.net-connected account — Warden will permanently ban it, and nothing here is
written with online play in mind. Modded ⇒ offline-only anyway.

---

## 1. State: what is TRUE right now

**Committed and verified live:**
- `06f272b` — **the random-crash bug.** `GetCursorPos` ≡ `GetPhysicalCursorPos` (one address;
  likewise `SetCursorPos`/`SetPhysicalCursorPos`). `MovePointer` installed the 16-byte physical stub
  then called `CursorPos()`, whose different 21-byte stub landed on the SAME address while
  `physStubActive` stayed true; the next `AimPhysical` spliced 8 coordinate bytes into the middle of
  the wrong stub. D2R calls that fn every frame → AV. It looked random because **the corrupted bytes
  ARE the cursor coordinates**.
- `c47d9a6` — **flag-trust class killed.** All `*StubActive` bools deleted; every `Override*`/
  `Restore*` re-reads the address (`stubIntact`) and reinstalls whole if it isn't ours. Before this,
  `-fixinput` (the REPAIR tool) would crash a running bot. Also: crash≠death (`StatusGameGone` asks
  the OS via `GetExitCodeProcess`, instead of inferring liveness from memory that stops existing at
  the same moment — every crash used to log as "dead"); Rabies-aware scored targeting; damage
  watchdog; town guard.
- `e3f430d` + `a732a51` — **WP row map measured, absolute aim fixed** (see §3).

**Instrumentation (all working, all validated):**
- `crashwatch.ps1` — run after any crash. Exit code + WER + dumps + TDR + orderly-vs-hard verdict.
- Security 4689 process-exit auditing (validated with a known exit 42 → `0x2a`).
- WER LocalDumps for `D2R.exe` (capped at 3) + **SilentProcessExit** (⚠️ see §5).

---

## 2. THE OPEN REGRESSION — fix this first, and MEASURE it

**Uncommitted in the working tree.** `hoverPickClick` now **fails to open the WP panel**, which it
did reliably all session. The cause is almost certainly the aim change in `a732a51`.

The contradiction, stated honestly:
- The new mapping (`physical = origin*scale + client`) is **proven for panels**: it predicted that
  aiming at (250,260)/1.5 = (167,173) would cancel the bug — it travelled 40→42 — and with the fix
  natural coords travel BOTH ways (40→42, 42→40, two different rows).
- But under the OLD mapping the game saw `client*1.5`, so hovering a thing at game-client x=426
  required aiming at 284 — a 142px gap the ±80 sweep should NOT have covered. Yet it demonstrably
  did. **So the model is right for the panel and not obviously right for the world, and there is no
  explanation covering both.**

**Do not guess.** Guessing produced three wrong root causes tonight. **Measure:** `hoverPickClick`
already finds the exact `(px,py)` where the game reports a hover on a known object. Log that against
the *predicted* `gameToScreen` point — the delta IS the world's true mapping. One small probe.

Also uncommitted: `MouseMoveClientSync` removed (its premise was wrong — rows ignored clicks because
of the aim, not the hover — and `SendMessage` BLOCKS on a frozen D2R, which is how farmbot hangs).

---

## 3. Waypoints — measured, with provenance

`-wpcal` = zero-click oracle: opens the panel, reports which row the blue compass lights for the
area she is STANDING IN. That (area → litRow) pair is ground truth.

`clickY = 178 + 41*row` — the SAME row-space `panelLitRow` scans. One map, not two.

| area | row | provenance |
|---|---|---|
| 40 Lut Gholein | 0 | **MEASURED** (`-wpcal` in town, lit r0 @ y=178, blueness 59.7 vs ≤1.5) + travelled to from 42 |
| 42 Dry Hills | 2 | **MEASURED** (`-wpcal` in Dry Hills, lit r2 @ y=260) + travelled to from 40 |
| 48 Sewers L2 | 1 | ASSUMED (vanilla order). Confirm via `-wpcal`. Do not promote on vibes. |
| 43 Far Oasis | 4 | ASSUMED (vanilla order). Confirm via `-wpcal`. |

Per-row blueness independently confirms the table: rows 0/1/2/4 read non-zero, rows 3/5/6/7 read
exactly 0.0 — precisely the UNLOCKED waypoints.

`-panelfn pos|all` DELETED (they switched between identical functions; `all` wrote the pos stub last
so it silently WAS pos). Only `phys` and `info` (the one genuinely distinct export) remain.

---

## 4. The crashes — NOT ours, and now instrumented

**Signature: exit `0xffffffff` (−1), no WER record, no LocalDump, no teardown.** Seen 3×.

**Why the black box looked empty — the null result WAS the evidence:** D2R **catches its own fault**,
shows the **Blizzard dialog**, and exits −1. Windows never sees an unhandled exception. (This was
only decodable because the user reported the dialog. Ask.)

**farmbot is exonerated for the game-side crash.** Cleanest instance: farmbot exited `0x0` at
06:16:28, D2R autosaved at 06:17:54, then crashed with nothing of ours attached or patched. An
earlier one (05:15) likewise ran 10 minutes after `-hookscan` verified all 8 input fns pristine.

**Ruled out:** cooling/thermals; the GPU (RX 7900 XTX, driver 32.0.31021.5001, `CM_PROB_NONE`); the
AMD overlay (`amdihk64.dll` IS injected but hooks D3D/DXGI only — all 8 input fns verified
byte-identical to pristine via `-hookscan`); the missing `Stash_PageNavigation` sprite (present in a
session that then ran 5.5 min — benign); DirectX/.NET/redists (would fail deterministically at
startup).

**THE ARTIFACTS — the first hard forensic evidence this project has ever had:**
```
C:\dev\koolo-build\crashdumps\D2R.exe-(PID-36840).dmp   8574 MB   death-moment (SilentProcessExit)
C:\dev\koolo-build\crashdumps\D2R.exe-(PID-15204).dmp   7234 MB   death-moment (SilentProcessExit)
C:\dev\koolo-build\crashdumps\D2R_hang_060742.dmp       4384 MB   manual, captured while hung
```
Captured via `MiniDumpWriteDump`/SilentProcessExit — **no debugger** (`DebugActiveProcess` KILLS D2R;
Arxan). Reading these is the best lead on the game-side crash.

---

## 5. ⚠️ DISK HAZARD — armed right now

**SilentProcessExit has no `DumpCount` cap** and writes **~7–8 GB per crash, forever**. `LocalDumps`
is capped at 3; this is a separate mechanism and is NOT. ~436 GB free as of writing. Either prune, or
disarm once the dumps are read:
```powershell
Remove-Item "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\SilentProcessExit\D2R.exe" -Recurse
Remove-Item "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options\D2R.exe" -Recurse
# and to disarm LocalDumps:
Remove-Item "HKLM:\SOFTWARE\Microsoft\Windows\Windows Error Reporting\LocalDumps\D2R.exe" -Recurse
auditpol /set /subcategory:"Process Termination" /success:disable
```

---

## 6. Build / run

- Game: a **frozen** D2R install (build 3.0.91636) + D2RMM "Reimagined". Frozen matters: every memory
  offset here is pinned to that build and will drift if the game updates. Client 853×480 logical.
  Bind **Force Move → E**, **Toggle Run/Walk → R**. Paths below are examples — use your own.
- Code: this repo; forked d2go alongside it via `go mod edit -replace`.
```bash
export PATH="/c/dev/tools/goroot/go/bin:$PATH"; export GOROOT="C:/dev/tools/goroot/go"; export GOPATH="C:/dev/tools/gopath"; export GOMODCACHE="C:/dev/tools/gopath/pkg/mod"; export CGO_ENABLED=1; export GOTMPDIR="C:/dev/koolo-build/gotmp"
cd /c/dev/koolo-build && go build -o farmbot.exe ./cmd/farmbot
cp farmbot.exe "E:\Games\Diablo II - Resurrected\farmbot.exe"   # build artifact, never committed
```
**Log to a file** — backgrounded runs lose stderr: `.\farmbot.exe ... *>&1 | Out-File "shots\x.log"`.

Farm loop: `.\farmbot.exe -seconds 300 -move e -runwalk r -nav -livegrid -loot -objects -radius 90 -diff normal`

Useful: `-wpcal` `-wpgoto <area>` `-uiclick x,y` (composes with `-wpcal` so the panel is PROVEN open)
`-aimsweep` `-hookscan` (read-only; also a slot-layout audit — NOTE it measures the gap to the
nearest SCANNED fn, not the nearest real export, so it can falsely report "ok") `-fixinput`
`-resumethreads` `-npcprobe <nameID>` `-hovergrid` `-shotdir` `-screenshot`.

---

## 7. Disciplines (hard-won)

- **NEVER force-kill farmbot while D2R is ALIVE** — it skips the input restore and breaks the human's
  mouse until `-fixinput` or a restart. (If D2R is already dead there is nothing to leak into; killing
  a hung farmbot is then safe.)
- **No debugger, ever.** Arxan kills D2R on `DebugActiveProcess`. RPM/WPM + system-DLL patching only.
- **Clear-before-interact** — a WP/chest/NPC under a monster pack cannot be hovered (bodies occlude).
- Distances are **SUBTILES** (5 per tile).
- While the bot runs you cannot click in D2R (it reads the injected cursor). Inherent.

---

## 8. Next, in order

1. **Measure the world aim mapping** → resolve §2. Nothing else is trustworthy until aim is.
2. **Read the dumps** (§4) — the game-side crash.
3. **Deletions** (each is a future self-deception): `-realcursor`/`-hwmove` (violate the plan's own
   "no SetCursorPos / no real HID" standing constraint and are reachable from `walkToHold`),
   `-wptown`/`-wpat`/`-hoverprobe` (superseded, and their coords disagree with `-wpgoto`), the dead
   CONFINEMENT BREAK-OUT block (gated `navi == nil` → dead whenever `-nav` is on), duplicate
   `carrotScreen`/`blueness` closures, `OverrideGetKeyboardState` (zero callers, writes to a live
   process).
4. **Injector mutex** — still outstanding and known. The signal handler runs `Unload()` on a goroutine
   while the loop writes; there is no synchronisation anywhere. The `stubIntact` read-back downgrades
   the race from "AV with nothing alive to heal it" to a benign reinstall, but it is still a data race
   (`-race` will flag it). Needs unexported-locked-internals across ~10 methods.
5. **Docs** — `CAMPAIGN_PLAN.md` still says M1 is "IN PROGRESS / the current blocker" and asserts
   `AvailableWaypoints` "works on this build" (d2go's own source says it is only valid while the panel
   is open, for the selected tab; `HANDOFF` said FLAKY; nothing gates on it — `PlayerUnit.Area` is the
   reliable oracle). `MORNING_REPORT.md` recommends the Interception driver while `CAMPAIGN_PLAN.md`
   lists "No Interception driver" under **never violate**. Archive `MORNING_REPORT.md`,
   `ALIGNMENT_REPORT.md`, `TRAVERSAL_REPORT.md`, `d2ralignment.md` → `docs/archive/` (they are
   outbound advisor QUESTIONS about problems now solved — provenance, not guidance).

## 9. Known-bad, not yet fixed (from two audits, full detail in git log)

`hoverPickClick` returns true in three ways, only one meaningful (it checks the ground-item list, so
for a WP/chest/NPC `still` is false on the first poll → instant true) — this makes `-wpaim`
unfalsifiable. `interactNPC` returns true on hovering ANY UnitType==5 (her own wolves 421/424/425,
merc 338) and never checks a menu opened. `-tp` opens a portal and never walks into it — the whole
emergency-escape feature is a no-op. `-objects` logs success for chests it never opened and
blacklists them BEFORE the click. Chicken can't fire in time (a tick can block ~20s inside
`hoverPickClick` with no HP read). `main.go:34-37` claims "MOD-AGNOSTIC, ZERO vanilla assumptions"
while enemy filtering runs through `IsGoodNPC()`'s hardcoded vanilla ID list. Probes can't use the
Navigator because `navi` is constructed inside `if *nav` AFTER every probe's early return — hoist it
and add one `approach()` helper (fixes npcprobe oscillation, the goto zero-motion wedge, and the
pen-in bug at once). Zero persistence: a crash loses all run state; `-wpgoto` already demonstrates
the durable pattern (verify-then-act keyed on `PlayerUnit.Area`, which survives any crash).

## 10. Further detail
Chronological findings live in the maintainer's local notes, outside this repo. Everything
load-bearing has been folded into this file; treat any older "PROVEN" claim you find elsewhere with
§0 in mind.
