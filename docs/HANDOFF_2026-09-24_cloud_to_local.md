# HANDOFF — 2026-09-24 — cloud agent → local (laptop) agent, full ownership

The cloud agent is stepping away. **The laptop agent now owns everything:** code,
testing, merging, live runs, decisions within the owner's rulings below.

## Where things are
- Working branch: `claude/diablo-bot-states-x834xf` on MoniVibe/d2rfarmbot (PUBLIC), HEAD `1837b4e`+.
  Draft PR MoniVibe/d2rfarmbot#1 (base `laptop-3.2`). Merge to `laptop-3.2` when you're satisfied.
- d2go fork: `../d2go-local` = MoniVibe/d2go (private). Mod data: MoniVibe/d2go branch `moddata`
  (private; NEVER commit mod/Blizzard data to the public repo). At runtime the bot reads it from
  `C:\Program Files (x86)\Diablo II Resurrected\mods\D2RMM\D2RMM.mpq` (`-moddata`).
- Test gate: `tools/test.sh` (Linux/WSL: pure tests natively, Windows tests via wine, build + vet).
  On Windows: `go test ./internal/azbot/... ./internal/game/ ./cmd/azbot/` and
  `go build -o build\azbot.exe.new .\cmd\azbot` (never a plain `go build ./cmd/azbot` — it
  overwrites the tracked azbot.exe). Ratchet tests guard ESC use, time.Sleep counts, and movement
  (only `internal/azbot/moveto` may move the character).
- Plans: `docs/AZBOT_V2.md` (state model, done), `docs/AZBOT_V3_FOUNDATIONS.md` (review + roadmap),
  `docs/AZBOT_MISSION.md` (mission layer design — the next big build), `docs/RELAY.md` (retire it
  now that you drive locally, or keep the PR thread as a run log).

## Owner rulings (binding)
- Offline, modded (Reimagined 3.0.10 via D2RMM), single-player only. Never Battle.net.
- Session end: town if easy, else ESC-pause confirmed by sight and exit; death beyond that is OK.
  The ESC pause menu DOES freeze the world. Unattended runs are fine (D2R focused).
- Loot: uniques, runes, gems, charms, quest items, mod rows (orbs etc.) only; skip a unique charm
  already carried. Everything else skipped for now (`config/loot.yaml`).
- Build: Leap Attack core (143); Carnage = skill 144 on LMB (Whirlwind-like, req 18);
  1 point Frenzy (147) for the buff; explore the rest. Later: "science" builds with respec tokens.
- Direction approved: replace priority bidding with scripted quest missions (docs/AZBOT_MISSION.md).
- VM: laptop is Windows 11 Home (no Hyper-V). Plan = give the bot the laptop while it runs (focused
  input); Pro upgrade or a second PC is the owner's call.

## Merged on the branch, NOT yet proven live (R13 ran at a5cbc70; R14 queued at 873a407)
- HP% over the effective max life (MaxLife stat omits bonuses; read ~127 at full).
- Learned Leap-vs-Carnage combat (`verb=choose/strike`, `engage summary`, `leap aoe`).
- Coverage exploration persisted per seed/area (`logs/coverage_*.png`).
- Map fusion: live > atlas > trusted maphack > priced unknown (`mapfuse … trust=`, `logs/mapfuse_*.png`).
- Loot tiers + 10×8 bag + room-making; Fence never sells cube/runes/charms.
- R10 fixes: service panel lock, fence pickup put-back, same-NPC handoff, cursor item never dropped
  in town, watchdog never convicts a holder at its panel / with cursor item, wind-down town-or-pause.
- MoveTo = sole mover (65 sites), steerAround deleted, move-guard ratchet.
- Door fixes: Up/Down-aware entrance pick, arrival-door exclusion, search never takes back-doors,
  contact ≤6 tiles on Arrived, 3s click verdict, own-portal-first return.
- gamedata: mod tables typed (items/uniques/monsters/levels/warps/objects/skills). Verified: skill
  144=Carnage, 143 Leap Attack, 147 Frenzy; misc +15 shift from Warlock grimoires; orbs 759–797;
  Act 2 normal mlvls 16–18; LvlWarp/object click boxes; quest objects 354/356/149/152.

## Next, in order
1. Run R14 (60 min, `-Janitor`, from Lut Gholein). Check: Maggot Lair L1→L2→L3 without bouncing;
   `arrival door`, `landed wrong area`, `portal-first`, `verb=move`, `mapfuse`, `coverage`, loot census,
   `hp=` ≈100 at full, wind-down ends in town or paused.
2. Level gating from gamedata (mission step 1): MinLevel = mlvl−3 / level-gap rule; fixes the L27 stall.
3. Skill plan profile (`config/azbot/<char>.yaml`) + Spend using skills.txt prereqs and skilldesc tree
   seats: Leap Attack, Frenzy 1, Carnage; bank the rest.
4. Aim with gamedata hitboxes: entrances (LvlWarp select box), objects, monsters (monstats2).
   Pickups landed 0/101 in old runs — fix the pickup verb with item boxes.
5. Game systems from the review: repair at Fara (measured button, real click), merc revive + potions,
   rejuv lane + chicken, elite/immunity awareness, battle cries if taken.
6. Mission layer (docs/AZBOT_MISSION.md steps 2–8): world books + shadow mode → runner (interrupt-only
   arbiter) → TownRoutine → objectives + Act 2 quest script → science harness → delete Advance & co.
7. Focused-only input mode (SendInput only, no in-process patches/fake focus) for unattended runs.

## Known open issues
- Fence Ctrl+click may pick items up instead of selling ("SELL BECAME A PICKUP") — measure.
- Pickup verb unproven (0/101 historically). Potion looting gated off.
- Inventory-key backup door never proven to open the bag.
- Some unique ids after the "Warlock Class Pack" row may be off by one vs the table comment.
- Orbit verdicts in mazes (watch after MoveTo/coverage).
