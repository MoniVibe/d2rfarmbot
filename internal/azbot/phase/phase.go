// Package phase gives every activity a typed, logged phase and honest verdicts.
// An activity embeds a Phaser over its own Stringer enum; every transition is one
// log line with its reason, and per-phase budgets are spent in HELD time (the
// arbiter's ledger), so preemption and gate blocks never burn them.
package phase

import (
	"fmt"
	"time"
)

// Verdict is what one Step reports to the executive.
type Verdict uint8

const (
	Running   Verdict = iota
	Wait              // parked until a wake time; no actuation
	Blocked           // can't act this tick (gate, precondition pending); held clock stops
	Done              // terminal: goal met
	Abandoned         // terminal: gave up, with a Reason
)

func (v Verdict) String() string {
	switch v {
	case Running:
		return "running"
	case Wait:
		return "wait"
	case Blocked:
		return "blocked"
	case Done:
		return "done"
	case Abandoned:
		return "abandoned"
	default:
		return "?"
	}
}

// Terminal reports whether the episode is over.
func (v Verdict) Terminal() bool { return v == Done || v == Abandoned }

// Reason is the typed "why" beside every verdict, suspend and transition.
type Reason uint8

const (
	NoReason     Reason = iota // unset — never a success by accident
	Completed                  // the goal was met
	NoTarget                   // nothing left to act on
	Unreachable                // the path/goal can't be reached
	Timebox                    // a held-time budget overran
	Deaf                       // the game ignored the input (no evidence after acting)
	Refused                    // the game answered no (vendor, gold, full inventory)
	Precondition               // a required state wasn't there (panel, item, mode)
	Outbid                     // same-class rival took the grant
	Preempted                  // higher class took the grant
	Mute                       // held the grant with no progress past the bar
	UIWedge                    // the screen is in a state the activity can't leave
	Judged                     // a monitor (watchdog, deadman) convicted it
)

func (r Reason) String() string {
	switch r {
	case NoReason:
		return "none"
	case Completed:
		return "completed"
	case NoTarget:
		return "no-target"
	case Unreachable:
		return "unreachable"
	case Timebox:
		return "timebox"
	case Deaf:
		return "deaf"
	case Refused:
		return "refused"
	case Precondition:
		return "precondition"
	case Outbid:
		return "outbid"
	case Preempted:
		return "preempted"
	case Mute:
		return "mute"
	case UIWedge:
		return "ui-wedge"
	case Judged:
		return "judged"
	default:
		return "?"
	}
}

// Phase is an activity's phase enum: comparable and printable.
type Phase interface {
	comparable
	String() string
}

// Clock is injectable so tests run on recorded time.
type Clock interface{ Now() time.Time }

// Transition is one recorded phase change.
type Transition[P Phase] struct {
	From, To P
	Why      string
	At       time.Time
	InPhase  time.Duration // wall time spent in From
	Held     time.Duration // held time at the transition
}

// Phaser tracks one activity's phase. The zero value is usable (silent, wall clock,
// no budgets) and starts in P's zero value.
type Phaser[P Phase] struct {
	Act   string               // activity name for the log line
	Log   func(line string)    // sink; nil = silent
	Held  func() time.Duration // held-time source (arbiter.Held(Act)); nil = last Overrun's heldNow
	Clock Clock                // nil = wall clock

	cur       P
	enteredAt time.Time
	heldAt    time.Duration // held time on entering cur
	lastHeld  time.Duration // freshest heldNow seen by Overrun
	budgets   map[P]time.Duration
	last      Transition[P]
	n         int
}

func (p *Phaser[P]) now() time.Time {
	if p.Clock == nil {
		return time.Now()
	}
	return p.Clock.Now()
}

func (p *Phaser[P]) held() time.Duration {
	if p.Held != nil {
		return p.Held()
	}
	return p.lastHeld
}

// Phase is the current phase.
func (p *Phaser[P]) Phase() P { return p.cur }

// InPhase is wall time in the current phase (0 before the first transition).
func (p *Phaser[P]) InPhase() time.Duration {
	if p.enteredAt.IsZero() {
		return 0
	}
	return p.now().Sub(p.enteredAt)
}

// Last is the most recent transition; ok is false before the first.
func (p *Phaser[P]) Last() (Transition[P], bool) { return p.last, p.n > 0 }

// Transitions counts transitions since the last Reset.
func (p *Phaser[P]) Transitions() int { return p.n }

// To moves to next and logs one line. Re-entering the current phase is a no-op:
// a retry must not refill the phase's budget.
func (p *Phaser[P]) To(next P, why string) {
	if next == p.cur {
		return
	}
	now, h := p.now(), p.held()
	t := Transition[P]{From: p.cur, To: next, Why: why, At: now, InPhase: p.InPhase(), Held: h}
	p.cur, p.enteredAt, p.heldAt = next, now, h
	p.last = t
	p.n++
	if p.Log != nil {
		p.Log(fmt.Sprintf("phase act=%s from=%s to=%s why=%q inPhase=%.1fs",
			p.Act, t.From, t.To, why, t.InPhase.Seconds()))
	}
}

// Budget caps the held time phase ph may spend per entry; d <= 0 removes the cap.
func (p *Phaser[P]) Budget(ph P, d time.Duration) {
	if d <= 0 {
		delete(p.budgets, ph)
		return
	}
	if p.budgets == nil {
		p.budgets = map[P]time.Duration{}
	}
	p.budgets[ph] = d
}

// Overrun reports whether the current phase has spent more than its budget of
// held time, and which phase that is. Call it every Step with the arbiter's
// Held for this activity; an overrun is Abandoned(Timebox).
func (p *Phaser[P]) Overrun(heldNow time.Duration) (bool, P) {
	p.lastHeld = heldNow
	b, ok := p.budgets[p.cur]
	return ok && heldNow-p.heldAt > b, p.cur
}

// Reset returns to P's zero phase for a new episode. Budgets are configuration
// and survive.
func (p *Phaser[P]) Reset() {
	var zero P
	p.cur, p.enteredAt, p.lastHeld = zero, time.Time{}, 0
	p.heldAt = p.held()
	p.last, p.n = Transition[P]{}, 0
}
