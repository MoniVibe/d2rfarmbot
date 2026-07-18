#!/bin/bash
# step.sh — ergonomic driver for the farmbot STEP harness.
#   ./step.sh hold                          freeze behaviors (ACKED: re-writes until the log confirms)
#   ./step.sh "aim 761,377" "rawclick 761,377" "shot"    batch of step commands
#   ./step.sh resume                        release
# Prints logs/step_result.txt when the batch completes; screenshots land in shots/step_N.png.
#
# WHY THE ACK LOOP: control.txt is polled once per main-loop tick, and under goto churn a
# tick can take many seconds (measured: 23s lag). A single "hold" write gets overwritten by
# the next step batch before the bot ever reads it — the bot then fights the pilot for the
# mouse. Hold now re-writes every 2s until the "STEP MODE" line appears in the newest log.
cd /c/dev/koolo-build
if [ "$1" = "resume" ]; then
  echo resume > logs/control.txt
  echo "resume sent"
  exit 0
fi
if [ "$1" = "hold" ]; then
  RL=$(ls -t logs/overnight_[0-9]*.log logs/devrun_*.log logs/manual_*.log 2>/dev/null | head -1)
  N0=$(grep -c "STEP MODE" "$RL" 2>/dev/null); N0=${N0:-0}
  for i in $(seq 1 20); do
    echo hold > logs/control.txt
    sleep 2
    N1=$(grep -c "STEP MODE" "$RL" 2>/dev/null); N1=${N1:-0}
    if [ "$N1" -gt "$N0" ]; then
      echo "hold CONFIRMED (write $i, log $RL)"
      exit 0
    fi
  done
  echo "hold NOT CONFIRMED after 20 writes — is a run alive?"
  exit 1
fi
rm -f logs/step_result.txt
{ for c in "$@"; do echo "step $c"; done; echo "step shot"; } > logs/control.txt
for i in $(seq 1 100); do
  [ -f logs/step_result.txt ] && break
  sleep 0.3
done
cat logs/step_result.txt 2>/dev/null || echo "TIMEOUT: no result after 30s"
