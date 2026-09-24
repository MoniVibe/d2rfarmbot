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
