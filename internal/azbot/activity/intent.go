// intent.go — THE ROUTE INTENT (the owner: "if we can get the bot to be more
// deliberate about where it wants to go and how it gets there, it will really
// fly"). Advance's destination selection used to be recomputed from scratch
// every tick, so a single flickered area read — or a shallower campaign
// recomputation — could whip the target back at the town gate and hand the
// wheel to the legacy Travel/Return road walkers (the Lut Gholein 40 <-> Rocky
// Waste 41 bounce). A committed intent fixes WHERE it wants to go: once
// adopted, the target area is HELD until it is reached, its commit window
// lapses, or the ladder proves it unreachable — and it NEVER yields to a
// shallower campaign goal while committed.
package activity

import (
	"fmt"
	"os"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// deliberate gates the whole deliberate-routing subsystem — route intent +
// commitment (intent.go), the seam-crossing hysteresis (Travel/Return), the
// escalation ladder, and journey-routed clearing/cross. OFF by default: the
// OFF path is the legacy per-tick behavior, structurally byte-identical to
// before this lane. The owner arms it for a live run with AZBOT_DELIBERATE=1.
var deliberate = os.Getenv("AZBOT_DELIBERATE") == "1"

// Deliberate reports whether the deliberate-routing subsystem is armed.
func Deliberate() bool { return deliberate }

// SetDeliberate overrides the flag — the executive logs it at boot; tests flip
// it around a single case.
func SetDeliberate(on bool) { deliberate = on }

// commitWindow is the minimum tenure of an adopted intent (the brief: >= 8s).
// Twelve seconds is one honest gate approach on this mod; a shallower
// recomputation inside the window cannot switch the target.
const commitWindow = 12 * time.Second

// seamHysteresis bars the legacy town-side road walkers from re-crossing a seam
// immediately after a crossing while Advance holds a committed field intent —
// long enough to break the bounce, short enough that a truly stalled march
// still hands back off in seconds.
const seamHysteresis = 6 * time.Second

// RouteIntent is Advance's COMMITTED destination: the campaign area it marches
// toward (Area) and the door it currently aims at (Goal). Commitment is the
// anti-flip law — see the file header.
type RouteIntent struct {
	Goal        data.Position // the current border target (door) — a hint, refreshed as she walks
	Area        area.ID       // the committed destination area (a campaign leg)
	Reason      string        // why this target was adopted (ledgered on change)
	Since       time.Time
	CommitUntil time.Time
}

// Active reports whether an intent is currently held.
func (ri RouteIntent) Active() bool { return ri.Area != 0 }

// lastSeamCrossAt — the shared crossing timestamp (item 2). Stamped by the
// cartographer on every CONFIRMED area transition; read by Travel/Return to
// hold the seam hysteresis. Reset per world (NewWorld).
var lastSeamCrossAt time.Time

// advanceCommitBeyondTownUntil — Advance stamps this each tick it holds a
// committed intent whose area is beyond the town (like marchLawfulUntil). The
// legacy road walkers read it to know the smart marcher owns the exit right
// now; it goes stale in 2s if Advance stops bidding.
var advanceCommitBeyondTownUntil time.Time

// NoteSeamCross records a confirmed area crossing — the shared timestamp the
// seam hysteresis leans on. Called by the executive's cartographer.
func NoteSeamCross() { lastSeamCrossAt = time.Now() }

// legIndexOf is the STRICT itinerary index of an area (-1 when off-itinerary) —
// unlike place(), which returns the last adopted leg for side caves.
func (a *Advance) legIndexOf(ar area.ID) int {
	for i, lg := range a.Itinerary {
		if lg.Area == ar {
			return i
		}
	}
	return -1
}

// intentIdx is the itinerary index of the committed area (-1 when none/off).
func (a *Advance) intentIdx() int {
	if !a.intent.Active() {
		return -1
	}
	return a.legIndexOf(a.intent.Area)
}

// intentLeg is THE ONE destination gate (the brief: "Destination selection in
// Advance.Step goes through one function"). Given the raw per-tick candidate
// leg index (deepest level-lawful campaign leg from campIdx), it returns the
// leg index Advance actually commits to this tick.
//
// With the flag OFF it returns rawIdx unchanged — the legacy path. With it ON:
//   - a live commitment that is DEEPER-OR-EQUAL to a shallower recompute is
//     HELD (the anti-flip); progress toward a deeper leg re-commits forward;
//   - reaching (standing in or past) the committed area retires the intent;
//   - a lapsed commit window lets the target be recomputed freely.
func (a *Advance) intentLeg(ctx *Ctx, s *percept.Snapshot, rawIdx int) int {
	if !deliberate || len(a.Itinerary) == 0 {
		return rawIdx
	}
	if rawIdx < 0 {
		rawIdx = 0
	} else if rawIdx > len(a.Itinerary)-1 {
		rawIdx = len(a.Itinerary) - 1
	}
	now := time.Now()
	rawArea := a.Itinerary[rawIdx].Area

	// REACHED: standing in the committed area, or deeper on the itinerary,
	// retires it — the next tick adopts the next leg fresh.
	if a.intent.Active() {
		if li := a.legIndexOf(s.Me.Area); s.Me.Area == a.intent.Area || (li >= 0 && li >= a.intentIdx()) {
			a.clearIntent(ctx, "reached")
		}
	}

	// HOLD a live, deeper-or-equal commitment against a shallower recompute.
	if a.intent.Active() && now.Before(a.intent.CommitUntil) {
		ci := a.intentIdx()
		if ci >= 0 {
			a.stampBeyondTown(ci)
			if rawIdx <= ci {
				return ci // keep the deeper committed goal — the flip dies here
			}
			// rawIdx is DEEPER: progress is always welcome — re-commit forward.
		}
	}

	// (Re)commit to the raw candidate — a fresh adoption, a forward step, or a
	// recomputation after the window lapsed.
	if !a.intent.Active() || a.intent.Area != rawArea {
		a.commitIntent(ctx, rawArea, fmt.Sprintf("adopt leg %d (%d) from %d", rawIdx, int(rawArea), int(s.Me.Area)))
	}
	a.stampBeyondTown(rawIdx)
	return rawIdx
}

// stampBeyondTown refreshes the beyond-town commitment window when the leg the
// march owns is past the town — the signal the legacy road walkers read.
func (a *Advance) stampBeyondTown(legIdx int) {
	if legIdx < 0 || legIdx > len(a.Itinerary)-1 {
		return
	}
	if !a.Itinerary[legIdx].Area.IsTown() {
		advanceCommitBeyondTownUntil = time.Now().Add(2 * time.Second)
	}
}

// commitIntent adopts a new target area and opens its commit window.
func (a *Advance) commitIntent(ctx *Ctx, ar area.ID, reason string) {
	now := time.Now()
	a.intent = RouteIntent{Area: ar, Reason: reason, Since: now, CommitUntil: now.Add(commitWindow)}
	if ctx != nil && ctx.Led != nil {
		ctx.Led.Append(verbs.Outcome{Verb: "intent", Holder: a.Name(), Result: verbs.ResDone,
			Evidence: "commit " + reason})
	}
}

// clearIntent retires the current intent, ledgering why.
func (a *Advance) clearIntent(ctx *Ctx, reason string) {
	if !a.intent.Active() {
		return
	}
	if ctx != nil && ctx.Led != nil {
		ctx.Led.Append(verbs.Outcome{Verb: "intent", Holder: a.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("release %d (%s)", int(a.intent.Area), reason)})
	}
	a.intent = RouteIntent{}
}

// committedSeamHold reports whether the legacy town-side road walkers should
// stand down this tick: the deliberate marcher holds a beyond-town commitment
// AND a crossing fired inside the hysteresis window. Read by Travel/Return.
func committedSeamHold() bool {
	if !deliberate {
		return false
	}
	now := time.Now()
	return now.Before(advanceCommitBeyondTownUntil) && now.Sub(lastSeamCrossAt) < seamHysteresis
}
