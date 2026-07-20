# Yes. You are closer than the symptoms make it look.

Your bot already has several ingredients that many automation projects never reach: a structured executive, typed postconditions, a watchdog, persistent learned facts, a flight recorder, and explicit activity arbitration.  The problem is not simply “bad navigation.” It is **three feedback loops amplifying one another**:

1. The executive changes holders too readily.
2. The path follower can select targets behind its current progress.
3. Movement and UI commands are sometimes ignored, but higher layers react as though they were meaningful.

That combination produces the observed dancing, pocket staring, portal boomerangs, and seemingly random priorities. 

A strong autonomous bot is largely an **anti-oscillation machine**. It does not need to choose perfectly every tick. It needs to avoid changing its mind because of one noisy read.

---

# The architecture I would use

Separate the bot into four ownership layers:

```text
MISSION
"Reach and activate the Stony Field waypoint"

    ↓

TACTIC
Travel / fight / recover / interact / service

    ↓

NAVIGATION CONTROLLER
Road follower / seam crossing / local avoidance / pocket escape

    ↓

MOTOR
Ground click / force-move / interact / keypress

    ↓

VERIFICATION
Position changed? UI changed? Area actually crossed?
```

The critical rule is:

> A lower layer may fail or switch methods without erasing the higher-layer intention.

For example, fighting at a doorway should temporarily replace the **travel tactic**, but it must not replace the mission, discard the route, reset breadcrumb progress, or choose the opposite doorway afterward.

Your current class order is reasonable. The weakness is probably grant churn and the fact that roughly 20 activities are competing too directly. 

---

## 1. Make the mission persistent and tactics interruptible

Do not let `Fight`, `Loot`, `Travel`, `Explore`, and `Service` all behave like equal-sized goals.

Use two arbitration levels:

### Mission selector

Runs slowly, perhaps once per second or when the current mission becomes invalid.

Examples:

* Recover corpse
* Reach named area
* Activate waypoint
* Complete town service
* Clear current objective
* Explore frontier

### Tactic selector

Runs at the executive tick rate and decides how to continue the current mission:

* Move
* Fight
* Use potion
* Escape geometry
* Pick up nearby item
* Interact with mission object

The mission should carry a **resume token**:

```text
mission: Reach Stony Field waypoint
route edge: Cold Plains → Stony Field
breadcrumb progress: 71%
seam phase: approach
last forward anchor: (x, y)
```

After combat, the bot resumes from that token. It does not ask the whole activity population what life means now.

### Add leases and switching hysteresis

Keep the current tactic while it remains valid unless:

* A hard preemption occurs: death, critical health, menu lock.
* The tactic reports completion.
* The tactic reports invalidity.
* It exceeds a progress timeout and recovery has been attempted.
* A higher-priority demand persists for several observations.

A candidate should not win because it was slightly stronger for one tick. Require either a priority-class increase or a meaningful score margin.

Conceptually:

```text
switch if candidate is a hard preempt
or current is invalid
or candidate_class > current_class and candidate persisted
or candidate_score > current_score + switching_margin
```

Add a switching cost for recently selected activities. That alone often removes “priority soup.”

---

# 2. Use hybrid navigation, not pure experience and not grid-first

Given your evidence, I would make the sources authoritative in this order:

1. **Successful experience:** roads, crossings, doors, safe anchors.
2. **Current movement feedback:** actual displacement and failures.
3. **Locally streamed collision information:** useful nearby, unknown beyond that.
4. **Static map geometry:** a prior or topology hint, never unquestioned truth.

The report shows that the live grid is incomplete, the mod map can be geometrically misaligned, and some fences do not exist in either source.  A global metric planner built on those inputs will confidently manufacture elegant nonsense.

### Global layer: topological route graph

Represent the world as anchors and edges:

```text
Cold Plains waypoint
    → known road
    → Stony transition approach
    → transition crossing
    → Stony clear-side anchor
```

Each learned edge should store:

* Direction
* Seed and mod/build scope
* Successful trajectory polyline
* Expected traversal duration
* Success and failure counts
* Known danger or stuck regions
* Entry and exit anchors
* Whether reverse traversal was independently proven

Do not assume a successful route is equally reliable backward. Store directional edges.

### Local layer: corridor following

Replay a successful road as a **corridor**, not as a chain of exact dots.

Exact breadcrumb targeting commonly causes zigzagging:

1. Character passes a breadcrumb.
2. “Nearest breadcrumb” becomes one behind the character.
3. Character turns around.
4. The next nearest breadcrumb becomes one ahead.
5. Dance recital begins.

Instead, project the character onto the route polyline and track an arc-length value, call it `routeS`.

```text
routeS = distance progressed along the learned road
target = point on road at routeS + lookahead
```

Outside an explicit recovery mode, `routeS` should be monotonic. Permit tiny measurement noise, but do not allow ordinary navigation to reduce it.

A practical rule:

```text
projectedTargetS < currentRouteS - rollbackTolerance
    → reject target
```

Use a lookahead point several tiles ahead instead of the nearest breadcrumb. Increase lookahead in open ground and shorten it near turns, doors, and local obstacles. This is much smoother than point-chasing.

### Local obstacle controller

When the direct lookahead click fails, sample alternatives in a forward fan:

```text
                desired direction
                       ↑
              ·    ·   ·    ·
                 ·  player ·
              ·             ·
```

Score candidate points using:

* Forward route progress
* Clearance
* Novelty
* Cross-track distance from the road
* Recent failure penalty
* Reverse-seam penalty
* Distance from monsters, when appropriate

The local controller only needs to reach the next useful corridor point. It does not need to globally solve the entire area.

---

# 3. Treat an area crossing as a transaction

The crossing drive is directionally correct, but it should be formalized as a state machine rather than a temporary movement preference.

```text
APPROACH
  ↓
ARM
  ↓
COMMIT
  ↓
CLEAR
  ↓
VERIFY
  ↓
RELEASE
```

## Approach

Move to a known pre-seam anchor. Clear doorway enemies before committing when practical.

## Arm

Latch:

* Intended source area
* Intended destination area
* Crossing direction
* Seam normal or entry-to-exit vector
* Pre-seam and clear-side anchors
* Route progress
* Transaction identifier

## Commit

The navigation transaction owns movement. Ordinary travel, loot, shrines, exploration, and nonessential combat cannot replace it.

Area reads are observations, not steering commands. Flickering area IDs must not reverse movement.

## Clear

Define progress geometrically. Let the crossing direction be unit vector `n` and the seam anchor be `p`.

```text
signedDistance = dot(playerPosition - p, n)
```

The crossing is not clear merely because the area ID changed once. Require:

* Positive signed distance beyond the clear threshold
* Increasing distance from the seam
* No recent motion back toward the source
* Destination area stable for several snapshots, when the read is trustworthy

Your existing 14-tile commitment can remain as an initial value, but apply it to **signed forward clearance**, not merely Euclidean distance from a changing target.

## Reverse taboo

After crossing, reject destinations behind the seam until one of these occurs:

* The mission explicitly requests returning.
* The bot has reached a safe clear-side anchor.
* Recovery determines that retreat is necessary.
* A substantial cooldown and distance threshold are satisfied.

This single rule eliminates many seam orbits.

## Combat during the crossing

Combat should have crossing-aware behavior:

* Before commit: clear blockers normally.
* During commit: only engage an immediate lethal threat.
* Do not chase a target behind the seam.
* If survival interrupts, preserve the crossing transaction and resume it afterward.

The mistake is letting combat decide the route.

---

# 4. Pocket recovery needs a ladder, not one escape trick

Your portal pocket breaker is valuable, but teleporting to town should be the final rung. It is expensive and can generate its own portal-loop pathology.

First distinguish **dead input** from **blocked geometry**. The report already indicates that ordinary ground clicks can sometimes produce no movement while force-move and interaction inputs still work. 

A movement command should produce one of these classified results:

```text
ACCEPTED_AND_MOVED
ACCEPTED_BUT_BLOCKED
NOT_ACCEPTED
PARTIAL_PROGRESS
WORLD_CHANGED
```

Do not mark a wall when the input was simply ignored.

## Recovery ladder

### 1. Retry with another motor verb

For example:

* Ground click
* Force-move
* Short hold
* Alternate input path

If another verb moves toward the same point, this is a motor reliability problem, not an obstacle.

### 2. Short radial escape

Try nearby points in an expanding ring or forward-biased fan. Favor:

* Points away from the most recent failed segment
* Points with novel headings
* Points with visible or inferred clearance
* Points that preserve route progress

### 3. Tangent or wall-follow behavior

When forward movement repeatedly fails, select a consistent side and travel tangentially around the inferred obstruction for a bounded duration.

Do not switch left/right every tick. Latch the chosen wall-follow side for the recovery attempt.

### 4. Backtrack to a freedom anchor

Continuously record the last position where:

* Movement succeeded in multiple headings
* Local clearance appeared high
* The bot was not in a seam band
* No recent stuck condition existed

Backtrack along the actual traveled trajectory to that anchor. This is one of the few modes where route progress is deliberately allowed to decrease.

### 5. Learn an experiential obstruction

Repeated failures from independent positions should form a temporary local obstacle.

A useful evidence progression:

```text
one failure:
    suspicious destination

repeated failure with accepted input:
    blocked segment

failures from multiple approach angles:
    inferred fence or pocket boundary
```

Scope it by seed and area. Decay weak evidence. Persist high-confidence barriers.

### 6. Portal reset

Only after local recovery is exhausted.

Record why the reset happened so the same pocket can be avoided rather than rediscovered twenty minutes later.

### Important watchdog change

**Bench failed strategies, not the mission.**

If ground-click movement fails, bench that motor verb temporarily. Do not bench `Travel` when it is the only mechanism capable of continuing the mission. The report identifies watchdog benching of the sole mover as one contributor to the pass dance. 

---

# 5. Serialize every input action

The disappearing waypoint panel strongly resembles stale or overlapping input, especially because the panel vanishes in roughly 300 ms and you already suspect a queued click. 

Use a **single-flight input pipeline**:

```text
precondition
    ↓
send one command
    ↓
wait for postcondition or timeout
    ↓
classify result
    ↓
only then permit the next command
```

Each queued command should include:

* Snapshot generation
* Expected game mode
* Expected UI mode
* Target object or coordinate
* Cancellation predicate
* Timeout
* Postcondition

When the world or UI mode changes, cancel stale commands.

For waypoint use:

1. Cancel outstanding movement.
2. Verify the character is stationary or within interaction tolerance.
3. Interact with the waypoint.
4. Wait for the panel to be visible in two consecutive observations.
5. Capture the panel geometry from the current frame.
6. Click the destination row.
7. Wait for either destination transition or a classified failure.
8. Never allow a pre-panel ground click to execute after step 3.

### Visual UI verification

A screenshot oracle is a good direction, but a generic “pixels changed” test is not sufficient by itself. It proves motion, not correctness.

Use two stages:

```text
ROI changed?
    ↓ yes
Does the new ROI match a known UI state?
```

For fixed-resolution offline UI, inexpensive methods are often enough:

* Downsampled grayscale difference
* Edge-map difference
* Small perceptual hash
* Template matching for panel borders and distinctive icons
* Presence tests for expected labels or layout regions

Keep the regions small. The waypoint panel, vendor panel, inventory, and pause menu should each have dedicated regions.

### Interception driver

I would not make the driver the next move.

Hardware-level input may improve acceptance, but it will not fix:

* Stale queued clicks
* Incorrect coordinate conversion
* Commands sent in the wrong UI mode
* Multiple in-flight actions
* Bad postconditions

First:

1. Diff the predecessor’s movement plumbing exactly.
2. Implement single-flight serialization.
3. Measure command acceptance by motor verb.
4. Test an ordinary hardware-like input path, if available.
5. Consider the driver only if acceptance remains materially worse.

The frozen predecessor is your strongest experimental control. Use it as one, not merely as code inspiration.

---

# 6. Re-aligning the map is worth testing, but do it as a falsifiable experiment

Model the relationship as:

```text
livePosition = A × mapPosition + b
```

Test increasingly flexible models:

1. Translation only
2. Rotation plus translation
3. Uniform scale, rotation, and translation
4. Full affine transform

Two coordinate pairs can algebraically fit some simple models, but they cannot robustly demonstrate that the model is correct. They are especially weak when both anchors lie on the same doorway or seam.

Use at least three non-collinear anchor pairs, preferably more, and fit robustly. Candidate anchors include:

* Transition objects
* Waypoint pads
* Stable map objects
* Room centers
* Known interactables
* Repeated successful crossing endpoints

Use outlier rejection such as a RANSAC-style process:

1. Fit transform from a subset.
2. Transform all known map anchors.
3. Measure residuals against live anchors.
4. Count consistent inliers.
5. Select the model with low residual and broad support.

Then inspect residuals by room.

### Interpretation

* **Constant residual:** likely translation error.
* **Residual rotates or scales consistently:** similarity transform may work.
* **Globally stable affine residual:** usable, though suspicious.
* **Residual jumps at room boundaries:** map chunks are not in one reliable metric frame.
* **Residual changes with streaming:** geometry should remain topological only.
* **Transformed geometry aligns, but fences still fail:** expected, because those fences do not exist in the source.

So the direct answer is:

> Use experience routing as the global authority, local live feedback for control, and transformed map geometry only as a confidence-weighted hint.

Do not bet the entire architecture on either pure breadcrumbs or one recovered offset.

---

# 7. Priorities need budgets, not only ordering

Your priority order can remain:

```text
Survive > Recover > Fight > Loot > Service > Travel > Explore
```

But each category should have entry conditions and budgets.

Examples:

### Loot

Loot may interrupt travel only when:

* Item value exceeds threshold
* Detour is bounded
* No crossing or portal transaction is active
* Threat level is acceptable

### Shrine or well

Take it when:

* It lies inside the current travel corridor, or
* Estimated detour is below a doctrine threshold

### Fight

Fight when:

* Enemy directly threatens the character, mercenary, route, or mission object
* Target has line of sight or verified accessibility
* Pursuit will not reverse a latched crossing

### Service

Service is a mission when required, not a tick-by-tick temptation. Once the service chain begins, finish or explicitly abort it.

### Explore

Explore only when no better topological edge exists. It should never steal control from proven road replay.

This creates the “driven” behavior you want without turning the character into a blind route script.

---

# 8. Capability profiles for barbarian and paladin

Use capability interfaces internally, with class profiles supplying configuration.

A generic sustain capability could look like:

```text
SustainEffect:
    activation method
    validity predicate
    refresh margin
    duration estimate
    resource cost
    conflict group
    safe-cast conditions
    desired count
```

Then represent:

* Aura: selected-state sustain, usually mutually exclusive by conflict group
* Shout or armor buff: timed sustain with refresh margin
* Summon: count-based sustain
* Enchant or weapon buff: target-bound timed sustain
* Stance: selected-state sustain with tactical switching constraints

Profiles should specify what exists and default preferences. The executive should reason over capabilities, not class names.

That gives you reliability from explicit per-class data without embedding class conditionals throughout the engine.

---

# The implementation order I would use

## 1. Separate mission from tactic

Add persistent mission state, resume tokens, and leases. This should immediately reduce priority churn.

## 2. Add route arc-length progress

Replace nearest-breadcrumb targeting with forward-only polyline progress and lookahead tracking.

## 3. Formalize seam transactions

Add signed progress, clear-side verification, and reverse taboo.

## 4. Make input single-flight

Cancel stale commands on world or UI changes. This should help waypoint panels, portals, and dead action cascades.

## 5. Split motor failure from geometry failure

Alternate motor verbs before learning an obstacle.

## 6. Add the pocket recovery ladder

Radial escape, latched wall-follow, freedom-anchor backtrack, learned fence, then portal.

## 7. Run the map transform experiment

Fit translation, similarity, and affine transforms from multiple anchors. Promote geometry only if residuals prove it deserves trust.

---

# Instrument the three numbers that reveal almost everything

For each run, graph:

### Forward mission progress

Examples:

* Route arc-length
* Distance to mission anchor
* Crossing signed distance
* Service-chain step

### Decision churn

Count:

* Mission changes
* Tactic changes
* Navigation-mode changes
* Target reversals

### Motor reliability

Per verb:

* Commands sent
* Commands accepted
* Commands producing displacement
* Median displacement
* Timeout rate
* UI transition success rate

Also record the last 10 `(state, action, result)` triples. Repeated sequences such as:

```text
click east → no move
click west → move
click east → no move
click west → move
```

should be recognized as loops and trigger a controller-mode change rather than another arbitration cycle.

---

# Bottom line

Do not rewrite the bot around a smarter global pathfinder.

The highest-value changes are:

1. **Persistent intention**
2. **Monotonic route progress**
3. **Transactional seam crossing**
4. **Serialized verified input**
5. **Recovery that changes strategy without abandoning the goal**

With those in place, your breadcrumbs become roads instead of magnets, combat becomes an interruption instead of a personality transplant, and invisible fences become learnable local facts rather than existential crises.

The next most useful code to inspect is the arbiter, breadcrumb target selection, crossing controller, and movement command queue.
