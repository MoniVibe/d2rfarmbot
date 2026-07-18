// Package verbs implements azbot's actuation contract layer: LAW 3 — silence is never
// success. Every verb carries preconditions, a hard budget, and a typed postcondition;
// every terminal state appends an Outcome to the ledger. There is no fire-and-forget
// input anywhere above this package.
package verbs

import (
	"sync"
	"time"
)

type Result uint8

const (
	ResDone    Result = iota
	ResDeaf           // input registered nothing observable (the corpse-click class)
	ResBlocked        // effect started, then obstructed
	ResWhiff          // aimed act missed its target identity
	ResTimeout        // expectation window expired
	ResRefused        // precondition failed; the verb never fired
)

func (r Result) String() string {
	switch r {
	case ResDone:
		return "done"
	case ResDeaf:
		return "deaf"
	case ResBlocked:
		return "blocked"
	case ResWhiff:
		return "whiff"
	case ResTimeout:
		return "timeout"
	default:
		return "refused"
	}
}

// Outcome is the structured record of one verb execution. Blacklists, wedges,
// calibration suspicion, and the nightly report all consume this — one substrate.
type Outcome struct {
	Verb     string    `json:"verb"`
	Holder   string    `json:"holder"`
	Target   string    `json:"target"` // unit ref / cell / screen point, stringified
	Result   Result    `json:"result"`
	Evidence string    `json:"evidence"`
	HeldMS   int64     `json:"held_ms"`
	At       time.Time `json:"at"`
	// AimDX/AimDY: the sweep offset that confirmed the hover — fed back to the
	// caller as the next shot's hint (volley aim tracking). Not ledger-worthy.
	AimDX int `json:"-"`
	AimDY int `json:"-"`
}

// Ledger is an in-memory ring + sink hook. The memory store persists what needs
// scope>tick; the ring feeds in-band consumers (blacklists, reports).
type Ledger struct {
	mu   sync.Mutex
	ring []Outcome
	max  int
	Sink func(Outcome) // optional; called synchronously under no lock guarantees
}

func NewLedger(max int) *Ledger { return &Ledger{max: max} }

func (l *Ledger) Append(o Outcome) {
	if o.At.IsZero() {
		o.At = time.Now()
	}
	l.mu.Lock()
	l.ring = append(l.ring, o)
	if len(l.ring) > l.max {
		l.ring = l.ring[len(l.ring)-l.max:]
	}
	sink := l.Sink
	l.mu.Unlock()
	if sink != nil {
		sink(o)
	}
}

// Recent returns up to n most recent outcomes, newest last.
func (l *Ledger) Recent(n int) []Outcome {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.ring) {
		n = len(l.ring)
	}
	out := make([]Outcome, n)
	copy(out, l.ring[len(l.ring)-n:])
	return out
}
