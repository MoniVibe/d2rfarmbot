package exec

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/trace"
)

// THE SESSION (docs/AZBOT_V2.md layer 0, migration step 8). The top of the
// executive tick: when the session is not InGame, only the session acts. It
// owns the relog ritual — once an activity (Relog, arbiter class Recover), now
// a request the executive hands to the session:
//
//	Attaching ──valid snapshot──▶ InGame ◀──────────────── fail (world intact:
//	    ▲                           │ Request              pause menu left up;
//	    │                           ▼                      the janitor clicks
//	    │   Relogging{OpenPause → ClickSaveExit → AwaitMenu   Return to Game)
//	    │             → ClickPlay → AwaitWorld} ──new world──▶ InGame
//	    └── fail out of game (world never returned) ◀──┘
//	any ──F10──▶ Disengaged ──re-engaged──▶ Attaching
//	Chicken: reserved (the survival exit; not built)
//
// THE SAFE SESSION END (relay R4: a -seconds timer exited mid-fight in the Dry
// Hills and left the character undriven). The run never just stops: Stop
// (time budget spent, logs/stop.now, Ctrl-C) records the request and the next
// tick outside a relog enters WindDown, where the normal loop keeps running:
//
//	InGame/Attaching ──Stop──▶ WindDown ──in town, or field quiet 3s──▶ Stopped (exit)
//	                             │ still hot: WindTown (the town road bids,
//	                             │ class Recover — over Fight, under Survive)
//	                             │ 120s and not safe: WindHold (disengage)
//	                             ▼
//	Disengaged ──Stop──▶ WindDown/Hold ◀──F10── (owner drives, or the cap)
//	                       │ safe (whoever brought him there): Stopped
//	                       │ every 30s: the hold reminder
//	                       └──F10 re-engaged──▶ WindDown (a fresh cap)
//
// Never Stopped while a living monster stands within WindRadius tiles.
//
// Every phase is judged BY SIGHT or by memory validity with a bounded wait:
// no sleeps, no ESC loops. The one ESC is sent only on a stable Reading that
// is World with nothing blocking, and a second only after the same check
// passes again on a fresh reading. Pure: time, the snapshot's validity, the
// map seed and the published screen observation come in; the act to perform
// goes out; the executive owns the motor and reports back with Acted.

// SessionState is layer 0.
type SessionState uint8

const (
	Attaching  SessionState = iota // no valid world yet (startup, re-engage, a relog that lost the world)
	InGame                         // the arbiter runs
	Relogging                      // the session owns every tick (Phase says where)
	Chicken                        // reserved: the survival exit (not built)
	Disengaged                     // F10: the owner drives
	WindDown                       // the run is ending: to safety first (Hold: the motor disengaged)
	Stopped                        // safe: the executive stops the motor and exits
)

func (s SessionState) String() string {
	switch s {
	case Attaching:
		return "Attaching"
	case InGame:
		return "InGame"
	case Relogging:
		return "Relogging"
	case Chicken:
		return "Chicken"
	case Disengaged:
		return "Disengaged"
	case WindDown:
		return "WindDown"
	case Stopped:
		return "Stopped"
	}
	return "?"
}

// RelogPhase is where a relog stands.
type RelogPhase uint8

const (
	RelogNone     RelogPhase = iota
	OpenPause                // one ESC on a clear world screen, the pause menu seen
	ClickSaveExit            // click Save and Exit; the menu goes, the world unloads
	AwaitMenu                // the character screen settles
	ClickPlay                // click Play (launches the selected character)
	AwaitWorld               // a valid world again; the seed names it
)

func (p RelogPhase) String() string {
	switch p {
	case RelogNone:
		return "-"
	case OpenPause:
		return "OpenPause"
	case ClickSaveExit:
		return "ClickSaveExit"
	case AwaitMenu:
		return "AwaitMenu"
	case ClickPlay:
		return "ClickPlay"
	case AwaitWorld:
		return "AwaitWorld"
	}
	return "?"
}

// SessionAct is the one input the session asks of the motor this tick.
type SessionAct uint8

const (
	SesNoAct SessionAct = iota
	SesEsc              // one hardware ESC (the OpenPause key; foreground required)
	SesClick            // a hardware menu click at SessionOut.At (screenshot pixels)
)

func (a SessionAct) String() string {
	switch a {
	case SesEsc:
		return "esc"
	case SesClick:
		return "click"
	}
	return "-"
}

// Menu buttons in SCREENSHOT pixels of the 1920x1050 physical client — the
// motor's RealMenuClick divides by the DPI scale (1.25) into the 1536x840
// logical client and adds the window origin. Measured by the relog drill
// 2026-07-19 and re-confirmed on relay R2's pause_menu capture (Save and Exit
// drawn centred at 960,821; screen/testdata/pause_menu.jpg).
var (
	SaveExitShot = screen.Point{X: 958, Y: 822} // pause menu: Save and Exit
	PlayShot     = screen.Point{X: 922, Y: 822} // character screen: Normal (launches the selected char)
)

// Session defaults.
const (
	SesSettle        = 250 * time.Millisecond // a reading this long after an act judges it
	SesFresh         = time.Second            // an observation older than this is stale
	SesPauseWait     = 2 * time.Second        // per ESC: the pause menu must be seen within
	SesSaveExitRetry = 3 * time.Second        // menu still up this long after the click: click once more
	SesSaveExitWait  = 10 * time.Second       // the world must unload within
	SesUnloadGrace   = 2 * time.Second        // invalid this long counts as unloaded even if the menu still draws
	SesMenuSettle    = 3 * time.Second        // the character screen settles before Play
	SesPlayRefused   = 30 * time.Second       // Play refused for want of foreground this long: give up
	SesPlayRetry     = 20 * time.Second       // still no world: click Play once more
	SesWorldWait     = 45 * time.Second       // a valid world must return within (from the first Play)
	SesWorldStable   = time.Second            // valid this long counts as back

	WindRadius = 40                // tiles (Chebyshev): a living monster this close keeps the field hot
	WindQuiet  = 3 * time.Second   // a field quiet this long is safe to stop in
	WindCap    = 120 * time.Second // not safe this long after the request: disengage and hold
	WindSay    = 30 * time.Second  // the hold reminder's period
)

// WindAct is what a winding-down session asks of the executive this tick.
type WindAct uint8

const (
	WindNone WindAct = iota
	WindTown         // bid the town road (the Recall activity) this tick
	WindHold         // the cap: stop the motor, end the holder, DISENGAGE — then hold
	WindExit         // safe: stop the motor and exit normally
)

func (a WindAct) String() string {
	switch a {
	case WindTown:
		return "town"
	case WindHold:
		return "hold"
	case WindExit:
		return "exit"
	}
	return "-"
}

// Hold reminders, verbatim in the owner's log every WindSay.
const (
	HoldCapSay   = "WINDDOWN: could not reach safety — disengaged and holding; press F10 to resume or kill the process"
	HoldOwnerSay = "WINDDOWN: disengaged, the owner drives — holding; exits once in town or the field is quiet 3s; F10 resumes the wind-down"
)

// SessionIn is one tick's evidence.
type SessionIn struct {
	Now     time.Time
	Engaged bool   // the F10 kill-switch
	Focused bool   // D2R owns the foreground (no ESC is ever sent unfocused)
	Valid   bool   // percept Snapshot.Valid
	Seed    uint64 // the map seed as the executive last fetched it
	Seen    *Seen  // the latest published screen observation (nil before the first)
	InTown  bool   // WindDown: percept Me.InTown (the safe harbour)
	Hot     bool   // WindDown: a living monster within WindRadius tiles
}

// SessionOut is the session's answer for one tick.
type SessionOut struct {
	Owns  bool         // the session holds the tick: nothing below acts
	Act   SessionAct   // perform it, then call Acted
	At    screen.Point // SesClick target (screenshot pixels)
	Ended *RelogEnd    // set on the tick a relog ends, however it ends
	Wind  WindAct      // WindDown: what the executive does about the ending run
	Say   string       // a line for the owner's log (the hold reminder)
}

// RelogEnd is how a relog ended. Why is Completed on success; otherwise the
// typed failure: Refused (no foreground), Precondition (screen not clear for
// the ESC), Deaf (the pause menu never rose / Save and Exit never took),
// Timebox (the world never came back), Preempted (F10 — the owner took over).
type RelogEnd struct {
	Why     phase.Reason
	Phase   RelogPhase // the phase it ended in
	Churned bool       // the world was left: a failure now costs a new game
	Detail  string
	Took    time.Duration
}

// OK: the relog reached a new world.
func (e RelogEnd) OK() bool { return e.Why == phase.Completed }

// Session is the layer-0 FSM.
type Session struct {
	Trace func(line string) // nil = silent
	Tick  uint64            // stamped on trace lines; the caller advances it

	// Timings (NewSession sets the defaults).
	Settle, Fresh, PauseWait, SaveExitRetry, SaveExitWait, UnloadGrace time.Duration
	MenuSettle, PlayRefused, PlayRetry, WorldWait, WorldStable         time.Duration
	WindQuiet, WindCap, WindSay                                        time.Duration
	// Buttons (screenshot pixels): -exitxy / -playxy override them.
	SaveExit, Play screen.Point

	st  SessionState
	ph  RelogPhase
	why string // the request's reason

	began, phAt time.Time // relog start, phase entry
	seed        uint64    // the seed when the relog began
	actAt       time.Time // last act performed (zero: none this phase)
	acts        int       // acts performed this phase
	refused     bool      // the last act was refused (no foreground)
	pending     SessionAct
	invalidAt   time.Time // AwaitMenu/ClickSaveExit: when the snapshot went invalid
	playAt      time.Time // first Play click
	validAt     time.Time // AwaitWorld: when the snapshot turned valid

	// The safe end (WindDown).
	windWhy string    // the stop request's reason ("" = none; the first one stands)
	windAt  time.Time // the cap's clock: the request, or the F10 that resumed it
	hold    bool      // WindDown/Hold: the motor is disengaged, nothing is driven
	offSeen bool      // Hold: a disengaged tick was seen (only then does engaged mean F10)
	holdSay string    // the reminder the hold repeats
	quietAt time.Time // WindDown: the field has been quiet since (zero: hot or unknown)
	saidAt  time.Time // the last hold reminder
}

// NewSession starts Attaching with the documented defaults.
func NewSession() *Session {
	return &Session{
		Settle: SesSettle, Fresh: SesFresh, PauseWait: SesPauseWait, SaveExitRetry: SesSaveExitRetry,
		SaveExitWait: SesSaveExitWait, UnloadGrace: SesUnloadGrace, MenuSettle: SesMenuSettle,
		PlayRefused: SesPlayRefused, PlayRetry: SesPlayRetry, WorldWait: SesWorldWait, WorldStable: SesWorldStable,
		WindQuiet: WindQuiet, WindCap: WindCap, WindSay: WindSay,
		SaveExit: SaveExitShot, Play: PlayShot,
	}
}

// State is the session's layer-0 state.
func (s *Session) State() SessionState { return s.st }

// Phase is the relog phase (RelogNone outside Relogging).
func (s *Session) Phase() RelogPhase { return s.ph }

// Relogging reports whether the session owns the tick for a relog.
func (s *Session) Relogging() bool { return s.st == Relogging }

// Winding reports whether a stop was requested (the run is ending).
func (s *Session) Winding() bool { return s.windWhy != "" }

// String is the state line's ses= value: "InGame", "Relogging/AwaitMenu",
// "WindDown/Hold".
func (s *Session) String() string {
	if s.st == Relogging {
		return s.st.String() + "/" + s.ph.String()
	}
	if s.st == WindDown && s.hold {
		return "WindDown/Hold"
	}
	return s.st.String()
}

// Claims: the panels the session holds while it acts. The pause menu is the
// relog's road, so the janitor leaves it up while Relogging; the moment the
// relog ends it is foreign again and the janitor clicks Return to Game.
func (s *Session) Claims() screen.Panel {
	if s.st == Relogging {
		return screen.PauseMenu
	}
	return 0
}

func (s *Session) to(now time.Time, st SessionState, ph RelogPhase, why string) {
	from := s.String()
	s.st, s.ph, s.phAt, s.hold = st, ph, now, false
	s.actAt, s.acts, s.refused, s.pending = time.Time{}, 0, false, SesNoAct
	s.invalidAt, s.validAt = time.Time{}, time.Time{}
	if s.Trace != nil {
		s.Trace(trace.Session(from, s.String(), why, s.Tick))
	}
}

// RelogReady says whether a relog may begin on this observation: a fresh
// stable reading that is World with nothing blocking — or with exactly the
// pause menu up (the relog's own road: no ESC needed).
func RelogReady(now time.Time, seen *Seen, fresh time.Duration) (bool, string) {
	if seen == nil {
		return false, "no screen reading yet"
	}
	if now.Sub(seen.At) > fresh {
		return false, fmt.Sprintf("screen reading stale (%s)", now.Sub(seen.At).Round(time.Millisecond))
	}
	r := Stable(*seen)
	if r.Mode != screen.World {
		return false, "mode " + r.Mode.String()
	}
	if b := r.Panels &^ screen.PauseMenu; b.Blocking() {
		return false, "panels up: " + b.String()
	}
	return true, "clear"
}

// Request asks for a relog. Accepted only InGame, engaged, on a ready screen;
// a refusal costs nothing (no transition, no line) — the requester asks again.
func (s *Session) Request(now time.Time, why string, seed uint64, seen *Seen) (bool, string) {
	if s.st != InGame {
		return false, "session " + s.String()
	}
	if ok, r := RelogReady(now, seen, s.Fresh); !ok {
		return false, r
	}
	s.why, s.began, s.seed, s.playAt = why, now, seed, time.Time{}
	s.to(now, Relogging, OpenPause, "relog: "+why)
	return true, ""
}

// Disengage is the -disengaged start: Disengaged from the first tick with the
// owner's reason on the line (Step's own entry names F10).
func (s *Session) Disengage(now time.Time, why string) {
	if s.st == Attaching || s.st == InGame {
		s.to(now, Disengaged, RelogNone, why)
	}
}

// Stop asks for the safe end of the run (time budget spent, the owner's stop
// file, a signal). Recorded only — the next Step outside a relog enters
// WindDown. The first request stands; false for a repeat.
func (s *Session) Stop(now time.Time, why string) bool {
	if s.windWhy != "" {
		return false
	}
	s.windWhy, s.windAt = why, now
	return true
}

// windTo is to() for the wind-down's two faces (Hold is a flag, not a phase).
func (s *Session) windTo(now time.Time, hold bool, why string) {
	from := s.String()
	s.st, s.ph, s.phAt, s.hold = WindDown, RelogNone, now, hold
	s.actAt, s.acts, s.refused, s.pending = time.Time{}, 0, false, SesNoAct
	if s.Trace != nil {
		s.Trace(trace.Session(from, s.String(), why, s.Tick))
	}
}

// safe: the one condition the run may end on — in town, or a valid field with
// no living monster within WindRadius for WindQuiet. An invalid snapshot is
// never safe (nothing can be judged) and restarts the quiet clock.
func (s *Session) safe(in SessionIn) (bool, string) {
	switch {
	case !in.Valid:
		s.quietAt = time.Time{}
		return false, ""
	case in.InTown:
		return true, "safe: in town"
	case in.Hot:
		s.quietAt = time.Time{}
		return false, ""
	}
	if s.quietAt.IsZero() {
		s.quietAt = in.Now
	}
	if in.Now.Sub(s.quietAt) >= s.WindQuiet {
		return true, fmt.Sprintf("safe: no monster within %d for %s", WindRadius, s.WindQuiet)
	}
	return false, ""
}

// holdOut repeats the hold reminder every WindSay (the first on entry).
func (s *Session) holdOut(now time.Time) SessionOut {
	if !s.saidAt.IsZero() && now.Sub(s.saidAt) < s.WindSay {
		return SessionOut{}
	}
	s.saidAt = now
	return SessionOut{Say: s.holdSay}
}

// windDown is one WindDown tick. The session never owns it: the arbiter keeps
// fighting and the sentinel keeps drinking while the town road bids.
func (s *Session) windDown(in SessionIn) SessionOut {
	now := in.Now
	if ok, why := s.safe(in); ok {
		s.to(now, Stopped, RelogNone, why)
		return SessionOut{Owns: true, Wind: WindExit}
	}
	if s.hold {
		if !in.Engaged {
			s.offSeen = true
		} else if s.offSeen {
			s.windAt, s.saidAt = now, time.Time{}
			s.windTo(now, false, "F10: re-engaged — the wind-down resumes (a fresh "+s.WindCap.String()+")")
			return s.seek(in)
		}
		return s.holdOut(now)
	}
	if !in.Engaged {
		s.holdSay, s.saidAt, s.offSeen = HoldOwnerSay, time.Time{}, true
		s.windTo(now, true, "F10: the owner took control")
		return s.holdOut(now)
	}
	return s.seek(in)
}

// seek: engaged and not yet safe — the town road, or the cap.
func (s *Session) seek(in SessionIn) SessionOut {
	now := in.Now
	if now.Sub(s.windAt) >= s.WindCap {
		// The executive disengages on this answer; F10 counts only after a
		// disengaged tick has been seen (the cap's own tick is still engaged).
		s.holdSay, s.saidAt, s.offSeen = HoldCapSay, time.Time{}, false
		s.windTo(now, true, "could not reach safety within "+s.WindCap.String()+": disengage and hold")
		out := s.holdOut(now)
		out.Wind = WindHold
		return out
	}
	if in.Valid && !in.InTown {
		return SessionOut{Wind: WindTown}
	}
	return SessionOut{}
}

// Acted reports the executive's performance of the last Step's act: ok false
// means the motor refused it (no verified foreground — nothing was sent).
func (s *Session) Acted(at time.Time, ok bool) {
	if s.pending == SesNoAct {
		return
	}
	s.pending = SesNoAct
	if !ok {
		s.refused = true
		return
	}
	s.actAt = at
	s.acts++
}

// freshSince: the observation, when it was taken at least Settle after t
// (t zero: any observation within Fresh of now).
func (s *Session) freshSince(in SessionIn, t time.Time) *Seen {
	if in.Seen == nil {
		return nil
	}
	if t.IsZero() {
		if in.Now.Sub(in.Seen.At) > s.Fresh {
			return nil
		}
		return in.Seen
	}
	if in.Seen.At.Before(t.Add(s.Settle)) {
		return nil
	}
	return in.Seen
}

func (s *Session) act(a SessionAct, at screen.Point) SessionOut {
	s.pending = a
	return SessionOut{Owns: true, Act: a, At: at}
}

// end closes the relog: to the next state, with the typed end.
func (s *Session) end(now time.Time, next SessionState, why phase.Reason, detail string) SessionOut {
	e := &RelogEnd{Why: why, Phase: s.ph, Churned: s.ph >= AwaitMenu, Detail: detail, Took: now.Sub(s.began)}
	line := "relog " + why.String() + ": " + detail
	if why == phase.Completed {
		line = "relog done: " + detail
	}
	s.to(now, next, RelogNone, line)
	// The ending tick stays the session's (the next one starts clean) — except
	// under F10, where the executive's disengaged path runs exactly as before.
	return SessionOut{Owns: next != Disengaged, Ended: e}
}

// fail ends the relog where the world stands: intact (InGame — the pause menu
// may still be up, and is foreign now) or left behind (Attaching).
func (s *Session) fail(now time.Time, why phase.Reason, detail string) SessionOut {
	next := InGame
	if s.ph >= ClickPlay {
		next = Attaching
	}
	return s.end(now, next, why, detail)
}

// Step advances the session one tick.
func (s *Session) Step(in SessionIn) SessionOut {
	now := in.Now
	switch {
	case s.st == Stopped:
		return SessionOut{Owns: true, Wind: WindExit}
	case s.st == WindDown:
		return s.windDown(in)
	case s.windWhy != "" && s.st != Relogging:
		// A stop waits out a relog (the world is in flux); anywhere else the
		// wind-down begins now — held if the owner drives.
		if !in.Engaged {
			s.holdSay, s.saidAt, s.offSeen = HoldOwnerSay, time.Time{}, true
			s.windTo(now, true, s.windWhy+" (disengaged: the owner drives)")
		} else {
			s.windTo(now, false, s.windWhy)
		}
		return s.windDown(in)
	}
	if !in.Engaged {
		if s.st == Disengaged {
			return SessionOut{}
		}
		if s.st == Relogging {
			return s.end(now, Disengaged, phase.Preempted, "F10: the owner took control in "+s.ph.String())
		}
		s.to(now, Disengaged, RelogNone, "F10")
		return SessionOut{}
	}
	if s.st == Disengaged {
		s.to(now, Attaching, RelogNone, "F10: re-engaged")
	}
	switch s.st {
	case Attaching:
		if in.Valid {
			s.to(now, InGame, RelogNone, "valid world")
		}
		return SessionOut{}
	case InGame, Chicken:
		return SessionOut{}
	}

	// Relogging.
	if s.refused {
		s.refused = false
		switch s.ph {
		case AwaitWorld:
			s.acts++ // the Play retry was refused: no further retry, keep waiting
		case ClickPlay:
			// Out of the game: nothing to fall back to — retry the click
			// until the owner yields the desktop or the budget runs out.
			if now.Sub(s.phAt) >= s.PlayRefused {
				return s.fail(now, phase.Refused, "Play refused for "+s.PlayRefused.String()+": no foreground")
			}
			s.actAt = now // retry after the settle below
		default:
			return s.fail(now, phase.Refused, s.ph.String()+": foreground refused — the owner holds the desktop")
		}
	}
	switch s.ph {
	case OpenPause:
		return s.openPause(in)
	case ClickSaveExit:
		return s.clickSaveExit(in)
	case AwaitMenu:
		return s.awaitMenu(in)
	case ClickPlay:
		return s.clickPlay(in)
	case AwaitWorld:
		return s.awaitWorld(in)
	}
	return s.fail(now, phase.Precondition, "no relog phase")
}

// clearForEsc: the one condition an ESC is ever sent on.
func clearForEsc(r screen.Reading) bool { return r.Mode == screen.World && !r.Panels.Blocking() }

func (s *Session) openPause(in SessionIn) SessionOut {
	now := in.Now
	seen := s.freshSince(in, s.actAt)
	if seen != nil {
		r := Stable(*seen)
		if r.Sight.Has(screen.PauseMenu) {
			if r.Sight.Has(screen.SubPanel) {
				return s.fail(now, phase.Precondition, "a sub-panel covers the pause menu")
			}
			why := "pause menu seen"
			if s.acts == 0 {
				why = "pause menu already up (no ESC)"
			}
			s.to(now, Relogging, ClickSaveExit, why)
			return s.clickSaveExit(in)
		}
	}
	if s.acts > 0 && now.Sub(s.actAt) < s.PauseWait {
		return SessionOut{Owns: true} // waiting for the menu to rise
	}
	if s.acts >= 2 {
		return s.fail(now, phase.Deaf, "two ESCs and the pause menu never seen")
	}
	if !in.Focused {
		return s.fail(now, phase.Refused, "game unfocused — no ESC, no focus steal")
	}
	if seen == nil {
		if s.acts == 0 && now.Sub(s.phAt) < s.PauseWait {
			return SessionOut{Owns: true} // no fresh reading yet
		}
		return s.fail(now, phase.Precondition, "no fresh screen reading to judge the ESC on")
	}
	r := Stable(*seen)
	if !clearForEsc(r) {
		detail := "screen not clear for an ESC: " + r.String()
		if s.acts > 0 {
			detail = "pause menu not seen after the ESC; " + detail
		}
		return s.fail(now, phase.Precondition, detail)
	}
	return s.act(SesEsc, screen.Point{})
}

func (s *Session) clickSaveExit(in SessionIn) SessionOut {
	now := in.Now
	if s.acts == 0 {
		return s.act(SesClick, s.SaveExit)
	}
	seen := s.freshSince(in, s.actAt)
	menuUp := seen == nil || seen.Reading.Sight.Has(screen.PauseMenu)
	if !in.Valid {
		if s.invalidAt.IsZero() {
			s.invalidAt = now
		}
		switch {
		case !menuUp:
			s.to(now, Relogging, AwaitMenu, "pause menu gone, world unloading")
			return SessionOut{Owns: true}
		case now.Sub(s.invalidAt) >= s.UnloadGrace || now.Sub(s.phAt) >= s.SaveExitWait:
			s.to(now, Relogging, AwaitMenu, "world unloaded (pause menu still drawn)")
			return SessionOut{Owns: true}
		}
	} else {
		s.invalidAt = time.Time{}
	}
	if now.Sub(s.phAt) >= s.SaveExitWait {
		if seen != nil && !menuUp {
			return s.fail(now, phase.Deaf, "Save and Exit not taking: pause menu gone, world intact")
		}
		return s.fail(now, phase.Deaf, "Save and Exit not taking: pause menu still up")
	}
	if in.Valid && s.acts == 1 && seen != nil && menuUp && now.Sub(s.actAt) >= s.SaveExitRetry {
		return s.act(SesClick, s.SaveExit) // the click missed; the menu provably stands
	}
	return SessionOut{Owns: true}
}

func (s *Session) awaitMenu(in SessionIn) SessionOut {
	now := in.Now
	if in.Valid {
		if s.validAt.IsZero() {
			s.validAt = now
		}
		if now.Sub(s.validAt) >= s.WorldStable {
			return s.end(now, InGame, phase.Deaf, "Save and Exit bounced: the world came back before Play")
		}
		return SessionOut{Owns: true}
	}
	s.validAt = time.Time{}
	if now.Sub(s.phAt) >= s.MenuSettle {
		s.to(now, Relogging, ClickPlay, "character screen settled")
		return s.clickPlay(in)
	}
	return SessionOut{Owns: true}
}

func (s *Session) clickPlay(in SessionIn) SessionOut {
	now := in.Now
	if s.actAt.IsZero() || now.Sub(s.actAt) >= s.MenuSettle {
		return s.act(SesClick, s.Play)
	}
	if s.acts > 0 {
		if s.playAt.IsZero() {
			s.playAt = s.actAt
		}
		s.to(now, Relogging, AwaitWorld, "Play clicked")
	}
	return SessionOut{Owns: true}
}

func (s *Session) awaitWorld(in SessionIn) SessionOut {
	now := in.Now
	if in.Valid {
		if s.validAt.IsZero() {
			s.validAt = now
		}
		if now.Sub(s.validAt) >= s.WorldStable {
			if in.Seed != s.seed {
				return s.end(now, InGame, phase.Completed, fmt.Sprintf("new world seed %d→%d", s.seed, in.Seed))
			}
			return s.end(now, InGame, phase.Completed, fmt.Sprintf("world back, seed unchanged (%d)", in.Seed))
		}
		return SessionOut{Owns: true}
	}
	s.validAt = time.Time{}
	if now.Sub(s.playAt) >= s.WorldWait {
		return s.fail(now, phase.Timebox, fmt.Sprintf("world never returned %s after Play", s.WorldWait))
	}
	if s.acts == 0 && now.Sub(s.playAt) >= s.PlayRetry {
		return s.act(SesClick, s.Play) // once: the first click may have landed on a settling screen
	}
	return SessionOut{Owns: true}
}
