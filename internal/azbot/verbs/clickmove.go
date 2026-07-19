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
}

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
	m.MoveStop() // a held force-move key overrides the click's walk
	d := gr.GetData()
	start := d.PlayerUnit.Position
	bx := int(float32((cm.To.X-start.X)-(cm.To.Y-start.Y))*19.8) + gr.GameAreaSizeX/2
	by := int(float32((cm.To.X-start.X)+(cm.To.Y-start.Y))*9.9) + gr.GameAreaSizeY/2
	// Clamp inside the window preserving direction — a click at the screen
	// edge walks that way; the game paths the rest.
	if bx < 30 {
		bx = 30
	} else if bx > gr.GameAreaSizeX-30 {
		bx = gr.GameAreaSizeX - 30
	}
	if by < 30 {
		by = 30
	} else if by > gr.GameAreaSizeY-90 {
		by = gr.GameAreaSizeY - 90 // the belt bar eats bottom clicks
	}
	m.AimPhysical(bx, by)
	time.Sleep(45 * time.Millisecond)
	if hd := gr.GetData().HoverData; hd.IsHovered {
		// A unit under the click point turns the move into an attack/talk —
		// nudge the aim and re-check once; if still owned, refuse honestly.
		by += 24
		m.AimPhysical(bx, by)
		time.Sleep(45 * time.Millisecond)
		if hd2 := gr.GetData().HoverData; hd2.IsHovered {
			o.Result = ResRefused
			o.Evidence = "every aim point hovered a unit — a click would strike, not walk"
			led.Append(o)
			return o
		}
	}
	m.BareClick(bx, by)
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
			o.Evidence = fmt.Sprintf("walked (%d,%d) -> (%d,%d)", start.X, start.Y, now.X, now.Y)
			led.Append(o)
			return o
		}
	}
	o.Result = ResBlocked
	o.Evidence = fmt.Sprintf("click at (%d,%d) moved nothing in %dms", cm.To.X, cm.To.Y, hold.Milliseconds())
	led.Append(o)
	return o
}
