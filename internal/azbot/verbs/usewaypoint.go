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
	Want     []area.ID // preference order, deepest first; empty = touch/activate only
	KeepOpen bool      // diagnostic/open-only mode: leave the verified panel standing
}

// LastPanelOpenAt: when a waypoint panel last VERIFIABLY stood (10:16: the
// deaf ride verdict blocked the lit-ledger write while he stood at an open
// panel — activation was proven and thrown away). Advance reads this to mark
// the CURRENT pad lit regardless of how the ride went.
var LastPanelOpenAt time.Time

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

// portalNearPad answers the safety question for a blind pad click.  The click
// can only enter a portal when the portal overlaps the pad's interaction
// neighborhood; a portal beside the player but on the other side of the town
// square is irrelevant.  Keeping this geometry separate also makes the guard
// deterministic and testable.
func portalNearPad(portals []data.Position, pad data.Position, radius int) bool {
	for _, pos := range portals {
		if wpCheb(pos, pad) <= radius {
			return true
		}
	}
	return false
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
	// HOVER DIES UNFOCUSED (04:33) BUT CLICKS DO NOT (the world walks unfocused
	// on posted clicks — only the hover HIGHLIGHT goes dark). So the ride needs
	// no hover at all: click the pad's computed base and confirm via the
	// OpenMenus.Waypoint MEMORY flag, then drive rows by click + area-change.
	// The blind open is armed only where it is safe: IN TOWN (no monster to
	// mis-click into an attack) and with NO live portal hugging the pad (a
	// stray click on a cast town portal would ride to the field). Focused,
	// the hover sweep still leads; blind is the fallback the sweep hands off to.
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
	blindOK := start.IsTown()
	if blindOK {
		var portals []data.Position
		for i := range d.Objects {
			if d.Objects[i].IsPortal() || d.Objects[i].IsRedPortal() {
				portals = append(portals, d.Objects[i].Position)
			}
		}
		blindOK = !portalNearPad(portals, wp.Position, 12)
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
	// THE FLAG LIES AT LEVEL, TRUTH LIVES ON THE EDGE (03:18, fifth lingering-
	// read organ): OpenMenus.Waypoint stays true from ANY previous open — the
	// verb skipped the pad click entirely, photographed grass, and whiffed
	// '0 lit'. She never clicked the pad. A stale true is RESET (ground
	// click), and only a fresh false->true transition counts as opened.
	if d.OpenMenus.Waypoint {
		groundClose()
		time.Sleep(400 * time.Millisecond)
	}
	{
		opened := false
		clickedEver := false
		for try := 0; try < 3 && !opened; try++ {
			if try > 0 && !gr.GetData().OpenMenus.Waypoint {
				// A prior blind click may have opened the WRONG panel (an NPC
				// under the projection). Ground-click clears any stray menu so
				// the retry starts clean; harmless if nothing is open.
				groundClose()
				time.Sleep(300 * time.Millisecond)
			}
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
			if !clicked && blindOK {
				// THE BLIND OPEN (04:53, owner asleep, game unfocused all night):
				// the hover sweep found nothing because the highlight is dark,
				// but the pad is a flat ground rune and its base projection is
				// exact. Click it directly; OpenMenus.Waypoint (memory, not
				// hover) is the honest confirmation below. Safe: town-only,
				// portal-guarded (blindOK).
				if bx >= 20 && by >= 20 && bx <= gr.GameAreaSizeX-20 && by <= gr.GameAreaSizeY-20 {
					m.ClickLeft(bx, by)
					clicked = true
				}
			}
			if clicked {
				clickedEver = true
				// The click may start a WALK to the pad — the wait extends
				// while she is still closing distance (up to 8s total).
				dl := time.Now().Add(3500 * time.Millisecond)
				hard := time.Now().Add(8 * time.Second)
				lastP := gr.GetData().PlayerUnit.Position
				for time.Now().Before(dl) && time.Now().Before(hard) {
					time.Sleep(150 * time.Millisecond)
					dd2 := gr.GetData()
					if dd2.OpenMenus.Waypoint {
						opened = true
						break
					}
					if dd2.PlayerUnit.Position != lastP {
						lastP = dd2.PlayerUnit.Position
						dl = time.Now().Add(1500 * time.Millisecond) // still walking: keep faith
					}
				}
			} else if try < 2 {
				// RE-ROLL THE ANGLE (night-2 audit finding 3: three sweeps from
				// the same standing spot are one sweep done thrice). One ground
				// step changes every projection — but the step is HOVER-GUARDED
				// (04:02: this blind click landed on the town-square portal
				// cluster and rode him back to the field mid-ritual; the town
				// portals were eating every pad attempt).
				m.AimPhysical(bx, by+30)
				time.Sleep(50 * time.Millisecond)
				if !gr.GetData().HoverData.IsHovered {
					m.BareClick(bx, by+30)
					time.Sleep(700 * time.Millisecond)
				}
			}
		}
		if !opened {
			o.Result = ResWhiff
			hd := gr.GetData().HoverData
			if clickedEver {
				o.Evidence = "pad clicked but the panel never opened"
			} else {
				// Name what the hover DID see — a systematic wp.ID mismatch
				// (the ghost table's cousin) would show here as a stable
				// wrong ID under the cursor.
				o.Evidence = fmt.Sprintf("hover never confirmed on the pad (want id=%d; last hover: is=%v id=%d type=%d) — no click ever fired",
					int(wp.ID), hd.IsHovered, int(hd.UnitID), int(hd.UnitType))
			}
			led.Append(o)
			return o
		}
	}
	time.Sleep(300 * time.Millisecond)
	LastPanelOpenAt = time.Now() // the open is PROVEN: this pad is lit
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
		if !uw.KeepOpen {
			groundClose()
		}
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
	// THE LIST MAY NOT VETO — NOR NARROW (photo 03:23: three lit, list read
	// zero; photo 10:16: list claimed Stony lit, pixels showed it DARK — the
	// list lies in both directions). It may only REORDER: the claimed-lit
	// candidate goes first, but every want gets its row clicked — an unlit
	// row is a harmless no-op and the area change is the only judge.
	cands := append([]area.ID{}, uw.Want...)
	if dest != 0 {
		reordered := []area.ID{dest}
		for _, w := range cands {
			if w != dest {
				reordered = append(reordered, w)
			}
		}
		cands = reordered
	}
	if len(cands) > 3 {
		cands = cands[:3]
	}

	// RIDE: row click scaled by the client height ratio, swept because no
	// layout constant is trusted unverified. Believe only the area change.
	// THE PANEL MUST STAND for every click (01:52 photo: she stood ON the pad,
	// flag true, NO panel on screen — our own queued approach-click closed it
	// within 300ms and the rows fired into the void). Verify before each
	// click; re-open on evaporation.
	// MEASURED GEOMETRY (03:47 photo, logs/wp_panel_open.png: nine rows,
	// Rogue Encampment y≈262 → Catacombs 2 y≈738 at 1051-high capture — row
	// pitch 59.5, NOT the legacy 158+41 formula, whose row-6 click landed on
	// the Black Marsh/Outer Cloister boundary and rode nowhere). Fractions of
	// the capture height travel to any client size.
	fh := float64(gr.GameAreaSizeY)
	rowY := func(row int) int { return int(fh * (262.0 + 59.5*float64(row-1)) / 1051.0) }
	rx := int(fh * 395.0 / 1051.0)
	for _, cand := range cands {
		addr, okAddr := area.WPAddresses[cand]
		if !okAddr {
			continue
		}
		dest = cand
		for _, dyOff := range []int{0, 12, -12} {
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
			ry := rowY(addr.Row) + dyOff
			if !gr.GetData().OpenMenus.Waypoint {
				continue // evaporated before the click: never click the void
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
			// THE PANEL TAKES THE PATCHED CURSOR (night-2 audit finding 4: rows
			// were clicked with the world lane while every PROVEN panel
			// interaction on this mod — repair, skill tree, Akara's shop —
			// needs the uiClick recipe; a deaf row walked all candidates and
			// reported ResDeaf while the ride was one honest click away).
			// Row geometry is measured in the logical client space, while the
			// panel's cursor hit-test is in the physical render space.  Keep the
			// lParam logical and scale only the injected cursor offset.
			m.UIClickScaled(rx, ry, m.PanelScale())
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
