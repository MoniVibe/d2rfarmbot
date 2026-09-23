package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// Equal-score uniques used to tie on snapshot iteration order, flipping the target
// every tick (the 2026-09-23 Rocky Waste orbit). Nearest wins, and the chosen
// target sticks even when an equally good drop appears first in a later snapshot.
func TestLootPickIsNearestAndSticky(t *testing.T) {
	l := NewLoot()
	me := data.Position{X: 100, Y: 100}
	far := percept.ItemRef{ID: 1, Pos: data.Position{X: 110, Y: 100}, Quality: 7}
	near := percept.ItemRef{ID: 2, Pos: data.Position{X: 104, Y: 100}, Quality: 7}
	s := &percept.Snapshot{Valid: true, Items: []percept.ItemRef{far, near}}
	s.Me.Pos, s.Me.InvFree, s.Me.HPPct = me, 10, 100

	it, _, ok := l.pick(s)
	if !ok || it.ID != near.ID {
		t.Fatalf("got %d ok=%v, want nearest unique %d", it.ID, ok, near.ID)
	}

	// Committed to the far one; a nearer equal drop listed first must not steal it.
	l.target = far.ID
	s.Items = []percept.ItemRef{near, far}
	if it, _, _ := l.pick(s); it.ID != far.ID {
		t.Fatalf("target flipped to %d, want sticky %d", it.ID, far.ID)
	}
}

// Bottles on the floor are wanted while the belt has room, never over a unique,
// and not at all once the belt is full.
func TestLootWantsPotionsForTheBelt(t *testing.T) {
	LootPotions = true
	defer func() { LootPotions = false }()
	l := NewLoot()
	s := &percept.Snapshot{Valid: true}
	s.Me.InvFree, s.Me.BeltSlots, s.Me.BeltUsed = 10, 8, 3
	red := percept.ItemRef{ID: 1, Quality: 2, Potion: "health"}
	uniq := percept.ItemRef{ID: 2, Quality: 7, Potion: "unknown"}
	if l.wanted(s, red) <= 0 {
		t.Fatal("belt 3/8: a red bottle must be wanted")
	}
	if l.wanted(s, uniq) <= l.wanted(s, red) {
		t.Fatal("a unique must outrank a bottle")
	}
	s.Me.BeltUsed = 8
	if l.wanted(s, red) != 0 {
		t.Fatal("full belt: bottles are not wanted")
	}
}
