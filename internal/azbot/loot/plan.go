package loot

import (
	"fmt"

	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
)

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
	// Owned: the unique row is already owned outside the bag (stash, worn).
	Owned func(row int) bool
}

// WeakUnique judges one unique row against his level and what he owns: weak =
// not worth a cell. Quest artifacts are never weak; jewelry, charms and jewels
// are small and often strong at any level — weak only as duplicates. A
// weapon/armor row is weak past Space.UniqueLevelGap below his level, and only
// when the table row's base code matches the item (a mismatched table can never
// condemn the wrong thing). req nil = the mod table (modUniqueReq).
func (c *Config) WeakUnique(row int, cl Class, level int, usesBow bool, owned func(int) bool,
	req func(int) (string, int, bool)) (bool, string) {
	switch cl.Kind {
	case KindQuest, KindCube:
		return false, ""
	}
	if row <= 0 {
		return false, ""
	}
	if req == nil {
		req = modUniqueReq
	}
	code, lreq, known := req(row)
	matched := known && code == cl.Code
	if owned != nil && owned(row) && (matched || !known) {
		return true, "duplicate unique (already owned)"
	}
	switch cl.Kind {
	case KindAmmo:
		if !usesBow {
			return true, "unique quiver, no bow"
		}
	case KindGear:
		if gap := c.Space.UniqueLevelGap; matched && level > 0 && gap > 0 && lreq+gap < level {
			return true, fmt.Sprintf("weak unique (req %d, level %d)", lreq, level)
		}
	}
	return false, ""
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
	// ONLY STRONG UNIQUES STAY (owner, 2026-09-25: "it needs to sell uniques
	// sometimes" and "not fill it with low level uniques or duplicates, gather
	// only strong uniques"): a copy of one he already owns (stash, worn, or an
	// earlier bag copy), an outlevelled weapon/armor piece, or a quiver with no
	// bow is sold. The first bag copy registers only once judged a keeper.
	if it.Item.Quality == QUnique && it.Identified && it.Unique > 0 && !it.Upgrade {
		if p.uniques == nil {
			p.uniques = map[int]bool{}
		}
		owned := func(row int) bool { return p.uniques[row] || (p.Owned != nil && p.Owned(row)) }
		req := p.UniqueReq
		if req == nil { // the rule is off: duplicates only
			req = func(int) (string, int, bool) { return "", 0, false }
		}
		if weak, why := p.c.WeakUnique(it.Unique, cl, p.Level, p.UsesBow, owned, req); weak {
			return DispSell, why
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
