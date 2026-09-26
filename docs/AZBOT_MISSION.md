# AZBOT mission layer — design (2026-09-24)

Owner-approved direction: scripted, quest-aware missions replace top-level priority
bidding. The arbiter keeps only interrupts. See AZBOT_V3_FOUNDATIONS.md for why.

## Layering
Session → Janitor gate → Arbiter (INTERRUPTS ONLY) → Mission runner (one Life, ClassMission)
→ Objective → Services (Travel, Town errands, Interact, Engage) → MoveTo + verbs.

Arbiter classes after migration: Survive (Stand, Breakout, Flee, Dodge, Chicken) >
Recover (Respawn, Unstick, Recall) > Fight/Loot reflexes (Fight, Loot, Discard, Imbibe) >
Mission. Advance, Travel, Return, Explore, Haul, Withdraw and the town services stop
bidding; Reclaim becomes a mission detour.

- Fight/Loot read the current objective's Engagement{Radius, KillFilter, LootFloor}
  instead of the marchLawfulUntil / crossingBracketUntil / ExpWorthwhile globals.
- The runner never withdraws its bid while a mission is live, so its held-time ledger
  (objective budgets) survives interrupts. It is bench-exempt and implements Judgeable:
  watchdog verdicts go to the objective's failure ladder.
- Resume re-verifies: Satisfied? still in Where()? else insert GoToArea; drop cached ids.
- Detour stack (depth ≤2): townNeeded → ReturnToTown + TownRoutine; corpse → RecoverCorpse.

## Objective contract (pure package internal/azbot/mission; executors in activity)
Key, Where, Satisfied(w) (postcondition read from memory = idempotency), Pre, Engagement,
Budget (held time), Life hooks. Attempts counted per game; 3 failures → Failed → rule's
alternative or halt with a state line.

| Objective | Postcondition | Budget | Ladder |
|---|---|---|---|
| GoToArea(a) | Area==a ×3 reads, inside frame | 3m + 2m/hop | replan → alt door → frontier → TP+WP reroute |
| ClearLevel(a,cov,maxT) | game-scoped sweep ≥cov and no hostile in sight, or maxT | maxT | NoTarget |
| KillBoss(sel) | unit Death/Dead or quest bit | 5m | 2 deaths → Refused → Grind |
| Interact(obj,expect) | expect(w) | 60s | 3× Deaf → reposition |
| OpenChest / PickupQuestItem / TalkTo / UseCube / TownRoutine / ReturnToTown | see transcript | | |

Selectors never use coordinates: objects by the mod's objects.txt class, NPCs/bosses by
monstats/superuniques ids (or Unique/SuperUnique type on unknown levels), items by mod
item code. Level graph from the live room graph's External rooms + entrance units.

## Act scripts
ActScript{Act, Town, Rules}; Next(w) = first rule whose evidence is unmet (stateless →
idempotent after crash/relog). Quest done = quest bit OR artifact evidence (item held,
waypoint lit, NPC state). Rule 0: level gap → Grind in the best measured XP/hr area
(replaces the itinerary MinLevels that stall at L27).
Act 2: Radament (optional) → Cube (Halls 3) → Staff of Kings (Maggot Lair 3) → Viper
Amulet (Claw Viper 2, Drognan, Jerhyn) → cube the staff → Summoner (+journal, Canyon WP)
→ real tomb (HoradricOrifice) → Duriel → Tyrael → Jerhyn → Meshif.

## Science harness (later phase)
RunSpec YAML {id, build, levels, reps, order: interleave, stop}. LevelRun = TownRoutine →
GoToArea → ClearLevel(0.9, 8m) → ReturnToTown, resumable. Metrics: clear_s, travel_s,
coverage, kills, kills/min, xp/hr, hp_lost, deaths, chickens, potions, gold, loot_value,
unsticks → memory + logs/science/<run>.csv. Compare builds on kills/min and HP lost per
kill; interleave A/B on a fixed seed. RespecTo(build) uses the token, then Spend.

## Build profile config/azbot/<char>.yaml
build id; keys; skill plan by skill id with targets (prereqs auto-inserted from mod
skills.txt, tree seat from skilldesc.txt); stat plan (str: need, dex: need, vit: rest);
thresholds (drink, rejuv, chicken, merc, town triggers); pickit file. Owner's plan:
Leap Attack core, 1 Frenzy (buff), Carnage (Whirlwind at 30); explore the rest.

## Migration (runnable after every step; tools/test.sh green)
1. gamedata (levels/objects/skills/skilldesc/superuniques/misc) — MinLevel = mlvl−3 unblocks L27.
2. World books (QuestBook, WaypointBook, InvByCode) + -mission=shadow logging `M next=…`.
3. Runner + ClassMission behind -mission=on; GoToArea lifts Advance's crossing code into
   travel.go over MoveTo (requires MoveTo as sole mover); ClearLevel wraps coverage.
4. TownRoutine docket + detours; Reclaim leaves the roster.
5. Interact/TalkTo/PickupQuestItem/UseCube/KillBoss, then the Act 2 script (one drill per rule).
6. Profile + Spend plan.
7. Science harness.
8. Delete Advance/itineraries/-goal, intent/escalate/AZBOT_DELIBERATE, Travel/Return/Explore,
   the march globals, ExpWorthwhile, CoolAllServices, ServicesPending; ratchet: only
   interrupts and the runner may build a Demand.

## Risks
Quest bytes unverified (evidence-or-bit; census drill first); bench exemption could hide a
wedged objective (budgets + ladder + Pinned/Pacer); orifice/cube panels pixel-only until
the widget tree; Duriel at L27–30 melee; science confounds; mod may re-ID quest objects
(fail loudly, don't wander).
