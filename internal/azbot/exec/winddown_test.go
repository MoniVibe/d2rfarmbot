package exec

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// THE SAFE END (relay R4: -seconds 240 expired mid-fight in the Dry Hills and
// the process exited, leaving the character undriven). Pure table tests of the
// Session's WindDown: the run ends only in town or in a field quiet for 3s;
// otherwise the town road bids, and after 120s the motor is disengaged and
// the process holds — never exiting while a monster is near.

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
			w := windWorld(t, c.town, c.hot)
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
