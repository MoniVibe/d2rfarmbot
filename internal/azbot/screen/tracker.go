package screen

import (
	"fmt"
	"strings"
	"time"
)

// Transition is one believed screen change, ready for the log:
// "screen: world -> world+inventory (right-half panel)".
type Transition struct {
	At       time.Time
	From, To State
	Evidence string
}

func (t Transition) String() string {
	s := fmt.Sprintf("screen: %s -> %s", t.From, t.To)
	if t.Evidence != "" {
		s += " (" + t.Evidence + ")"
	}
	return s
}

// DefaultAgree: consecutive agreeing observations before a belief flips. One
// frame lies (a panel's slide-in, a fade, a capture mid-redraw); two agreeing
// frames at the executive's cadence do not.
const DefaultAgree = 2

// Tracker debounces Readings into a believed State. Each panel bit, the mode
// and the cursor flip only after N consecutive observations disagree with the
// belief. An Unsure panel casts no vote — blindness neither confirms nor
// refutes. Pure: time comes in with each observation.
type Tracker struct {
	N int // 0 means DefaultAgree

	cur        State
	since      time.Time
	bitStreak  [32]int
	modeCand   Mode
	modeStreak int
	curStreak  int
}

// NewTracker starts believing nothing (Unknown, no panels).
func NewTracker(n int) *Tracker { return &Tracker{N: n} }

func (t *Tracker) need() int {
	if t.N <= 0 {
		return DefaultAgree
	}
	return t.N
}

// State is the current belief.
func (t *Tracker) State() State { return t.cur }

// Since: when the current belief last changed (zero before the first change).
func (t *Tracker) Since() time.Time { return t.since }

// Update folds one observation in; it returns the transition when the belief
// changed on this observation.
func (t *Tracker) Update(at time.Time, r Reading) (Transition, bool) {
	n := t.need()
	next := t.cur
	var ev []string

	if r.Mode != t.cur.Mode {
		if r.Mode == t.modeCand && t.modeStreak > 0 {
			t.modeStreak++
		} else {
			t.modeCand, t.modeStreak = r.Mode, 1
		}
		if t.modeStreak >= n {
			next.Mode = r.Mode
			t.modeStreak = 0
		}
	} else {
		t.modeStreak = 0
	}

	for i, q := range All {
		seen := r.Panels&q != 0
		held := t.cur.Panels&q != 0
		if !seen && r.Unsure&q != 0 {
			continue // no vote
		}
		if seen == held {
			t.bitStreak[i] = 0
			continue
		}
		t.bitStreak[i]++
		if t.bitStreak[i] < n {
			continue
		}
		t.bitStreak[i] = 0
		if seen {
			next.Panels |= q
			ev = append(ev, r.Evidence[q])
		} else {
			next.Panels &^= q
			ev = append(ev, panelNames[q]+" gone")
		}
	}

	if r.CursorItem != t.cur.CursorItem {
		t.curStreak++
		if t.curStreak >= n {
			t.curStreak = 0
			next.CursorItem = r.CursorItem
			if r.CursorItem {
				ev = append(ev, "cursor item")
			} else {
				ev = append(ev, "cursor empty")
			}
		}
	} else {
		t.curStreak = 0
	}

	if next == t.cur {
		return Transition{}, false
	}
	tr := Transition{At: at, From: t.cur, To: next, Evidence: strings.Join(nonEmpty(ev), "; ")}
	t.cur, t.since = next, at
	return tr, true
}

func nonEmpty(ss []string) []string {
	out := ss[:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
