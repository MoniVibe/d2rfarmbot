# AZBOT — Advisor Briefing (2026-07-20, ~02:30)

Prepared for an outside advisor. Owner: MoniVibe. Engineer: Claude (agentic
session, live-operating the bot all night). Repo: `github.com/MoniVibe/d2rfarmbot`,
branch `laptop-3.2`, working tree `C:\dev\koolo-build`.

## 1. What this is

An autonomous farming bot ("azbot") for **offline, modded** Diablo II
Resurrected 3.2 (D2RMM "Reimagined" repack; never Battle.net; no debugger —
the game is Arxan-protected, all state comes from external memory reads via a
d2go fork, all input from posted window messages / a virtual-cursor injector).
Current character: **Mamazon**, a level-12 softcore bow Amazon with a rogue
mercenary, marching an Act 1 itinerary (Blood Moor → … → Catacombs/Andariel).
A predecessor bot ("farmbot", koolo-derived) is frozen as a reference
implementation with weeks of proven runtime.

## 2. Intent

The owner's vision, in his own words across the night:

- "An intelligent bot, not a fish" — behavior should emerge from readable
  state + doctrine, not scripted routes.
- Class-agnostic core: "bots should be aware of what's possible for them" —
  no class name may appear in a conditional; class knowledge lives in ONE
  priors table. The bowzon is the pilot; a **barbarian or paladin is to be
  rolled next** as the portability test.
- Constant, *driven* progress: "more driven, not an aimless zerker" — ride
  waypoints, take shrines/wells en route, prioritize resurrecting shamans,
  never orbit, never idle.

## 3. How we work (method)

1. **Spec-first**: every behavior lives in `docs/AZBOT_PROCEDURES_STE.md` —
   an ASD-STE100-style controlled document (numbered procedures P-1…P-10,
   WARNINGS each bought with a measured loss). Owner complaint → spec
   amendment → code → build.
2. **Owner-in-the-loop live ops**: the owner watches the game and reports
   symptoms ("she's stuck on a wall"); the engineer reads the structured log,
   the flight recorder (1 Hz snapshots; a death dumps a 300-frame black box
   replayable offline with `-replay`), screenshots, and now `logs/nav.png`
   (a 3-second navigation overlay: local walkability, her position, march
   target, last click point) — added tonight at the owner's request so
   failures can be *pointed at* instead of described.
3. **Staged deploys**: fixes build to `azbot.exe.new2`; a swap ritual (kill,
   heal game input, copy, relaunch) runs at lawful windows (town/dead/idle).
   ~35 deploys tonight (runs 80 → 113).
4. **Persistent memory**: a WAL fact store (scoped: tick/game/seed/forever)
   records learned doors, vendor cells, roads; provenance on every fact.

## 4. Architecture (one paragraph)

A **sentinel** (100 ms; drinks potions, detects death, kill-switch) runs
beside an **executive** (~200 ms tick): perceive (snapshot from memory) →
stamp derived facts (e.g. line-of-sight per enemy) → collect **demands** from
~20 activities → an **arbiter** grants one holder by class order
(Survive > Recover > Fight > Loot > Service > Travel > Explore) → the holder
steps, acting through **verbs** with typed postconditions (a click that
changed nothing reports `deaf`, never assumed). A watchdog diagnoses
pathologies (stuck/orbit/thrash) and benches offenders; several "oracles"
(blood/time-to-die, experience-per-area, gear, hidden requirements) inform
demands.

## 5. What provably works tonight

- Combat doctrine: stand-and-fight above 33% HP (owner's "flee floor"),
  magic arrows always above 75% mana, resurrecting shamans die first,
  monsters behind walls do not exist for targeting or crowd math.
- Town services chain: heal, identify, fence, restock (potion/scroll cells
  probe-learned), repair, **stat-point spending proven live** (level-12
  vitality landed at 01:32), equip with hidden-requirement oracle.
- Recovery: death → respawn → cross-area corpse march by learned border
  facts → gear back (proven multiple times); a "pocket breaker" TPs her out
  of geometry traps (proven 01:27).
- Learning: every crossing records both door sides; tonight added **road
  breadcrumbs** — successful walks persist as waypoint chains and replay.
- Safety nets: a menu sentry + behavioral ESC probes + pre-attach medic
  against the offline pause-menu freeze family.

## 6. The owner's recurring complaints (the honest symptom list)

1. **"Thrashing/dancing at passes"** — area-transition zones (esp. Blood
   Moor ↔ Cold Plains ↔ Stony Field) produce back-and-forth orbits.
2. **"She's stuck on a wall / staring at a pocket"** — mod-added fence pens.
3. **"ESC keeps coming up"** — the offline pause menu freezes the world.
4. **"Pops a TP, goes in, immediately comes back"** — portal round-trips
   with no purpose (three distinct root causes found and fixed so far;
   latest: the pocket breaker's own return portal, fixed 02:30).
5. Waypoints not ridden (see §7.2).

## 7. Open problems — attempted, not yet closed

### 7.1 The pass dance (the big one)
Root causes peeled tonight, in order: fight-class preemption at door camps;
watchdog benching the sole mover; the *area read flickering* faster at seams
than any read-driven reaction (every reactive push becomes an oscillator);
the live collision grid lying about unstreamed rooms (planner orbit); the
mod's map data being **geometrically misaligned** (its grid doesn't even
cover her true coordinates — topology only, geometry never); and the mod's
invented fences existing in **no data source at all**. Current mitigations:
a "crossing drive" (commit to geometry, ignore area reads until 14 tiles
past the seam), a door-band with no planner, road-breadcrumb replay, click
gait. **Status: improved, not eliminated.**

### 7.2 Waypoint riding (P-10)
The chain is proven except the last inch: pad found (map + live), panel
opens, the lit-destination list reads honestly ("Stony Field lit") — but the
panel **evaporates in ~300 ms** (likely our own queued click), and the row
coordinates for this client remain unmeasured because every photo so far
caught the panel already gone. Tonight's build verifies the panel stands
before *each* row click and photographs only under a verified panel.

### 7.3 Dead movement clicks
Plain ground left-clicks sometimes move her not at all (posted-message click
path), while force-move keys and interaction clicks (corpses, portals,
vendors) work. Modifier latching and coordinate space were ruled out.
Suspected: click-to-move needs a different input path on this build (the
frozen predecessor bot moved by clicks successfully — its exact click
plumbing should be diffed against azbot's motor).

### 7.4 Vanilla UI bytes lie on this mod
Three bytes so far read false/stale against visible reality (shop-open,
quit-menu, plus lingering vendor stock). Standing rule adopted: **no UI byte
is trusted until observed reading TRUE once on this mod**; behavioral probes
are the sentries meanwhile.

## 8. Questions for the advisor

1. **Navigation**: given (a) misaligned map geometry, (b) a live grid blind
   to unstreamed rooms, (c) invisible mod fences, is the right architecture
   *pure experience routing* — breadcrumb roads + the game's own click
   pathfinder — with grids demoted to hints? Or is there a known way to
   re-align koolo-map output against live coordinates (an offset solve from
   two learned door facts per area pair)?
2. **Seam crossing**: is there a known-good pattern for D2R area ribbons
   (position-based hysteresis? commit windows?) beyond our crossing drive?
3. **Class profiles**: we intend barbarian/paladin next. The frame is
   hands-based (Contact/Reach tools + per-class priors); the known gap is a
   **SUSTAIN tool class** (auras = keep-selected, shouts/armors = rebuff
   timers, summons = headcount economy). Any prior art on modeling these as
   capability classes rather than class scripts is welcome — as are opinions
   on whether per-class *profiles* (owner's instinct) beat capability
   inference for reliability.
4. **Input**: posted messages vs. the Interception driver (not installed;
   needs admin + reboot). Menus need hardware-level input; is the driver
   worth the operational cost for menu reliability alone?
5. **Verification**: our postcondition discipline catches lies well but each
   new UI surface costs a night of probe-writing. Is there a cheaper general
   oracle for "did the UI actually change" (frame-diff hashing of screenshot
   regions is the current candidate)?

## 9. Current status line

Level 12, Stony Field frontier, ~35 commits tonight, all pushed to
`laptop-3.2`. Bot is live (run 113) under a 5-minute autonomous watch loop.
Blocking the "good bot" verdict: the pass dance closed for good, one clean
waypoint ride observed end-to-end, and then the barbarian experiment.
