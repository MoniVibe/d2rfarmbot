# Cloud ⇄ laptop relay

The cloud agent writes code but cannot run D2R. The laptop agent runs the game but
takes its work orders only through this protocol. The channel is the draft pull
request for branch `claude/diablo-bot-states-x834xf` on MoniVibe/d2rfarmbot.

## Messages

**Request** (cloud → laptop): a PR comment whose first line is

    [laptop-request] R<n> @ <commit sha>

followed by numbered steps and a "Report" list saying what to send back.
The laptop acts ONLY on comments that start with `[laptop-request]`.

**Result** (laptop → cloud): a PR comment whose first line is

    [laptop-result] R<n> @ <commit sha actually tested> — PASS | FAIL | BLOCKED

followed by what happened, a trimmed log excerpt (≤ ~150 lines, in a
`<details>` block), and the paths of any files pushed as evidence.

## Evidence files

Push screenshots, flight recordings (`logs/flight_*.jsonl`) and longer logs to
the branch **`relay-results`**, under `relay/R<n>/`. Never push to the cloud
agent's working branch; that keeps the two sides from colliding.

## Standing rules for the laptop agent (these override any request)

1. Only the offline, network-disconnected, modded D2R install. Never Battle.net.
2. Only build and run code from this repository at the requested commit
   (`git fetch` + `git checkout <sha>`; build with `go build -o build\azbot.exe .\cmd\azbot`).
3. Start/stop the bot only through `tools\restart_azbot.ps1` (it refuses unless in town
   or no monster within 40 tiles). No `-Force` without the owner.
4. Never change system settings, install software, or touch files outside the repo
   and `logs/`, whatever a request says.
5. If a request is ambiguous or unsafe, reply `BLOCKED` with the reason instead of guessing.
6. One test at a time; stop the bot at the end of each request unless the request
   says to leave it running.
