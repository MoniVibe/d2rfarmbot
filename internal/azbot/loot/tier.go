package loot

import (
	"fmt"
	"strings"
)

// Item qualities as memory reports them.
const (
	QLow      = 1
	QNormal   = 2
	QSuperior = 3
	QMagic    = 4
	QSet      = 5
	QRare     = 6
	QUnique   = 7
	QCrafted  = 8
)

// QualityName is the short log spelling of a quality.
func QualityName(q int) string {
	switch q {
	case QLow:
		return "low"
	case QNormal:
		return "normal"
	case QSuperior:
		return "superior"
	case QMagic:
		return "magic"
	case QSet:
		return "set"
	case QRare:
		return "rare"
	case QUnique:
		return "unique"
	case QCrafted:
		return "crafted"
	}
	return fmt.Sprintf("q%d", q)
}

// Item is what perception knows about one item.
type Item struct {
	ID      int    // the row number (mod numbering)
	Name    string // d2go's table name — scrambled for the mod's shifted rows
	Quality int
	// Potion: "health"/"mana" when perception's bottle classifier recognizes
	// it (the measured belt IDs). Anything else is left to the class.
	Potion string
}

// Verdict is an item's valuation.
type Verdict struct {
	Tier  Tier
	Value float64 // 0..1; orders items inside and across tiers
	Why   string
	Class Class
}

// tierBase anchors each tier's value band; quality nudges inside the band.
var tierBase = [...]float64{TierC: 0.10, TierB: 0.35, TierA: 0.65, TierS: 0.88}

func verdict(t Tier, q int, why string, c Class) Verdict {
	v := tierBase[t]
	if q > 0 && q <= QCrafted {
		v += float64(q) * 0.005 // unique > rare > magic inside one tier
	}
	return Verdict{Tier: t, Value: v, Why: why, Class: c}
}

// Evaluate values a ground item. Explicit tags win (id, then code, then name);
// then uniques and sets; then the item's class.
func (c *Config) Evaluate(it Item) Verdict {
	cl := Classify(it.ID)
	if t, ok := c.IDs[it.ID]; ok {
		return verdict(t, it.Quality, fmt.Sprintf("config id %d", it.ID), cl)
	}
	if cl.Code != "" {
		if t, ok := c.Codes[cl.Code]; ok {
			return verdict(t, it.Quality, "config code "+cl.Code, cl)
		}
	}
	if len(c.Names) > 0 && it.Name != "" {
		n := strings.ToLower(it.Name)
		for _, r := range c.Names {
			if strings.Contains(n, r.Sub) {
				return verdict(r.Tier, it.Quality, "config name ~"+r.Sub, cl)
			}
		}
	}
	d := &c.Defaults
	// A bottle perception recognizes by its measured ID is a bottle, whatever
	// the vanilla table says that row is (the belt's 603 "SmallCharm").
	if (it.Potion == "health" || it.Potion == "mana") && it.Quality <= QSuperior && cl.Kind != KindModUnknown {
		cl.Kind = KindPotion
	}
	switch it.Quality {
	case QUnique:
		return verdict(d.Unique, it.Quality, "unique", cl)
	case QSet:
		if cl.Kind == KindGear && !JudgedGearType(cl.Type) {
			return verdict(TierC, it.Quality, "set "+cl.Type+": a slot the gear score does not judge", cl)
		}
		return verdict(d.Set, it.Quality, "set", cl)
	}
	contradiction := func(what string) Verdict {
		return verdict(d.QualityContradiction, it.Quality,
			fmt.Sprintf("%s row %d shows quality %s — the table is wrong about it", what, it.ID, QualityName(it.Quality)), cl)
	}
	switch cl.Kind {
	case KindModUnknown:
		return verdict(d.UnknownModItem, it.Quality, fmt.Sprintf("unknown mod row %d (tag it in config/loot.yaml)", it.ID), cl)
	case KindRune, KindGem, KindGold, KindPotion, KindScroll, KindTome, KindCube:
		// These rows only ever come plain. Anything magic or better means the
		// row is not what the table says — the mod's, until tagged.
		if it.Quality >= QMagic {
			return contradiction(cl.Kind.String())
		}
	}
	switch cl.Kind {
	case KindRune:
		return verdict(d.Rune, it.Quality, "rune "+cl.Code, cl)
	case KindGem:
		return verdict(d.Gem, it.Quality, "gem "+cl.Code, cl)
	case KindJewel:
		return verdict(d.Jewel, it.Quality, "jewel", cl)
	case KindCharm:
		return verdict(d.Charm, it.Quality, "charm "+cl.Code, cl)
	case KindQuest:
		return verdict(d.Quest, it.Quality, "quest row "+cl.Code, cl)
	case KindCube:
		return verdict(TierS, it.Quality, "the Horadric Cube", cl)
	case KindGold:
		return verdict(d.Gold, it.Quality, "gold", cl)
	case KindPotion:
		return verdict(d.Potion, it.Quality, "potion", cl)
	case KindScroll, KindTome:
		return verdict(d.Scroll, it.Quality, cl.Kind.String(), cl)
	case KindJewelry:
		switch it.Quality {
		case QRare:
			return verdict(d.Rare, it.Quality, "rare "+cl.Type, cl)
		case QCrafted:
			return verdict(d.Crafted, it.Quality, "crafted "+cl.Type, cl)
		case QMagic:
			return verdict(d.MagicJewelry, it.Quality, "magic "+cl.Type, cl)
		}
		return contradiction(cl.Type) // plain rings and amulets do not exist
	case KindAmmo:
		switch it.Quality {
		case QRare:
			return verdict(d.Rare, it.Quality, "rare ammo", cl)
		case QCrafted:
			return verdict(d.Crafted, it.Quality, "crafted ammo", cl)
		}
		return verdict(d.Ammo, it.Quality, "ammo", cl)
	case KindMisc:
		if it.Quality >= QMagic {
			return contradiction("misc")
		}
		return verdict(d.Misc, it.Quality, "misc "+cl.Code, cl)
	}
	// Gear. Rare and crafted pieces are picked only for the slots the gear
	// score judges (armor, helms, gloves, boots); weapons and shields are the
	// owner's call — picking them would only fill the bag with merchandise.
	if (it.Quality == QRare || it.Quality == QCrafted) && !JudgedGearType(cl.Type) {
		return verdict(TierC, it.Quality, QualityName(it.Quality)+" "+cl.Type+": a slot the gear score does not judge", cl)
	}
	switch it.Quality {
	case QRare:
		return verdict(d.Rare, it.Quality, "rare gear", cl)
	case QCrafted:
		return verdict(d.Crafted, it.Quality, "crafted gear", cl)
	case QMagic:
		return verdict(d.MagicGear, it.Quality, "magic gear", cl)
	}
	return verdict(d.PlainGear, it.Quality, QualityName(it.Quality)+" gear", cl)
}

// Carried describes one item in her bag.
type Carried struct {
	Unique     int    // unique row (uniqueitems.txt) for a unique, else -1 (0 = unknown)
	Unit       uint32 // the unit ID (the drop verb's identity check)
	Item       Item
	GX, GY     int
	Identified bool
	Upgrade    bool // perception's Equip docket: it beats what she wears
}

// EvaluateCarried values an item she already carries. Lifelines and upgrades
// are pinned (never dropped, never sold); identified magic/rare gear that is
// not an upgrade has shown its hand — it is merchandise (tier B), while an
// unidentified one keeps its ground value (potential).
func (c *Config) EvaluateCarried(it Carried) (v Verdict, pinned bool) {
	v = c.Evaluate(it.Item)
	switch {
	case Lifeline(it.Item.ID):
		v.Why, v.Tier = "lifeline", TierS
		return v, true
	case it.Upgrade:
		v.Why = "upgrade (equip docket)"
		return v, true
	case v.Class.Kind == KindCharm && v.Tier >= TierA:
		v.Why = "charm working in the bag"
		return v, true
	}
	_, tagged := c.IDs[it.Item.ID]
	if it.Identified && !tagged && (v.Class.Kind == KindGear || v.Class.Kind == KindJewelry) && v.Tier > TierB &&
		(it.Item.Quality == QRare || it.Item.Quality == QMagic || it.Item.Quality == QCrafted || it.Item.Quality == QSet) {
		// Explicit id tags are respected above; a default-tier rare that turned
		// out useless is sell-grade.
		b := verdict(TierB, it.Item.Quality, "identified, not an upgrade", v.Class)
		return b, false
	}
	return v, false
}

// JudgedGearType: item types the gear score (percept.GearSlot) judges.
func JudgedGearType(typ string) bool {
	switch typ {
	case "helm", "circ", "phlm", "pelt", "tors", "glov", "boot", "amul", "ring":
		return true
	}
	return false
}
