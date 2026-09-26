package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/inventory"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- escape reflexes
//
// OWNER (2026-09-26, after she died standing inside Breakout — 12 s pinned by
// 25 in Catacombs 4): "it should cast traps as it runs away when low hp. it
// should also consider picking up hp from the ground as it does". Survive-
// class escapes outrank Traps and Loot, so neither ever ran while she fled.
// Both reflexes now ride the escapes themselves, one bounded action per tick.

const (
	escapeTrapGap   = 700 * time.Millisecond
	escapeTrapMinMP = 10
	escapeTrapNear  = 10 // chasers within this get a sentry
	healGrabReach   = 5  // tiles: a bottle this close is worth the detour
)

var escapeTrapAt time.Time
var healGrabBan = map[data.UnitID]time.Time{}

// escapeTrapPoint: two tiles from her toward the chasers' centroid — the
// sentry stands between her and them, in cast reach.
func escapeTrapPoint(me data.Position, enemies []percept.EnemyRef) (data.Position, bool) {
	sx, sy, n := 0, 0, 0
	for _, e := range enemies {
		if !e.Walled && chebyshev(me, e.Pos) <= escapeTrapNear {
			sx, sy, n = sx+e.Pos.X, sy+e.Pos.Y, n+1
		}
	}
	if n == 0 {
		return data.Position{}, false
	}
	c := data.Position{X: sx / n, Y: sy / n}
	d := chebyshev(me, c)
	if d <= 2 {
		return c, true
	}
	return data.Position{X: me.X + (c.X-me.X)*2/d, Y: me.Y + (c.Y-me.Y)*2/d}, true
}

// escapeTrap lays one sentry toward the chasers (rate-limited). true = cast.
func escapeTrap(ctx *Ctx, s *percept.Snapshot, holder string) bool {
	b, ok := trapBinding()
	if !ok || s.Me.MPPct < escapeTrapMinMP || time.Since(escapeTrapAt) < escapeTrapGap {
		return false
	}
	at, ok := escapeTrapPoint(s.Me.Pos, s.Enemies)
	if !ok || !groundAt(ctx, at, b.Key, false) {
		return false
	}
	escapeTrapAt = time.Now()
	ctx.Led.Append(verbs.Outcome{Verb: "trap", Holder: holder, Result: verbs.ResDone,
		Evidence: fmt.Sprintf("escape sentry at (%d,%d): hp %d%%, mp %d%%", at.X, at.Y, s.Me.HPPct, s.Me.MPPct)})
	return true
}

// healNear: a healing or rejuvenation bottle on the ground within reach, not
// recently tried.
func healNear(s *percept.Snapshot, now time.Time) (percept.ItemRef, bool) {
	for _, it := range s.Items {
		p := inventory.PotionOf(it.Class)
		if p != inventory.PotHP && p != inventory.PotRV {
			continue
		}
		if until, banned := healGrabBan[it.ID]; banned && now.Before(until) {
			continue
		}
		if chebyshev(s.Me.Pos, it.Pos) <= healGrabReach {
			return it, true
		}
	}
	return percept.ItemRef{}, false
}

// grabHeal picks up a bottle within reach while escaping. true = tried.
func grabHeal(ctx *Ctx, s *percept.Snapshot, holder string) bool {
	it, ok := healNear(s, time.Now())
	if !ok {
		return false
	}
	healGrabBan[it.ID] = time.Now().Add(10 * time.Second) // one try per bottle per 10 s
	o := verbs.Pickup{Target: it.ID, TargetPos: it.Pos, TargetQuality: it.Quality, AllowBelow: true, Window: 900 * time.Millisecond}.
		Do(ctx.M, ctx.GR, ctx.P, ctx.Led, holder)
	ctx.Led.Append(verbs.Outcome{Verb: "pickup", Holder: holder, Result: o.Result,
		Evidence: fmt.Sprintf("escape grab: bottle unit %d at (%d,%d), hp %d%%", int(it.ID), it.Pos.X, it.Pos.Y, s.Me.HPPct)})
	return true
}
