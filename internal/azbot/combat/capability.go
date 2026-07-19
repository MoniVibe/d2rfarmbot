// Package combat: the self-model's combat surface. Capability is SELF-CALIBRATING:
// a key binding is believed only after pressing it and reading the selection back —
// the KeyBindings memory block is dead on this repack, and flags are only claims.
// "The bot needs to know and be aware of itself" — this is where it learns what it
// can actually do.
package combat

import (
	"log/slog"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/game"
)

// Binding is one PROVEN key→skill mapping.
type Binding struct {
	Key   byte     `json:"key"`
	Skill skill.ID `json:"skill"`
}

// Capability is what the character can provably do right now.
type Capability struct {
	Known      map[skill.ID]int // skills present in the live Skills map (level; 0 = granted)
	Proven     []Binding        // key presses that demonstrably flipped RightSkill
	Melee      *Binding         // preferred melee strike (proven)
	Throw      *Binding         // proven throw selection
	RangedCast *Binding         // proven bow-skill selection (Magic/Fire/Cold Arrow...)
	TownTP     *Binding         // proven town portal selection
	Identify   *Binding         // proven identify selection
}

// Calibrate presses each candidate key once and reads RightSkill back. Run at session
// start and after any weapon change; results are ScopeGame facts (weapon-dependent).
// The caller must hold combat-safe conditions (no enemies near) — M4's harness does.
func Calibrate(log *slog.Logger, gr *game.MemoryReader, hid *game.HID, mem *memory.Store, candidates []string) Capability {
	cap := Capability{Known: map[skill.ID]int{}}
	d := gr.GetData()
	for id, pts := range d.PlayerUnit.Skills {
		cap.Known[id] = int(pts.Level)
	}
	before := d.PlayerUnit.RightSkill
	for _, name := range candidates {
		key := hid.GetASCIICode(name)
		hid.PressKey(key)
		time.Sleep(120 * time.Millisecond)
		after := gr.GetData().PlayerUnit.RightSkill
		if after != before {
			b := Binding{Key: key, Skill: after}
			cap.Proven = append(cap.Proven, b)
			log.Info("capability: proven binding", "key", name, "skill", int(after))
			// The SELECTION FLIP is itself behavioral proof she owns the skill (the mod
			// grants some at level 0 — a points requirement wrongly disarmed Magic
			// Arrow, measured 03:14). Fight's flinch audit is the guard against a
			// scrambled table: a "skill" that never hurts anyone gets demoted live.
			switch after {
			case skill.Jab, skill.PowerStrike, skill.AttackSkill:
				if cap.Melee == nil {
					v := b
					cap.Melee = &v
				}
			case skill.MagicArrow, skill.FireArrow, skill.ColdArrow, skill.IceArrow, skill.GuidedArrow, skill.MultipleShot:
				if cap.RangedCast == nil {
					v := b
					cap.RangedCast = &v
				}
			case skill.Throw:
				v := b
				cap.Throw = &v
			case skill.TomeOfTownPortal, skill.ScrollOfTownPortal:
				v := b
				cap.TownTP = &v
			case skill.TomeOfIdentify, skill.ScrollOfIdentify:
				v := b
				cap.Identify = &v
			}
			before = after
		} else {
			log.Info("capability: key selected nothing (unbound or unusable)", "key", name)
		}
	}
	mem.PutJSON("capability", memory.ScopeGame,
		memory.Provenance{Source: "measured", Evidence: "selection readback calibration"}, cap.Proven)
	return cap
}
