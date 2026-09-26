# Handoff 2026-09-26 — Act 3, Leap-only barbarian

Branch `claude/diablo-bot-states-x834xf`, HEAD `fb9705c`, pushed to `public` (MoniVibe/d2rfarmbot). 61 commits on 09-25 and 09-26. Read this first; `docs/HANDOFF_2026-09-24_cloud_to_local.md` still holds the older rules.

## Where the character is

- **Character:** Fableboi, level 34 Barbarian, **Leap Attack only**. F5 is Leap Attack and left click is plain Attack; Carnage and Concentrate were respecced away.
- **Location:** Act 3, Kurast Docks.
- **Act 2 is done.** The owner finished the Summoner, the Orifice and Duriel partly by hand.
- **Khalim's relics:** the bag holds the Eye (`qey`) and the Brain (`qbr`). The Heart (`qhr`, chest 405 in Sewers Level 2, entered from the Kurast Bazaar) is **still missing**. The cube (`box`) is in the bag.
- **Lit Act 3 waypoints:** Kurast Docks (75) and Flayer Jungle (78). The Spider Forest pad exists, but its map preset is wrong (`wpdead.466817790.76`).

## Idempotence: yes

The bot restarts anywhere and resumes. All state lives in game memory or the WAL (`logs/azmem/facts.wal`, append-only JSON, last write wins):

- **Quest legs:** done when the artifact is held (bag, stash, cube or equipped), or persisted as `questdone.<char>.<area>`.
- **Waypoints:** `wplit.<char>.<area>` records lit pads. A pad counts as lit only when it reads **mode Opened**; proximity does NOT activate on this mod.
- **Per-seed refutations:**
  - `tombs.wrong.<seed>` — tombs that turned out not to be the true tomb.
  - `arcane.wings/guesses.<seed>` — Sanctuary wings and journal guesses already searched.
  - `wpdead.<seed>.<area>` — map waypoint presets that proved empty.

Owner resets are written as WAL records with `src: owner`.

## The big finding: the map oracle lies in randomized areas

koolo-map generates layouts from vanilla D2 level data. On Reimagined the **randomized areas do not match**: the Arcane Sanctuary journal and Summoner, the true tomb and its Orifice, waypoint pads, level exits (Spider Forest → Flayer Jungle "no qualifying entrance"), quest chests, and likely the "walls" the grid invents. Towns and fixed layouts (Palace, tombs' interiors) are right. The seed read (`466817790`) has also not changed across ~20 relogs — possibly stuck.

**Rule used everywhere now:** a live object is one with **unit ID ≠ 0**. `game.MemoryReader` merges map presets into `Objects` with ID 0, so `findLive` requires a unit ID. Live beats map; map presets are only guesses, and refuted guesses persist per seed.

**Next big job (owner offered, not started):** exits by exploration. Find level exits from what the game streams (warp tiles and objects), not from `AdjacentLevels`. It unblocks all of Act 3 and later acts.

## Built this session (all live-tested unless noted)

**Quest and route**

- Act 2: portal hops.
  - Palace Cellar 3 → Sanctuary portal.
  - `routeVia`: Sanctuary via Cellar 3, Canyon via the Sanctuary, tomb via the Canyon.
  - Sanctuary journal search: live journal → map guesses → wing-by-wing walk (persisted) → sweep.
- Tombs and the Orifice.
  - True tomb by live Orifice. There's a 15-minute sweep per tomb, and the last tomb standing is never refuted.
  - Socket errand (`socket.go`, table `socketJobs`): hover-click the object, lift the artifact, place it in the slot, press **Transmute**. The anvil geometry is upstream's 1280×720 points × 1050/720. Owner-confirmed working.
- Act 3.
  - `Act3Itinerary`: Docks → Travincal, with the Khalim relic legs (chests 405, 406, 407).
  - Act 3 town NPCs: Hratli (repair), Ormus (restock, fence, heal), Cain3 (identify).
  - NPC seek: neighbour hint (Hratli ← Meshif2), then town circles snapped to walkable cells, then follow the last sighting when the NPC flickers at the stream edge.
- Waypoint objective: an area with a pad in levels.txt and no lit record gets its pad touched at any distance.
- The quest log is unreadable on this build (all d2go quest bytes are 0). Artifacts and places are the only quest truth.

**Combat (Leap-only spec)**

- **Leap-first:** `policy.Config.LeapFirst` means any feasible leap wins; the cooldown is 0.7s.
- **Evasive march:** while marching, fight only when pressed (HP < 65, or 4+ enemies within 8); field travel may leap.
- **Mana:** `MPPct` is computed against the observed peak; the old reading showed 209% because MaxMana omits gear. Blues are drunk at 35%, and the belt keeps 2 blue columns (`MinMPCols`).
- **Breakout:** the surrounded escape also needs HP < 70. After a breakout, that landing area gets no waypoint ride for 15 minutes (`hotLanding`).

**Loot and inventory**

- **Mod-table classification:** `loot.Classify` uses gamedata for code, type and size. Mod rows 508–522 are Warlock grimoires.
- **Strong uniques only:** no duplicates (stash, worn or bag), no uniques outlevelled by more than 8, **no normal-tier bases** (uniques are unidentified on the ground, so the base tier is the ground rule), and no quivers without a bow.
- **Shape-aware fit:** `FitsShape` checks for a free W×H block.
- **March-first loot:** only within 8 tiles while marching, and a 25s streak earns a 60s rest; quest items are exempt.
- **Pickup loops:** repeated deaf pickups get a 15-minute ban.
- **No junk from walking:** a walk click dodges a hovered item.
- **Shed errand:** in a calm field, the bag's sell list is dropped with Ctrl+click.
- **No re-looting:** a unit ever carried is never re-looted.
- **Scrolls:** bought from the read vendor stock and judged by the tome's quantity.
- **Unload:** goes home by the lit pad when there are no TP charges, and waits for tier A+ loot within 25 tiles.

## Known open problems

1. **Map-oracle exits and walkability in randomized areas.** See above; this is the root of most stalls.
2. **Flayer Jungle density.** Level 34 keeps breaking out there. An owner-lit Lower Kurast or Bazaar pad would bypass it.
3. **D2R crashes.** Six on 09-25:
   - Four in the Arcane Sanctuary: the particle limit, "animated mesh particles" up to 306/128.
   - Two in the Spider Forest with no particle spam; the log ends at the bot's shift line (the owner doubts shift is the cause).
   - There is no unattended launcher, so the owner must relaunch.
   - A 6-minute Sanctuary relog clock exists (`sanctuaryBudget`).
4. **Khalim's Will:** the cube transmute (flail + eye + brain + heart) and the Compelling Orb smash are not built. The itinerary stops at Travincal.
5. **Cube storage:** the owner asked for it, but it is deferred ("don't linger").
6. **Tomb-pick log wording:** a fallback tomb pick still says "the Horadric Orifice is on its map".

## Tools

- `build/look.exe -r 0 -n 0`: area and position. `-presets` works only when the reader has the map.
- `build/invdump.exe`: bag with mod codes and sizes; uniques with stash page; quantities; quest bytes; durability.
- Restart the bot:
  1. `pwsh tools/restart_azbot.ps1 -Tag rNN -Seconds 3600 -Janitor [-Force]`
  2. Force D2R to the front first with scratchpad `forcefg.ps1`.
  3. Stop gently with `touch logs/stop.now`: WindDown recalls, or pauses in the field.
- Never run a plain `go build ./cmd/azbot` at the repo root; always build with `-o build/azbot.exe.new`.
- Log sink allowlist (`cmd/azbot/main.go`): quest, waypoint, loot, stash, identify, fence, belt, door and stand outcomes reach the log.

## Owner rulings this session

- Progress first: advancing, waypoints and quests come before inventory (quest items excepted).
- Only strong uniques ("above the baseline"). Drop junk rather than carry it; keepers stay.
- Waypoints are a click. Hratli sits south at the smithy, sometimes by Meshif.
- Leap Attack only; don't use regular attacks.
- Mana potions exist for the leap: drink them.
