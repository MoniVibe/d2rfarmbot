package activity

import (
	"fmt"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/coverage"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
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
	area.HallsOfTheDeadLevel3:  {chest: 354, item: "box", label: "Horadric Cube"},
	area.MaggotLairLevel3:      {chest: 356, item: "msf", label: "Staff of Kings"},
	area.ClawViperTempleLevel2: {chest: 149, item: "vip", label: "Amulet of the Viper"}, // TaintedSunShrine altar
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
		// Spent: the artifact is on the ground (Loot, quest tier S, picks it up —
		// the leg stays pending while it lies there) or gone from this game.
		if questItemOnGround(d, q.item) {
			return q, chest, true, true
		}
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
	if seed := uint(ctx.GR.MapSeed()); seed != a.questSeed {
		a.questSeed = seed
	}
	if a.questSince == nil {
		a.questSince, a.questDone = map[string]time.Time{}, map[string]bool{}
	}
	q, chest, seen, pending := questState(d.Data, s.Me.Area)
	key := fmt.Sprintf("%d.%d", ctx.GR.MapSeed(), int(s.Me.Area))
	if !pending {
		if _, isQuest := questLegs[s.Me.Area]; isQuest && !a.questDone[key] {
			a.questDone[key] = true // also stops Demand's quest bid for this area (this game)
			held := questItemHeld(d.Data, q.item)
			// A spent chest, the artifact neither carried nor on the ground: THIS
			// game will never give it (R43: the Staff chest opened in R35 stayed
			// open — bot restarts keep the same game). A relog makes a new game,
			// where the chest re-arms. Once per area per run.
			if !held && seen && !a.relogAsked[s.Me.Area] {
				if a.relogAsked == nil {
					a.relogAsked = map[area.ID]bool{}
				}
				a.relogAsked[s.Me.Area] = true
				RequestRelog(fmt.Sprintf("%s: the chest is spent in this game and the artifact is gone — a new game re-arms it", q.label))
				delete(a.questDone, key) // pending again in the new game
			}
			// FOREVER only with the artifact in hand (owner, R41: "we still miss the
			// staff in maggot lair 3" — R35 found the chest spent, held=false, and
			// marked the leg done for good). A spent chest without the artifact
			// re-arms in a new game until it is taken; only a chest spent in two
			// different games (the cube the old fence sold) ends the leg for good.
			if held || a.spentInGames(ctx, s.Me.Area) >= 2 {
				a.markQuestForever(ctx, s.Me.Area, held, seen)
			}
			ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("%s leg complete in area %d (held=%v chestSeen=%v)", q.label, int(s.Me.Area), questItemHeld(d.Data, q.item), seen)})
		}
		return false
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
		if a.questDone[fmt.Sprintf("%d.%d", ctx.GR.MapSeed(), int(a.Itinerary[i].Area))] || a.questForeverDone(ctx, a.Itinerary[i].Area) {
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

// questWanted: Demand's cheap check (no game data) — this area has a quest leg
// not yet ended on the current seed. Step's questHold decides the rest.
func (a *Advance) questWanted(ar area.ID) bool {
	if _, ok := questLegs[ar]; !ok {
		return false
	}
	return !a.questDone[fmt.Sprintf("%d.%d", a.questSeed, int(ar))] && !a.questForever[ar]
}

// A spent quest chest stays spent in this save: the chest does not re-arm on a
// new game, so an artifact lost afterwards (the old fence SOLD the cube, row 564)
// can never be re-taken there. R21: every new process re-marched to Halls of the
// Dead 3 for nothing. The leg's end is remembered per character, for good.
func questForeverKey(char string, ar area.ID) string {
	return fmt.Sprintf("questdone.%s.%d", char, int(ar))
}

func (a *Advance) markQuestForever(ctx *Ctx, ar area.ID, held, chestSeen bool) {
	if a.questForever == nil {
		a.questForever = map[area.ID]bool{}
	}
	a.questForever[ar] = true
	if ctx.Mem != nil {
		ctx.Mem.PutJSON(questForeverKey(ctx.GR.GetData().PlayerUnit.Name, ar), memory.ScopeForever,
			memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("quest leg ended: held=%v chestSpent=%v", held, chestSeen)}, true)
	}
}

func (a *Advance) questForeverDone(ctx *Ctx, ar area.ID) bool {
	if a.questForever[ar] {
		return true
	}
	if ctx.Mem == nil {
		return false
	}
	done := false
	if ctx.Mem.GetJSON(questForeverKey(ctx.GR.GetData().PlayerUnit.Name, ar), &done) && done {
		if a.questForever == nil {
			a.questForever = map[area.ID]bool{}
		}
		a.questForever[ar] = true
		return true
	}
	return false
}

// questRun names this bot run: the map seed survives a relog, so distinct runs
// (processes) stand in for distinct games.
var questRun = uint(time.Now().Unix())

// spentInGames records this run as one where the area's quest chest was found
// spent without the artifact, and returns how many distinct runs did so.
func (a *Advance) spentInGames(ctx *Ctx, ar area.ID) int {
	if ctx.Mem == nil {
		return 0
	}
	key := fmt.Sprintf("questspent.%s.%d", ctx.GR.GetData().PlayerUnit.Name, int(ar))
	var seeds []uint
	ctx.Mem.GetJSON(key, &seeds)
	seed := questRun
	for _, x := range seeds {
		if x == seed {
			return len(seeds)
		}
	}
	seeds = append(seeds, seed)
	ctx.Mem.PutJSON(key, memory.ScopeForever, memory.Provenance{Source: "measured", Evidence: "quest chest spent, artifact not held"}, seeds)
	return len(seeds)
}

// questItemOnGround: the artifact lies on the ground within reach of the reads.
func questItemOnGround(d data.Data, code string) bool {
	db := gamedata.Get()
	if db == nil {
		return false
	}
	for _, it := range d.Inventory.ByLocation(item.LocationGround) {
		if row := db.Item(int(it.ID)); row != nil && row.Code == code {
			return true
		}
	}
	return false
}

// relog requests from activities (the executive takes one per call).
var relogReq struct {
	sync.Mutex
	why string
}

// RequestRelog asks the session for a new game.
func RequestRelog(why string) {
	relogReq.Lock()
	relogReq.why = why
	relogReq.Unlock()
}

// TakeRelogRequest returns and clears a pending request ("" = none).
func TakeRelogRequest() string {
	relogReq.Lock()
	defer relogReq.Unlock()
	w := relogReq.why
	relogReq.why = ""
	return w
}
