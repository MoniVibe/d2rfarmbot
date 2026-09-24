package loot

import "github.com/hectorgimenez/koolo/internal/azbot/gamedata"

// ---------------------------------------------------------------- the bag plan
//
// OWNER (2026-09-24, R27): "it doesn't clear its inventory ... it sells a few
// potions and that's it — we need to rethink things". Eight activities each held
// their own idea of what a bag item is. This is the ONE answer: every carried item
// gets exactly one disposition, and percept (the sell list), Stash (the stash
// list), the fence and the full-bag trip home all read it.

// Disposition is what the town routine does with a carried item.
type Disposition uint8

const (
	DispSell     Disposition = iota // the vendor takes it
	DispKeepBag                     // stays in the bag: tomes, cube, quest items, working charms, potions
	DispStash                       // the stash takes it: uniques, runes, gems, the mod's rows, surplus charms
	DispWear                        // an upgrade on the equip docket
	DispIdentify                    // unidentified magic+: Cain first, then it is judged again
)

func (d Disposition) String() string {
	return [...]string{"sell", "keep", "stash", "wear", "identify"}[d]
}

// CharmBagCells: charm cells the bag keeps working; charms past it are stashed.
const CharmBagCells = 12

// OutlevelGap: a unique whose level requirement sits this far below his level sells.
const OutlevelGap = 12

// TightFree: below this many free cells every charm may go to the stash.
const TightFree = 10

// Planner walks the bag in order; the charm budget is spent first-come.
type Planner struct {
	c          *Config
	tight      bool
	charmCells int
	// Level is the character's level (outlevelled uniques are sold); 0 = unknown.
	Level int
	// UsesBow: he carries a bow or crossbow (arrows/bolts are then keepers).
	UsesBow bool
	uniques map[int]bool // unique rows already kept this plan (a second copy sells)
	// UniqueReq resolves a unique row to (its base code, its level requirement);
	// ok=false when unknown. Wired to gamedata by the caller; nil = rule off.
	UniqueReq func(row int) (code string, req int, ok bool)
}

// NewPlanner starts a plan for one bag reading.
func (c *Config) NewPlanner(invFree int) *Planner {
	return &Planner{c: c, tight: invFree < TightFree, UniqueReq: modUniqueReq}
}

// modUniqueReq reads the mod's uniqueitems.txt row (nil table: unknown).
func modUniqueReq(row int) (string, int, bool) {
	db := gamedata.Get()
	if db == nil || row < 0 || row >= len(db.Uniques) || db.Uniques[row] == nil {
		return "", 0, false
	}
	u := db.Uniques[row]
	return u.Code, u.LevelReq, true
}

// Dispose is one carried item's disposition and the reason, in bag order.
func (p *Planner) Dispose(it Carried) (Disposition, string) {
	if p == nil || p.c == nil {
		return DispKeepBag, "no policy"
	}
	if it.Upgrade {
		return DispWear, "upgrade"
	}
	if Lifeline(it.Item.ID) {
		return DispKeepBag, "lifeline"
	}
	cl := Classify(it.Item.ID)
	switch cl.Kind {
	case KindQuest, KindCube, KindTome:
		return DispKeepBag, cl.Kind.String()
	case KindPotion, KindScroll:
		return DispKeepBag, "fuel (percept keeps the reserve)"
	}
	if it.Item.Quality >= QMagic && !it.Identified && cl.Kind != KindCharm {
		return DispIdentify, "unidentified"
	}
	if cl.Kind == KindCharm {
		cells := cl.W * cl.H
		if !p.tight && p.charmCells+cells <= CharmBagCells {
			p.charmCells += cells
			return DispKeepBag, "charm working in the bag"
		}
		return DispStash, "charm past the bag budget"
	}
	// UNIQUES SELL SOMETIMES (owner, 2026-09-25: "it needs to sell uniques
	// sometimes, it gets filled up"): a second copy of a unique already kept,
	// or one outlevelled by more than OutlevelGap, is sold. Charms and jewelry
	// keep their own rules. The table row must match the item's base code, so a
	// mismatched table can never sell the wrong thing.
	if it.Item.Quality == QUnique && it.Identified && it.Unique > 0 && cl.Kind == KindGear && !it.Upgrade {
		if p.uniques == nil {
			p.uniques = map[int]bool{}
		}
		if p.uniques[it.Unique] {
			return DispSell, "duplicate unique"
		}
		if p.UniqueReq != nil && p.Level > 0 {
			if code, req, ok := p.UniqueReq(it.Unique); ok && code == cl.Code && req+OutlevelGap < p.Level {
				return DispSell, "outlevelled unique"
			}
		}
		p.uniques[it.Unique] = true
	}
	// Unique arrows/bolts for a character with no bow (R38: two unique quivers
	// rode a barbarian's bag): merchandise.
	if cl.Kind == KindAmmo && !p.UsesBow {
		return DispSell, "ammo, no bow"
	}
	v, pinned := p.c.EvaluateCarried(it)
	if pinned || v.Tier >= TierA {
		return DispStash, "keeper (" + v.Why + ")"
	}
	return DispSell, "tier " + v.Tier.String()
}
