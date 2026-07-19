package verbs

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

// Pickup grabs one ground item: hover-confirm its unit, click, then demand the typed
// postcondition — ground-gone within the window. A click that changes nothing is
// ResDeaf and the CALLER decides (different bearing, blacklist), never a blind retry.
type Pickup struct {
	Target    data.UnitID
	TargetPos data.Position
	Window    time.Duration // default 1.2s
}

func (pk Pickup) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	win := pk.Window
	if win <= 0 {
		win = 1200 * time.Millisecond
	}
	o := Outcome{Verb: "pickup", Holder: holder, Target: fmt.Sprintf("item=%d", pk.Target)}
	if !m.Engage.Engaged() {
		o.Result = ResRefused
		o.Evidence = "motor disengaged"
		led.Append(o)
		return o
	}
	m.MoveStop()
	d := gr.GetData()
	// P-6.2: evidence names its item — "why did she pick THAT up" must be
	// answerable from the ledger (the owner asked and the log had no answer).
	for _, it := range d.Inventory.ByLocation(item.LocationGround) {
		if it.UnitID == pk.Target {
			o.Target = fmt.Sprintf("item=%d type=%d qual=%d", pk.Target, int(it.ID), int(it.Quality))
			break
		}
	}
	me := d.PlayerUnit.Position
	bx := int(float32((pk.TargetPos.X-me.X)-(pk.TargetPos.Y-me.Y))*19.8) + gr.GameAreaSizeX/2
	by := int(float32((pk.TargetPos.X-me.X)+(pk.TargetPos.Y-me.Y))*9.9) + gr.GameAreaSizeY/2

	confirmed, px, py := false, bx, by
	for _, dy := range []int{0, -8, 8, -16} {
		for _, dx := range []int{0, -10, 10, -20, 20} {
			cx, cy := bx+dx, by+dy
			if cx < 20 || cy < 20 || cx > gr.GameAreaSizeX-20 || cy > gr.GameAreaSizeY-20 {
				continue
			}
			m.AimPhysical(cx, cy)
			time.Sleep(45 * time.Millisecond)
			hd := gr.GetData().HoverData
			if hd.IsHovered && hd.UnitID == pk.Target {
				confirmed, px, py = true, cx, cy
				break
			}
		}
		if confirmed {
			break
		}
	}
	if !confirmed {
		o.Result = ResWhiff
		o.Evidence = "no hover confirmation on item"
		led.Append(o)
		return o
	}
	m.ClickLeft(px, py)

	deadline := time.Now().Add(win)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		still := false
		for _, it := range gr.GetData().Inventory.ByLocation(item.LocationGround) {
			if it.UnitID == pk.Target {
				still = true
				break
			}
		}
		if !still {
			o.Result = ResDone
			o.Evidence = "ground-gone"
			led.Append(o)
			return o
		}
	}
	o.Result = ResDeaf
	o.Evidence = "item still on ground after click"
	led.Append(o)
	return o
}
