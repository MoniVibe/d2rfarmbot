package activity

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Haul (ClassTravel)

// Haul is the loot room trip: a tier S drop lies in reach, the bag is full,
// nothing carried may be dropped for it — but the Fence would free enough
// room. Out by Withdraw's proven portal road (a live door, else the TP cast;
// three dud casts = an empty tome), the Fence sells while the trip is on
// (its Demand and ServicesPending read theLoot.hauling), then back through
// the same portal — landing beside the item, where Loot's plan now reads
// TAKE. The pending plan (item + position) sits in memory at area scope.
type Haul struct {
	w Withdraw
}

// haulCoolUntil: an abandoned trip cools the whole idea for a while (an empty
// tome, a dead door) — the brain's HaulOK reads it.
var haulCoolUntil time.Time

func NewHaul() *Haul { return &Haul{} }

func (h *Haul) Name() string { return "haul" }

func (h *Haul) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.HPPct < 50 {
		return nil
	}
	if s.Me.InTown {
		// The return leg: once the town errands are settled (the Fence has
		// sold), ride the standing portal back. Travel outranks Service by
		// class, so the errands must be done first — ServicesPending.
		if !theLoot.hauling() || ServicesPending(s) || !liveDoor(s) {
			return nil
		}
		return &arbiter.Demand{Who: h.Name(), Class: arbiter.ClassTravel,
			Urgency: 0.8, // over Advance's town exit: the prize waits at the portal
			Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
	}
	if !theLoot.haulReady(s) || time.Now().Before(haulCoolUntil) {
		return nil
	}
	if !townPortalBound && !liveDoor(s) {
		return nil
	}
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 12 {
			return nil // the portal cast is a calm-field ritual (Withdraw's rule)
		}
	}
	return &arbiter.Demand{Who: h.Name(), Class: arbiter.ClassTravel,
		Urgency: 0.6, // over Withdraw (0.5) and the march: tier S always wins
		Commit:  arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (h *Haul) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if s.Me.InTown {
		// The loot brain marks the town leg off her feet (observe). Not on a
		// trip (or the Fence still has work): the errands take it from here.
		if !theLoot.hauling() || ServicesPending(s) {
			return Done
		}
		// The return leg: Return's proven portal ride.
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
		if bd == 1<<30 {
			theLoot.abandonRoom("the portal back closed")
			return Abandoned
		}
		if bd > 20 {
			moveTo(ctx, best.Pos, moveto.Opts{Holder: h.Name(), Purpose: moveto.Travel, MaxHold: 1200 * time.Millisecond})
		} else {
			verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, h.Name())
		}
		return Running
	}
	if theLoot.hauling() || !theLoot.haulReady(s) {
		return Done // through the portal (the brain closes the trip), or nothing to haul
	}
	v := h.w.ride(ctx, h.Name())
	if v == Abandoned {
		haulCoolUntil = time.Now().Add(2 * time.Minute)
		theLoot.abandonRoom("no portal road (no binding, or an empty tome)")
	}
	return v
}
