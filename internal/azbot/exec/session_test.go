package exec

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// world is a scripted game for the session: a fake clock, the snapshot's
// validity and seed, what the screen shows, and how the game answers each act.
type world struct {
	t       *testing.T
	now     time.Time
	engaged bool
	focused bool
	valid   bool
	seed    uint64
	pause   bool         // the pause menu is drawn
	panels  screen.Panel // anything else on screen
	refuse  bool         // the motor finds no foreground
	town    bool         // WindDown: the character stands in town
	hot     bool         // WindDown: a living monster within WindRadius
	hp      int          // WindDown: the character's HP%
	spent   bool         // WindDown: Recall gave up the town road (no tome / an empty one)

	onEsc   func(w *world)
	onClick func(w *world, p screen.Point)
	later   []event

	ses   *Session
	lines []string
	acts  []string
	ends  []RelogEnd
	owned int      // ticks the session held
	winds []string // WindDown: "<act>@<seconds since newWorld>" for every act but town
	towns int      // WindDown: ticks that asked for the town road
	says  []string // hold reminders
	rungs []string // WINDDOWN rung=<name> lines
	t0    time.Time
}

type event struct {
	at time.Time
	do func(w *world)
}

func newWorld(t *testing.T) *world {
	w := &world{t: t, now: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
		engaged: true, focused: true, valid: true, seed: 111, hp: 80}
	w.ses = NewSession()
	w.ses.FleeFloor = 33 // activity.FleeFloor (the executive sets it; an import cycle keeps it out of here)
	w.ses.Trace = func(l string) { w.lines = append(w.lines, l) }
	w.ses.Rung = func(l string) { w.rungs = append(w.rungs, l) }
	w.run(time.Second) // Attaching → InGame
	w.t0 = w.now
	return w
}

// after schedules a change of the game d from now.
func (w *world) after(d time.Duration, do func(w *world)) {
	w.later = append(w.later, event{w.now.Add(d), do})
}

// seen photographs the scripted screen: the Tracker's belief agrees with the
// frame (a steady screen), so Stable == the frame.
func (w *world) seen() *Seen {
	mode := screen.World
	if !w.valid {
		mode = screen.Unknown
	}
	p := w.panels
	if w.pause {
		p |= screen.PauseMenu
	}
	r := screen.Reading{Mode: mode, Panels: p, Sight: p, Evidence: map[screen.Panel]string{}, Close: map[screen.Panel]screen.Point{}}
	if w.pause {
		r.Close[screen.PauseMenu] = screen.Point{X: 960, Y: 685}
	}
	return &Seen{At: w.now, State: r.State(), Reading: r}
}

// tick is one 100ms executive tick: the game's scheduled changes, then the session.
func (w *world) tick() SessionOut {
	w.now = w.now.Add(100 * time.Millisecond)
	var keep []event
	for _, e := range w.later {
		if !w.now.Before(e.at) {
			e.do(w)
		} else {
			keep = append(keep, e)
		}
	}
	w.later = keep
	w.ses.Tick++
	out := w.ses.Step(SessionIn{Now: w.now, Engaged: w.engaged, Focused: w.focused, Valid: w.valid, Seed: w.seed, Seen: w.seen(),
		InTown: w.valid && w.town, Hot: w.valid && w.hot, HPPct: w.hp, RecallSpent: w.spent})
	if out.Owns {
		w.owned++
	}
	switch out.Wind {
	case WindNone:
	case WindTown:
		w.towns++
	default:
		w.winds = append(w.winds, fmt.Sprintf("%s@%.1f", out.Wind, w.now.Sub(w.t0).Seconds()))
	}
	if out.Say != "" {
		w.says = append(w.says, out.Say)
	}
	if out.Ended != nil {
		w.ends = append(w.ends, *out.Ended)
	}
	switch out.Act {
	case SesEsc:
		w.acts = append(w.acts, "esc")
		if !w.refuse && w.onEsc != nil {
			w.onEsc(w)
		}
		w.ses.Acted(w.now, !w.refuse)
	case SesClick:
		w.acts = append(w.acts, fmt.Sprintf("click %d,%d", out.At.X, out.At.Y))
		if !w.refuse && w.onClick != nil {
			w.onClick(w, out.At)
		}
		w.ses.Acted(w.now, !w.refuse)
	}
	return out
}

func (w *world) run(d time.Duration) {
	for end := w.now.Add(d); w.now.Before(end); {
		w.tick()
	}
}

// runUntil ticks until the session leaves Relogging (bounded).
func (w *world) runUntil(max time.Duration) {
	w.t.Helper()
	for end := w.now.Add(max); w.now.Before(end); {
		w.tick()
		if !w.ses.Relogging() {
			return
		}
	}
	w.t.Fatalf("still %s after %s; lines:\n%s", w.ses, max, strings.Join(w.lines, "\n"))
}

func (w *world) request(why string) {
	w.t.Helper()
	if ok, r := w.ses.Request(w.now, why, w.seed, w.seen()); !ok {
		w.t.Fatalf("request refused: %s", r)
	}
}

// path is the from→to chain of the T L=session lines.
func (w *world) path() []string {
	var out []string
	for _, l := range w.lines {
		f := strings.Fields(l)
		out = append(out, strings.TrimPrefix(f[2], "from=")+">"+strings.TrimPrefix(f[3], "to="))
	}
	return out
}

func (w *world) escs() int {
	n := 0
	for _, a := range w.acts {
		if a == "esc" {
			n++
		}
	}
	return n
}

// The game as it behaves: ESC raises the pause menu, Save and Exit drops it
// and unloads the world, Play loads a new seed.
func realGame(w *world) {
	w.onEsc = func(w *world) { w.after(300*time.Millisecond, func(w *world) { w.pause = true }) }
	w.onClick = func(w *world, p screen.Point) {
		switch p {
		case SaveExitShot:
			if w.pause {
				w.after(400*time.Millisecond, func(w *world) { w.pause, w.valid = false, false })
			}
		case PlayShot:
			if !w.valid {
				w.after(6*time.Second, func(w *world) { w.valid, w.seed = true, 222 })
			}
		}
	}
}

func wantEnd(t *testing.T, w *world, why phase.Reason, ph RelogPhase, churned bool) RelogEnd {
	t.Helper()
	if len(w.ends) != 1 {
		t.Fatalf("ends = %+v, want exactly one", w.ends)
	}
	e := w.ends[0]
	if e.Why != why || e.Phase != ph || e.Churned != churned {
		t.Fatalf("end = %+v, want why=%s phase=%s churned=%v", e, why, ph, churned)
	}
	return e
}

func TestSessionHappyPath(t *testing.T) {
	w := newWorld(t)
	realGame(w)
	if w.ses.String() != "InGame" || w.ses.Claims() != 0 {
		t.Fatalf("start: %s claims=%s", w.ses, w.ses.Claims())
	}
	w.request("corpse far")
	if w.ses.Claims() != screen.PauseMenu {
		t.Fatalf("relogging claims %s, want the pause menu", w.ses.Claims())
	}
	w.runUntil(time.Minute)
	want := []string{
		"Attaching>InGame",
		"InGame>Relogging/OpenPause",
		"Relogging/OpenPause>Relogging/ClickSaveExit",
		"Relogging/ClickSaveExit>Relogging/AwaitMenu",
		"Relogging/AwaitMenu>Relogging/ClickPlay",
		"Relogging/ClickPlay>Relogging/AwaitWorld",
		"Relogging/AwaitWorld>InGame",
	}
	if got := w.path(); !reflect.DeepEqual(got, want) {
		t.Fatalf("path\n got %v\nwant %v", got, want)
	}
	if want := []string{"esc", "click 958,822", "click 922,822"}; !reflect.DeepEqual(w.acts, want) {
		t.Fatalf("acts %v, want %v", w.acts, want)
	}
	t.Log("\n" + strings.Join(w.lines, "\n"))
	e := wantEnd(t, w, phase.Completed, AwaitWorld, true)
	if !e.OK() || !strings.Contains(e.Detail, "111→222") {
		t.Fatalf("end %+v", e)
	}
	if last := w.lines[len(w.lines)-1]; !strings.HasPrefix(last, `T L=session from=Relogging/AwaitWorld to=InGame why="relog done: new world seed 111→222" tick=`) {
		t.Fatalf("last line %q", last)
	}
	if w.ses.Claims() != 0 {
		t.Fatal("claims must drop with the relog")
	}
}

func TestSessionPauseAlreadyUpNoEsc(t *testing.T) {
	w := newWorld(t)
	realGame(w)
	w.pause = true
	w.request("drill")
	w.runUntil(time.Minute)
	if w.escs() != 0 {
		t.Fatalf("acts %v: the pause menu was already up, no ESC", w.acts)
	}
	wantEnd(t, w, phase.Completed, AwaitWorld, true)
}

func TestSessionRequestRefused(t *testing.T) {
	w := newWorld(t)
	w.panels = screen.Inventory
	if ok, why := w.ses.Request(w.now, "x", 1, w.seen()); ok || !strings.Contains(why, "inventory") {
		t.Fatalf("request with the bag up: ok=%v why=%q", ok, why)
	}
	w.panels = 0
	if ok, why := w.ses.Request(w.now.Add(5*time.Second), "x", 1, w.seen()); ok || !strings.Contains(why, "stale") {
		t.Fatalf("stale reading: ok=%v why=%q", ok, why)
	}
	if ok, why := w.ses.Request(w.now, "x", 1, nil); ok || why == "" {
		t.Fatalf("no reading: ok=%v why=%q", ok, why)
	}
	if len(w.lines) != 1 {
		t.Fatalf("a refused request writes no transition: %v", w.lines)
	}
}

func TestSessionPauseNotAppearing(t *testing.T) {
	cases := []struct {
		name      string
		onEsc     func(w *world)
		wantEscs  int
		why       phase.Reason
		wantInWhy string
	}{
		// A deaf game: two ESCs, each on a verified-clear screen, then give up.
		{"deaf", nil, 2, phase.Deaf, "two ESCs"},
		// The first ESC landed on something else (a panel rose): the second is
		// withheld — the screen is no longer clear.
		{"panel rose", func(w *world) { w.panels = screen.Chat }, 1, phase.Precondition, "not clear"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.onEsc = c.onEsc
			w.request("x")
			w.runUntil(time.Minute)
			if w.escs() != c.wantEscs {
				t.Fatalf("escs = %d (%v), want %d", w.escs(), w.acts, c.wantEscs)
			}
			e := wantEnd(t, w, c.why, OpenPause, false)
			if !strings.Contains(e.Detail, c.wantInWhy) {
				t.Fatalf("detail %q, want %q", e.Detail, c.wantInWhy)
			}
			if w.ses.State() != InGame {
				t.Fatalf("state %s, want InGame", w.ses)
			}
			// Never an ESC loop: nothing more is sent however long it runs.
			n := len(w.acts)
			w.run(30 * time.Second)
			if len(w.acts) != n {
				t.Fatalf("acts after the failure: %v", w.acts[n:])
			}
			if e.Took > 5*time.Second {
				t.Fatalf("took %s: the pause wait is bounded", e.Took)
			}
		})
	}
}

func TestSessionUnfocusedSendsNoEsc(t *testing.T) {
	w := newWorld(t)
	w.focused = false
	w.request("x")
	w.runUntil(time.Minute)
	if len(w.acts) != 0 {
		t.Fatalf("acts %v: unfocused, nothing is sent", w.acts)
	}
	wantEnd(t, w, phase.Refused, OpenPause, false)
}

func TestSessionForegroundRefused(t *testing.T) {
	w := newWorld(t)
	w.refuse = true
	w.request("x")
	w.runUntil(time.Minute)
	if w.escs() != 1 {
		t.Fatalf("acts %v", w.acts)
	}
	e := wantEnd(t, w, phase.Refused, OpenPause, false)
	if !strings.Contains(e.Detail, "foreground refused") {
		t.Fatalf("detail %q", e.Detail)
	}
}

// Save and Exit not taking: one retry while the menu provably stands, then the
// relog fails back to InGame with the pause menu still up — foreign now, so
// the janitor's gate clicks Return to Game.
func TestSessionSaveExitNotTaking(t *testing.T) {
	w := newWorld(t)
	realGame(w)
	w.onClick = nil // the click lands nowhere
	w.request("x")
	w.runUntil(time.Minute)
	if want := []string{"esc", "click 958,822", "click 958,822"}; !reflect.DeepEqual(w.acts, want) {
		t.Fatalf("acts %v, want %v", w.acts, want)
	}
	e := wantEnd(t, w, phase.Deaf, ClickSaveExit, false)
	if !strings.Contains(e.Detail, "pause menu still up") {
		t.Fatalf("detail %q", e.Detail)
	}
	if w.ses.State() != InGame || !w.pause {
		t.Fatalf("state %s pause=%v", w.ses, w.pause)
	}
	// While relogging the gate left the pause menu alone; now it is foreign.
	g := Gate(Stable(*w.seen()), HolderNeeds{Mode: ModeOf(screen.World), Claims: w.ses.Claims()})
	if g.Open || g.Action.Kind != screen.ActClick || g.Action.X != 960 || g.Action.Y != 685 {
		t.Fatalf("gate after the failure: %+v, want a click on Return to Game", g)
	}
}

func TestSessionGateLeavesClaimedPause(t *testing.T) {
	w := newWorld(t)
	realGame(w)
	w.request("x")
	for i := 0; i < 20 && w.ses.Phase() != ClickSaveExit; i++ {
		w.tick()
	}
	g := Gate(Stable(*w.seen()), HolderNeeds{Mode: AnyMode, Claims: w.ses.Claims()})
	if !g.Open {
		t.Fatalf("gate while relogging: %+v — the session claims the pause menu", g)
	}
}

func TestSessionWorldNeverReturns(t *testing.T) {
	w := newWorld(t)
	realGame(w)
	w.onClick = func(w *world, p screen.Point) {
		if p == SaveExitShot {
			w.after(400*time.Millisecond, func(w *world) { w.pause, w.valid = false, false })
		}
	}
	w.request("x")
	w.runUntil(2 * time.Minute)
	if want := []string{"esc", "click 958,822", "click 922,822", "click 922,822"}; !reflect.DeepEqual(w.acts, want) {
		t.Fatalf("acts %v, want %v (one Play retry)", w.acts, want)
	}
	e := wantEnd(t, w, phase.Timebox, AwaitWorld, true)
	if w.ses.State() != Attaching {
		t.Fatalf("state %s: out of the game, the session waits Attaching", w.ses)
	}
	if e.Took > time.Minute {
		t.Fatalf("took %s", e.Took)
	}
	// The owner loads a game by hand: the session attaches.
	w.valid, w.seed = true, 333
	w.tick()
	if w.ses.State() != InGame {
		t.Fatalf("state %s after a valid world", w.ses)
	}
}

func TestSessionSaveExitBounced(t *testing.T) {
	w := newWorld(t)
	realGame(w)
	w.onClick = func(w *world, p screen.Point) {
		if p == SaveExitShot {
			w.after(400*time.Millisecond, func(w *world) { w.pause, w.valid = false, false })
			w.after(1500*time.Millisecond, func(w *world) { w.valid = true })
		}
	}
	w.request("x")
	w.runUntil(time.Minute)
	e := wantEnd(t, w, phase.Deaf, AwaitMenu, true)
	if !strings.Contains(e.Detail, "bounced") || w.ses.State() != InGame {
		t.Fatalf("end %+v state %s", e, w.ses)
	}
	if n := len(w.acts); w.acts[n-1] != "click 958,822" {
		t.Fatalf("acts %v: no Play into a live world", w.acts)
	}
}

func TestSessionF10DuringRelog(t *testing.T) {
	for _, at := range []RelogPhase{OpenPause, ClickSaveExit, AwaitMenu, AwaitWorld} {
		t.Run(at.String(), func(t *testing.T) {
			w := newWorld(t)
			realGame(w)
			w.request("x")
			for i := 0; i < 400 && w.ses.Phase() != at; i++ {
				w.tick()
			}
			if w.ses.Phase() != at {
				t.Fatalf("never reached %s (%s)", at, w.ses)
			}
			w.engaged = false
			out := w.tick()
			if out.Owns || out.Act != SesNoAct {
				t.Fatalf("F10 tick: %+v — the disengaged path must run as before", out)
			}
			e := wantEnd(t, w, phase.Preempted, at, at >= AwaitMenu)
			if !strings.Contains(e.Detail, "F10") {
				t.Fatalf("detail %q", e.Detail)
			}
			if w.ses.State() != Disengaged || w.ses.Claims() != 0 {
				t.Fatalf("state %s claims %s", w.ses, w.ses.Claims())
			}
			n := len(w.acts)
			w.run(time.Minute)
			if len(w.acts) != n || len(w.ends) != 1 {
				t.Fatalf("acted while disengaged: %v", w.acts[n:])
			}
			// Re-engaged: Attaching, then InGame once a valid world is read.
			w.engaged = true
			w.valid = false
			w.tick()
			if w.ses.State() != Attaching {
				t.Fatalf("re-engaged into %s", w.ses)
			}
			w.valid = true
			w.tick()
			if w.ses.State() != InGame {
				t.Fatalf("state %s", w.ses)
			}
			if !strings.Contains(strings.Join(w.lines, "\n"), `to=Disengaged why="relog preempted: F10`) {
				t.Fatalf("lines:\n%s", strings.Join(w.lines, "\n"))
			}
		})
	}
}

func TestSessionPlayRefusedRetries(t *testing.T) {
	w := newWorld(t)
	realGame(w)
	w.request("x")
	for i := 0; i < 400 && w.ses.Phase() != ClickPlay; i++ {
		w.tick()
		if w.ses.Phase() == AwaitMenu {
			w.refuse = true
		}
	}
	w.run(5 * time.Second)
	w.refuse = false
	w.runUntil(time.Minute)
	plays := 0
	for _, a := range w.acts {
		if a == "click 922,822" {
			plays++
		}
	}
	if plays < 2 {
		t.Fatalf("acts %v: a refused Play is retried", w.acts)
	}
	wantEnd(t, w, phase.Completed, AwaitWorld, true)
}

func TestSessionStrings(t *testing.T) {
	for st := Attaching; st <= Stopped; st++ {
		if st.String() == "?" {
			t.Fatalf("state %d unnamed", st)
		}
	}
	for ph := RelogNone; ph <= AwaitWorld; ph++ {
		if ph.String() == "?" {
			t.Fatalf("phase %d unnamed", ph)
		}
	}
}
