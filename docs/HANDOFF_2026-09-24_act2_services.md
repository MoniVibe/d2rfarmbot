# HANDOFF — 2026-09-24 — Act 2 town services, aim, navigation (MSI laptop)

Branch `laptop-3.2` pushed to `public` (github.com/MoniVibe/d2rfarmbot) at 12a2aa9.
`origin` is the UPSTREAM fork (danonji89/koolo_Fixed) — never push there.

## State at close
- Fableboi (Barbarian) **level 26**, alive, last seen Dry Hills / Lut Gholein, gear on,
  belt full (3x id 603 health + 4x id 608 mana). D2R closed by the owner.
- Bot stopped; input healed; no worktrees besides main.

## Operating contract (owner, 2026-09-23)
- D2R focused is the proven mode, but the owner uses the laptop (Discord in front),
  so `-fakefocus` defaults ON (posts WM_ACTIVATE*). Menus/shops foreground D2R
  themselves (real input only).
- Restart ONLY in town or with no living monster within 40 tiles:
  `pwsh tools\restart_azbot.ps1 -Tag <x>` (enforces it; `-Classic` = old nav).
- Before a field run: `build\azbot.exe -selftest` in town (6/6 = focus, in-town,
  belt, NPC aim, trade-open, one verified potion buy).

## Proven this session (live evidence)
- Belt potion id **603** ("SmallCharm" in the scrambled table) = health; 608 = mana.
  Sentinel drinks verified.
- Aim calibration `logs/aimcal.json` (cmd/aimcal): tiles 11% larger than koolo
  19.8/9.9, player 27.5 px above center, unit hover box 52.7 px above feet.
- Drognan (Act 2) purchase chain: talk -> real-key Trade -> tab + real RIGHT click
  -> gold/count verified. Restock filled the belt 1 -> 7 in a live run.
- Screen oracles (memory cannot see them): pause menu, pause sub-panels
  (Chronicle/Loot Filter), shop open (red X or dark grid). Tests on real captures
  in internal/game/testdata.

## Built, NOT yet proven live
- Fence (sell junk): measured 10x8 inventory grid + real Ctrl+click + exactly-this-
  item-gone check (halts 30 min if anything else leaves the bag). Watch the first
  `verb=fence` lines.
- Deliberate navigation (AZBOT_DELIBERATE=1 via restart script): route intent,
  seam hysteresis, escalation ladder, Journey-routed crossings. Watch
  `verb=intent` / `verb=escalate`, the 40<->41 gate bounce, cave-mouth entries.
- Tracked hover for NPC talk and attacks (fresh positions per probe).

## Open, in priority order
1. Repair at Fara: repair-all button still a posted Act 1 click — needs a screenshot
   of Fara's panel, measured button, real click (same pattern as buy/fence).
2. Potion looting gated OFF (`activity.LootPotions`): item piles (90+ drops) let
   neighbours own the hover. Fix = accept any wanted item under the cursor.
3. Melee fights still slow: aimed swings often land on neighbours; widen melee
   accept and track targets.
4. Waypoint panel + repair not in the self-test yet.
5. 14+ banked skill points ("no proven REACH TOOL") — spend policy for the barb.

## Tools
look.exe (memory -> situation report), shot.exe (true 1920x1050 capture),
ctl.exe (key/hover/click/realclick/state), aimcal.exe, panelprobe.exe, beltdump.exe.
