package inventory

import "testing"

// mod rows: 602 hp1, 607 mp1, 530 rvs (loot.Classify, shift undone)
const hp, mp, rv = 602, 607, 530

func belt(rows int, cols ...[]int) []BeltSlot {
	var out []BeltSlot
	u := uint32(100)
	for c, col := range cols {
		for r, id := range col {
			out = append(out, BeltSlot{Unit: u, ID: id, Index: r*4 + c})
			u++
		}
	}
	return out
}

func TestPotionOfUsesModCodes(t *testing.T) {
	if PotionOf(602) != PotHP || PotionOf(603) != PotHP || PotionOf(607) != PotMP || PotionOf(530) != PotRV || PotionOf(626) != PotNone {
		t.Fatal("hp1/hp2=hp, mp1=mp, rvs=rv, a rune is not a potion")
	}
}

func TestAllManaBeltEvictsForHealing(t *testing.T) {
	// the owner's belt: every column mana, healing waiting in the bag
	m := &Model{BeltRows: 4, Belt: belt(4, []int{mp, mp, mp, mp}, []int{mp, mp}, []int{mp, mp, mp}, []int{mp, mp, mp, mp}),
		Bag: []Item{{Unit: 1, ID: hp, GX: 8, GY: 7}}}
	mv, ok := m.BeltPlan()
	if !ok || mv.Kind != MoveEvict || mv.Column != 1 {
		t.Fatalf("want evict the lightest mana column (1), got %+v ok=%v", mv, ok)
	}
}

func TestHealingFillsItsColumnFirst(t *testing.T) {
	m := &Model{BeltRows: 4, Belt: belt(4, []int{hp, hp}, []int{mp}),
		Bag: []Item{{Unit: 7, ID: mp, GX: 1, GY: 1}, {Unit: 8, ID: hp, GX: 2, GY: 2}}}
	mv, ok := m.BeltPlan()
	if !ok || mv.Kind != MoveFill || mv.Unit != 8 {
		t.Fatalf("healing goes first, got %+v", mv)
	}
}

func TestManaNeverTakesTheReservedHealingColumn(t *testing.T) {
	// one hp column, one mana column full, two empty: mana may use at most one empty column
	m := &Model{BeltRows: 1, Belt: belt(1, []int{hp}, []int{mp}), Bag: []Item{{Unit: 9, ID: mp}}}
	if mv, ok := m.BeltPlan(); !ok || mv.Unit != 9 {
		t.Fatalf("one spare empty column: mana may fill it, got %+v ok=%v", mv, ok)
	}
	m = &Model{BeltRows: 1, Belt: belt(1, []int{hp}, []int{mp}, []int{mp}), Bag: []Item{{Unit: 9, ID: mp}}}
	if mv, ok := m.BeltPlan(); ok {
		t.Fatalf("the last empty column is reserved for healing, got %+v", mv)
	}
}

func TestFullGoodBeltNeedsNothing(t *testing.T) {
	m := &Model{BeltRows: 2, Belt: belt(2, []int{hp, hp}, []int{hp, hp}, []int{mp, mp}, []int{rv, rv}),
		Bag: []Item{{Unit: 5, ID: hp}}}
	if mv, ok := m.BeltPlan(); ok {
		t.Fatalf("a full belt with two healing columns: nothing to do, got %+v", mv)
	}
}
