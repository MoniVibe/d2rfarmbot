// Reclaim: the corpse run — a death leaves her gear and gold on the body, and the
// first death of the rampage (level 4, Blood Moor, 2026-07-19) proved the gap: Respawn
// revived a naked amazon with no way home to her bow. ClassRecover outranks Fight, so
// she walks PAST the monsters that killed her rather than punching them barehanded;
// deaths on the way are acceptable, the loop repeats until the gear is back.
package activity

import (
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

type Reclaim struct {
	j       *journey.Journey
	clickAt time.Time
	tries   int
}

func NewReclaim() *Reclaim { return &Reclaim{} }

func (rc *Reclaim) Name() string { return "reclaim" }

func (rc *Reclaim) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.HPPct <= 0 || !s.Me.CorpseFound {
		return nil
	}
	// In town, bid only for a body that is HERE (the relog materializes it at the
	// spawn); a far body from town is Relog's problem — Recover outranks Travel, so
	// bidding on an unreachable corpse would deadlock her at the gate.
	if s.Me.InTown && chebyshev(s.Me.Pos, s.Me.CorpsePos) > 150 {
		return nil
	}
	return &arbiter.Demand{Who: rc.Name(), Class: arbiter.ClassRecover,
		Urgency: 0.9, // under Respawn's 1.0 — being alive precedes having gear
		Commit:  arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (rc *Reclaim) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if !s.Me.CorpseFound {
		rc.j, rc.tries = nil, 0
		return Done // the body is reclaimed (or despawned) — capability recalibrates on the armed flip
	}
	me := s.Me.Pos
	d := chebyshev(me, s.Me.CorpsePos)
	if d > 4 {
		if ctx.Grid != nil {
			if rc.j == nil || chebyshev(rc.j.Goal, s.Me.CorpsePos) > 5 {
				rc.j = journey.New(ctx.GR, ctx.Grid, s.Me.CorpsePos, rc.Name())
			}
			st := rc.j.Step(ctx.M, ctx.P, ctx.Led)
			if st.State == journey.NoPath || st.State == journey.Stalled {
				verbs.Stride{To: s.Me.CorpsePos, Hold: 1200 * time.Millisecond, MinGain: 1}.
					Do(ctx.M, ctx.GR, ctx.P, ctx.Led, rc.Name())
				rc.j = nil
			}
		} else {
			verbs.Stride{To: s.Me.CorpsePos, Hold: 1200 * time.Millisecond, MinGain: 1}.
				Do(ctx.M, ctx.GR, ctx.P, ctx.Led, rc.Name())
		}
		return Running
	}
	// At the body: hover-confirm and click. The corpse unit's own IsHovered is the
	// honest oracle — never click blind (the NPC lesson).
	if time.Since(rc.clickAt) < 1500*time.Millisecond {
		return Running // give the pickup animation its moment
	}
	ctx.M.MoveStop()
	cur := ctx.GR.GetData()
	cp := cur.Corpse.Position
	bx := int(float32((cp.X-cur.PlayerUnit.Position.X)-(cp.Y-cur.PlayerUnit.Position.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((cp.X-cur.PlayerUnit.Position.X)+(cp.Y-cur.PlayerUnit.Position.Y))*9.9) + ctx.GR.GameAreaSizeY/2
	for _, off := range []data.Position{{X: 0, Y: 0}, {X: 0, Y: -10}, {X: -10, Y: 0}, {X: 10, Y: 0}, {X: 0, Y: 10}, {X: 0, Y: -20}} {
		cx, cy := bx+off.X, by+off.Y
		if cx < 20 || cy < 20 || cx > ctx.GR.GameAreaSizeX-20 || cy > ctx.GR.GameAreaSizeY-20 {
			continue
		}
		ctx.M.AimPhysical(cx, cy)
		time.Sleep(50 * time.Millisecond)
		if ctx.GR.GetData().Corpse.IsHovered {
			ctx.M.BareClick(cx, cy)
			rc.clickAt = time.Now()
			rc.tries = 0
			return Running
		}
	}
	rc.tries++
	if rc.tries >= 8 {
		// The hover never confirmed from this angle — step off and re-approach.
		verbs.Stride{To: data.Position{X: me.X + 6, Y: me.Y + 6}, Hold: 800 * time.Millisecond}.
			Do(ctx.M, ctx.GR, ctx.P, ctx.Led, rc.Name())
		rc.tries = 0
	}
	return Running
}
