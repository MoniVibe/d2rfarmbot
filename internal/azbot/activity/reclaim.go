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
	rounds  int // failed hover-sweep rounds at the body
	coolAt  time.Time
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
	if time.Now().Before(rc.coolAt) {
		return nil // recent give-up: come back at it from a fresh angle in a moment
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
	// GUARDED BODY: monsters standing on the corpse eat every click (the owner watched
	// her "punch things near it" — the blind fallback landing on a monster is a naked
	// attack). The human move: LURE — run away, the swarm follows HER, not the body;
	// loop back to a clear corpse.
	guards := 0
	for _, e := range s.Enemies {
		if chebyshev(s.Me.CorpsePos, e.Pos) <= 7 {
			guards++
		}
	}
	if guards >= 2 && d < 25 {
		away := data.Position{X: me.X + (me.X-s.Me.CorpsePos.X)*3 + 7, Y: me.Y + (me.Y-s.Me.CorpsePos.Y)*3 - 7}
		slideStride(ctx, away, 1500*time.Millisecond, 2, rc.Name())
		return Running
	}
	if d < 1 {
		// Standing ON the body: her own sprite owns the cursor — a corpse under her
		// feet never hovers (measured: pinned 0x0 sweeping forever). Step off first.
		verbs.Stride{To: data.Position{X: me.X + 3, Y: me.Y + 3}, Hold: 600 * time.Millisecond, MinGain: 1}.
			Do(ctx.M, ctx.GR, ctx.P, ctx.Led, rc.Name())
		return Running
	}
	if d > 2 {
		// TRUE adjacency required: chebyshev proximity counts THROUGH walls — she once
		// hover-swept a corpse 5 tiles away on the far side of a trap-fence (the owner's
		// screenshot). Arrive=2 makes the planner route AROUND the fence to really reach it.
		if ctx.Grid != nil {
			if rc.j == nil || chebyshev(rc.j.Goal, s.Me.CorpsePos) > 5 {
				rc.j = journey.New(ctx.GR, ctx.Grid, s.Me.CorpsePos, rc.Name())
				rc.j.Arrive = 2
			}
			st := rc.j.Step(ctx.M, ctx.P, ctx.Led)
			if st.State == journey.NoPath || st.State == journey.Stalled {
				slideStride(ctx, s.Me.CorpsePos, 1200*time.Millisecond, 1, rc.Name())
				rc.j = nil
			}
		} else {
			slideStride(ctx, s.Me.CorpsePos, 1200*time.Millisecond, 1, rc.Name())
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
		rc.tries = 0
		rc.rounds++
		switch {
		case (rc.rounds == 2 || rc.rounds == 3) && guards == 0:
			// Hover refuses to confirm and NOTHING stands on the body — corpses are
			// BIG targets: click the projection blind. With any guard present a blind
			// click is a punch, never a pickup.
			ctx.M.BareClick(bx, by)
			rc.clickAt = time.Now()
		case rc.rounds >= 4:
			// This angle is spent: give the grant back honestly, cool briefly, and
			// come at it fresh — an eternal sweep at one spot is the ponder disease.
			rc.rounds = 0
			rc.coolAt = time.Now().Add(20 * time.Second)
			return Abandoned
		default:
			// Round 1: step off and re-approach from a new angle.
			verbs.Stride{To: data.Position{X: me.X + 6, Y: me.Y - 6}, Hold: 800 * time.Millisecond}.
				Do(ctx.M, ctx.GR, ctx.P, ctx.Led, rc.Name())
		}
	}
	return Running
}
