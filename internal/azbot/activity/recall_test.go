package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// Recall is the session's wind-down town road: silent unless the executive
// asks (exec.WindTown), silent in town or dead, and at class Recover — over
// Fight, under Survive — when it bids.
func TestRecallBidsOnlyWhenTheSessionAsks(t *testing.T) {
	r := NewRecall()
	s := &percept.Snapshot{Valid: true}
	s.Me.HPPct = 80
	s.Me.Pos = data.Position{X: 100, Y: 100}
	// far enough that the recall stands (a monster within 8 = fight first: owner, R34)
	s.Enemies = []percept.EnemyRef{{Pos: data.Position{X: 120, Y: 100}}}
	if d := r.Demand(s); d != nil {
		t.Fatalf("bid unasked: %+v", d)
	}
	r.Want(true)
	d := r.Demand(s)
	if d == nil || d.Class != arbiter.ClassRecover {
		t.Fatalf("demand %+v, want class recover", d)
	}
	if !(arbiter.ClassSurvive < d.Class && d.Class < arbiter.ClassFight) {
		t.Fatalf("recover must sit over fight and under survive")
	}
	s.Me.InTown = true
	if d := r.Demand(s); d != nil {
		t.Fatalf("bid in town: %+v", d)
	}
	s.Me.InTown, s.Me.HPPct = false, 0
	if d := r.Demand(s); d != nil {
		t.Fatalf("bid dead: %+v", d)
	}
	if reg := Registry(nil, nil); reg.Recall == nil || reg.Get("recall") == nil {
		t.Fatal("recall not registered")
	}
}

// NO TOWN PORTAL BINDING (calibration found none, or -tpkey=none): Recall is
// spent before it ever bids — the wind-down goes straight to the pause rung —
// unless a live door already stands on the field; Withdraw never bids.
func TestNoTownPortalBindingSkipsThePortalRoad(t *testing.T) {
	defer SetTownPortal(true)
	SetTownPortal(false)
	r := NewRecall()
	r.Want(true)
	s := &percept.Snapshot{Valid: true}
	s.Me.HPPct = 30
	if d := r.Demand(s); d != nil || !r.Spent() {
		t.Fatalf("unbound, no door: demand %+v spent %v, want nil and spent", d, r.Spent())
	}
	w := NewWithdraw()
	if d := w.Demand(s); d != nil {
		t.Fatalf("withdraw bid with no binding and no door: %+v", d)
	}
	s.Portals = []percept.PortalRef{{ID: 4242, Pos: data.Position{X: 3, Y: 3}}}
	if d := r.Demand(s); d == nil || r.Spent() {
		t.Fatalf("a live door is still a road: demand %+v spent %v", d, r.Spent())
	}
	if d := w.Demand(s); d == nil {
		t.Fatal("withdraw must ride a live door even unbound")
	}
	SetTownPortal(true)
	s.Portals = nil
	if d := r.Demand(s); d == nil || r.Spent() {
		t.Fatalf("bound: demand %+v spent %v", d, r.Spent())
	}
}

// No tome bound: the town road is abandoned at once and Spent tells the
// session (its ladder pauses the game); Recall stops bidding while it cools.
func TestRecallSpentWithoutATome(t *testing.T) {
	r := NewRecall()
	r.Want(true)
	s := &percept.Snapshot{Valid: true}
	s.Me.HPPct = 80
	if r.Spent() {
		t.Fatal("spent before any ride")
	}
	if v := r.Step(&Ctx{Snap: s}); v != Abandoned {
		t.Fatalf("verdict %v, want Abandoned", v)
	}
	if !r.Spent() || r.Demand(s) != nil {
		t.Fatalf("spent %v demand %+v", r.Spent(), r.Demand(s))
	}
}
