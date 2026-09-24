package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// Run u (step 11): the approach clock kept the d=3 the pickups had reached, so
// after two probes she stood at d=6 "without progress" and the drop was banned
// though nothing was stuck. The window restarts at every pickup click and
// judges only its own best.
func TestLootProgressWindowRestartsAfterOurClicks(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	var p lootProgress
	p.restart(t0)
	for i, d := range []int{9, 7, 5, 3} {
		if p.stuck(d, at(i*500), 4*time.Second) {
			t.Fatalf("approach %d flagged stuck", d)
		}
	}
	// Two pickups at d=3 take ~3.5s; each restarts the window.
	p.restart(at(2500))
	p.restart(at(5500))
	// She stands at 6, then 10, 9 — our own clicks' doing — then closes in.
	for _, c := range []struct {
		ms, d int
	}{{6000, 6}, {7000, 10}, {8000, 9}, {9000, 5}, {9500, 4}} {
		if p.stuck(c.d, at(c.ms), 4*time.Second) {
			t.Fatalf("d=%d at +%dms banned inside a fresh window", c.d, c.ms)
		}
	}
	// The old clock (best=3 from before the clicks, improved at +1500ms) would
	// have banned at the first d=6 read: 4.5s "without progress".
}

func TestLootProgressWindowStillCatchesAStall(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var p lootProgress
	p.restart(t0)
	p.stuck(8, t0, 4*time.Second)
	if p.stuck(9, t0.Add(3*time.Second), 4*time.Second) {
		t.Fatal("3s is inside the patience")
	}
	if !p.stuck(8, t0.Add(4100*time.Millisecond), 4*time.Second) {
		t.Fatal("4.1s without beating the window's best must be a stall")
	}
	// At hand the pickup's failure count rules, never the clock.
	p.restart(t0)
	p.stuck(3, t0, 4*time.Second)
	if p.stuck(3, t0.Add(10*time.Second), 4*time.Second) {
		t.Fatal("d<=3 is never a walking stall")
	}
}

// The probe's policy oracle: any wanted, unbanned item under the cursor.
func TestLootAcceptForFollowsPolicyAndBans(t *testing.T) {
	l := NewLoot()
	s := &percept.Snapshot{Valid: true}
	s.Me.InvFree = 10
	now := time.Unix(1000, 0)
	uniq := data.Item{UnitID: 5, Quality: item.QualityUnique}
	key := data.Item{UnitID: 6, Quality: item.QualityNormal}
	if !l.acceptFor(s, now)(uniq) {
		t.Fatal("a unique under the cursor is worth the click")
	}
	if l.acceptFor(s, now)(key) {
		t.Fatal("a key is not loot (uniques only)")
	}
	l.ban[5] = now.Add(time.Minute)
	if l.acceptFor(s, now)(uniq) {
		t.Fatal("a banned item is not accepted")
	}
	delete(l.ban, 5)
	s.Me.InvFree = 1
	if l.acceptFor(s, now)(uniq) {
		t.Fatal("no bag space: not wanted")
	}
}

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
	resetLoot(t)
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
