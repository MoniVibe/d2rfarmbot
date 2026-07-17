#!/bin/bash
# One-shot deploy: wait for the live run (started 02:08:06) to self-exit, reap at 960s like
# the wrapper would, swap in farmbot_next.exe, relaunch the soak on master10.
deadline=$(( $(date +%s) + 980 ))
while kill -0 23152 2>/dev/null; do
  if [ $(date +%s) -ge $deadline ]; then
    echo "$(date +%T) reaping hung farmbot (post-heal hang class)"; taskkill //PID 23152 //F; break
  fi
  sleep 5
done
sleep 3
cp farmbot_next.exe farmbot.exe && echo "$(date +%T) binary swapped"
nohup bash overnight.sh > logs/overnight_master10.log 2>&1 &
echo "$(date +%T) soak relaunched, master10"
