# HANDOFF - 2026-09-23 - azbot resume (for a local agent on the MSI laptop)

Scope: OFFLINE single-player D2R, modded via D2RMM. Owner's own game, own machine. Never Battle.net online play, never online mod servers.

## Machine / paths (laptop "MSI")
- Game: `C:\Program Files (x86)\Diablo II Resurrected\D2R.exe` (v3.2.27241). **Launch ONLY via the Battle.net app** (it's running; direct D2R.exe launch crashes: `Failed to connect to bgs: 1001` -> ntdll 0xc0000005). In Battle.net game settings, "Additional command line arguments" = `-mod D2RMM -txt` (owner set it).
- Mods: D2RMM 1.9.1 at `...\Diablo II Resurrected\D2RMM 1.9.1\` (Reimagined, IncreaseDroprate, BuffedMercenaries, MercEquip, SingleplayerRunewords, SocketPunching, FusedUniques, Unique To Jewel, Rich Merchants, RunesAndGemsOnSale, QoL6_1b, ...). Merged output `...\mods\D2RMM\D2RMM.mpq`.
- Saves: `C:\Users\shonh\Saved Games\Diablo II Resurrected\mods\D2RMM\` - Fableboi.d2s (Barbarian L13), Mamazon, BenGvir, BenjiNetanyahu.
- Desktop shortcuts: "D2RMM Mod Manager" (fine). "D2R Modded (D2RMM)" points at D2R.exe directly -> WILL CRASH; repoint or delete it.
- Ignore the two cracked copies in `Downloads` (Infernal Edition / RUNE PORTABLE). Don't use them.
- Bot: `C:\dev\koolo-build` (azbot, fork of danonji89/koolo_Fixed, branch laptop-3.2, **51 uncommitted files** - checkpoint them first: review `git status`, exclude logs/shots/build/crashdumps, WIP commit, don't push without owner go). Memory lib: `C:\dev\d2go-local` (go.mod replace). Go 1.27.1 installed today at `C:\Program Files\Go\bin\go.exe` (not on PATH for ssh sessions).
- Previous handoff (read it): `C:\dev\koolo-build\docs\HANDOFF_2026-07-20_leap_day.md` + `docs\AZBOT_PROCEDURES_STE.md`.

## State right now
- D2R running (launched via Battle.net), Fableboi loaded in **Lut Gholein**. Died last session in Underground Passage (area 10); corpse (gear + belt) still there. WAL missing `wplit.Fableboi.4` (Stony Field WP is lit in game) - seed it at next azbot launch.
- New tool: `cmd\beltdump\main.go` -> `build\beltdump.exe` (read-only: attaches to D2R, prints player + belt items id/name/x/y). Ran OK: attached, read "Fableboi"; belt EMPTY (belt is on the corpse). Some d2go patterns report NOT FOUND (known on this build); hp% reads 128 (quirk).

## Next steps (in order)
1. Checkpoint the 51 dirty files (above).
2. Get potions into the belt: recover the corpse (gets the exact "stranger" potions back) or buy healing pots in town.
3. Run `build\beltdump.exe` (as the same user; admin if attach fails). Record every belt item id/name.
4. THE OPEN WOUND: extend the potion recognizer - BeltHP only counts id 602/"Healing" (Mamazon era), mod potions carry other ids -> Sentinel reads full belt as empty -> never drinks -> died at hp=1 with potions. Fix the percept filter / HPCols to include the mod's HP (and mana/rejuv) ids; add a unit test; rebuild `build\azbot.exe`.
5. Seed the Stony Field WP in the WAL, then resume Fableboi's leveling run under azbot. Watch the first death report (`sentinel: drink col=... hp=...`).
6. Write your own handoff at the end.
