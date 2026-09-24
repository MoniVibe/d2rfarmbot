package gamedata

import "strings"

// ItemType is one itemtypes.txt row. Equiv1/Equiv2 are its parents in the
// type DAG ("hpot" → "poti" → "misc").
type ItemType struct {
	Name     string
	Code     string
	Equiv1   string
	Equiv2   string
	Body     bool
	BodyLoc1 string
	BodyLoc2 string
	Beltable bool
	// Ancestors is the flattened closure: the code itself, its parents, their
	// parents... (each once, breadth-first).
	Ancestors []string
}

// Category is what an item is for, from its flattened type (first match in
// the order below wins).
type Category string

const (
	CatGold   Category = "gold"
	CatRune   Category = "rune"
	CatGem    Category = "gem"
	CatJewel  Category = "jewel"
	CatCharm  Category = "charm"
	CatQuest  Category = "quest"
	CatPotion Category = "potion"
	CatScroll Category = "scroll"
	CatTome   Category = "tome"
	CatKey    Category = "key"
	CatStack  Category = "stack" // the mod's gem/rune/orb stacks (gemx/runx/orbx)
	CatAmmo   Category = "ammo"
	CatWeapon Category = "weapon"
	CatArmor  Category = "armor"
	CatMisc   Category = "misc"
)

// Item is one weapons/armor/misc row. ID is the item class id memory reports
// (txtFileNo): weapons, then armor, then misc, counted as the game counts them.
type Item struct {
	ID      int
	Code    string
	TxtName string // the table's name column (a designer label, not shown in game)
	NameKey string // namestr: the string-table key
	Name    string // the in-game English name, cleaned (see Clean); TxtName when unresolved
	Source  string // "weapons", "armor" or "misc"

	Type, Type2 string
	Types       []string // flattened type closure of Type and Type2
	Category    Category

	InvW, InvH int
	Level      int // quality level (qlvl)
	LevelReq   int
	ReqStr     int
	ReqDex     int

	MinDam, MaxDam       int // one-hand (or the only) damage
	TwoHandMinDam        int
	TwoHandMaxDam        int
	MinMisDam, MaxMisDam int // thrown damage
	MinDef, MaxDef       int // armor class (minac/maxac)
	Block                int
	Speed                int
	RangeAdder           int
	TwoHanded            bool
	Durability           int
	GemSockets           int
	NormCode, UberCode   string
	UltraCode            string
	Quest                bool // the quest column is set (or the type is "ques")
	Stackable            bool
	MinStack, MaxStack   int
	Spawnable            bool
	Useable              bool
	Beltable             bool // some type in its closure is beltable (itemtypes Beltable)
	typeSet              map[string]bool
}

// Is reports whether the item's flattened type contains code ("rune", "poti", "weap").
func (it *Item) Is(code string) bool { return it != nil && it.typeSet[code] }

func (it *Item) IsGold() bool   { return it.Is("gold") }
func (it *Item) IsRune() bool   { return it.Is("rune") }
func (it *Item) IsGem() bool    { return it.Is("gem") }
func (it *Item) IsJewel() bool  { return it.Is("jewl") }
func (it *Item) IsCharm() bool  { return it.Is("char") }
func (it *Item) IsPotion() bool { return it.Is("poti") }
func (it *Item) IsScroll() bool { return it.Is("scro") }
func (it *Item) IsTome() bool   { return it.Is("book") }
func (it *Item) IsQuest() bool  { return it != nil && (it.Quest || it.Is("ques")) }

// UniqueItem is one uniqueitems.txt row (ID = the unique's row id memory reports).
type UniqueItem struct {
	ID       int
	Key      string // index column (also the string key)
	Name     string
	Code     string // base item code
	Level    int    // qlvl
	LevelReq int
	Enabled  bool // not disabled
	Spawn    bool
}

// SetItem is one setitems.txt row.
type SetItem struct {
	ID       int
	Key      string
	Name     string
	Set      string
	Code     string
	Level    int
	LevelReq int
}

func (db *DB) loadItemTypes() {
	t := db.table("itemtypes.txt")
	if t == nil {
		return
	}
	t.each(func(_ int, r row) {
		code := r.str("Code")
		if code == "" {
			return
		}
		if _, dup := db.Types[code]; dup {
			return // the mod repeats "helm"; the first row wins
		}
		db.Types[code] = &ItemType{Name: r.str("ItemType"), Code: code, Equiv1: r.str("Equiv1"),
			Equiv2: r.str("Equiv2"), Body: r.bool("Body"), BodyLoc1: r.str("BodyLoc1"),
			BodyLoc2: r.str("BodyLoc2"), Beltable: r.bool("Beltable")}
	})
	for _, ty := range db.Types {
		ty.Ancestors = db.flatten(ty.Code)
	}
}

// flatten is the type closure of codes, breadth-first, each code once. Codes
// missing from itemtypes still appear (they are the item's own claim).
func (db *DB) flatten(codes ...string) []string {
	var out []string
	seen := map[string]bool{}
	q := append([]string(nil), codes...)
	for len(q) > 0 {
		c := q[0]
		q = q[1:]
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if ty := db.Types[c]; ty != nil {
			q = append(q, ty.Equiv1, ty.Equiv2)
		}
	}
	return out
}

func (db *DB) loadItems() {
	for _, src := range []string{"weapons", "armor", "misc"} {
		t := db.table(src + ".txt")
		if t == nil {
			continue
		}
		t.each(func(_ int, r row) {
			it := &Item{ID: len(db.Items), Code: r.str("code"), TxtName: r.str("name"),
				NameKey: r.str("namestr"), Source: src, Type: r.str("type"), Type2: r.str("type2"),
				InvW: r.int("invwidth"), InvH: r.int("invheight"), Level: r.int("level"),
				LevelReq: r.int("levelreq"), ReqStr: r.int("reqstr"), ReqDex: r.int("reqdex"),
				MinDam: r.int("mindam"), MaxDam: r.int("maxdam"),
				TwoHandMinDam: r.int("2handmindam"), TwoHandMaxDam: r.int("2handmaxdam"),
				MinMisDam: r.int("minmisdam"), MaxMisDam: r.int("maxmisdam"),
				MinDef: r.int("minac"), MaxDef: r.int("maxac"), Block: r.int("block"),
				Speed: r.int("speed"), RangeAdder: r.int("rangeadder"), TwoHanded: r.bool("2handed"),
				Durability: r.int("durability"), GemSockets: r.int("gemsockets"),
				NormCode: r.str("normcode"), UberCode: r.str("ubercode"), UltraCode: r.str("ultracode"),
				Quest: r.bool("quest"), Stackable: r.bool("stackable"), MinStack: r.int("minstack"),
				MaxStack: r.int("maxstack"), Spawnable: r.bool("spawnable"), Useable: r.bool("useable")}
			if it.NameKey == "" {
				it.NameKey = it.Code
			}
			it.Name = db.Strings.Name(it.NameKey, it.TxtName)
			it.Types = db.flatten(it.Type, it.Type2)
			it.typeSet = make(map[string]bool, len(it.Types))
			for _, c := range it.Types {
				it.typeSet[c] = true
				if ty := db.Types[c]; ty != nil && ty.Beltable {
					it.Beltable = true
				}
			}
			it.Category = categorize(it)
			db.Items = append(db.Items, it)
			if _, dup := db.itemByCode[it.Code]; !dup && it.Code != "" {
				db.itemByCode[it.Code] = it
			}
		})
	}
}

func categorize(it *Item) Category {
	switch {
	case it.Is("gold"):
		return CatGold
	case it.Is("rune"):
		return CatRune
	case it.Is("gem"):
		return CatGem
	case it.Is("jewl"):
		return CatJewel
	case it.Is("char"):
		return CatCharm
	case it.IsQuest():
		return CatQuest
	case it.Is("poti"):
		return CatPotion
	case it.Is("scro"):
		return CatScroll
	case it.Is("book"):
		return CatTome
	case it.Is("key"):
		return CatKey
	case it.Is("gemx"), it.Is("runx"), it.Is("orbx"):
		return CatStack
	case it.Is("misl"):
		return CatAmmo
	case it.Is("weap"):
		return CatWeapon
	case it.Is("armo"):
		return CatArmor
	}
	return CatMisc
}

func (db *DB) loadUniques() {
	if t := db.table("uniqueitems.txt"); t != nil {
		t.each(func(idx int, r row) {
			k := r.str("index")
			db.Uniques = append(db.Uniques, &UniqueItem{ID: idx, Key: k, Name: db.Strings.Name(k, k),
				Code: r.str("code"), Level: r.int("lvl"), LevelReq: r.int("lvl req"),
				Enabled: !r.bool("disabled"), Spawn: r.bool("spawnable")})
		})
	}
	if t := db.table("setitems.txt"); t != nil {
		t.each(func(idx int, r row) {
			k := r.str("index")
			db.SetItems = append(db.SetItems, &SetItem{ID: idx, Key: k, Name: db.Strings.Name(k, k),
				Set: r.str("set"), Code: r.str("item"), Level: r.int("lvl"), LevelReq: r.int("lvl req")})
		})
	}
}

// Item is the row for an item class id (nil when out of range or not loaded).
func (db *DB) Item(id int) *Item {
	if db == nil || id < 0 || id >= len(db.Items) {
		return nil
	}
	return db.Items[id]
}

// ItemByCode is the first row with that code.
func (db *DB) ItemByCode(code string) *Item {
	if db == nil {
		return nil
	}
	return db.itemByCode[strings.TrimSpace(code)]
}

// ItemName is the in-game name for an item class id ("" when unknown).
func (db *DB) ItemName(id int) string {
	if it := db.Item(id); it != nil {
		return it.Name
	}
	return ""
}
