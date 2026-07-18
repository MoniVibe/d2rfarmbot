# AZBOT — Ground-Up Architecture for an Intelligent D2R Farmbot

**Status:** Design, approved for build-out
**Replaces:** `cmd/farmbot` (kept untouched as the reference implementation and lesson archive)
**Lives at:** `cmd/azbot` + `internal/azbot/...`, reusing `internal/game`, `internal/gear`, and the atlas
**Prime directive:** every mechanism that exists in farmbot as a comment, a magic constant, or a `continue` at the right line number must exist in azbot as a *type* — or not exist at all.

---

## 0. The Five Laws (non-negotiable invariants)

Everything below derives from these. If an implementation choice violates one, the choice is wrong.

- **LAW 1 — One arbiter, one currency.** Exactly one activity holds the actuator per decision cycle. There are no behaviors outside the ledger: no hoisted blocks, no `continue` chains, no bespoke booleans (`oppFight`), no state machines that run "above" arbitration. Survival is not a queue position; it is a separate execution tier that cannot be slept away.
- **LAW 2 — The cursor is a scheduled instrument.** It is one physical resource with three mutually exclusive roles — steering vector, aim effector, and the build's only trustworthy identity *sensor*. It is reserved like telescope time: exclusive, time-boxed, tagged `Sense` / `Aim` / `Steer`. No code touches the mouse without a reservation.
- **LAW 3 — Silence is never success.** Every world interaction is a monitored journey with a typed postcondition: click → expect motion → expect arrival → expect state change. Every verb emits a structured outcome (`Done` / `Deaf` / `Blocked` / `Whiff` / `Timeout`) into one ledger that targeting, blacklists, and the atlas consume. Ten deaf hover-confirmed corpse clicks followed by death can never happen silently again.
- **LAW 4 — Dead reads are unrepresentable.** Perception channels get a liveness verdict at attach. A channel proven dead (KeyBindings, monster Life, OpenMenus, the five PATTERN-NOT-FOUND offsets) is not exposed as a field. Decisions consume one immutable snapshot per cycle; verification is a distinct, freshness-bounded read primitive. `gr.GetData()` has exactly two call sites in azbot, not 164.
- **LAW 5 — Experience survives its boundary.** Every fact declares a survival scope (tick / activity / area / game-session / seed / forever) and provenance (measured / derived / prior / folklore). Durable facts flush crash-safely the moment they are learned — the nights that crash are the nights that learned the most, and they must not evaporate at `atlas.Save()`-on-graceful-exit.

---

## 1. Process Model — Who Owns Time

Farmbot's only scheduler was `time.Sleep` (191 sites), and every sleep was global anesthesia: the death monitor was off during death recovery's own 18s respawn probe. Azbot has exactly three goroutines with distinct time contracts:

```
┌────────────────────────────────────────────────────────────────┐
│ SENTINEL goroutine        fixed 100ms cadence, NEVER blocks    │
│   reads: minimal survival snapshot (Mode, HP%, MP%, area,      │
│          process/window liveness) via bounded RPM reads        │
│   acts:  belt keypress (key lane only — no cursor needed)      │
│          raises Survive-class demands to the Executive         │
│          writes heartbeat + death_state facts                  │
├────────────────────────────────────────────────────────────────┤
│ EXECUTIVE goroutine       bounded cycle, target 8–15 Hz        │
│   snapshot → collect demands → arbiter grants ONE lease →      │
│   activity.Step(lease) [hard budget ≤ 120ms] → ledger append   │
├────────────────────────────────────────────────────────────────┤
│ SCRIBE goroutine          write-behind WAL for memory/ledger   │
│   fsyncs learned facts within 1s of Put(); compacts on exit    │
└────────────────────────────────────────────────────────────────┘
```

**Time rules:**

1. `time.Sleep` is forbidden outside the `motor` package, and inside it every wait is bounded by a verb's declared deadline. Grep-enforceable in CI.
2. Long operations (hover sweeps, portal entry, respawn probing, vendor menus) are *states in a state machine*, not sleeps. `Step()` does a bounded slice of work and returns; the wait happens by returning `Running` and being stepped again next cycle.
3. Every activity deadline is expressed in **held time** — time accumulated only while the activity actually held the lease — via a `HeldClock` the arbiter maintains. Wall-clock deadlines exist only in verbs (physical expectations: "portal animation completes in ≤4s"). This kills the resume-reset bug class (the fix farmbot hand-wrote three times) structurally: preemption pauses your clock instead of you remembering to reset it.
4. Preemption latency is bounded by the step budget: the Sentinel can seize the Executive within ≤ ~120ms + one cycle, versus farmbot's multi-second-to-48-minute blind spots.

---

## 2. Package Layout

```
cmd/azbot/
    main.go                 // attach, epistemics gate, wiring, run loop, control channel

internal/azbot/
    percept/
        channels.go         // Channel[T], Liveness, attach-time verdicts
        snapshot.go         // Snapshot (immutable, per-cycle), Verifier (fresh bounded reads)
        attach.go           // pattern-scan audit; refuses garbage offsets (LAW 4)
    motor/
        motor.go            // cursor reservation (Sense/Aim/Steer), key lane, dead-man release
        input.go            // thin wrappers over internal/game hid/mouse/keyboard/sendinput
    verbs/
        verb.go             // Verb interface: contract = precondition+action+expectation+escalation
        stride.go           // release-aim-press edge law, gait, displacement postcondition
        hoverstrike.go      // aim-settle-reaim double confirm, fresh-pos read, evidence contract
        panel.go            // PanelClick: physical px, cell-diff + cursor-occupancy postcondition
        menunav.go          // UIBytes[0xF4] oracle, keyhold nav, deadline on EVERY wait
        pickup.go           // hover confirm → click → ground-gone ∧ inventory-gain ∧ cursor-empty
        cast.go             // skill-select readback verify, then aimed cast
        belt.go             // cursor-free key verbs (drink, TP cast)
    journey/
        journey.go          // THE movement authority: goal, escalation ladder, honest verdicts
        navigator.go        // adopted from farmbot nav.go (algorithm kept wholesale)
        moverstep.go        // adopted click-gait/LOS-carrot executor, Blocked verdict made reachable
        obstacles.go        // one obstacle source; changes invalidate plans explicitly
    arbiter/
        demand.go           // Demand, Priority classes, Commitment
        arbiter.go          // Decide() — the one decision function
    activity/
        activity.go         // Activity interface, StepCtx, HeldClock
        fight.go            // §7.1
        recover.go          // corpse recovery — §7.2
        loot.go             // unified pickup (absorbs critloot + armloot)
        travel.go           // route progression, area crossing, exit-seek (unified)
        errand.go           // town trip + vendor/identify/cube — §7.3
        explore.go          // frontier advance
        flee.go             // chicken (Survive class), hazard-pocket escape
        respawn.go          // death → respawn probe (uses remembered key)
    combat/
        capability.go       // self-calibrating skill map: Skills ∧ proven bindings
        targeting.go        // score-over-nearest; build terms attach to skill metadata
        threat.go           // posture oracle v1 (calibrated folklore, instrumented for v2)
    memory/
        store.go            // Fact{Scope, Provenance}, WAL Put, crash-safe
        atlas.go            // wraps internal/game atlas; wedges flush on learn, not on exit
        roads.go            // measured town roads as data with provenance
        calib.go            // panel pixels, dpi, respawn key, townHubPos — all persisted
    ledger/
        ledger.go           // Outcome stream; consumers: blacklist, wedges, deaf-click detector
```

Reused unchanged: `internal/game` (memory_reader, roomgraph, map_client, atlas persistence substrate, hid/mouse/keyboard/sendinput, screenshot), `internal/gear` (item tables for equip/keep decisions).

---

## 3. Perception — `internal/azbot/percept`

### 3.1 Channel liveness at attach

```go
type Liveness uint8

const (
    LiveProven  Liveness = iota // closed loop confirmed on this build (Mode, position, HP%, Hover, UIBytes[0xF4], Skills, Corpse)
    LivePrior                   // trustworthy only after live validation (koolo-map grids, mapped exits)
    LiveDead                    // proven dead on this repack (KeyBindings, monster Life, OpenMenus, Roster, WidgetStates, Waypoints, GameData)
    LiveUnknown                 // pattern resolved but never verified
)

type AttachReport struct {
    Channels map[ChannelID]Liveness
    Misses   []PatternMiss // every FindPattern failure, loudly
}

// Attach refuses to construct accessors for LiveDead channels and
// FAILS ATTACH if any pattern miss would leave arithmetic running from
// pattern=0 ("ignoring errors, always best practices" is deleted).
func Attach(proc game.Process, calib *memory.Calib) (*Perceptor, AttachReport, error)
```

Consequences, baked in:

- The `Snapshot` type **has no monster Life field**. It has `Mode`. The frozen-Life disease cannot recur because there is nothing to consult.
- KeyBindings is not read, not assembled, not surfaced. Bindings come from `combat.Capability` (flags + selection-readback proof).
- `OpenMenus` does not exist; panel state is `Snapshot.MenuOpen` sourced from UIBytes[0xF4] — the one panel oracle (farmbot's four private detectors collapse here).
- The five garbage-offset channels (Roster, WidgetStates, Waypoints, Expansion-dependent flags, GameData) are simply absent. `GetActiveWeaponSlot` fiction, `AvailableWaypoints` fiction: unrepresentable.
- Load-screen garbage: `Snapshot` carries `Valid bool` + `Seq`; the perceptor refuses to publish a snapshot whose position sanity check fails (farmbot main.go:945 logic promoted to the source).

### 3.2 One snapshot per cycle, one verifier for fresh reads

```go
type Snapshot struct {
    Seq   uint64
    At    time.Time
    Valid bool

    Me       PlayerState   // pos, area, Mode, HP%, MP%, gold, stats, skills, selection
    Corpse   CorpseState   // Found only meaningful in corpse's own area (measured law, typed in)
    Units    []Unit        // monsters (Mode-filtered — no vacuous Life clause), items, objects, NPCs
    Rooms    RoomGraph     // streamed Room2 graph, frontier-aware
    MenuOpen bool          // UIBytes[0xF4]
}

// Verifier is the ONLY way to read the world outside the cycle snapshot.
// Every call is tagged with purpose and bounded by freshness.
type Verifier struct{ ... }
func (v *Verifier) FreshSelf() (PlayerState, error)                    // for aiming (hoverAim's implicit admission, made explicit)
func (v *Verifier) Hover(point ScreenPt) (HoverResult, error)          // aim-settle-reaim ritual inside
func (v *Verifier) Expect(probe Probe, within time.Duration) Await     // postcondition polling primitive
```

Decision code takes `*Snapshot` and cannot call the verifier; verbs take `*Verifier` and cannot call `Snapshot()`. Enforced by interface visibility. The syntactic ambiguity of farmbot's 164 `GetData()` sites — decision read vs verification read — is now a compile-time distinction.

---

## 4. Motor & Verbs — the actuation contract layer

### 4.1 Motor: the cursor scheduler (LAW 2)

```go
type CursorRole uint8
const (
    RoleSteer CursorRole = iota // cursor as movement direction sample
    RoleAim                     // cursor as strike/click effector
    RoleSense                   // cursor parked on target to read HoverData
)

type CursorLease struct {
    Role     CursorRole
    Holder   ActivityID
    Deadline time.Time    // hard; motor force-releases + moveStop at expiry (dead-man kept)
}

func (m *Motor) ReserveCursor(role CursorRole, holder ActivityID, d time.Duration) (*CursorLease, error)
func (m *Motor) KeyLane() *KeyLane // belt/skill keys: NO cursor reservation required
```

- The Sentinel drinks through `KeyLane` — survival reflexes never contend for the cursor.
- A `RoleSense` sweep can never be turned into locomotion: the motor releases any held move key before granting `RoleSense` (the moveStop-by-convention becomes moveStop-by-construction).
- The held-key-with-re-aim model is deleted. The **stride** is the only locomotion verb, built on the measured key-edge law: a held key keeps its key-down direction; direction change requires an edge.

### 4.2 The Verb contract

```go
type Result uint8
const (
    ResDone Result = iota
    ResDeaf        // input registered nothing observable (the corpse-click class)
    ResBlocked     // effect started, then obstructed (body-block, wall)
    ResWhiff       // aimed act missed its target identity
    ResTimeout     // expectation window expired
    ResRefused     // precondition failed; verb never fired
)

type Outcome struct {
    Verb     string
    Holder   ActivityID
    Target   UnitRef      // or ScreenPt / CellRef
    Result   Result
    Evidence string       // what was observed (or not)
    HeldMS   int64
    At       time.Time
}

type Verb interface {
    Name() string
    Needs() Needs                                  // cursor role, key lane, panel state
    Pre(s *percept.Snapshot) error                 // typed precondition
    Start(m *motor.Motor, v *percept.Verifier) error
    Poll(m *motor.Motor, v *percept.Verifier) (State, *Outcome) // bounded slice; Running/terminal
    Budget() time.Duration                         // hard wall-clock deadline — EVERY wait bounded
}
```

Every verb terminal state appends its `Outcome` to the ledger. **There is no fire-and-forget input anywhere in azbot.**

### 4.3 The verb set, with the measured laws baked into the types

| Verb | Action | Baked-in laws | Postcondition (typed, mandatory) | Escalation on failure |
|---|---|---|---|---|
| `Stride` | release → aim → press edge, commit window, re-read | key-edge law; 2.5s gait hold; ensureGait closed loop on Mode | net displacement ≥ threshold within window | report `ResBlocked` to journey — never self-retry blind |
| `HoverStrike` | select skill (readback-verify) → fresh self pos → sweep offsets → confirm `HoverData.UnitID==target` via re-aim ritual → click | hover async 1-frame law; -dpiscale-aware offsets from `memory.Calib`; sweep is *incremental* across Polls (no 1.1s freeze) | damage evidence: target Mode transition / poison state / corpse appears, within evidence window | `ResWhiff` → re-sweep once; then surface to Fight for target decision. **Never** silently converts to a walk (blind-click fallback deleted) |
| `PanelClick` | uiClick physical px pick → place | panels-unscaled law; pixels from `memory.Calib` with provenance | destination cell changed **∧** `LocationCursor` empty (the axe lesson, universal — autoequip's contract generalized to every panel op) | cursor occupied → put-back at origin, `ResDeaf`, abort pass, flag calibration suspect |
| `MenuNav` | approach → hover-confirm NPC body → interact → poll UIBytes[0xF4] → keyhold Down/Enter | menu byte oracle; keyhold override law | menu byte flip within deadline — **including the happy path** (the unbounded phase-2 loop is unrepresentable: `Budget()` is part of the interface) | `ResDeaf` → step-away/re-approach once with hover confirm, then abandon errand with verdict |
| `Pickup` | hover-confirm item → click | hover law | ground-gone ∧ inventory-gain ∧ cursor-empty | partial success states distinguishable; `ResDeaf` on rode-the-cursor |
| `CastAt` | select skill → readback `RightSkill` verify → aimed click at *fresh* coords | selection-flip law; quantity floor for throwables | selection confirmed pre-click; evidence window post | one strike pipeline — farmbot's six unequal paths collapse to `HoverStrike`/`CastAt` |
| `BeltDrink` / `CastTP` | key lane press | belt from flags (KeyBindings dead) | HP delta / portal object appears | portal absent → `ResDeaf`, retry once, then no-TP verdict to holder |

The stale-coordinate disease (sx,sy computed from a seconds-old tick snapshot, clicked by four of six paths) is dead by construction: aimed verbs are the only click path and they must call `Verifier.FreshSelf()` inside `Start`.

---

## 5. Journey — the single movement authority

Adopts the paid-for algorithms wholesale (Navigator's inflated-A*/string-pull/monotonic-progress; Mover.Step's farthest-visible carrot, click gait, fat/thin ray) and fixes ownership:

```go
type Goal struct {
    Kind    GoalKind   // Point, Unit, AreaCrossing, Portal, Entrance
    Pos     data.Position
    Area    area.ID
    Arrive  float64    // arrival radius
}

type JourneyStatus struct {
    State   JState     // Moving, Arrived, BlockedByBody, NoPath, Stalled, Abandoned
    Blocker UnitRef    // when BlockedByBody: violence is the nav fallback — but the ACTIVITY decides
    Tried   []Escape   // memory of escape attempts (the pond-oscillation cure: escapes are recorded, not Groundhog-Day'd)
}

func (j *Journey) Step(l *motor.CursorLease, s *percept.Snapshot, v *percept.Verifier) JourneyStatus
```

Design points, each answering a named defect:

1. **One stuck clock.** The 7+ independent clocks (navigator 2.5s, unstick 2.5/6s, goto 40s, candidate 25s, ring 8s, orbit 12s, stall 5s) collapse into one escalation ladder owned by Journey: `carrot → replan → localEscape(with memory) → frontier detour → Stalled`. The ladder's rungs are ordered and each attempt is recorded in `Tried`; the same escape is never repeated from the same pose.
2. **Blocked is reachable.** Mover's dead `MoveBlocked` branch (bestAt reset immediately before the check) is fixed; `BlockedByBody` is a first-class return the holding activity must handle — Fight spawns a strike (fightThrough's *doctrine* survives; its private fifth attack path does not).
3. **Exit-seek and crossing-candidates unify.** One `AreaCrossing` goal kind owns candidate selection (room-graph borders, nearest-first, per-candidate held-time budget, failed-candidate sit-out), the entrance ritual (hover-confirmed), and the transpose-validated map prior (validation at ingestion in `percept`/`memory.atlas`, not at the consumer).
4. **Obstacles are a versioned set.** Changing the set invalidates plans explicitly (plan carries obstacle epoch) instead of navWalk rebuilding everything per call under a plan that never hears about it.
5. **The rotating compass and confinement break-out are deleted.** No second authority guesses blind; escalation belongs to the owner.
6. **Town roads are atlas data** (provenance: hand-piloted, measured 2026-07) with Journey as the sole consumer. The two-consumers-one-cursor bug class is gone.

---

## 6. Arbiter — one decision, with commitment

### 6.1 Demands

```go
type Priority uint8
const (   // strict class ordering — LAW 1's fixed spine
    ClassSurvive Priority = iota // chicken/flee, hazard-pocket escape, sentinel Resupply (belt-empty + HP pressure + safe heal in reach)
    ClassRecover                 // corpse recovery, respawn, naked re-arm
    ClassFight
    ClassLoot                    // unified pickup (absorbs critloot + armloot)
    ClassTravel                  // route progression, area crossing
    ClassService                 // town trips, vendor, identify, cube-stash, autoequip
    ClassExplore
)

type Demand struct {
    Who     ActivityID
    Class   Priority
    Urgency float64        // ranks within class only — cross-class promotion is FORBIDDEN
    Commit  Commitment
}

type Commitment struct {
    MinHold      time.Duration // held-time before a same-class rival may evict
    SwitchMargin float64       // rival must exceed holder's urgency by this much
}
```

Two hard rules learned from farmbot's scars:

- **Cross-class promotion is forbidden.** Farmbot's herb-vs-corpse collision was "solved" by editing critloot's trigger — a pairwise patch. Here the conflict is resolved once, by class: `Recover > Loot`, so corpse recovery always outbids herb-chasing; and the genuinely survival-relevant case (belt empty, HP falling, heal on ground, no contact) is a *Sentinel-raised* `ClassSurvive` Resupply demand — principled, not a hoisted clone.
- **Everything bids.** Town trips, trap escape, cube stash, autoequip, corpse recovery — all activities with demands. The bespoke `oppFight` boolean, the tpPhase machine that starved survival for whole trips, the position-preempting trap escape: all unrepresentable, because there is no position to preempt from.

### 6.2 The decision function

```go
func (a *Arbiter) Decide(s *percept.Snapshot, demands []Demand, cur *Grant) Grant {
    best := highest(demands) // max by (Class asc as priority, then Urgency desc)

    if cur == nil || cur.State().Terminal() { return a.grant(best) }

    // 1. Fear overrides dwell: a strictly higher CLASS preempts immediately.
    if best.Class < cur.Demand.Class {
        cur.Pause(PreemptedBy(best.Who))       // clean release of leases; HeldClock stops
        return a.grant(best)
    }
    // 2. Same class: hysteresis. Evict only past MinHold AND by SwitchMargin.
    if best.Class == cur.Demand.Class && best.Who != cur.Who {
        if cur.Held() >= cur.Demand.Commit.MinHold &&
           best.Urgency >= cur.Demand.Urgency+cur.Demand.Commit.SwitchMargin {
            cur.Pause(OutbidBy(best.Who))
            return a.grant(best)
        }
    }
    // 3. Otherwise the incumbent keeps the actuator. Commitment is an arbiter
    //    property — never a blocking sleep.
    return *cur
}
```

The Grant carries the motor leases; `activity.Step(StepCtx{Lease, Snapshot, Verifier, Verbs, Ledger, Held})` runs one bounded slice. Preempted activities receive `Pause` (must release verbs, may checkpoint) and later `Resume` — with their `HeldClock` intact, so progress deadlines survive preemption without any hand-written reset lists. Per-area state lives *inside* activities and journeys keyed by area; the area-crossing event tears down journeys naturally (their goal area no longer matches) instead of a 14-item manual reset block.

Farmbot's posture hysteresis (dwell + margin + fear-overrides-dwell) is thus promoted from a combat-local low-pass filter to the arbiter's universal switching law.

---

## 7. Activities — explicit state machines with honest verdicts

```go
type Activity interface {
    ID() ActivityID
    Demand(s *percept.Snapshot, mem *memory.Store) *Demand // nil = no bid this cycle
    Step(ctx StepCtx) Verdict                              // Running | Yield | Done(why) | Abandoned(why)
    Pause(r PauseReason)
    Resume()
}
```

`Done` and `Abandoned` are *verdicts with evidence*, appended to the ledger. An activity that gives up says why (unreachable / timebox / deaf / outbid), and the route layer, atlas, and nightly report consume that.

### 7.1 Fight

```
        ┌──────────────┐
        │ SelectTarget │◄────────────────────────┐
        └──────┬───────┘                         │
     target?   │ no → Verdict: Done(no targets)  │
               ▼                                 │
        ┌──────────────┐   posture=regroup/kite  │
        │   Posture    │────► journey to kite    │
        └──────┬───────┘      point (Survive     │
       engage  │              handles true flee) │
               ▼                                 │
        ┌──────────────┐  BlockedByBody(u)       │
        │   Approach   │────► strike u first ────┤
        │  (Journey)   │                         │
        └──────┬───────┘                         │
      in range │                                 │
               ▼                                 │
        ┌──────────────┐  Outcome: Whiff/Deaf    │
        │    Strike    │────► evidence ledger ───┤
        │ (HoverStrike │                         │
        │  / CastAt)   │                         │
        └──────┬───────┘                         │
               ▼                                 │
        ┌──────────────┐ no evidence for budget  │
        │   Assess     │────► blacklist(target,  │
        │              │      cause) ────────────┘
        └──────────────┘
```

- **Targeting** keeps score-over-nearest with the raiser/elite/spread terms — but build terms attach to *skill metadata resolved from `combat.Capability`* (Rabies terms exist iff the character provably has Rabies selected-and-bound), not to a launch-flag string whose default silently rabies-shapes every character.
- **Capability** is self-calibrating: at session start (and after weapon changes) it presses each configured skill key once and reads `RightSkill` back — a binding is believed only when proven, the Skills-map gate generalized. Curse pass (Amp) becomes a capability-conditional policy entry in the same pipeline, not a bespoke dead block.
- **Damage watchdog** runs on honest evidence only — target Mode transitions, poison state, corpse appearance, pet-swarm convergence — with a held-time budget. The give-up doctrine survives; the dead Life sensor that degenerated it into a flat 8s false-unkillable timer cannot be consulted (LAW 4).
- **Posture oracle** (`combat/threat.go`): v1 ships the calibrated arithmetic *as one named function* with named inputs, and — critically — every posture decision is ledgered with its inputs and whether the next 10 held-seconds contained a near-death. That is the substrate for replacing folklore constants with fitted ones (v2), instead of adding a memorial constant per death forever.

### 7.2 Corpse Recovery (`ClassRecover`)

Demand fires whenever a `death_state` fact exists in memory (scope: game-session, crash-safe — written by Sentinel at death detection).

```
 PlanApproach ──► TravelToArea ──► ApproachCorpse ──► SweepConfirm ──► ClickCorpse ──► VerifyRecovered
      │            (Journey,          (Journey;         (resumable        (verb with       (Corpse.Found
      │            town roads         threat oracle     13x7 hover        DOUBLE post-     flips; gear
      │            from atlas)        gate: don't       sweep verb,       condition:       check; clear
      │                               feed the camp     N probes per      1. self MOTION   death_state
      │                               — pack check      Step slice —      within 1.5s      fact)
      │                               before closing)   preemptible)      2. Found flip
      │                                                                   within budget)
      └── all states share ONE held-time timebox (3 min held); on expiry → Abandoned(timebox)
```

The deaf-click cure, explicitly: farmbot clicked the corpse ten times, double-hover-confirmed, at constant dist=6 — the click registered but the game-internal walk it delegates was pond-blocked, and nothing watched for motion. Here `ClickCorpse`'s postcondition #1 is **self-motion**: no displacement within 1.5s ⇒ `ResDeaf` ⇒ the activity does not retry the same click — it asks Journey for a *different approach bearing* (recorded in `Tried`), and after 3 deaf outcomes from distinct bearings it wedges the blocked approach in the atlas (scope: seed) and routes around. The blind-click fallback is deleted.

Proven pieces retained as-is: position memory with gone-streak (dist≤10, 8-tick), the cross-area scope law (Corpse.Found only read in the corpse's area — typed into `percept.CorpseState`), pack-assessment gate on approach.

### 7.3 Town Errand (`ClassService`) — and the TownTrip parent

`TownTrip` is an activity composing verbs, not a phase machine above survival:

```
 CastTP (BeltCast: portal object must appear ≤2s, retry once)
   → EnterPortal (hover-confirm portal, interact, postcondition: AREA CHANGE ≤ 8s — the
     happy path has a deadline because Budget() is not optional)
   → run child errands (each an activity slice)
   → ReturnPortal (same contract)
 Walk-fallback for TP-banned areas is a Journey goal, not tpPhase 4/5 pinning a global.
```

Vendor errand, rebuilt on the proven cores and stripped of superstition:

```
 EnsureClear (MenuOpen? → ESC via MenuNav with byte-flip postcondition)
   → TravelToNPC   (Journey + atlas road; anchor is a prior, arrival is NPC unit loaded)
   → EngageMenu    (MenuNav verb: HOVER-CONFIRM the NPC body — replaces the 6 guessed
                    pixel offsets and the step-away dance; UIBytes[0xF4] postcondition;
                    keyhold Down+Enter for dialog nav — both proven channels kept)
   → SellPass      (per item: PanelClick pick → PanelClick drop, postcondition
                    origin-empty ∧ CURSOR-EMPTY — the sell pass finally gets the
                    autoequip contract; cursor-occupied ⇒ put back, abort pass,
                    Outcome ResDeaf with cell evidence)
   → IdentifyPass  (identifyErrand generalized — it was already the template:
                    skill-presence precondition, world right-click ID cursor,
                    per-item Identified-flag closed loop)
   → CloseMenus → Done
```

- The left-click buy experiment stays an *experiment*: a `calib` probe run under an explicit flag, whose outcome (inventory delta) is ledgered; it graduates to a verb only on a proven closed loop. The panel right-click deaf negative is recorded in `memory` as a law (provenance: proven-negative) so nobody rediscovers it.
- CubeStash keeps its proven mechanics (direct cube key, keeper filter, put-back-on-failure) and gains the cursor-occupancy postcondition; it is `ClassService` with an inventory-pressure urgency — it can no longer freeze the loop mid-field because it is a state machine stepped under lease like everything else, and Sentinel keeps running regardless.
- Autoequip is ported nearly verbatim — it was the only interaction in farmbot with a complete contract, and its contract (effect check + anomaly check + rollback + self-disable) is now the *mandatory shape* of every panel verb.

---

## 8. Memory & Ledger — the learning substrate

### 8.1 The store (LAW 5)

```go
type Scope uint8
const (
    ScopeTick Scope = iota; ScopeActivity; ScopeArea; ScopeGame; ScopeSeed; ScopeForever
)

type Provenance struct {
    Source   string // "measured" | "derived" | "prior" | "proven-negative" | "hand-piloted"
    Evidence string // log ref, calibration date, commit
}

type Fact struct {
    Key   string; Scope Scope; Prov Provenance
    Val   []byte; At time.Time
}

func (s *Store) Put(f Fact)          // appends to WAL; Scribe fsyncs ≤1s — a wedge learned at 03:12 survives the 03:13 crash
func (s *Store) Get(key string) (Fact, bool)
```

What moves in, with scopes:

| Fact | Scope | Today in farmbot |
|---|---|---|
| refusal wedges, phantom exits, deaf-approach bearings | Seed | atlas, flushed only at graceful exit — lost on crash |
| town roads, panel pixel calibrations, dpi scale | Forever | hardcoded from single-day calibrations |
| respawn key | Forever | re-probed esc/enter/esc at every death |
| townHubPos, NPC anchors | Seed | re-learned per process |
| death_state (area+pos) | Game | file, kept — now written by Sentinel |
| routeIdx / progression gauges | Game | loose globals |
| proven-negative laws (panel right-click deaf, Life frozen) | Forever | comments |

### 8.2 The ledger (LAW 3 + M5)

Every verb `Outcome`, journey `Stalled/Abandoned`, activity verdict, and posture decision is appended as structured data. In-band consumers:

- **Blacklist** — built from strike outcomes (no evidence within budget), cause-tagged.
- **Atlas wedges** — built from journey escape exhaustion and deaf-approach outcomes.
- **Calibration suspicion** — repeated `ResDeaf` on a `PanelClick` cell flags the pixel calibration instead of silently eating a night.
- **Nightly report** — outcome histograms per verb/activity replace grepping 13,807-line logs; the human learning loop gets structured input, and each new "measured law" lands as a `Fact` with provenance instead of a comment.

### 8.3 The Self-Model — the bot knows and is aware of itself (owner requirement, verbatim)

The self-model is not a new subsystem; it is the *named union* of three layers above, and every decision consumes it rather than raw reads:

```go
// Assembled once per cycle from Snapshot + Store + Ledger; the only "who am I" API.
type Self struct {
    // WHO I AM (Snapshot.Me + proven facts)
    Class      data.Class
    Level      int
    Stats      StatBlock          // via BaseStats (the live path)
    Skills     combat.Capability  // skills I provably have + keys PROVEN to select them
    Equipped   map[Slot]ItemRef   // per-slot, with resolved TRUE identities
    Inventory  []ItemRef          // mod name-remapping resolved once, cached as ScopeGame facts
                                  //   ("Jawbone"@(0,0) IS TomeOfTownPortal — never re-derived)
    Belt       BeltState
    Gold       int
    Charges    map[skill.ID]int   // TP/ID tome charges, javelin quantity

    // WHAT I NEED (computed, drives Demand urgencies)
    Needs      Needs              // Armed, Healed, Leveling, Solvent, Repaired ∈ [0,1]

    // WHAT I'VE DONE (ledger views)
    Deaths     []DeathFact        // where, to what, how many times (the camp counter is
                                  //   a remembered fact, not a hardcoded shun radius)
    Attempts   AttemptIndex       // per-goal outcome history: recovery bearings tried,
                                  //   areas that starved XP, escapes that failed from a pose
}
```

Rules that make it *awareness* rather than a cache:

- **Changes are events.** The perceptor diffs `Self` across cycles; a lost weapon, an emptied belt, a level-up, a vanished tome each raise a typed event the arbiter sees the same cycle. "My weapon is gone" spikes `Needs.Armed` — no block ever discovers nakedness by accident again.
- **Needs, not block order, set urgencies.** A weaponless bot pursues arming because `Needs.Armed≈1`, choosing among known paths (ground gear / corpse / vendor) by their *remembered cost in Attempts* — the six-death camp carries its price tag.
- **Identity facts persist.** Proven key bindings, resolved item identities, the respawn key, panel calibrations: `ScopeForever`/`ScopeSeed` facts. A fresh character on the same seed inherits the world knowledge and re-proves only what is character-bound (bindings, skills) via the M4 capability calibration.

---

## 9. Migration Plan & Build-Out Order

**Rules:** `cmd/farmbot` is frozen as reference. Azbot reuses `internal/game` + `internal/gear` read-only; the only shared-code change permitted is *adding* failure semantics to `d2go` offset resolution (returning misses instead of arithmetic-from-zero), which farmbot ignores and azbot gates on. Each milestone ends with a soak criterion before the next begins.

- **M0 — Attach & Epistemics (gate).** `cmd/azbot` attaches, runs pattern audit, prints the AttachReport, refuses to proceed if a required channel is unresolved. *Soak: 112 simulated re-attaches, zero garbage offsets consumed.*
- **M1 — Perceive & Survive.** Perceptor snapshot loop + Sentinel goroutine + Scribe. Bot stands in town: drinks on HP threshold via key lane, detects death by Mode, writes heartbeat + death_state facts, survives load-screen dropouts. *Soak: overnight idle with induced deaths; zero silent gaps > 2s in heartbeat.*
- **M2 — Motor & Verbs.** Cursor scheduler, Stride, PanelClick, BeltDrink, MenuNav. Bot strides a fixed town road (atlas data) to the gate and back; every stride outcome ledgered. *Soak: 500 strides, displacement postconditions ≥98% Done, zero unscheduled cursor touches.*
- **M3 — Journey.** Navigator + mover executor adopted; escalation ladder; area crossing town↔Blood Moor both directions, wedges learned and crash-flushed. *Soak: 100 crossings incl. forced kills mid-run; wedge count monotone across restarts.*
- **M4 — Fight.** Capability calibration, targeting, one strike pipeline, posture v1, arbiter with Survive/Fight/Travel demands live. *Soak: Blood Moor clear-and-survive night; blacklist causes all evidence-tagged; zero stale-coordinate clicks (fresh-read enforced by type).*
- **M5 — Loot.** Unified pickup activity (absorbs critloot/armloot with urgency terms: heal-when-belt-low, wearable-when-slot-empty); Sentinel Resupply demand. *Soak: weaponless-recovery scenario replay — run-18/24/26 death patterns do not recur.*
- **M6 — Recover.** Corpse recovery machine incl. deaf-click bearing rotation; respawn activity using the remembered key. *Soak: scripted deaths at the pond corpse spot from overnight_20 — recovery succeeds via bearing change.*
- **M7 — Services.** TownTrip, identify (first — it is the template), sell, cube stash, autoequip port. *Soak: full errand cycle under induced mid-errand chicken; Sentinel latency < 300ms throughout.*
- **M8 — Progression.** Travel/Explore demands, route gauges (density-aware — the 150s runs-dry timer replaced by kill-rate normalized to spawn density), first full farm loop town↔Blood Moor↔Cold Plains.

---

## 10. Defect Ledger — every audited defect, and where it becomes impossible

| # | Defect (audit ref) | Killed by |
|---|---|---|
| 1 | Decision rate = action duration; 80ms tick floor patch (L0-01) | §1: Executive cadence decoupled from verb/activity duration; sleeps forbidden outside motor |
| 2 | Town trip runs above survival; 48-min silent hang (L0-04, L3-1) | §1 Sentinel goroutine; §7.3 TownTrip is a leased activity; `Verb.Budget()` bounds the happy path |
| 3 | Survival as queue position, blind during any block sleep (L0-06, M2) | §1: Sentinel never blocks, key-lane drinking needs no cursor |
| 4 | Town guard parked-forever via stale `curGoto` (L0-05, L0-09) | §6: no shared trigger globals; demands recomputed from snapshot+memory each cycle |
| 5 | Hand-maintained 14-item area reset list (L0-07) | §6.2: per-area state owned by activities/journeys, torn down by goal invalidation |
| 6 | Trap escape preempts by position (L0-08) | §6.1: hazard escape is a `ClassSurvive` demand |
| 7 | critloot pairwise corpse guard; herb-vs-corpse (L0-10, D1) | §6.1: strict class ordering, `Recover > Loot`; Sentinel Resupply for the survival case |
| 8 | armloot clone of unreachable loot block (L0-11) | §7 M5: one Loot activity with urgency terms; nothing is unreachable because nothing is positional |
| 9 | critloot/armloot shared progress clocks (K3) | §6.2 `HeldClock` per activity, owned by arbiter |
| 10 | Cube stash blocks the loop for seconds (L0-12) | §7.3: stepped state machine under lease |
| 11 | Dead `MoveBlocked` branch; fightThrough unreachable (L1-1) | §5.2: `BlockedByBody` is a first-class Journey verdict |
| 12 | navWalk per-tick rebinding/obstacle churn (L1-3) | §5.4: versioned obstacle set with plan epochs |
| 13 | Exit-seek vs crossing-candidates competing (L1-4/5) | §5.3: one `AreaCrossing` goal owner |
| 14 | Beeline sleep-the-loop commitment (L1-6) | §6.2: commitment is arbiter hysteresis, never a sleep |
| 15 | Held-key re-aim contradiction of the key-edge law (L1-8) | §4.3: Stride is the only locomotion verb, edge law in the type |
| 16 | Unstick compass second-guessing the Navigator (L1-9) | §5.5: deleted; one escalation ladder with attempt memory |
| 17 | 7+ independent stuck clocks (L1) | §5.1: one ladder, one clock |
| 18 | Resume-reset bug hand-fixed three times (D2) | §1.3: held-time deadlines pause with preemption structurally |
| 19 | Rabies terms keyed to flag default "f2" (L2-1) | §7.1: build terms attach to proven capability metadata |
| 20 | Six attack paths, four clicking stale coords unverified (L2-4) | §4.3: two aimed verbs, fresh-read enforced by construction |
| 21 | Frozen-Life damage watchdog false-blacklisting (L2-9) | LAW 4: Life field unrepresentable; evidence-based watchdog on held time |
| 22 | Posture folklore accreting a constant per death (L2-2) | §7.1: v1 instrumented via ledger for fitted v2; constants named, decision outcomes recorded |
| 23 | hoverAim 1.1s open-loop freeze, fixed pixel offsets (L2-5) | §4.3: incremental sweep across Polls; offsets from `memory.Calib` (dpi-aware) |
| 24 | Blind-click fallback converting failed attack into a walk (L2-5) | §4.3: deleted; `ResWhiff` surfaces to Fight |
| 25 | Sell/cube "origin empty = success" cursor hole (L3-3/6) | §4.3 PanelClick: cursor-occupancy postcondition universal |
| 26 | Six guessed NPC body-click offsets + approach dance (L3-2) | §7.3: hover-confirmed NPC engagement in MenuNav |
| 27 | Deaf corpse clicks → death (L3-7, D3) | §7.2: self-motion postcondition + bearing rotation + seed-scoped wedge |
| 28 | Four private panel detectors + ESC folklore (K5) | §3.1: one MenuOpen oracle (UIBytes[0xF4]) |
| 29 | Five PATTERN-NOT-FOUND channels serving garbage (L4) | §3.1: attach gate; dead channels unrepresentable |
| 30 | KeyBindings/OpenMenus assembled every tick though dead (L4) | §3.1: not read, not surfaced |
| 31 | Vacuous Life>0 clause as next-repack landmine (K2) | LAW 4: no Life field to consult |
| 32 | 164 GetData sites, decision/verify indistinguishable (M4) | §3.2: Snapshot vs Verifier, compile-time separation |
| 33 | Cursor sensor/effector/steering conflation (M1) | §4.1: role-tagged, time-boxed reservations; moveStop by construction |
| 34 | Nobody owns time; 191 sleeps (M2) | §1: three goroutines with time contracts; sleeps CI-banned outside motor |
| 35 | Crash loses the night's learned wedges; respawn key re-probed forever (M3, D5) | §8.1: WAL Put with ≤1s fsync; scoped facts incl. respawn key (Forever) |
| 36 | Learning entirely out-of-band, laws living in comments (M5) | §8.2: outcome ledger + provenance facts as the in-band substrate |

---

## 11. What Is Deliberately *Not* Being Rebuilt

- Navigator/mover algorithms, hover ritual, key-edge stride, UIBytes oracle, identify errand, autoequip contract, corpse position memory, transpose validation, seed-keyed atlas: **adopted, not re-derived** — they are paid-for measurements.
- The stash object: stays unbuilt; the 12x8 cube remains the de facto stash (strictly better today; travels with the character). Object-identity work is deferred until a ledgered need appears.
- Threat-model machine learning: v2 is *fitting named constants against ledgered outcomes*, not a model. The intelligence the owner ordered comes from closed loops and remembered evidence, not from cleverness in any single decision.

The fish died because it had thirty reflexes and no mind: no memory of what it tried, no evidence of what worked, and no owner of its own time. Azbot has one arbiter, one clock discipline, one truth about the world, verbs that refuse to lie about their outcomes, and a memory that survives the crash. That is the difference between twitching and behaving.