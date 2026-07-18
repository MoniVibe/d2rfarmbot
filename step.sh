#!/bin/bash
# step.sh — ergonomic driver for the farmbot STEP harness.
#   ./step.sh hold                          freeze behaviors
#   ./step.sh "aim 761,377" "rawclick 761,377" "shot"    batch of step commands
#   ./step.sh resume                        release
# Prints logs/step_result.txt when the batch completes; screenshots land in shots/step_N.png.
cd /c/dev/koolo-build
if [ "$1" = "hold" ] || [ "$1" = "resume" ]; then
  echo "$1" > logs/control.txt
  echo "$1 sent"
  exit 0
fi
rm -f logs/step_result.txt
{ for c in "$@"; do echo "step $c"; done; echo "step shot"; } > logs/control.txt
for i in $(seq 1 100); do
  [ -f logs/step_result.txt ] && break
  sleep 0.3
done
cat logs/step_result.txt 2>/dev/null || echo "TIMEOUT: no result after 30s"
