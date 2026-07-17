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
  # Staged deploy: a farmbot_next.exe dropped here is swapped in between runs — combined
  # with `echo exit > logs/control.txt` (graceful in-run shutdown) deploys never wait.
  if [ -f farmbot_next.exe ]; then
    mv -f farmbot_next.exe farmbot.exe && echo "[$i] deployed staged binary $(date +%H:%M:%S)"
  fi
  echo "[$i] run start $(date +%H:%M:%S)"
  timeout 960 ./farmbot.exe -seconds 900 -move e -runwalk r -nav -livegrid -chicken 30 \
    -radius 90 -diff normal -dpiscale 1.25 \
    -werewolf "" -rabies "" -spirit "" -wolves "" -creeper "" \
    -attackrange 25 -meleebelow 15 -loot -objects -autoprogress \
    -summon f1 -maxpets 4 -golem f2 -autostat "347,305,347,428,347,552" \
    > logs/overnight_$i.log 2>&1
  echo "[$i] exit=$? end $(date +%H:%M:%S)"
  ./farmbot.exe -charprobe 2>&1 | grep -oE 'level=[0-9]+ xp=[0-9]+ area=[0-9]+' | head -1
  echo "[$i] bites=$(grep -cE 'msg=bite' logs/overnight_$i.log) deaths=$(grep -cE 'DEAD' logs/overnight_$i.log) recovered=$(grep -cE 'corpse: RECOVERED' logs/overnight_$i.log) loot=$(grep -cE 'loot: picked' logs/overnight_$i.log) drinks=$(grep -cE 'msg=drink' logs/overnight_$i.log) objects=$(grep -cE 'object: interacted' logs/overnight_$i.log) skillups=$(grep -cE 'autoskill: result' logs/overnight_$i.log)"
  sleep 5
done
echo "overnight soak finished $(date +%H:%M:%S)"
