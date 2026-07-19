# Handoff — the day watch (2026-07-19, ~07:20–12:10, Claude on the desk)

## Where she is
- **Mamazon, level 9**, run 66 live with the full doctrine stack, clean
  calibration (F1=Magic Arrow proven→Reach, F2 owner-declared Contact,
  F3=Identify 218, F4=TP 220). **She crossed the bridge**: Cold Plains
  reached at level 9 under the blast-first doctrine; Stony Field (full
  XP at her level) is the next march. Blaise the rogue holds the flank.
- The unique bow is NOT in her bag (photographed 10:11) — possibly
  stashed by the owner during the cursor incident, possibly fenced in a
  ghost window hours ago. ASK THE OWNER. Stash-reading is the next
  wardrobe milestone either way.

## The governing spec grew all shift — READ IT FIRST
`docs/AZBOT_PROCEDURES_STE.md`: now 9 WARNINGS, P-1..P-9 + section 4
PROFILES (the class-profile design, code not yet written). Every law
below entered the doc before the code, and every measured failure
updated doc+code in one commit (~35 commits, all pushed to laptop-3.2).

## The owner's doctrine, in their words (all implemented)
- "ignore far away monsters and punch through" → P-5.7 forced march
- "shoot her way through with the bow" → P-5.8 (subsumed by bow-only)
- "more aggressive with her bow, clear stuff instead of cowering" → P-1.8
- "she died to a single zombie without hitting it back" → P-1.12
- "shoot more than moving if enemies are nearby" → radius 10, dodge is
  a wounded art, crowd bar 16 healthy, eject seat 6-below-half
- "can we have a survival hp oracle" → P-2.0 THE BLOOD ORACLE
  (TimeToDie from a 5s HP window + 40 blood runway per belt heal;
  20+ density backstop stands at any verdict)
- "not the same back and forth more than 3 times" → P-2.11 flee fatigue
- "maximum killing when pinned 3s with enemies near" → P-1.14 Stand
  (urgency 1.5, over every retreat except the critical dive)
- "mana arrows are cheap" → P-1.13 skill on every shot above 10% mana
- "drop her javelin cqb switch" → P-1.7 bow-only; javelins serve only
  a dry quiver
- "fire magic arrows on stacks" → subsumed by P-1.13
- "have the gear oracle know what can/cannot be equipped" → P-9.1
  hidden-req oracle (percept/hiddenreq.go parses the mod's own
  uniqueitems/setitems.txt by UniqueSetID) + P-4.4 refusal oracle
  (3 silent refusals drop an item from the docket)
- "class profiles seem unavoidable" → spec section 4: profiles are DATA
  FILES seeding one engine; every spec threshold is a migration field

## Hard-won laws of the shift (blood order)
1. **Unfocused ≠ paused** (WARNING 7 rewritten): she bled 131→55 during
   an 11-min "pause." Executive now takes the window back itself.
2. **WARNING 9 THE CURSOR ITEM**: a held item eats every click; parking
   (Equip, 0.85) precedes every ritual; all services interlock.
3. **Mid-relog kills leave the pause menu up; mid-trade kills leave the
   vendor panel up** (WARNING 6 grown twice): the successor self-heals —
   multi-bearing stride shadow test for the menu, readable-vendor-stock
   proof + ESC for the panel, both before calibration.
4. **Fuel gauges**: TP/ID tome charges (533/534 Quantity), belt-delta
   proof on potion buys (a full belt buys nothing), bag potion reserve
  of 4/kind, scroll cells LEARNED by tome-delta probing (ScopeForever
  keys shop.akara.cell.tpscroll/idscroll — not yet learned live).
5. **Beliefs re-arm per world** (activity.NewWorld) and the ranged
   audit resets when the hands change (Fight.Recalibrated).
6. **P-4.9 band approach**: NPC slides aim 5 out on the bearing;
   proportional backstep; hover-arc after 2 misses. Repair to Charsi
   still flaky — errand deaths now name their phase (P-6.2); read the
   next "verb=errand" evidence before touching the code.

## Unproven / next (compass order)
1. **SPEND live test**: stat points banked at level 9 + the two-door
   ritual (New Stats button client (397,662); 'c' hotkey plan B) with
   photos spend_pre/door/click.png — watch the next town visit. Skill
   points read 0 so far (mod may auto-grant); machinery ready (P-8.7,
   farmbot's proven tree grid, recipient = Cap.Reach.Skill).
2. **Class profiles** (spec section 4) — design done, code next.
3. **Stash keepers + the unique bow question.**
4. **Charsi repair root cause** (errand telemetry will name it).
5. **Attack-speed-derived volley cadence** (350ms constant loses DPS on
   fast weapons — reviewer finding 6) and the drinkAt/field-floor dead
   band (finding 4) — design work, not urgent.
6. Quests: Den of Evil, Cain. Merc upkeep (Blaise) not yet modeled.

## Cadence (unchanged + new gotchas)
Swap ritual: build azbot.exe.new2 → verify "BUILD OK" (&&, never |head)
→ town/dead window, NEVER mid-relog or mid-trade → taskkill →
./farmbot.exe -fixinput → cp → relaunch with remaining -seconds →
re-arm the log monitor (ribbon-filtered: 3-stable-read area changes).
Shell cwd resets between calls — cd /c/dev/koolo-build first, always.
go.exe lives at C:\dev\tools\goroot\go\bin. Run budget ends ~15:00;
relaunch on process-exit notification with a fresh budget.
