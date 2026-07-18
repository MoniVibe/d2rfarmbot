#!/bin/bash
# Overnight aquarium soak — MAMAZON EDITION (2026-07-18 night). Sequential self-exiting
# farm runs (never force-kill farmbot while D2R is alive; one farmbot at a time — both
# disciplines hold by construction).
#
# The character: Mamazon, level-1 softcore Amazon, javelin+buckler, Jab on F1 (mod grant),
# TP tome on F3, ID tome on F4 (bound via -bindskill probes). No summons, no casts — Jab
# via -melee, passives via the -autoskill build list, stats via -autostat, all verified by
# memory readback. Route sticks to OPEN-AIR areas only: Blood Moor (to lvl 6), Cold Plains
# (to 12), Burial Grounds (to 18, Blood Raven lives there), Stony Field (terminal). Cave
# mouths are fenced in-code (the static-click law: id=0 stairs never respond — a cave
# entered is a night lost), with a TP-out seatbelt if a fence ever leaks.
source /c/dev/tools/goenv.sh 2>/dev/null
cd /c/dev/koolo-build
for i in $(seq 1 36); do
  if ! tasklist.exe 2>/dev/null | grep -qi "D2R.exe"; then
    echo "[$i] D2R gone — stopping ($(date +%H:%M:%S))"
    break
  fi
  # Staged deploy: a farmbot_next.exe dropped here is swapped in between runs — combined
  # with `echo exit > logs/control.txt` (graceful in-run shutdown) deploys never wait.
  if [ -f farmbot_next.exe ]; then
    mv -f farmbot_next.exe farmbot.exe && echo "[$i] deployed staged binary $(date +%H:%M:%S)"
  fi
  echo "[$i] run start $(date +%H:%M:%S)"
  timeout 960 ./farmbot.exe -seconds 900 -move e -runwalk r -nav -livegrid -chicken 30 \
    -radius 90 -diff normal -dpiscale 1.25 \
    -werewolf "" -rabies "" -spirit "" -wolves "" -creeper "" \
    -summon "" -golem "" -melee f1 -throw f2 -meleebelow 10 -attackrange 4 \
    -loot -objects -autoprogress -route "2:6,3:12,17:18,4:99" \
    -tp f3 -idkey f4 -cubekey "" -tripitems 22 -townhub "5992,4941" \
    -autostat "347,305,347,428,347,552" \
    -autoskill "1523,201,1651,279,9,3;1380,201,1397,279,10,6;1523,201,1524,365,13,2;1523,201,1524,625,29,2;1523,201,1651,449,23,3;1523,201,1651,279,9,10;1380,201,1397,279,10,15;1523,201,1651,449,23,10;1523,201,1524,365,13,8;1523,201,1524,625,29,8" \
    > logs/overnight_$i.log 2>&1
  echo "[$i] exit=$? end $(date +%H:%M:%S)"
  ./farmbot.exe -charprobe 2>&1 | grep -oE 'level=[0-9]+ xp=[0-9]+ area=[0-9]+' | head -1
  echo "[$i] bites=$(grep -cE 'msg=bite' logs/overnight_$i.log) throws=$(grep -cE 'msg=throw' logs/overnight_$i.log) deaths=$(grep -cE 'DEAD' logs/overnight_$i.log) recovered=$(grep -cE 'corpse: RECOVERED' logs/overnight_$i.log) loot=$(grep -cE 'loot: picked' logs/overnight_$i.log) drinks=$(grep -cE 'msg=drink' logs/overnight_$i.log) skillups=$(grep -cE 'autoskill: result' logs/overnight_$i.log) statups=$(grep -cE 'autostat: spent' logs/overnight_$i.log) fences=$(grep -cE 'fence: trap mouth' logs/overnight_$i.log) traps=$(grep -cE 'TRAP AREA' logs/overnight_$i.log) trips=$(grep -cE 'towntrip: AUTO|towntrip: cast' logs/overnight_$i.log) sold=$(grep -cE 'vendor: SOLD' logs/overnight_$i.log)"
  sleep 5
done
echo "overnight soak finished $(date +%H:%M:%S)"
