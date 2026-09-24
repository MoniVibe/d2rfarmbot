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
// lands without closing — the activity's contactRange. Relay R9 kept it: the
// left strike credited 13 of 21 swings at d=3 against 19 of 42 at d=2 (most
// d=2 silences were a second swing at a body another swing had just killed);
// nothing supports a longer reach, and a SHIFT+left at d=4 is a swing at air.
const MeleeReach = 3

// THE LEAP GATES (relay R9, 2026-09-24: Leap Attack 132 strikes vs the primary
// Concentrate/Carnage 84; 84 of the 132 leaps were at d=4-5 — one or two tiles
// beyond reach — 83 were followed straight by ANOTHER leap at a target still
// beyond reach (median 0.87s apart), 43 were fired from inside a pack of 3+
// within 5 tiles, 66% resolved deaf, and 43 of the 53 locks that ended without
// a kill had seen nothing but leaps). Leap Attack stays the gap-closer, but
// only for a real gap, one at a time, and never out of a pack we already stand
// in: short gaps are closed on foot (the pack walks into Carnage's reach anyway).
const (
	// LeapMinGap: the smallest gap (Chebyshev tiles) worth a leap. A 4-5 tile
	// gap is a step or two — walk it (or let the aimed left strike's attack
	// command close it) and swing.
	LeapMinGap = 6
	// LeapCooldown: after a leap, no second leap for this long — the landing
	// is followed by swings, not by a chain of leaps after moving targets.
	LeapCooldown = 2 * time.Second
	// PackRadius / PackHold: PackHold or more sighted enemies within
	// PackRadius of us is a pack we are already in — they come to us; a leap
	// out of it only trades the reach we have for a landing among others.
	PackRadius = 5
	PackHold   = 3
)

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
	// The leap gates' inputs (zero values = no veto beyond reach and gap).
	LeapCooling bool // a Leap Attack fired less than LeapCooldown ago (LeapClock)
	PackNear    int  // sighted enemies within PackRadius of us
	LineWalled  bool // the straight line to the target crosses unwalkable ground
}

// LeapVeto reports why a Leap Attack may NOT fire this tick ("" = it may).
// It is the single definition of the leap gates: Choose obeys it.
func LeapVeto(in Inputs) string {
	switch {
	case !in.LeapProven:
		return "no proven Leap Attack"
	case in.Dist <= MeleeReach:
		return "in reach"
	case in.Dist < LeapMinGap:
		return "short gap: walk it"
	case in.LeapBlocked:
		return "blocked (movement leap hunger or context)"
	case !in.LeapReady:
		return "mana"
	case in.LeapCooling:
		return "cooldown"
	case in.PackNear >= PackHold:
		return "inside a pack"
	case in.LineWalled:
		return "walled line"
	}
	return ""
}

// Choose is the strike policy.
//
//   - Left proven and not benched: Carnage is the primary strike at any range
//     it can land — in reach always; beyond reach only when the aimed (hover)
//     strike can confirm the monster, because the game's own attack command
//     then closes and swings. Beyond reach, a Leap Attack that passes every
//     leap gate (LeapVeto) closes the gap first. With neither, Approach.
//   - Otherwise (no left skill, or benched): the pre-Carnage order — Leap
//     (gated) beyond reach, Double Swing above 10% mana, the generic binding,
//     basic.
func Choose(in Inputs) Kind {
	far := in.Dist > MeleeReach
	leapOK := LeapVeto(in) == ""
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

// LeapClock is the leap cooldown's memory: when the last Leap Attack went out.
type LeapClock struct{ last time.Time }

// Fired stamps a Leap Attack issued at now.
func (c *LeapClock) Fired(now time.Time) { c.last = now }

// Cooling reports whether a leap fired less than LeapCooldown before now.
func (c *LeapClock) Cooling(now time.Time) bool {
	return !c.last.IsZero() && now.Sub(c.last) < LeapCooldown
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
