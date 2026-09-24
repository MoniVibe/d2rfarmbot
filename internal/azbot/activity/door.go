package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Door (ClassRecover)

// OWNER (2026-09-25, R36): "stuck on a door in claw viper 1". azbot never opened
// doors: 11 minutes held at a closed door with idle monsters behind it. Door bids
// when a closed door stands within doorReach and he has not moved for doorStill,
// clicks it (hover-confirmed, one aim per tick), and is judged by the door no
// longer being selectable. Three tries on one door ban it for two minutes.

const (
	doorReach = 5
	doorStill = 3 * time.Second
)

type Door struct {
	ring    []posAt
	target  data.UnitID
	tries   int
	clickAt time.Time
	ban     map[data.UnitID]time.Time
	aimIdx  int
	aimX    int
	aimY    int
	aimed   bool
}

func NewDoor() *Door { return &Door{ban: map[data.UnitID]time.Time{}} }

func (d *Door) Name() string { return "door" }

func (d *Door) nearest(s *percept.Snapshot, now time.Time) (percept.PortalRef, bool) {
	best, bd := percept.PortalRef{}, 1<<30
	for _, dr := range s.Doors {
		if until, ok := d.ban[dr.ID]; ok && now.Before(until) {
			continue
		}
		if dd := chebyshev(s.Me.Pos, dr.Pos); dd < bd {
			best, bd = dr, dd
		}
	}
	return best, bd <= doorReach
}

func (d *Door) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct < 30 || len(s.Doors) == 0 {
		d.ring = nil
		return nil
	}
	now := time.Now()
	d.ring = append(d.ring, posAt{now, s.Me.Pos})
	for len(d.ring) > 0 && now.Sub(d.ring[0].at) > doorStill+time.Second {
		d.ring = d.ring[1:]
	}
	if d.target == 0 { // a fresh bid needs held ground; a live one keeps its grant
		if len(d.ring) < 3 || now.Sub(d.ring[0].at) < doorStill {
			return nil
		}
		for _, p := range d.ring {
			if chebyshev(p.pos, s.Me.Pos) > 2 {
				return nil // he is moving: no door holds him
			}
		}
	}
	if _, ok := d.nearest(s, now); !ok {
		return nil
	}
	return &arbiter.Demand{Who: d.Name(), Class: arbiter.ClassRecover, Urgency: 0.935,
		Commit: arbiter.Commitment{MinHold: 2 * time.Second}}
}

func (d *Door) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	now := time.Now()
	dr, ok := d.nearest(s, now)
	if !ok {
		d.target = 0
		return Done
	}
	if dr.ID != d.target {
		d.target, d.tries, d.aimed = dr.ID, 0, false
	}
	// the door opened: it is no longer selectable (dropped from s.Doors)
	if time.Since(d.clickAt) < 900*time.Millisecond {
		return Running
	}
	if d.tries >= 3 {
		d.ban[dr.ID] = now.Add(2 * time.Minute)
		ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: d.Name(), Result: verbs.ResDeaf,
			Evidence: fmt.Sprintf("door %d at (%d,%d): 3 clicks, still closed — banned 2m", int(dr.ID), dr.Pos.X, dr.Pos.Y)})
		d.target = 0
		return Done
	}
	if d.aimed {
		if hd := ctx.GR.GetData().HoverData; hd.IsHovered && hd.UnitID == dr.ID {
			ctx.M.BareClick(d.aimX, d.aimY)
			d.clickAt, d.aimed = now, false
			d.tries++
			ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: d.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("clicked door %d at (%d,%d) (try %d)", int(dr.ID), dr.Pos.X, dr.Pos.Y, d.tries)})
			return Running
		}
	}
	ctx.M.MoveStop()
	me := ctx.GR.GetData().PlayerUnit.Position
	bx := int(float32((dr.Pos.X-me.X)-(dr.Pos.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((dr.Pos.X-me.X)+(dr.Pos.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2
	for n := 0; n < len(stashAim); n++ {
		o := stashAim[d.aimIdx%len(stashAim)]
		d.aimIdx++
		cx, cy := bx+o[0], by+o[1]
		if verbs.ClickableLogical(ctx.GR, cx, cy) {
			ctx.M.AimPhysical(cx, cy)
			d.aimX, d.aimY, d.aimed = cx, cy, true
			return Running
		}
	}
	d.tries++ // no clickable aim this tick counts against the door
	return Running
}
