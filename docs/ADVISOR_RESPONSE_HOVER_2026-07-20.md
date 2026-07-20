# Advisor Response — Hover/Click Reliability + The Long Idles (2026-07-20)

Received 2026-07-20 in answer to ADVISOR_ASK_HOVER_2026-07-20.md. Verbatim below.

## Adoption ledger (ours)

**Verified against code:**
- Torn cursor writes — ALREADY FIXED before the ask: `OverridePhysicalCursorPos`
  packs X/Y into one imm64, single 8-byte write; the old two-store version is
  documented as having flickered hover. No action.
- Unbounded SendMessage — GUILTY. Three synchronous sends per AimPhysical; we
  even documented the hazard when deleting MouseMoveClientSync and kept doing
  it in the pump. → sendTimed (SendMessageTimeoutW, SMTO_ABORTIFHUNG, 150ms,
  slow-send logging). WARNING 11.
- lParam conventions — GUILTY both ways: AimPhysical fed CLIENT coords to
  WM_NCHITTEST (wants screen); MovePointer fed SCREEN coords to WM_MOUSEMOVE
  (wants client); WM_SETCURSOR's magic said "triggered by WM_LBUTTONDOWN".
  → corrected spaces, HTCLIENT|WM_MOUSEMOVE. A/B risk accepted: the wrong-space
  trio measurably worked; if hover regresses on the next run, revert this piece.

**Shipped this patch:**
1. sendTimed everywhere in mouse.go, slow sends (>60ms) logged.
2. Conveyor-belt rule in HoverStrike: hold each offset for a second fresh
   sample before declaring it wrong; probe list trimmed 26 → 10.
3. Mutual-veto detector (P-2.10): an in-reach Fight lock with no issued input
   for 1.2s is quarantined 2s and retargeted; no target left → Demand dies →
   the march resumes. Liveness fed by assess(ResDone) + strike().

**Queued (next sessions):**
- HOLDER / PHASE / BLOCKED_REASON / DEADLINE visible line (nav.png + ledger).
- StepKind protocol (Acted/Waiting/Blocked/Done/Failed) with absolute deadlines.
- Cursor-epoch tagging of aim→confirm→click transactions.
- Experiment matrix A–D on the pump composition (A/B measurements, not vibes).
- Instrument per-message send durations + first-fresh-snapshot latency.
- Two-fresh-sample rule for the other sweep sites (imbibe, pickup, waypoint).

---

## Verbatim response

Yes. The long idle is the important clue.
The hover miss is probably the trigger, but the visible idling is a liveness bug above it. An interaction enters a waiting phase, retains control, and neither succeeds nor fails quickly enough.
Because this happens around both Akara and monsters, I would suspect the shared Contact/Reach/target-acquisition layer, not separate NPC and combat bugs. Your hover system already reports 1 to 10 retries, with up to roughly 96 misses in a busy run. Your executive runs at roughly 200 ms per tick, so if a sweep advances by one probe per executive tick, ten probes alone can consume two seconds before settle times, backoff, or verification.

What is probably happening near Akara: Service wins arbitration → Approach Akara → Distance says "close enough", so movement stops → AcquireHover cannot confirm Akara → Service reports "still working" → Service retains the grant → No movement, no click, no failure. The bot is not technically idle. It is trapped inside an interaction phase that looks idle from outside.
A second version: Hover confirms → Click is issued → Action waits for dialogue/shop postcondition → Panel never opens → Timeout or deadline is accidentally reset every tick. That last bug is extremely common. The action is reconstructed each executive cycle, so startedAt, deadline, attempt, or firstConfirmation gets reset.

Monsters create another classic deadlock: Enemy proximity prevents Travel; Fight wins or remains eligible; attack executor rejects current target (no hover / no LoS / unreachable / wrong skill state / target moved); Fight retains ownership. Travel says, "I cannot move because combat is active." Combat says, "I cannot attack this target yet." Nobody acts. It is a tiny bureaucratic collapse.
Another likely version is target churn: select A, probe hover, A moves or snapshot changes, reset acquisition, select A again…

Add a "no silent idle" invariant. Every activity step returns exactly one of: StepActed, StepWaiting, StepBlocked, StepDone, StepFailed. Waiting must always identify a condition and an absolute deadline. Acted means an input was actually issued. Failed probes do not reset the overall deadline. Reconstructing the activity does not reset its phase age. A holder may not return an unnamed no-op.
Display one compact line: HOLDER Service/Akara | PHASE AcquireHover | AGE 684ms | WANT NPC#17 | GOT none | PROBE 4/6 | WAIT fresh_frame | INPUT none | DEADLINE 216ms | LAST_PROGRESS 684ms.
Also record: worldHeartbeatAge, holderAge, phaseAge, targetAge, inputQueueDepth, inFlightAction, nextAllowedAttempt, lastMeaningfulProgress, lastActionResult, hoverWantedID, hoverObservedID, hoverSnapshotSequence, aimSequence.
Track stall milliseconds by reason, not only hover-miss counts. Twenty fast misses may be harmless; one four-second wait is poisonous.

Move hovering into a bounded micro-transaction. The high-level executive chooses the target; it should not personally meter every cursor probe at 200 ms intervals. Once an interaction is granted, give a fast interaction controller temporary ownership: APPROACH → AIM → WAIT FOR FRESH GAME SAMPLE → CONFIRM → CLICK → VERIFY → RECOVER OR COMPLETE. Keep the arbiter out of it except for hard survival preemption.
A robust handshake: (1) record current snapshot sequence; (2) write cursor position; (3) deliver the movement notification; (4) keep the cursor at that exact offset; (5) wait until at least one newer game sample exists; (6) read HoverData; (7) wait for another distinct game sample; (8) require the same target ID again; (9) click immediately, before any other aim or camera-changing action; (10) verify the postcondition.
Do not change sweep offset immediately after one wrong read. If hover is one frame behind, changing the offset every read creates a conveyor belt where each observation describes the previous cursor position. Hold one offset for two genuinely fresh samples before declaring it spatially wrong.
Tag the whole transaction with a CursorEpoch; any new aim, camera movement, player movement, UI transition, or target displacement invalidates the confirmation.

Your Windows messages are not a game-frame barrier. PostMessage returns without waiting. SendMessage waits until the window procedure processes the message — but that does not prove D2R's simulation consumed it and updated HoverData; that may occur on a subsequent game frame. There is also a direct possible explanation for long idles: your hover pump performs synchronous SendMessage calls. A cross-thread SendMessage can block the caller until the receiver processes it. Replace unbounded calls with SendMessageTimeout and record their durations. A few 500 ms or multi-second SendMessage calls would explain the apparently random standing immediately.

Message correctness checks: WM_MOUSEMOVE uses client-area coordinates in lParam. WM_NCHITTEST uses screen coordinates in lParam. WM_SETCURSOR does not take cursor coordinates in lParam; its low word is the hit-test result and its high word identifies the triggering mouse message. Verify those independently. WM_NCHITTEST concerns which part of the window contains a screen coordinate; WM_SETCURSOR concerns the Windows cursor. Neither is a documented command to recompute D2R's world-unit hover — retain them only if A/B measurements show they improve results. WM_MOUSEHOVER / TrackMouseEvent are unlikely to help.
Experiment matrix: A: current trio; B: SendMessageTimeout(MOUSEMOVE) only; C: NCHITTEST + SETCURSOR + SendMessageTimeout(MOUSEMOVE); D: PostMessage(MOUSEMOVE) then wait for two fresh game samples. Measure confirmation latency and miss rate.

Check two subtle memory races. (1) Torn cursor-position updates: if X and Y are written separately the game may observe new X + old Y. Publish atomically — packed aligned 64-bit (x,y) or a seqlock. (2) Torn HoverData reads: read IsHovered, unit type, UnitID as one coherent structure where possible. Diagnostic: hold the virtual cursor still on Akara 500 ms and sample raw hover every snapshot — if it becomes correct after a frame or two and stays, it is frame latency; if it toggles while stationary, suspect read coherence or competing cursor sources.

Recovery must happen before the user notices. NPCs/static: fresh-frame hover acquisition 250–350 ms; max 3–5 offsets at one position; click postcondition 500–800 ms; total transaction 1.2–1.8 s. After two fresh-frame misses at one approach position: stop sweeping, move one or two tiles to a different angle, reproject, new cursor epoch; after two failed reapproaches, release the holder briefly.
Monsters: no attack issued within 300–500 ms → reposition, select another target, or quarantine this target (avoid until now + 750 ms; repeated failures lengthen it). An unattackable monster must not retain the Fight grant while also preventing Travel. Treat hover as target acquisition, not a ritual before every attack: once freshly confirmed, grant a brief target-lock lease while the unit stays alive, its projection has not moved far, the camera is unchanged, and attack postconditions keep occurring.

The first patch I would ship: (1) the visible HOLDER/PHASE/BLOCKED_REASON/DEADLINE line; (2) time every SendMessage and replace unbounded calls with SendMessageTimeout; (3) absolute deadlines that survive executive ticks; (4) hold each cursor offset for two distinct game snapshots before sweeping; (5) confirmation and click inside one cursor epoch; (6) the combat mutual-veto detector: Fight prevents Travel AND Fight has issued no attack for 500 ms → forced retarget or reposition.

My strongest suspicion is a combination of hover acquisition being paced by the slow executive, deadlines being refreshed by retries, and Fight or Service retaining ownership while blocked. The cursor race explains the initial miss; those three behaviors explain why a small miss sometimes blossoms into several seconds of statue mode.
