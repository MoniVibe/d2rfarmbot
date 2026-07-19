# Handoff — azbot Act 1 rampage session (2026-07-19, ~01:30–03:30)

## Where she is
- **Mamazon, fresh amazon, level 5**, seed pool world (seed re-rolls per game!), Blood Moor.
- Run 14 live at handoff: `./azbot.exe -goal rampage -meleekey f2 -rangedkey f1 -seconds 7200`
  (owner-declared build: **Jab=F2, MagicArrow=F1**; F3=Identify tome; TP tome binding
  DISPLACED by the Jab rebind — verify where tome 220 lives now, rebind if unbound).
- Economy ALIVE: first fully self-directed town trip proven 03:12 — Fence sold junk
  (gold 0→151), Restock bought potions (151→92), Advance marched out. ~92g at handoff.
- Kill-switch F10 is a REAL two-way toggle now (tap-proof). `farmbot.exe -fixinput`
  heals D2R input after any unclean bot kill — ALWAYS run it after taskkill of an
  ENGAGED bot, from `C:\dev\koolo-build` (a wrong-cwd relaunch once left patches in
  and killed the death recorder — cd first, always).

## Built this session (commits 528c2e8 → 3851b45+, all pushed to public/laptop-3.2)
1. **Advance** — Act 1 itinerary march (`-goal rampage`), level gates, ClassTravel 0.2.
   Topology from koolo-map (FetchMapData at attach), geometry ONLY from live truth:
   koolo-map world placement LIES on this mod (BM gate "east", really west).
2. **Live border oracle** — `MemoryReader.AdjacentLevelRooms()` (cross-level rooms,
   world subtiles = Rect×5); validated against the hand road to the exact 40×40 room.
3. **Cartographer** — every crossing writes both sides of the door as WAL facts,
   seed-namespaced (`BorderKey(seed,from,to)`). Seed pool observed: 466817790 ↔ 1502982702.
4. **Relog ritual** — exit+re-enter puts the corpse IN TOWN (proven: 1 tile from spawn,
   3s). Relog activity (Recover 0.95) + menu constants: Save+Exit shot(958,822),
   Play shot(922,822); **menu coord law**: logical = shot/1.25 + (WindowLeftX, WindowTopY);
   menus need OS-level input (SendInput; Interception driver absent).
5. **Reclaim** — corpse runs: journey with Arrive=2 (fence-proof: proximity ≠
   reachability), guard-lure (run away, swarm follows HER, loop back), blind-click only
   on unguarded body, honest give-up. Corpse latch in percept survives room unloading.
6. **Dodge** — missile oracle (d2go `Missiles()`, unit table type 3); velocity measured
   across snapshots; only CLOSING missiles threaten (own arrows fly away; born-near-me
   tracks excluded). Sidestep perpendicular, away from teeth. Fired live on moor archers.
7. **Fence** — sells inventory junk at Akara (ctrl-click quick-sell `SellClick`,
   amnesty after). Junk audit in percept: keep 1 TP tome, 1 ID tome, potions, magic+.
8. **Watched stride hold** — strides re-read the world every 120ms; abort on damage
   (ResDone, NOT blocked — no slide retries), death, early arrival. Was THE sluggishness.
9. **Optimistic pathing** — unknown rooms = LowPriority (plannable at penalty), real
   walls hard. Wall-slide (±45°) on planless strides. LoS oracle `losClear` — arrows
   refuse known walls, she repositions ("shoot through walls" fix). Loot journeys
   through doors (house-flicker fix).
10. **Arbiter rule zero** — a grant lives only while its demand persists (dead Breakout
    held the death screen forever). Focus gating — **offline D2R PAUSES when unfocused**;
    executive stands by, watchdog reset on refocus. Watchdog: standing volley = work;
    dodge blips ≠ thrash. Death reports (12s trail) on every death.
11. **Injector Unload keeps the handle + resets isLoaded** (the one-way-F10 bug);
    `Close()` only at teardown.
12. **Capability**: selection-flip = ownership proof (mod grants skills at level 0 —
    points guard was wrong); flinch audit demotes fakes (15 volleys, 0 flinches);
    calibration ends on a combat selection (probing used to leave IDENTIFY armed);
    probes f1–f8; `-meleekey/-rangedkey` owner overrides. **KeyBindings memory is DEAD
    on this repack — the bot CANNOT read binds; it probes or is told.**

## Open issues / next steps
- **Hotkey self-provisioning** (owner's ask): bot should ASSIGN a key to an unbound
  known skill via the skill-select popup (proven popup coords in memory notes) —
  build as a `-bindtest` drill. Then binding changes need no human.
- **Per-weapon-set selections**: D2R keeps a right-skill PER SET; javelin set held a
  stale Identify. Declared keys mostly fix it (strike presses key each time), but a
  set-aware capability (calibrate per WeaponKind) is the clean design.
- **Verify TP tome binding** after the F2→Jab rebind (breakout needs it to cast exits).
- Underground Passage leg (first ENTRANCE hop) untested — the search/entrance ritual
  is written but undrilled. Jail/Catacombs interiors untested.
- Watchdog reclaim-orbit tuning; Explore is still the dumb heading-walker (atlas
  frontier is the M8 answer).
- Gamble activity for `-goal gamble` still unbuilt (oracle drilled, loop proven).
- Chests aren't targeted at all (owner saw item-in-house flicker; chest OPENING is
  a separate feature).

## Cadence used tonight
Monitor via `tail -F logs/rampageN.log | grep -E "cartographer|DEATH REPORT|grant to=|MOTOR|..."`.
Swap ritual: build `azbot.exe.new` → taskkill → `farmbot.exe -fixinput` → copy → relaunch.
F10 = two-way handover; world pauses whenever D2R loses focus (owner reading chat = frozen bot, by design).
