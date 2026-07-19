package verbs

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

// EnterPortal clicks THROUGH a portal object — the farmbot's proven towntrip recipe.
// Walking onto a portal does nothing in D2R; the portal must be clicked, and the
// sprite is TALL: the clickable label lives well above the base position, so the
// sweep climbs -80..+16 pixels. Hover is DOUBLE-CONFIRMED (re-aim the exact point,
// settle, re-read) because a single read may reflect a prior probe's cursor position
// — the frame-latency lesson. Postcondition is the only honest one: the area changes.
type EnterPortal struct {
	Target    data.UnitID
	TargetPos data.Position
	Window    time.Duration // wait for the area transition; default 4s (the click starts a walk-and-enter)
	// Desperate: entering IS the survival act. The sweep shortens (time is blood) and
	// a failed hover-confirm falls through to a BLIND click at the label projection —
	// in a swarm the bodies own the hover, and a missed click costs one swing while a
	// refused click costs the life (run 24: pinned beside her own portal, 65->0 in 11s).
	Desperate bool
}

// portalDeaf tracks per-portal deaf streaks — a portal that clicks but never
// transitions is a DEAD DOOR. Hammering it is the in/out loop the owner
// watched (2026-07-19 evening: "in and out of a town portal... rinse
// repeat"). Three deaf clicks blacklist the portal for 45 s so the caller
// (Flee/Return) falls through to marching or fighting instead.
type portalDeafRec struct {
	n  int
	at time.Time
}

var portalDeaf = map[data.UnitID]*portalDeafRec{}

func (ep EnterPortal) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	win := ep.Window
	if win <= 0 {
		win = 4 * time.Second
		if ep.Desperate {
			win = 2 * time.Second // a miss must return the actuator fast
		}
	}
	o := Outcome{Verb: "enterportal", Holder: holder, Target: fmt.Sprintf("portal=%d", ep.Target)}
	if !m.Engage.Engaged() {
		o.Result = ResRefused
		o.Evidence = "motor disengaged"
		led.Append(o)
		return o
	}
	// A DEAD DOOR is refused, not hammered (the portal loop-breaker).
	if r := portalDeaf[ep.Target]; r != nil {
		if time.Since(r.at) > 45*time.Second {
			delete(portalDeaf, ep.Target) // the blacklist ages out; try once more
		} else if r.n >= 3 {
			o.Result = ResRefused
			o.Evidence = "dead door — refused (3 deaf clicks, no transition)"
			led.Append(o)
			return o
		}
	}
	m.MoveStop()       // the sweep aims the cursor — a held move would walk toward every probe
	m.ModifierAmnesty() // a latched shift turns the portal click into an air-swing beside it

	d := gr.GetData()
	start := d.PlayerUnit.Area
	me := d.PlayerUnit.Position
	bx := int(float32((ep.TargetPos.X-me.X)-(ep.TargetPos.Y-me.Y))*19.8) + gr.GameAreaSizeX/2
	by := int(float32((ep.TargetPos.X-me.X)+(ep.TargetPos.Y-me.Y))*9.9) + gr.GameAreaSizeY/2

	dyStep, dxProbes := 8, []int{0, -8, 8, -16, 16, -26, 26}
	if ep.Desperate {
		dyStep, dxProbes = 16, []int{0, -10, 10} // ~1s worst case instead of ~4s
	}
	confirmed, px, py := false, bx, by
sweep:
	for dy := -80; dy <= 16; dy += dyStep {
		for _, dx := range dxProbes {
			cx, cy := bx+dx, by+dy
			if cx < 20 || cy < 20 || cx > gr.GameAreaSizeX-20 || cy > gr.GameAreaSizeY-20 {
				continue
			}
			m.AimPhysical(cx, cy)
			time.Sleep(50 * time.Millisecond)
			hd := gr.GetData().HoverData
			if !hd.IsHovered || hd.UnitID != ep.Target {
				continue
			}
			// Double-confirm: re-aim THIS point, settle longer, and only believe a
			// hover that survives — then the cursor is truly on the label.
			m.AimPhysical(cx, cy)
			time.Sleep(70 * time.Millisecond)
			hd = gr.GetData().HoverData
			if hd.IsHovered && hd.UnitID == ep.Target {
				confirmed, px, py = true, cx, cy
				break sweep
			}
		}
	}
	if !confirmed {
		if !ep.Desperate {
			o.Result = ResWhiff
			o.Evidence = "no hover confirmation on portal"
			led.Append(o)
			return o
		}
		// Desperate blind fallback: the label sits ~40px above the base — click it.
		px, py = bx, by-40
		o.Evidence = "blind click (swarm owns the hover)"
	}

	m.ClickLeft(px, py)

	// The click starts a walk-to-and-enter; wait (uninterrupted) for the transition.
	deadline := time.Now().Add(win)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		now := gr.GetData().PlayerUnit.Area
		if now != start && now != 0 {
			o.Result = ResDone
			o.Evidence = fmt.Sprintf("area %d -> %d", int(start), int(now))
			delete(portalDeaf, ep.Target) // a live door clears its record
			led.Append(o)
			return o
		}
	}
	// Count this deaf click toward the dead-door blacklist (loop-breaker).
	r := portalDeaf[ep.Target]
	if r == nil {
		r = &portalDeafRec{}
		portalDeaf[ep.Target] = r
	}
	r.n++
	r.at = time.Now()
	o.Result = ResDeaf
	o.Evidence = fmt.Sprintf("clicked portal but area never changed (deaf %d/3)", r.n)
	led.Append(o)
	return o
}
