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
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

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

func (w *Withdraw) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if s.Me.InTown {
		w.castTries = 0
		return Done // home — the service activities take it from here
	}
	// A portal down is the decision already made: use it (approach until clickable).
	if len(s.Portals) > 0 {
		best, bd := s.Portals[0], chebyshev(s.Me.Pos, s.Portals[0].Pos)
		for _, pt := range s.Portals[1:] {
			if d := chebyshev(s.Me.Pos, pt.Pos); d < bd {
				best, bd = pt, d
			}
		}
		if bd > 20 {
			verbs.Stride{To: best.Pos, Hold: 1200 * time.Millisecond, MinGain: 1}.
				Do(ctx.M, ctx.GR, ctx.P, ctx.Led, w.Name())
		} else {
			verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, w.Name())
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
		verbs.CastSelf{Key: ctx.Cap.TownTP.Key}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, w.Name())
		w.castAt = time.Now()
	}
	return Running
}
