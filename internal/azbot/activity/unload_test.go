package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func TestUnloadGoesHomeWhenFullAndCalm(t *testing.T) {
	u := NewUnload()
	s := townSnap()
	s.Me.InTown = false
	s.Me.InvFree, s.Me.JunkCount = 3, 5
	if d := u.Demand(s); d == nil {
		t.Fatal("full bag with junk in a calm field: Unload must bid")
	}
	s.Enemies = []percept.EnemyRef{{Pos: data.Position{X: s.Me.Pos.X + 5, Y: s.Me.Pos.Y}}}
	if u.Demand(s) != nil {
		t.Fatal("an enemy within 12: no portal cast (Fight first)")
	}
	s.Enemies = nil
	s.Me.JunkCount, s.Me.StashCount = 0, 0
	if u.Demand(s) != nil {
		t.Fatal("full of keepers only: nothing to clear in town")
	}
	s.Me.InvFree, s.Me.JunkCount = 30, 5
	if u.Demand(s) != nil {
		t.Fatal("room left: keep working")
	}
}

func TestRecallStandsForHimselfFirst(t *testing.T) {
	r := NewRecall()
	r.want = true
	s := townSnap()
	s.Me.InTown = false
	s.Enemies = []percept.EnemyRef{{Pos: data.Position{X: s.Me.Pos.X + 3, Y: s.Me.Pos.Y}}}
	if r.Demand(s) != nil {
		t.Fatal("a monster at 3 tiles: fight first, no portal")
	}
	s.Enemies = nil
	if r.Demand(s) == nil {
		t.Fatal("a calm field: recall")
	}
}

// R44: a keeper Loot wants in reach holds the trip home — for a bounded streak.
func TestUnloadWaitsForTreasureBounded(t *testing.T) {
	t0 := time.Now()
	treasure.first, treasure.last = time.Time{}, time.Time{}
	defer func() { treasure.first, treasure.last = time.Time{}, time.Time{} }()
	if treasurePending(t0) {
		t.Fatal("nothing seen: no hold")
	}
	noteTreasure(t0, true)
	if !treasurePending(t0.Add(time.Second)) {
		t.Fatal("just seen: hold")
	}
	if treasurePending(t0.Add(5 * time.Second)) {
		t.Fatal("unseen for 5s: no hold")
	}
	for d := time.Duration(0); d <= 70*time.Second; d += 2 * time.Second {
		noteTreasure(t0.Add(d), true)
	}
	if treasurePending(t0.Add(70 * time.Second)) {
		t.Fatal("a streak past treasureWaitMax no longer holds (unreachable drop)")
	}
	noteTreasure(t0.Add(90*time.Second), true) // a new streak after the gap
	if !treasurePending(t0.Add(91 * time.Second)) {
		t.Fatal("a fresh sighting after the gap holds again")
	}
}
