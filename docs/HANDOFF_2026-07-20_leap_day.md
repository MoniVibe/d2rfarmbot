# HANDOFF — 2026-07-20, the leap day (session end ~14:00)

## State at close
- **Fableboi (Barbarian) is DEAD — again — with health potions ON HIM.**
  Level 13, corpse in the Underground Passage (area 10), likely a second
  corpse from the owner's manual recovery run. The owner ended the session
  here ("we will pick this up later"). azbot is STOPPED, input healed
  (farmbot -fixinput ran; the owner's mouse works).
- Game waypoint list shows **Stony Field LIT** (photo-proof, 13:55
  logs/shot.png) — but `wplit.Fableboi.4` is NOT in the WAL. **Seed it at
  next launch** or town rides won't use it.
- Gold ~7.8k. Cube (id 549) safe all session. Uniques-only loot doctrine
  active (owner's order, 13:08).

## THE OPEN WOUND — died with potions on him (owner's last report)
Prime suspect, ties three threads together: **the mod's potions carry IDs
the percept filter doesn't recognize** (P-4.9 discovered "a belt full of
strangers" reading as EMPTY — BeltHP counts only id 602/"Healing"). The
Sentinel drinks by HPCols (belt columns of RECOGNIZED potions) — if his
bottles are unknown IDs, HPCols is empty/wrong and the drink presses
nothing while he dies at hp=1 with a full belt (13:03 death report shows
`sentinel: drink col=1 hp=1` — one drink, too late, possibly wrong column).
**Next session: read his actual belt item IDs live and extend the
recognizer (602/607 are Mamazon-era; the barb's drops differ).**

## What shipped today (~25 commits, all pushed on laptop-3.2)
1. **Advisor's first patch** (docs/ADVISOR_RESPONSE_HOVER_2026-07-20.md):
   WARNING 11 hostage law (SendMessageTimeout 150ms + slow-send logging —
   only ~3 marginal 60-70ms sends seen all day), lParam space corrections,
   conveyor-belt hover rule (hold offset 2 fresh samples, fan 26→10),
   P-2.10 mutual-veto watchdog (in-reach lock silent 1.2s → quarantine).
2. **Aggression doctrine** (owner ×3): brawler Fight radius = EYESIGHT (45,
   all ground); raiser locks pursued through rings and never stolen by
   biters; whiff→march-swing fallback (no silent Fight cycles); horde-aware
   door bracket.
3. **P-2.11 THE VAULT (Leap, F5=skill 132)** — full economy PROVEN by 13:55
   (mana debited across the click): RoleVault prior → Capability.Vault;
   verbs.Vault readback-verified, displacement-judged, RANGE-CLAMPED to 9;
   VK_RBUTTON key-state override on ClickRight (THE cast fix — right never
   had left's interactClick medicine); MANA HUNGER (contact skill stands
   down 4s when a leap site is mana-short — Double Swing was drinking every
   mana-per-hit point); sites: ring escape (LEAP OUTRANKS PORTAL, multi-
   sector multi-range, refusals written), raiser leap, travel gait (6s,
   road+march), seam leap (mapWalk-vouched), door leap (clickTry 12),
   wp-pad leap.
4. **The fixed-point lie**: player stats are RAW on this repack — `>>8`
   zeroed MaxMana since the Mamazon era (33-mana barb read 0, Double Swing
   silently disabled, mana wells never drunk). Magnitude heuristic decode;
   mp/maxmana in every status line (TELEMETRY LAW: any stat a doctrine
   gates on must print). Saved to permanent memory.
5. **Charge-flee oscillator killed**: 65 flee grants/16min at 96 blood —
   brawler flee now needs blood <55 plus ring/density. (Watch: both UP
   deaths came AFTER this — the gate may now be too permissive for UP
   density at his level; balance against the potion fix first.)
6. **P-4.9 belt-eats-nothing**: Akara potion spam (~2.3k gold) closed —
   occupancy-based free slots, beltFrozen wired, every abandon cools.
7. **Uniques-only loot** (quality ≥7; naked-rearm + dry-quiver exceptions).
8. **Focus-polite relog**: RealEsc returns false without VERIFIED
   foreground (no more ESCs sprayed into the owner's desktop — Windows
   blocks foreground-steal while the owner works; yesterday's "proven
   live" was proven on an empty desktop); six state-aware ESCs; honest
   backoffs. 13:52-13:55 residual: ESCs verified-delivered but the
   OWNER'S OPEN WAYPOINT PANEL ate them — when the owner drives, the bot
   must be OFF (done by hand this time; consider an owner-input detector).
9. **70-tile waypoint detour** (was 40/30s → 70/60s + travel leap + nav
   `wp-touch` lines) — Stony's pad got lit this session.
10. Swap-waiter hardening: `tail -60 | grep -v sendinput` (sendinput spam
    buried the 3-line window detection).

## Open, in priority
1. **Belt potion IDs** (the death-with-potions wound) — read live belt,
   extend 602/607 recognizer, verify Sentinel drink columns.
2. **Seed `wplit.Fableboi.4`** in logs/azmem/facts.wal at next launch.
3. **UP deaths (2×)**: blackbox_1784541829.jsonl + barb38 death report
   unread in depth. What melts 100+ HP at level 13 — density + no working
   drinks is the leading theory; verify after (1).
4. Leap: confirm clean `ev=leapt` in solo play (cast proven, displacement
   verdict was polluted by owner co-driving).
5. Relog end-to-end under the new focus etiquette (needs an idle desktop
   or owner-granted focus).
6. Advisor queue: HOLDER/PHASE visible line, StepKind protocol, cursor
   epochs, pump experiment matrix A-D, per-message send latency
   instrumentation.
7. Cave-entry (UP mouth) arch-clicks: breaker fired at a Stony door again
   13:37 — the ritual + door-leap still unproven on a live mouth.
8. Fence NPC ring still Mamazon-hardcoded (flagged, unfixed).

## Ritual reminders (unchanged)
- Build: cd /c/dev/koolo-build && PATH=/c/dev/tools/goroot/go/bin:$PATH
  GOTMPDIR=/c/dev/koolo-build/gotmp go build -o azbot.exe.new2 ./cmd/azbot
- Swap: taskkill azbot → farmbot -fixinput → cp → relaunch
  `-goal rampage -meleekey f3 -seconds 86400 > logs/barbN.log 2>&1 &`
  (barb43 is next). NEVER two azbot processes. cd FIRST — cwd resets.
- STE spec was REFORMATTED by the owner today — read structure before editing.
