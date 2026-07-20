// Vault — THE LEAP (P-2.11; the owner, 2026-07-20: "i gave our barb leap,
// mapped it to f5 — use it from time to time, get through problematic
// situations, jump through doors or something"). A cursor-targeted
// self-displacement: select the proven vault skill, verify the selection by
// readback (a key press is a hope; RightSkill is the truth — the tome law),
// then one right-click at the projected landing. Bodies, rings, and doorstep
// clutter are scenery to a leap. The CALLER owns the cooldown and the landing
// choice; this verb refuses only what it can prove wrong.
package verbs

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

type Vault struct {
	To      data.Position
	Key     byte
	SkillID int // the proven binding's skill — the readback must name it before any click
}

func (v Vault) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	o := Outcome{Verb: "vault", Holder: holder, Target: fmt.Sprintf("(%d,%d)", v.To.X, v.To.Y)}
	if !m.Engage.Engaged() {
		o.Result = ResRefused
		o.Evidence = "motor disengaged"
		led.Append(o)
		return o
	}
	m.MoveStop()
	m.PressKey(v.Key)
	time.Sleep(60 * time.Millisecond)
	if int(gr.GetData().PlayerUnit.RightSkill) != v.SkillID {
		m.PressKey(v.Key) // one corrective re-press (weapon-set skill memory)
		time.Sleep(80 * time.Millisecond)
		if int(gr.GetData().PlayerUnit.RightSkill) != v.SkillID {
			o.Result = ResRefused
			o.Evidence = fmt.Sprintf("readback refused: right skill=%d want=%d — never leap blind",
				int(gr.GetData().PlayerUnit.RightSkill), v.SkillID)
			led.Append(o)
			return o
		}
	}
	d := gr.GetData()
	me := d.PlayerUnit.Position
	bx := int(float32((v.To.X-me.X)-(v.To.Y-me.Y))*19.8) + gr.GameAreaSizeX/2
	by := int(float32((v.To.X-me.X)+(v.To.Y-me.Y))*9.9) + gr.GameAreaSizeY/2
	if bx < 20 {
		bx = 20
	} else if bx > gr.GameAreaSizeX-20 {
		bx = gr.GameAreaSizeX - 20
	}
	if by < 20 {
		by = 20
	} else if by > gr.GameAreaSizeY-20 {
		by = gr.GameAreaSizeY - 20
	}
	m.ClickRight(bx, by)
	// Displacement postcondition: a leap that fired moves the body within its
	// animation (~600ms). Judged loosely — the caller's cooldown absorbs a whiff.
	time.Sleep(650 * time.Millisecond)
	after := gr.GetData().PlayerUnit.Position
	if chebyshev := func(a, b data.Position) int {
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
	}; chebyshev(me, after) >= 3 {
		o.Result = ResDone
		o.Evidence = fmt.Sprintf("leapt (%d,%d)→(%d,%d)", me.X, me.Y, after.X, after.Y)
	} else {
		o.Result = ResWhiff
		o.Evidence = "no displacement after leap click (mana dry or bad landing)"
	}
	led.Append(o)
	return o
}
