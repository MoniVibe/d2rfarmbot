package gamedata

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestRealModData runs against the real tables when AZBOT_MODDATA points at
// them (a flat dump or a D2R data root); skipped otherwise — the data is the
// mod author's and Blizzard's and never lives in this repo. Run with -v to
// read the validation report.
func TestRealModData(t *testing.T) {
	dir := os.Getenv("AZBOT_MODDATA")
	if dir == "" {
		t.Skip("AZBOT_MODDATA not set")
	}
	db, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("gamedata loaded: %s", db.Summary())
	check := func(what string, ok bool, got string) {
		t.Helper()
		if ok {
			t.Logf("PASS %-44s %s", what, got)
		} else {
			t.Errorf("FAIL %-44s %s", what, got)
		}
	}

	// Numbering: the counted index against the tables' own id comments. Where
	// the comment is maintained (monstats, skills, setitems) they must agree;
	// objects' and uniqueitems' comments drift at the tail (new rows inserted
	// without renumbering), so there the d2go/koolo anchors decide instead.
	idCol := []struct {
		file, col string
		strict    bool
	}{{"monstats.txt", "*hcIdx", true}, {"skills.txt", "*Id", true}, {"setitems.txt", "*ID", true},
		{"objects.txt", "*ID", false}, {"uniqueitems.txt", "*ID", false}}
	for _, c := range idCol {
		tb, err := openTable(db.ExcelDir, c.file)
		if err != nil {
			t.Fatal(err)
		}
		bad, first := 0, ""
		tb.each(func(idx int, r row) {
			if n, ok := r.intOK(c.col); ok && n != idx {
				if bad == 0 {
					first = fmt.Sprintf(" (first: counted %d %q says %d)", idx, r.str(tb.firstCol()), n)
				}
				bad++
			}
		})
		msg := fmt.Sprintf("%d disagree%s", bad, first)
		if c.strict {
			check("counted ids == "+c.file+" "+c.col, bad == 0, msg)
		} else {
			t.Logf("INFO %s %s comment drift: %s", c.file, c.col, msg)
		}
	}
	// Anchors from d2go's vanilla tables (and koolo's manual GoodChest=580).
	for _, c := range []struct {
		class string
		id    int
	}{{"HoradricCubeChest", 354}, {"PlaceUniqueChest", 580}, {"PlaceRandomTreasureChest", 581}} {
		o := db.ObjectByClass(c.class)
		check(fmt.Sprintf("object %s = %d", c.class, c.id), o != nil && o.ID == c.id, fmt.Sprintf("%d", o.ID))
	}
	for _, c := range []struct {
		key string
		id  int
	}{{"Coldkill", 129}, {"Stealskull", 203}, {"Hellfire Torch", 400}, {"Black Cleft", 406}} {
		var got *UniqueItem
		for _, u := range db.Uniques {
			if u.Key == c.key {
				got = u
				break
			}
		}
		check(fmt.Sprintf("unique %s = %d", c.key, c.id), got != nil && got.ID == c.id, fmt.Sprintf("%+v", got))
	}

	// Skills.
	for _, c := range []struct {
		id   int
		name string
	}{{144, "Carnage"}, {143, "Leap Attack"}, {133, "Double Swing"}, {147, "Frenzy"}} {
		s := db.Skill(c.id)
		check(fmt.Sprintf("skill %d", c.id), s != nil && s.Name == c.name,
			fmt.Sprintf("%q (key %q, tree %d/%d/%d, reqlvl %d)", s.Name, s.Key, s.Page, s.Row, s.Column, s.ReqLevel))
	}
	var tree []string
	for _, s := range db.ClassSkills("bar") {
		tree = append(tree, fmt.Sprintf("%d:%s@%d/%d/%d", s.ID, s.Name, s.Page, s.Row, s.Column))
	}
	t.Logf("barbarian tree (id:name@page/row/col): %s", strings.Join(tree, " "))
	classes := map[string]int{}
	for _, s := range db.Skills {
		if s.Page > 0 {
			classes[s.Class]++
		}
	}
	t.Logf("tree skills per class: %v", classes)

	// Items: the misc anchors (+15 vs vanilla d2go).
	for _, c := range []struct {
		id   int
		code string
	}{{196, "7ha"}, {523, "elx"}, {533, "tbk"}, {534, "ibk"}, {538, "gld"}, {564, "box"}, {602, "hp1"},
		{603, "hp2"}, {607, "mp1"}, {608, "mp2"}, {618, "cm1"}, {625, "r01"}, {626, "r02"}, {673, "std"}} {
		it := db.Item(c.id)
		check(fmt.Sprintf("item %d = %s", c.id, c.code), it != nil && it.Code == c.code,
			fmt.Sprintf("%s %q [%s %s] %dx%d", it.Code, it.Name, it.TxtName, it.Category, it.InvW, it.InvH))
	}
	src := map[string][2]int{}
	for _, it := range db.Items {
		r, ok := src[it.Source]
		if !ok {
			r = [2]int{it.ID, it.ID}
		}
		r[1] = it.ID
		src[it.Source] = r
	}
	t.Logf("item id ranges: weapons %v armor %v misc %v", src["weapons"], src["armor"], src["misc"])
	for _, it := range db.Items {
		if it.Source == "armor" && it.ID > 507 {
			t.Logf("  armor past vanilla: %d %s %q", it.ID, it.Code, it.Name)
		}
	}
	for _, it := range db.Items {
		if it.ID > 673 {
			t.Logf("  misc past vanilla: %d %s %q (%s, %s)", it.ID, it.Code, it.Name, it.TxtName, it.Type)
		}
	}

	// Levels: Act 2.
	for _, id := range db.LevelIDs() {
		l := db.Level(id)
		if l.Act != 1 {
			continue
		}
		var ex []string
		for _, e := range l.Exits {
			if w := db.Warp(e.Warp); w != nil {
				ex = append(ex, fmt.Sprintf("→%d via warp %d %q sel(%d,%d %dx%d) walk(%d,%d) off(%d,%d) %s",
					e.Level, w.ID, w.Name, w.SelectX, w.SelectY, w.SelectDX, w.SelectDY, w.ExitWalkX, w.ExitWalkY, w.OffsetX, w.OffsetY, w.Direction))
			}
		}
		t.Logf("level %d %-26q town=%v wp=%d mlvl=%v classic=%v %s", id, l.Name, l.IsTown, l.Waypoint, l.MonLvl, l.MonLvlClassic, strings.Join(ex, "; "))
	}
	for _, c := range []struct {
		id   int
		name string
	}{{43, "Far Oasis"}, {44, "Lost City"}, {45, "Valley of Snakes"}, {62, "Maggot Lair Level 1"}, {64, "Maggot Lair Level 3"}} {
		l := db.Level(c.id)
		check(fmt.Sprintf("level %d = %s", c.id, c.name), l != nil && l.Name == c.name, fmt.Sprintf("%q normal mlvl %d", l.Name, l.MonLvl[Normal]))
	}

	// Objects.
	for _, class := range []string{"HoradricCubeChest", "HoradricScrollChest", "StaffOfKingsChest", "TaintedSunShrine",
		"SevenTombsReceptacle", "WaypointAct2", "Bank", "TownPortal", "DurielPortal"} {
		o := db.ObjectByClass(class)
		check("object "+class, o != nil, fmt.Sprintf("%+v", o))
	}

	// Monsters.
	for _, code := range []string{"andariel", "duriel", "radament", "nihlathakboss", "sandmaggot1", "clawviper3"} {
		m := db.MonsterByCode(code)
		check("monster "+code, m != nil, fmt.Sprintf("id=%d %q lvl=%v boss=%v immune(N)=%v size=%+v", m.ID, m.Name, m.Level, m.Boss, m.Immunities(Normal), m.Size))
	}
	var su []string
	for _, s := range db.SuperUniques {
		su = append(su, fmt.Sprintf("%d:%s(%s)", s.ID, s.Name, s.Class))
	}
	t.Logf("superuniques: %s", strings.Join(su, " "))
	t.Logf("strings: files=%v dups=%d", db.Strings.Files, db.Strings.Dups)
}
