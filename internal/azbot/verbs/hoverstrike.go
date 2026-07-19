package verbs

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

// tomeSkill: right-clicking one of these at a monster is a book, not a weapon —
// the right-skill is PER WEAPON SET on this build, so a stale Identify can sit
// armed on the set we just swapped to no matter what key we pressed moments ago.
func tomeSkill(id skill.ID) bool {
	switch id {
	case skill.TomeOfIdentify, skill.ScrollOfIdentify, skill.TomeOfTownPortal, skill.ScrollOfTownPortal:
		return true
	}
	return false
}

// HoverStrike is THE aimed attack: select the skill (readback-verified by the caller's
// Capability), sweep the cursor until the GAME confirms the hover is on the target's
// unit id, click, then demand evidence within the window. A swing without a confirmed
// hover cannot happen here, and there is no other attack path in azbot.
type HoverStrike struct {
	Target    data.UnitID
	TargetPos data.Position // fresh world position from THIS cycle's snapshot
	SelectKey byte          // 0 = plain left-click attack; else press-then-right-click
	Evidence  time.Duration // window to observe target Mode change; default 900ms
	// Volley: fire-and-track. Skip the evidence window — the CALLER observes the
	// target's Mode from its own snapshot stream. The per-shot evidence block was
	// the pause between shots: cadence must be bounded by the attack animation,
	// not by an observation window.
	Volley bool
	// HintDX/HintDY: the sweep offset that confirmed hover on the LAST shot at this
	// target. Targets move smoothly — the same offset usually confirms on probe one.
	HintDX, HintDY int
	// SkipSelect: the caller proved this SelectKey is already the live selection
	// (same key, moments ago) — save the press + 50ms settle.
	SkipSelect bool
}

func (h HoverStrike) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	ev := h.Evidence
	if ev <= 0 {
		ev = 900 * time.Millisecond
	}
	o := Outcome{Verb: "hoverstrike", Holder: holder, Target: fmt.Sprintf("unit=%d", h.Target)}
	if !m.Engage.Engaged() {
		o.Result = ResRefused
		o.Evidence = "motor disengaged"
		led.Append(o)
		return o
	}
	m.MoveStop() // attack aim must never double as a walk order

	// Fresh self-position for the projection — the stale-coordinate disease dies here.
	d := gr.GetData()
	me := d.PlayerUnit.Position
	bx := int(float32((h.TargetPos.X-me.X)-(h.TargetPos.Y-me.Y))*19.8) + gr.GameAreaSizeX/2
	by := int(float32((h.TargetPos.X-me.X)+(h.TargetPos.Y-me.Y))*9.9) + gr.GameAreaSizeY/2

	// Incremental hover sweep: confirm identity or refuse to click. The hint offset
	// probes FIRST — on a tracked target it usually confirms immediately, collapsing
	// the sweep from up to 25 probes to one.
	confirmed := false
	px, py := bx, by
	probes := [][2]int{{h.HintDX, h.HintDY}}
	for _, dy := range []int{-12, 0, -24, -36, 8} {
		for _, dx := range []int{0, -10, 10, -20, 20} {
			probes = append(probes, [2]int{dx, dy})
		}
	}
	for _, pr := range probes {
		cx, cy := bx+pr[0], by+pr[1]
		if cx < 20 || cy < 20 || cx > gr.GameAreaSizeX-20 || cy > gr.GameAreaSizeY-20 {
			continue
		}
		hidAim(m, cx, cy)
		time.Sleep(30 * time.Millisecond)
		hd := gr.GetData().HoverData
		if hd.IsHovered && hd.UnitID == h.Target {
			confirmed, px, py = true, cx, cy
			o.AimDX, o.AimDY = pr[0], pr[1]
			break
		}
	}
	if !confirmed {
		o.Result = ResWhiff
		o.Evidence = "no hover confirmation on target"
		led.Append(o)
		return o
	}

	// Strike: skill right-click (selection already proven by Capability) or plain attack.
	// THE READBACK GUARD (the owner: "it uses identify instead of jab in close combat"):
	// pressing the key is a hope; the game's RightSkill is the truth. A tome armed on
	// this weapon set gets one corrective re-press; still a tome → plain attack instead.
	// A right-click never fires without knowing what it will cast.
	if h.SelectKey != 0 {
		if !h.SkipSelect {
			m.PressKey(h.SelectKey)
			time.Sleep(50 * time.Millisecond)
		}
		rs := gr.GetData().PlayerUnit.RightSkill
		if tomeSkill(rs) {
			m.PressKey(h.SelectKey)
			time.Sleep(80 * time.Millisecond)
			rs = gr.GetData().PlayerUnit.RightSkill
		}
		if tomeSkill(rs) {
			o.Evidence = fmt.Sprintf("tome armed (skill=%d) — plain attack instead", int(rs))
			m.ClickLeft(px, py)
		} else {
			m.ClickRight(px, py)
		}
	} else {
		m.ClickLeft(px, py)
	}

	// VOLLEY: the click was the verb's whole job. Evidence belongs to the caller's
	// snapshot stream; the next shot can begin as soon as the animation allows.
	if h.Volley {
		o.Result = ResDone
		o.Evidence = "volley"
		led.Append(o)
		return o
	}

	// Evidence: the target's Mode transitions (hit-recoil/dying/dead) inside the window.
	deadlineT := time.Now().Add(ev)
	for time.Now().Before(deadlineT) {
		time.Sleep(120 * time.Millisecond)
		for _, mon := range gr.GetData().Monsters {
			if mon.UnitID == h.Target {
				if mon.Mode == mode.NpcDeath || mon.Mode == mode.NpcDead || mon.Mode == mode.NpcGettingHit {
					o.Result = ResDone
					o.Evidence = fmt.Sprintf("target mode=%d", mon.Mode)
					led.Append(o)
					return o
				}
			}
		}
	}
	o.Result = ResTimeout
	o.Evidence = "no mode evidence in window (may still have dealt damage)"
	led.Append(o)
	return o
}

// Thin motor pass-throughs (kept here so the verb layer, not callers, touches input).
func hidAim(m *motor.Motor, x, y int)          { m.AimPhysical(x, y) }
