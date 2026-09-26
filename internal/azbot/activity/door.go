package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Door (ClassRecover)
//
// OWNER (2026-09-25, R36): "stuck on a door in claw viper 1"; (2026-09-26):
// "why is it having a hard time with doors though". Three faults in the first
// cut, fixed here:
//   - REACTIVE: it bid only after 3 s standing within 5 of a closed door, but
//     she BUMPS a door (jitter over 2 tiles reads as moving) and the unstick
//     watchdog took her away first. A closed door within doorNear now bids at
//     once — the planner walks straight through doors.
//   - HOVER-ONLY: it clicked only on a hover confirmation; unfocused the hover
//     is dark, so it re-aimed forever and never clicked (nor ever banned).
//     After doorDarkAims dark aims it clicks the box center blind — the posted
//     click is background-safe.
//   - GUESSED AIM: the stash chest's offsets. The object table has each door's
//     selection box (classic px from the object's tile): aim at its center.

const (
	doorNear     = 4 // a closed door this close is in the way: open it now
	doorReach    = 5 // the stuck rule's reach
	doorStill    = 3 * time.Second
	doorDarkAims = 2
)

type Door struct {
	ring     []posAt
	target   data.UnitID
	tries    int
	dark     int
	clickAt  time.Time
	ban      map[data.UnitID]time.Time
	aimIdx   int
	aimX     int
	aimY     int
	aimed    bool
	boxX     int // the current target's box center (the blind click point)
	boxY     int
	boxKnown bool
}

func NewDoor() *Door { return &Door{ban: map[data.UnitID]time.Time{}} }

func (d *Door) Name() string { return "door" }

func (d *Door) nearest(s *percept.Snapshot, now time.Time, reach int) (percept.PortalRef, bool) {
	best, bd := percept.PortalRef{}, 1<<30
	for _, dr := range s.Doors {
		if until, ok := d.ban[dr.ID]; ok && now.Before(until) {
			continue
		}
		if dd := chebyshev(s.Me.Pos, dr.Pos); dd < bd {
			best, bd = dr, dd
		}
	}
	return best, bd <= reach
}

// held: the stuck rule — no ground gained for doorStill.
func (d *Door) held(s *percept.Snapshot, now time.Time) bool {
	if len(d.ring) < 3 || now.Sub(d.ring[0].at) < doorStill {
		return false
	}
	for _, p := range d.ring {
		if chebyshev(p.pos, s.Me.Pos) > 2 {
			return false
		}
	}
	return true
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
	_, near := d.nearest(s, now, doorNear)
	_, reach := d.nearest(s, now, doorReach)
	if !(near || (reach && (d.target != 0 || d.held(s, now)))) {
		return nil
	}
	return &arbiter.Demand{Who: d.Name(), Class: arbiter.ClassRecover, Urgency: 0.935,
		Commit: arbiter.Commitment{MinHold: 2 * time.Second}}
}

// doorAimOffsets: click offsets (projection px) inside the door's selection
// box, center first; the stash offsets when the table has no box.
func doorAimOffsets(obj object.Name) []data.Position {
	desc := obj.Desc()
	if box := boxOffsetsXY(desc.Left, desc.Top, desc.Width, desc.Height); box != nil {
		return box
	}
	out := make([]data.Position, 0, len(stashAim))
	for _, o := range stashAim {
		out = append(out, data.Position{X: o[0], Y: o[1]})
	}
	return out
}

func (d *Door) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	now := time.Now()
	dr, ok := d.nearest(s, now, doorReach)
	if !ok {
		d.target = 0
		return Done // opened (no longer selectable) or out of reach
	}
	if dr.ID != d.target {
		d.target, d.tries, d.dark, d.aimed, d.boxKnown = dr.ID, 0, 0, false, false
	}
	if time.Since(d.clickAt) < 900*time.Millisecond {
		return Running // the click's answer: the door leaves the closed list
	}
	if d.tries >= 3 {
		d.ban[dr.ID] = now.Add(2 * time.Minute)
		ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: d.Name(), Result: verbs.ResDeaf,
			Evidence: fmt.Sprintf("door %d (obj %d) at (%d,%d): 3 clicks, still closed — banned 2m", int(dr.ID), int(dr.Obj), dr.Pos.X, dr.Pos.Y)})
		d.target = 0
		return Done
	}
	me := ctx.GR.GetData().PlayerUnit.Position
	bx := int(float32((dr.Pos.X-me.X)-(dr.Pos.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((dr.Pos.X-me.X)+(dr.Pos.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2
	offs := doorAimOffsets(dr.Obj)
	d.boxX, d.boxY, d.boxKnown = bx+offs[0].X, by+offs[0].Y, true
	click := func(x, y int, how string) {
		ctx.M.BareClick(x, y)
		d.clickAt, d.aimed = now, false
		d.tries++
		ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: d.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("clicked door %d (obj %d) at (%d,%d) (try %d, %s)", int(dr.ID), int(dr.Obj), dr.Pos.X, dr.Pos.Y, d.tries, how)})
	}
	if d.aimed {
		if hd := ctx.GR.GetData().HoverData; hd.IsHovered && hd.UnitID == dr.ID {
			click(d.aimX, d.aimY, "hover-confirmed")
			return Running
		}
		d.dark++
		if d.dark >= doorDarkAims && verbs.ClickableLogical(ctx.GR, d.boxX, d.boxY) {
			d.dark = 0
			click(d.boxX, d.boxY, "box center, hover dark")
			return Running
		}
	}
	ctx.M.MoveStop()
	for n := 0; n < len(offs); n++ {
		o := offs[d.aimIdx%len(offs)]
		d.aimIdx++
		cx, cy := bx+o.X, by+o.Y
		if verbs.ClickableLogical(ctx.GR, cx, cy) {
			ctx.M.AimPhysical(cx, cy)
			d.aimX, d.aimY, d.aimed = cx, cy, true
			return Running
		}
	}
	d.tries++ // no clickable aim this tick counts against the door
	return Running
}
