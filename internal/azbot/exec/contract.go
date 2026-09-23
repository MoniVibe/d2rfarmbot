// Package exec is the executive's pure core: the activity contract v2 types, the
// lifecycle driver that turns arbiter Changes into Begin/Suspend/End, the
// registry-order demand collector, the screen cadence and the published Reading.
//
// PURE: arbiter, phase, screen and stdlib only — the activity package imports
// internal/game (Windows), so everything testable on Linux lives here and the
// activity package supplies the concrete context type.
package exec

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// ModeSet is the screen modes an activity may act in. A set, not one Mode:
// Respawn and Relog act where World-only activities must not.
type ModeSet uint8

// AnyMode admits every mode (Relog's menus, Respawn until Dead is proven).
const AnyMode ModeSet = 0xFF

// ModeOf builds a set from modes.
func ModeOf(ms ...screen.Mode) ModeSet {
	var s ModeSet
	for _, m := range ms {
		s |= 1 << m
	}
	return s
}

// Allows reports whether m is in the set.
func (s ModeSet) Allows(m screen.Mode) bool { return s&(1<<m) != 0 }

func (s ModeSet) String() string {
	if s == AnyMode {
		return "any"
	}
	out := ""
	for _, m := range []screen.Mode{screen.Unknown, screen.World, screen.Loading, screen.Dead} {
		if s.Allows(m) {
			if out != "" {
				out += "|"
			}
			out += m.String()
		}
	}
	if out == "" {
		return "none"
	}
	return out
}

// CursorPolicy says what an activity tolerates riding the cursor.
type CursorPolicy uint8

const (
	CursorEmpty CursorPolicy = iota // a cursor item is foreign: wait for the Janitor
	CursorOwn                       // the activity picks items up itself (bag work)
	CursorAny                       // indifferent (menus, death screen)
)

func (c CursorPolicy) String() string {
	switch c {
	case CursorEmpty:
		return "empty"
	case CursorOwn:
		return "own"
	case CursorAny:
		return "any"
	}
	return "?"
}

// Needs is what the holder requires of the screen before it may Step. Claims are
// the panels it may open; they live only while it holds the grant.
type Needs struct {
	Mode   ModeSet
	Claims screen.Panel
	Cursor CursorPolicy
}

// Status is one Step's report. Long waits are V=Wait with WakeAt, never a sleep.
type Status struct {
	V        phase.Verdict
	Why      phase.Reason
	Phase    string       // current phase name ("" = the activity has none)
	Claim    screen.Panel // panels it opened and still owns
	Evidence string
	WakeAt   time.Time
}

// Life is the lifecycle half of the v2 contract, generic over the executive's
// context so the driver tests on Linux with fakes.
type Life[C any] interface {
	Name() string
	Begin(c C, resumed bool)
	Suspend(c C, why phase.Reason) // MoveStop only; no new clicks
	End(c C, v phase.Verdict, why phase.Reason)
}
