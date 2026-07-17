package gear

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
)

func TestLoadTablesFixture(t *testing.T) {
	tb := loadFixtureTables(t)

	if got := tb.Stats.ItemsLoaded; got != 11 {
		t.Errorf("ItemsLoaded = %d, want 11", got)
	}
	if got := tb.Stats.TypesLoaded; got != 9 {
		t.Errorf("TypesLoaded = %d, want 9 (the codeless 'Any' row is skipped)", got)
	}
	if got := tb.Stats.GemsLoaded; got != 3 {
		t.Errorf("GemsLoaded = %d, want 3", got)
	}
	if got := tb.Stats.RecipesParsed; got != 3 {
		t.Errorf("RecipesParsed = %d, want 3 (got skips: %v)", got, tb.Stats.SkipReasons)
	}
	if got := tb.Stats.RecipesSkipped; got != 3 {
		t.Errorf("RecipesSkipped = %d, want 3 (%v)", got, tb.Stats.SkipReasons)
	}
	if tb.Stats.SkipReasons["disabled"] != 1 ||
		tb.Stats.SkipReasons["comment row"] != 1 ||
		tb.Stats.SkipReasons["unknown input code: Hellfire Torch"] != 1 {
		t.Errorf("unexpected skip reasons: %v", tb.Stats.SkipReasons)
	}

	capDef := tb.Items["cap"]
	if capDef == nil || capDef.Name != "Cap" || capDef.GemSockets != 2 || capDef.GemApplyType != 1 || capDef.MaxDef != 5 {
		t.Errorf("cap parsed wrong: %+v", capDef)
	}
}

func TestTypeTree(t *testing.T) {
	tb := loadFixtureTables(t)

	cases := []struct {
		itemCode, typeCode string
		want               bool
	}{
		{"cap", "helm", true},
		{"cap", "armo", true}, // via helm -> armo
		{"cap", "shie", false},
		{"cap", "cap", true}, // spec code may be the item code itself
		{"gcv", "gem", true}, // gema -> gem
		{"gcv", "armo", false},
		{"nope", "armo", false},
	}
	for _, c := range cases {
		if got := tb.IsA(c.itemCode, c.typeCode); got != c.want {
			t.Errorf("IsA(%q,%q) = %v, want %v", c.itemCode, c.typeCode, got, c.want)
		}
	}

	if got := tb.BodySlot("cap"); got != "head" {
		t.Errorf("BodySlot(cap) = %q, want head", got)
	}
	if got := tb.BodySlot("gcv"); got != "" {
		t.Errorf("BodySlot(gcv) = %q, want empty (gems are not equippable)", got)
	}
	if got := tb.MaxSocketsFor("cap"); got != 2 {
		t.Errorf("MaxSocketsFor(cap) = %d, want 2 (base gemsockets under type cap 3)", got)
	}
}

func TestParseInputSpec(t *testing.T) {
	spec := parseInputSpec("armo,mag,eth,sock=3,qty=2,nru")
	if spec.Code != "armo" || spec.Quality != item.QualityMagic || spec.Sockets != 3 ||
		spec.Quantity != 2 || !spec.NotRuneword || spec.Ethereal == nil || !*spec.Ethereal {
		t.Errorf("parsed spec wrong: %+v", spec)
	}
	if len(spec.Unknown) != 0 {
		t.Errorf("unexpected unknown qualifiers: %v", spec.Unknown)
	}

	spec = parseInputSpec("amu,noe,nos")
	if spec.Ethereal == nil || *spec.Ethereal || !spec.NoSockets || spec.Quantity != 1 || spec.Sockets != -1 {
		t.Errorf("parsed spec wrong: %+v", spec)
	}

	spec = parseInputSpec("amu,frobnicate")
	if len(spec.Unknown) != 1 {
		t.Errorf("unknown qualifier not captured: %+v", spec)
	}
}

func TestParseOutputSpec(t *testing.T) {
	out := parseOutputSpec("usetype,mod")
	if out.Kind != OutputUseType || !out.Regenerate {
		t.Errorf("usetype,mod parsed wrong: %+v", out)
	}
	out = parseOutputSpec("useitem")
	if out.Kind != OutputUseItem {
		t.Errorf("useitem parsed wrong: %+v", out)
	}
	out = parseOutputSpec(`gfv,qty=2`)
	if out.Kind != OutputCode || out.Code != "gfv" || out.Quantity != 2 {
		t.Errorf("code output parsed wrong: %+v", out)
	}
}

func TestMatches(t *testing.T) {
	tb := loadFixtureTables(t)

	magicCap := newTestItem(t, "cap", "Cap", item.QualityMagic)
	rareAmu := newTestItem(t, "amu", "Amulet", item.QualityRare)
	sockCap := newTestItem(t, "cap", "Cap", item.QualityNormal,
		stat.Data{ID: stat.NumSockets, Value: 2})

	cases := []struct {
		spec string
		it   data.Item
		want bool
	}{
		{"armo,mag", magicCap, true},
		{"helm,mag", magicCap, true},
		{"cap,mag", magicCap, true},
		{"armo,rar", magicCap, false},   // wrong quality
		{"amu,mag", rareAmu, false},     // wrong quality
		{"amu,rar", rareAmu, true},
		{"shie,mag", magicCap, false},   // wrong type branch
		{"cap,nos", magicCap, true},     // no sockets
		{"cap,nos", sockCap, false},     // has 2 sockets
		{"cap,sock=2", sockCap, true},
		{"cap,sock=1", sockCap, false},
		{"cap,sock", sockCap, true},     // bare sock = any socketed
		{"cap,eth", magicCap, false},    // not ethereal
		{"cap,frob", magicCap, false},   // unknown qualifier fails closed
	}
	for _, c := range cases {
		if got := tb.Matches(parseInputSpec(c.spec), c.it); got != c.want {
			t.Errorf("Matches(%q, %s/%v) = %v, want %v", c.spec, c.it.Desc().Code, c.it.Quality, got, c.want)
		}
	}
}

func TestNoPanicsOnMalformedRows(t *testing.T) {
	// A short, ragged, garbage-filled cubemain must never panic — only count.
	dir := writeFixtureDir(t)
	garbage := "description\tenabled\tnuminputs\tinput 1\toutput\n" +
		"just one cell\n" +
		"bad numinputs\t1\tbanana\tgcv\tgfv\n" +
		"no output\t1\t1\tgcv\t\n" +
		"\t\t\t\t\n"
	if err := writeFile(dir, "cubemain.txt", garbage); err != nil {
		t.Fatal(err)
	}
	tb, err := LoadTables(dir)
	if err != nil {
		t.Fatalf("LoadTables: %v", err)
	}
	if tb.Stats.RecipesParsed != 0 {
		t.Errorf("parsed %d garbage recipes", tb.Stats.RecipesParsed)
	}
	if tb.Stats.RecipesSkipped == 0 {
		t.Error("garbage rows were not counted as skipped")
	}
}
