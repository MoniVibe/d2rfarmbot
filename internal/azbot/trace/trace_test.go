package trace

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

func eq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestGrant(t *testing.T) {
	eq(t, Grant(arbiter.Change{From: "fight", To: "flee", Kind: arbiter.Preempt, Reason: "survive > fight"}, 42),
		`T L=grant from=fight to=flee why="preempt: survive > fight" tick=42`)
	eq(t, Grant(arbiter.Change{To: "explore", Kind: arbiter.Fresh, Reason: "no holder"}, 1),
		`T L=grant from=- to=explore why="fresh: no holder" tick=1`)
	eq(t, Ended("fight", phase.Abandoned, phase.Judged, "watchdog Stuck", 9),
		`T L=grant from=fight to=- why="end: abandoned/judged: watchdog Stuck" tick=9`)
	eq(t, Ended("loot", phase.Done, phase.Completed, "", 9),
		`T L=grant from=loot to=- why="end: done/completed" tick=9`)
}

func TestUI(t *testing.T) {
	tr := screen.Transition{From: screen.State{Mode: screen.World},
		To: screen.State{Mode: screen.World, Panels: screen.Inventory}, Evidence: "right-half panel"}
	eq(t, UI(tr, 7, "fence"), `T L=ui from=world to=world+inventory why="right-half panel" tick=7 hold=fence`)
	eq(t, UI(tr, 7, ""), `T L=ui from=world to=world+inventory why="right-half panel" tick=7 hold=-`)
}

func TestGateLine(t *testing.T) {
	eq(t, Gate("fight", "foreign: right-panel", "click 1788,18", 5),
		`T L=gate hold=fight why="foreign: right-panel" act="click 1788,18" tick=5`)
	eq(t, Gate("", "clear", "", 6), `T L=gate hold=- why="clear" act="-" tick=6`)
}

func TestPhase(t *testing.T) {
	var got string
	p := &phase.Phaser[testPhase]{Act: "fence", Log: func(l string) { got = Phase(l, 12) }}
	p.To(1, "npc loaded")
	eq(t, got, `T L=phase act=fence from=Seek to=Approach why="npc loaded" inPhase=0.0s tick=12`)
}

type testPhase uint8

func (p testPhase) String() string { return [...]string{"Seek", "Approach"}[p] }

func TestNav(t *testing.T) {
	l, ok := Nav("advance", "nav: following -> stuck (no gain 1.5s)", 5)
	if !ok {
		t.Fatal("transition evidence must parse")
	}
	eq(t, l, `T L=nav act=advance from=following to=stuck why="no gain 1.5s" tick=5`)
	l, ok = Nav("loot", "nav: idle -> following", 6)
	if !ok {
		t.Fatal("why is optional")
	}
	eq(t, l, `T L=nav act=loot from=idle to=following why="" tick=6`)
	if _, ok := Nav("advance", "me=(1,2) area=3 tgt=(4,5)", 5); ok {
		t.Fatal("a nav debug note is not a transition")
	}
	if _, ok := Nav("advance", "map-oracle exit (1,2) ate a 60s leg", 5); ok {
		t.Fatal("a march note is not a transition")
	}
}

func TestStateLine(t *testing.T) {
	at := time.Date(2026, 9, 24, 15, 4, 5, 0, time.UTC)
	s := State{At: at, Tick: 48213, Session: "InGame", Mode: ModeName(screen.World), UI: UIName(0),
		Hold: "fight", Held: 7250 * time.Millisecond, Valid: true, HP: 64, MP: 30, X: 5012, Y: 4431, Enemies: 5}
	eq(t, s.String(), "S 15:04:05 #48213 ses=InGame mode=World ui=- cur=- hold=fight held=7.2s gate=- phase=- hp=64 mp=30 pos=5012,4431 en=5")
	blank := State{At: at, Tick: 1, Session: "Disengaged"}
	eq(t, blank.String(), "S 15:04:05 #1 ses=Disengaged mode=- ui=- cur=- hold=- held=- gate=- phase=- hp=- mp=- pos=- en=-")
	s.UI, s.Cursor, s.Phase = UIName(screen.Inventory|screen.Shop), "item", "Talk"
	eq(t, s.String(), "S 15:04:05 #48213 ses=InGame mode=World ui=shop+inventory cur=item hold=fight held=7.2s gate=- phase=Talk hp=64 mp=30 pos=5012,4431 en=5")
}

func TestModeName(t *testing.T) {
	for m, w := range map[screen.Mode]string{screen.Unknown: "Unknown", screen.World: "World", screen.Loading: "Loading", screen.Dead: "Dead"} {
		eq(t, ModeName(m), w)
	}
}

// Old flight files are bare snapshot lines: they must never read as frames,
// and a frame must survive the round trip.
func TestFrameRoundTrip(t *testing.T) {
	snapLine := []byte(`{"Seq":12,"At":"2026-09-24T15:04:05Z","Valid":true,"Me":{"Pos":{"X":1,"Y":2}}}`)
	if _, ok := IsFrame(snapLine); ok {
		t.Fatal("a snapshot line is not a frame")
	}
	if _, ok := IsFrame([]byte("garbage")); ok {
		t.Fatal("garbage is not a frame")
	}
	f := Frame{K: FrameKind, Tick: 9, Seq: 12, Holder: "fight", Change: "- -> fight (fresh: no holder)", Screen: "world"}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := IsFrame(b)
	if !ok || got.Holder != "fight" || got.Seq != 12 || got.Tick != 9 {
		t.Fatalf("round trip: %+v %v", got, ok)
	}
}
