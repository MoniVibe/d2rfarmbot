// Package policy is the melee STRIKE POLICY as a pure function: which proven
// hand swings this tick. No game, no input, no clock of its own — the Fight
// activity feeds it what calibration proved and what the snapshot shows, and
// the answer is a role the caller maps onto a key and a mouse button.
//
// The owner's 2026-09-24 build (Fableboi, Barbarian ~26): the mod's Carnage is
// mapped to the LEFT mouse button and is the primary strike. Leap Attack (a
// right skill) is kept only as a gap-closer beyond melee reach; Double Swing
// (right) is the fallback when the left strike keeps producing no evidence.
// Without a proven left skill the pre-Carnage order stands unchanged.
package policy

import "time"

// MeleeReach is the contact distance (Chebyshev tiles) inside which a swing
// lands without closing — the activity's contactRange.
const MeleeReach = 3

// Kind is the strike the policy chose.
type Kind uint8

const (
	// Basic: no proven skill for this tick — the plain attack (key 0: a
	// hover-confirmed left click or SHIFT+left, whichever the path uses).
	Basic Kind = iota
	// Left: the proven non-basic LEFT skill (Carnage). Key 0, left button,
	// NO right-skill key press before it.
	Left
	// Leap: the proven Leap Attack right binding — gap-closer only.
	Leap
	// Swing: the proven Double Swing right binding.
	Swing
	// Combat: the generic calibrated/owner-declared right combat binding.
	Combat
	// Approach: the left skill is primary but the target is out of reach, the
	// hover oracle cannot confirm it, and no leap is ready — an unconfirmed
	// left strike would swing air (SHIFT) or walk (bare click), so close the
	// gap on foot and swing when in reach.
	Approach
)

func (k Kind) String() string {
	switch k {
	case Left:
		return "left"
	case Leap:
		return "leap"
	case Swing:
		return "swing"
	case Combat:
		return "combat"
	case Approach:
		return "approach"
	default:
		return "basic"
	}
}

// Mouse is the button the strike fires on ("left" or "right"; "" for Approach).
func (k Kind) Mouse() string {
	switch k {
	case Leap, Swing, Combat:
		return "right"
	case Left, Basic:
		return "left"
	default:
		return ""
	}
}

// Inputs is everything the policy reads. All bindings are PROVEN ones.
type Inputs struct {
	LeftProven  bool // a non-basic left skill is proven (or forced by -leftskill)
	LeftBenched bool // the evidence audit benched the left skill (LeftAudit.Benched)
	LeapProven  bool // Leap Attack right binding
	SwingProven bool // Double Swing right binding
	// CombatProven: a generic right combat binding exists; CombatIsLeap marks
	// the case where that binding IS Leap Attack (then it obeys the leap gates).
	CombatProven bool
	CombatIsLeap bool
	Dist         int  // Chebyshev tiles to the nearest thing we would strike
	MPPct        int  // mana percent
	LeapReady    bool // the pool can afford a Leap Attack (caller's mana gate)
	LeapBlocked  bool // the pool is spoken for (movement leap hunger) or context forbids
	HoverOK      bool // a hover-confirmed aimed strike is possible this tick
}

// Choose is the strike policy.
//
//   - Left proven and not benched: Carnage is the primary strike at any range
//     it can land — in reach always; beyond reach only when the aimed (hover)
//     strike can confirm the monster, because the game's own attack command
//     then closes and swings. Beyond reach, a ready Leap Attack closes the gap
//     first. With neither, Approach.
//   - Otherwise (no left skill, or benched): the pre-Carnage order — Leap
//     beyond reach, Double Swing above 10% mana, the generic binding, basic.
func Choose(in Inputs) Kind {
	far := in.Dist > MeleeReach
	leapOK := far && !in.LeapBlocked && in.LeapReady && in.LeapProven
	if in.LeftProven && !in.LeftBenched {
		if leapOK {
			return Leap
		}
		if far && !in.HoverOK {
			return Approach
		}
		return Left
	}
	if leapOK {
		return Leap
	}
	if in.SwingProven && in.MPPct > 10 {
		return Swing
	}
	if in.CombatProven {
		if in.CombatIsLeap && !leapOK {
			return Basic // never re-issue Leap Attack the gates just rejected
		}
		return Combat
	}
	return Basic
}

// LeftAudit is the left skill's evidence audit (the flinch audit's left-hand
// twin): consecutive left strikes that produced no evidence bench it for a
// while so Double Swing carries the fight, then it re-arms and re-proves.
type LeftAudit struct {
	Deaf       int       // consecutive left strikes resolved without evidence
	BenchUntil time.Time // benched while now < BenchUntil
}

// DeafLimit silent left strikes in a row bench the left skill for BenchFor.
const (
	DeafLimit = 6
	BenchFor  = 20 * time.Second
)

// Resolve records one resolved LEFT strike: evidence=true clears the run, a
// silent one extends it and benches at DeafLimit. Reports whether this call
// benched the skill (the caller writes it to the ledger).
func (a *LeftAudit) Resolve(evidence bool, now time.Time) bool {
	if evidence {
		a.Deaf = 0
		return false
	}
	a.Deaf++
	if a.Deaf >= DeafLimit {
		a.Deaf = 0
		a.BenchUntil = now.Add(BenchFor)
		return true
	}
	return false
}

// Benched reports whether the left skill is benched at now.
func (a *LeftAudit) Benched(now time.Time) bool { return now.Before(a.BenchUntil) }
