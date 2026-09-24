package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/coverage"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- quest legs

// OWNER (2026-09-24, R14): "it's wasting time instead of running the quest".
// R14 reached Maggot Lair 3, counted the leg done on arrival and walked straight
// back out for Lost City without opening the Staff of Kings chest. A quest leg
// is done when its artifact is held (or its chest is spent), not when the area
// ID changes. This is the first cut of docs/AZBOT_MISSION.md's objectives,
// riding the existing itinerary.

// questLeg: the chest (objects.txt row, which is the object Name id) whose opening
// completes the leg, and the item code it yields.
type questLeg struct {
	chest object.Name
	item  string // mod item code (items tables are the mod's own, no shift)
	label string
}

var questLegs = map[area.ID]questLeg{
	area.HallsOfTheDeadLevel3: {chest: 354, item: "box", label: "Horadric Cube"},
	area.MaggotLairLevel3:     {chest: 356, item: "msf", label: "Staff of Kings"},
}

// questBudget: held time a quest leg may spend before the march moves on anyway.
const questBudget = 12 * time.Minute

// questReach: Imbibe's detour radius for a quest chest (a rite's is 25).
const questReach = 60

func isQuestChest(n object.Name) bool { return percept.QuestChests[n] }

// questItemHeld: the artifact is in the bag, the stash or the cube (the leg's
// idempotent postcondition: it survives a relog, where the chest respawns).
func questItemHeld(d data.Data, code string) bool {
	db := gamedata.Get()
	if db == nil {
		return false
	}
	for _, loc := range []item.LocationType{item.LocationInventory, item.LocationStash, item.LocationSharedStash, item.LocationCube, item.LocationEquipped} {
		for _, it := range d.Inventory.ByLocation(loc) {
			if row := db.Item(int(it.ID)); row != nil && row.Code == code {
				return true
			}
		}
	}
	return false
}

// questState: for the area he stands in. pending=false means no quest here, or done.
func questState(d data.Data, ar area.ID) (q questLeg, chest data.Object, seen, pending bool) {
	q, ok := questLegs[ar]
	if !ok || questItemHeld(d, q.item) {
		return q, chest, false, false
	}
	for _, ob := range d.Objects {
		if ob.Name == q.chest {
			chest, seen = ob, true
			break
		}
	}
	if seen && !chest.Selectable {
		// Spent: the artifact is on the ground or already taken. Loot (quest tier S)
		// picks it up; a chest that is open with nothing held still ends the leg so
		// a failed pickup can't hold the march forever.
		return q, chest, true, false
	}
	return q, chest, seen, true
}

// questHold is Advance's quest hook, called every Step before the next leg is
// considered. true = this step belonged to the quest (explore toward the chest,
// or walk to it for Imbibe's click).
func (a *Advance) questHold(ctx *Ctx) bool {
	s := ctx.Snap
	if s.Me.InTown {
		return false
	}
	d := ctx.GR.GetData()
	q, chest, seen, pending := questState(d.Data, s.Me.Area)
	key := fmt.Sprintf("%d.%d", ctx.GR.MapSeed(), int(s.Me.Area))
	if !pending {
		if _, was := a.questSince[key]; was && !a.questDone[key] {
			a.questDone[key] = true
			ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("%s leg complete in area %d (held=%v chestSeen=%v)", q.label, int(s.Me.Area), questItemHeld(d.Data, q.item), seen)})
		}
		return false
	}
	if a.questSince == nil {
		a.questSince, a.questDone = map[string]time.Time{}, map[string]bool{}
	}
	since, ok := a.questSince[key]
	if !ok {
		since = time.Now()
		a.questSince[key] = since
		ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("%s leg: holding area %d until the chest (obj %d) is opened", q.label, int(s.Me.Area), int(q.chest))})
	}
	if a.questDone[key] {
		return false
	}
	if time.Since(since) > questBudget {
		a.questDone[key] = true
		ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResRefused,
			Evidence: fmt.Sprintf("%s leg: budget %s spent in area %d (chestSeen=%v) — marching on", q.label, questBudget, int(s.Me.Area), seen)})
		return false
	}
	if seen {
		// Stand beside it; Imbibe's quest-chest rite does the hover-confirmed click.
		if chebyshev(s.Me.Pos, chest.Position) > 4 {
			moveTo(ctx, chest.Position, moveto.Opts{Holder: a.Name(), Purpose: moveto.Approach, Arrive: 4, MaxHold: 1200 * time.Millisecond, Fallback: true})
		}
		return true
	}
	// Not in sight yet: sweep the level (the same coverage picker the door search uses).
	if st, ok := a.cov.step(ctx, coverage.Bias{}, a.Name()); ok && st == coverage.Exploring {
		return true
	}
	return false // nothing left to explore: let the march judge (the budget still ends it)
}

// questCapFor: the first itinerary leg, at or behind the saved frontier, whose
// quest is unfinished on this seed (artifact not held, hold not ended). -1 = none.
func (a *Advance) questCapFor(ctx *Ctx) int {
	far := a.idx
	if a.frontier > far {
		far = a.frontier
	}
	d := ctx.GR.GetData()
	for i := 0; i <= far && i < len(a.Itinerary); i++ {
		q, ok := questLegs[a.Itinerary[i].Area]
		if !ok || questItemHeld(d.Data, q.item) {
			continue
		}
		if a.questDone[fmt.Sprintf("%d.%d", ctx.GR.MapSeed(), int(a.Itinerary[i].Area))] {
			continue
		}
		return i
	}
	return -1
}

// logQuestCap: one ledger line per cap change, with what is held (the R16
// question: "is the Cube really missing, or is the held check blind?").
func (a *Advance) logQuestCap(ctx *Ctx) {
	d := ctx.GR.GetData()
	leg := area.ID(0)
	if a.questCap >= 0 && a.questCap < len(a.Itinerary) {
		leg = a.Itinerary[a.questCap].Area
	}
	ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("cap=%d (area %d) frontier=%d held: cube=%v staff=%v", a.questCap, int(leg), a.frontier,
			questItemHeld(d.Data, "box"), questItemHeld(d.Data, "msf"))})
}
