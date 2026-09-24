// ClickMove: THE FISH CURE, ported (farmbot's click gait — "far travel CLICKS
// the carrot and lets the game's own pathfinder walk; exact arrivals, zero
// thrash"). The game's pathfinder is the ONLY entity that knows the mod's
// invented fences — the panels exist in no grid, live or mapped, and every
// force-move stride of 2026-07-20's small hours fought walls the game routes
// around for free. A hover check dodges accidental attacks; the postcondition
// is displacement, honestly measured.
package verbs

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

type ClickMove struct {
	To   data.Position
	Hold time.Duration // judge window; default 1400ms
	// CombatKey: THE MARCH SWINGS (the owner, 04:33: a melee skill on right-
	// click walks toward the point and engages whatever it meets on the way).
	// Nonzero = select this skill and travel by RIGHT-click: the walk itself
	// is the weapon. A hovered monster under the point is a bonus, not a
	// hazard — the dodge is skipped.
	CombatKey byte
}

// LastWaystation: the point the last ClickMove actually clicked — the nav
// debugger draws it so the owner can SEE where her clicks land.
var LastWaystation data.Position

func (cm ClickMove) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	hold := cm.Hold
	if hold <= 0 {
		hold = 1400 * time.Millisecond
	}
	o := Outcome{Verb: "clickmove", Holder: holder, Target: fmt.Sprintf("(%d,%d)", cm.To.X, cm.To.Y)}
	if !m.Engage.Engaged() {
		o.Result = ResRefused
		o.Evidence = "motor disengaged"
		led.Append(o)
		return o
	}
	m.MoveStop()        // a held force-move key overrides the click's walk
	m.ModifierAmnesty() // a latched shift turns every click into an attack-in-place
	d := gr.GetData()
	start := d.PlayerUnit.Position
	// THE WAYSTATION (01:39: direction-scaled clicks landed on the river bank
	// — D2R ignores a click on unpathable ground, and 01:38's independent
	// clamps fed Blaise's portrait before that): never click a direction,
	// click a WALKABLE POINT. Sample the line to the target at decreasing
	// ranges and take the farthest one the map grid calls walkable; ranges
	// ≤30 project on-screen naturally, so no clamp exists to warp anything.
	total := cm.To.X - start.X
	if d2 := cm.To.Y - start.Y; d2 > total {
		total = d2
	}
	if total < 0 {
		total = -total
	}
	if t2 := start.X - cm.To.X; t2 > total {
		total = t2
	}
	if t2 := start.Y - cm.To.Y; t2 > total {
		total = t2
	}
	ad, hasMap := d.Areas[d.PlayerUnit.Area]
	// THE HUD LAW (step 11): a waystation that projects onto the bottom bar,
	// an orb or the portrait is not a walk order — it clicks a skill button,
	// the belt or the mini-menu. Such samples are passed over like unwalkable
	// ones; the shorter ranges project nearer the player, off the HUD.
	aim := HUD(gr)
	project := func(p data.Position) (int, int) {
		return int(float32((p.X-start.X)-(p.Y-start.Y))*19.8) + gr.GameAreaSizeX/2,
			int(float32((p.X-start.X)+(p.Y-start.Y))*9.9) + gr.GameAreaSizeY/2
	}
	onHUD := func(p data.Position) bool {
		x, y := project(p)
		return !aim.SafeLogical(x, y)
	}
	way := cm.To
	for _, dist := range []int{30, 22, 15, 10, 6} {
		if total <= dist {
			way = cm.To
		} else {
			way = data.Position{
				X: start.X + (cm.To.X-start.X)*dist/total,
				Y: start.Y + (cm.To.Y-start.Y)*dist/total,
			}
		}
		if onHUD(way) {
			continue
		}
		if !hasMap || ad.Grid == nil {
			break // no oracle: click the nearest sample and hope honestly
		}
		rp := ad.Grid.RelativePosition(way)
		if rp.X >= 0 && rp.Y >= 0 && rp.X < ad.Grid.Width && rp.Y < ad.Grid.Height &&
			ad.Grid.CollisionGrid[rp.Y][rp.X] == game.CollisionTypeWalkable {
			break
		}
	}
	// THE GEOMETRIC PORTAL-SHY (02:52:00, caught by the town-hop witness: the
	// hover dodge sampled once, read nothing, and the click swallowed him into
	// the UP-entrance corpse portal — "blocked, moved nothing in 1200ms", then
	// town. Hover flickers; geometry doesn't): a waystation within 5 tiles of
	// any live portal is shoved perpendicular before it earns a click.
	for i := range d.Objects {
		if !d.Objects[i].IsPortal() && !d.Objects[i].IsRedPortal() {
			continue
		}
		pp := d.Objects[i].Position
		pdx, pdy := way.X-pp.X, way.Y-pp.Y
		if pdx < 0 {
			pdx = -pdx
		}
		if pdy < 0 {
			pdy = -pdy
		}
		pd := pdx
		if pdy > pd {
			pd = pdy
		}
		if pd <= 5 {
			// perpendicular to the march line, on the side away from the portal
			mdx, mdy := way.X-start.X, way.Y-start.Y
			ox, oy := -mdy, mdx
			if (pp.X-way.X)*ox+(pp.Y-way.Y)*oy > 0 {
				ox, oy = -ox, -oy
			}
			n := ox
			if n < 0 {
				n = -n
			}
			if m := oy; m > n || -m > n {
				if m < 0 {
					m = -m
				}
				n = m
			}
			if n == 0 {
				n = 1
			}
			way = data.Position{X: way.X + ox*7/n, Y: way.Y + oy*7/n}
		}
	}
	bx, by := project(way)
	if bx < 130 || by < 130 || bx > gr.GameAreaSizeX-130 || by > gr.GameAreaSizeY-170 || !aim.SafeLogical(bx, by) {
		o.Result = ResRefused
		o.Evidence = fmt.Sprintf("no walkable on-screen waystation toward (%d,%d)", cm.To.X, cm.To.Y)
		if z := aim.ZoneAtLogical(bx, by); z != "" {
			o.Evidence += " (hud: " + z + ")"
		}
		led.Append(o)
		return o
	}
	m.AimPhysical(bx, by)
	time.Sleep(45 * time.Millisecond)
	// THE PORTAL AMBUSH (night 2, 02:31 + 02:35: two silent field→town trips
	// with no verb logged — old corpse/escape portals stand near doors, and a
	// march click that lands on their sprite is a ride home the log can't
	// even see). A hovered PORTAL is never a bonus: dodge it under any key.
	isPortalHover := func(id data.UnitID) bool {
		for i := range d.Objects {
			if d.Objects[i].ID == id {
				return d.Objects[i].IsPortal() || d.Objects[i].IsRedPortal()
			}
		}
		return false
	}
	if hd := gr.GetData().HoverData; hd.IsHovered && (cm.CombatKey == 0 || isPortalHover(hd.UnitID)) {
		// A unit under the click point turns the move into an attack/talk/ride —
		// nudge the aim and re-check once; if still owned, refuse honestly.
		// The nudge goes DOWN (toward the feet) — unless down is the HUD.
		if aim.SafeLogical(bx, by+24) {
			by += 24
		} else {
			by -= 24
		}
		m.AimPhysical(bx, by)
		time.Sleep(45 * time.Millisecond)
		if hd2 := gr.GetData().HoverData; hd2.IsHovered && (cm.CombatKey == 0 || isPortalHover(hd2.UnitID)) {
			o.Result = ResRefused
			o.Evidence = "every aim point hovered a unit/portal — a click would strike or ride, not walk"
			led.Append(o)
			return o
		}
	}
	LastWaystation = way
	// THE READBACK GUARD, MARCHING HAND (step 11): hoverstrike's tome guard
	// never reached the march swing — with the Identify tome still armed on
	// the right hand, the "walk-and-engage" right-click read a book and opened
	// the inventory. Press, read RightSkill back, one corrective re-press;
	// still a tome → walk with a plain left click instead. A right-click never
	// fires without knowing what it will cast.
	right := false
	if cm.CombatKey != 0 {
		m.PressKey(cm.CombatKey)
		time.Sleep(50 * time.Millisecond)
		rs := gr.GetData().PlayerUnit.RightSkill
		if tomeSkill(rs) {
			m.PressKey(cm.CombatKey)
			time.Sleep(80 * time.Millisecond)
			rs = gr.GetData().PlayerUnit.RightSkill
		}
		right = !tomeSkill(rs)
		if !right {
			o.Evidence = fmt.Sprintf("tome armed (skill=%d) — walked by left click; ", int(rs))
		}
	}
	if right {
		m.ClickRight(bx, by) // walk-and-engage: the march swings
	} else {
		m.BareClick(bx, by)
	}
	deadline := time.Now().Add(hold)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		now := gr.GetData().PlayerUnit.Position
		dx, dy := now.X-start.X, now.Y-start.Y
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if dx >= 3 || dy >= 3 {
			o.Result = ResDone
			o.Evidence += fmt.Sprintf("walked (%d,%d) -> (%d,%d)", start.X, start.Y, now.X, now.Y)
			led.Append(o)
			return o
		}
	}
	o.Result = ResBlocked
	o.Evidence += fmt.Sprintf("click at (%d,%d) moved nothing in %dms", cm.To.X, cm.To.Y, hold.Milliseconds())
	led.Append(o)
	return o
}
