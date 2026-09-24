package activity

import (
	"sync"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/inventory"
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

// gearFailing: a broken piece (its bonuses stop counting) or one nearly so is a
// trip home for the smith (owner: "inventory should know that half its items are
// broken").
func gearFailing(s *percept.Snapshot) bool {
	return s.Valid && (s.Me.BrokenGear > 0 || s.Me.MinDurPct < inventory.FieldRepairPct) && s.Me.Gold >= 10
}

func (u *Unload) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 || !(bagFull(s) || gearFailing(s)) {
		return nil
	}
	if treasurePending(time.Now()) {
		return nil // Loot has a keeper in reach: pick it up first (bounded)
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

// ---------------------------------------------------------------- treasure in reach
//
// R44 (01:41, Lost City): Loot planned TAKE on an Orb of Infusion three times
// while Fight held the pocket; the moment Fight let go, Unload (a broken piece)
// cast the portal and the orb stayed on the floor. A tier A+ TAKE in reach now
// holds the trip home — bounded, so a drop Loot can never reach does not keep
// broken gear in the field.

const (
	treasureReach   = 25               // tiles: Loot's own approach range is 30
	treasureMinWant = 0.6              // tier A (≈0.65) and S (≈0.9)
	treasureWaitMax = 60 * time.Second // per sighting streak
	treasureGap     = 10 * time.Second // unseen this long: the streak is over
)

var treasure struct {
	mu          sync.Mutex
	first, last time.Time
}

// noteTreasure records one Loot tick's sighting (found) at now.
func noteTreasure(now time.Time, found bool) {
	treasure.mu.Lock()
	defer treasure.mu.Unlock()
	if !found {
		return
	}
	if treasure.last.IsZero() || now.Sub(treasure.last) > treasureGap {
		treasure.first = now
	}
	treasure.last = now
}

// treasurePending: a sighting in the last few seconds, inside the streak's budget.
func treasurePending(now time.Time) bool {
	treasure.mu.Lock()
	defer treasure.mu.Unlock()
	return !treasure.last.IsZero() && now.Sub(treasure.last) < 3*time.Second &&
		now.Sub(treasure.first) < treasureWaitMax
}

// treasureInReach: a ground item Loot would TAKE at tier A+ within reach and not banned.
func (l *Loot) treasureInReach(s *percept.Snapshot, now time.Time) bool {
	if !s.Valid || s.Me.InTown {
		return false
	}
	for _, it := range s.Items {
		if until, banned := l.ban[it.ID]; banned && now.Before(until) {
			continue
		}
		if chebyshev(s.Me.Pos, it.Pos) <= treasureReach && l.wanted(s, it) >= treasureMinWant {
			return true
		}
	}
	return false
}
