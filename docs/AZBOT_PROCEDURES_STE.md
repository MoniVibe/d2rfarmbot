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
| SPEND | One click on a plus button while the points panel is provably open | The unspent count decreases AND the target's level rises | 2 frozen reads → close the panel, retire spending for the session |

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
and stops the world. Press ESC only when a panel is provably open.

**WARNING 5 — PANEL HOTKEYS.** Panel hotkeys are deaf to every synthetic
input class (four photographed failures). Open the inventory only through the
IDENTIFY side door. Do not add a fifth attempt.

**WARNING 6 — SWAP DISCIPLINE.** Deploy a new binary only when she is in
town, dead, or the owner holds the controls. A field swap freezes her inside
whatever is chasing her. (Run 41: eighty-four monsters, seventeen seconds.)

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
7. With a TOOTH on her: ask for the CONTACT TOOL AND fire point-blank while
   the swap pends. The clinch is not a cease-fire.
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

### P-2 RETREAT

1. A CROWD is fled at any HP. Blood is a lagging indicator inside a horde.
2. Clearance from a CROWD is: fewer than 8 within 25.
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
   violence, not footwork.

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

1. HEAL first. Akara refills for free on TALK. Wounded below 55 pends.
2. IDENTIFY second. Unknown items cannot be judged.
3. SELL third. Judged non-keepers become gold and LANDING ROOM.
4. EQUIP fourth (requires LANDING ROOM). Open the panel by the IDENTIFY side
   door. Normalize the cursor with one plain click; put back what it grabs.
   Shift-click the candidate. Rotate the DOCKET. Respect level and stat
   requirements before the click; the game's refusal is silent.
5. BUY fifth: potions to doctrine (one row mana, rest HP), from stash-backed
   gold (pocket + bank is purchasing power).
6. REPAIR when worn below a quarter. Durability must rise per click.
7. Every service obeys its delta check (Lexicon) and its GHOST defense
   (WARNING 2).
8. The march waits while any DOCKET pends. Travel outranks Service by class;
   the pending check is the treaty that keeps errands alive.

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
   punch through"): on ground that does not pay, the march owns the
   ground. FIGHT engages only a TOOTH or a blocker within 4. A retreat
   that backtracks refunds nothing: when the march direction is not into
   the crowd, Flee retreats FORWARD along it — the moor is crossed, not
   orbited. P-2's triggers and clearances stand unchanged.
8. THE DOOR MOUTH IS SHOT OPEN (the owner, 2026-07-19, at the bridge:
   "I'd rather she shoot her way through with the bow"): within 12 of
   the march door, the REACH TOOL holds. The clinch swap is suppressed
   and the volley fires point-blank — the funnel rewards the pierce,
   not the poke. Holding the CONTACT TOOL there, she swaps back to the
   REACH TOOL at once. A dry or dead REACH TOOL still yields (P-1.11).

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

### P-9 JUDGE — CLASS-AGNOSTIC ITEM VALUATION

1. An item is judged by readable facts: slot, quality, type code, and
   requirements. Names have no vote (the table is scrambled).
2. A DRAWABLE magic+ item is held for judgment; identified non-DRAWABLE
   non-keepers are merchandise. LIFELINES stand outside all valuation
   (WARNING 1).
3. An upgrade is per-slot: better quality than the worn piece, with
   requirements within reach — met now, or met by the SPEND already
   pending (P-8.3).
4. The wardrobe defines the class; no judgment consults a class name.

---

## 4. COMPLIANCE

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
