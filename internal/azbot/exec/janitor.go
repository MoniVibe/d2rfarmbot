package exec

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// The Janitor and the gate (docs/AZBOT_V2.md step 6, executive tick step 6):
// before the holder Steps, its Needs are checked against the stable Reading. A
// foreign panel or a foreign cursor item earns ONE janitor action this tick and
// the holder does not Step (its held clock is paused). Everything here is pure
// policy; the executive owns the motor.
//
// ONLY POSITIVE SIGHTINGS COUNT. Most panel detectors are still stubs that
// answer Unsure on every frame; were Unsure foreign, every holder would freeze
// forever. A panel is foreign only when the capture SAW it (Reading.Sight) or a
// memory channel proven for exactly that panel asserts it (the 0xF4 NPC-menu
// byte, screen.MemoryProven) — and the holder does not claim it.

// HolderNeeds is the gate's activity-agnostic view of the holder: which modes it
// may act in, which panels it owns while it holds, and whether a cursor item is
// its own business (bag work) or foreign.
type HolderNeeds struct {
	Mode      ModeSet
	Claims    screen.Panel
	CursorOwn bool
}

// Holder is n seen by the gate. CursorAny (indifferent) counts as owning the
// cursor: only CursorEmpty holders need it cleared.
func (n Needs) Holder() HolderNeeds {
	return HolderNeeds{Mode: n.Mode, Claims: n.Claims, CursorOwn: n.Cursor != CursorEmpty}
}

// NoHolder is the gate's view when nobody holds the grant (startup, idle): any
// mode, no claims — every seen panel is foreign — and the cursor left alone
// (there is no one to block, and the next holder's own Needs judge the item).
var NoHolder = HolderNeeds{Mode: AnyMode, CursorOwn: true}

// GateResult is one gate judgment.
type GateResult struct {
	Open    bool          // the holder may Step
	Action  screen.Action // janitor step toward a clear screen (ActClick/ActKey), else ActNone/ActUnknown
	Drop    bool          // cursor rule: one plain click on the ground at her feet
	Foreign screen.Panel  // positively observed blocking panels the holder does not claim
	Cursor  bool          // a foreign item rides the cursor
	Why     string
}

// Acts reports whether the result asks the motor for something.
func (g GateResult) Acts() bool {
	return g.Drop || g.Action.Kind == screen.ActClick || g.Action.Kind == screen.ActKey
}

// Blocked is the state line's gate= spelling: "ok", or "blocked(<why>)".
func (g GateResult) Blocked() string {
	if g.Open {
		return "ok"
	}
	switch {
	case g.Foreign != 0 && g.Cursor:
		return "blocked(" + g.Foreign.String() + "+cursor)"
	case g.Foreign != 0:
		return "blocked(" + g.Foreign.String() + ")"
	case g.Cursor:
		return "blocked(cursor)"
	}
	return "blocked(" + g.Why + ")"
}

// Positive is what the reading positively observed eating input: sight, plus a
// proven memory channel. Unsure never enters; the Automap eats nothing.
func Positive(r screen.Reading) screen.Panel {
	return (r.Sight | (r.Panels & screen.MemoryProven)) &^ screen.Automap
}

// bag: panels a held item could be parked into (no free-cell measure yet).
const bag = screen.Inventory | screen.RightPanel

// Gate judges the holder's Needs against a (stable) Reading.
//
// Order: the mode gate first (nothing acts for a holder in the wrong mode);
// then a foreign cursor item over an open bag is LEFT (parking needs a free-cell
// measure the janitor does not have; a blind click there could land in the
// grid, an ESC could take the item with the panel); then foreign panels, closed
// by screen.CloseStep restricted to them; then a foreign cursor item on clear
// ground is dropped at her feet. Never an ESC for the cursor.
func Gate(r screen.Reading, n HolderNeeds) GateResult {
	if !n.Mode.Allows(r.Mode) {
		return GateResult{Why: "mode " + r.Mode.String() + " (holder needs " + n.Mode.String() + ")"}
	}
	foreign := Positive(r) &^ n.Claims
	cursor := r.CursorItem && !n.CursorOwn
	g := GateResult{Foreign: foreign, Cursor: cursor}
	if foreign == 0 && !cursor {
		g.Open, g.Why = true, "clear"
		return g
	}
	if b := r.Sight & bag; cursor && b != 0 {
		g.Why = "cursor item over open " + b.String() + ": left (no free-cell measure)"
		return g
	}
	if foreign != 0 {
		// CloseStep over the foreign panels only: a claimed panel is never a
		// click target — but a pause menu or sub-panel that is up (sanctioned or
		// claimed) still withholds the ESC, which would toggle it.
		q := r
		q.Sight = r.Sight & foreign
		q.Panels = r.Panels & (foreign | screen.PauseMenu | screen.SubPanel)
		q.Unsure = 0
		g.Action = screen.CloseStep(q)
		g.Why = "foreign: " + foreign.String()
		if !g.Acts() {
			g.Why += " (" + g.Action.Reason + ")"
		}
		if cursor {
			g.Why += "; cursor item waits"
		}
		return g
	}
	if r.Mode != screen.World {
		g.Why = "foreign cursor item: mode " + r.Mode.String() + ", no drop"
		return g
	}
	g.Drop, g.Why = true, "foreign cursor item: drop at feet"
	return g
}

// Stable is the reading the gate judges: the latest raw observation trimmed to
// what the Tracker's debounced belief agrees on. A panel must be both believed
// (N agreeing frames) and in the latest frame — a one-frame slide-in is not
// foreign, and a panel the latest frame no longer shows is not closed twice.
// The mode is the belief when the raw frame agrees, else Unknown (on
// disagreement the executive does not guess).
func Stable(s Seen) screen.Reading {
	r := s.Reading
	if r.Mode != s.State.Mode {
		r.Mode = screen.Unknown
	}
	r.Panels &= s.State.Panels
	r.Sight &= s.State.Panels
	r.CursorItem = r.CursorItem && s.State.CursorItem
	return r
}

// Janitor defaults.
const (
	JanitorMinGap      = 400 * time.Millisecond // at most one action per 400ms
	JanitorSettle      = 250 * time.Millisecond // a reading this long after the action judges it
	JanitorWedgeN      = 6                      // actions without the foreign set shrinking...
	JanitorWedgeWindow = 10 * time.Second       // ...within this window: ui_wedge
	JanitorWedgeRest   = 60 * time.Second       // a wedge stops the janitor acting this long
)

// Decision is the Janitor's answer for one tick.
type Decision struct {
	GateResult
	Act      bool   // perform Action / Drop now, then call Acted
	Wedged   bool   // ui_wedge: still gating, not acting
	NewWedge bool   // the wedge began on this call: log once, photograph
	Wait     string // why a wanted action was held back (rate, stale reading, wedge)
}

// Gate is the state line's gate= value: ok | blocked(<panels>) | wedge.
func (d Decision) Gate() string {
	if d.Wedged && !d.Open {
		return "wedge"
	}
	return d.Blocked()
}

type janitorAct struct {
	at  time.Time
	set uint64 // foreign panels, plus bit 32 for a foreign cursor item
}

func foreignSet(g GateResult) uint64 {
	s := uint64(g.Foreign)
	if g.Cursor {
		s |= 1 << 32
	}
	return s
}

// Janitor rate-limits the gate's actions, insists on a fresh reading before
// judging one, and names a wedge instead of looping. Pure: time comes in.
type Janitor struct {
	MinGap      time.Duration
	Settle      time.Duration
	WedgeN      int
	WedgeWindow time.Duration
	WedgeRest   time.Duration

	lastAct    time.Time
	acts       []janitorAct
	wedgeUntil time.Time
}

// NewJanitor has the documented defaults.
func NewJanitor() *Janitor {
	return &Janitor{MinGap: JanitorMinGap, Settle: JanitorSettle, WedgeN: JanitorWedgeN,
		WedgeWindow: JanitorWedgeWindow, WedgeRest: JanitorWedgeRest}
}

// Decide gates the holder on the latest published observation (nil before the
// first capture: open — the gate never blocks on blindness).
func (j *Janitor) Decide(now time.Time, s *Seen, n HolderNeeds) Decision {
	if s == nil {
		return Decision{GateResult: GateResult{Open: true, Why: "no reading yet"}}
	}
	d := Decision{GateResult: Gate(Stable(*s), n)}
	if d.Open {
		j.acts = j.acts[:0] // the screen cleared: whatever was trying worked
		return d
	}
	if now.Before(j.wedgeUntil) {
		d.Wedged, d.Wait = true, "ui_wedge"
		return d
	}
	if !d.Acts() {
		return d
	}
	if !j.lastAct.IsZero() {
		if now.Sub(j.lastAct) < j.MinGap {
			d.Wait = "rate"
			return d
		}
		if s.At.Before(j.lastAct.Add(j.Settle)) {
			d.Wait = "awaiting a fresh reading"
			return d
		}
	}
	cur := foreignSet(d.GateResult)
	kept := j.acts[:0]
	for _, a := range j.acts {
		if now.Sub(a.at) <= j.WedgeWindow {
			kept = append(kept, a)
		}
	}
	j.acts = kept
	if len(j.acts) > 0 {
		if first := j.acts[0].set; cur != first && cur&^first == 0 {
			j.acts = j.acts[:0] // the foreign set shrank: progress, a fresh window
		}
	}
	if len(j.acts) >= j.WedgeN {
		j.acts = j.acts[:0]
		j.wedgeUntil = now.Add(j.WedgeRest)
		d.Wedged, d.NewWedge, d.Wait = true, true, "ui_wedge"
		return d
	}
	j.acts = append(j.acts, janitorAct{at: now, set: cur})
	j.lastAct = now
	d.Act = true
	return d
}

// Acted stamps when the motor finished the action (a real click waits for the
// foreground first), so the fresh-reading rule measures from the real input.
func (j *Janitor) Acted(at time.Time) {
	if at.After(j.lastAct) {
		j.lastAct = at
	}
}

// WedgedUntil is the end of the current ui_wedge rest (zero if never wedged).
func (j *Janitor) WedgedUntil() time.Time { return j.wedgeUntil }
