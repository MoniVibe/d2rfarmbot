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
	"math"
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
	// RANGE CLAMP, tightened 9→5 (21:05: the 9-tile clamp still whiffed
	// mana-flat while the SAME ClickRight casts Double Swing all day — the
	// game rejects the ASK, and vanilla level-1 Leap reaches ~4.6 yards ≈ 5
	// tiles; an over-range leap click on this mod refuses outright instead
	// of clamping). Five tiles still clears a ring; the ceiling can rise
	// with the skill's level once leaps prove out.
	if dx, dy := float64(v.To.X-me.X), float64(v.To.Y-me.Y); dx*dx+dy*dy > 25 {
		k := 5 / math.Sqrt(dx*dx+dy*dy)
		v.To = data.Position{X: me.X + int(dx*k), Y: me.Y + int(dy*k)}
	}
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
	mpBefore := gr.GetData().PlayerUnit.MPPercent()
	m.ClickRight(bx, by)
	// Displacement postcondition: a leap that fired moves the body within its
	// animation. 900ms window (650 judged real leaps as whiffs — the arc plus
	// landing recovery outlasts it). Judged loosely — the caller's cooldown
	// absorbs a whiff.
	time.Sleep(1200 * time.Millisecond)
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
	}; chebyshev(me, after) >= 2 { // bar 3→2 (21:42: lvl-1 arcs land 2-3 tiles; honest short hops read as whiffs)
		o.Result = ResDone
		o.Evidence = fmt.Sprintf("leapt (%d,%d)→(%d,%d)", me.X, me.Y, after.X, after.Y)
	} else {
		o.Result = ResWhiff
		// Named evidence, not a shrug (11:30: "mana dry or bad landing" hid
		// that his pool was 33 and the whiffs were something else entirely).
		o.Evidence = fmt.Sprintf("no displacement after leap click (mp %d%%→%d%% at click, land=(%d,%d))",
			mpBefore, gr.GetData().PlayerUnit.MPPercent(), v.To.X, v.To.Y)
	}
	led.Append(o)
	return o
}
