package route

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/entrance"
)

func TestWarpLeads(t *testing.T) {
	if WarpLeads(entrance.A2LairUp) != LeadsUp || WarpLeads(entrance.A2LairDown) != LeadsDown {
		t.Fatal("lair stairs direction")
	}
	if WarpLeads(entrance.A2SewerDocktoTown) != LeadsTown {
		t.Fatal("to town")
	}
	if WarpLeads(entrance.A2DeserttoLair) != LeadsUnknown {
		t.Fatal("desert->lair has no up/down")
	}
}

// The 2ad968d trace: Lair L2 (63) -> L3 (64), map hint near the corner where the
// stairs back up to L1 stand. The choice must not pick them.
func TestChooseEntranceExcludesArrivalDoor(t *testing.T) {
	up := EntranceCand{ID: 20, Name: entrance.A2LairUp, Pos: data.Position{X: 100, Y: 100}}
	down := EntranceCand{ID: 21, Name: entrance.A2LairDown, Pos: data.Position{X: 180, Y: 160}}
	hint := data.Position{X: 104, Y: 98}
	// arrival fact only (no lvlwarp help: pretend names unknown)
	upU, downU := up, down
	upU.Name, downU.Name = entrance.Name(9999), entrance.Name(9999)
	g := DoorGoal{Hop: 64, Deeper: 1, Facts: []DoorFact{{Pos: data.Position{X: 103, Y: 104}, LeadTo: 62, Why: "arrival"}}}
	c, note, ok := ChooseEntrance([]EntranceCand{upU, downU}, hint, 96, g)
	if !ok || c.ID != 21 || !strings.Contains(note, "pick=(180,160)") {
		t.Fatalf("picked %+v %s", c, note)
	}
	// lvlwarp alone excludes the up stairs for a deeper hop
	c, _, ok = ChooseEntrance([]EntranceCand{up, down}, hint, 96, DoorGoal{Hop: 64, Deeper: 1})
	if !ok || c.ID != 21 {
		t.Fatalf("lvlwarp: picked %+v", c)
	}
	// but going back UP it is the right one
	c, _, ok = ChooseEntrance([]EntranceCand{up, down}, hint, 96, DoorGoal{Hop: 62, Deeper: -1})
	if !ok || c.ID != 20 {
		t.Fatalf("shallower hop: picked %+v", c)
	}
}

func TestChooseEntranceExcludesWrongDestination(t *testing.T) {
	a := EntranceCand{ID: 1, Name: entrance.Name(9999), Pos: data.Position{X: 10, Y: 10}}
	b := EntranceCand{ID: 2, Name: entrance.Name(9999), Pos: data.Position{X: 60, Y: 10}}
	g := DoorGoal{Hop: 64, Facts: []DoorFact{{Pos: a.Pos, LeadTo: 43, Why: "wrong-landing"}}}
	c, _, ok := ChooseEntrance([]EntranceCand{a, b}, data.Position{X: 10, Y: 10}, 96, g)
	if !ok || c.ID != 2 {
		t.Fatalf("picked %+v", c)
	}
	// only the wrong one: nothing
	if _, note, ok := ChooseEntrance([]EntranceCand{a}, a.Pos, 96, g); ok || !strings.Contains(note, "pick=none") {
		t.Fatal("wrong-landing door retaken")
	}
	// a fact FOR the hop is taken
	g.Facts = append(g.Facts, DoorFact{Pos: b.Pos, LeadTo: 64, Why: "fact"})
	if c, note, _ := ChooseEntrance([]EntranceCand{a, b}, a.Pos, 96, g); c.ID != 2 || !strings.Contains(note, "leadsTo=64") {
		t.Fatalf("%+v %s", c, note)
	}
	if !g.NearForeignFact(data.Position{X: 12, Y: 9}) || g.NearForeignFact(b.Pos) {
		t.Fatal("NearForeignFact")
	}
}

func TestSearchSkipsBackDoors(t *testing.T) {
	g := DoorGoal{Hop: 64, Deeper: 1, Facts: []DoorFact{{Pos: data.Position{X: 5, Y: 5}, LeadTo: 43, Why: "arrival"}}}
	if skip, _ := SearchSkip(entrance.Name(9999), data.Position{X: 6, Y: 6}, g); !skip {
		t.Fatal("arrival door (to far oasis) must be skipped")
	}
	if skip, _ := SearchSkip(entrance.A2LairUp, data.Position{X: 90, Y: 90}, g); !skip {
		t.Fatal("up stairs must be skipped on a deeper goal")
	}
	if skip, _ := SearchSkip(entrance.Name(9999), data.Position{X: 90, Y: 90}, g); skip {
		t.Fatal("unrecorded, no info: approachable")
	}
	if skip, _ := SearchSkip(entrance.A2LairDown, data.Position{X: 90, Y: 90}, g); skip {
		t.Fatal("down stairs are the way on")
	}
}

func TestContactOnArrived(t *testing.T) {
	if !ContactReady(5, true) || !ContactReady(6, false) || ContactReady(7, false) || ContactReady(14, true) {
		t.Fatal("contact rule")
	}
}

func TestDoorVerdict3s(t *testing.T) {
	t0 := time.Now()
	if done, _ := DoorVerdict(t0, t0.Add(1400*time.Millisecond), false); done {
		t.Fatal("1.4s is not deaf any more")
	}
	if done, ok := DoorVerdict(t0, t0.Add(2*time.Second), true); !done || !ok {
		t.Fatal("change at 2s is success")
	}
	if done, ok := DoorVerdict(t0, t0.Add(3*time.Second), false); !done || ok {
		t.Fatal("3s lapse is deaf")
	}
}

func TestClickBudgetResetsPerLeg(t *testing.T) {
	var b ClickBudget
	for i := 0; i < ClickCap; i++ {
		if !b.Allow(64, 20) {
			t.Fatal("cap too early")
		}
		b.N++
	}
	if b.Allow(64, 20) {
		t.Fatal("cap not enforced")
	}
	if !b.Allow(65, 20) {
		t.Fatal("new leg must reset the count")
	}
	b.N = ClickCap
	b.Reset()
	if !b.Allow(65, 20) {
		t.Fatal("Reset")
	}
}

func TestPortalFirst(t *testing.T) {
	if !PortalFirst(true, true, false, false, false) {
		t.Fatal("live own portal in town: portal first")
	}
	if PortalFirst(true, false, false, false, false) || PortalFirst(true, true, true, false, false) ||
		PortalFirst(true, true, false, true, false) || PortalFirst(true, true, false, false, true) || PortalFirst(false, true, false, false, false) {
		t.Fatal("guards")
	}
}
