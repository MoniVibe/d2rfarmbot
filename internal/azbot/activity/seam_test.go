package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// townSnap is a healthy town snapshot that clears ServicesPending and the
// naked-corpse guard, so only the seam gate is under test.
func townSnap() *percept.Snapshot {
	s := &percept.Snapshot{Valid: true}
	s.Me.InTown = true
	s.Me.HPPct = 100
	s.Me.Area = area.LutGholein
	s.Me.WeaponKind = "melee"
	return s
}

func armSeam(committed, crossed bool) {
	if committed {
		advanceCommitBeyondTownUntil = time.Now().Add(2 * time.Second)
	} else {
		advanceCommitBeyondTownUntil = time.Time{}
	}
	if crossed {
		lastSeamCrossAt = time.Now()
	} else {
		lastSeamCrossAt = time.Now().Add(-time.Hour)
	}
}

func TestTravelDemandHeldBySeamHysteresis(t *testing.T) {
	SetDeliberate(true)
	defer SetDeliberate(false)
	tr := &Travel{Road: []data.Position{{X: 1, Y: 1}, {X: 2, Y: 2}, {X: 3, Y: 3}}}

	armSeam(true, true) // committed beyond town + fresh crossing → held
	if d := tr.Demand(townSnap()); d != nil {
		t.Fatalf("travel must stand down during the seam hysteresis, got %+v", d)
	}

	armSeam(true, false) // committed but the crossing is old → the road resumes
	if d := tr.Demand(townSnap()); d == nil {
		t.Fatal("travel must resume once the crossing is outside the window")
	}

	armSeam(false, true) // fresh crossing but no beyond-town commitment → free
	if d := tr.Demand(townSnap()); d == nil {
		t.Fatal("without a beyond-town commitment the road is not held")
	}
}

func TestTravelDemandUnaffectedWhenFlagOff(t *testing.T) {
	SetDeliberate(false)
	tr := &Travel{Road: []data.Position{{X: 1, Y: 1}, {X: 2, Y: 2}, {X: 3, Y: 3}}}
	armSeam(true, true)
	if d := tr.Demand(townSnap()); d == nil {
		t.Fatal("flag OFF: the seam gate must never fire — legacy behavior")
	}
}

func TestReturnDemandHeldBySeamHysteresis(t *testing.T) {
	SetDeliberate(true)
	defer SetDeliberate(false)
	r := &Return{}
	s := townSnap()
	s.Portals = []percept.PortalRef{{ID: 4242, Pos: data.Position{X: 5, Y: 5}}} // a live door

	armSeam(true, true)
	if d := r.Demand(s); d != nil {
		t.Fatalf("return must stand down during the seam hysteresis, got %+v", d)
	}

	armSeam(true, false)
	if d := r.Demand(s); d == nil {
		t.Fatal("return must resume once the crossing is outside the window")
	}
}

func TestReturnDemandUnaffectedWhenFlagOff(t *testing.T) {
	SetDeliberate(false)
	r := &Return{}
	s := townSnap()
	s.Portals = []percept.PortalRef{{ID: 4243, Pos: data.Position{X: 5, Y: 5}}}
	armSeam(true, true)
	if d := r.Demand(s); d == nil {
		t.Fatal("flag OFF: the seam gate must never fire for Return either")
	}
}
