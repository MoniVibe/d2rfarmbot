# AZBOT CONTROLLED PROCEDURES — ASD-STE100 STYLE

This document is the governing specification for azbot behavior.
The code obeys this document. When the code and this document do not agree,
one of them is wrong, and the log must show which.

We adopt these rules from ASD-STE100:

- One word has one meaning. The Lexicon defines every approved word.
- One sentence gives one instruction.
- A procedure is a numbered list of single actions.
- Every action has a check. An action without a check did not happen.
- WARNINGS come before the step they protect.
- Do not use a synonym for an approved word. Do not overload an approved word.

Rule zero, above all: **silence is never success.** Every terminal state writes
an outcome. Every belief the bot holds is one the world can retire.

---

## 1. THE LEXICON

Each verb names ONE action, ONE check, and ONE failure verdict.
Code that performs the action must use the verb's name in its log line.

| Verb | Action (one meaning only) | Check (the only proof) | Failure verdict |
|---|---|---|---|
| STRIDE | Hold force-move toward a world point for a bounded time | Net displacement ≥ MinGain | BLOCKED |
| VOLLEY | Fire one walk-proof shot at a projected point | Target flinch appears in the snapshot stream (passive) | 8 silent volleys → BLACKLIST target |
| ENTER | Click through a portal object | Area id changes | DEAF |
| CROSS | Push through a walkable border | Area id changes and stays changed for 3 reads | (ribbon flicker is not a CROSS) |
| TALK | One bare click on a hover-confirmed NPC | Menu byte goes high | 6 tries → DEAD errand |
| BUY | One click on a vendor stock cell | Gold decreases | 2 frozen buys → GHOST window, abort |
| SELL | One ctrl-click on an inventory cell while a trade is open | Junk count decreases | 2 frozen sells → GHOST window, abort |
| REPAIR | One click on the repair-all button | MinDurPct increases | 2 frozen clicks → leave with what you got |
| IDENTIFY | Tome cast, then one click on an unidentified item cell | The item's Identified flag flips | 3 frozen counts → retire the belief |
| EQUIP | One shift-click on a wearable upgrade cell | The upgrade docket count decreases | 3 frozen counts → retire the belief |
| DRINK | One belt key press on a known-stocked column | HP/MP rises on the survival read | (Sentinel-owned; never blocks) |
| RETREAT | Strides away from a named threat until a named clearance | The clearance condition reads true | (see P-2) |
| RECLAIM | Hover-confirmed click on her own corpse | Armed flips true | 4 spent rounds → cool and re-approach |
| RELOG | Save+Exit, then Play, to materialize the corpse in town | The epistemics gate passes in a new world | Missed click → retry in 45 s; churn fail → 4 min |
| PROBE | Press one candidate key once, then read the selection back | The right-skill selection changes | Unbound — the key is retired for this weapon set (not an error) |
| SPEND | One click on a plus button while the points panel is provably open | The unspent count decreases AND the target's level rises | Door settle 1 s first (the slide-in eats early clicks — measured 08:41), then 2 frozen reads → close the panel, retire spending for the session |
| ACTIVATE | Touch her own-area waypoint object once on entering the area | The area joins AvailableWaypoints | Not near a WP, or none in this area — skip, never chase |
| WAYPOINT | Open a WP panel and click an ACTIVATED destination row | Area id changes to the destination | Panel not lit (no blue compass) → re-open; area unchanged after the row click → recalibrate the row, then abandon |

Approved nouns, one meaning each: TOOTH (an enemy in melee reach ≤3),
PRESSURE (2+ enemies within 7), CROWD (12+ enemies within 25), DOORMAN
(an enemy within 4 of a portal), DOCKET (a service's pending item list),
LIFELINE (tome, cube, quest-typed item), GHOST WINDOW (a trade read that
lingers after the panel closed), RIBBON (the flickering area-id band at a
border), LANDING ROOM (6+ free inventory cells).

Class-agnostic combat nouns, one meaning each: TOOL (a weapon-set plus a
proven selection that has flinched at least one enemy), REACH TOOL (a TOOL
with a proven flinch beyond 5), CONTACT TOOL (a TOOL proven only within
reach ≤3), FUEL (the consumable a TOOL drains — mana, ammo, or charges; a
dry TOOL is not a TOOL), WORTH (flinches per attempt; the only measure of
a skill's quality), DRAWABLE (an item type the character can provably
wield — admitted by a table prior or by one successful EQUIP of the type).

---

## 2. WARNINGS — THE IRREVERSIBLE CLASSES

Each WARNING was bought with a real loss. Do not renegotiate them in code.

**WARNING 1 — LIFELINES.** A LIFELINE is never merchandise, never a click
target for SELL, at any layer, in any quantity. (The tome and the cube were
both fenced before this law. Their loss survived the night; the items did not.)

**WARNING 2 — GHOST WINDOWS.** A SELL or BUY whose delta freezes twice is
firing into a GHOST WINDOW. A ctrl-click into a ghost window with any panel
open DROPS the item. Relog world-churn sweeps dropped items forever. Abort on
the second frozen delta. Never on the third.

**WARNING 3 — NAKED MARCH.** When Armed is false and a corpse exists, only
recovery procedures may hold the actuator. Travel of any kind is forbidden.

**WARNING 4 — THE PAUSE TRAP.** ESC with no panel open raises the pause menu
and stops the world. Press ESC only when a panel is provably open. THE MENU
IS BYTE-BLIND (0xF4 shows NPC menus only) and can arrive from OUTSIDE: a
binary swapped mid-relog leaves it up for the next process (measured 08:41:
100 s of invisible wall — strides refused, panel clicks eaten). Its only
shadow: town strides refused at zero gain with no enemy near. The cure: one
ESC, then a stride test; the toggle converges in two probes.

**WARNING 5 — PANEL HOTKEYS.** Panel hotkeys are deaf to every synthetic
input class (four photographed failures). Open the inventory only through the
IDENTIFY side door. Do not add a fifth attempt.

**WARNING 6 — SWAP DISCIPLINE.** Deploy a new binary only when she is in
town, dead, or the owner holds the controls. A field swap freezes her inside
whatever is chasing her. (Run 41: eighty-four monsters, seventeen seconds.)
Dead is lawful EXCEPT mid-relog: a kill between relog's first ESC and the
new world's load leaves the pause menu up for the successor (WARNING 4,
measured 08:41). A kill MID-TRADE leaves the vendor panel up instead
(measured 12:00: the successor's calibration pressed keys into a deaf
panel and every service fired into ghost windows). And a kill with the
BAG OPEN leaves a byte-blind panel no proof can see (photographed
12:12: six deaf talks with the inventory standing). The successor
heals itself blind: in town, one ESC at attach — the three-bearing
stride test then tells whether it closed a panel or raised the pause
menu, and a RealEsc restores the difference. Self-contained, before
calibration, every launch.

**WARNING 7 — THE FOCUS LAW.** An unfocused world is UNKNOWN. It may
freeze (measured: zero-gain strides, 2026-07-19 before dawn), or it may keep
running (measured: 131→55 blood across an 11-minute "pause", 2026-07-19
07:26–07:38 — the sentinel's drink landed; the executive stood by while
she was eaten). Do not aim input at an unfocused window. DO request the
window back — the foreground trick, rate-limited, only while the bot
holds the controls. Standing by without a refocus request is paralysis
under fire, the same death class as the field swap (WARNING 6).

**WARNING 8 — SEED NAMESPACE.** The world re-rolls per game. Geometry facts
from one seed must never steer another.

**WARNING 9 — THE CURSOR ITEM.** An item on the cursor owns every click:
panel buttons do not press, world clicks DROP it, and the stuck-detector
reads the paralysis as walls (measured 09:30: one missed put-back sent
every town service into a flailing loop the owner watched — "chimping
out"). No service clicks while the cursor holds an item. Recovery precedes
every ritual: with the panel provably open, park the item in a free grid
region; the cursor-empty read is the only proof of success.

---

## 3. PROCEDURES

### P-1 FIGHT

1. Select the target by sight first. A walled enemy loses to any visible enemy.
2. Score candidates: distance + 3 × (bodies within 8). Do not elect the
   center of a stack.
3. If the area does not pay experience (gap > 4), hunt only what P-5.7
   admits: a TOOTH, or a blocker within 4. Chasers are outrun, not fought.
4. Approach in 10-tile half-steps. Reassess between steps.
5. If a wall owns the arrow line, walk the door with the planner, or
   blacklist the target. Do not rub the wall.
6. VOLLEY at the pace of the animation (350 ms). Never wait for per-shot
   evidence. Never sweep the cursor for combat.
7. With a TOOTH on her: the REACH TOOL HOLDS — point-blank volleys, no
   swap (the owner, 11:50: "drop her javelin cqb switch, her bow is
   superior for now"). The CONTACT set serves only a dry REACH TOOL
   (P-1.11); holding it with a live quiver, she swaps back at once and
   strikes only while the swap pends. The clinch is not a cease-fire.
8. STAND AND LOOSE (the owner, 2026-07-19: "more aggressive with her bow —
   clear stuff instead of cowering"): healthy blood holds its ground and
   shoots; clearing the pack IS the defense. Kite only wounded (below
   half blood) AND under PRESSURE — never from either alone. One chaser,
   any blood, is a target. The CROWD law (P-2.1) still owns hordes.
9. Kites check the grid first. CORNERED = FIGHT.
10. Roles change on hysteresis: the CONTACT TOOL at reach ≤3, the REACH
    TOOL again only past 4. Between is a held decision, not an oscillation.
11. A character with one TOOL fights with it everywhere. A dry TOOL yields
    its role to any fueled TOOL; with no fueled TOOL, the dry basic attack
    is the TOOL of last resort.
12. THE LONE TOOTH IS FOUGHT AT ANY BLOOD (the owner, 2026-07-19: "she
    died to a single zombie without hitting it back"): the wounded
    stand-down applies to PRESSURE, never to a single enemy. A retreat
    from one chaser is a chase, and she loses chases — the sentinel
    drinks while she swings. One enemy near, any blood: FIGHT bids and
    Flee does not.
13. THE SKILL IS THE WORKHORSE (the owner, 11:30: "mana arrows are
    cheap and we have mana stolen per hit and per kill"): the REACH
    TOOL's skill fires on EVERY shot while mana holds above 10% — the
    basic arrow is the reserve below that line, not the default. No
    thrift bars, no stack arithmetic; the steal refills the pool.
14. THE CORNERED VERDICT (the owner, 11:45, the last law before sleep:
    "if she remains in spot for more than 3 seconds and enemies are
    nearby, shift to maximum killing"): the same ground held 3 seconds
    with an enemy within 12 — wedged, idling, trapped, whatever holds
    the actuator — belongs to the arrows. STAND outbids every retreat
    except the critical dive: a girl who cannot move cannot flee, and
    pretending otherwise is how she gets ganged walking into walls.
    Blaise holds the flank; the skill sees her through.

### P-2 RETREAT

0. THE BLOOD ORACLE (the owner, 11:40: "can we have a survival hp oracle"):
   retreat is judged by MEASURED survivability, never by counting heads.
   A sliding 5 s window yields the blood drop rate; each belt heal extends
   the runway 40 blood; TIME-TO-DIE is the verdict. She blasts while the
   runway holds (TTD ≥ 10 s), retreats when it shortens, and dives when it
   collapses. ONE density backstop remains: 20+ within 25 is fled at any
   oracle verdict — density kills before HP moves (run 41's eighty-four).
1. A CROWD (12+, 16 healthy) is fled only when the oracle agrees
   (TTD < 10 s) — a crowd she is out-sustaining is a target-rich
   environment, not an emergency.
2. Clearance from a CROWD is: fewer than 8 within 25. The eject seat
   (Breakout) arms at 6 surrounding with a collapsing runway (TTD < 12 s)
   — not at four nuisances under a scratch (the 57-HP sortie loop, 11:06).
3. With an empty belt, a RETREAT has a destination: plant the portal when
   the gap opens, run to it, ENTER it. Fleeing is not a lifestyle.
4. A portal that exists is a decision already made. Breakout stepping at all
   means emergency: ENTER, desperately if needed.
5. If DOORMEN crowd the mouth and blood is above the hard floor: VOLLEY the
   nearest DOORMAN until the mouth thins. Never a mute cycle.
6. At the hard floor (18%): dive blind. A missed click costs one swing; a
   refused click costs the life.
7. A started escape binds its owner until she is THROUGH or the portal is
   provably gone. One potion tick does not un-declare an emergency.
8. Dodge yields when body-locked (2+ adjacent). Inside a ring the answer is
   violence, not footwork. DODGE IS A WOUNDED ART: healthy blood (65+)
   trades — eat the arrow and answer with one; the sidestep only earns
   its 400 ms when the pool is already low (the owner: the archer-camp
   wiggle was footwork between every volley).
9. A PINNED RETREAT IS A FIGHT (measured 08:12–08:13: breakout and flee
   pinned at a wall in rotating 15 s cooldowns, striding into the stone,
   blood 51→24, no swing answered): when the retreat stride gains no
   ground and a TOOTH is on her, strike the nearest TOOTH between
   strides. The wall has declared CORNERED for her. Never a mute cycle.
10. THE PORTAL REMEMBERS THE JAWS (measured 11:03–11:08, the sortie
    loop): a breakout entered with a CROWD at the mouth marks its
    portal HOT for three minutes. Return does not walk a hot portal —
    the march re-enters by the gate and picks its own ground. An escape
    valve that feeds her back to the same jaws is not an escape.
11. THE THIRD RETREAT IS A LIE (the owner, 11:35: "not the same back
    and forth more than 3 times"): a third crowd-flee within 90 s and
    30 tiles of the last declares the retreat useless — Flee stands
    down for 60 s, the wounded stand-down suspends, and FIGHT owns the
    ground with the skill doing the arguing (P-1.13). The death floor
    is exempt: below 30 blood the fatigue never binds, and Breakout's
    critical band keeps the deep escape armed.

### P-3 RECOVER

1. Death screen: press ESC until the mode reads alive.
2. In town, unarmed, corpse far: RELOG. The corpse materializes at the spawn.
3. In town, unarmed, corpse near: RECLAIM it. Step off the body first; her
   own sprite owns the cursor.
4. Guarded body: LURE. Run out, the swarm follows the runner, loop back.
5. Blind-click the body only when zero guards stand on it.
6. While recovery owns her, no travel procedure may bid (WARNING 3).

### P-4 TOWN SERVICES — THE ORDERED CHOREOGRAPHY

The order is causal, not stylistic. Each step creates the next step's
precondition.

1. HEAL first. Akara refills for free on TALK. In town she tops up below
   75 (free is free; idling at 57 kept the eject seat armed all morning,
   11:06); only below 55 does the wound GATE the march.
2. IDENTIFY second. Unknown items cannot be judged. AN EMPTY TOME
   IDENTIFIES NOTHING (measured 09:50: the cast fizzles, the follow-up
   click GRABS the item, the next click throws it — WARNING 9 on
   repeat): at zero charges the service stands down until Restock
   refills the tome.
3. SELL third. Judged non-keepers become gold and LANDING ROOM.
4. EQUIP fourth (requires LANDING ROOM). Open the panel by the IDENTIFY side
   door. Normalize the cursor with one plain click; put back what it grabs.
   Shift-click the candidate. Rotate the DOCKET. Respect level and stat
   requirements before the click; the game's refusal is silent. A candidate
   equips onto the ACTIVE hands: swap to its set before the click (a bow
   candidate needs the bow set out, or the gesture benches the javelins).
   THE REFUSAL IS ITSELF AN ORACLE (photographed 10:11: a game-red armor
   docketed past every readable gate — the mod hides requirements): a
   candidate whose shift-click freezes the docket three times is REFUSED
   for the session, the docket drops it, and a docket of only refused
   items is an empty docket — the march never waits on the unequippable.
5. BUY fifth: potions to doctrine (one row mana, rest HP), from stash-backed
   gold (pocket + bank is purchasing power). THE BELT IS THE ONLY PROOF a
   potion buy landed: a full belt sends the bottle to the bag and the plan
   never converges (measured 09:50, the mana spray) — two buys with no belt
   rise end the bottle-buying for the visit. Then SCROLLS to BOTH tomes'
   floor of 8 — an empty TP tome un-writes P-2.3's destination, an empty ID
   tome starves P-4.2 (the owner, 2026-07-19: "she's out already"). A
   scroll BUY is judged by its TOME's quantity delta, never by gold — gold
   cannot tell a scroll from junk. POTION buys carry the same discipline
   (measured 12:15: the shop restocks over its own layout as she levels —
   the level-5 cells died at level 9): judged by the OWNED count, belt AND
   bag; cells LEARNED per kind per game; a learned cell that freezes twice
   is FORGOTTEN and re-probed, never trusted into a ghost. Wrong-probe
   junk goes to the fence like any other merchandise. THE BAG IS NOT A CELLAR: bag potions beyond a reserve of
   4 per kind are merchandise (the junky's dozen strangled the landing
   room, 10:01) — the belt is the tank.
6. REPAIR when worn below a quarter. Durability must rise per click.
7. Every service obeys its delta check (Lexicon) and its GHOST defense
   (WARNING 2).
8. The march waits while any DOCKET pends. Travel outranks Service by class;
   the pending check is the treaty that keeps errands alive.
8a. THE OPEN SHOP THE BOT COULDN'T SEE (mis-diagnosed 12:00→12:35 as a
    "poisoned world"; PHOTOGRAPHED 12:36 as the truth): Akara's trade
    window stood WIDE OPEN — stock readable, everything working — while
    the errand logged "menu never opened" and re-clicked her shut, over
    and over. THE LEXICON ALREADY KNEW: vendor stock is the honest
    oracle (BUY/SELL). A trade errand that can read the shelves IS in
    the shop — jump to act, never re-click an open shop closed. That
    short-circuit was the true fix. (A FOLLOW-ON over-correction removed
    the dialog navigation too and FROZE trading at gold=741, run 72: the
    click opens a DIALOG that Home/Down/Enter steers to Trade — the
    screenshot showed the END state, an open shop, not the PATH that
    opened it. Reverted.) The ghost/relog poison backstop survives for a
    REAL wedge, but this was never one.
    PILOT'S LESSON, TWICE OVER: a screenshot shows a STATE, not a
    MECHANISM — half an hour chasing a log's "ghost" narrative, then a
    frozen shop chasing a screenshot's end-state. Look, but confirm the
    FIX moved the needle (gold, junk) before believing it; revert fast
    when it didn't.
9. THE APPROACH AIMS AT THE BAND, never the body (measured 10:2x, the
   torch dance): sliding at an NPC's center lands in the hover-breaking
   clinch, and a backstep flung past the band re-overshoots forever. The
   destination is a point 5 out on the current bearing; the backstep
   returns TO the band, proportionally. Two hover misses on one bearing
   walk the 90° arc — the torch owns that line of sight, not the town.

### P-5 MARCH

1. Topology from map data. Geometry only from live truth.
2. The door direction is measured when known: push at the far-side fact.
   The center guess is a fallback, not a law.
3. A crossing is believed after 3 stable reads. The RIBBON flickers one.
4. A crossing is not an arrival. Push 12+ tiles onward before the next leg
   gets a thought.
5. Errand and march movement beyond 12 tiles goes by planner. Real walls
   demand real routing. Slides and arcs are for camp furniture only.
6. On ground that does not pay (gap > 4), the march urgency doubles. The
   march is the experience.
7. FORCED MARCH (the owner, 2026-07-19: "ignore far away monsters and
   punch through"; refined 11:15: "shoot more than moving if enemies are
   nearby"): on ground that does not pay, the march owns the ground.
   FIGHT engages anything within 10 — nearby aggro is SHOT, the far
   field is ignored. A retreat that backtracks refunds nothing: when the
   march direction is not into the crowd, Flee retreats FORWARD along
   it — the moor is crossed, not orbited.
8. THE DOOR MOUTH IS SHOT OPEN (the owner, 2026-07-19, at the bridge:
   "I'd rather she shoot her way through with the bow"): within 12 of
   the march door, the REACH TOOL holds. The clinch swap is suppressed
   and the volley fires point-blank — the funnel rewards the pierce,
   not the poke. Holding the CONTACT TOOL there, she swaps back to the
   REACH TOOL at once. A dry or dead REACH TOOL still yields (P-1.11).
9. THE MARCH CARRIES THE REACH TOOL (the owner, 2026-07-19, at the
   corner: "she's not pulling her bow out"): walking with no enemy
   within 8, a REACH set that exists and is not dry is the set in hand.
   The swap is rate-limited and verified by the next snapshot; contact
   arriving mid-swap hands the moment back to P-1.

### P-6 TELEMETRY (controlled language for the log)

1. A log line names its verb from the Lexicon.
2. A failure names its evidence, not its feeling: "junk count frozen at 9"
   and never "sell seems broken".
3. A retired belief names its disproof and its cost:
   "two talks, no HP change — this Akara does not heal".
4. Status lines carry the self-model: position, area, hp, gold, weapon,
   arrows, holder.

### P-7 ARM — THE CLASS-AGNOSTIC COMBAT SURFACE

The character is whatever her hands and her evidence say she is. No
procedure may ask "which class is this?" — only "what has she proven?"

1. PROBE every candidate key at session start, after any weapon change,
   and after RECLAIM (a corpse holds the weapons; selections change).
2. A selection is a claim. A TOOL is a claim with a flinch. Classify by
   evidence, never by a skill-ID list.
3. Tables vote as priors, with provenance. Owner declarations seed the
   roles and outrank tables. Behavior overrules both; a demotion logs its
   disproof first (P-6.3).
4. Fifteen attempts with zero flinches retire a claim for the session.
5. The TOOL with a proven flinch beyond 5 is the REACH TOOL. A TOOL proven
   only within reach is the CONTACT TOOL. One TOOL may hold both roles.
6. Every TOOL names its FUEL and its gauge at classification time. A TOOL
   whose gauge cannot be read fights only while a fallback TOOL stands.

### P-8 SPEND — POINTS ARE ORDNANCE

1. SPEND is a town service. It runs after IDENTIFY and before EQUIP: a
   stat point spent well clears an equip gate in the same visit.
2. The points panel opens by its screen button, never by hotkey
   (WARNING 5). No button on screen means nothing to spend; move on.
3. Stat points serve the wardrobe first: meet the requirements of the best
   held DRAWABLE upgrade, exactly to the gate, strength before dexterity.
   Points beyond every gate go to vitality. Energy is bought by evidence
   of starvation only (a session of dry-TOOL retreats), never by default.
4. Skill points follow WORTH: the proven skill with the highest measured
   worth takes the point. A prerequisite on its path counts as investment
   in it.
5. With no flinch record yet, bank. Banked points are a decision awaiting
   evidence, not a debt.
6. Every SPEND obeys its check (Lexicon). A frozen count retires spending
   for the session — the panel is lying or deaf, and points survive death.
7. SKILL POINTS ARE ORDNANCE TOO (the owner, 11:20: "maximize dps").
   The recipient is the proven REACH TOOL's skill — WORTH made flesh
   (P-8.4); with no proven TOOL, bank. The tree opens by farmbot's
   proven door and grid (skilldesc Page/Row/Column against the
   calibrated tab and cell coordinates — data with provenance, not
   class knowledge). The check: the skill's level rises in the live
   Skills map AND the unspent count falls. Two frozen reads retire
   skill spending for the session.

### P-9 JUDGE — CLASS-AGNOSTIC ITEM VALUATION

1. An item is judged by readable facts: slot, quality, type code, and
   requirements. Names have no vote (the table is scrambled). A unique's
   or set piece's REAL level gate lives in the mod's own excel row
   (uniqueitems/setitems, keyed by UniqueSetID) — the base item and the
   stats both lie silent (measured 10:11: a unique armor burned three
   refusals learning what the table knew). Tables are priors; the
   refusal oracle (P-4.4) remains the final judge.
2. A DRAWABLE magic+ item is held for judgment; identified non-DRAWABLE
   non-keepers are merchandise. LIFELINES stand outside all valuation
   (WARNING 1).
3. An upgrade is per-slot: better quality than the worn piece, with
   requirements within reach — met now, or met by the SPEND already
   pending (P-8.3).
4. The wardrobe defines the class; no judgment consults a class name.

### P-10 WAYPOINT — THE NETWORK IS THE ROAD

The owner, 2026-07-19 (after a corpse lost in Cold Plains): "she didn't
take the waypoint either — I had to do it manually." She walked every
area on foot; a death left her with no way back but the road or the
(broken) relog. The waypoint network is the fix — for progress AND for
recovery.

1. ACTIVATE ON ARRIVAL. Entering an area with a waypoint, touch it once
   (P-5 continues after). A touched waypoint is a saved return node; the
   network is built by passing through, at almost no cost.
2. TRAVEL WHEN IT PAYS. Standing at a waypoint or in town with the target
   area (or the deepest itinerary area toward the goal) ALREADY in
   AvailableWaypoints, WAYPOINT there — do not re-walk cleared ground.
   The march owns only the UNMAPPED frontier past the last waypoint.
3. RECOVERY RIDES THE NETWORK. After a town respawn with the gear back,
   the return to the fight is a WAYPOINT to the deepest activated area on
   the goal's path — never a naked or a full-length foot march. This is
   what the manual intervention did by hand.
4. ONLY ACTIVATED NODES. AvailableWaypoints is the whole truth of where a
   WAYPOINT can go. An un-activated area is reached on foot (P-5), and
   activating its waypoint (P-10.1) opens it for next time.
5. THE PANEL IS PROVEN BY BLUE. The lit blue compass column is the panel-
   open oracle; the destination row is chosen by it, not by a blind
   count. Area-change is the only proof of arrival (WAYPOINT, Lexicon).
   A row click that does not change the area recalibrates the row before
   it abandons — never spends the trip clicking a dead coordinate.

---

## 4. PROFILES — THE DESIGN (code follows this section, rule 2)

The owner, 11:30: "class profiles seem unavoidable... bots should be
aware of what's possible for them, and different classes play
differently." The resolution is not to abandon the class-agnostic
engine — it is to finish it. Rule 5 never forbade class knowledge; it
forbade class knowledge IN CONDITIONALS. A PROFILE is the lawful home:

1. A PROFILE is a DATA FILE, one per class/build, with provenance. It
   declares: the preferred engagement band (a sorceress opens at 20, a
   barbarian at 2); the resource model (what fuels the TOOLs, what
   refills it — steal, potions, warmth); the skill build order (which
   skills take points, in what sequence, with prerequisites); the kite
   posture (a javazon backpedals, a bear never does); and what "dry"
   means for this build.
2. The ENGINE stays one engine. Conditionals consult the PROFILE's
   numbers the way they consult the priors table — never a class name.
3. The profile SEEDS; behavior RULES. Every profile value is a prior
   the flinch audit, the blood oracle, and the delta checks may
   overrule live — a profile that lies loses to the evidence, same as
   any table (P-7.3).
4. The owner's declarations outrank the profile; the profile outranks
   the built-in defaults; measurement outranks everything.
5. Until profiles exist, the built-in constants ARE the amazon profile
   in disguise — every threshold this document names (bands, bars,
   floors) is a candidate profile field, and moving them into the
   profile file is the migration.

## 5. COMPLIANCE

1. Every conditional in an activity cites a procedure step (P-x.y) in its
   comment, or it is doctrine drift and must be deleted or promoted into
   this document.
2. New behavior enters this document before it enters the code.
3. A live failure that contradicts a procedure updates the procedure in the
   same commit that fixes the code.
4. The Lexicon grows by necessity, never by convenience. One word, one
   meaning, forever.
5. A class name or a skill ID inside a conditional is doctrine drift.
   Class knowledge lives in data tables with provenance; conditionals
   consult roles, evidence, and readable facts only. (The wardrobe defines
   the class, not the reverse.)
