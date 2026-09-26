# AZBOT v3 — foundations review and roadmap (2026-09-24)

Synthesized from two independent reviews (architecture; game systems) plus the relay
evidence R1–R10. v2 (docs/AZBOT_V2.md) fixed control flow — states, janitor, session,
watchdog, nav core. v3 fixes where the bot's *knowledge* and *intent* come from.

## The root cause

azbot rediscovers at runtime what the game already knows, fights its own input setup,
and has no plan:

- **Knowledge:** data tables, UI tree, map and save file all exist; the bot uses literals,
  pixel detectors, straight lines and F-key probing instead.
- **Input:** four input channels (posted messages, in-process cursor patches, fake focus,
  focus-stealing SendInput) so the bot can share a desktop with the owner. This is the
  source of the crash class, deaf shop panels and dead hover.
- **Intent:** behaviour emerges from 31 hand-set urgencies and ~25 cooldown globals; the
  watchdog, benching and mute policy exist to break loops a plan would never create.

Verified oversights (evidence in the review transcripts, file:line):

| # | Oversight | Fix | Size |
|---|---|---|---|
| P1 | Item/monster/level facts hardcoded (item ids 533–608, monster levels, raiser list, bag 10×4 vs measured 10×8); `internal/gear/tables.go` already parses the mod tables but azbot never imports it | `gamedata` package from the mod's excel + strings (relay R11) | S–M |
| P2 | UI read from pixels because the memory UI is "dead" — but the fork's offset scanner ignores errors and d2go's widget-tree reader (`ReadAllPanels`) is never called | Rebase d2go-local on upstream, re-find signatures, fail-hard attach self-test; widget tree = UI truth, pixels = test oracle | M |
| P3 | No fused world model (171 raw GetData calls; percept mixes reading and policy) | One WorldModel with source/freshness per field | M |
| P4 | Map knowledge discarded: void between rooms plannable, the live grid's unknown penalty dropped by journey, seed map used only for exits | 3-state grid (wall / walkable / unknown-in-room, void = wall); room-graph + tile A*; mapfuse (in progress) | S→M |
| A1 | Hybrid input fighting the environment | Dedicated environment (VM/second session/spare PC), SendInput only, pinned game settings (koolo `config/Settings.json`) → drop dpiscale/aimcal/patches | M |
| A2 | No camera model: the 19.8/9.9 projection copied 22× in 16 files; aimcal says tiles are 11% larger | One `Camera` refit per session from hover-confirmed points; hitboxes from monstats2/objects/LvlWarp | S |
| A3 | 55 movement call sites in 12 files | One `MoveTo(goal)`; ratchet test like the ESC guard | M |
| D1 | No mission layer | Scripted missions (koolo run shape: town routine → travel → objective → return) + a small interrupt set (survive > chicken > recover > stuck); utility only inside slots (target, loot, skill) | L |
| D2 | Policy as code (loot "STRICTLY UNIQUES", per-act struct fields, 45 flags + 3 env vars) | Per-character YAML profile, NIP pickit rules (templates exist in `config/template/`), a per-act Town interface | S–M |
| K1 | Remembers the wrong things (hardcoded seed + hand-piloted road; waypoints learned by walking; F-key probing) while the `.d2s` save holds waypoints, quests, hotkeys, stats | Read the save; learned `Params` with confidence instead of ~531 literal durations; per-area stats drive run choice | M |
| E1 | Untestable without the game (concrete MemoryReader/Motor, 261 time.Now) | WorldView/Actuator/Clock ports; full-Data recorder; headless simulator; every relay result becomes a regression test | L |
| E2 | Two architectures alive (janitor off by default, -Classic, AZBOT_DELIBERATE); 2.8k/2.6k/2.1k-line god files | Flags with expiry; delete old paths once drills pass; main.go = wiring | S–M |
| E3 | Knowledge in contradictory prose ("PROVEN" docs disagree on click-move, bag size) | Every measured rule = a test over recorded data | S |

## Game-systems gaps (Barbarian L27, Act 2 normal)

1. **Quests:** Act 2 route is a bare area list; no quest state (cube, Staff of Kings, Viper Amulet, Summoner, tombs, Duriel). Port `internal/run/leveling_act2.go`.
2. **Level gates contradict the monster-level table:** at L27 nothing in Act 2 is "worth" fighting and Valley of Snakes needs L28 → stalls near Lost City in 10-tile corridor mode.
3. **Barbarian skill points never spent** (Reach-only rule, `spend.go:377`) → owner skill plan.
4. **Mercenary ignored:** no alive check, revive, merc potions, gear.
5. **Repair broken by construction:** Act 1 coordinates + posted click that panels ignore.
6. **Loot uniques-only** → no gold → repair/potions/revive starve (loot agent in progress).
7. **Equip compares quality only** and never weapons/shield/belt/amulet/rings.
8. **Potions/chicken:** rejuvs wasted at 60%, no belt refill from bag, no emergency double-drink, chicken not built. (HP% bug fixed: MaxLife omitted bonuses.)
9. **No elite/immunity/poison awareness.**
10. **No battle-cry buffs** (Shout/BO/War Cry).

## Keep

The design laws that worked: typed outcomes, one arbiter for interrupts, held-time
clocks, phase machines, the janitor, the session FSM, the watchdog as judge, the relay +
flight recorder, `tools/test.sh`.

## Drop

Pixels as primary UI truth; top-level utility bidding; background play on the owner's
desktop via in-process patches; seed-scoped learned roads and the hardcoded seed;
runtime F-key discovery; prose laws and relay-as-test-loop.

## Roadmap (bot runnable after every step)

1. `gamedata` (mod tables + strings) and `Camera` as drop-ins replacing literals; void = wall grid. *(in flight: mapfuse, coverage, loot tiers)*
2. Game systems that unblock progress now: skill plan, level gates, repair at Fara, merc revive/potions, potion lanes + chicken, gold + quest-item pickup.
3. Rebase d2go; widget-tree UI in shadow mode vs the screen oracle.
4. Ports + Clock + full-Data recorder → simulator; relay results become fixtures.
5. `MoveTo` sole mover (ratchet); `Interact` service for NPC/object/entrance/item on hitboxes.
6. YAML profile + NIP pickit + per-act Town.
7. Mission layer (koolo run shape, quest-aware Act 2) over azbot verbs; arbiter shrinks to interrupts.
8. Dedicated environment + single input channel; delete patches/aimcal/fake focus. *(owner decision)*
9. Delete legacy flags and paths.
