package exec

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// THE SAFE END (relay R4: -seconds 240 expired mid-fight in the Dry Hills and
// the process exited, leaving the character undriven). Pure table tests of the
// Session's WindDown: the run ends only in town or in a field quiet for 3s;
// otherwise the town road bids, and after the budget the motor is disengaged
// and the process holds — never exiting while a monster is near. TestWindDown
// runs the -pausefailsafe=false ladder (2 → Hold) with the old 120s budget;
// TestWindDownPause runs the owner's full ladder at the defaults.

// windWorld: an engaged session InGame, the stop requested at t0.
func windWorld(t *testing.T, town, hot bool) *world {
	w := newWorld(t)
	w.town, w.hot = town, hot
	w.ses.Stop(w.now, "time budget spent")
	return w
}

func (w *world) stopped() bool { return w.ses.State() == Stopped }

func (w *world) wantPath(want ...string) {
	w.t.Helper()
	if got := w.path(); !reflect.DeepEqual(got, want) {
		w.t.Fatalf("path %v, want %v\nlines:\n%s", got, want, strings.Join(w.lines, "\n"))
	}
}

// holdWorld: windWorld on the -pausefailsafe=false ladder, -winddown 120s.
func holdWorld(t *testing.T, town, hot bool) *world {
	w := windWorld(t, town, hot)
	w.ses.PauseFailsafe, w.ses.WindCap = false, 120*time.Second
	return w
}

func TestWindDown(t *testing.T) {
	cases := []struct {
		name string
		town bool // at the request
		hot  bool
		// script runs after the request, ticking the world; it returns the
		// wanted winds (WindHold/WindExit acts with their time).
		script func(w *world)
		winds  []string
		path   []string
		towns  bool // the town road was asked for
		says   []string
	}{{
		name:   "in town: exit on the first tick",
		town:   true,
		script: func(w *world) { w.tick() },
		winds:  []string{"exit@0.1"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>Stopped"},
	}, {
		name: "field quiet 3s: exit at 3s, not before",
		script: func(w *world) {
			w.run(2900 * time.Millisecond)
			if w.stopped() {
				w.t.Fatalf("stopped before 3s of quiet")
			}
			w.tick()
			w.tick()
		},
		winds: []string{"exit@3.1"},
		path:  []string{"Attaching>InGame", "InGame>WindDown", "WindDown>Stopped"},
		towns: true, // the road home bids while the quiet proves itself
	}, {
		name: "field hot: the town road, then town: exit",
		hot:  true,
		script: func(w *world) {
			w.run(20 * time.Second)
			if w.stopped() || w.towns < 190 {
				w.t.Fatalf("hot field: %s, %d town ticks", w.ses, w.towns)
			}
			w.hot, w.town = false, true // the portal took him home
			w.tick()
		},
		winds: []string{"exit@20.1"},
		path:  []string{"Attaching>InGame", "InGame>WindDown", "WindDown>Stopped"},
		towns: true,
	}, {
		name: "a monster returning restarts the quiet clock",
		hot:  true,
		script: func(w *world) {
			w.run(time.Second)
			w.hot = false
			w.run(2 * time.Second)
			w.hot = true // back inside 40 before the 3s were up
			w.tick()
			w.hot = false
			w.run(2900 * time.Millisecond)
			if w.stopped() {
				w.t.Fatalf("stopped on an interrupted quiet")
			}
			w.run(200 * time.Millisecond)
		},
		winds: []string{"exit@6.2"},
		path:  []string{"Attaching>InGame", "InGame>WindDown", "WindDown>Stopped"},
		towns: true,
	}, {
		name: "invalid snapshot is never safe",
		script: func(w *world) {
			w.valid = false
			w.run(10 * time.Second)
			if w.stopped() {
				w.t.Fatalf("stopped blind")
			}
		},
		path: []string{"Attaching>InGame", "InGame>WindDown"},
		// the first tick of the request was valid and quiet: one town ask
		towns: false,
	}, {
		name: "120s hot: disengage and hold, remind every 30s, never exit while hot",
		hot:  true,
		script: func(w *world) {
			w.run(119900 * time.Millisecond)
			if len(w.winds) != 0 {
				w.t.Fatalf("early: %v", w.winds)
			}
			w.tick() // 120.0: the cap
			w.engaged = false
			w.run(95 * time.Second) // held: reminders at 120, 150, 180, 210
			if w.stopped() {
				w.t.Fatalf("exited while hot")
			}
		},
		winds: []string{"hold@120.0"},
		path:  []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Hold"},
		towns: true,
		says:  []string{HoldCapSay, HoldCapSay, HoldCapSay, HoldCapSay},
	}, {
		name: "held after the cap: the field goes quiet — exit",
		hot:  true,
		script: func(w *world) {
			w.run(120 * time.Second)
			w.engaged = false
			w.run(10 * time.Second)
			w.hot = false
			w.run(3100 * time.Millisecond)
		},
		winds: []string{"hold@120.0", "exit@133.1"},
		path:  []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Hold", "WindDown/Hold>Stopped"},
		towns: true,
		says:  []string{HoldCapSay},
	}, {
		name: "F10 during wind-down: hold, no town road; F10 again resumes with a fresh cap",
		hot:  true,
		script: func(w *world) {
			w.run(10 * time.Second)
			w.engaged = false
			w.run(40 * time.Second)
			towns := w.towns
			w.run(time.Second)
			if w.towns != towns {
				w.t.Fatalf("the town road bid while the owner drives")
			}
			w.engaged = true // 51s: resume — the cap now falls at 171s, not 120s
			w.run(119 * time.Second)
			if len(w.winds) != 0 {
				w.t.Fatalf("the old cap held: %v", w.winds)
			}
			w.run(2 * time.Second)
		},
		winds: []string{"hold@171.1"},
		path: []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Hold",
			"WindDown/Hold>WindDown", "WindDown>WindDown/Hold"},
		towns: true,
		says:  []string{HoldOwnerSay, HoldOwnerSay, HoldCapSay},
	}, {
		name: "held after the cap: F10 resumes the wind-down (the cap's own engaged tick does not)",
		hot:  true,
		script: func(w *world) {
			w.run(120 * time.Second)
			w.run(time.Second) // the motor not yet disengaged: still held
			if w.ses.String() != "WindDown/Hold" {
				w.t.Fatalf("resumed without F10: %s", w.ses)
			}
			w.engaged = false
			w.run(5 * time.Second)
			w.engaged = true // 126.1: F10
			w.tick()
			w.hot, w.town = false, true
			w.tick()
		},
		winds: []string{"hold@120.0", "exit@126.2"},
		path: []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Hold",
			"WindDown/Hold>WindDown", "WindDown>Stopped"},
		towns: true,
		says:  []string{HoldCapSay},
	}, {
		name: "F10 during wind-down: the owner walks him to town — exit",
		hot:  true,
		script: func(w *world) {
			w.run(5 * time.Second)
			w.engaged = false
			w.run(5 * time.Second)
			w.hot, w.town = false, true
			w.tick()
		},
		winds: []string{"exit@10.1"},
		path:  []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Hold", "WindDown/Hold>Stopped"},
		towns: true,
		says:  []string{HoldOwnerSay},
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := holdWorld(t, c.town, c.hot)
			c.script(w)
			if !reflect.DeepEqual(w.winds, c.winds) && !(len(w.winds) == 0 && len(c.winds) == 0) {
				t.Fatalf("winds %v, want %v\nlines:\n%s", w.winds, c.winds, strings.Join(w.lines, "\n"))
			}
			w.wantPath(c.path...)
			if (w.towns > 0) != c.towns {
				t.Fatalf("town ticks %d, want any=%v", w.towns, c.towns)
			}
			if !reflect.DeepEqual(w.says, c.says) && !(len(w.says) == 0 && len(c.says) == 0) {
				t.Fatalf("says %q, want %q", w.says, c.says)
			}
			if len(w.acts) != 0 {
				t.Fatalf("the wind-down sent session input: %v", w.acts)
			}
		})
	}
}

// The request's reason is the transition's why: the owner's grep target.
func TestWindDownTraceLine(t *testing.T) {
	w := windWorld(t, false, true)
	w.tick()
	want := `T L=session from=InGame to=WindDown why="time budget spent"`
	if got := w.lines[len(w.lines)-1]; !strings.HasPrefix(got, want) {
		t.Fatalf("line %q, want prefix %q", got, want)
	}
	if w.ses.String() != "WindDown" || !w.ses.Winding() {
		t.Fatalf("ses=%s winding=%v", w.ses, w.ses.Winding())
	}
	// A second request changes nothing.
	if w.ses.Stop(w.now, "owner stop") {
		t.Fatal("a second stop was taken")
	}
}

// Winding down, the session owns no tick (the loop fights on) and refuses a relog.
func TestWindDownKeepsTheLoopAndRefusesRelog(t *testing.T) {
	w := windWorld(t, false, true)
	owned := w.owned
	w.run(5 * time.Second)
	if w.owned != owned {
		t.Fatalf("the session held %d wind-down ticks", w.owned-owned)
	}
	if ok, _ := w.ses.Request(w.now, "corpse far", w.seed, w.seen()); ok {
		t.Fatal("a relog began during the wind-down")
	}
}

// A stop during a relog waits the relog out, then winds down in the new world.
func TestWindDownWaitsOutARelog(t *testing.T) {
	w := newWorld(t)
	realGame(w)
	w.hot = true
	w.request("corpse far")
	w.tick()
	w.ses.Stop(w.now, "time budget spent")
	w.runUntil(time.Minute)
	w.tick()
	if w.ses.State() != WindDown {
		t.Fatalf("after the relog: %s", w.ses)
	}
	p := w.path()
	if p[len(p)-2] != "Relogging/AwaitWorld>InGame" || p[len(p)-1] != "InGame>WindDown" {
		t.Fatalf("path %v", p)
	}
}

// Stopped while the owner drives (F10, or a -disengaged run): hold, exit once safe.
func TestWindDownWhileDisengaged(t *testing.T) {
	w := newWorld(t)
	w.hot = true
	w.engaged = false
	w.tick()
	w.ses.Stop(w.now, "time budget spent")
	w.run(time.Minute)
	if w.ses.String() != "WindDown/Hold" {
		t.Fatalf("ses %s", w.ses)
	}
	if w.towns != 0 {
		t.Fatalf("the town road bid while disengaged")
	}
	w.hot, w.town = false, true
	w.tick()
	if !w.stopped() {
		t.Fatalf("in town and still %s", w.ses)
	}
	w.wantPath("Attaching>InGame", "InGame>Disengaged", "Disengaged>WindDown/Hold", "WindDown/Hold>Stopped")
	if len(w.says) != 2 || w.says[0] != HoldOwnerSay {
		t.Fatalf("says %q", w.says)
	}
}

// The -disengaged start names itself on the line; F10 then attaches as usual.
func TestSessionDisengagedStart(t *testing.T) {
	s := NewSession()
	var lines []string
	s.Trace = func(l string) { lines = append(lines, l) }
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	s.Disengage(now, "-disengaged start")
	out := s.Step(SessionIn{Now: now.Add(time.Second), Valid: true})
	if s.State() != Disengaged || out.Owns || out.Act != SesNoAct {
		t.Fatalf("state %s out %+v", s, out)
	}
	s.Step(SessionIn{Now: now.Add(2 * time.Second), Engaged: true, Valid: true})
	s.Step(SessionIn{Now: now.Add(3 * time.Second), Engaged: true, Valid: true})
	if s.State() != InGame {
		t.Fatalf("after F10: %s", s)
	}
	if len(lines) != 3 || !strings.Contains(lines[0], `to=Disengaged why="-disengaged start"`) {
		t.Fatalf("lines %q", lines)
	}
}

// THE OWNER'S LADDER (rungs 1–4) at the defaults: -winddown 30s,
// -pausefailsafe on, the flee floor 33. The pause rung is Relogging's
// OpenPause: one ESC on a clear World, the menu seen within 2s, one retry;
// seen, the run exits with the menu LEFT UP — claimed, never closed.

// untilStopped ticks until the session stops (bounded).
func (w *world) untilStopped(max time.Duration) {
	w.t.Helper()
	for end := w.now.Add(max); w.now.Before(end); {
		w.tick()
		if w.stopped() {
			return
		}
	}
	w.t.Fatalf("still %s after %s; lines:\n%s", w.ses, max, strings.Join(w.lines, "\n"))
}

var rungName = regexp.MustCompile(`^WINDDOWN rung=(\w+) why=".+"$`)

func (w *world) rungNames() []string {
	var out []string
	for _, l := range w.rungs {
		m := rungName.FindStringSubmatch(l)
		if m == nil {
			w.t.Fatalf("rung line %q", l)
		}
		out = append(out, m[1])
	}
	return out
}

// janitorLeaves: with the session's claims, the janitor takes no action on
// the scripted screen (and without them it would — the claim is load-bearing).
func janitorLeaves(t *testing.T, w *world) {
	t.Helper()
	n := NoHolder
	n.Claims |= w.ses.Claims()
	if d := NewJanitor().Decide(w.now, w.seen(), n); d.Act {
		t.Fatalf("the janitor acts on the paused screen: %+v", d)
	}
	if d := NewJanitor().Decide(w.now, w.seen(), NoHolder); !d.Act {
		t.Fatalf("control: an unclaimed pause menu draws no janitor action: %+v", d)
	}
}

func TestWindDownPause(t *testing.T) {
	late := func(w *world) { w.after(300*time.Millisecond, func(w *world) { w.pause = true }) }
	cases := []struct {
		name   string
		setup  func(w *world) // before the first tick
		script func(w *world)
		winds  []string
		path   []string
		acts   []string
		rungs  []string
		says   []string
		paused bool // Stopped by the pause rung: menu up and claimed
	}{{
		name:   "rung 1: in town, exit at once",
		setup:  func(w *world) { w.town, w.hot = true, false },
		script: func(w *world) { w.tick() },
		winds:  []string{"exit@0.1"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>Stopped"},
		rungs:  []string{"exit"},
	}, {
		name:   "rung 2: the portal takes him home inside the budget",
		script: func(w *world) { w.run(20 * time.Second); w.hot, w.town = false, true; w.tick() },
		winds:  []string{"exit@20.1"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>Stopped"},
		rungs:  []string{"recall", "exit"},
	}, {
		name:  "budget spent: stop the feet, one ESC, the menu seen — exit with it up",
		setup: func(w *world) { w.onEsc = late },
		script: func(w *world) {
			w.run(29900 * time.Millisecond)
			if w.ses.String() != "WindDown" || w.towns < 290 {
				w.t.Fatalf("29.9s: %s, %d town ticks", w.ses, w.towns)
			}
			w.untilStopped(5 * time.Second)
		},
		winds:  []string{"pause@30.0", "exit@30.4"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Pause", "WindDown/Pause>Stopped"},
		acts:   []string{"esc"},
		rungs:  []string{"recall", "pause", "exit"},
		says:   []string{PausedSay},
		paused: true,
	}, {
		name:   "empty tome: Recall abandoned — pause at once",
		setup:  func(w *world) { w.onEsc = late; w.after(5*time.Second, func(w *world) { w.spent = true }) },
		script: func(w *world) { w.untilStopped(10 * time.Second) },
		winds:  []string{"pause@5.0", "exit@5.4"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Pause", "WindDown/Pause>Stopped"},
		acts:   []string{"esc"},
		rungs:  []string{"recall", "pause", "exit"},
		says:   []string{PausedSay},
		paused: true,
	}, {
		name: "HP under the flee floor: pause (33 itself rides on)",
		setup: func(w *world) {
			w.onEsc, w.hp = late, 33
			w.after(5*time.Second, func(w *world) { w.hp = 32 })
		},
		script: func(w *world) { w.untilStopped(10 * time.Second) },
		winds:  []string{"pause@5.0", "exit@5.4"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Pause", "WindDown/Pause>Stopped"},
		acts:   []string{"esc"},
		rungs:  []string{"recall", "pause", "exit"},
		says:   []string{PausedSay},
		paused: true,
	}, {
		name:   "the menu already up at the rung: no ESC, exit",
		setup:  func(w *world) { w.after(29*time.Second, func(w *world) { w.pause = true }) },
		script: func(w *world) { w.run(29900 * time.Millisecond); w.untilStopped(2 * time.Second) },
		winds:  []string{"exit@30.0"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Pause", "WindDown/Pause>Stopped"},
		rungs:  []string{"recall", "pause", "exit"},
		says:   []string{PausedSay},
		paused: true,
	}, {
		name:  "a panel up: the janitor gets 3s, then the ESC",
		setup: func(w *world) { w.onEsc = late; w.panels = screen.Inventory },
		script: func(w *world) {
			w.after(31500*time.Millisecond, func(w *world) { w.panels = 0 })
			w.run(31400 * time.Millisecond)
			if w.owned != 0 { // the pause waits unowned: the janitor and the loop run
				w.t.Fatalf("the session held %d ticks before the screen cleared", w.owned)
			}
			w.untilStopped(3 * time.Second)
		},
		winds:  []string{"pause@31.5", "exit@31.9"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Pause", "WindDown/Pause>Stopped"},
		acts:   []string{"esc"},
		rungs:  []string{"recall", "pause", "exit"},
		says:   []string{PausedSay},
		paused: true,
	}, {
		name:   "a panel that never clears: Hold after 3s, no ESC",
		setup:  func(w *world) { w.onEsc = late; w.panels = screen.Inventory },
		script: func(w *world) { w.run(40 * time.Second) },
		winds:  []string{"hold@33.0"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Pause", "WindDown/Pause>WindDown/Hold"},
		rungs:  []string{"recall", "pause", "hold"},
		says:   []string{HoldPauseSay},
	}, {
		name:   "pause never confirmed: ESC, retry once, Hold — never a third",
		script: func(w *world) { w.run(60 * time.Second) },
		winds:  []string{"pause@30.0", "hold@34.1"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Pause", "WindDown/Pause>WindDown/Hold"},
		acts:   []string{"esc", "esc"},
		rungs:  []string{"recall", "pause", "hold"},
		says:   []string{HoldPauseSay},
	}, {
		name:   "the ESC refused (no foreground): Hold",
		setup:  func(w *world) { w.onEsc, w.refuse = late, true },
		script: func(w *world) { w.run(35 * time.Second) },
		winds:  []string{"pause@30.0", "hold@30.2"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Pause", "WindDown/Pause>WindDown/Hold"},
		acts:   []string{"esc"},
		rungs:  []string{"recall", "pause", "hold"},
		says:   []string{HoldPauseSay},
	}, {
		name: "-pausefailsafe=false: the budget goes straight to Hold; tome and HP ride on",
		setup: func(w *world) {
			w.ses.PauseFailsafe, w.onEsc = false, late
			w.after(5*time.Second, func(w *world) { w.spent, w.hp = true, 20 })
		},
		script: func(w *world) { w.run(40 * time.Second) },
		winds:  []string{"hold@30.0"},
		path:   []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Hold"},
		rungs:  []string{"recall", "hold"},
		says:   []string{HoldCapSay},
	}, {
		name: "F10 during Pause: the owner drives — no second ESC, the claim released",
		script: func(w *world) {
			w.run(31 * time.Second) // the ESC at 30.1; the menu never rose
			w.engaged = false
			w.run(10 * time.Second)
			if c := w.ses.Claims(); c != 0 {
				w.t.Fatalf("claims %s while the owner drives", c)
			}
		},
		winds: []string{"pause@30.0"},
		path:  []string{"Attaching>InGame", "InGame>WindDown", "WindDown>WindDown/Pause", "WindDown/Pause>WindDown/Hold"},
		acts:  []string{"esc"},
		rungs: []string{"recall", "pause", "hold"},
		says:  []string{HoldOwnerSay},
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := windWorld(t, false, true)
			if c.setup != nil {
				c.setup(w)
			}
			c.script(w)
			lines := strings.Join(w.lines, "\n")
			if !reflect.DeepEqual(w.winds, c.winds) && !(len(w.winds) == 0 && len(c.winds) == 0) {
				t.Fatalf("winds %v, want %v\nlines:\n%s", w.winds, c.winds, lines)
			}
			w.wantPath(c.path...)
			if !reflect.DeepEqual(w.acts, c.acts) && !(len(w.acts) == 0 && len(c.acts) == 0) {
				t.Fatalf("acts %v, want %v\nlines:\n%s", w.acts, c.acts, lines)
			}
			if got := w.rungNames(); !reflect.DeepEqual(got, c.rungs) {
				t.Fatalf("rungs %v, want %v\n%s", got, c.rungs, strings.Join(w.rungs, "\n"))
			}
			if !reflect.DeepEqual(w.says, c.says) && !(len(w.says) == 0 && len(c.says) == 0) {
				t.Fatalf("says %q, want %q", w.says, c.says)
			}
			if w.ses.Paused() != c.paused {
				t.Fatalf("paused %v, want %v (%s)", w.ses.Paused(), c.paused, w.ses)
			}
			if !c.paused {
				return
			}
			// The exit: the menu stays up and claimed; nothing more is sent.
			acts := len(w.acts)
			w.run(5 * time.Second)
			if len(w.acts) != acts || !w.stopped() {
				t.Fatalf("after the pause exit: acts %v, %s", w.acts, w.ses)
			}
			if w.ses.Claims() != screen.PauseMenu || !w.pause {
				t.Fatalf("claims %s, pause drawn %v", w.ses.Claims(), w.pause)
			}
			janitorLeaves(t, w)
		})
	}
}

// The pause rung's transition line names its trigger: the owner's grep target.
func TestWindDownPauseTraceLine(t *testing.T) {
	w := windWorld(t, false, true)
	w.hp = 20
	w.tick()
	want := []string{
		`T L=session from=InGame to=WindDown why="time budget spent"`,
		`T L=session from=WindDown to=WindDown/Pause why="hp 20% under the flee floor 33%: pause the game"`,
	}
	for i, l := range w.lines[len(w.lines)-2:] {
		if !strings.HasPrefix(l, want[i]) {
			t.Fatalf("line %q, want prefix %q", l, want[i])
		}
	}
	if w.rungs[len(w.rungs)-1] != `WINDDOWN rung=pause why="hp 20% under the flee floor 33%"` {
		t.Fatalf("rungs %q", w.rungs)
	}
}
