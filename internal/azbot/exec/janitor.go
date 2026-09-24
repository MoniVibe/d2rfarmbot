package exec

import (
	"fmt"
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
	// Survival: the holder is survival class (Stand/Flee/Breakout/Dodge). The
	// janitor never delays it and never acts for it unless the pause menu is
	// up (Janitor rule 5). The executive sets it from the grant's class.
	Survival bool
	// World facts the executive vouches for — THE CURSOR RULE (relay R10,
	// 09:20:43: the gate dropped a benched parker's item on the town floor):
	// Town: in town a cursor item is NEVER dropped. CursorJunk: the item on
	// the cursor is KNOWN junk (the percept judged it merchandise while it lay
	// in the bag) — only such an item may be dropped, and only in the field.
	Town       bool
	CursorJunk bool
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
	Open   bool          // the holder may Step
	Action screen.Action // janitor step toward a clear screen (ActClick/ActKey), else ActNone/ActUnknown
	Drop   bool          // cursor rule: one plain click on the ground at her feet (field, known junk only)
	// Park: cursor rule — the bag is SEEN open: place the item in a free cell
	// of the measured bag grid (the executive's parker reads the free region
	// from memory); a later Reading judges it by CursorItem.
	Park bool
	// CursorHold: cursor rule — nothing safe to do for the item (the bag is
	// shut, and dropping is forbidden): the holder is HELD and the executive
	// hands the item back to whoever parks it (Equip in town) and logs it.
	CursorHold bool
	Foreign    screen.Panel // positively observed blocking panels the holder does not claim
	Cursor     bool         // a foreign item rides the cursor
	Why        string
}

// Acts reports whether the result asks the motor for something.
func (g GateResult) Acts() bool {
	return g.Drop || g.Park || g.Action.Kind == screen.ActClick || g.Action.Kind == screen.ActKey
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
// then a foreign cursor item over a SEEN bag is PARKED (the executive places it
// in a free cell of the measured grid; an ESC could take the item with the
// panel), or LEFT over a half panel that is not the bag; then foreign panels,
// closed by screen.CloseStep restricted to them; then a foreign cursor item on
// clear ground: dropped at her feet only in the field and only when it is
// known junk — otherwise the holder is HELD (CursorHold) for the item's parker.
// Never an ESC for the cursor, never a drop in town (relay R10).
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
	if cursor && r.Mode == screen.World && r.Sight&screen.Inventory != 0 {
		g.Park, g.Why = true, "foreign cursor item over the open bag: park in a free cell"
		return g
	}
	if b := r.Sight & bag; cursor && b != 0 {
		g.Why = "cursor item over open " + b.String() + ": left (not the bag: no grid to park in)"
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
		if g.Action.Kind == screen.ActKey && r.Unsure&screen.SubPanel != 0 {
			// Something that looks like a sub-panel is up but was ruled
			// unreachable or quarantined: were it real, the ESC would land on
			// it, not on the target. Nothing is pressed while it stands.
			g.Action = screen.Action{Kind: screen.ActUnknown, Panel: g.Action.Panel,
				Reason: "ESC withheld: a sub-panel reads Unsure (" + r.Why(screen.SubPanel) + ")"}
		}
		g.Why = "foreign: " + foreign.String()
		if !g.Acts() {
			g.Why += " (" + g.Action.Reason + ")"
			if r.Mode != screen.World {
				// Off the world (a death screen, a menu walk) with nothing
				// safe to press: blocking the any-mode holder (Respawn)
				// would only freeze the one activity that can leave.
				g.Open = true
				g.Why += "; off-world holder proceeds"
				return g
			}
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
	switch {
	case n.Town:
		g.CursorHold, g.Why = true, "foreign cursor item in town: HELD — never dropped; the bag is shut, awaiting its parker"
	case !n.CursorJunk:
		g.CursorHold, g.Why = true, "foreign cursor item not known junk: HELD — never dropped; the bag is shut, awaiting its parker"
	default:
		g.Drop, g.Why = true, "foreign cursor item (known junk, field): drop at feet"
	}
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

	// THE LOOP BREAKER (relay R9): the wedge above never tripped on a
	// phantom loop — the gate opened between clicks and the foreign set kept
	// changing (shop -> subpanel -> pause -> subpanel), so its window kept
	// restarting. This one counts EVERY action, any panels, and nothing
	// resets it but time: more than LoopN in LoopWindow is a ui_wedge.
	JanitorLoopN      = 8
	JanitorLoopWindow = 30 * time.Second

	// JanitorEscFrames: consecutive readings that must positively show the
	// panel an ESC targets. One frame is a rumor; an ESC at nothing raises the
	// pause menu.
	JanitorEscFrames = 2
	// JanitorPhantomWindow: how long after an ESC a newly raised pause menu
	// convicts the ESC's target.
	JanitorPhantomWindow = 1500 * time.Millisecond
	// JanitorQuarantine: a detector convicted of a phantom reads Unsure this long.
	JanitorQuarantine = 5 * time.Minute
)

// Decision is the Janitor's answer for one tick.
type Decision struct {
	GateResult
	Act      bool   // perform Action / Drop now, then call Acted
	Wedged   bool   // ui_wedge: gating on the pause menu only, not acting
	NewWedge bool   // the wedge began on this call: log once, photograph
	Wait     string // why a wanted action was held back (rate, stale reading, wedge, esc, survival)
	// Phantom: a detector was convicted on this call ("phantom: shop — ESC
	// raised pause; quarantined 5m"). When Act is set too, Why carries it and
	// the action is its Return to Game.
	Phantom string
}

// Gate is the state line's gate= value: ok | blocked(<panels>) | wedge. A
// wedge reads "wedge" whether or not it holds the holder (it holds only on
// the pause menu): the janitor is resting either way.
func (d Decision) Gate() string {
	if d.Wedged {
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

// pending is the last janitor action awaiting its verdict on a fresh reading.
type pending struct {
	at          time.Time
	esc         bool
	target      screen.Panel // what the action meant to close
	before      screen.Panel // Positive at the time of the action
	pauseBefore bool
}

// Janitor rate-limits the gate's actions, insists on a fresh reading before
// judging one, convicts phantom detectors, and names a wedge instead of
// looping. Pure: time comes in.
//
// THE SAFETY RULES (relay R9, 2026-09-24 — a phantom shop and a phantom
// sub-panel in the Maggot Lair, mid-fight: ESC at nothing raised the pause
// menu, 98 "close" clicks landed in the world, the owner hit F10):
//
//  1. ESC only for a panel ESC closes (screen.EscCloses) that was positively
//     seen in >= EscFrames consecutive readings.
//  2. Phantom conviction: an ESC followed (within PhantomWindow) by a pause
//     menu that was not up before raised that menu itself — its target was
//     never there. One Return to Game click, and the target's detector is
//     quarantined (Unsure) for Quarantine. Likewise a close click after
//     which the panel is still seen and nothing else changed.
//  3. The loop breaker: more than LoopN actions in LoopWindow, whatever the
//     panels — ui_wedge.
//  4. A ui_wedge stops the janitor for WedgeRest and blocks the holder only
//     on a pause menu (the game is frozen then); nothing else holds it.
//  5. A survival holder (Stand/Flee/Breakout/Dodge) is never delayed by the
//     janitor and never gets a janitor action unless the pause menu is up
//     (the game is frozen then): mid-fight, a phantom click is worse than a
//     real open panel.
type Janitor struct {
	MinGap        time.Duration
	Settle        time.Duration
	WedgeN        int
	WedgeWindow   time.Duration
	WedgeRest     time.Duration
	LoopN         int
	LoopWindow    time.Duration
	EscFrames     int
	PhantomWindow time.Duration
	Quarantine    time.Duration

	lastAct    time.Time
	acts       []janitorAct
	all        []time.Time // every action, any panels (the loop breaker)
	wedgeUntil time.Time

	streak   map[screen.Panel]int // consecutive readings each panel was positive
	streakAt time.Time            // the reading last counted
	quar     map[screen.Panel]time.Time
	pend     *pending
	rtgOwed  time.Time // a convicted ESC's Return to Game is owed until then
	note     string    // phantom verdict to report on this Decide
}

// NewJanitor has the documented defaults.
func NewJanitor() *Janitor {
	return &Janitor{MinGap: JanitorMinGap, Settle: JanitorSettle, WedgeN: JanitorWedgeN,
		WedgeWindow: JanitorWedgeWindow, WedgeRest: JanitorWedgeRest,
		LoopN: JanitorLoopN, LoopWindow: JanitorLoopWindow, EscFrames: JanitorEscFrames,
		PhantomWindow: JanitorPhantomWindow, Quarantine: JanitorQuarantine}
}

// Quarantined: the panels whose detector is on quarantine at now.
func (j *Janitor) Quarantined(now time.Time) screen.Panel {
	var q screen.Panel
	for p, until := range j.quar {
		if now.Before(until) {
			q |= p
		} else {
			delete(j.quar, p)
		}
	}
	return q
}

// quarantine demotes quarantined panels to Unsure (the maps are shared with
// the published Seen and are left alone).
func (j *Janitor) quarantine(now time.Time, r screen.Reading) screen.Reading {
	q := j.Quarantined(now) & (r.Panels | r.Sight)
	if q == 0 {
		return r
	}
	r.Panels &^= q
	r.Sight &^= q
	r.Unsure |= q
	return r
}

func (j *Janitor) convict(now time.Time, p screen.Panel) {
	p &^= screen.PauseMenu // the pause detector is the arbiter of phantoms, never on trial
	if p == 0 {
		return
	}
	if j.quar == nil {
		j.quar = map[screen.Panel]time.Time{}
	}
	for _, q := range screen.All {
		if p&q != 0 {
			j.quar[q] = now.Add(j.Quarantine)
		}
	}
}

// count advances the per-panel streaks once per new reading.
func (j *Janitor) count(at time.Time, r screen.Reading) {
	if !j.streakAt.IsZero() && !at.After(j.streakAt) {
		return
	}
	j.streakAt = at
	if j.streak == nil {
		j.streak = map[screen.Panel]int{}
	}
	pos := Positive(r)
	for _, q := range screen.All {
		if pos&q != 0 {
			j.streak[q]++
		} else {
			delete(j.streak, q)
		}
	}
}

func quarantineMins(d time.Duration) string {
	return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
}

// resolve judges the pending action on a fresh reading (phantom conviction).
func (j *Janitor) resolve(now time.Time, s *Seen) {
	p := j.pend
	if p == nil || s.At.Before(p.at.Add(j.Settle)) {
		return
	}
	r := s.Reading
	if p.esc {
		if s.At.After(p.at.Add(j.PhantomWindow)) {
			j.pend = nil // too late to be this ESC's doing
			return
		}
		if r.Sight&screen.PauseMenu != 0 && !p.pauseBefore {
			j.convict(now, p.target)
			j.note = "phantom: " + p.target.String() + " — ESC raised pause; quarantined " + quarantineMins(j.Quarantine)
			j.rtgOwed = now.Add(5 * time.Second)
			j.pend = nil
		}
		return
	}
	j.pend = nil
	if pos := Positive(r); pos&p.target != 0 && pos == p.before {
		j.convict(now, p.target)
		j.note = "phantom: " + p.target.String() + " — close click changed nothing; quarantined " + quarantineMins(j.Quarantine)
	}
}

// wedge starts a ui_wedge on d.
func (j *Janitor) wedge(now time.Time, d *Decision, why string) {
	j.acts, j.all = j.acts[:0], j.all[:0]
	j.wedgeUntil = now.Add(j.WedgeRest)
	d.Wedged, d.NewWedge, d.Wait = true, true, "ui_wedge"
	d.Why += "; " + why
	wedgeGate(d)
}

// wedgeGate: while wedged the holder is gated on the pause menu only — and on
// a foreign cursor item, which is never let loose into the world (a holder
// Stepping with it would click it onto the ground).
func wedgeGate(d *Decision) {
	switch {
	case d.Open || d.Foreign&screen.PauseMenu != 0:
	case d.Cursor:
		d.Why += " [ui_wedge: the cursor item is HELD]"
	default:
		d.Open = true
		d.Why += " [ui_wedge: gating on the pause menu only]"
	}
}

// Decide gates the holder on the latest published observation (nil before the
// first capture: open — the gate never blocks on blindness).
func (j *Janitor) Decide(now time.Time, s *Seen, n HolderNeeds) Decision {
	if s == nil {
		return Decision{GateResult: GateResult{Open: true, Why: "no reading yet"}}
	}
	j.resolve(now, s)
	raw := j.quarantine(now, s.Reading)
	j.count(s.At, raw)
	st := *s
	st.Reading = raw
	d := Decision{GateResult: Gate(Stable(st), n)}
	d.Phantom, j.note = j.note, ""
	paused := raw.Sight&screen.PauseMenu != 0

	if now.Before(j.wedgeUntil) {
		d.Wedged, d.Wait = true, "ui_wedge"
		wedgeGate(&d)
		if !d.Open && n.Survival && !paused {
			d.Open = true // rule 5: a survival holder is never delayed, not even for the cursor
		}
		return d
	}
	if n.Survival && !paused {
		if !d.Open {
			d.Open, d.Wait = true, "survival"
			d.Why += "; survival holder: the janitor stands down"
		}
		return d
	}
	fresh := j.lastAct.IsZero() || (now.Sub(j.lastAct) >= j.MinGap && !s.At.Before(j.lastAct.Add(j.Settle)))
	if !j.rtgOwed.IsZero() {
		pt, ok := raw.Close[screen.PauseMenu]
		switch {
		case !paused || !ok || now.After(j.rtgOwed) || n.Claims&screen.PauseMenu != 0:
			// Gone, unclickable, stale — or the holder walks the pause menu
			// on purpose (the session's relog): not ours to dismiss.
			j.rtgOwed = time.Time{}
		case fresh:
			// The convicted ESC's pause menu: Return to Game, once.
			j.rtgOwed = time.Time{}
			d.Open, d.Drop = false, false
			d.Foreign |= screen.PauseMenu
			d.Action = screen.Action{Kind: screen.ActClick, X: pt.X, Y: pt.Y, Panel: screen.PauseMenu,
				Reason: "Return to Game (the pause menu a phantom ESC raised)"}
			if d.Phantom != "" {
				d.Why, d.Phantom = d.Phantom, ""
			} else {
				d.Why = "Return to Game: the pause menu a phantom ESC raised"
			}
			return j.act(now, d, raw, paused)
		}
	}
	if d.Open {
		j.acts = j.acts[:0] // the screen cleared: whatever was trying worked
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
	if d.Action.Kind == screen.ActKey {
		tgt := d.Action.Panel
		if tgt == 0 || tgt&^screen.EscCloses != 0 {
			d.Wait = "esc withheld: " + tgt.String() + " is not a panel ESC closes"
			return d
		}
		for _, q := range screen.All {
			if tgt&q != 0 && j.streak[q] < j.EscFrames {
				d.Wait = fmt.Sprintf("esc withheld: %s seen in %d consecutive reading(s), need %d", q, j.streak[q], j.EscFrames)
				return d
			}
		}
	}
	kept := j.all[:0]
	for _, at := range j.all {
		if now.Sub(at) < j.LoopWindow {
			kept = append(kept, at)
		}
	}
	j.all = kept
	if len(j.all) >= j.LoopN {
		j.wedge(now, &d, fmt.Sprintf("loop breaker: %d janitor actions in %s", len(j.all), j.LoopWindow))
		return d
	}
	cur := foreignSet(d.GateResult)
	keptActs := j.acts[:0]
	for _, a := range j.acts {
		if now.Sub(a.at) <= j.WedgeWindow {
			keptActs = append(keptActs, a)
		}
	}
	j.acts = keptActs
	if len(j.acts) > 0 {
		if first := j.acts[0].set; cur != first && cur&^first == 0 {
			j.acts = j.acts[:0] // the foreign set shrank: progress, a fresh window
		}
	}
	if len(j.acts) >= j.WedgeN {
		j.wedge(now, &d, fmt.Sprintf("%d actions without the foreign set shrinking", len(j.acts)))
		return d
	}
	return j.act(now, d, raw, paused)
}

// act books an action: the rate clock, both wedge windows, and the pending
// verdict (an ESC, or a close click on anything but the pause menu).
func (j *Janitor) act(now time.Time, d Decision, raw screen.Reading, paused bool) Decision {
	j.acts = append(j.acts, janitorAct{at: now, set: foreignSet(d.GateResult)})
	j.all = append(j.all, now)
	j.lastAct = now
	j.pend = nil
	switch {
	case d.Action.Kind == screen.ActKey:
		j.pend = &pending{at: now, esc: true, target: d.Action.Panel, before: Positive(raw), pauseBefore: paused}
	case d.Action.Kind == screen.ActClick && d.Action.Panel != screen.PauseMenu:
		j.pend = &pending{at: now, target: d.Action.Panel, before: Positive(raw)}
	}
	d.Act = true
	return d
}

// Acted stamps when the motor finished the action (a real click waits for the
// foreground first), so the fresh-reading rule measures from the real input.
func (j *Janitor) Acted(at time.Time) {
	if at.After(j.lastAct) {
		j.lastAct = at
	}
	if j.pend != nil && at.After(j.pend.at) {
		j.pend.at = at
	}
}

// Refused: the motor did not deliver the last action (no foreground) — there
// is nothing to judge it by.
func (j *Janitor) Refused() { j.pend = nil }

// WedgedUntil is the end of the current ui_wedge rest (zero if never wedged).
func (j *Janitor) WedgedUntil() time.Time { return j.wedgeUntil }
