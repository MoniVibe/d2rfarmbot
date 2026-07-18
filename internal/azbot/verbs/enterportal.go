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
}

func (ep EnterPortal) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	win := ep.Window
	if win <= 0 {
		win = 4 * time.Second
	}
	o := Outcome{Verb: "enterportal", Holder: holder, Target: fmt.Sprintf("portal=%d", ep.Target)}
	if !m.Engage.Engaged() {
		o.Result = ResRefused
		o.Evidence = "motor disengaged"
		led.Append(o)
		return o
	}
	m.MoveStop() // the sweep aims the cursor — a held move would walk toward every probe

	d := gr.GetData()
	start := d.PlayerUnit.Area
	me := d.PlayerUnit.Position
	bx := int(float32((ep.TargetPos.X-me.X)-(ep.TargetPos.Y-me.Y))*19.8) + gr.GameAreaSizeX/2
	by := int(float32((ep.TargetPos.X-me.X)+(ep.TargetPos.Y-me.Y))*9.9) + gr.GameAreaSizeY/2

	confirmed, px, py := false, bx, by
sweep:
	for dy := -80; dy <= 16; dy += 8 {
		for _, dx := range []int{0, -8, 8, -16, 16, -26, 26} {
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
		o.Result = ResWhiff
		o.Evidence = "no hover confirmation on portal"
		led.Append(o)
		return o
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
			led.Append(o)
			return o
		}
	}
	o.Result = ResDeaf
	o.Evidence = "clicked portal but area never changed"
	led.Append(o)
	return o
}
