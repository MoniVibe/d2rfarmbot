#!/bin/bash
# Overnight aquarium soak: sequential self-exiting farm runs (never force-kill farmbot
# while D2R is alive; one farmbot at a time — both disciplines hold by construction).
source /c/dev/tools/goenv.sh 2>/dev/null
cd /c/dev/koolo-build
for i in $(seq 1 36); do
  if ! tasklist.exe 2>/dev/null | grep -qi "D2R.exe"; then
    echo "[$i] D2R gone — stopping ($(date +%H:%M:%S))"
    break
  fi
  echo "[$i] run start $(date +%H:%M:%S)"
  timeout 960 ./farmbot.exe -seconds 900 -move e -runwalk r -nav -livegrid -chicken 30 \
    -radius 90 -diff normal -dpiscale 1.25 \
    -werewolf "" -rabies f1 -spirit "" -wolves "" -creeper "" \
    -castrange 25 -meleebelow 15 -loot -objects -autoskill "1522,201,1524,279" -autoprogress \
    -summon f2 -maxpets 1 \
    > logs/overnight_$i.log 2>&1
  echo "[$i] exit=$? end $(date +%H:%M:%S)"
  ./farmbot.exe -charprobe 2>&1 | grep -oE 'level=[0-9]+ xp=[0-9]+ area=[0-9]+' | head -1
  echo "[$i] bites=$(grep -cE 'msg=bite' logs/overnight_$i.log) deaths=$(grep -cE 'DEAD' logs/overnight_$i.log) recovered=$(grep -cE 'corpse: RECOVERED' logs/overnight_$i.log) loot=$(grep -cE 'loot: picked' logs/overnight_$i.log) drinks=$(grep -cE 'msg=drink' logs/overnight_$i.log) objects=$(grep -cE 'object: interacted' logs/overnight_$i.log) skillups=$(grep -cE 'autoskill: result' logs/overnight_$i.log)"
  sleep 5
done
echo "overnight soak finished $(date +%H:%M:%S)"
