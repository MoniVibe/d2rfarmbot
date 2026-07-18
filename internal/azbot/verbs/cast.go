package verbs

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

// CastSelf selects a skill by key (readback-verified) and right-clicks at screen center
// to cast it at own feet — town portal, self-buffs. Postcondition is the caller's
// (the portal object appearing); this verb reports whether the selection took.
type CastSelf struct {
	Key      byte
	WantID   int // the skill.ID the key should select; 0 = accept any change
	OrigSkill int
}

func (c CastSelf) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	o := Outcome{Verb: "castself", Holder: holder}
	if !m.Engage.Engaged() {
		o.Result = ResRefused
		led.Append(o)
		return o
	}
	m.MoveStop()
	before := gr.GetData().PlayerUnit.RightSkill
	m.PressKey(c.Key)
	time.Sleep(90 * time.Millisecond)
	after := gr.GetData().PlayerUnit.RightSkill
	if int(after) == int(before) && c.WantID != 0 && int(after) != c.WantID {
		o.Result = ResDeaf
		o.Evidence = "skill selection did not change"
		led.Append(o)
		return o
	}
	cx, cy := gr.GameAreaSizeX/2, gr.GameAreaSizeY/2
	m.ClickRight(cx, cy)
	o.Result = ResDone
	o.Evidence = "cast at feet"
	led.Append(o)
	return o
}
