package loot

import "testing"

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
	if d, why := p.Dispose(u(5)); d != DispSell || why != "outlevelled unique" {
		t.Fatalf("req 3 at level 30: sell, got %v %s", d, why)
	}
	if d, _ := p.Dispose(u(9)); d != DispStash {
		t.Fatalf("req 25 at level 30: keep (stash), got %v", d)
	}
	if d, why := p.Dispose(u(9)); d != DispSell || why != "duplicate unique" {
		t.Fatalf("second copy: sell, got %v %s", d, why)
	}
	if d, _ := p.Dispose(Carried{Unique: 5, Item: Item{ID: 44, Quality: QUnique}, Identified: true, Upgrade: true}); d != DispWear {
		t.Fatal("an upgrade is worn, never sold")
	}
}
