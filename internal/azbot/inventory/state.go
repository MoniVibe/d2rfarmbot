package inventory

import (
	"fmt"
	"time"
)

// ---------------------------------------------------------------- named states
//
// OWNER (2026-09-24): "can we also let it know states like 'inventory open' or
// 'item held' or 'inventory swap loop' for when it gets stuck ... so it knows how
// to clear potential stuck states". One named state per tick, from memory (cursor,
// bag) and sight (which panels stand); a detector over the recent history names
// the stuck states; each stuck state has ONE prescribed way out (Rx).

// Phase is the inventory's surface state.
type Phase uint8

const (
	Idle       Phase = iota // nothing open, nothing held
	BagOpen                 // the bag stands
	StashOpen               // the stash (and the bag with it)
	VendorOpen              // a trade window (and the bag)
	ItemHeld                // something rides the cursor
)

func (p Phase) String() string {
	return [...]string{"idle", "bag open", "stash open", "vendor open", "item held"}[p]
}

// Stuck is a named stuck state; StuckNone when things are moving.
type Stuck uint8

const (
	StuckNone      Stuck = iota
	StuckHeld            // an item rode the cursor longer than HeldLimit
	StuckSwapLoop        // the same item bounced bag<->cursor SwapLoopN times in SwapWindow
	StuckNoEffect        // the same move was issued NoEffectN times without memory changing
	StuckPanelIdle       // a panel stands with nothing planned for PanelIdleLimit
)

func (s Stuck) String() string {
	return [...]string{"-", "item held too long", "inventory swap loop", "move not taking", "panel open, nothing to do"}[s]
}

// Rx is the prescribed way out of a stuck state.
type Rx uint8

const (
	RxNone       Rx = iota
	RxParkOrShed    // held item: park it in a free bag cell; if none, drop junk (field) / stash or sell (town)
	RxQuarantine    // loop / no effect: bench that item's moves for QuarantineFor, log it
	RxClosePanel    // idle panel: close by sight
)

func (r Rx) String() string {
	return [...]string{"-", "park it, or shed it (drop junk / stash / sell)", "quarantine that item's moves", "close the panel by sight"}[r]
}

// Limits (measured against the loops seen in R24-R31).
const (
	HeldLimit      = 4 * time.Second
	SwapLoopN      = 3
	SwapWindow     = 30 * time.Second
	NoEffectN      = 3
	PanelIdleLimit = 6 * time.Second
	QuarantineFor  = 10 * time.Minute
)

// Observation is what the executor reports each tick.
type Observation struct {
	At         time.Time
	Held       uint32 // unit on the cursor (0 = none)
	BagOpen    bool
	StashOpen  bool
	VendorOpen bool
	Planned    bool // the planner has a move for this state
}

// Tracker keeps the short history the stuck states are named from.
type Tracker struct {
	phase      Phase
	heldUnit   uint32
	heldSince  time.Time
	lifts      map[uint32][]time.Time // cursor pick-ups per unit
	lastMove   string
	lastUnit   uint32
	sameMoves  int
	panelSince time.Time
	quarantine map[uint32]time.Time
}

func NewTracker() *Tracker {
	return &Tracker{lifts: map[uint32][]time.Time{}, quarantine: map[uint32]time.Time{}}
}

// Phase is the current surface state.
func (t *Tracker) Phase() Phase { return t.phase }

// Observe updates the state from one tick and names any stuck state with its Rx.
func (t *Tracker) Observe(o Observation) (Phase, Stuck, Rx, string) {
	switch {
	case o.Held != 0:
		t.phase = ItemHeld
	case o.StashOpen:
		t.phase = StashOpen
	case o.VendorOpen:
		t.phase = VendorOpen
	case o.BagOpen:
		t.phase = BagOpen
	default:
		t.phase = Idle
	}
	// held item: when it started riding, and a lift counted per unit
	if o.Held != t.heldUnit {
		if o.Held != 0 {
			t.heldSince = o.At
			t.lifts[o.Held] = append(pruned(t.lifts[o.Held], o.At), o.At)
		}
		t.heldUnit = o.Held
	}
	if o.Held != 0 {
		if n := len(pruned(t.lifts[o.Held], o.At)); n >= SwapLoopN {
			return t.phase, StuckSwapLoop, RxQuarantine, fmt.Sprintf("unit %d lifted %d times in %s", o.Held, n, SwapWindow)
		}
		if held := o.At.Sub(t.heldSince); held > HeldLimit {
			return t.phase, StuckHeld, RxParkOrShed, fmt.Sprintf("unit %d on the cursor %s", o.Held, held.Round(time.Second))
		}
	}
	if t.sameMoves >= NoEffectN {
		return t.phase, StuckNoEffect, RxQuarantine, fmt.Sprintf("%q issued %d times with no change", t.lastMove, t.sameMoves)
	}
	// a panel standing with nothing to do
	if (t.phase == BagOpen || t.phase == StashOpen || t.phase == VendorOpen) && !o.Planned {
		if t.panelSince.IsZero() {
			t.panelSince = o.At
		} else if o.At.Sub(t.panelSince) > PanelIdleLimit {
			return t.phase, StuckPanelIdle, RxClosePanel, t.phase.String() + " with nothing planned"
		}
	} else {
		t.panelSince = time.Time{}
	}
	return t.phase, StuckNone, RxNone, ""
}

// Issued records a move the executor sent; changed=true when memory moved since
// the previous move (the move took), which resets the no-effect count.
func (t *Tracker) Issued(key string, unit uint32, changed bool) {
	t.lastUnit = unit
	if key == t.lastMove && !changed {
		t.sameMoves++
		return
	}
	t.lastMove, t.sameMoves = key, 1
}

// StuckUnit is the unit a stuck state concerns: the held one, else the last moved.
func (t *Tracker) StuckUnit() uint32 {
	if t.heldUnit != 0 {
		return t.heldUnit
	}
	return t.lastUnit
}

// Quarantine benches a unit's moves; Quarantined reports it.
func (t *Tracker) Quarantine(u uint32, now time.Time) {
	t.quarantine[u] = now.Add(QuarantineFor)
	delete(t.lifts, u)
	t.sameMoves = 0
}

func (t *Tracker) Quarantined(u uint32, now time.Time) bool {
	until, ok := t.quarantine[u]
	return ok && now.Before(until)
}

func pruned(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, x := range ts {
		if now.Sub(x) <= SwapWindow {
			out = append(out, x)
		}
	}
	return out
}
