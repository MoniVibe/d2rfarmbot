package activity

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// widePolicy is the pre-ruling wide net: these tests exercise every tier's
// mechanics (swap, haul, urgency). The shipped default is the owner's narrow
// "uniques and special items only" rule (config/loot.yaml).
const widePolicy = `tiers:
  unique: S
  set: S
  rare: A
  crafted: A
  magic_jewelry: A
  magic_gear: B
  plain_gear: C
  rune: S
  gem: S
  jewel: S
  charm: S
  gold: A
  potion: A
  quest: S
  ammo: C
  scroll: C
  misc: C
  unknown_mod_item: A
  quality_contradiction: A
space:
  swap_for_a: true
`

// useWideLoot points the loot brain at the wide policy for this test.
func useWideLoot(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "loot.yaml")
	if err := os.WriteFile(p, []byte(widePolicy), 0o644); err != nil {
		t.Fatal(err)
	}
	old := LootConfigPath
	LootConfigPath = p
	t.Cleanup(func() { LootConfigPath = old })
}

// resetLoot gives each test a fresh brain on the wide policy (no wiring).
func resetLoot(t *testing.T) {
	t.Helper()
	useWideLoot(t)
	theLoot = &lootMind{}
	marchLawfulUntil = time.Time{}
	haulCoolUntil = time.Time{}
	discardWorks.Store(true)
	t.Cleanup(func() {
		theLoot = &lootMind{}
		marchLawfulUntil = time.Time{}
	})
}

func lootSnap(seq uint64, free int, items ...percept.ItemRef) *percept.Snapshot {
	s := &percept.Snapshot{Seq: seq, Valid: true, Items: items}
	s.Me.Pos, s.Me.HPPct, s.Me.InvFree = data.Position{X: 100, Y: 100}, 100, free
	s.Me.Area = 2
	s.Me.TPScrolls, s.Me.IDScrolls = 10, 10
	s.Me.BeltSlots, s.Me.BeltUsed = 8, 8
	s.Me.MinDurPct = 100
	return s
}

// The owner's complaint: runes, charms and the mod's own rows were walked past.
func TestLootWantsRunesCharmsAndModRows(t *testing.T) {
	resetLoot(t)
	l := NewLoot()
	at := data.Position{X: 104, Y: 100}
	eld := percept.ItemRef{ID: 1, Pos: at, Name: "LumRune", Quality: 2, Class: 626}
	charm := percept.ItemRef{ID: 2, Pos: at, Name: "OrtRune", Quality: 4, Class: 618}
	modRow := percept.ItemRef{ID: 3, Pos: at, Name: "", Quality: 2, Class: 746}
	magicAxe := percept.ItemRef{ID: 4, Pos: at, Name: "Axe", Quality: 4, Class: 1}
	s := lootSnap(0, 20, eld, charm, modRow, magicAxe)
	for _, it := range []percept.ItemRef{eld, charm, modRow} {
		if l.wanted(s, it) <= 0 {
			t.Errorf("%s (row %d) must be wanted", it.Name, it.Class)
		}
	}
	if l.wanted(s, magicAxe) != 0 {
		t.Error("magic gear is sell-grade: skipped by default")
	}
	if l.wanted(s, eld) <= l.wanted(s, modRow) {
		t.Error("a tier S rune outranks a tier A unknown row")
	}
}

// Full bag, junk carried, a rune on the ground: Loot walks to it (approach
// only), never clicks it, and Discard bids once she stands at it.
func TestLootSwapApproachThenDiscard(t *testing.T) {
	resetLoot(t)
	l := NewLoot()
	d := NewDiscard()
	eld := percept.ItemRef{ID: 7, Pos: data.Position{X: 110, Y: 100}, Name: "LumRune", Quality: 2, Class: 626}
	s := lootSnap(1, 0, eld)
	s.Bag = []percept.BagItem{
		{Unit: 50, ID: 541, Name: "Scalp", GX: 0, GY: 0, Qual: 2, Ident: true},   // arrows: tier C
		{Unit: 51, ID: 533, Name: "Jawbone", GX: 1, GY: 0, Qual: 2, Ident: true}, // the TP tome
	}
	theLoot.observe(s)
	if theLoot.pending == nil || theLoot.pending.Act.String() != "swap" || theLoot.pending.Victim.Unit != 50 {
		t.Fatalf("pending = %+v, want a swap dropping the arrows", theLoot.pending)
	}
	if l.wanted(s, eld) <= 0 {
		t.Fatal("far from the prize: Loot approaches it")
	}
	if theLoot.takeable(s, eld) {
		t.Fatal("no room: the prize is not clickable")
	}
	if d.Demand(s) != nil {
		t.Fatal("Discard waits until she stands at the prize")
	}
	s2 := lootSnap(2, 0, eld)
	s2.Bag, s2.Me.Pos = s.Bag, data.Position{X: 108, Y: 100}
	theLoot.observe(s2)
	if l.wanted(s2, eld) != 0 {
		t.Fatal("at the prize Loot stands down for Discard")
	}
	if d.Demand(s2) == nil {
		t.Fatal("at the prize with a swap pending, Discard bids")
	}
	// Hostiles in the corridor: no bag ritual.
	s2.Enemies = []percept.EnemyRef{{ID: 9, Pos: data.Position{X: 105, Y: 100}}}
	if d.Demand(s2) != nil {
		t.Fatal("no bag ritual with a hostile in the corridor")
	}
	// The swap lands: the victim is never looted again and the prize is a take.
	theLoot.swapped(50)
	victim := percept.ItemRef{ID: 50, Pos: data.Position{X: 108, Y: 101}, Name: "Scalp", Quality: 2, Class: 541}
	s3 := lootSnap(3, 3, eld, victim)
	s3.Me.Pos = s2.Me.Pos
	if !theLoot.takeable(s3, eld) || theLoot.takeable(s3, victim) {
		t.Fatal("after the drop: the prize is a take, the victim is not")
	}
}

// Trying to progress (the march is lawful and bidding): a full bag keeps
// moving for tier A; tier S still makes room.
func TestLootProgressUrgency(t *testing.T) {
	resetLoot(t)
	l := NewLoot()
	rare := percept.ItemRef{ID: 1, Pos: data.Position{X: 110, Y: 100}, Quality: 6, Class: 1}
	junk := []percept.BagItem{{Unit: 60, ID: 1, GX: 0, GY: 0, Qual: 2, Ident: true}}
	s := lootSnap(0, 0, rare)
	s.Bag = junk
	if l.wanted(s, rare) <= 0 {
		t.Fatal("calm: a rare may displace plain junk")
	}
	marchLawfulUntil = time.Now().Add(2 * time.Second)
	if l.wanted(s, rare) != 0 {
		t.Fatal("urgent march, full bag: tier A is left behind")
	}
	uniq := percept.ItemRef{ID: 2, Pos: data.Position{X: 110, Y: 100}, Quality: 7, Class: 1}
	s = lootSnap(0, 0, uniq)
	s.Bag = junk
	if l.wanted(s, uniq) <= 0 {
		t.Fatal("urgent march: tier S still wins")
	}
}

// Nothing droppable but junk the Fence would take: a town trip for tier S,
// and the Fence sells while the trip is on.
func TestLootHaulTrip(t *testing.T) {
	resetLoot(t)
	h := NewHaul()
	uniq := percept.ItemRef{ID: 5, Pos: data.Position{X: 104, Y: 100}, Quality: 7, Class: 1}
	s := lootSnap(1, 0, uniq)
	s.Bag = []percept.BagItem{{Unit: 70, ID: 533, GX: 0, GY: 0, Qual: 2, Ident: true}}
	s.Junk = []percept.InvItem{{ID: 1, GX: 3, GY: 3, Qual: 2}} // an axe: 6 cells of Fence stock
	theLoot.observe(s)
	if theLoot.pending == nil || theLoot.pending.Act.String() != "haul" {
		t.Fatalf("pending = %+v, want a haul", theLoot.pending)
	}
	if h.Demand(s) == nil {
		t.Fatal("Haul bids for the trip")
	}
	// In town: the brain marks the town leg, the Fence bids despite a fat purse.
	town := lootSnap(2, 0)
	town.Me.InTown, town.Me.Area = true, 1
	town.Me.Gold, town.Me.JunkCount, town.Junk = 90000, 1, s.Junk
	theLoot.observe(town)
	if !theLoot.hauling() {
		t.Fatal("in town on the trip")
	}
	if NewFence().demand(town) == nil {
		t.Fatal("the Fence sells on a loot trip even when solvent and light")
	}
	if !ServicesPending(town) {
		t.Fatal("the trip's sale gates the march")
	}
	// Sold out: Haul rides the standing portal back.
	town2 := lootSnap(3, 3)
	town2.Me.InTown, town2.Me.Area = true, 1
	town2.Me.Gold, town2.Me.HPPct = 90000, 100
	town2.Me.BeltUsed = 8
	town2.Portals = []percept.PortalRef{{ID: 900, Pos: data.Position{X: 101, Y: 100}}}
	theLoot.observe(town2)
	if h.Demand(town2) == nil {
		t.Fatal("errands done: Haul rides the portal back")
	}
	// Back beside the item: the trip closes and Loot's plan reads take.
	back := lootSnap(4, 6, uniq) // the Fence freed the axe-sized junk
	theLoot.observe(back)
	if theLoot.pending != nil || !theLoot.takeable(back, uniq) {
		t.Fatalf("back: pending=%+v takeable=%v", theLoot.pending, theLoot.takeable(back, uniq))
	}
}
