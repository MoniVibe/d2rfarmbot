# AZBOT v2 — the bot always knows what state it is in

Status: plan, 2026-09-24. Supersedes the executive parts of AZBOT_DESIGN.md §1, §6, §7,
which were only partly built (see "Why" below). Synthesized from two independent
design passes plus three code audits on `laptop-3.2` @ 21b69b9.

## Why

The owner's symptoms map to measured code faults:

| Symptom | Cause in code |
|---|---|
| Hugs walls | Navigator `Diverged` carries no target; Journey strides to world (0,0) (journey.go/navigator.go). Carrot ellipse bends heading ≤20° (stride.go isoCarrot). A* expands through walls (pather/astar). `steerAround` is a second steering authority. |
| Gets stuck | Blocked stride → replan resets the stuck clock, so the same spot replans forever; escapes are fixed cardinal offsets; monsters/objects never reach the planner. |
| Opens menus | Blind ESCs (relog ×3 on a dead QuitMenu flag, respawn every 2.5s, cursor drop, watchdog probe, equip/spend). Identify leaves the inventory open (closeShop can't see it). Spend presses C/T blind. ClickMove right-clicks with the ID tome selected. Blind attack clicks pushed onto the bottom HUD. |
| "Confused" | No model of the screen; nothing checks it before clicking. Activities have no Suspend/cleanup, so a preempted task leaves its panel open. Eight mechanisms act above the arbiter and the holder never learns why. State is ~10 bools per activity + ~15 package globals; logs show grants but not reasons or phases. |

## The layered state model

Each layer has exactly one owner. Evaluated top-down every tick; a layer that is not
"ready" acts itself and nothing below it acts that tick (the veto rule).

| L | State | Owner |
|---|---|---|
| 0 Session | Attaching, InGame, Relogging{phases}, Chicken, Disengaged | `exec.Session` FSM |
| 1 Mode | Unknown, Loading, World, Dead | `screen.Tracker` fused with percept |
| 2 UI | Panels bitmask, CursorItem, Unsure | `screen.Tracker`; acted on by the Janitor |
| 3 Mission | Hunt{leg}, TownVisit{docket}, Recover{corpse} | `director` (later step) |
| 4 Activity | holder, class, held time | arbiter |
| 5 Phase | typed enum per activity | the activity |
| 6 Verb/Nav | verb in flight, nav follower state, cursor lease | verbs, `nav` |

Truth sources: the SCREEN decides panels; memory decides Valid and HP. On disagreement
Mode is Unknown for ≤1s, then the screen wins. Unknown/Unsure ⇒ no clicks, never a blind key.

## Activity contract v2

```go
type Needs struct{ Mode screen.Mode; Claims screen.Panels; Cursor CursorPolicy }
type Activity interface {
    Name() string
    Demand(w *World) *arbiter.Demand
    Needs(w *World) Needs
    Begin(c *Ctx, resumed bool)
    Step(c *Ctx) Status            // ≤120ms; long waits are Status{V: Wait, WakeAt}
    Suspend(c *Ctx, why Reason)    // MoveStop only; no new clicks
    End(c *Ctx, v Verdict, why Reason)
}
```

- Verdicts: Running, Wait, Blocked, Done, Abandoned — always with a typed Reason.
- Every activity has a Stringer phase enum driven through `Phaser.To(next, why)`, which
  logs one line and supports per-phase held-time budgets (overrun ⇒ Abandoned(Timebox)).
- UI ownership: an activity may open a panel only when the Reading shows it closed, and
  declares it in `Claims`. Claims live only while it holds the grant; on Suspend/End the
  executive drops them, the panel becomes foreign, and the Janitor closes it by sight.
  Cleanup is guaranteed by the executive, not remembered by each activity.
- Held time is kept per activity across preemption (arbiter `held` map); Blocked and
  Suspended time does not count.

## Executive tick

1. Perceive: snapshot → `screen.Observe` → `Tracker.Update`; publish the Reading atomically
   for the Sentinel.
2. Session.Step — if not InGame, only the session acts.
3. Mode gate — which activities are eligible.
4. Pure observers (grid, cartographer, trail...) — never actuate.
5. Demands from a fixed registry (deterministic ties) → arbiter.Decide → Change{From,To,Reason}.
6. Gate: holder's Needs vs Reading. Foreign panel or foreign cursor item ⇒ ONE Janitor
   action this tick; holder is Blocked (clock paused). Otherwise Step.
7. Verdict handling, claim release, benching by reason.
8. Monitors (watchdog, mute, deadman) write prescriptions/demands for next tick — never act.
9. Trace: transitions, 1 Hz state line, flight frame.

Survival: belt keys stay in the Sentinel and work with panels open, except Chat (would
type) and PauseMenu (game frozen) — there it raises an urgent Janitor flag instead.

## Where the above-arbiter mechanisms go

| Now (main.go) | v2 |
|---|---|
| Pause sentry, startup hygiene, MenuSanctionUntil, safeEsc, closeShop/EnsureWorld | Janitor |
| Cursor-item drop (blind click + ESC) | Janitor cursor rule: park in inventory if open, else drop at feet; no ESC |
| Watchdog escape strides, pocket breaker, ESC probe, deadman/pacer | pure watchdog verdicts → `Unstick` activity (ClassRecover): Stride → CastTP → AwaitPortal → Enter. ESC probe deleted |
| Idle breaker, CoolAllServices | per-activity benching by reason; Director ends TownVisit |
| Stall alarm | arbiter mute policy (no outcome and no phase change for 2× bar ⇒ End(Mute)) |
| Capability recalibration F1–F8 | `Calibrate` activity: world, no panels, no enemies within 15 |
| Relog ×3 RealEsc | Session Relogging phases; one ESC only when Reading is World with no panels |
| Respawn ESC every 2.5s | kept, only when Mode=Dead |

Guard tests (Linux): ESC (0x1B/RealEsc) allowed only in janitor/session/respawn files;
`time.Sleep` count per file may only go down (ratchet).

## Observability

- Transition line: `T L=ui from=World|Inv to=World by=janitor why=close:Inventory tick=48213 hold=fight/Strike`
- 1 Hz state line: `S 12:03:04 #48213 ses=InGame mode=World ui=- cur=- hold=fight/Strike held=7.2s gate=ok nav=Follow(5/12) hp=64 mp=30 pos=5012,4431`
- Flight recorder gains decision frames (Reading, bids, grant+reason, gate, phase, verdict);
  one shared activity registry for live and `-replay`; injected clock so decision replay
  runs as a Linux test over `testdata/flights`.

## Migration (bot runnable after every step; each verified by a Linux test or a named live drill)

1. **nav + Journey rewrite; carrot fix** — Linux tests. Live: corridor walk, post-fight return to path.
2. **screen package** (pure; ported detectors, Tracker, CloseStep) — Linux golden tests.
   Real detectors for the remaining panels once relay R2 captures land.
3. **Arbiter v2**: held map, deterministic ties, Change/Reason, Bench, mute policy — Linux tests.
4. **Registry + contract v2 + legacy adapter + Phaser + trace/state line** — Linux exec tests
   with fake activities. Live: 15-min "state-line soak".
5. **Screen wired read-only** — Live: "panel census" (owner opens each panel, state line names it).
6. **Janitor + gate**; delete sentry and blind ESCs — Live: "stray panel drill" (owner presses
   I/C/T/ESC/Enter while engaged; each closed within 1s, no toggle loops).
7. **Services onto contract v2** (phase enums, claims, sleeps removed; Identify/Equip/Spend first) —
   Live: town docket run + preempt drill.
8. **Session FSM + Relog** — Live: relog drill, death→relog.
9. **Pure watchdog + Unstick** — Linux synthetic traces + replayed wedges. Live: known wedge site.
10. **Calibrate, Director, step-budget enforcement** — Live: overnight soak with acceptance:
    no gate=blocked >2s, zero ESC outside allowed files, zero mute releases.
11. **Advance / errand walkTo / Reclaim onto nav**; HUD no-click zone; ClickMove tome check;
    Interception extended-key flag.

## Progress (branch `claude/diablo-bot-states-x834xf`, PR MoniVibe/d2rfarmbot#1)

Live testing goes through the relay (docs/RELAY.md). `tools/test.sh` is the Linux gate
(pure tests natively, Windows tests under wine, Windows build + vet).

| Step | State | Proof so far |
|---|---|---|
| 1 nav + Journey, carrot angle | merged | Linux tests; R6: 0% zero-gain strides in all focus modes, heading 2–9° |
| 2 screen oracle | merged | 15 sight detectors, 0 errors on 34 real captures (R2); 1.5 ms/frame |
| 3 arbiter v2 | merged | Linux tests |
| 4 contract v2, registry, trace/state line | merged | Linux tests; awaiting R7 |
| 5 screen shadow mode (+ cursor before/after each flip) | merged | awaiting R7 Part A |
| 6 janitor + gate (`-Janitor`) | merged, OFF by default | awaiting R7 Part B |
| 7 town services on v2 (phases, claims, no sleeps) | merged | Linux/wine tests; awaiting R7 Part C |
| 8 Session FSM + relog | merged | Linux tests; awaiting R7 Part D |
| 9 pure watchdog + Unstick | merged | synthetic traces; awaiting R7 Part C |
| 10 Calibrate, Director, step budgets | not started | |
| 11 nav for Advance/errand/Reclaim; HUD zone; tome check; loot probe | HUD/tome/loot merged; nav migration pending R6b (cursor reach) | |

Also merged: route plausibility + ladder that acts (phantom Halls door, R1 run w);
zero-size screenshot guard; Stand exempt from stuck; DC leak fix; town idle valve.

Open, measured: an 80 ms tap travels ~7 tiles because the game walks to the cursor
point (R6) — `Stride.Reach` + `stridecal -reach` exist; R6b decides the follower's reach.
