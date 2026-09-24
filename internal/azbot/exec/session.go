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
// tick outside a relog enters WindDown, where the normal loop keeps running.
// THE OWNER'S LADDER, one rung at a time (each logged `WINDDOWN rung=<name>`):
//
//	1 exit    in town ──▶ Stopped (exit). Outside town there is NO exit on
//	          quiet (relay R10 exited in an empty Maggot Lair, town=false; the
//	          owner: "town if easy, otherwise pause; death beyond that is fine")
//	2 recall  WindDown: the town road bids (Recall, class Recover — over
//	          Fight, under Survive; the sentinel keeps drinking) for the
//	          Recall budget (-winddown, 30s)
//	3 pause   WindDown/Pause, when the budget is spent, Recall abandoned (no
//	          tome / an empty one), or HP under the flee floor: Relogging's
//	          OpenPause (one ESC on a clear World, the menu seen within 2s,
//	          one retry; a panel up gets 3s for the janitor first) ──seen──▶
//	          Stopped with the pause menu LEFT UP (claimed: nobody closes it)
//	4 hold    WindDown/Hold: the pause not confirmed (or -pausefailsafe=false
//	          and the budget spent) — disengage and hold
//
//	Disengaged ──Stop──▶ WindDown/Hold ◀──F10── (owner drives, from any face)
//	                       │ safe (whoever brought him there): Stopped
//	                       │ every 30s: the hold reminder
//	                       └──F10 re-engaged──▶ WindDown (a fresh budget)
//
// Outside town the run is never Stopped but by rung 3, where the world is
// frozen under the pause menu (confirmed by sight) — however quiet the field.
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

	WindCap    = 30 * time.Second // the Recall budget (-winddown): not in town this long after the request → Pause (or Hold)
	WindClear  = 3 * time.Second  // WindDown/Pause: the screen not clear this long before the first ESC → Hold
	WindSay    = 30 * time.Second // the hold reminder's period
)

// WindAct is what a winding-down session asks of the executive this tick.
type WindAct uint8

const (
	WindNone  WindAct = iota
	WindTown          // bid the town road (the Recall activity) this tick
	WindHold          // the cap: stop the motor, end the holder, DISENGAGE — then hold
	WindExit          // safe: stop the motor and exit normally
	WindPause         // the pause rung: stop the feet and end the holder (the session's ESC follows)
)

func (a WindAct) String() string {
	switch a {
	case WindTown:
		return "town"
	case WindHold:
		return "hold"
	case WindExit:
		return "exit"
	case WindPause:
		return "pause"
	}
	return "-"
}

// Hold reminders, verbatim in the owner's log every WindSay.
const (
	HoldCapSay   = "WINDDOWN: could not reach safety — disengaged and holding; press F10 to resume or kill the process"
	HoldOwnerSay = "WINDDOWN: disengaged, the owner drives — holding; exits once in town; F10 resumes the wind-down"
	HoldPauseSay = "WINDDOWN: could not reach safety and the pause menu was not confirmed — disengaged and holding; press F10 to resume or kill the process"
	PausedSay    = "WINDDOWN: paused and exiting (menu left up)"
)

// windFace is WindDown's face: seeking safety (rung 2), pausing (rung 3) or
// holding (the motor disengaged).
type windFace uint8

const (
	windSeek windFace = iota
	windPause
	windHold
)

// SessionIn is one tick's evidence.
type SessionIn struct {
	Now     time.Time
	Engaged bool   // the F10 kill-switch
	Focused bool   // D2R owns the foreground (no ESC is ever sent unfocused)
	Valid   bool   // percept Snapshot.Valid
	Seed    uint64 // the map seed as the executive last fetched it
	Seen    *Seen  // the latest published screen observation (nil before the first)
	InTown  bool   // WindDown: percept Me.InTown (the one safe harbour)
	HPPct   int    // WindDown: percept Me.HPPct (under FleeFloor: the pause rung)
	// WindDown: the Recall activity gave up the town road (no tome, or three
	// dud casts — Withdraw.ride's empty-tome detector) and cools.
	RecallSpent bool
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
	WindCap, WindClear, WindSay                                        time.Duration
	// PauseFailsafe (-pausefailsafe): rung 3 is on. Off, a spent Recall
	// budget goes straight to Hold and the tome/HP triggers do nothing.
	PauseFailsafe bool
	// FleeFloor: HP% under which the wind-down stops riding and pauses (the
	// executive sets activity.FleeFloor; 0 = off).
	FleeFloor int
	// Rung hears each `WINDDOWN rung=<name> why=".."` line (nil = silent).
	Rung func(line string)
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
	windAt  time.Time // the budget's clock: the request, or the F10 that resumed it
	face    windFace  // WindDown's face (Hold: the motor is disengaged, nothing is driven)
	sought  bool      // the recall rung was announced for this face
	stilled bool      // WindDown/Pause: the feet were stopped and the holder ended
	paused  bool      // Stopped by the pause rung: the menu stays up, claimed
	offSeen bool      // Hold: a disengaged tick was seen (only then does engaged mean F10)
	holdSay string    // the reminder the hold repeats
	saidAt  time.Time // the last hold reminder
}

// NewSession starts Attaching with the documented defaults.
func NewSession() *Session {
	return &Session{
		Settle: SesSettle, Fresh: SesFresh, PauseWait: SesPauseWait, SaveExitRetry: SesSaveExitRetry,
		SaveExitWait: SesSaveExitWait, UnloadGrace: SesUnloadGrace, MenuSettle: SesMenuSettle,
		PlayRefused: SesPlayRefused, PlayRetry: SesPlayRetry, WorldWait: SesWorldWait, WorldStable: SesWorldStable,
		WindCap: WindCap, WindClear: WindClear, WindSay: WindSay,
		PauseFailsafe: true, SaveExit: SaveExitShot, Play: PlayShot,
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
// "WindDown/Pause", "WindDown/Hold".
func (s *Session) String() string {
	if s.st == Relogging {
		return s.st.String() + "/" + s.ph.String()
	}
	if s.st == WindDown {
		switch s.face {
		case windPause:
			return "WindDown/Pause"
		case windHold:
			return "WindDown/Hold"
		}
	}
	return s.st.String()
}

// Paused reports a run Stopped by the pause rung (the menu left up).
func (s *Session) Paused() bool { return s.st == Stopped && s.paused }

// Claims: the panels the session holds while it acts. The pause menu is the
// relog's road, so the janitor leaves it up while Relogging; the moment the
// relog ends it is foreign again and the janitor clicks Return to Game. The
// pause rung claims it too — while it rises and after, until the exit: the
// run ends with the menu up and nobody clicks Return to Game on it.
func (s *Session) Claims() screen.Panel {
	if s.st == Relogging || (s.st == WindDown && s.face == windPause) || s.Paused() {
		return screen.PauseMenu
	}
	return 0
}

func (s *Session) to(now time.Time, st SessionState, ph RelogPhase, why string) {
	from := s.String()
	s.st, s.ph, s.phAt, s.face, s.stilled = st, ph, now, windSeek, false
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

// windTo is to() for the wind-down's faces (Pause and Hold are faces, not
// relog phases).
func (s *Session) windTo(now time.Time, face windFace, why string) {
	from := s.String()
	s.st, s.ph, s.phAt, s.face, s.sought, s.stilled = WindDown, RelogNone, now, face, false, false
	s.actAt, s.acts, s.refused, s.pending = time.Time{}, 0, false, SesNoAct
	if s.Trace != nil {
		s.Trace(trace.Session(from, s.String(), why, s.Tick))
	}
}

// rung logs one step of the owner's ladder: `WINDDOWN rung=<name> why=".."`.
func (s *Session) rung(name, why string) {
	if s.Rung != nil {
		s.Rung(fmt.Sprintf("WINDDOWN rung=%s why=%q", name, why))
	}
}

// safe: the one condition the run may end on without the pause rung — a
// valid snapshot in town. A quiet field is NOT safe (relay R10: "safe: no
// monster within 40 for 3s" ended the run in the Maggot Lair and left her
// standing there); outside town the ladder is recall → pause → hold.
func (s *Session) safe(in SessionIn) (bool, string) {
	if in.Valid && in.InTown {
		return true, "safe: in town"
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

// holdOwner: F10 — the owner drives (from any face); the town road stops.
func (s *Session) holdOwner(now time.Time, why string) SessionOut {
	s.holdSay, s.saidAt, s.offSeen = HoldOwnerSay, time.Time{}, true
	s.windTo(now, windHold, why)
	s.rung("hold", why)
	return s.holdOut(now)
}

// holdCap: the last rung — the executive disengages on this answer; F10
// counts only after a disengaged tick has been seen (this tick is still
// engaged).
func (s *Session) holdCap(now time.Time, say, why string) SessionOut {
	s.holdSay, s.saidAt, s.offSeen = say, time.Time{}, false
	s.windTo(now, windHold, why)
	s.rung("hold", why)
	out := s.holdOut(now)
	out.Wind = WindHold
	return out
}

// windDown is one WindDown tick. Seeking, the session never owns it: the
// arbiter keeps fighting and the sentinel keeps drinking while the town road
// bids. Pausing, it owns the ticks of the ESC and its confirmation.
func (s *Session) windDown(in SessionIn) SessionOut {
	now := in.Now
	if ok, why := s.safe(in); ok {
		s.to(now, Stopped, RelogNone, why)
		s.rung("exit", why)
		return SessionOut{Owns: true, Wind: WindExit}
	}
	if s.face == windHold {
		if !in.Engaged {
			s.offSeen = true
		} else if s.offSeen {
			s.windAt, s.saidAt = now, time.Time{}
			s.windTo(now, windSeek, "F10: re-engaged — the wind-down resumes (a fresh "+s.WindCap.String()+")")
			return s.seek(in)
		}
		return s.holdOut(now)
	}
	if !in.Engaged {
		return s.holdOwner(now, "F10: the owner took control")
	}
	if s.face == windPause {
		return s.windPause(in)
	}
	return s.seek(in)
}

// seek: engaged and not yet safe — rung 2, the town road, until the budget is
// spent, Recall gives up, or the blood runs under the flee floor.
func (s *Session) seek(in SessionIn) SessionOut {
	now := in.Now
	field := in.Valid && !in.InTown
	why := ""
	switch {
	case now.Sub(s.windAt) >= s.WindCap:
		why = "recall budget " + s.WindCap.String() + " spent"
	case !s.PauseFailsafe:
		// Only the budget ends rung 2 without the pause rung: holding (the
		// motor disengaged — no drinks) on low blood would be worse than riding.
	case field && in.RecallSpent:
		why = "recall abandoned: no town portal (tome missing or empty)"
	case field && s.FleeFloor > 0 && in.HPPct > 0 && in.HPPct < s.FleeFloor:
		why = fmt.Sprintf("hp %d%% under the flee floor %d%%", in.HPPct, s.FleeFloor)
	}
	if why != "" {
		if !s.PauseFailsafe {
			return s.holdCap(now, HoldCapSay, why+" (-pausefailsafe=false): disengage and hold")
		}
		s.windTo(now, windPause, why+": pause the game")
		s.rung("pause", why)
		return s.windPause(in)
	}
	if !s.sought {
		s.sought = true
		s.rung("recall", fmt.Sprintf("%s: town portal, budget %s", s.windWhy, (s.WindCap-now.Sub(s.windAt)).Round(100*time.Millisecond)))
	}
	if field {
		return SessionOut{Wind: WindTown}
	}
	return SessionOut{}
}

// windPause is rung 3: Relogging's OpenPause ritual (pauseStep), then the exit
// with the menu left up — or, not confirmed, Hold.
func (s *Session) windPause(in SessionIn) SessionOut {
	now := in.Now
	if s.refused {
		return s.holdCap(now, HoldPauseSay, "pause refused: no foreground — disengage and hold")
	}
	p := s.pauseStep(in)
	switch {
	case p.up:
		s.paused = true
		s.to(now, Stopped, RelogNone, "paused: "+p.detail+" — menu left up")
		s.rung("exit", "paused: "+p.detail)
		return SessionOut{Owns: true, Wind: WindExit, Say: PausedSay}
	case p.wait && (s.acts > 0 || s.stilled):
		return SessionOut{Owns: true} // the menu may still rise
	case p.esc && !s.stilled:
		// First own the tick without input: the feet stop and the holder's
		// episode ends; the ESC goes on the next tick's reading.
		s.stilled = true
		return SessionOut{Owns: true, Wind: WindPause}
	case p.esc:
		return s.act(SesEsc, screen.Point{})
	}
	// Not ready (or failed). Before the first ESC the janitor gets WindClear to
	// clear a panel (and the owner to hand back the foreground): the loop runs.
	if s.acts == 0 && now.Sub(s.phAt) < s.WindClear {
		s.stilled = false
		return SessionOut{}
	}
	return s.holdCap(now, HoldPauseSay, "pause not confirmed: "+p.detail+" — disengage and hold")
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
			why := s.windWhy + " (disengaged: the owner drives)"
			s.holdSay, s.saidAt, s.offSeen = HoldOwnerSay, time.Time{}, true
			s.windTo(now, windHold, why)
			s.rung("hold", why)
		} else {
			s.windTo(now, windSeek, s.windWhy)
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

// pauseVerdict is one tick of the OpenPause ritual, shared by Relogging's
// OpenPause and the wind-down's pause rung: exactly one ESC, only on a fresh
// stable Reading that is World with nothing blocking; the pause menu judged
// by sight within PauseWait; one retry on the same check; never a third.
type pauseVerdict struct {
	up     bool         // the pause menu is seen (detail says how)
	sub    bool         // ... with a sub-panel over it
	esc    bool         // send the ESC now
	wait   bool         // hold on: the menu may still rise, or no reading yet
	why    phase.Reason // otherwise: not ready / failed, typed
	detail string
}

func (s *Session) pauseStep(in SessionIn) pauseVerdict {
	now := in.Now
	seen := s.freshSince(in, s.actAt)
	if seen != nil {
		r := Stable(*seen)
		if r.Sight.Has(screen.PauseMenu) {
			why := "pause menu seen"
			if s.acts == 0 {
				why = "pause menu already up (no ESC)"
			}
			return pauseVerdict{up: true, sub: r.Sight.Has(screen.SubPanel), detail: why}
		}
	}
	if s.acts > 0 && now.Sub(s.actAt) < s.PauseWait {
		return pauseVerdict{wait: true} // waiting for the menu to rise
	}
	if s.acts >= 2 {
		return pauseVerdict{why: phase.Deaf, detail: "two ESCs and the pause menu never seen"}
	}
	if !in.Focused {
		return pauseVerdict{why: phase.Refused, detail: "game unfocused — no ESC, no focus steal"}
	}
	if seen == nil {
		if s.acts == 0 && now.Sub(s.phAt) < s.PauseWait {
			return pauseVerdict{wait: true} // no fresh reading yet
		}
		return pauseVerdict{why: phase.Precondition, detail: "no fresh screen reading to judge the ESC on"}
	}
	r := Stable(*seen)
	if !clearForEsc(r) {
		detail := "screen not clear for an ESC: " + r.String()
		if s.acts > 0 {
			detail = "pause menu not seen after the ESC; " + detail
		}
		return pauseVerdict{why: phase.Precondition, detail: detail}
	}
	return pauseVerdict{esc: true}
}

func (s *Session) openPause(in SessionIn) SessionOut {
	now := in.Now
	p := s.pauseStep(in)
	switch {
	case p.up && p.sub:
		return s.fail(now, phase.Precondition, "a sub-panel covers the pause menu")
	case p.up:
		s.to(now, Relogging, ClickSaveExit, p.detail)
		return s.clickSaveExit(in)
	case p.wait:
		return SessionOut{Owns: true}
	case p.esc:
		return s.act(SesEsc, screen.Point{})
	}
	return s.fail(now, p.why, p.detail)
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
