# URGENT HANDOFF — the recovery wall (2026-07-19 ~13:25)

## READ FIRST: her exact state right now
- **She is SAFE, not lost.** D2R is sitting on the in-game PAUSE MENU
  (Save and Exit visible). The pause menu FREEZES offline D2R — she
  cannot die, nothing is corrupting. She will sit here, frozen and
  alive, until you act. azbot is NOT running (I stopped it).
- **She is naked (weapon=none), level 9, her corpse is in Cold Plains.**
  She died there legitimately (thin belt in tougher territory), and the
  automated gear-recovery could not complete — see the wall below.

## THE WALL (why I stopped and left her paused)
The RELOG recovery (Save+Exit → rejoin → corpse materializes in town →
reclaim) is broken by a MENU-CLICK COORDINATE that I could not fix blind:
- Relog's `RealEsc` opens the pause menu correctly. But the Save+Exit
  CLICK (`relogExitBtnX/Y = 958,822` in `internal/azbot/activity/relog.go`,
  clicked via `RealMenuClick` → `sx = shotX/panelScale + WindowLeftX`)
  lands at screen ~(766,646) while the button is at screen ~(933,808).
  It misses, the world never unloads, relog abandons. Six+ abandons.
- panelScale = -dpiscale (default 1.25). But the shot.exe screenshot is
  2304x1260 while the client rect measured 1536x840 — a **1.5x** ratio,
  not 1.25. The menu renders in a space where the 1.25 assumption is
  wrong. IN-GAME panels (vendor/inventory via `UIClick`, a DIFFERENT
  transform) work fine — gold moved, she shopped. Only the menu path is
  off.
- I could not verify a corrected coordinate remotely: my only test
  avenue (OS `mouse_event` via PowerShell) does NOT reach the D2R menu
  — it ignores synthetic clicks the same way the panels do, responding
  only to the bot's interception-level `SendClickRealScreen`. So I
  cannot calibrate it without your display.

## THE FIX YOU CAN DO IN 30 SECONDS
1. Click **Save and Exit** yourself (it's on screen). → char select.
2. Rejoin the character (Enter, or click Play). The corpse materializes
   in town (softcore).
3. Launch azbot: from `C:\dev\koolo-build`,
   `./azbot.exe -goal rampage -meleekey f2 -rangedkey f1 -seconds 13000`
   It will reclaim the town corpse, rearm, and march. (If a panel is up
   at launch, the startup hygiene ESC handles it.)

## THE DURABLE FIX (for the relog coordinate)
Recalibrate `relogExitBtnX/Y` in `internal/azbot/activity/relog.go`
against YOUR display. With the pause menu open, note where Save and Exit
actually is, and solve `shotX = (screenX - WindowLeftX) * panelScale`.
My computed candidate was ~(1166,1025) but I could not verify it — do
not trust it without a test. The `relogPlayBtnX/Y` (char-select Play)
likely needs the same recalibration. Consider deriving menu clicks from
the SAME transform as the working `UIClick` instead of the divergent
`RealMenuClick` formula — that mismatch is the likely root.

## WHAT IS SOLID (all pushed, laptop-3.2, ~50 commits today)
Everything EXCEPT recovery works and is battle-tested tonight:
- **Trade FIXED**: the "wedged Akara / poisoned world" was a 35-min
  phantom — the bot was blind to its own OPEN shop (this mod opens the
  vendor via a dialog that Home/Down/Enter steers to Trade; vendor stock
  is the honest oracle). She buys and sells, gold moves. (WARNING 8a.)
- **Combat**: the blood oracle (P-2.0, time-to-die), flee fatigue
  (P-2.11), cornered verdict (P-1.14 Stand), bow-only (P-1.7, javelin
  dropped), skill-on-every-shot (P-1.13), forced march radius 10,
  stand-and-loose, lone-tooth. She crossed to Cold Plains and leveled
  7→9 on this.
- **Spend hot-spin FIXED** (88k grants/min → retires cleanly). Poison-
  relog backstop DEFANGED (was wrong-theory code hijacking the march).
- The reviewer's fragility fixes (belief re-arming per world, breakout
  cast reset, demoted-bow revival, naked-guard gaps).

## PILOT'S LESSONS (in the spec, WARNING 8a, in blood)
- A screenshot shows a STATE, not a MECHANISM. I chased a log's "ghost"
  narrative for 30 min, then broke a working shop chasing a screenshot's
  end-state. LOOK early, but confirm the FIX moved the needle before
  believing it; revert fast when it didn't.
- Over-engineered guards broke common cases to catch rare ones (the
  worldFrozen shadow test stranded her naked; the trade-ignores-menu
  froze the shop). Prefer the proven simple path.
- **A binary swap resets per-process state; a swap during recovery can
  starve the very cure.** When she's stuck, slow down.

## OPEN, in priority
1. **Relog menu coordinate** (above) — blocks ALL death recovery.
2. Skill-spend needs F1's skill ID (calibration can't capture a
   pre-selected key's skill — the no-flip blind spot). Points bank safe.
3. Class profiles (spec section 4, design done, code pending).
4. The unique bow's whereabouts (bag says gone — stash or fenced?).
