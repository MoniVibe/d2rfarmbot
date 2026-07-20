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

// With an empty Want, the verb is the TOUCH ritual: opening the panel IS the
// activation (a dark pad lights on its first open) — ESC closes it and the
// network has grown. With Want, the first LIT destination rides.
type UseWaypoint struct {
	Want []area.ID // preference order, deepest first; empty = touch/activate only
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
	// SINGLE-FLIGHT PRECONDITION (the advisor; and 03:08's "0 lit" race): no
	// pad click while movement is still in flight — wait until two reads
	// agree she is STATIONARY, so no queued click can evaporate the panel.
	prev := gr.GetData().PlayerUnit.Position
	for i := 0; i < 10; i++ {
		time.Sleep(150 * time.Millisecond)
		now := gr.GetData().PlayerUnit.Position
		if now == prev {
			break
		}
		prev = now
	}
	// groundClose: the ONLY safe panel-closer. ESC raises the quit menu when
	// the panel is already gone, and the Waypoint flag LINGERS after close
	// (03:08 — the fourth organ of the lingering-read disease), so no byte
	// can authorize an ESC. A ground click closes any world panel and at
	// worst walks her one step.
	groundClose := func() {
		bx := gr.GameAreaSizeX/2 + 60 // a few tiles south-east of her feet
		by := gr.GameAreaSizeY/2 + 60
		m.AimPhysical(bx, by)
		time.Sleep(50 * time.Millisecond)
		if !gr.GetData().HoverData.IsHovered { // never click a unit by accident
			m.BareClick(bx, by)
		}
	}

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
	// Photograph the OPEN panel every session (00:54: the failure photo showed
	// no panel at all — the first bad row click had closed it, and the picture
	// that could have named the true rows was taken twenty seconds too late).
	if img := gr.Screenshot(); img != nil {
		if f, err := os.Create("logs/wp_panel_open.png"); err == nil {
			_ = png.Encode(f, img)
			f.Close()
		}
	}

	// TOUCH mode: the open was the point — the pad is lit now and forever.
	if len(uw.Want) == 0 {
		groundClose()
		o.Result = ResDone
		o.Evidence = "pad touched — the network grows"
		led.Append(o)
		return o
	}

	// CHOOSE: the panel's own list is the only honest activation read — read
	// TWICE (the advisor's two-consecutive-observations law): a mid-
	// evaporation read says "0 lit" and a race must not be believed.
	avail := gr.GetData().PlayerUnit.AvailableWaypoints
	if len(avail) == 0 {
		time.Sleep(300 * time.Millisecond)
		avail = gr.GetData().PlayerUnit.AvailableWaypoints
	}
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
		groundClose()
		o.Result = ResWhiff
		o.Evidence = fmt.Sprintf("no wanted destination lit (%d lit on this tab)", len(avail))
		led.Append(o)
		return o
	}

	// RIDE: row click scaled by the client height ratio, swept because no
	// layout constant is trusted unverified. Believe only the area change.
	// THE PANEL MUST STAND for every click (01:52 photo: she stood ON the pad,
	// flag true, NO panel on screen — our own queued approach-click closed it
	// within 300ms and the rows fired into the void). Verify before each
	// click; re-open on evaporation.
	addr := area.WPAddresses[dest]
	scale := float64(gr.GameAreaSizeY) / 720.0
	rx := int(200.0 * scale)
	for _, dyOff := range []int{0, 12, -12, 24} {
		if !gr.GetData().OpenMenus.Waypoint {
			// Evaporated: one quiet re-open (the pad is at her feet), then verify.
			time.Sleep(400 * time.Millisecond) // let any queued clicks land first
			reopened := false
			for r := 0; r < 2 && !reopened; r++ {
				dd := gr.GetData()
				me2 := dd.PlayerUnit.Position
				rbx := int(float32((wp.Position.X-me2.X)-(wp.Position.Y-me2.Y))*19.8) + gr.GameAreaSizeX/2
				rby := int(float32((wp.Position.X-me2.X)+(wp.Position.Y-me2.Y))*9.9) + gr.GameAreaSizeY/2
				m.AimPhysical(rbx, rby)
				time.Sleep(60 * time.Millisecond)
				if hd := gr.GetData().HoverData; hd.IsHovered && hd.UnitID == wp.ID {
					m.ClickLeft(rbx, rby)
					dl := time.Now().Add(2500 * time.Millisecond)
					for time.Now().Before(dl) {
						time.Sleep(150 * time.Millisecond)
						if gr.GetData().OpenMenus.Waypoint {
							reopened = true
							break
						}
					}
				}
			}
			if !reopened {
				continue // next sweep iteration retries the whole cycle
			}
			time.Sleep(250 * time.Millisecond)
		}
		ry := int((158.0+41.0*float64(addr.Row-1))*scale) + dyOff
		m.AimPhysical(rx, ry)
		time.Sleep(60 * time.Millisecond)
		if !gr.GetData().OpenMenus.Waypoint {
			continue // evaporated between aim and click: never click the void
		}
		// One photo per session under a VERIFIED-standing panel — the honest
		// row measurement (the 01:52 photo showed grass because the panel had
		// already evaporated).
		if dyOff == 0 {
			if img := gr.Screenshot(); img != nil {
				if f, err := os.Create("logs/wp_panel_open.png"); err == nil {
					_ = png.Encode(f, img)
					f.Close()
				}
			}
		}
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
	groundClose()
	o.Result = ResDeaf
	o.Evidence = fmt.Sprintf("panel open, %s lit, but every row click left the area unchanged (photo saved)", area.Areas[dest].Name)
	led.Append(o)
	_ = p
	return o
}
