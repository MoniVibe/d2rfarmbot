package loot

import (
	"strings"
	"testing"
)

func TestBagPlanDispositions(t *testing.T) {
	c := Default()
	p := c.NewPlanner(40)
	want := func(it Carried, d Disposition) {
		t.Helper()
		if got, why := p.Dispose(it); got != d {
			t.Fatalf("item %d q=%d ident=%v: want %v, got %v (%s)", it.Item.ID, it.Item.Quality, it.Identified, d, got, why)
		}
	}
	want(Carried{Item: Item{ID: 533}, Identified: true}, DispKeepBag)                     // TP tome
	want(Carried{Item: Item{ID: 564}, Identified: true}, DispKeepBag)                     // cube
	want(Carried{Item: Item{ID: 92, Quality: QUnique}, Identified: true}, DispKeepBag)    // Staff of Kings (quest)
	want(Carried{Item: Item{ID: 33, Quality: QMagic}}, DispIdentify)                      // unidentified blue sword
	want(Carried{Item: Item{ID: 33, Quality: QMagic}, Identified: true}, DispSell)        // identified blue sword
	want(Carried{Item: Item{ID: 33, Quality: QRare}, Identified: true}, DispSell)         // identified rare: sold (owner ruling)
	want(Carried{Item: Item{ID: 53, Quality: QUnique}, Identified: true}, DispStash)      // unique Trident
	want(Carried{Item: Item{ID: 626}, Identified: true}, DispStash)                       // a rune
	want(Carried{Item: Item{ID: 33, Quality: QMagic}, Identified: true, Upgrade: true}, DispWear)
	// charms: 12 cells stay (four grand charms = 12), the fifth goes to the stash
	for i := 0; i < 4; i++ {
		want(Carried{Item: Item{ID: 620, Quality: QMagic}, Identified: true}, DispKeepBag)
	}
	want(Carried{Item: Item{ID: 620, Quality: QMagic}, Identified: true}, DispStash)
	// a tight bag stashes every charm
	tight := c.NewPlanner(5)
	if d, _ := tight.Dispose(Carried{Item: Item{ID: 618, Quality: QMagic}, Identified: true}); d != DispStash {
		t.Fatalf("tight bag: charms go to the stash, got %v", d)
	}
}

func TestUniquesSellWhenDuplicateOrOutlevelled(t *testing.T) {
	p := Default().NewPlanner(40)
	p.Level = 30
	p.UniqueReq = func(row int) (string, int, bool) {
		switch row {
		case 5:
			return "tax", 3, true // Gull's-like low unique on a throwing axe
		case 9:
			return "tax", 25, true
		}
		return "", 0, false
	}
	u := func(row int) Carried {
		return Carried{Unique: row, Item: Item{ID: 44, Quality: QUnique}, Identified: true} // row 44 = tax (throwing axe)
	}
	if c := Classify(44); c.Code != "tax" {
		t.Skipf("row 44 is %s, not tax", c.Code)
	}
	if d, why := p.Dispose(u(5)); d != DispSell || !strings.HasPrefix(why, "weak unique") {
		t.Fatalf("req 3 at level 30: sell, got %v %s", d, why)
	}
	if d, _ := p.Dispose(u(9)); d != DispStash {
		t.Fatalf("req 25 at level 30: keep (stash), got %v", d)
	}
	if d, why := p.Dispose(u(9)); d != DispSell || !strings.HasPrefix(why, "duplicate unique") {
		t.Fatalf("second copy: sell, got %v %s", d, why)
	}
	if d, _ := p.Dispose(Carried{Unique: 5, Item: Item{ID: 44, Quality: QUnique}, Identified: true, Upgrade: true}); d != DispWear {
		t.Fatal("an upgrade is worn, never sold")
	}
}

// Owner 2026-09-25: "not fill it with low level uniques or duplicates" — a row
// already in the stash sells; req 22 at level 30 (inside the gap) is kept; a
// unique quiver without a bow sells; jewelry is kept at any level.
func TestStrongUniquesOnly(t *testing.T) {
	c := Default()
	req := func(row int) (string, int, bool) {
		switch row {
		case 5:
			return "tax", 22, true
		case 7:
			return "tax", 25, true
		case 11:
			return "rin", 3, true
		case 12:
			return "aqv", 15, true
		}
		return "", 0, false
	}
	tax := Classify(44)
	if tax.Code != "tax" {
		t.Skipf("row 44 is %s, not tax", tax.Code)
	}
	stash := func(row int) bool { return row == 7 }
	if weak, _ := c.WeakUnique(5, tax, 30, false, stash, req); weak {
		t.Fatal("req 22 at level 30 is within the gap: strong")
	}
	if weak, why := c.WeakUnique(7, tax, 30, false, stash, req); !weak || !strings.HasPrefix(why, "duplicate") {
		t.Fatalf("already in the stash: weak duplicate, got %v %s", weak, why)
	}
	if weak, _ := c.WeakUnique(11, Class{Code: "rin", Kind: KindJewelry}, 30, false, stash, req); weak {
		t.Fatal("a low ring is still kept (jewelry: duplicates only)")
	}
	if weak, _ := c.WeakUnique(12, Class{Code: "aqv", Kind: KindAmmo}, 30, false, stash, req); !weak {
		t.Fatal("a unique quiver with no bow is weak")
	}
	if weak, _ := c.WeakUnique(12, Class{Code: "aqv", Kind: KindAmmo}, 30, true, stash, req); weak {
		t.Fatal("with a bow, the quiver is kept")
	}
	if weak, _ := c.WeakUnique(7, Class{Code: "vip", Kind: KindQuest}, 30, false, func(int) bool { return true }, req); weak {
		t.Fatal("quest artifacts are never weak")
	}
	p := c.NewPlanner(40)
	p.Level, p.UniqueReq, p.Owned = 30, req, stash
	if d, _ := p.Dispose(Carried{Unique: 7, Item: Item{ID: 44, Quality: QUnique}, Identified: true}); d != DispSell {
		t.Fatalf("a bag copy of a stashed unique sells, got %v", d)
	}
}

// R56: scattered free cells are not room for a 1x3.
func TestFitsShape(t *testing.T) {
	// column x=0 filled at rows 0..6 with 1x1 potions (602), leaving (0,7) free;
	// every other column filled by 1x1s except one cell each at row 7.
	var bag []Carried
	for x := 0; x < BagW; x++ {
		for y := 0; y < BagH-1; y++ {
			bag = append(bag, Carried{Item: Item{ID: 602}, GX: x, GY: y})
		}
	}
	if !FitsShape(bag, 1, 1) {
		t.Fatal("a free row of 1x1 cells fits a 1x1")
	}
	if FitsShape(bag, 1, 3) {
		t.Fatal("ten free cells in one row do not fit a 1x3")
	}
	if !FitsShape(bag, 3, 1) {
		t.Fatal("a free row fits a 3x1")
	}
}

// R60: an unidentified unique quiver on the ground (row 0) is still weak with no bow.
func TestUnidentifiedQuiverWeak(t *testing.T) {
	if weak, _ := Default().WeakUnique(0, Class{Code: "cqv", Kind: KindAmmo}, 30, false, nil, nil); !weak {
		t.Fatal("no bow: a unique quiver is weak even unidentified")
	}
}
