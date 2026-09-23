package percept

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
)

func TestPotionKindForItemRecognizesVariants(t *testing.T) {
	for _, id := range []int{509, 511, 587, 588, 589, 590, 591, 602, 606, 515, 516} {
		if got := potionKindForItem(data.Item{ID: id}); got != potionHealth {
			t.Errorf("ID %d: got potion kind %d, want health", id, got)
		}
	}
	for _, id := range []int{510, 512, 592, 593, 594, 595, 596, 608, 609} {
		if got := potionKindForItem(data.Item{ID: id}); got != potionMana {
			t.Errorf("ID %d: got potion kind %d, want mana", id, got)
		}
	}

	// 607 is the measured legacy mana row unless the mod gives us a trustworthy
	// red/blue name at runtime.
	if got := potionKindForItem(data.Item{ID: 607}); got != potionMana {
		t.Errorf("unknown ID 607: got potion kind %d, want legacy mana", got)
	}
	if got := potionKindForItem(data.Item{ID: 607, Name: item.Name("LargeRedPotion")}); got != potionHealth {
		t.Errorf("named red ID 607: got potion kind %d, want health", got)
	}
	if got := potionKindForItem(data.Item{ID: 607, Name: item.Name("LargeBluePotion")}); got != potionMana {
		t.Errorf("named blue ID 607: got potion kind %d, want mana", got)
	}

	for _, name := range []string{"hp1", "hp2", "hp3", "hp4", "hp5", "HealthPotion2", "Healing Potion 2"} {
		if got := potionKindForItem(data.Item{Name: item.Name(name)}); got != potionHealth {
			t.Errorf("name %q: got potion kind %d, want health", name, got)
		}
	}
	if got := potionKindForItem(data.Item{Name: item.Name("Herb")}); got != potionHealth {
		t.Errorf("name %q: got potion kind %d, want health", "Herb", got)
	}
	for _, name := range []string{"mp1", "mp2", "mp3", "mp4", "mp5", "ManaPotion2", "Mana Potion 2"} {
		if got := potionKindForItem(data.Item{Name: item.Name(name)}); got != potionMana {
			t.Errorf("name %q: got potion kind %d, want mana", name, got)
		}
	}
	if got := potionKindForItem(data.Item{ID: 607, Name: item.Name("hp2")}); got != potionHealth {
		t.Errorf("tiered health name on ID 607: got potion kind %d, want health", got)
	}

	if got := potionKindForItem(data.Item{ID: 603, Name: item.Name("SmallCharm")}); got != potionNone {
		t.Errorf("charm: got potion kind %d, want none", got)
	}
}

// The live belt reports its red potions as ID 603 "SmallCharm" (beltdump
// 2026-09-23). In the belt that is health; in the bag it stays a charm.
func TestBelt603IsHealthButBag603IsCharm(t *testing.T) {
	it := data.Item{ID: 603, Name: item.Name("SmallCharm")}
	if got := potionKindForBeltItem(it); got != potionHealth {
		t.Errorf("belt 603: got potion kind %d, want health", got)
	}
	if got := potionKindForItem(it); got != potionNone {
		t.Errorf("bag 603: got potion kind %d, want none", got)
	}
}

func TestBeltColumnNormalizesFlattenedSlots(t *testing.T) {
	for x, want := range []int{0, 1, 2, 3, 0, 1, 2, 3, 0, 1, 2, 3, 0, 1, 2, 3} {
		got, ok := beltColumn(data.Position{X: x})
		if !ok || got != want {
			t.Errorf("slot x=%d: got (%d,%v), want (%d,true)", x, got, ok, want)
		}
	}
	for _, x := range []int{-1, 16} {
		if got, ok := beltColumn(data.Position{X: x}); ok {
			t.Errorf("invalid slot x=%d: got (%d,true), want false", x, got)
		}
	}
}

func TestAppendBeltColumnDeduplicatesKeyColumns(t *testing.T) {
	cols := appendBeltColumn(nil, 2)
	cols = appendBeltColumn(cols, 6%4)
	cols = appendBeltColumn(cols, 1)
	if len(cols) != 2 || cols[0] != 2 || cols[1] != 1 {
		t.Fatalf("got columns %v, want [2 1]", cols)
	}
}
