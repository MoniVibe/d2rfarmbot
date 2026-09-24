// Package loot is azbot's item value model: what a ground or carried item IS
// on this modded game, what it is WORTH to her (tiers S/A/B/C), and what to do
// about it when the bag is full (take, swap, town trip, skip). Pure: no memory
// reads, no input — the activity package feeds it and acts on its plans.
package loot

import (
	"github.com/hectorgimenez/d2go/pkg/data/item"
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

// Classify resolves a mod row number.
func Classify(id int) Class {
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
