package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- the trap opener
//
// OWNER (2026-09-26, KillaryClinton the assassin): "combat is pretty easy,
// just launch some sentries and melee the rest ... sentries, traps, and move
// along". A proven trap binding lays sentries at the densest pack in reach,
// a few at a time, then the ordinary melee fight runs. The game keeps at
// most five traps alive and each fires a limited volley, so the budget is
// a count over a lifetime window rather than a cast per tick.

const (
	trapReach   = 12                     // tiles: packs further off are the march's business
	trapCluster = 4                      // tiles: bodies around the aim that count as one pack
	trapMinPack = 2                      // a lone monster gets the claws, not the mana
	trapMax     = 4                      // live traps wanted (the game caps five)
	trapLife    = 10 * time.Second       // a sentry's working life: older casts are spent
	trapGap     = 450 * time.Millisecond // one cast animation between sentries
	trapMinMP   = 25                     // below this the pool is kept for the belt to refill
)

// trapAim picks the pack: the unwalled enemy within trapReach with the most
// unwalled neighbours inside trapCluster (ties: nearer). ok=false when no
// pack reaches trapMinPack.
func trapAim(me data.Position, enemies []percept.EnemyRef) (data.Position, int, bool) {
	var aim data.Position
	best, bestD := 0, 1<<30
	for _, e := range enemies {
		if e.Walled {
			continue
		}
		d := chebyshev(me, e.Pos)
		if d > trapReach {
			continue
		}
		n := 0
		for _, o := range enemies {
			if !o.Walled && chebyshev(e.Pos, o.Pos) <= trapCluster {
				n++
			}
		}
		if n > best || (n == best && d < bestD) {
			aim, best, bestD = e.Pos, n, d
		}
	}
	return aim, best, best >= trapMinPack
}

// trapsLive prunes spent casts and reports how many are still working.
func (f *Fight) trapsLive(now time.Time) int {
	kept := f.trapAt[:0]
	for _, t := range f.trapAt {
		if now.Sub(t) < trapLife {
			kept = append(kept, t)
		}
	}
	f.trapAt = kept
	return len(kept)
}

// layTrap casts one sentry at the pack when the budget allows; true = a cast
// went out this tick (the caller yields the tick).
func (f *Fight) layTrap(ctx *Ctx, s *percept.Snapshot) bool {
	if ctx.Cap == nil || ctx.Cap.Trap == nil || s.Me.InTown || s.Me.MPPct < trapMinMP {
		return false
	}
	now := time.Now()
	if f.trapsLive(now) >= trapMax {
		return false
	}
	if n := len(f.trapAt); n > 0 && now.Sub(f.trapAt[n-1]) < trapGap {
		return false
	}
	aim, pack, ok := trapAim(s.Me.Pos, s.Enemies)
	if !ok {
		return false
	}
	if !groundAt(ctx, aim, ctx.Cap.Trap.Key, false) {
		return false
	}
	f.trapAt = append(f.trapAt, now)
	f.lastKey, f.lastStrikeAt = ctx.Cap.Trap.Key, now
	ctx.Led.Append(verbs.Outcome{Verb: "trap", Holder: f.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("sentry at (%d,%d): pack of %d, %d live, mp %d%%", aim.X, aim.Y, pack, len(f.trapAt), s.Me.MPPct)})
	return true
}
