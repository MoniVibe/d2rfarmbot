#!/usr/bin/env bash
# Linux-side test gate: pure packages natively, everything else as Windows test
# binaries under wine (apt-get install wine). Plus the Windows build + vet.
set -euo pipefail
cd "$(dirname "$0")/.."
export WINEDEBUG=-all
go test ./internal/azbot/nav/... ./internal/azbot/arbiter/... ./internal/azbot/phase/... \
  ./internal/azbot/screen/... ./internal/azbot/exec/... ./internal/azbot/trace/... ./internal/azbot/route/... \
  ./internal/azbot/watchdog/... ./internal/azbot/unstick/... ./internal/azbot/hud/... \
  ./cmd/strikereplay/... ./internal/azbot/combat/learn/... ./internal/azbot/combat/policy/... ./internal/azbot/coverage/... ./internal/azbot/loot/... ./internal/azbot/mapfuse/...
wine cmd /c exit >/dev/null 2>&1 || true # first wine start creates its prefix; don't let it fail a test
GOOS=windows go test -exec wine ./internal/azbot/... ./internal/game/
GOOS=windows go build -o /dev/null ./cmd/azbot ./cmd/stridecal ./cmd/shot
GOOS=windows go vet ./internal/azbot/... ./cmd/azbot
echo "ALL GREEN"
