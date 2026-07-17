# d2rfarmbot

An autoplay bot for a **modded, offline, single-player** Diablo II: Resurrected install. Built on a
fork of [koolo](https://github.com/hectorgimenez/koolo) / [d2go](https://github.com/hectorgimenez/d2go),
reusing their memory-reader and injector layer.

> **Read [`HANDOFF.md`](HANDOFF.md) before touching anything.** Its §0 is not boilerplate — this
> codebase has a documented history of stamping "PROVEN" on experiments that could not have failed,
> and several confident-looking claims turned out to be void. §0 tells you how to tell the difference.

## Scope and safety

- **Offline single-player only.** Point this at a Battle.net-connected account and Warden will
  permanently ban it. Nothing here is designed, tested, or intended for online play — upstream koolo
  carries the same warning and it is not decorative. A modded install is offline-only regardless.
- **Pinned to one game build** (3.0.91636). Every memory offset is tied to it; a game update drifts
  them. Frozen installs only.
- **Memory-driven input, no debugger.** All steering is `ReadProcessMemory`/`WriteProcessMemory` plus
  patching Windows *system DLLs* (user32) inside the game process — never the protected game
  executable. D2R uses Arxan anti-tamper: `DebugActiveProcess` **kills the game**, so no debugger ever
  attaches. A side effect is that the bot runs in the background while you keep using the PC.

## What works

Background movement, live-collision navigation (the grid is read from the game's own room memory, so
it is mod-accurate and needs no external map data), combat, loot and object interaction, and waypoint
travel.

## What's honest about the state

- **An open regression** — the absolute cursor-mapping fix is proven for UI panels but appears to
  break world hovering. `HANDOFF.md` §2 states the contradiction plainly and names the measurement
  that resolves it.
- **A game-side crash** that occurs with no bot attached: exit `0xffffffff`, no Windows fault record,
  because the game catches its own fault and exits. See §4 — don't waste a night blaming the injector.
- A ranked known-bad list is in §9. It includes this project's own diagnostic tools, which have their
  own blind spots.

## Build

Requires Go and CGO. `d2go` is consumed via a local `go mod edit -replace` fork.

```bash
go build -o farmbot.exe ./cmd/farmbot
```

Binaries and diagnostic output are gitignored — always rebuild.

## Credits

`koolo` and `d2go` by hectorgimenez and contributors; this fork keeps their licence.
