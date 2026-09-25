// Package loot is azbot's item value model: what a ground or carried item IS
// on this modded game, what it is WORTH to her (tiers S/A/B/C), and what to do
// about it when the bag is full (take, swap, town trip, skip). Pure: no memory
// reads, no input — the activity package feeds it and acts on its plans.
package loot

import (
	"sync"

	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
)

// THE MOD'S SHIFTED TABLE (measured from the relay flights, 2026-09-24). The
// mod inserted 15 rows ahead of the vanilla misc section, so every misc row
// reads 15 past its vanilla number while d2go's static tables still carry the
// vanilla names — the "scrambled" names. Every anchor agrees:
//
//	mod 533/534 = TP/ID tome        (vanilla 518/519 tbk/ibk)
//	mod 538     = gold ("Fang")      (vanilla 523 gld; no "Gold" row ever seen)
//	mod 541/543 = arrows/bolts ("Scalp"/"Key", magic..unique qualities seen)
//	mod 535/537 = amulet/ring ("Horn"/"Flag", magic/set/rare seen)
//	mod 564     = the Horadric Cube  (vanilla 549 box; sits 2x2 at bag (2,0))
//	mod 602/603 = hp1/hp2  ("Herb"/"SmallCharm", the measured red belt potion)
//	mod 607/608 = mp1/mp2  ("INVALID607/608", the measured blue potions)
//	mod 618-620 = small/large/grand charm ("Ort/Thul/AmnRune", magic seen)
//	mod 625/626 = El/Eld rune ("Io/LumRune"; the bag shows ELD x2 at mod 626)
//
// Rows below modMiscStart are equipment (the inserted rows sit somewhere in
// the weapon/armor block; the vanilla base there is approximate but it IS
// gear). Rows past the vanilla table's end are the mod's own items.
const (
	ModShift     = 15
	modMiscStart = 508 + ModShift // vanilla Elixir, the first misc row
	vanillaLast  = 658            // d2go's last vanilla row (Standard of Heroes)
	modLastKnown = vanillaLast + ModShift
)

// The mod's bag (measured: the Fence's 10x8 grid, the R2 inventory capture).
const (
	BagW     = 10
	BagH     = 8
	BagCells = BagW * BagH
)

// Lifeline IDs (mod numbering): never sold, never dropped.
const (
	TomeTP = 533
	TomeID = 534
	Cube   = 549 + ModShift // 564
	// legacyCube is the vanilla number the Fence's old guard knew. Kept as a
	// lifeline too: on this mod it is a body-part row nobody drops, and
	// refusing to sell it is a cheap price for never selling a cube on a
	// vanilla table.
	legacyCube = 549
)

// Kind is what an item is for, once the mod's numbering is undone.
type Kind uint8

const (
	KindGear       Kind = iota // weapons and armor (bases below the misc block)
	KindGold                   // gold on the ground
	KindRune                   // r01..r33
	KindGem                    // gems and skulls
	KindJewel                  // jewels
	KindCharm                  // small/large/grand charms
	KindJewelry                // amulets and rings
	KindPotion                 // healing/mana/rejuvenation and other bottles
	KindQuest                  // quest rows: uber keys, essences, organs, tokens
	KindTome                   // TP/ID tomes
	KindCube                   // the Horadric Cube
	KindScroll                 // TP/ID scrolls
	KindAmmo                   // arrows and bolts
	KindMisc                   // keys, body parts, torches, elixirs, herbs
	KindModUnknown             // a row past the vanilla table: the mod's own item
)

var kindNames = [...]string{"gear", "gold", "rune", "gem", "jewel", "charm", "jewelry", "potion",
	"quest", "tome", "cube", "scroll", "ammo", "misc", "mod-unknown"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "?"
}

// Class is an item row with the mod's shift undone.
type Class struct {
	ID        int    // the row as the game (and memory) numbers it
	VanillaID int    // the vanilla row it corresponds to; -1 for mod rows
	Code      string // the vanilla item code ("r09", "cm1", "gld"); "" for mod rows
	Type      string // the vanilla item type code ("rune", "scha", "amul")
	Kind      Kind
	W, H      int // inventory footprint (1x1 when unknown)
}

// Classify resolves a mod row number. With the mod's own tables loaded
// (gamedata), the row's code, type and footprint come from THEM — the owner,
// 2026-09-25: the bot must know "different item sizes footprints, modded or
// otherwise" (the mod's 1x3 grand charms and quivers past the vanilla table
// read 1x1 here, and the gear rows past the insertion point wore the wrong
// vanilla base). The vanilla shift below is the fallback when no table is.
func Classify(id int) Class {
	if c, ok := classifyMod(gamedata.Get(), id); ok {
		return c
	}
	return classifyVanilla(id)
}

// classifyMod reads the mod's row. A vanilla code keeps the vanilla kind
// rules (so the tested anchors cannot drift); a mod-only code is sorted by
// its type closure, and whatever the bot has no rule for stays the mod's own
// item (orbs, grabbers, the mod's potions: tier unknown_mod_item).
func classifyMod(db *gamedata.DB, id int) (Class, bool) {
	it := db.Item(id)
	if it == nil || it.Code == "" {
		return Class{}, false
	}
	c := Class{ID: id, VanillaID: -1, Code: it.Code, Type: it.Type, W: 1, H: 1}
	if it.InvW > 0 && it.InvH > 0 {
		c.W, c.H = it.InvW, it.InvH
	}
	if vid, ok := vanillaByCode()[it.Code]; ok {
		c.VanillaID = vid
	}
	switch {
	case questGear[it.Code]:
		c.Kind = KindQuest
	case it.Source != "misc":
		c.Kind = KindGear
	case c.VanillaID >= 0:
		c.Kind = kindOfMisc(item.Desc[c.VanillaID])
	case it.Is("gem"), it.Is("gemx"):
		c.Kind = KindGem
	case it.Is("rune"), it.Is("runx"):
		c.Kind = KindRune
	case it.Is("char"):
		c.Kind = KindCharm
	case it.Is("jewl"):
		c.Kind = KindJewel
	case it.Is("misl"):
		c.Kind = KindAmmo
	case it.IsQuest():
		c.Kind = KindQuest
	default:
		c.Kind = KindModUnknown
	}
	return c, true
}

var (
	vanillaCodesOnce sync.Once
	vanillaCodes     map[string]int
)

// vanillaByCode: d2go's vanilla row per item code (lowest row wins).
func vanillaByCode() map[string]int {
	vanillaCodesOnce.Do(func() {
		vanillaCodes = make(map[string]int, len(item.Desc))
		for id, d := range item.Desc {
			if prev, dup := vanillaCodes[d.Code]; !dup || id < prev {
				vanillaCodes[d.Code] = id
			}
		}
	})
	return vanillaCodes
}

// classifyVanilla undoes the measured +15 misc shift on d2go's vanilla table.
func classifyVanilla(id int) Class {
	c := Class{ID: id, VanillaID: -1, W: 1, H: 1}
	switch {
	case id < 0:
		c.Kind = KindModUnknown
		return c
	case id > modLastKnown:
		c.Kind = KindModUnknown
		return c
	case id >= modMiscStart:
		c.VanillaID = id - ModShift
	default:
		// Equipment: the vanilla row of the same number is the best base we
		// have (approximate past the insertion point, but always gear-sized).
		c.VanillaID = id
	}
	d, ok := item.Desc[c.VanillaID]
	if !ok {
		c.Kind, c.VanillaID = KindModUnknown, -1
		return c
	}
	c.Code, c.Type = d.Code, d.Type
	if d.InventoryWidth > 0 && d.InventoryHeight > 0 {
		c.W, c.H = d.InventoryWidth, d.InventoryHeight
	}
	if questGear[d.Code] {
		c.Kind = KindQuest // quest artifacts that live in the weapon/armor tables
		return c
	}
	if id < modMiscStart {
		c.Kind = KindGear
		return c
	}
	c.Kind = kindOfMisc(d)
	return c
}

// kindOfMisc sorts a vanilla misc row by its type code (codes for the few
// rows whose type is too broad).
func kindOfMisc(d item.Description) Kind {
	switch d.Code {
	case "box":
		return KindCube
	case "tbk", "ibk":
		return KindTome
	case "tsc", "isc":
		return KindScroll
	}
	switch d.Type {
	case "gold":
		return KindGold
	case "rune":
		return KindRune
	case "gema", "gemt", "gems", "geme", "gemr", "gemd", "gemz":
		return KindGem
	case "jewl":
		return KindJewel
	case "scha", "mcha", "lcha":
		return KindCharm
	case "amul", "ring":
		return KindJewelry
	case "hpot", "mpot", "rpot", "spot", "apot", "wpot", "tpot":
		return KindPotion
	case "ques":
		return KindQuest
	case "book":
		return KindTome
	case "scro":
		return KindScroll
	case "bowq", "xboq":
		return KindAmmo
	}
	return KindMisc
}

// Cells is the item's footprint in bag cells.
func (c Class) Cells() int { return c.W * c.H }

// Lifeline: an item she must never sell or drop — the tomes, the cube, and
// every quest row (type-by-number beats the scrambled name table).
func Lifeline(id int) bool {
	if id == TomeTP || id == TomeID || id == Cube || id == legacyCube {
		return true
	}
	k := Classify(id).Kind
	return k == KindQuest || k == KindCube || k == KindTome
}

// Occupied counts the bag cells n items cover: the UNION of their footprints
// clipped to the 10x8 grid (a misread footprint can never count a cell twice
// or push the bag past full). at(i) yields item i's row and grid cell.
func Occupied(n int, at func(i int) (id, gx, gy int)) int {
	var occ [BagW][BagH]bool
	count := 0
	for i := 0; i < n; i++ {
		id, gx, gy := at(i)
		cl := Classify(id)
		for x := gx; x < gx+cl.W; x++ {
			for y := gy; y < gy+cl.H; y++ {
				if x >= 0 && y >= 0 && x < BagW && y < BagH && !occ[x][y] {
					occ[x][y] = true
					count++
				}
			}
		}
	}
	return count
}

// questGear: quest items stored in the weapons/armor tables, which would otherwise
// classify as plain gear (tier C, never picked). R14: the Staff of Kings chest was
// the goal, and "msf" read as a staff. Codes are vanilla (the gear rows are unshifted).
var questGear = map[string]bool{
	"msf": true, // Staff of Kings
	"hst": true, // Horadric Staff
	"vip": true, // Amulet of the Viper
	"g33": true, // The Gidbinn
	"qf1": true, "qf2": true, // Khalim's Flail / Will
	"leg": true, // Wirt's Leg
	"hfh": true, // Hell Forge Hammer
}

// FitsShape: a free w x h block exists in the 10x8 bag given what it carries
// (R56: "fits 7/3 cells free" — seven scattered cells, no free 1x3 column; a
// grand charm and a wrist blade were clicked ~40 times each, every click deaf).
func FitsShape(bag []Carried, w, h int) bool {
	var occ [BagW][BagH]bool
	for _, it := range bag {
		cl := Classify(it.Item.ID)
		for x := it.GX; x < it.GX+cl.W; x++ {
			for y := it.GY; y < it.GY+cl.H; y++ {
				if x >= 0 && y >= 0 && x < BagW && y < BagH {
					occ[x][y] = true
				}
			}
		}
	}
	for x := 0; x+w <= BagW; x++ {
		for y := 0; y+h <= BagH; y++ {
			free := true
			for dx := 0; dx < w && free; dx++ {
				for dy := 0; dy < h; dy++ {
					if occ[x+dx][y+dy] {
						free = false
						break
					}
				}
			}
			if free {
				return true
			}
		}
	}
	return false
}
