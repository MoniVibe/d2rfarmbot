# Campaign plan — 2026-09-26 (full authority)

Goal (owner): an idempotent bot that runs the whole D2R campaign (Reimagined,
offline) as fast as possible, preferably in the background.

Test character: **KillaryClinton** (assassin, trap opener + melee), fresh in Act 1.
Fableboi (barb 34, Act 3) stays as the mid-campaign regression character.

## Findings that shape the plan

1. **The map seed read is broken.** `getMapSeed` (actMisc+0x840/+0x868) gives
   466817790 on every relog; single-player D2R re-rolls the seed per game. That
   is why koolo-map is right on fixed layouts (towns, palace) and wrong in every
   randomized area (tombs, Sanctuary, waypoints, exits, chests).
2. **The game holds the real map.** DrlgLevel → Room2 list → PresetUnits gives
   every room rectangle and every preset (waypoint, chests, journal, Orifice,
   stairs/cave mouths) at exact positions. Decoded and validated (see
   `internal/game/drlg.go`, `cmd/drlgprobe`). Neighbouring levels are chained
   via DrlgLevel+0x1B8.
3. **Background play is half there.** World play (walk, fight, potions,
   waypoint rows) runs unfocused via posted messages + in-process key/cursor
   patches. Every panel action (NPC menus, shop, stash, inventory, belt
   refill, socket, relog ESC) needs a real foreground.
4. **Crashes need a supervisor.** D2R is started as
   `D2R.exe -mod D2RMM -txt ""`; nothing relaunches it today.

## Workstreams, in order

| # | Workstream | Why first | Done when |
|---|---|---|---|
| P0 | **Live map truth**: presets + room graph replace koolo-map objects/exits | Root of most stalls in randomized areas | Bot finds waypoints, cave mouths, stairs, chests from live presets; koolo-map only a fallback |
| P1 | **Campaign content**: Act 3 Khalim's Will + Orb + Mephisto; Act 4 (Diablo seals); Act 5 (Ancients, Baal) | Itinerary stops at Travincal | Itineraries + quest hooks for every required quest |
| P2 | **Supervisor**: relaunch D2R after a crash, menu → Play, restart azbot | Unattended runs die on every crash | A killed D2R is back in game and botting with no human |
| P3 | **Background mode**: batch every panel action into short foreground windows; world play unfocused | Owner preference | A run where D2R stays unfocused except town visits |
| P4 | **Speed**: fewer town visits, waypoint-first routing, skip optional quests, trap build | Least time | Campaign clock tracked per act in the log |

Required quest path only: A1 Andariel · A2 staff → Summoner → Duriel · A3
Khalim's Will → Travincal → Mephisto · A4 Izual is optional, Diablo (seals)
· A5 Ancients → Baal. Optional quests are skipped unless they pay time back
(e.g. the Act 1 cold plains waypoint route is free).
