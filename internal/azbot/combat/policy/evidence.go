package policy

import "github.com/hectorgimenez/d2go/pkg/data/mode"

// Verdict is one open strike's judgement against a tick's observation.
type Verdict uint8

const (
	// Pending: no evidence yet and the window is still open.
	Pending Verdict = iota
	// Hit: the struck unit flinched, was knocked back, or died.
	Hit
	// Deaf: the window closed with no answer from the struck unit.
	Deaf
	// Overkill: the unit's death was already credited to an EARLIER strike —
	// this swing met a body that was dying or dead. Neither evidence nor
	// silence: the left audit must not count it against the skill.
	Overkill
)

func (v Verdict) String() string {
	switch v {
	case Hit:
		return "hit"
	case Deaf:
		return "deaf"
	case Overkill:
		return "overkill"
	default:
		return "pending"
	}
}

// Seen is what one tick shows about the unit an open strike was aimed at.
type Seen struct {
	Present     bool   // still among the live (non-dead) enemies
	Mode        uint32 // its mode now (Present only)
	PrevMode    uint32 // its mode when the strike last looked (edge detection)
	FlinchTaken bool   // this tick's flinch edge on the unit already credited another strike
	Corpse      bool   // it is in Death/Dead mode in the raw monster list
	DeathTaken  bool   // an earlier strike already took this unit's death
	InReach     bool   // the strike was thrown at d ≤ MeleeReach
	Expired     bool   // the strike's evidence window has closed
}

// Judge is the strike-evidence rule (relay R9 measured what the old one
// missed: of Concentrate's 36 "deaf" strikes 20 hit a unit that another strike
// had just been credited with, and 38 of the deaf leaps' targets were already
// gone from the live list at the next snapshot — kills the corpse check never
// saw):
//
//   - live unit: a rising edge into GettingHit OR KnockedBack is a hit (one
//     edge credits one strike);
//   - gone from the live list: a death already credited → Overkill at once; a
//     corpse → Hit; vanished without a corpse after an IN-REACH strike →
//     Hit (the live list only drops a unit when its Life or Mode says dead;
//     monsters do not despawn mid-fight);
//   - otherwise Deaf once the window closes, Pending before.
func Judge(s Seen) Verdict {
	if s.Present {
		m := s.Mode
		if (m == uint32(mode.NpcGettingHit) || m == uint32(mode.NpcKnockedBack)) && m != s.PrevMode && !s.FlinchTaken {
			return Hit
		}
	} else {
		switch {
		case s.DeathTaken:
			return Overkill
		case s.Corpse, s.InReach:
			return Hit
		}
	}
	if s.Expired {
		return Deaf
	}
	return Pending
}
