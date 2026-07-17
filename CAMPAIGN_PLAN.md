# farmbot — Campaign-completion build plan

_Synthesis of two independent advisor rulings (deep-reasoner / Opus + Codex/GPT-5.4), 2026-07-16. They were briefed separately and converged — treat the shared conclusions as high-confidence._

## The reframe that changes everything

**A full campaign runs in ONE contiguous game session at a single difficulty.** You do not create new games between acts. So the unsolved "menu wall" (synthetic input filtered at char-select/difficulty) only bites at **initial game creation** (semi-attended — the human makes the game, then the bot attaches) and at **death/restart**. Everything between is hands-off in-game where our memory-driven input works.

Consequence: the **Interception kernel driver / unattended-idempotency dependency is OFF the campaign critical path.** We do not need it to complete a campaign. (It would only matter for fully-unattended *repeated* farming.) This removes the biggest perceived blocker. Build for "human creates one game, bot plays it to completion (or death), checkpointing so a crash resumes."

## The keystone

Every remaining system — waypoint, vendor, stash, gamble, repair, identify, **skill/stat allocation on level-up**, merc, cube — is "click a specific UI element." **Nothing ships until the panel-click primitive is solid.** That is the current blocker (WP row-click) and it is M1.

## Build order

### M1 — Panel-click primitive (the keystone) — IN PROGRESS
- Resolve the WP row-click (see `HANDOFF.md` §5; `-wpaim` aim-oracle + `-panelfn` sweep). Root cause is client-vs-screen `lParam` and/or aim coordinate-space and/or missing hover-before-click — all three now addressed in code (`defec61`, hover-move), pending one live confirmation.
- Generalize `uiClick` → **`uiClickElement`: a closed loop** — aim → verify the target element is highlighted (screenshot luminance, or `HoverData` if UI elements register there) → click → verify the state changed. Model it on `hoverPickClick`'s use of game feedback instead of guessed pixels.
- **Make panel geometry a PROFILE** (config, not constants): tab centers, row centers/pitch, DPI scale, client origin. The mod moved every layout; a resolution/mod change must be a data edit, not a code edit.

### M2 — Waypoint network (data-driven)
- `AvailableWaypoints` **works on this build** (reads unlocked area IDs) — use it, don't parse the panel to know destinations.
- Build an empirical **area-ID → (act tab, row index)** table (drive each WP once, read `PlayerUnit.Area`, correlate; the `-wpaim` highlight curve gives the row map for free). This is the act-to-act fast-travel spine. **Hardcode nothing from vanilla WP order.**

### M3 — Town services
- NPC nav (see unknown #2) + `uiClickElement` for shop / stash / repair / gamble / identify.
- Prefer **memory** over pixels where readable (e.g. shop-grid item positions come from the same live `UnitTable` as ground items). Service identities, NPC/object IDs, inventory rules = data-driven.
- Closes the farm→fill→town→restock→back loop for long runs.

### M4 — Progression UI (gates survival past ~Act 2)
- **Skill/stat allocation on level-up.** koolo's `EnsureStatPoints`/`EnsureSkillPoints` (`internal/action/leveling_tools.go`) are **stubbed `return nil`** — must be built. Skill-tree layout is mod-moved → `uiClickElement` targets.
- Merc hire/revive.

### M5 — Per-act quest scripts (the long pole)
- Data-driven sequences: goto area → kill boss (live UnitID) → pick up quest item (live UnitID) → return to NPC → advance dialog → act unlocks.
- **Every ID discovered live**, never from koolo/vanilla tables.

### M6 — Resumable orchestrator
- String acts together in one session. **Checkpoint campaign state to disk after every step** — crashes are systemic and intermittent (repack instability), so every milestone must be idempotent/re-entrant. On relaunch (human recreates the game) attach and resume from the checkpoint. Death = end run, surface to human.

## Load-bearing unknowns (resolve in this order)
1. **Panel row-click** (M1) — everything gates on it. _[one live session away]_
2. **Town / dense-area traversal + dynamic obstacles.** `BuildLiveGrid` is proven for wilderness only; NPCs and the bot's own merc/summons block movement but aren't in tile collision (the disclosed pen-in bug). Gates reaching any NPC/WP.
3. **WP travel reliability** — `AvailableWaypoints` + empirical row map. Gates act-to-act.
4. **Read + allocate skill/stat via UI.** Gates surviving past Act 2.
5. **Quest / act-unlock state** — detectable from memory, or must be inferred from area reachability? Gates the campaign spine.

## Where the mod hurts most (worst → least)
1. **Quest chains & quest-item IDs** (act unlocks) — deepest, most brittle; each needs live discovery, likely per-quest RE.
2. **Boss UnitIDs / arena mechanics** — big overhaul; bosses may be moved/re-statted.
3. **Panel/UI coordinates** — WP rows, shop grids, skill tree — every layout moved.
4. **Area IDs & WP order** — remapped but **readable** (`AvailableWaypoints` + live `Area`).
5. **Monster/item/object types** — LEAST hurt; already read live from `UnitTable`, mod-agnostic.

## Data-driven vs hardcoded
- **Data-driven (discover live / config):** area IDs, WP row map, monster/item/object `txtFileNo`, boss/quest-item UnitIDs, NPC positions, panel element coords+signatures. Anything the mod can remap.
- **Hardcoded (D2R-version invariants; the exe is frozen so these stay valid):** the input stubs (`GetPhysicalCursorPos` patch, `GetKeyState(VK_LBUTTON)` override), the memory offset chains (DrlgLevel/path/room, `HoverData`, `AvailableWaypoints`), the nav algorithm, the closed-loop interaction patterns.

## Standing constraints (never violate)
- **Memory-driven input only.** No Interception driver, no `SetCursorPos`, no `SetForegroundWindow`, no real HID — the user keeps using the PC while the bot runs. The `GetPhysicalCursorPos`/keystate patches ARE the aquarium.
- **Never force-kill farmbot** — it skips the input restore/heal and corrupts D2R's mouse/keyboard for the human until a D2R restart. Let `-seconds` self-exit; `-fixinput` heals.
- **No debugger** — Arxan anti-tamper kills D2R on attach.
- **Clear-before-interact** — a WP/chest/NPC in a monster pack can't be hovered (bodies occlude the label). Fight first.
- **Offline/disconnected repack + the character only** — never a Battle.net-connected account (Warden = permanent ban).
