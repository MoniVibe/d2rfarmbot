# Handoff — the autonomy session (2026-07-19, ~04:00–07:10)

## Where she is
- **Mamazon, level 7**, run 43 live: `./azbot.exe -goal rampage -meleekey f2 -rangedkey f1 -seconds 28800`
  (build: Jab=F2, MagicArrow=F1, **ID tome=F3 (218), TP tome=F4 (220) — both proven**).
- ~3.2k gold (pocket+stash summed — vendors draw from the bank on this build).
- Wearing: helm, armor, gloves (equipped by the bot via the identify side door).
  **A unique bow rides the bag** — gated by level/stat requirements or landing room;
  it equips itself the moment both clear. Two deaths tonight (swarm + my field-swap
  mistake), both fully recovered by the lattice: respawn → relog → town corpse →
  reclaim → rearm.

## THE GOVERNING SPEC (the owner's mentality shift)
**`docs/AZBOT_PROCEDURES_STE.md`** — ASD-STE100-style controlled procedures.
READ IT FIRST. One verb = one meaning = one check = one failure verdict.
Eight WARNINGS, each bought with a real loss. Six numbered procedures.
Compliance: conditionals cite P-x.y; new behavior enters the doc BEFORE the code;
a live failure updates doc and code in the same commit.
**The compass (owner, verbatim): "whatever would help us make the bot we want,
an independent diablo autobot, regardless of class."**

## Built this session (commits a932575 → abb0161, all pushed)
1. **Combat**: sweep-free volleys (skill right-clicks + SHIFT-attack via 2-key
   override — the hover sweep was ~800ms of standing paralysis per shot);
   pack-aware targeting; 10-tile half-step approaches; sight-first target picks
   with door-pathing for walled enemies; clinch shoots point-blank while the
   swap pends; wall-aware kites, CORNERED = FIGHT; body-locked dodge yields;
   bow-first hysteresis (in ≤3, out >4).
2. **Retreat doctrine**: CROWD (12+ in 25) fled at ANY hp; Flee plants and USES
   the portal when dry; Breakout commitment (a cast portal binds it until she's
   THROUGH); doorman clearing at the portal mouth; desperate blind entry at the
   hard floor.
3. **Town choreography** (causal order): Heal (Akara's free talk-refill, belief-
   calibrated) → Identify (tome ritual, count-judged) → Fence (ghost-window
   defense: two frozen deltas = abort) → Equip (identify side door, landing room,
   lvl/str/dex gates, docket rotation) → Restock (stash-backed gold) → Repair
   (durability must rise per click). March waits on pending dockets — WITH the
   escape clause: a docket whose service cannot run does not gate the march.
4. **Exp oracle**: Act 1 mlvl table; gap >4 shrinks the hunt to self-defense
   radius 12 and doubles march urgency. She pushes for ground that pays.
5. **Item intelligence v1**: unidentified magic+ (rares included — ordering bug
   fixed) → ID docket; identified non-usables → merchandise; usables/rares held;
   upgrades detected per slot with requirements; space-aware looting; LIFELINES
   (tomes, cube 549, all quest-typed) untouchable at two layers.
6. **Instruments**: FLIGHT RECORDER (1 Hz corpus logs/flight_*.jsonl + black box
   on death) and OFFLINE REPLAY (`-replay file.jsonl` — full Demand+arbiter
   timeline, no game needed). cmd/shot = pure-reader screenshot probe (safe
   beside a live bot). Photographic debugging proved 4 input classes deaf and
   found the packed-bag equip refusal.

## Hard-won laws (the WARNINGS, in blood order)
- Panel hotkeys are DEAF to all synthetic input; the identify cast is the panel
  door. Ghost trade windows DROP items (the tome/cube death mechanism) — trust
  transaction deltas only. Field binary swaps kill (run 41: 84 monsters ate her
  during my 15s swap freeze) — deploy only in town/dead/owner-F10. Camp
  furniture is in NO grid — planner for walls, slide/arc for furniture. The
  ribbon flickers single reads — believe 3. Density kills before HP moves.

## Open issues / next (the compass order)
1. **Class-agnostic doctrine** — combat roles, item valuation, and skill-spend
   from capability + data tables; no amazon hardcode. Design into the STE spec
   FIRST (compliance rule 2). This is the "regardless of class" milestone.
2. **Skill/stat point auto-spend** — she banks every point since level 1;
   farmbot's skilldesc.txt tree oracle + proven tree coords are the port source.
3. **Stash keepers** — the identify side door + ctrl-click quick-move (needs
   stash-open state; same gesture family as SellClick).
4. **Quest awareness** — Den of Evil (free point, merc unlock), Cain (free IDs).
   A merc is the biggest survivability multiplier available.
5. **Tome restock** — Restock should keep TP scrolls topped up (shopmap drill
   for the tome cell).
6. **FMEA rows + sterile-cockpit phases** into the spec; replay-verified fixes
   as standard practice (run the black box through -replay before deploying).
7. Verify live: the full equip chain end-to-end (side door → shift-click →
   docket drop) once she has landing room; Heal's first live refill was proven
   (54→100 at 04:29).

## Cadence
Swap ritual: build azbot.exe.new → **WARNING 6 check (town/dead/F10 only)** →
taskkill → `./farmbot.exe -fixinput` from koolo-build → copy → relaunch.
Monitor the newest logs/rampageN.log; grep -v sendinput. F10 = two-way toggle.
Offline D2R pauses unfocused. Seed re-rolls per game; facts are seed-namespaced.
Replay a black box: `./azbot.exe -replay logs/blackbox_<t>.jsonl -goal rampage`.

She started this session level 5 with a lost tome and a bag full of mysteries.
She ends it level 7 — dressed by her own hands, retreating from hordes by
doctrine, recorded on every frame, governed by a written standard — and still
marching.
