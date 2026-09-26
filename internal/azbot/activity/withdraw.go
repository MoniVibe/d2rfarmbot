// Withdraw: the calm retreat nobody owned. HP never regenerates in this game — a
// wounded amazon with an empty belt does not get better by standing in the moor, and
// the old demand table proved it: below Fight's floor, above Flee's pressure trigger,
// with Explore gated at 50%, NOTHING bid and she idled at 40% forever (the owner:
// "it stands idle"). Breakout owns the surrounded case; Withdraw owns the quiet one:
// no potions, real damage taken, no teeth on her right now → cast the town portal,
// step through, let Restock/Fence/Return do their proven work.
package activity

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// townPortalBound: calibration (or -tpkey) left a Town Portal binding. Set by
// the executive after every calibration; true until told otherwise so a bare
// roster behaves as before. Without it the portal road only exists through a
// door already standing on the field — nothing casts, no key is pressed.
var townPortalBound = true

// SetTownPortal records whether a Town Portal binding exists (see
// combat.NoTownPortalLine).
func SetTownPortal(bound bool) { townPortalBound = bound }

// liveDoor reports whether a usable (not dead) portal stands in the snapshot.
func liveDoor(s *percept.Snapshot) bool {
	for _, pt := range s.Portals {
		if !verbs.IsDeadDoor(pt.ID) {
			return true
		}
	}
	return false
}

type Withdraw struct {
	castAt    time.Time
	castTries int // casts that produced no portal — the empty-tome (poverty) detector
	coolAt    time.Time
}

func NewWithdraw() *Withdraw { return &Withdraw{} }

func (w *Withdraw) Name() string { return "withdraw" }

func (w *Withdraw) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 || s.Me.HealPots > 0 {
		return nil
	}
	if s.Me.HPPct >= 55 {
		return nil // healthy enough to keep working the field dry
	}
	if time.Now().Before(w.coolAt) {
		return nil // tome proven empty moments ago — fighting on IS the plan
	}
	if !townPortalBound && !liveDoor(s) {
		return nil // no binding and no door: there is no portal road to take
	}
	// Under pressure this is Flee/Breakout's moment, not a portal-cast window.
	for _, e := range s.Enemies {
		if chebyshev(s.Me.Pos, e.Pos) <= 12 {
			return nil
		}
	}
	return &arbiter.Demand{Who: w.Name(), Class: arbiter.ClassTravel,
		Urgency: 0.5, // above Advance (0.2) and Return (0.35): leaving beats marching
		Commit:  arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (w *Withdraw) Step(ctx *Ctx) Verdict { return w.ride(ctx, w.Name()) }

// ride is THE town road by portal — shared by Withdraw and Recall so the one
// proven ritual (use a live door, else cast; three dud casts = an empty tome)
// is written once. who names the ledger holder.
func (w *Withdraw) ride(ctx *Ctx, who string) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if s.Me.InTown {
		w.castTries = 0
		return Done // home — the service activities take it from here
	}
	// A portal down is the decision already made: use it (approach until clickable).
	// P-2.4: a dead door is ABSENT — fall through and cast a fresh one.
	var best percept.PortalRef
	bd := 1 << 30
	for _, pt := range s.Portals {
		if verbs.IsDeadDoor(pt.ID) {
			continue
		}
		if d := chebyshev(s.Me.Pos, pt.Pos); d < bd {
			best, bd = pt, d
		}
	}
	if bd < 1<<30 {
		if bd > 20 {
			moveTo(ctx, best.Pos, moveto.Opts{Holder: who, Purpose: moveto.Travel, MaxHold: 1200 * time.Millisecond})
		} else {

			verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, who)
		}
		return Running
	}
	// No portal: cast one. Three casts with nothing materializing = the tome is empty
	// (0 gold, 0 scrolls) — stop pretending, cool off, and let the field activities
	// keep her earning instead of pinning her to a dead ritual.
	if ctx.Cap == nil || ctx.Cap.TownTP == nil {
		w.coolAt = time.Now().Add(60 * time.Second)
		return Abandoned
	}
	if time.Since(w.castAt) > 2500*time.Millisecond {
		if !w.castAt.IsZero() {
			w.castTries++
		}
		if w.castTries >= 3 {
			w.castTries = 0
			w.castAt = time.Time{}
			w.coolAt = time.Now().Add(60 * time.Second)
			return Abandoned
		}
		verbs.CastSelf{Key: ctx.Cap.TownTP.Key}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, who)
		w.castAt = time.Now()
	}
	return Running
}

// Recall is the session's wind-down demand (exec.Session WindDown, relay R4:
// the run's timer once exited mid-fight and left him undriven). While the
// executive says the run is ending and the field is still hot, Recall rides
// Withdraw's town road at class Recover: over Fight (the road home beats one
// more kill), under Survive (Breakout, Stand, Flee and Dodge keep him alive,
// and the sentinel keeps drinking). Urgency sits over Reclaim and under
// Unstick, whose prescriptions may themselves be a portal home. No tome or an
// empty one: Recall abandons and cools like Withdraw — Spent tells the
// session, whose ladder then pauses the game (or its quiet field or budget
// ends the run).
type Recall struct {
	want  bool
	door  bool // the last Demand saw a live portal on the field
	pad   bool // the last Demand saw a lit waypoint road home (no TP needed)
	empty bool // the last Demand saw the TP tome empty
	byPad bool // this bid rides the waypoint (k3, 2026-09-26: an empty tome
	// abandoned the recall and she stood disengaged in Black Marsh among 152)
	w Withdraw
	u Unload // the pad road (rideHome)
}

func NewRecall() *Recall { return &Recall{} }

func (r *Recall) Name() string { return "recall" }

// Want is set by the executive every tick: true while the session asks for
// the town road (exec.WindTown).
func (r *Recall) Want(on bool) { r.want = on }

// Spent: the town road was abandoned — no tome bound, or three casts with no
// portal (ride's empty-tome detector) — and Recall cools. The session reads it
// as exec.SessionIn.RecallSpent: rung 2 is over, pause the game. With NO
// Town Portal binding (SetTownPortal(false)) and no door on the field Recall
// is spent from the first tick — the ladder goes straight to the pause rung.
func (r *Recall) Spent() bool {
	if r.pad {
		return false // the waypoint road home is still open
	}
	return time.Now().Before(r.w.coolAt) || (!townPortalBound && !r.door) || (r.empty && !r.door)
}

func (r *Recall) Demand(s *percept.Snapshot) *arbiter.Demand {
	if s.Valid {
		r.door = liveDoor(s)
		r.pad = !s.Me.InTown && wpHomeReady(s)
		r.empty = s.Me.TPScrolls == 0
	}
	if !r.want || !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 {
		return nil
	}
	tpOut := !townPortalBound || s.Me.TPScrolls == 0 || time.Now().Before(r.w.coolAt)
	r.byPad = !r.door && tpOut && r.pad
	if !r.door && tpOut && !r.pad {
		return nil // nothing to cast with, nothing to ride: Spent says so
	}
	// STAND FOR HIMSELF FIRST (owner, R34: "bot tried entering the portal but
	// mobs prevented it"): monsters on him block the portal's hover and click.
	// Fight clears the pocket; the wind-down budget still ends in the pause.
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 8 {
			return nil
		}
	}
	return &arbiter.Demand{Who: r.Name(), Class: arbiter.ClassRecover,
		Urgency: 0.91, // over Reclaim (0.9), under Unstick (0.92) and Respawn (1.0)
		Commit:  arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (r *Recall) Step(ctx *Ctx) Verdict {
	if r.byPad {
		return r.u.rideHome(ctx)
	}
	return r.w.ride(ctx, r.Name())
}
