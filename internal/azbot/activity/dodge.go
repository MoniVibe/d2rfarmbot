// Dodge: the projectile reflex — the owner's ask, verbatim: "blood raven is going to
// mess her up if she won't dance around her arrows."
//
// The oracle reads NO ownership and NO velocity from memory. Velocity is MEASURED from
// the snapshot stream (two frames of the same UnitID give the vector), and hostility is
// inferred from trajectory: her own arrows fly AWAY from her, so only missiles CLOSING
// on her position threaten. Merc/friendly fire launched beside her is excluded by
// origin ("born near me" tracks never threaten — they fly past the shoulder, outward).
package activity

import (
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

type missTrack struct {
	pos        data.Position
	at         time.Time
	vx, vy     float64 // tiles/sec, measured across frames
	hasVel     bool
	bornNearMe bool // first sighted within 4 tiles of her — hers or the merc's; never a threat
	seen       uint64
}

type Dodge struct {
	tracks map[data.UnitID]*missTrack
	// last threat assessment (computed in Demand, consumed by Step the same cycle)
	threatPos    data.Position
	threatVX, threatVY float64
	hasThreat    bool
	lastStrideAt time.Time
}

func NewDodge() *Dodge { return &Dodge{tracks: map[data.UnitID]*missTrack{}} }

func (dg *Dodge) Name() string { return "dodge" }

// observe updates missile tracks from the snapshot and finds the most urgent closing
// missile. Threat = measured speed ≥ 8 tiles/s, closing on her, and the extrapolated
// path passes within 3 tiles of her inside the next 1.5s.
func (dg *Dodge) observe(s *percept.Snapshot) {
	dg.hasThreat = false
	me := s.Me.Pos
	seen := s.Seq
	bestT := math.MaxFloat64
	for _, ms := range s.Missiles {
		tr, ok := dg.tracks[ms.ID]
		if !ok {
			dg.tracks[ms.ID] = &missTrack{pos: ms.Pos, at: s.At, seen: seen,
				bornNearMe: chebyshev(me, ms.Pos) <= 4}
			continue
		}
		dt := s.At.Sub(tr.at).Seconds()
		if dt >= 0.03 {
			nvx := float64(ms.Pos.X-tr.pos.X) / dt
			nvy := float64(ms.Pos.Y-tr.pos.Y) / dt
			if tr.hasVel { // light smoothing over the tile quantization
				nvx, nvy = 0.5*nvx+0.5*tr.vx, 0.5*nvy+0.5*tr.vy
			}
			tr.vx, tr.vy, tr.hasVel = nvx, nvy, true
			tr.pos, tr.at = ms.Pos, s.At
		}
		tr.seen = seen
		if tr.bornNearMe || !tr.hasVel {
			continue
		}
		speed := math.Hypot(tr.vx, tr.vy)
		if speed < 8 || chebyshev(me, tr.pos) > 30 {
			continue
		}
		// Closing? Relative position dotted with velocity.
		rx, ry := float64(me.X-tr.pos.X), float64(me.Y-tr.pos.Y)
		if rx*tr.vx+ry*tr.vy <= 0 {
			continue // flying away or parallel-outward
		}
		// Closest approach of the ray pos+v*t to her, within the horizon.
		tCA := (rx*tr.vx + ry*tr.vy) / (speed * speed)
		if tCA > 1.5 {
			continue
		}
		cax := float64(tr.pos.X) + tr.vx*tCA - float64(me.X)
		cay := float64(tr.pos.Y) + tr.vy*tCA - float64(me.Y)
		if math.Hypot(cax, cay) > 3 {
			continue // passes wide
		}
		if tCA < bestT {
			bestT = tCA
			dg.threatPos, dg.threatVX, dg.threatVY = tr.pos, tr.vx, tr.vy
			dg.hasThreat = true
		}
	}
	// Prune tracks that vanished (missiles die fast; the map must not grow).
	for id, tr := range dg.tracks {
		if tr.seen != seen {
			delete(dg.tracks, id)
		}
	}
}

func (dg *Dodge) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 {
		return nil
	}
	dg.observe(s)
	if !dg.hasThreat {
		return nil
	}
	return &arbiter.Demand{Who: dg.Name(), Class: arbiter.ClassSurvive,
		Urgency: 1.3, // over Flee (≤1.0); under a critical Breakout (1.4+) — bleeding out beats sidestepping
		Commit:  arbiter.Commitment{MinHold: 400 * time.Millisecond}}
}

func (dg *Dodge) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	dg.observe(s)
	if !dg.hasThreat {
		return Done // the arrow flew past (or died on someone else)
	}
	if time.Since(dg.lastStrideAt) < 250*time.Millisecond {
		return Running // let the current sidestep stride play out
	}
	// Sidestep PERPENDICULAR to the missile's velocity — both signs escape the line;
	// pick the side farther from the nearest enemy so the dance doesn't dodge INTO a bite.
	speed := math.Hypot(dg.threatVX, dg.threatVY)
	px, py := -dg.threatVY/speed, dg.threatVX/speed
	const hop = 6
	me := s.Me.Pos
	a := data.Position{X: me.X + int(px*hop), Y: me.Y + int(py*hop)}
	b := data.Position{X: me.X - int(px*hop), Y: me.Y - int(py*hop)}
	tgt := a
	if nearestEnemyDist(s, a) < nearestEnemyDist(s, b) {
		tgt = b
	}
	verbs.Stride{To: tgt, Hold: 350 * time.Millisecond, MinGain: 1}.
		Do(ctx.M, ctx.GR, ctx.P, ctx.Led, dg.Name())
	dg.lastStrideAt = time.Now()
	return Running
}

func nearestEnemyDist(s *percept.Snapshot, p data.Position) int {
	best := 1 << 30
	for _, e := range s.Enemies {
		if d := chebyshev(p, e.Pos); d < best {
			best = d
		}
	}
	return best
}
