package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func TestUnloadGoesHomeWhenFullAndCalm(t *testing.T) {
	u := NewUnload()
	s := townSnap()
	s.Me.InTown = false
	s.Me.InvFree, s.Me.JunkCount = 3, 5
	s.Me.TPScrolls = 5
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
	s.Me.TPScrolls = 20
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

// R50: an empty TP tome is no road home.
func TestUnloadNeedsTPCharges(t *testing.T) {
	u := NewUnload()
	s := townSnap()
	s.Me.InTown = false
	s.Me.InvFree, s.Me.JunkCount = 3, 5
	s.Me.TPScrolls = 0
	if u.Demand(s) != nil {
		t.Fatal("empty tome, no portal standing: Unload must not bid")
	}
	s.Me.TPScrolls = 5
	u.w.coolAt = time.Now().Add(time.Minute)
	if u.Demand(s) != nil {
		t.Fatal("inside Withdraw's cool-off after three dead casts: no bid")
	}
	u.w.coolAt = time.Time{}
	if u.Demand(s) == nil {
		t.Fatal("charges and calm: go home")
	}
}

// Evasive march: only a wounded or boxed-in barbarian stops to fight.
func TestPressed(t *testing.T) {
	s := townSnap()
	s.Me.InTown, s.Me.HPPct = false, 100
	at := func(dx int) percept.EnemyRef {
		return percept.EnemyRef{Pos: data.Position{X: s.Me.Pos.X + dx, Y: s.Me.Pos.Y}}
	}
	s.Enemies = []percept.EnemyRef{at(2), at(3), at(7)}
	if pressed(s) {
		t.Fatal("three nearby at full health: leap past")
	}
	s.Enemies = append(s.Enemies, at(8))
	if !pressed(s) {
		t.Fatal("four within 8: a pack threatens, leap in")
	}
	s.Enemies, s.Me.HPPct = nil, 50
	if !pressed(s) {
		t.Fatal("wounded: fight")
	}
}

// k3 (2026-09-26): an EMPTY tome abandoned the wind-down recall and she stood
// disengaged among 152 monsters. A lit pad in the area is the road home.
func TestRecallRidesThePadWithAnEmptyTome(t *testing.T) {
	r := NewRecall()
	r.want = true
	s := townSnap()
	s.Me.InTown = false
	s.Me.Area = area.BlackMarsh
	s.Me.TPScrolls = 0
	if r.Demand(s) != nil || !r.Spent() {
		t.Fatal("empty tome, no pad known: nothing to ride, Spent")
	}
	notePadLit(area.BlackMarsh, data.Position{X: 10, Y: 10})
	defer func() { padsLit.Lock(); delete(padsLit.m, area.BlackMarsh); padsLit.Unlock() }()
	if r.Demand(s) == nil || !r.byPad || r.Spent() {
		t.Fatal("empty tome, lit pad here: ride the waypoint home, not Spent")
	}
}

func TestHealOutSendsHerHome(t *testing.T) {
	s := townSnap()
	s.Me.InTown, s.Me.Gold, s.Me.HealPots = false, 5000, 0
	if !healOut(s) {
		t.Fatal("no healing anywhere and gold to buy: go home")
	}
	s.Bag = append(s.Bag, percept.BagItem{ID: 602}) // a red in the bag: the belt refills from it
	if healOut(s) {
		t.Fatal("a healing potion in the bag is not 'out'")
	}
	s.Bag, s.Me.HealPots = nil, 2
	if healOut(s) {
		t.Fatal("belt potions left: not out")
	}
}
