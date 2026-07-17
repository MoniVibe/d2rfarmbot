# FARMBOT ORCHESTRATION HANDOFF — the Oracle era (2026-07-18, ~02:00)

You are the new orchestrator for the **aquarium farmbot**: a hands-off bot playing the user's
OFFLINE, modded (D2RMM "Reimagined" repack) Diablo II Resurrected 3.2.92777 on this MSI laptop,
watched like a fish tank. This document is your cold start. Companion docs (older but valid):
`C:\dev\HANDOFF_laptop_3.2.md` (bringup-era), desktop `HANDOFF.md` §0 epistemics warning, and
the Claude memory dir `C:\Users\shonh\.claude\projects\C--dev\memory\` (read MEMORY.md first).

## 0. SAFETY LOCK (non-negotiable)
OFFLINE repack only, char **BenjiNetanyahu**. NEVER touch Battle.net-connected anything
(Warden = permaban). No debugger ever (Arxan kills D2R ~160ms after attach). RPM/WPM +
user32 patching only. The user sources the game/crack; you handle everything after disk.

## 1. INTENTIONS (what the user wants, in their words and priorities)
- A self-sufficient aquarium: farm, level, survive, progress areas, town errands, get better
  gear — watchable and *deliberate*, "mastery over how it walks/runs, not flop like a fish".
- **Oracles**: single authorities that KNOW things — movement (obstacle-aware maphack),
  gear ("buying this and socketing those yields a better thing"), combat (don't go balls-deep
  into a 12× pack with two minions). All three exist in v1; see §4.
- The user respecced Benji as a **skeleton summoner** so testing is easier and survivable.
  Their stat rule (encoded in autostat): keep str/dex a small buffer (~10) over gear
  requirements, dump the rest into vitality. **-autoskill is deliberately OFF** — build
  points are the user's design; ask before re-enabling.
- The user is hands-on, watches live, and interrupts with observations. Treat every "he's
  doing X weird" as a measured-bug report — every single one tonight was real.

## 2. CURRENT STATE
- **Char**: BenjiNetanyahu, SOFTCORE Necromancer ~L10 (~69k xp). RaiseSkeleton 4 /
  SkeletonMastery 1 / ClayGolem 2. **F1 = Raise Skeleton, F2 = Clay Golem** (bindings survive
  death; the right-skill *selection* resets to Attack on death). Unique ShortBow equipped
  (left-click = bow shot; the mod needs no arrows), full unique/rare kit, unique HandAxe on
  weapon set II (a 'W' press swaps sets — that explains any "hands swapped" sighting).
  As of handoff he was mid corpse-run to Stony Field (died to the 12× pack pre-combat-oracle).
- **Soak v9 running**: `overnight.sh` (repo root) loops 36×15-min self-exiting runs, launched
  01:52:47, log `logs/overnight_master9.log`, per-run `logs/overnight_N.log` + scoreboard
  lines. A persistent Monitor tails the master log for failures only.
- **Repos**: `C:\dev\koolo-build` branch **laptop-3.2**, pushes to
  https://github.com/MoniVibe/d2rfarmbot (HEAD `6a751ca`). `C:\dev\d2go-local` (master) is
  go.mod-replaced in — **not in the public repo** (not self-contained; the corpse-detection
  fix lives there). Git identity set repo-locally (Monivibe <shonhay@gmail.com>).
- Build: `source /c/dev/tools/goenv.sh && cd /c/dev/koolo-build && go build -o farmbot.exe ./cmd/farmbot`
- Canonical run flags (what overnight.sh uses):
  `-seconds 900 -move e -runwalk r -nav -livegrid -chicken 30 -radius 90 -diff normal
   -dpiscale 1.25 -werewolf "" -rabies "" -spirit "" -wolves "" -creeper ""
   -attackrange 25 -meleebelow 15 -loot -objects -autoprogress
   -summon f1 -maxpets 4 -golem f2 -autostat "347,305,347,428,347,552"`

## 3. THE DESIGN (the five-layer stack — this is the architecture, defend it)
1. **Perception**: d2go memory reads at ~3Hz, with per-field epistemics. Known liars: monster
   Life stat (frozen 32768 on 3.2 — use Mode for death), OpenMenus (stable garbage — never
   trust for panel detection), KeyBindings (stale — keys come from flags/bindings).
2. **Closed-loop actuation**: every action verifies its effect (click→hover confirmed,
   walk→position changed, summon→pet count rose, stat-click→StatPoints fell). *Silence is
   never success.* Every bug tonight was an open loop; close them on sight.
3. **Oracles** (one authority per question, prior + observed truth, truth wins): §4.
4. **Measured executors + intent**: locomotion decided by the movelab bake-off (numbers, not
   vibes); behavior arbitration wants hysteresis/commitment (partially done — see tasks).
5. **Ops harness**: self-exiting runs; wrapper `timeout 960` REAPS post-heal hangs (farmbot
   can hang after 'done'+heal — never run without the wrapper); state files survive restarts
   (`logs/death_state.txt` area+pos, `logs/route_state.txt` route index, `logs/atlas/`);
   failure-only Monitors; per-run scoreboard.

**Culture**: the project's §0 epistemics — every claim needs a check that could falsify it;
measured constants don't transfer between machines; when a loop won't close, change STRATEGY
(fight/ring-search/abandon), never retry the same verb harder; time-box everything.

## 4. THE ORACLES
### Movement (tasks #1,#2,#4 — mostly done, intent layer open)
- **Mover** (`cmd/farmbot/mover.go`): sole locomotion owner. Executor = movelab winner:
  force-move carrot at the FARTHEST plan point with fat-ray LOS (16.5s/0.98 efficiency vs
  19.3s/0.88 near-waypoint; **click-move REFUTED** 1/4 arrivals — D2R ignores synthetic move
  clicks; do not relitigate). Closed-loop stall/orbit detection, planner flip (live↔map),
  openBurst (clearance-scored 8-ray escape), MoveBlocked → caller brings violence
  (fightThrough) . All path-following rides navWalk→Mover: chase, explore, corpse, death-seek,
  entrance approach.
- **Atlas** (`internal/game/atlas.go` + tests): persistent per-(mapSeed, area) collision truth
  accumulated from live rooms (masked to Room1Ptr!=0), 2-bit packed on disk, corrupt-tolerant.
  Feeds and is fed by every live-grid build (`acquireGrid`). `FrontierNear` powers
  exploration: walk to known-walkable-touching-unknown → rooms load → frontier advances.
  Blind wander (the circle generator) is DELETED.
- **Map prior**: koolo-map seed generation (`C:\dev\d2lod` + `tools/koolo-map.exe`), 136
  areas. Traps: town collision is garbage for the mod (use livegrid); some area grids come
  AXIS-TRANSPOSED (live Blood Moor 280×480 vs map 480×280) — buildMapNavi scores all 4
  transpose variants against live collision and only accepts >0.65 agreement; **mapped exit
  positions lie by ~30 subtiles** (measured at Den and Burial Grounds) → the exit-seek
  ring-searches (radius 32, 8 sectors) after 8s parked, and after 2 fruitless laps ABANDONS
  the route stop and advances (a lying exit costs 90s, not a night).
- Entrances are WALK-ONTO (transition on contact); click only as 6s fallback. Route
  (`-autoprogress`): Den(8)→Cold Plains(3)→Burial(9)→Stony(4)→Dark Wood(5)→Black Marsh(6),
  advancing on 2.5-min no-enemies exhaustion or exit-abandonment; index persisted.
- **OPEN (task #4)**: intent commitment — one committed intent (kill X / travel Y / loot Z)
  with min-hold + completion/failure conditions replacing per-tick re-arbitration. Target
  selection is already sticky; the flip risk is between behavior classes.

### Gear (task #9 — knowledge done, execution open)
- `internal/gear` (16/16 tests): parses the MOD'S OWN tables at
  `C:\Program Files (x86)\Diablo II Resurrected\mods\D2RMM\D2RMM.mpq\data\global\excel\`
  — 15,742/15,874 cube recipes (132 name-based exotics fail closed, counted in
  Stats.SkipReasons), item/type DAG, gems. `ScoreItem` (summoner weights; str/dex valued only
  to requirement targets; unidentified scored at base + "identify first"),
  `FeasibleUpgrades(equipped, inventory, gold, w)` → ranked EQUIP/SOCKET/CUBE Suggestions
  (randoms flagged Gamble). Probe: `farmbot.exe -gearoracle`.
- **OPEN**: vendor-stock reading (the buy arm — TODO(vendor) seam exists), and EXECUTION
  (equip/socket/cube via inventory+cube UI — rides the town-errand muscle, §5).
- NOTE: a reading taken mid-corpse-run sees a naked char and suggests wearing spares —
  correct but confusing; read at post-recovery boundaries.

### Combat (task #10 — v1 shipped, tune with data)
- Posture before targets each tick: threat = pack mass within 30 (elites/raisers ×3);
  strength = pets×3 + 2 (+2 if hp>60). threat>strength → **kite** (never advance into the
  pack; step back when centroid closer than attackrange-5); threat>3×strength → **regroup**
  (retreat, let the summon block raise from corpses, or just leave). Posture transitions log
  with numbers. Founding incident: bow necro charging a 12× pack with two minions.
- Iterate with soak data: pull-single-target while kiting, corpse availability in strength,
  boss cases, posture stats in the scoreboard.

## 5. HOW TO PROCEED (task queue, in order)
1. **TP round-trip on F3** (task #6): self-bind Tome of Town Portal to F3 via the skill-popup
   bind flow (`-bindskill "1030,1013,<tomeX>,<tomeY>,f3"` — take a fresh popup screenshot
   first, positions shift as skills grow). Cast → find the TownPortal object (IsPortal ✓) →
   walk-onto/click through → confirm town → fresh portal back. This unlocks all town logistics.
2. **Akara vendor** (task #7): interactNPC (exists, body-hover) → shop panel calibration
   (uisnap + screenshots; panel clicks are RAW PHYSICAL pixels via uiClick — proven on skill
   tree and char panel) → sell filter-trash, buy potions with gold-pile money → also unlocks
   the gear oracle's buy arm (vendor stock read).
3. **Identify + repair** (task #8): ID tome (66 charges) right-click → item click over the
   inventory grid; repair at Charsi when equipped durability < threshold (durability reads
   work; the bow degrades).
4. **Gear execution**: act on -gearoracle Suggestions (equip/socket/cube) with per-step
   verification; cube UI is new calibration.
5. **Intent commitment** (task #4) + combat-oracle tuning with scoreboard data.
6. Backlog: INVALID607 belt items (likely the mod's mana drink — currently unrecognized, so
   mana potions only match by name), stat.Gold read for gold-aware decisions, movement
   telemetry (stall-seconds) in the scoreboard, Act 2 (route + town tables).

## 6. OPS RUNBOOK (violate any of these and you'll repeat our nights)
- **ONE farmbot at a time — including probes.** A probe's exit-heal restores input functions
  a live bot believes are patched (the desktop's crash class). Probe only between runs; the
  wrapper's own between-run charprobe is the pattern.
- **Never force-kill farmbot while D2R lives** (leaves input patched; user's mouse dies —
  `-fixinput` heals). Self-exiting `-seconds` runs only. Post-heal hangs are reaped by the
  wrapper's `timeout 960` (heal lands at 900) — safe by construction.
- **Deploy dance**: build to `farmbot_next.exe`; stop wrapper (PowerShell, match CommandLine
  '*overnight.sh*'); wait current run's self-exit; `cp farmbot_next.exe farmbot.exe`; do any
  supervised probes; relaunch `nohup bash overnight.sh > logs/overnight_masterN.log &`;
  re-arm the failure Monitor on the new master log (stop the old one).
- **The esc trap**: a synthetic ESC with nothing open opens the PAUSE MENU (game pauses).
  Toggle panels with their own keys ('t' tree, 'c' char); `-press esc` is the surgical fix;
  NEVER click pause-menu buttons synthetically (its hit-test space differs — a "Return to
  Game" click once hit "Loot Filter"/"Save and Exit"). Real input requires D2R focus and the
  always-on-top terminal eats clicks — avoid; the synthetic path works UNFOCUSED.
- **Measured constants (this laptop)**: display 125% → `-dpiscale 1.25` ALWAYS. World aim =
  client×scale (clamped in-window); panels = unscaled raw physical pixels; these are
  DIFFERENT spaces, both measured — see memory `d2r-world-aim-scale` / `d2r-ui-panel-clicking`.
  Char panel + buttons x347 / y305,428,552. HUD skill slot (1030,1013).
- D2R crash signature: exit 0xffffffff, no WER, no dump = D2R's own handler; not your fault.
  Game window: check D2R alive before blaming code.

## 7. WHERE THINGS LIVE
- Code: `cmd/farmbot/main.go` (the loop + probes), `mover.go`, `internal/game/atlas.go`,
  `internal/game/{roomgraph,memory_reader,mouse,screenshot}.go`, `internal/gear/*`.
- Probes (all in farmbot, run between soak runs): `-charprobe -objprobe -gearprobe
  -gearoracle -statsnap -statalloc -aimprobe -movelab -uisnap -paneltest -bindskill -press
  -interact -dietest -fixinput`.
- Logs: `logs/overnight_master*.log` (wrapper), `logs/overnight_N.log` (runs),
  `logs/atlas/` (cartographer), `shots/` (screenshots). State: `logs/death_state.txt`,
  `logs/route_state.txt`.
- History: every measured discovery is in git log on `laptop-3.2` — commit messages are the
  real lab notebook. Read `git log --oneline -30` before assuming anything is unknown.
