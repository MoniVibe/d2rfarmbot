package loot

import "fmt"

// Action is what to do about one ground item.
type Action uint8

const (
	Skip Action = iota // leave it
	Take               // walk over and pick it up: it fits
	Swap               // drop Plan.Drop first, then pick it up
	Haul               // town trip: sell junk at the Fence, portal back, pick it up
)

func (a Action) String() string {
	switch a {
	case Take:
		return "take"
	case Swap:
		return "swap"
	case Haul:
		return "haul"
	}
	return "skip"
}

// Situation is her side of the decision.
type Situation struct {
	Free      int  // free bag cells
	BeltFree  int  // free belt slots
	Urgent    bool // the march is lawful and bidding: she is trying to progress
	PotionsOn bool // bottle looting is enabled (activity.LootPotions)
	HealPots  int  // healing/rejuv bottles in the belt (mana waits until they are stocked)
	HaulOK    bool // a town trip is possible now (portal binding or a live door; calm field)
	SellCells int  // bag cells a Fence visit would free (perception's junk list)
	Bag       []Carried
}

// Plan is the decision for one ground item.
type Plan struct {
	Act     Action
	Verdict Verdict
	Need    int      // bag cells the item needs (0: gold, belt bottles)
	Drop    *Carried // Swap: the carried item to drop first
	DropV   Verdict  // Swap: its valuation
	Why     string
}

// Plan decides one ground item against her situation.
//
//   - C never; B only when the policy says so and the bag is roomy.
//   - A: taken when it fits; when it does not, it may displace a carried item
//     it beats — but never while the march is urgent (the owner: a full bag
//     that is "trying to progress" keeps moving for anything less than S).
//   - S always wins: taken when it fits, else a worse carried item is
//     dropped for it, else a town trip frees room (only if the Fence would
//     free enough), else it is left with the reason logged.
func (c *Config) Plan(it Item, sit Situation) Plan {
	v := c.Evaluate(it)
	p := Plan{Verdict: v, Need: v.Class.Cells()}
	switch v.Class.Kind {
	case KindGold:
		p.Need = 0
	case KindPotion:
		// Bottles ride the belt: the belt, not the bag, is their room.
		if !sit.PotionsOn && v.Tier < TierS {
			p.Why = "potion looting gated off"
			return p
		}
		// THE BELT IS FOR HEALING FIRST (2026-09-26, the fourth death: she drank
		// her reds, picked the floor's blues, and D2 put them in the emptied
		// red columns — "no health lane, belt 8/8" while reds dropped around
		// her and were skipped as "belt full"). Healing rides the belt or, full,
		// the bag (the reserve refills the belt); mana only once healing is
		// stocked, so it never takes a column healing needs.
		if it.Potion == "mana" && sit.HealPots < 4 {
			p.Why = "mana waits: healing is not stocked (belt is for reds first)"
			return p
		}
		if sit.BeltFree <= 0 {
			if it.Potion != "health" || sit.Free <= 0 {
				p.Why = "belt full"
				return p
			}
			p.Need = 1 // a red into the bag: the belt refills from it
			break
		}
		p.Need = 0
	}
	// A unique charm can be carried once per character: the game refuses a
	// second of the same kind, and a refused pickup would be retried forever.
	if v.Class.Kind == KindCharm && it.Quality == QUnique {
		for _, c := range sit.Bag {
			if c.Item.ID == it.ID && c.Item.Quality == QUnique {
				p.Why = "unique charm of this kind already carried (one per character)"
				return p
			}
		}
	}
	switch v.Tier {
	case TierC:
		p.Why = "junk (tier C)"
		return p
	case TierB:
		if !c.Space.PickTierB {
			p.Why = "sell-grade (tier B): not worth the click"
			return p
		}
		if sit.Urgent {
			p.Why = "sell-grade (tier B) while the march is urgent"
			return p
		}
		if sit.Free-p.Need < BagCells/2 {
			p.Why = "sell-grade (tier B): bag not roomy"
			return p
		}
		p.Act, p.Why = Take, "sell-grade (tier B) into a roomy bag"
		return p
	}
	// The SHAPE must fit, not only the count (R56: seven scattered cells read
	// as room for a 1x3; ~120 deaf pickups). With the bag's layout known, a
	// free W x H block is the test; without it (Bag empty), the count.
	shapeOK := len(sit.Bag) == 0 || p.Need == 0 || FitsShape(sit.Bag, v.Class.W, v.Class.H)
	if p.Need <= sit.Free && shapeOK {
		p.Act, p.Why = Take, fmt.Sprintf("tier %s fits (%d/%d cells free)", v.Tier, sit.Free, p.Need)
		return p
	}
	full := fmt.Sprintf("bag full (%d free, needs %d)", sit.Free, p.Need)
	if !shapeOK {
		full = fmt.Sprintf("no free %dx%d block (%d cells free, scattered)", v.Class.W, v.Class.H, sit.Free)
	}
	if v.Tier == TierA {
		if sit.Urgent {
			p.Why = full + "; march urgent: tier A waits for no one"
			return p
		}
		if !c.Space.SwapForA {
			p.Why = full + "; swaps for tier A are off"
			return p
		}
		if d, dv, ok := c.worstDroppable(v, p.Need, sit); ok {
			p.Act, p.Drop, p.DropV = Swap, d, dv
			p.Why = fmt.Sprintf("%s; beats carried %d (%s, tier %s)", full, d.Item.ID, dv.Why, dv.Tier)
			return p
		}
		p.Why = full + "; nothing carried is worse"
		return p
	}
	// Tier S.
	if c.Space.SwapForS {
		if d, dv, ok := c.worstDroppable(v, p.Need, sit); ok {
			p.Act, p.Drop, p.DropV = Swap, d, dv
			p.Why = fmt.Sprintf("%s; tier S displaces carried %d (%s, tier %s)", full, d.Item.ID, dv.Why, dv.Tier)
			return p
		}
	}
	if c.Space.TownTripForS && sit.HaulOK && sit.Free+sit.SellCells >= p.Need {
		p.Act = Haul
		p.Why = fmt.Sprintf("%s; nothing to drop — town trip frees %d cells at the Fence", full, sit.SellCells)
		return p
	}
	switch {
	case sit.Free+sit.SellCells < p.Need:
		// TODO(stash): no proven stash routine exists (screen detection only);
		// until one is drilled, a bag full of keepers cannot make room.
		p.Why = full + "; bag full of keepers and no proven stash routine (TODO)"
	case !sit.HaulOK:
		p.Why = full + "; no town trip possible now (no portal road or hostiles near)"
	default:
		p.Why = full + "; room-making is off in the config"
	}
	return p
}

// worstDroppable picks the lowest-value carried item that is not pinned, is
// in a lower tier than the target, and frees enough room once dropped.
func (c *Config) worstDroppable(target Verdict, need int, sit Situation) (*Carried, Verdict, bool) {
	var best *Carried
	var bestV Verdict
	for i := range sit.Bag {
		it := &sit.Bag[i]
		v, pinned := c.EvaluateCarried(*it)
		if pinned || v.Tier >= target.Tier || v.Value >= target.Value {
			continue
		}
		if sit.Free+v.Class.Cells() < need {
			continue
		}
		if best == nil || v.Value < bestV.Value ||
			(v.Value == bestV.Value && v.Class.Cells() < bestV.Class.Cells()) {
			best, bestV = it, v
		}
	}
	return best, bestV, best != nil
}

// SellGrade reports whether a carried item is merchandise under the active
// policy: tier B or C and not pinned. bagPct is how full the bag is (0..100);
// identified rare gear only becomes merchandise past Space.SellRaresPct.
func (c *Config) SellGrade(it Carried, bagPct int) bool {
	v, pinned := c.EvaluateCarried(it)
	if pinned || v.Tier >= TierA {
		return false
	}
	if it.Item.Quality == QRare || it.Item.Quality == QCrafted {
		return bagPct >= c.Space.SellRaresPct
	}
	return true
}

// Keep reports whether a carried item must never be sold as junk: lifelines
// and every tier S or A item (runes, gems, charms, the mod's unknown rows...).
// Perception's old sell list fenced magic charms and runes and even listed
// the cube (mod 564); this is the gate that stops it.
func (c *Config) Keep(id, quality int) bool {
	if Lifeline(id) {
		return true
	}
	v := c.Evaluate(Item{ID: id, Quality: quality})
	if v.Class.Kind == KindGear || v.Class.Kind == KindJewelry || v.Class.Kind == KindAmmo || v.Class.Kind == KindPotion {
		return v.Tier >= TierS // gear-like rows: perception's own rules decide below S
	}
	return v.Tier >= TierA
}
