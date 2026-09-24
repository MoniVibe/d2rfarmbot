package route

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
)

// Run w's geometry: Dry Hills live frame around her, the phantom 56 door.
var (
	dryHills = Rect{X: 5200, Y: 4200, W: 800, H: 800}
	me       = data.Position{X: 5630, Y: 4558}
	phantom  = data.Position{X: 15080, Y: 6580}
)

func TestPlausibleRejectsZeroAndPhantom(t *testing.T) {
	if Plausible(data.Position{}, me, dryHills) {
		t.Fatal("the zero value is unknown, never a door")
	}
	if Plausible(phantom, me, dryHills) {
		t.Fatal("a door 9450 tiles outside her level's frame is not a door of that level")
	}
	if Plausible(phantom, me, Rect{}) {
		t.Fatal("with no frame, a door farther than a level's span is still implausible")
	}
}

func TestPlausibleAcceptsInsideAndSeam(t *testing.T) {
	if !Plausible(data.Position{X: 5553, Y: 4698}, me, dryHills) {
		t.Fatal("a door inside the live frame is plausible")
	}
	// A walkable border's nearest far-side tile sits just past the frame edge.
	if !Plausible(data.Position{X: 6000 + 5, Y: 4600}, me, dryHills) {
		t.Fatal("a seam point just outside the frame is plausible")
	}
	if Plausible(data.Position{X: 6000 + DoorMargin + 1, Y: 4600}, me, dryHills) {
		t.Fatal("beyond the seam margin is not")
	}
}

func TestPlausibleIgnoresStaleFrame(t *testing.T) {
	// Last area's grid does not hold her: judge by span, not by that frame.
	stale := Rect{X: 100, Y: 100, W: 50, H: 50}
	if !Plausible(data.Position{X: 5700, Y: 4600}, me, stale) {
		t.Fatal("a frame that does not contain her is stale and must not veto a near door")
	}
}

func TestPickDoorFallsThroughImplausibleToNextSource(t *testing.T) {
	rooms := data.Position{X: 5999, Y: 4500}
	cands := []Candidate{{"fact", data.Position{}}, {"map", phantom}, {"rooms", rooms}}
	p := PickDoor(cands, me, dryHills, nil)
	if !p.Known || p.Door.Src != "rooms" || p.Door.Pos != rooms {
		t.Fatalf("pick = %+v, want the rooms door", p)
	}
	if len(p.Rejected) != 2 {
		t.Fatalf("rejected = %+v, want the zero fact and the phantom map hint", p.Rejected)
	}
}

func TestPickDoorUnknownWhenNothingPlausible(t *testing.T) {
	p := PickDoor([]Candidate{{"map", phantom}}, me, dryHills, nil)
	if p.Known || p.Door.Pos != (data.Position{}) {
		t.Fatalf("pick = %+v, want unknown", p)
	}
}

func TestPickDoorSkipsDisbelieved(t *testing.T) {
	now := time.Now()
	var d Disbelief
	fact := data.Position{X: 5700, Y: 4300}
	d.Add(fact, now.Add(time.Minute))
	rooms := data.Position{X: 5999, Y: 4500}
	p := PickDoor([]Candidate{{"fact", fact}, {"rooms", rooms}}, me, dryHills,
		func(q data.Position) bool { return d.Has(q, now) })
	if !p.Known || p.Door.Src != "rooms" {
		t.Fatalf("pick = %+v, want the next source past the disbelieved fact", p)
	}
	// The conviction expires: the fact gets a second look later.
	later := now.Add(2 * time.Minute)
	p = PickDoor([]Candidate{{"fact", fact}}, me, dryHills, func(q data.Position) bool { return d.Has(q, later) })
	if !p.Known || p.Door.Src != "fact" {
		t.Fatalf("pick after expiry = %+v, want the fact again", p)
	}
}

func TestDisbeliefBlindAndClear(t *testing.T) {
	now := time.Now()
	var d Disbelief
	d.Blind(now.Add(30 * time.Second))
	if !d.Has(data.Position{X: 1, Y: 1}, now) {
		t.Fatal("a blind window doubts every hint")
	}
	if d.Has(data.Position{X: 1, Y: 1}, now.Add(31*time.Second)) {
		t.Fatal("the blind window ends")
	}
	d.Add(data.Position{X: 10, Y: 10}, now.Add(time.Hour))
	d.Clear()
	if d.Has(data.Position{X: 10, Y: 10}, now) {
		t.Fatal("Clear forgets every conviction")
	}
}

func TestProjectKeepsRelativePlace(t *testing.T) {
	mapFrame := Rect{X: 14000, Y: 6000, W: 1600, H: 800}
	p, ok := Project(phantom, mapFrame, dryHills)
	if !ok {
		t.Fatal("a hint inside the map frame projects")
	}
	// 1080/1600 across, 580/800 down -> 540, 580 into an 800x800 frame.
	if p != (data.Position{X: 5200 + 540, Y: 4200 + 580}) {
		t.Fatalf("projected = %+v", p)
	}
	if _, ok := Project(phantom, dryHills, mapFrame); ok {
		t.Fatal("a hint outside the source frame says nothing")
	}
}

func TestLadderClimbsWrapsAndResets(t *testing.T) {
	t0 := time.Now()
	var l Ladder
	want := []Rung{Replan, Alternate, Portal, FailLeg, Replan, Alternate}
	for i, w := range want {
		l = l.Climb("intent.56", t0.Add(time.Duration(i)*10*time.Second))
		if l.Rung != w {
			t.Fatalf("climb %d = %s, want %s", i, l.Rung, w)
		}
	}
	if l.Cycle != 1 {
		t.Fatalf("cycle = %d, want 1 after passing FAIL-LEG once", l.Cycle)
	}
	// A quiet ladder restarts at REPLAN, keeping its cycle.
	l = l.Climb("intent.56", l.At.Add(LadderQuiet+time.Second))
	if l.Rung != Replan || l.Cycle != 1 {
		t.Fatalf("quiet climb = %+v, want REPLAN cycle 1", l)
	}
	// A new key starts over entirely.
	l = l.Climb("intent.57", l.At.Add(time.Second))
	if l.Rung != Replan || l.Cycle != 0 || l.Key != "intent.57" {
		t.Fatalf("new key = %+v, want fresh REPLAN", l)
	}
}

func TestSearchWindowGrowsAndCaps(t *testing.T) {
	if SearchWindow(0) != 30*time.Second || SearchWindow(1) != time.Minute {
		t.Fatalf("windows = %s %s", SearchWindow(0), SearchWindow(1))
	}
	if SearchWindow(9) != 2*time.Minute {
		t.Fatalf("cap = %s", SearchWindow(9))
	}
}

func TestTurnHeadingNewSideEachTime(t *testing.T) {
	seen := map[int]bool{}
	h := 1
	for i := 0; i < 8; i++ {
		h = TurnHeading(h, 8)
		if h == 0 {
			t.Fatal("heading must stay non-zero (search reads 0 as unset)")
		}
		seen[h%8] = true
	}
	if len(seen) != 8 {
		t.Fatalf("eight turns of 135° should visit all 8 bearings, saw %d", len(seen))
	}
}
