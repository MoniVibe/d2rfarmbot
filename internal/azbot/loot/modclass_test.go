package loot

import (
	"testing"

	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
)

// With the installed mod's tables, rows classify by the mod's own code, type and
// footprint (owner 2026-09-25: "different item sizes footprints, modded or
// otherwise"). Skips on a machine without the mod.
func TestClassifyFromModTables(t *testing.T) {
	db, err := gamedata.Load(gamedata.DefaultRoot)
	if err != nil || db.Item(564) == nil || db.Item(564).Code != "box" {
		t.Skip("mod tables not installed")
	}
	gamedata.Set(db)
	defer gamedata.Set(nil)
	for _, c := range []struct {
		id   int
		code string
		kind Kind
		w, h int
	}{
		{564, "box", KindCube, 2, 2},
		{533, "tbk", KindTome, 1, 2},
		{602, "hp1", KindPotion, 1, 1},
		{92, "msf", KindQuest, 1, 3},
		{521, "wae", KindGear, 2, 2},       // a Warlock grimoire, not the Viper amulet
		{536, "vip", KindQuest, 1, 1},      // the Viper amulet, shifted
		{691, "cs2", KindCharm, 1, 3},      // the mod's grand charm: 1x3, not 1x1
		{692, "gmm", KindGem, 1, 1},        // the mod's Amethyst
		{708, "s01", KindRune, 1, 1},       // an El rune stack
		{741, "z01", KindAmmo, 1, 3},       // the mod's quiver
		{765, "ooa", KindModUnknown, 1, 1}, // Orb of Assemblage: the mod's own (tier S)
	} {
		got := Classify(c.id)
		if got.Code != c.code || got.Kind != c.kind || got.W != c.w || got.H != c.h {
			t.Errorf("row %d: got %s/%s %dx%d, want %s/%s %dx%d", c.id, got.Code, got.Kind, got.W, got.H, c.code, c.kind, c.w, c.h)
		}
	}
	if !Lifeline(564) || !Lifeline(533) {
		t.Fatal("cube and tome stay lifelines on the mod table")
	}
}

// Owner 2026-09-25: only "above unique baseline" — normal-tier bases are weak.
func TestNormalBaseFromModTables(t *testing.T) {
	db, err := gamedata.Load(gamedata.DefaultRoot)
	if err != nil || db.ItemByCode("hax") == nil {
		t.Skip("mod tables not installed")
	}
	gamedata.Set(db)
	defer gamedata.Set(nil)
	if !NormalBase("hax") || NormalBase("9ha") || NormalBase("7ha") {
		t.Fatal("hand axe is normal; hatchet (exceptional) and tomahawk (elite) are not")
	}
	if weak, _ := Default().WeakUnique(0, Classify(db.ItemByCode("hax").ID), 30, false, nil, nil); !weak {
		t.Fatal("an unidentified unique on a normal base is weak on the ground")
	}
	if weak, _ := Default().WeakUnique(0, Classify(db.ItemByCode("9ha").ID), 30, false, nil, nil); weak {
		t.Fatal("an exceptional base unique is taken")
	}
}
