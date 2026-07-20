# Advisor Ask — The Hover/Click Reliability Problem

Prepared 2026-07-20. Follow-up to your architecture review (which we adopted:
arc-length roads, signed clearing, single-flight preconditions, the idle
breaker, cursed-ground learning). This is a narrower, lower-level ask about
one persistent failure that survives all of the above.

## The symptom

The bot frequently **fails to hover-confirm or click a target on the first
try** — town NPCs, ground items, waypoint pads, shrines, chests, portals,
cave mouths. It usually succeeds after 1–10 retries via a sweep; sometimes it
never does and falls back (blind click, arc-and-reapproach, or abandon).
Per-run hover-miss counts (`hoverConfirmed=false` / `no hover confirmation`)
range from single digits to ~96 in a busy combat run. It is not fatal —
retries and fallbacks absorb it — but it costs seconds, causes visible
"reaching for it" behavior, and is the root of several bugs we've chased all
night (evaporating panels, "missed Akara," dead pickups).

## The stack (ground truth, not summary)

This is an **external-memory bot** on a protected game (D2R 3.2, Arxan;
no code injection into game logic, no DirectInput hook — the Interception
driver is available but not installed). All I/O:

**Reads** — game state via a d2go memory-reader fork. Crucially, the game's
`HoverData` (which unit the cursor is over) is a **memory read**, populated by
the game's own hit-testing, which runs off the game's **polled cursor
position**, not off the messages we send.

**Cursor positioning** — we do NOT use SetCursorPos. We patch
`user32!GetPhysicalCursorPos` (and, when it doesn't alias, `GetCursorPos`)
with a resident stub that returns a **fixed physical coordinate we control**.
So when the game polls "where is the cursor," it reads our injected value.
This is `AimPhysical(x,y)`: compute physical px from client px (measured
transform: `physical = windowOrigin*dpiScale + clientOffset`), write it into
the stub.

**Hover refresh** — writing the stub alone did NOT refresh hover; the game
only re-hit-tests on **mouse events**. So `AimPhysical` also posts, to the
game HWND:
```
SendMessage(WM_NCHITTEST)      // synchronous
SendMessage(WM_SETCURSOR)      // synchronous
PostMessage(WM_MOUSEMOVE)      // async
```
Adding this trio ("the hover pump") was the single biggest reliability jump
of the project. We then added a **double-tap**: the trio, sleep 25ms, the
NCHITTEST+MOUSEMOVE again — which helped further but did not eliminate misses.

**Clicks** — `SendMessage(WM_LBUTTONDOWN, 1, lParam)`, sleep ~random 20–40ms,
`SendMessage(WM_LBUTTONUP)`. The **world ignores lParam** and hit-tests off
the polled cursor; **UI panels read lParam** (client coords). A click with a
stale/unrefreshed cursor lands on the wrong world target ("dead click," "walk
instead of pickup").

**Confirm loop** — before an important click we sweep offsets around the
projected label, `AimPhysical` each, read `HoverData`, and require
`IsHovered && UnitID==target`, **double-confirmed** (re-aim, settle, re-read,
because a single read can reflect the previous probe's cursor — frame
latency). We click only a confirmed offset. The sweep is the retry mechanism
that masks the underlying flakiness.

## What we believe is happening (please correct us)

The hover state is **sampled by the game on its own frame cadence**, and our
"set cursor + post mouse events" sequence **races that sampling**. One
message trio lands cleanly most of the time; a few percent of the time it
falls between the game's hit-test frames and the read comes back stale, so
`HoverData` doesn't reflect the aim we just issued. The double-tap improved
the odds but the race is still a race.

We also suspect the **async `PostMessage(WM_MOUSEMOVE)`** vs **synchronous
`SendMessage(WM_NCHITTEST)`** ordering may matter: NCHITTEST is processed
immediately, MOUSEMOVE is queued, so the game may hit-test before the move it
was told about is applied.

## The constraints (why the obvious fixes are hard)

- **No SetCursorPos / real hardware cursor.** The bot must run headless-ish
  (the game window is often not foreground; offline D2R pauses when
  unfocused, so we can't rely on foreground). Real input needs foreground.
- **No injection into game code** (Arxan). We can patch user32 stubs and
  read/post window messages; we cannot hook the game's hit-test directly.
- **The Interception driver** (kernel input) is available but uninstalled;
  it delivers real hardware-level input but still to the foreground window,
  and pausing-when-unfocused remains.
- Everything must stay **background-capable** — posted messages reach the
  window without focus; that property is load-bearing.

## The questions

1. **Is there a synchronous way to force the game to re-hit-test at our
   cursor and have `HoverData` reflect it before we read?** e.g. is there a
   message (or message *order*) that makes the hover update synchronously —
   should NCHITTEST/SETCURSOR/MOUSEMOVE be all `SendMessage`, or is there a
   `WM_MOUSEHOVER`/`WM_MOUSEACTIVATE`/`TrackMouseEvent` angle we're missing?
2. **Is reading `HoverData` from memory the wrong oracle?** Would reading the
   game's own cursor-hit-test result (if such an address exists) after a
   settle be more truthful than re-deriving hover from posted moves? Put
   differently: is there a memory address that says "the unit under the
   cursor as the game currently sees it" that we should poll instead of
   racing message posts?
3. **Cursor-position write timing** — we write the physical-cursor stub value
   then post a move. Is there a frame-sync signal (present/vblank/a game
   heartbeat address) we could gate on so we aim *between* the game's polls
   rather than during them?
4. **Is the double-confirm sweep the right paradigm at all**, or is it
   papering over a fixable root cause? If the race is fundamental to posted
   input, is the correct answer just "sample N times and vote," and if so
   what N / what settle time do you'd expect for a 25–60fps game?
5. **Would the Interception driver actually help here** given hover is a
   *read* problem (does the game hit-test differently for driver-delivered
   moves vs posted WM_MOUSEMOVE?), or is it orthogonal because the game
   polls cursor position regardless of how it got there?
6. Any **general pattern for reliable synthetic hover+click on a
   poll-based hit-test game** that we should know — from bot/automation
   practice — that isn't "install a real input driver and take focus"?

## What we've already ruled out

- Coordinate transform is correct (proven: waypoint rows, cave mouths,
  combat all hit when the hover *does* confirm; the misses are intermittent,
  not systematic offset).
- Modifier latching (we amnesty shift before clicks now).
- It is not focus-dependent in the sense of input delivery — posted messages
  arrive unfocused; the misses happen focused too.
- The hover pump (posting the event trio) and the double-tap both measurably
  helped, so the direction is right; we're asking how to close the last few
  percent.

Anything you can tell us about how a poll-based game samples synthetic cursor
state, and how to make a memory-read hover oracle agree with a
posted-message cursor deterministically, would be gold.
