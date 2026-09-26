package gamedata

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testdata/flat holds SMALL SYNTHETIC tables (invented rows, never mod data):
// weapons.txt and misc.txt are CRLF, armor.txt has a blank line, and every
// numbered table carries an "Expansion" divider the game does not count.
func loadFixture(t *testing.T) *DB {
	t.Helper()
	db, err := Load(filepath.Join("testdata", "flat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Missing) != 0 {
		t.Fatalf("missing tables: %v", db.Missing)
	}
	return db
}

func TestItemClassIDsSkipExpansionAndSpanTables(t *testing.T) {
	db := loadFixture(t)
	want := []string{"tax", "tcl", "trc", "tvs", "tpv", "ttb", "tgd", "tr1", "tp1", "trj", "tcm", "tgm", "tqb", "tje"}
	var got []string
	for i, it := range db.Items {
		if it.ID != i {
			t.Fatalf("item %d carries ID %d", i, it.ID)
		}
		got = append(got, it.Code)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("item order\n got %v\nwant %v", got, want)
	}
	if it := db.Item(1); it.Source != "weapons" || it.TwoHandMaxDam != 30 || !it.TwoHanded || it.ReqStr != 40 || it.LevelReq != 18 {
		t.Errorf("CRLF weapons row misparsed: %+v", it)
	}
	if it := db.Item(4); it.Source != "armor" || it.MinDef != 40 || it.MaxDef != 55 || it.InvW != 2 || it.InvH != 3 {
		t.Errorf("armor after a blank line misparsed: %+v", it)
	}
	if it := db.ItemByCode("tqb"); it == nil || it.ID != 12 || it.Category != CatQuest || !it.IsQuest() {
		t.Errorf("quest misc row: %+v", it)
	}
	if db.Item(-1) != nil || db.Item(len(db.Items)) != nil {
		t.Error("out-of-range ids must be nil")
	}
}

func TestItemNamesResolveAndClean(t *testing.T) {
	db := loadFixture(t)
	cases := map[string]string{
		"tax": "Practice Axe",   // plain key (item-names.json with a BOM)
		"tcl": "Heavy Cleaver",  // color code stripped
		"tp1": "HP1",            // the mod's renamed potions keep their short name
		"tr1": "Test Rune (#1)", // item-runes.json; the ~Pick Up~ line above the name dropped
		"trc": "Relic Club",     // unresolved namestr falls back to the table name
		"tpv": "Plated Vest",    // no string at all: the table name
	}
	for code, want := range cases {
		if got := db.ItemByCode(code).Name; got != want {
			t.Errorf("%s: name %q, want %q", code, got, want)
		}
	}
	if got := db.ItemName(7); got != "Test Rune (#1)" {
		t.Errorf("ItemName(7) = %q", got)
	}
	if u := db.Uniques; len(u) != 2 || u[1].ID != 1 || u[1].Key != "Ironhide" || u[1].Enabled || u[0].LevelReq != 3 || u[0].Code != "tax" {
		t.Errorf("uniques: %+v %+v", u[0], u[len(u)-1])
	}
	if s := db.SetItems; len(s) != 1 || s[0].Set != "Tester Garb" || s[0].Code != "tvs" || s[0].LevelReq != 2 {
		t.Errorf("set items: %+v", s)
	}
}

func TestTypeHierarchyFlattened(t *testing.T) {
	db := loadFixture(t)
	rj := db.ItemByCode("trj")
	if want := []string{"rpot", "hpot", "mpot", "poti", "misc"}; !reflect.DeepEqual(rj.Types, want) {
		t.Errorf("rejuv closure %v, want %v", rj.Types, want)
	}
	if !rj.IsPotion() || !rj.Is("mpot") || rj.Category != CatPotion || !rj.Beltable {
		t.Errorf("rejuv flags: %+v", rj)
	}
	checks := []struct {
		code string
		cat  Category
		is   func(*Item) bool
	}{
		{"tr1", CatRune, (*Item).IsRune}, {"tgm", CatGem, (*Item).IsGem}, {"tcm", CatCharm, (*Item).IsCharm},
		{"tje", CatJewel, (*Item).IsJewel}, {"tgd", CatGold, (*Item).IsGold}, {"ttb", CatTome, (*Item).IsTome},
		{"tp1", CatPotion, (*Item).IsPotion},
	}
	for _, c := range checks {
		it := db.ItemByCode(c.code)
		if it.Category != c.cat || !c.is(it) {
			t.Errorf("%s: category %s (types %v), want %s", c.code, it.Category, it.Types, c.cat)
		}
	}
	if it := db.ItemByCode("tax"); it.Category != CatWeapon || !it.Is("weap") || it.Is("misc") {
		t.Errorf("axe: %s %v", it.Category, it.Types) // the duplicate "axe" type row must not win
	}
	if it := db.ItemByCode("trc"); it.Category != CatQuest || !it.Is("weap") {
		t.Errorf("quest weapon: %s %v", it.Category, it.Types)
	}
	if it := db.ItemByCode("tvs"); it.Category != CatArmor || it.Beltable {
		t.Errorf("vest: %s beltable=%v", it.Category, it.Beltable)
	}
}

func TestMonsters(t *testing.T) {
	db := loadFixture(t)
	if len(db.Monsters) != 3 {
		t.Fatalf("monsters %d", len(db.Monsters))
	}
	b := db.Monster(1)
	if b.Code != "testboss" || b.Name != "Boss of Tests" || !b.Boss || b.Level != [3]int{12, 40, 70} {
		t.Errorf("boss: %+v", b)
	}
	if !b.HasSize || b.Size.SizeX != 3 || b.Size.HtTop != -150 || b.Size.HtHeight != 150 || !b.Size.NoGfxHitTest || !b.Size.Large {
		t.Errorf("boss size via MonStatsEx: %+v", b.Size)
	}
	if got := b.Immunities(Normal); !reflect.DeepEqual(got, []Element{Lightning}) {
		t.Errorf("normal immunities %v", got)
	}
	if got := b.Immunities(Hell); !reflect.DeepEqual(got, []Element{Physical, Fire, Lightning}) {
		t.Errorf("hell immunities %v", got)
	}
	beast := db.MonsterByCode("testbeast")
	if beast.Immune(Nightmare, Fire) || !beast.Immune(Hell, Poison) || beast.Res[Hell][Fire] != 75 {
		t.Errorf("beast resists %v", beast.Res)
	}
	if v := db.Monster(2); v.Name != "Missing Key" || !v.NPC || v.Killable || v.HasSize {
		t.Errorf("vendor: %+v", v)
	}
	if su := db.SuperUnique(7); su == nil || su.Class != "testboss" || su.MonsterID != 1 {
		t.Errorf("superunique hcIdx 7: %+v", su)
	}
	if su := db.SuperUniques[0]; su.Name != "Chief Tester" || su.MonsterID != 0 {
		t.Errorf("superunique 0: %+v", su)
	}
}

func TestLevelsAndWarps(t *testing.T) {
	db := loadFixture(t)
	if len(db.Levels) != 4 {
		t.Fatalf("levels %v", db.LevelIDs())
	}
	f := db.Level(2)
	if f.Name != "The Test Fields" || f.Waypoint != -1 || f.IsTown || f.MonLvl != [3]int{4, 36, 80} || f.MonLvlClassic != [3]int{2, 27, 52} {
		t.Errorf("fields: %+v", f)
	}
	if !reflect.DeepEqual(f.Exits, []Exit{{1, -1}, {3, 1}}) || !reflect.DeepEqual(f.Mon, []string{"testbeast", "testboss"}) {
		t.Errorf("fields exits/monsters: %+v %v", f.Exits, f.Mon)
	}
	if c := db.Level(3); c.Waypoint != 1 || c.MonLvl != [3]int{5, 30, 55} { // no MonLvlEx: classic
		t.Errorf("cave: %+v", c)
	}
	if town := db.Level(1); !town.IsTown || town.Waypoint != 0 {
		t.Errorf("town: %+v", town)
	}
	w := db.WarpTo(2, 3)
	if w == nil || w.ID != 1 || w.SelectX != -90 || w.SelectY != -100 || w.SelectDX != 90 || w.SelectDY != 110 ||
		w.ExitWalkX != 5 || w.OffsetY != -1 || w.Direction != "l" {
		t.Errorf("fields→cave warp: %+v", w)
	}
	if db.WarpTo(2, 1) != nil {
		t.Error("an open border has no warp")
	}
	if l := db.LevelByName("test fields"); l == nil || l.ID != 2 {
		t.Errorf("LevelByName: %+v", l)
	}
}

func TestObjects(t *testing.T) {
	db := loadFixture(t)
	wantKinds := []ObjKind{ObjOther, ObjContainer, ObjWaypoint, ObjQuestContainer, ObjStash, ObjDoor, ObjPortal, ObjDoor}
	for i, k := range wantKinds {
		o := db.Object(i)
		if o == nil || o.ID != i || o.Kind != k {
			t.Errorf("object %d: %+v, want kind %s", i, o, k)
		}
	}
	p := db.ObjectByClass("TestPortal")
	if p.ID != 6 || p.Left != -40 || p.Top != -100 || p.Width != 80 || p.Height != 110 || p.SizeX != 4 || !p.Selectable[0] {
		t.Errorf("portal box: %+v", p)
	}
	if c := db.ObjectByClass("TestCubeChest"); c.ID != 3 || c.OperateFn != 39 {
		t.Errorf("cube chest after the Expansion divider: %+v", c)
	}
}

func TestSkills(t *testing.T) {
	db := loadFixture(t)
	f := db.Skill(2)
	if f.Key != "Focus" || f.Name != "Carnage" || f.Class != "bar" || f.Page != 1 || f.Row != 4 || f.Column != 2 || f.ReqLevel != 6 {
		t.Errorf("skill 2: %+v", f)
	}
	if !reflect.DeepEqual(f.ReqIDs, []int{1, -1}) {
		t.Errorf("req ids %v", f.ReqIDs)
	}
	if got := f.ManaCost(1); got != 3 { // 256<<0/256 = 1, raised to minmana 3
		t.Errorf("mana cost %v", got)
	}
	s := db.Skill(1)
	if s.Name != "Slam" { // skills.json wins over item-names.json's same key
		t.Errorf("skill 1 name %q", s.Name)
	}
	if got := s.ManaCost(3); got != 4 { // (2+1*2)<<8/256
		t.Errorf("slam mana %v", got)
	}
	if !db.Skill(3).Aura || !db.Skill(4).Passive || db.Skill(4).Name != "Test Hardiness" {
		t.Errorf("aura/passive/fallback name: %+v %+v", db.Skill(3), db.Skill(4))
	}
	var keys []string
	for _, s := range db.ClassSkills("bar") {
		keys = append(keys, s.Key)
	}
	if !reflect.DeepEqual(keys, []string{"Test Slam", "Focus", "Test Hardiness"}) {
		t.Errorf("bar tree %v", keys)
	}
}

func TestModInfoAndSummary(t *testing.T) {
	db := loadFixture(t)
	if db.Mod.String() != "Testmod 1.2.3" {
		t.Errorf("mod %q", db.Mod)
	}
	s := db.Summary()
	for _, want := range []string{"items=14", "monsters=3", "levels=4", "skills=5", "mod=Testmod 1.2.3"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q lacks %q", s, want)
		}
	}
}

// The D2R data-root layout (…/D2RMM.mpq/data/global/excel + data/local/lng/strings)
// loads the same as the flat dump.
func TestD2RLayout(t *testing.T) {
	root := t.TempDir()
	copyDir(t, filepath.Join("testdata", "flat", "excel"), filepath.Join(root, "data", "global", "excel"))
	copyDir(t, filepath.Join("testdata", "flat", "strings"), filepath.Join(root, "data", "local", "lng", "strings"))
	os.Remove(filepath.Join(root, "data", "global", "excel", "objects.txt")) // a table may be absent
	db, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Items) != 14 || db.ItemName(0) != "Practice Axe" || db.Skill(2).Name != "Carnage" {
		t.Errorf("D2R layout: items=%d name=%q", len(db.Items), db.ItemName(0))
	}
	if !reflect.DeepEqual(db.Missing, []string{"objects.txt"}) || len(db.Objects) != 0 {
		t.Errorf("missing %v objects %d", db.Missing, len(db.Objects))
	}
	// …and pointing straight at the excel directory finds the strings too.
	db2, err := LoadPaths(Paths{Excel: filepath.Join(root, "data", "global", "excel")})
	if err != nil || db2.ItemName(0) != "Practice Axe" {
		t.Errorf("excel-only paths: %v %q", err, db2.ItemName(0))
	}
}

func TestLoadFailsWithoutTables(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("an empty directory must fail")
	}
}

func TestParseTableTolerance(t *testing.T) {
	in := "\uFEFFname\tCode\t*eol\r\n\r\nA\tx\r\nExpansion\r\nB\r\n\t\t\r\nC\ty\t0"
	tb, err := parseTable("t.txt", strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	tb.each(func(i int, r row) { got = append(got, r.str("NAME")+"/"+r.str("code")+"/"+string(rune('0'+i))) })
	if want := []string{"A/x/0", "B//1", "C/y/2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("rows %v, want %v", got, want)
	}
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"ÿc8Stack: El Runeÿc9 (ÿc0#1ÿc9)\nÿc;~Pick Up~ÿc0": "Stack: El Rune (#1)",
		"Jewelÿc2*ÿc3": "Jewel*",
		"[ms]Schwert":  "Schwert",
		"plain":        "plain",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProcessInstance(t *testing.T) {
	defer Set(nil)
	Set(nil)
	if Get() != nil {
		t.Fatal("nothing loaded yet")
	}
	db := loadFixture(t)
	Set(db)
	if Get() != db {
		t.Fatal("Set/Get")
	}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
