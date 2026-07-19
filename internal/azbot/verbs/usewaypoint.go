// UseWaypoint rides the network (P-10). The pad is a FLAT GROUND RUNE — its
// hover lives at the base projection (dy ±34), not 80px up like a portal. The
// panel-open state (OpenMenus.Waypoint) and the LIT destinations
// (AvailableWaypoints — filled ONLY while the panel is open, per selected tab)
// are readable memory: no byte-blind clicking. Row geometry derives from the
// legacy 1280x720 layout scaled by the client height ratio, with a small
// row-sweep because no constant survives a new client unverified (the relog
// coordinate lesson). The only believed postcondition is the area change.
package verbs

import (
	"fmt"
	"image/png"
	"os"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

type UseWaypoint struct {
	Want []area.ID // preference order, deepest first; the first LIT one rides
}

func wpCheb(a, b data.Position) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

func (uw UseWaypoint) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	o := Outcome{Verb: "waypoint", Holder: holder}
	if !m.Engage.Engaged() {
		o.Result = ResRefused
		o.Evidence = "motor disengaged"
		led.Append(o)
		return o
	}
	d := gr.GetData()
	start := d.PlayerUnit.Area

	var wp data.Object
	best := 1 << 30
	for _, ob := range d.Objects {
		if ob.IsWaypoint() && ob.ID != 0 {
			if dd := wpCheb(d.PlayerUnit.Position, ob.Position); dd < best {
				best, wp = dd, ob
			}
		}
	}
	if best > 30 {
		o.Result = ResRefused
		o.Evidence = fmt.Sprintf("no waypoint pad within 30 (nearest %d)", best)
		led.Append(o)
		return o
	}

	m.MoveStop()
	m.ModifierAmnesty()

	// OPEN: hover-confirmed click on the pad base; the click may include the
	// game's own walk-to, so the panel gets a real wait.
	if !d.OpenMenus.Waypoint {
		opened := false
		for try := 0; try < 3 && !opened; try++ {
			dd := gr.GetData()
			me := dd.PlayerUnit.Position
			bx := int(float32((wp.Position.X-me.X)-(wp.Position.Y-me.Y))*19.8) + gr.GameAreaSizeX/2
			by := int(float32((wp.Position.X-me.X)+(wp.Position.Y-me.Y))*9.9) + gr.GameAreaSizeY/2
			clicked := false
		sweep:
			for dy := -34; dy <= 34; dy += 6 {
				for _, dx := range []int{0, -10, 10, -20, 20, -32, 32} {
					cx, cy := bx+dx, by+dy
					if cx < 20 || cy < 20 || cx > gr.GameAreaSizeX-20 || cy > gr.GameAreaSizeY-20 {
						continue
					}
					m.AimPhysical(cx, cy)
					time.Sleep(45 * time.Millisecond)
					hd := gr.GetData().HoverData
					if !hd.IsHovered || hd.UnitID != wp.ID {
						continue
					}
					m.AimPhysical(cx, cy) // double-confirm: frame latency lies once
					time.Sleep(60 * time.Millisecond)
					hd = gr.GetData().HoverData
					if hd.IsHovered && hd.UnitID == wp.ID {
						m.ClickLeft(cx, cy)
						clicked = true
						break sweep
					}
				}
			}
			if clicked {
				dl := time.Now().Add(3500 * time.Millisecond)
				for time.Now().Before(dl) {
					time.Sleep(150 * time.Millisecond)
					if gr.GetData().OpenMenus.Waypoint {
						opened = true
						break
					}
				}
			}
		}
		if !opened {
			o.Result = ResWhiff
			o.Evidence = "pad clicked but the panel never opened"
			led.Append(o)
			return o
		}
	}
	time.Sleep(300 * time.Millisecond)

	// CHOOSE: the panel's own list is the only honest activation read.
	avail := gr.GetData().PlayerUnit.AvailableWaypoints
	var dest area.ID
	for _, w := range uw.Want {
		for _, av := range avail {
			if av == w {
				dest = w
				break
			}
		}
		if dest != 0 {
			break
		}
	}
	if dest == 0 {
		m.KeyLane().Press(0x1B) // close what we opened — never leave a panel standing
		o.Result = ResWhiff
		o.Evidence = fmt.Sprintf("no wanted destination lit (%d lit on this tab)", len(avail))
		led.Append(o)
		return o
	}

	// RIDE: row click scaled by the client height ratio, swept because no
	// layout constant is trusted unverified. Believe only the area change.
	addr := area.WPAddresses[dest]
	scale := float64(gr.GameAreaSizeY) / 720.0
	rx := int(200.0 * scale)
	for _, dyOff := range []int{0, 12, -12, 24} {
		ry := int((158.0+41.0*float64(addr.Row-1))*scale) + dyOff
		m.AimPhysical(rx, ry)
		time.Sleep(60 * time.Millisecond)
		m.BareClick(rx, ry)
		dl := time.Now().Add(4 * time.Second)
		extended := false
		for time.Now().Before(dl) {
			time.Sleep(150 * time.Millisecond)
			now := gr.GetData().PlayerUnit.Area
			if now == 0 && !extended {
				dl = dl.Add(3 * time.Second) // load screen: the ride is happening
				extended = true
			}
			if now != 0 && now != start {
				o.Result = ResDone
				o.Evidence = fmt.Sprintf("rode the network %d -> %d (wanted %d)", int(start), int(now), int(dest))
				led.Append(o)
				return o
			}
		}
	}

	// Every row guess dead: photograph the panel for offline measurement,
	// close it, and report honestly.
	if img := gr.Screenshot(); img != nil {
		if f, err := os.Create(fmt.Sprintf("logs/wp_fail_%d.png", time.Now().Unix())); err == nil {
			_ = png.Encode(f, img)
			f.Close()
		}
	}
	m.KeyLane().Press(0x1B)
	o.Result = ResDeaf
	o.Evidence = fmt.Sprintf("panel open, %s lit, but every row click left the area unchanged (photo saved)", area.Areas[dest].Name)
	led.Append(o)
	_ = p
	return o
}
