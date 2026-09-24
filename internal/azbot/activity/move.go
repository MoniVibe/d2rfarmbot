package activity

import (
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

// moveTo is every activity's locomotion (A3: MoveTo is the only mover). One
// bounded step toward goal on the executive's fused grid; the Status says
// whether she moved, arrived, or the planner refused.
func moveTo(ctx *Ctx, goal data.Position, o moveto.Opts) moveto.Status {
	return moveToOn(ctx, ctx.Grid, goal, o)
}

// moveToOn is moveTo on a grid the caller holds (Advance's leg grid).
func moveToOn(ctx *Ctx, g *game.Grid, goal data.Position, o moveto.Opts) moveto.Status {
	e := moveto.Game(ctx.M, ctx.GR, ctx.P, ctx.Led, g)
	e.LeapReady = func() bool { return travelLeapReady(ctx) }
	e.Leap = func(land data.Position) bool { return travelLeap(ctx, land) }
	return moveto.Default.MoveTo(e, goal, o)
}

// forgetMove drops the holder's trip: its next moveTo plans afresh (the old
// "j = nil" after a verdict the caller acted on).
func forgetMove(who string) { moveto.Default.Forget(who) }

// marchOpts is the travel click gait (THE FISH CURE: click a waypoint of the
// route, let the game's own pathfinder walk it — the only walker that knows
// the mod's invented fences; a dead click falls to a planned stride). P-5.9
// for the brawler (the owner, 04:33): in the field the click is a RIGHT-click
// with the melee skill selected — the walk itself engages what it meets.
func marchOpts(ctx *Ctx, who string, hold time.Duration) moveto.Opts {
	key := byte(0)
	if brawlerMode && ctx.Snap != nil && !ctx.Snap.Me.InTown && ctx.Cap != nil && ctx.Cap.Contact != nil && ctx.Snap.Me.MPPct > 10 {
		key = ctx.Cap.Contact.Key // FIELD ONLY: a Double Swing near Charsi is not a greeting (04:36)
	}
	return moveto.Opts{Holder: who, Purpose: moveto.Travel, Click: true, CombatKey: key, MaxHold: hold}
}

// stalled: the planner's verdict was NoPath or Stalled.
func stalled(st moveto.Status) bool {
	return st.State == moveto.NoPath || st.State == moveto.Stalled
}

// wallTurned: a heading walker's stride found a wall (the blind-heading laws
// of Explore and Advance.search turn 45° on it): a planner refusal, or a
// route-less stride that gained nothing.
func wallTurned(st moveto.Status) bool {
	return stalled(st) || (st.Blocked && st.Mode != "journey")
}

// reach extends from→to to at least n tiles (same bearing): an "away" point
// one tile off would read as arrived and freeze the retreat.
func reach(from, to data.Position, n int) data.Position {
	dx, dy := to.X-from.X, to.Y-from.Y
	m := maxInt(absInt(dx), absInt(dy))
	if m == 0 || m >= n {
		return to
	}
	return data.Position{X: from.X + dx*n/m, Y: from.Y + dy*n/m}
}

// travelLeapReady: the travel leap's gate (P-2.11(4)) — field, pool, and one
// leap per 6s; asked before MoveTo spends a route on a landing.
func travelLeapReady(ctx *Ctx) bool {
	s := ctx.Snap
	return s != nil && s.Valid && !s.Me.InTown && canVault(ctx, s) && time.Since(travelVaultAt) >= 6*time.Second
}

// travelLeap — P-2.11(4) (the owner, 11:20: "if it's trying to get somewhere
// it could run there and leap it"): MoveTo picks the landing ~12 tiles along
// the PLANNED route (never a straight line through walls); this fires it on
// grid-vouched ground. The walk continues underneath either way.
func travelLeap(ctx *Ctx, land data.Position) bool {
	if !travelLeapReady(ctx) || !vaultLandable(ctx, land) {
		return false
	}
	travelVaultAt = time.Now()
	verbs.Vault{To: land, Key: ctx.Cap.Vault.Key, SkillID: int(ctx.Cap.Vault.Skill)}.
		Do(ctx.M, ctx.GR, ctx.P, ctx.Led, "travelvault")
	return true
}
