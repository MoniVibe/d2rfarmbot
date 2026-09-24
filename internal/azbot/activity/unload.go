package activity

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// ---------------------------------------------------------------- Unload (ClassRecover)

// OWNER (2026-09-24, R27): "it should not go kill things when it's full like
// that, we need to make sure it can clear its items to vendors or stash". A bag
// that cannot take a 2x4 drop, with something the bag plan sells or stashes, is a
// trip home — ahead of the march and the fight (Recover outranks both; Survive
// still outranks it). The portal ride is Withdraw's proven one, calm-gated: with
// an enemy within 12 it does not cast, and Fight clears the pocket first.

// unloadFree: fewer free cells than this (a 2x4 piece) = the bag is full.
const unloadFree = 8

type Unload struct{ w Withdraw }

func NewUnload() *Unload { return &Unload{} }

func (u *Unload) Name() string { return "unload" }

// bagFull: the plan has work for the town and the bag cannot take a big drop.
func bagFull(s *percept.Snapshot) bool {
	return s.Valid && s.Me.InvFree < unloadFree && s.Me.JunkCount+s.Me.StashCount > 0
}

func (u *Unload) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 || !bagFull(s) {
		return nil
	}
	if !townPortalBound && !liveDoor(s) {
		return nil // no portal road
	}
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 12 {
			return nil // the portal cast is a calm-field ritual: Fight clears the pocket first
		}
	}
	return &arbiter.Demand{Who: u.Name(), Class: arbiter.ClassRecover, Urgency: 0.5,
		Commit: arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (u *Unload) Step(ctx *Ctx) Verdict { return u.w.ride(ctx, u.Name()) }
