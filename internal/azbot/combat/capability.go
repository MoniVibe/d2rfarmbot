// Package combat: the self-model's combat surface. Capability is SELF-CALIBRATING:
// a key binding is believed only after pressing it and reading the selection back —
// the KeyBindings memory block is dead on this repack, and flags are only claims.
// "The bot needs to know and be aware of itself" — this is where it learns what it
// can actually do.
package combat

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/game"
)

// Binding is one PROVEN key→skill mapping.
type Binding struct {
	Key   byte     `json:"key"`
	Skill skill.ID `json:"skill"`
}

// LeftBinding is the LEFT mouse skill as the game reports it. Nothing is
// pressed to learn it: PlayerUnit.LeftSkill is read straight from memory. The
// owner maps the left skill by hand (2026-09-24: the mod's Carnage on
// Fableboi), so the bot's job is to SEE it, never to select it.
type LeftBinding struct {
	Skill skill.ID `json:"skill"`
	Name  string   `json:"name"`  // the local d2go table's name, or "skill#<id>" when it has none
	Mouse string   `json:"mouse"` // always "left" — the marker the strike telemetry prints
	// Proven: LeftSkill is not the basic Attack AND the skill is present in the
	// live Skills map (she owns it; a stale or scrambled id reads as absent).
	Proven bool `json:"proven"`
	// Forced: the owner's -leftskill=on declared it without calibration's proof.
	Forced bool `json:"forced,omitempty"`
	// Disabled: the owner's -leftskill=off. The binding stays so telemetry
	// still names what a plain (key 0) left strike fires.
	Disabled bool `json:"disabled,omitempty"`
}

// Primary reports whether the left skill is the fight's primary strike.
func (l *LeftBinding) Primary() bool { return l != nil && !l.Disabled && (l.Proven || l.Forced) }

// ReadLeft reads the live left skill and judges it: proven when it is not
// the basic Attack and the skill is present in the Skills map.
func ReadLeft(pu data.PlayerUnit) *LeftBinding {
	id := pu.LeftSkill
	_, owned := pu.Skills[id]
	return &LeftBinding{Skill: id, Name: SkillName(id), Mouse: "left",
		Proven: id != skill.AttackSkill && owned}
}

// Capability is what the character can provably do right now. Role fields
// speak the Lexicon (P-7.5): CONTACT and REACH are roles seeded by priors,
// not weapon names — the wardrobe defines the class, not the reverse.
type Capability struct {
	Known   map[skill.ID]int // skills present in the live Skills map (level; 0 = granted)
	Proven  []Binding        // key presses that demonstrably flipped RightSkill
	Contact *Binding         // CONTACT TOOL seed: proven strike selection
	// LeapAttack is the preferred damage skill for the current Barbarian spec.
	// DoubleSwing is the low-mana fallback; Contact remains the generic/travel
	// binding so a combat leap is never accidentally used as a movement gait.
	LeapAttack  *Binding
	DoubleSwing *Binding
	Combat      *Binding
	Throw       *Binding  // proven throw selection (reach with an ammo gauge)
	Reach       *Binding  // REACH TOOL seed: proven projected shot/cast selection
	TownTP      *Binding  // proven town portal selection
	Identify    *Binding  // proven identify selection
	Vault       *Binding  // proven cursor-targeted displacement (Leap-family)
	Trap        *Binding  // proven ground-placed sentry (assassin traps)
	Buffs       []Binding // proven self-buffs with a readable state (BuffState)
	// Left is the live LEFT mouse skill (read, not pressed). Left.Primary()
	// makes it the fight's primary strike; Leap Attack is then a gap-closer
	// and Double Swing the no-evidence fallback (combat/policy).
	Left *LeftBinding
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
	// THE LEFT HAND FIRST, before any key is pressed: a candidate key bound to
	// a left skill would repaint LeftSkill, and the owner's mapping is the one
	// he left on the button.
	cap.Left = ReadLeft(d.PlayerUnit)
	log.Info(fmt.Sprintf("capability: left skill %s (id %d)", cap.Left.Name, int(cap.Left.Skill)),
		"skill", int(cap.Left.Skill), "skill_name", cap.Left.Name, "mouse", "left", "proven", cap.Left.Proven)
	leftKeys := map[skill.ID]byte{} // keys observed to repaint the LEFT skill
	leftBefore := d.PlayerUnit.LeftSkill
	before := d.PlayerUnit.RightSkill
	initial := before
	// THE SELF-SELECTION BLIND SPOT (relay R9, 08:13:36): a key bound to the
	// skill ALREADY selected on the right flips nothing — F1 read "selected
	// nothing" with Book of Townportal armed from the last session, and the
	// recalibration two seconds later (selection then on Leap Attack) proved
	// F1 = Book of Townportal. Silent keys get a second look below once the
	// selection has moved off the initial skill.
	var silent []string
	probe := func(name string, second bool) {
		key := hid.GetASCIICode(name)
		hid.PressKey(key)
		time.Sleep(120 * time.Millisecond)
		pu := gr.GetData().PlayerUnit
		after := pu.RightSkill
		if pu.LeftSkill != leftBefore {
			leftKeys[pu.LeftSkill] = key
			log.Warn("capability: key repainted the LEFT skill", "key", name,
				"from", int(leftBefore), "to", int(pu.LeftSkill), "skill_name", SkillName(pu.LeftSkill))
			leftBefore = pu.LeftSkill
		}
		if after != before {
			if second {
				log.Info("capability: second look — the key selects the skill that was armed at start",
					"key", name, "skill", int(after), "skill_name", skillName(after))
			}
			b := Binding{Key: key, Skill: after}
			cap.Proven = append(cap.Proven, b)
			log.Info("capability: proven binding", "key", name, "skill", int(after), "skill_name", skillName(after))
			// Match the live table by its name as well as by the enum. The local
			// d2go build's generated enum and skills table do not share positions
			// for every class skill, so numeric constants alone are unsafe here.
			switch canonicalSkillName(skillName(after)) {
			case "leapattack":
				v := b
				cap.LeapAttack = &v
			case "doubleswing":
				v := b
				cap.DoubleSwing = &v
			}
			// The SELECTION FLIP is itself behavioral proof she owns the skill (the mod
			// grants some at level 0 — a points requirement wrongly disarmed Magic
			// Arrow, measured 03:14). Fight's flinch audit is the guard against a
			// scrambled table: a "skill" that never hurts anyone gets demoted live.
			// P-7.2/P-7.3: the prior table seeds the role; no skill ID is named here.
			switch Prior(after) {
			case RoleContact:
				if cap.Contact == nil {
					v := b
					cap.Contact = &v
				}
			case RoleReach:
				if cap.Reach == nil {
					v := b
					cap.Reach = &v
				}
			case RoleThrow:
				v := b
				cap.Throw = &v
			case RoleTownTP:
				v := b
				cap.TownTP = &v
			case RoleIdentify:
				v := b
				cap.Identify = &v
			case RoleVault:
				v := b
				cap.Vault = &v
			case RoleBuff:
				cap.Buffs = append(cap.Buffs, b)
			case RoleTrap:
				if cap.Trap == nil {
					v := b
					cap.Trap = &v
				}
			}
			before = after
		} else if !second {
			silent = append(silent, name)
			log.Info("capability: key selected nothing (unbound or unusable)", "key", name)
		}
	}
	for _, name := range candidates {
		probe(name, false)
	}
	if NeedSecondLook(initial, before, cap.Proven) {
		for _, name := range silent {
			probe(name, true)
		}
	}
	// Keep travel on an ordinary contact skill, but give combat the requested
	// priority: Leap Attack first, Double Swing second, then the generic contact
	// binding discovered by calibration.
	if cap.DoubleSwing != nil {
		cap.Contact = cap.DoubleSwing
	}
	switch {
	case cap.LeapAttack != nil:
		v := *cap.LeapAttack
		cap.Combat = &v
	case cap.DoubleSwing != nil:
		v := *cap.DoubleSwing
		cap.Combat = &v
	case cap.Contact != nil:
		v := *cap.Contact
		cap.Combat = &v
	}
	// Put the owner's left skill back if probing moved it (possible only when
	// a probed key is bound to a left skill and another probed key restores it).
	if cur := gr.GetData().PlayerUnit.LeftSkill; cur != cap.Left.Skill {
		if k, ok := leftKeys[cap.Left.Skill]; ok {
			hid.PressKey(k)
			time.Sleep(120 * time.Millisecond)
			cur = gr.GetData().PlayerUnit.LeftSkill
		}
		if cur != cap.Left.Skill {
			log.Warn("capability: probing left the LEFT skill changed — the primary strike follows the live skill",
				"was", int(cap.Left.Skill), "now", int(cur), "now_name", SkillName(cur))
			cap.Left = ReadLeft(gr.GetData().PlayerUnit)
		}
	}
	if cap.Combat != nil {
		log.Info("capability: combat binding", "skill", int(cap.Combat.Skill),
			"skill_name", skillName(cap.Combat.Skill), "key", int(cap.Combat.Key))
	}
	mem.PutJSON("capability", memory.ScopeGame,
		memory.Provenance{Source: "measured", Evidence: "selection readback calibration"}, cap.Proven)
	return cap
}

func skillName(id skill.ID) string { return SkillName(id) }

// SkillName names a skill by the local d2go table, falling back to the enum's
// name and finally to "skill#<id>" — the mod's own skills (Carnage) may be
// absent from both; the ID is the stable coordinate.
func SkillName(id skill.ID) string {
	if def, ok := skill.Skills[id]; ok && def.Name != "" {
		return def.Name
	}
	if n := skill.SkillNames[id]; n != "" {
		return n
	}
	return fmt.Sprintf("skill#%d", int(id))
}
