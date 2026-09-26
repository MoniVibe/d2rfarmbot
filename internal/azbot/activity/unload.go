package activity

import (
	"fmt"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/inventory"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
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

type Unload struct {
	w     Withdraw
	byPad bool      // this bid rides the waypoint home (no TP road)
	wpAt  time.Time // the last pad ride attempt
}

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
	// R77: with the town errands cooled (the smith unreachable), the trip home
	// repeats forever — no repair, back out, home again.
	return s.Valid && (s.Me.BrokenGear > 0 || s.Me.MinDurPct < inventory.FieldRepairPct) && s.Me.Gold >= 10 && !servicesCooled()
}

func (u *Unload) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 || !(bagFull(s) || gearFailing(s)) {
		return nil
	}
	if treasurePending(time.Now()) {
		return nil // Loot has a keeper in reach: pick it up first (bounded)
	}
	if !townPortalBound && !liveDoor(s) && !wpHomeReady(s) {
		return nil // no portal road
	}
	// R50 (Palace Cellar 3, 25 min): the TP tome was EMPTY (Quantity omitted =
	// 0) and Unload re-bid the dead cast ritual every 8 s — three casts, abandon,
	// re-grant — ignoring Withdraw's own cool-off. No charges and no standing
	// portal = no road home by portal; the march goes on with a full bag (the
	// swap still makes room for a keeper) until a waypoint or a town visit.
	u.byPad = false
	if !liveDoor(s) && (s.Me.TPScrolls == 0 || time.Now().Before(u.w.coolAt)) {
		if !wpHomeReady(s) {
			return nil
		}
		u.byPad = true // no portal: the lit pad is the road home
	}
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 12 {
			return nil // the portal cast is a calm-field ritual: Fight clears the pocket first
		}
	}
	return &arbiter.Demand{Who: u.Name(), Class: arbiter.ClassRecover, Urgency: 0.5,
		Commit: arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (u *Unload) Step(ctx *Ctx) Verdict {
	if u.byPad {
		return u.rideHome(ctx)
	}
	return u.w.ride(ctx, u.Name())
}

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

// ---------------------------------------------------------------- the road home by waypoint
//
// Owner (2026-09-25): "can we let it know it can return home from the wp and
// sell stuff or stash? it runs pretty full". With no TP charges (R50/R55 ran
// the tome dry) the lit pad of this area is the road home: walk to it and ride
// to the act's town; the town services clear the bag and Advance's town ride
// brings him back to the deepest lit leg.

var padsLit = struct {
	sync.Mutex
	m map[area.ID]data.Position
}{m: map[area.ID]data.Position{}}

// notePadLit: Advance saw this area's pad lit (live Opened, or the ledger).
func notePadLit(ar area.ID, at data.Position) {
	padsLit.Lock()
	padsLit.m[ar] = at
	padsLit.Unlock()
}

func padLitAt(ar area.ID) (data.Position, bool) {
	padsLit.Lock()
	defer padsLit.Unlock()
	p, ok := padsLit.m[ar]
	return p, ok
}

// actTown: the town of an area's act (d2go's act ranges), 0 when unknown.
func actTown(ar area.ID) area.ID {
	if ar <= 0 {
		return 0
	}
	towns := []area.ID{area.RogueEncampment, area.LutGholein, area.KurastDocks, area.ThePandemoniumFortress, area.Harrogath}
	return towns[ar.Act()-1]
}

// wpHomeReady: no portal road, but a lit pad here and a known town.
func wpHomeReady(s *percept.Snapshot) bool {
	_, ok := padLitAt(s.Me.Area)
	return ok && actTown(s.Me.Area) != 0
}

// rideHome walks to the lit pad and rides to town.
func (u *Unload) rideHome(ctx *Ctx) Verdict {
	s := ctx.Snap
	pad, ok := padLitAt(s.Me.Area)
	town := actTown(s.Me.Area)
	if !ok || town == 0 {
		return Abandoned
	}
	if chebyshev(s.Me.Pos, pad) > 5 {
		moveTo(ctx, pad, marchOpts(ctx, u.Name(), 1200*time.Millisecond))
		return Running
	}
	if time.Since(u.wpAt) < 6*time.Second {
		return Running
	}
	u.wpAt = time.Now()
	ctx.Led.Append(verbs.Outcome{Verb: "waypoint", Holder: u.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("going home by waypoint: area %d -> town %d (no TP charges)", int(s.Me.Area), int(town))})
	verbs.UseWaypoint{Want: []area.ID{town}}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, u.Name())
	// The panel stood: this area's pad is lit (the ride home proves it — the
	// Outer Cloister pad was ridden from and never ledgered).
	if ctx.Mem != nil && time.Since(verbs.LastPanelOpenAt) < 10*time.Second {
		ctx.Mem.PutJSON(LitKey(ctx.GR.GetData().PlayerUnit.Name, s.Me.Area), memory.ScopeForever,
			memory.Provenance{Source: "measured", Evidence: "the waypoint panel stood on the ride home"}, true)
	}
	return Running
}

// ---------------------------------------------------------------- hot landings
//
// R85/R88: every waypoint ride to the Flayer Jungle landed in the same pack, a
// breakout sent him home, and the next ride landed him there again. An area he
// had to break out of is not a waypoint destination for hotFor: he walks in
// from elsewhere instead.

const hotFor = 15 * time.Minute

var hot = struct {
	sync.Mutex
	at map[area.ID]time.Time
}{at: map[area.ID]time.Time{}}

func noteHotLanding(ar area.ID) {
	hot.Lock()
	hot.at[ar] = time.Now()
	hot.Unlock()
}

func hotLanding(ar area.ID) bool {
	hot.Lock()
	defer hot.Unlock()
	t, ok := hot.at[ar]
	return ok && time.Since(t) < hotFor
}
