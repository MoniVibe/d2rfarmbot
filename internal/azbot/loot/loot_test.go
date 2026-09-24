package loot

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The +15 shift, pinned to the measured anchors (relay flights and the R2 bag capture).
func TestClassifyUndoesTheModShift(t *testing.T) {
	cases := []struct {
		id   int
		kind Kind
		code string
	}{
		{533, KindTome, "tbk"}, {534, KindTome, "ibk"},
		{538, KindGold, "gld"},                         // "Fang" on the ground
		{541, KindAmmo, "aqv"}, {543, KindAmmo, "cqv"}, // "Scalp", "Key"
		{535, KindJewelry, "amu"}, {537, KindJewelry, "rin"}, // "Horn", "Flag"
		{564, KindCube, "box"},
		{602, KindPotion, "hp1"}, {603, KindPotion, "hp2"}, {608, KindPotion, "mp2"},
		{618, KindCharm, "cm1"}, {620, KindCharm, "cm3"}, // "OrtRune", "AmnRune"
		{625, KindRune, "r01"}, {626, KindRune, "r02"}, // "IoRune" = El, "LumRune" = Eld
		{658, KindJewel, "jew"},
		{673, KindQuest, "std"},
		{674, KindModUnknown, ""}, {746, KindModUnknown, ""},
		{0, KindGear, "hax"}, {509, KindGear, ""},
	}
	for _, c := range cases {
		got := Classify(c.id)
		if got.Kind != c.kind || (c.code != "" && got.Code != c.code) {
			t.Errorf("Classify(%d) = %s/%q, want %s/%q", c.id, got.Kind, got.Code, c.kind, c.code)
		}
	}
	if cl := Classify(564); cl.Cells() != 4 {
		t.Errorf("the cube is 2x2, got %d cells", cl.Cells())
	}
	for _, id := range []int{533, 534, 564, 549} {
		if !Lifeline(id) {
			t.Errorf("%d must be a lifeline", id)
		}
	}
	if Lifeline(626) {
		t.Error("a rune is not a lifeline")
	}
}

func TestEvaluateTiers(t *testing.T) {
	c := wide()
	cases := []struct {
		it   Item
		tier Tier
	}{
		{Item{ID: 0, Quality: QUnique}, TierS},
		{Item{ID: 0, Quality: QSet}, TierS},
		{Item{ID: 626, Quality: QNormal, Name: "LumRune"}, TierS},          // Eld rune
		{Item{ID: 618, Quality: QMagic, Name: "OrtRune"}, TierS},           // magic small charm
		{Item{ID: 658, Quality: QRare}, TierS},                             // rare jewel
		{Item{ID: 0, Quality: QRare}, TierA},                               // rare gear
		{Item{ID: 537, Quality: QMagic, Name: "Flag"}, TierA},              // magic ring
		{Item{ID: 538, Quality: QNormal, Name: "Fang"}, TierA},             // gold
		{Item{ID: 0, Quality: QMagic}, TierB},                              // magic gear
		{Item{ID: 0, Quality: QNormal}, TierC},                             // plain gear
		{Item{ID: 541, Quality: QNormal, Name: "Scalp"}, TierC},            // arrows
		{Item{ID: 530, Quality: QNormal, Name: "ScrollOfIdentify"}, TierA}, // rejuv (vanilla 515) = potion
	}
	for _, cs := range cases {
		if v := c.Evaluate(cs.it); v.Tier != cs.tier {
			t.Errorf("Evaluate(%+v) = %s (%s), want %s", cs.it, v.Tier, v.Why, cs.tier)
		}
	}
	// Unique beats rare beats magic inside the value order.
	u, r := c.Evaluate(Item{ID: 0, Quality: QUnique}), c.Evaluate(Item{ID: 0, Quality: QRare})
	if u.Value <= r.Value {
		t.Error("a unique must outvalue a rare")
	}
}

// New mod bits are never ignored: unknown rows and impossible qualities are A.
func TestUnknownModItemsWideA(t *testing.T) {
	c := wide()
	if v := c.Evaluate(Item{ID: 746, Quality: QNormal}); v.Tier != TierA || !strings.Contains(v.Why, "unknown mod row") {
		t.Fatalf("unknown mod row: %s (%s), want A", v.Tier, v.Why)
	}
	if v := c.Evaluate(Item{ID: 999, Quality: QMagic}); v.Tier != TierA {
		t.Fatalf("unknown magic mod row: %s, want A", v.Tier)
	}
	// A rune row that shows magic quality is not a rune: the table is wrong.
	if v := c.Evaluate(Item{ID: 626, Quality: QMagic}); v.Tier != TierA || !strings.Contains(v.Why, "table is wrong") {
		t.Fatalf("contradiction: %s (%s), want A", v.Tier, v.Why)
	}
	// ...and the owner's tag wins over the default.
	c.IDs[746] = TierS
	if v := c.Evaluate(Item{ID: 746, Quality: QNormal}); v.Tier != TierS {
		t.Fatalf("tagged 746: %s, want S", v.Tier)
	}
}

func TestConfigParsing(t *testing.T) {
	doc := `
tiers:
  rare: B
  unknown_mod_item: S
ids:
  746: S
  603: c
codes:
  R01: B
names:
  orb: S
  sacredglobe: A
space:
  pick_tier_b: true
  sell_rares_pct: 50
census_every: 90s
`
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if c.Defaults.Rare != TierB || c.Defaults.UnknownModItem != TierS || c.Defaults.Unique != TierS {
		t.Fatalf("tiers: %+v", c.Defaults)
	}
	if c.IDs[746] != TierS || c.IDs[603] != TierC || c.Codes["r01"] != TierB {
		t.Fatalf("tags: ids=%v codes=%v", c.IDs, c.Codes)
	}
	if len(c.Names) != 2 || c.Names[0].Sub != "sacredglobe" {
		t.Fatalf("names must sort longest first: %+v", c.Names)
	}
	if !c.Space.PickTierB || c.Space.SellRaresPct != 50 || !c.Space.SwapForS || c.CensusEvery != 90*time.Second {
		t.Fatalf("space/census: %+v %v", c.Space, c.CensusEvery)
	}
	if v := c.Evaluate(Item{ID: 625}); v.Tier != TierB || !strings.Contains(v.Why, "config code r01") {
		t.Fatalf("code tag: %s %s", v.Tier, v.Why)
	}
	if v := c.Evaluate(Item{ID: 3, Name: "SacredGlobe", Quality: QNormal}); v.Tier != TierA {
		t.Fatalf("name tag: %s %s", v.Tier, v.Why)
	}
	for _, bad := range []string{
		"tiers: {rare: X}",
		"tiers: {rares: A}",
		"ids: {746: SS}",
		"unknown_key: 1",
		"space: {sell_rares_pct: 150}",
		"census_every: 1s",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) accepted a bad document", bad)
		}
	}
	if c, err := Parse(nil); err != nil || c.Defaults.Unique != TierS {
		t.Fatalf("an empty document is the defaults: %v", err)
	}
}

// The shipped config/loot.yaml parses and equals the built-in defaults.
func TestShippedConfigMatchesDefaults(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "config", "loot.yaml")
	c, ok, err := Load(path)
	if err != nil || !ok {
		t.Fatalf("load %s: ok=%v err=%v", path, ok, err)
	}
	d := Default()
	if c.Defaults != d.Defaults || c.Space != d.Space || c.CensusEvery != d.CensusEvery {
		t.Fatalf("shipped config drifted from wide():\n%+v\n%+v", c, d)
	}
	if _, ok, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); ok || err != nil {
		t.Fatal("a missing file is the defaults, not an error")
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(bad, []byte("tiers: {rare: Q}"), 0o644)
	if c, _, err := Load(bad); err == nil || c == nil {
		t.Fatal("a bad file reports its error and still hands back defaults")
	}
}

func bagOf(items ...Carried) []Carried { return items }

func TestPlanSpaceDecisions(t *testing.T) {
	c := wide()
	unique := Item{ID: 0, Quality: QUnique} // hand axe: 1x3
	rare := Item{ID: 0, Quality: QRare}
	junkAxe := Carried{Unit: 11, Item: Item{ID: 0, Quality: QNormal}, Identified: true} // plain gear, 3 cells
	idRare := Carried{Unit: 12, Item: Item{ID: 0, Quality: QRare}, Identified: true}    // shown its hand: B
	tome := Carried{Unit: 13, Item: Item{ID: TomeTP, Quality: QNormal}, Identified: true}
	charm := Carried{Unit: 14, Item: Item{ID: 620, Quality: QMagic}, Identified: true}

	// Room: take.
	if p := c.Plan(unique, Situation{Free: 10}); p.Act != Take {
		t.Fatalf("room: %s (%s)", p.Act, p.Why)
	}
	// Full, junk carried: S swaps the worst.
	p := c.Plan(unique, Situation{Free: 0, Bag: bagOf(tome, charm, idRare, junkAxe)})
	if p.Act != Swap || p.Drop == nil || p.Drop.Unit != 11 {
		t.Fatalf("S full with junk: %s drop=%v (%s)", p.Act, p.Drop, p.Why)
	}
	// Lifelines and working charms are never dropped; with only them, S hauls.
	sit := Situation{Free: 0, Bag: bagOf(tome, charm), HaulOK: true, SellCells: 4}
	if p := c.Plan(unique, sit); p.Act != Haul {
		t.Fatalf("S full, nothing droppable, junk to sell: %s (%s)", p.Act, p.Why)
	}
	sit.SellCells = 0
	if p := c.Plan(unique, sit); p.Act != Skip || !strings.Contains(p.Why, "stash") {
		t.Fatalf("S full of keepers: %s (%s)", p.Act, p.Why)
	}
	sit.SellCells, sit.HaulOK = 4, false
	if p := c.Plan(unique, sit); p.Act != Skip {
		t.Fatalf("no portal road: %s", p.Act)
	}
	// A swaps only for something it beats: an identified non-upgrade rare is B.
	if p := c.Plan(rare, Situation{Free: 0, Bag: bagOf(idRare)}); p.Act != Swap {
		t.Fatalf("A beats an identified rare: %s (%s)", p.Act, p.Why)
	}
	unid := Carried{Unit: 15, Item: Item{ID: 0, Quality: QRare}}
	if p := c.Plan(rare, Situation{Free: 0, Bag: bagOf(unid, tome)}); p.Act != Skip {
		t.Fatalf("A must not displace an equal unidentified rare: %s", p.Act)
	}
	// An upgrade is pinned.
	up := Carried{Unit: 16, Item: Item{ID: 0, Quality: QNormal}, Identified: true, Upgrade: true}
	if p := c.Plan(unique, Situation{Free: 0, Bag: bagOf(up)}); p.Act == Swap {
		t.Fatal("an upgrade is never dropped")
	}
	// The dropped item must free enough room.
	arrows := Carried{Unit: 17, Item: Item{ID: 541, Quality: QNormal}, Identified: true} // arrows 1x3
	if p := c.Plan(Item{ID: 564, Quality: QNormal}, Situation{Free: 0, Bag: bagOf(arrows)}); p.Act == Swap {
		t.Fatalf("a 3-cell drop cannot make room for a 4-cell item: %s", p.Why)
	}
}

// "Trying to progress": with the march urgent and the bag full, tier A waits
// and tier S still wins.
func TestPlanProgressUrgency(t *testing.T) {
	c := wide()
	junk := Carried{Unit: 1, Item: Item{ID: 0, Quality: QNormal}, Identified: true}
	sit := Situation{Free: 0, Urgent: true, Bag: bagOf(junk)}
	if p := c.Plan(Item{ID: 0, Quality: QRare}, sit); p.Act != Skip || !strings.Contains(p.Why, "urgent") {
		t.Fatalf("urgent + full: tier A must be skipped, got %s (%s)", p.Act, p.Why)
	}
	if p := c.Plan(Item{ID: 626, Quality: QNormal}, sit); p.Act != Swap {
		t.Fatalf("urgent + full: tier S always wins, got %s (%s)", p.Act, p.Why)
	}
	// Room to spare: urgency does not stop an A pickup.
	sit.Free = 20
	if p := c.Plan(Item{ID: 0, Quality: QRare}, sit); p.Act != Take {
		t.Fatalf("urgent with room: %s", p.Act)
	}
	// Tier B stays on the ground by default; with pick_tier_b only when roomy and calm.
	if p := c.Plan(Item{ID: 0, Quality: QMagic}, Situation{Free: 70}); p.Act != Skip {
		t.Fatal("tier B is skipped by default")
	}
	c.Space.PickTierB = true
	if p := c.Plan(Item{ID: 0, Quality: QMagic}, Situation{Free: 70}); p.Act != Take {
		t.Fatal("pick_tier_b: roomy and calm takes B")
	}
	if p := c.Plan(Item{ID: 0, Quality: QMagic}, Situation{Free: 70, Urgent: true}); p.Act != Skip {
		t.Fatal("pick_tier_b: never while marching")
	}
}

func TestPlanPotionsAndGold(t *testing.T) {
	c := wide()
	red := Item{ID: 603, Quality: QNormal, Potion: "health"}
	if p := c.Plan(red, Situation{Free: 0, BeltFree: 3}); p.Act != Skip || !strings.Contains(p.Why, "gated") {
		t.Fatalf("potions gated off: %s (%s)", p.Act, p.Why)
	}
	if p := c.Plan(red, Situation{Free: 0, BeltFree: 3, PotionsOn: true}); p.Act != Take || p.Need != 0 {
		t.Fatalf("belt room: %s need=%d", p.Act, p.Need)
	}
	if p := c.Plan(red, Situation{Free: 40, BeltFree: 0, PotionsOn: true}); p.Act != Skip {
		t.Fatal("full belt: no bottle")
	}
	if p := c.Plan(Item{ID: 538, Quality: QNormal}, Situation{Free: 0}); p.Act != Take {
		t.Fatalf("gold needs no bag room: %s (%s)", p.Act, p.Why)
	}
}

func TestSellGradeAndKeep(t *testing.T) {
	c := wide()
	idRare := Carried{Item: Item{ID: 0, Quality: QRare}, Identified: true}
	if c.SellGrade(idRare, 40) {
		t.Fatal("rares are kept while the bag is roomy")
	}
	if !c.SellGrade(idRare, 75) {
		t.Fatal("past sell_rares_pct an identified non-upgrade rare is merchandise")
	}
	if c.SellGrade(Carried{Item: Item{ID: 626, Quality: QNormal}, Identified: true}, 100) {
		t.Fatal("a rune is never merchandise")
	}
	for _, id := range []int{564, 626, 618, 746, 533} {
		q := QNormal
		if id == 618 {
			q = QMagic
		}
		if !c.Keep(id, q) {
			t.Errorf("Keep(%d) = false: the Fence would sell it", id)
		}
	}
	if c.Keep(541, QNormal) || c.Keep(0, QMagic) {
		t.Error("arrows and magic gear are merchandise")
	}
}

func TestCensusAndCatalog(t *testing.T) {
	t0 := time.Unix(1000, 0)
	cs := NewCensus(time.Minute, t0)
	cs.Seen(1, TierS)
	cs.Seen(1, TierC) // first sighting counts
	cs.Seen(2, TierC)
	cs.Picked(TierS)
	cs.Skipped(2, TierC, "policy")
	cs.Skipped(2, TierC, "policy") // once per unit
	if _, ok := cs.Line(t0.Add(30 * time.Second)); ok {
		t.Fatal("census before its window")
	}
	line, ok := cs.Line(t0.Add(61 * time.Second))
	if !ok || !strings.Contains(line, "seen=S1/A0/B0/C1") || !strings.Contains(line, "picked=S1/") || !strings.Contains(line, "C:policy=1") {
		t.Fatalf("census line: %q", line)
	}
	cat := NewCatalog()
	c := wide()
	it := Item{ID: 746, Name: "", Quality: QNormal}
	e, isNew := cat.Note(it, c.Evaluate(it), 3, "ground", t0)
	if !isNew || e.Tier != "A" || e.TagHint == "" || e.VanillaID != -1 {
		t.Fatalf("first sighting: %+v %v", e, isNew)
	}
	if _, again := cat.Note(it, c.Evaluate(it), 3, "ground", t0); again {
		t.Fatal("a row is cataloged once")
	}
	cat.Know(CatalogKey(626, QNormal))
	if _, isNew := cat.Note(Item{ID: 626, Quality: QNormal}, c.Evaluate(Item{ID: 626}), 1, "bag", t0); isNew {
		t.Fatal("a row remembered from memory is not new")
	}
}

// Occupancy on the mod's 10x8 bag: the union of footprints, clipped — the cube
// (row 564) is 2x2 here, not the 1x1 topaz d2go's row says.
func TestOccupiedBag(t *testing.T) {
	type at struct{ id, gx, gy int }
	bag := []at{
		{TomeTP, 0, 0}, {TomeID, 1, 0}, // tomes 1x2 each
		{Cube, 2, 0}, // 2x2
		{626, 8, 7},  // a rune 1x1
		{626, 8, 7},  // a misread duplicate: never counted twice
		{0, 9, 6},    // a 1x3 axe hanging off the bottom: clipped to 2 cells
	}
	n := Occupied(len(bag), func(i int) (int, int, int) { return bag[i].id, bag[i].gx, bag[i].gy })
	if want := 2 + 2 + 4 + 1 + 2; n != want {
		t.Fatalf("Occupied = %d, want %d", n, want)
	}
	if BagCells != 80 {
		t.Fatal("this mod's bag is 10x8")
	}
}

// wide is the pre-ruling policy: the mechanics tests (swap, haul, sell, census)
// exercise every tier with it; the shipped default is the owner's narrow rule.
func wide() *Config {
	c := Default()
	c.Defaults = Defaults{
		Unique: TierS, Set: TierS, Rare: TierA, Crafted: TierA,
		MagicJewelry: TierA, MagicGear: TierB, PlainGear: TierC,
		Rune: TierS, Gem: TierS, Jewel: TierS, Charm: TierS,
		Gold: TierA, Potion: TierA, Quest: TierS, Ammo: TierC,
		Scroll: TierC, Misc: TierC,
		UnknownModItem: TierA, QualityContradiction: TierA,
	}
	c.Space.SwapForA = true
	return c
}

// The owner's ruling (2026-09-24): only uniques and special items by default.
func TestDefaultIsUniquesAndSpecialOnly(t *testing.T) {
	d := Default().Defaults
	if d.Unique != TierS || d.Quest != TierS || d.UnknownModItem != TierS {
		t.Fatalf("uniques, quest items and mod rows must be S: %+v", d)
	}
	if d.Rune != TierS || d.Gem != TierS || d.Charm != TierS {
		t.Fatalf("runes, gems and charms must be S: %+v", d)
	}
	for name, tr := range map[string]Tier{"set": d.Set, "rare": d.Rare,
		"gold": d.Gold, "potion": d.Potion, "magic_jewelry": d.MagicJewelry} {
		if tr != TierC {
			t.Errorf("%s must be C under the ruling, got %v", name, tr)
		}
	}
}

func TestUniqueCharmCarriedOnceIsSkipped(t *testing.T) {
	c := Default()
	charm := Item{ID: 620, Quality: QUnique}
	if v := c.Evaluate(charm); v.Class.Kind != KindCharm {
		t.Skipf("id 620 is not a charm row here (%v)", v.Class.Kind)
	}
	sit := Situation{Free: 20, Bag: []Carried{{Item: charm}}}
	if p := c.Plan(charm, sit); p.Act != Skip || !strings.Contains(p.Why, "already carried") {
		t.Fatalf("second unique charm: %+v", p)
	}
	if p := c.Plan(charm, Situation{Free: 20}); p.Act == Skip {
		t.Fatalf("first unique charm must be taken: %+v", p)
	}
}
