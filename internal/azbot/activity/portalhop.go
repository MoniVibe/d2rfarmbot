package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/coverage"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// portalHop is Advance's road for the legs whose way in is an OBJECT: the
// Arcane Sanctuary portal in Palace Cellar 3, and Horazon's red portal from the
// Sanctuary to the Canyon of the Magi. ok=false: not such a leg (the level-warp
// march takes it).
func (a *Advance) portalHop(ctx *Ctx, to area.ID) (Verdict, bool) {
	s := ctx.Snap
	switch {
	case s.Me.Area == area.PalaceCellarLevel3 && to == area.ArcaneSanctuary:
		return a.enterObjectPortal(ctx, object.ArcaneSanctuaryPortal, "the Arcane Sanctuary portal"), true
	case s.Me.Area == area.ArcaneSanctuary && to == area.CanyonOfTheMagi:
		// The red portal stands once the journal is read: enter it.
		if _, live := findLive(ctx.GR.GetData().Objects, object.PermanentTownPortal); live {
			return a.enterObjectPortal(ctx, object.PermanentTownPortal, "Horazon's portal to the Canyon"), true
		}
		// Otherwise walk to the journal: the Summoner guards it (Fight preempts
		// the march), and Imbibe reads it (a quest object, 60-tile reach).
		return a.seekJournal(ctx), true
	}
	return 0, false
}

// seekJournal: the live journal first; else the map's guesses in turn (the
// journal preset, then the Summoner's), each dropped once he stands at it with
// no live journal in sight; then a coverage sweep until the rooms stream it.
// R52: the journal preset sat in the void beside the west platform — he stood
// "arrived" 5 tiles short for 20 minutes with no journal object anywhere.
func (a *Advance) seekJournal(ctx *Ctx) Verdict {
	s := ctx.Snap
	dd := ctx.GR.GetData()
	if ob, ok := findLive(dd.Objects, object.YetAnotherTome); ok {
		a.notePortalHop(ctx, "Horazon's Journal (live)", true, ob.Position)
		if chebyshev(s.Me.Pos, ob.Position) > 4 {
			moveTo(ctx, ob.Position, marchOpts(ctx, a.Name(), 1200*time.Millisecond))
		}
		return Running
	}
	var guesses []data.Position
	if ad, ok := dd.Areas[s.Me.Area]; ok {
		if ob, ok := findObject(ad.Objects, object.YetAnotherTome); ok {
			guesses = append(guesses, ob.Position)
		}
		for _, n := range ad.NPCs {
			if n.ID == npc.Summoner {
				guesses = append(guesses, n.Positions...)
			}
		}
	}
	if a.journalTried == nil {
		a.journalTried = map[data.Position]bool{}
	}
	for _, g := range guesses {
		if a.journalTried[g] {
			continue
		}
		if chebyshev(s.Me.Pos, g) <= 7 || (a.journalWalkFor == g && time.Since(a.journalWalkAt) > 90*time.Second) {
			a.journalTried[g] = true // stood there (or could not): no journal — next guess
			ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResRefused,
				Evidence: fmt.Sprintf("no live journal at the map's guess (%d,%d) — next", g.X, g.Y)})
			continue
		}
		if a.journalWalkFor != g {
			a.journalWalkFor, a.journalWalkAt = g, time.Now()
			a.notePortalHop(ctx, fmt.Sprintf("the journal guess (%d,%d)", g.X, g.Y), true, g)
		}
		moveTo(ctx, g, marchOpts(ctx, a.Name(), 1200*time.Millisecond))
		return Running
	}
	a.notePortalHop(ctx, "Horazon's Journal (sweeping the Sanctuary)", false, data.Position{})
	if st, ok := a.cov.step(ctx, coverage.Bias{}, a.Name()); ok && st == coverage.Exploring {
		return Running
	}
	return Abandoned
}

func findObject(obs []data.Object, name object.Name) (data.Object, bool) {
	for _, ob := range obs {
		if ob.Name == name {
			return ob, true
		}
	}
	return data.Object{}, false
}

// objectPos: the live object, else the map oracle's preset for this area.
func (a *Advance) objectPos(ctx *Ctx, name object.Name) (data.Object, bool, bool) {
	dd := ctx.GR.GetData()
	if ob, ok := findLive(dd.Objects, name); ok {
		return ob, true, true
	}
	if ad, ok := dd.Areas[ctx.Snap.Me.Area]; ok {
		if ob, ok := findObject(ad.Objects, name); ok {
			return ob, false, true
		}
	}
	return data.Object{}, false, false
}

// approachObject walks to an object (live or on the map); unknown: sweep the level.
func (a *Advance) approachObject(ctx *Ctx, name object.Name, label string) Verdict {
	s := ctx.Snap
	ob, _, known := a.objectPos(ctx, name)
	a.notePortalHop(ctx, label, known, ob.Position)
	if !known {
		if st, ok := a.cov.step(ctx, coverage.Bias{}, a.Name()); ok && st == coverage.Exploring {
			return Running
		}
		return Abandoned
	}
	if chebyshev(s.Me.Pos, ob.Position) > 4 {
		moveTo(ctx, ob.Position, marchOpts(ctx, a.Name(), 1200*time.Millisecond))
	}
	return Running
}

// enterObjectPortal walks to a portal object and clicks it (hover-confirmed by
// unit, the area change is the postcondition — EnterPortal's own recipe).
func (a *Advance) enterObjectPortal(ctx *Ctx, name object.Name, label string) Verdict {
	s := ctx.Snap
	ob, live, known := a.objectPos(ctx, name)
	if !live || chebyshev(s.Me.Pos, ob.Position) > 5 {
		return a.approachObject(ctx, name, label)
	}
	if time.Since(a.hopClickAt) < 2*time.Second {
		return Running
	}
	a.hopClickAt = time.Now()
	ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: a.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("portal hop: entering %s (obj %d) at (%d,%d) known=%v", label, int(name), ob.Position.X, ob.Position.Y, known)})
	verbs.EnterPortal{Target: ob.ID, TargetPos: ob.Position, Window: 5 * time.Second}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
	return Running
}

// notePortalHop: one log line per area and label.
func (a *Advance) notePortalHop(ctx *Ctx, label string, known bool, at data.Position) {
	key := fmt.Sprintf("%d:%s", int(ctx.Snap.Me.Area), label)
	if a.hopNoted == key {
		return
	}
	a.hopNoted = key
	ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("objective: %s (on the map=%v at (%d,%d))", label, known, at.X, at.Y)})
}

// talRashaTombs: the seven tombs off the Canyon; one holds the Horadric Orifice.
var talRashaTombs = []area.ID{area.TalRashasTomb1, area.TalRashasTomb2, area.TalRashasTomb3,
	area.TalRashasTomb4, area.TalRashasTomb5, area.TalRashasTomb6, area.TalRashasTomb7}

// noteRealTomb: in the Canyon (or a tomb), the map oracle names the true tomb —
// the one whose presets hold the Horadric Orifice (upstream's own recipe; the
// quest log's symbol is unreadable here) — and it joins the itinerary as the
// next leg. Idempotent.
func (a *Advance) noteRealTomb(ctx *Ctx) {
	s := ctx.Snap
	if s.Me.Area != area.CanyonOfTheMagi && !isTomb(s.Me.Area) {
		return
	}
	if len(a.Itinerary) == 0 || isTomb(a.Itinerary[len(a.Itinerary)-1].Area) {
		return
	}
	dd := ctx.GR.GetData()
	for _, t := range talRashaTombs {
		ad, ok := dd.Areas[t]
		if !ok {
			continue
		}
		if _, has := findObject(ad.Objects, object.HoradricOrifice); has {
			a.Itinerary = append(a.Itinerary, Leg{Area: t, MinLevel: 30})
			ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("the true tomb is area %d (the Horadric Orifice is on its map) — it is the next leg", int(t))})
			return
		}
	}
}

func isTomb(ar area.ID) bool {
	for _, t := range talRashaTombs {
		if t == ar {
			return true
		}
	}
	return false
}

// routeVia: the level-warp waypoint on the way to a leg whose door is an
// object (R51: the Sanctuary was the leg from Palace Cellar 1 and nextHop, which
// knows only warps, found no road — 30 minutes pinned). The Canyon is reached
// through the Sanctuary, the Sanctuary through Palace Cellar 3, a tomb through
// the Canyon.
func routeVia(cur, to area.ID) area.ID {
	if cur == to {
		return to
	}
	for i := 0; i < 3; i++ {
		switch {
		case isTomb(to) && cur != area.CanyonOfTheMagi && !isTomb(cur):
			to = area.CanyonOfTheMagi
		case to == area.CanyonOfTheMagi && cur != area.ArcaneSanctuary:
			to = area.ArcaneSanctuary
		case to == area.ArcaneSanctuary && cur != area.PalaceCellarLevel3:
			to = area.PalaceCellarLevel3
		default:
			return to
		}
	}
	return to
}

// findLive: an object the LIVE unit table holds. game.MemoryReader merges the
// map oracle's presets into Objects (unit ID 0, maybe unreal positions — R53:
// the journal preset in the void read as "live").
func findLive(obs []data.Object, name object.Name) (data.Object, bool) {
	for _, ob := range obs {
		if ob.Name == name && ob.ID != 0 {
			return ob, true
		}
	}
	return data.Object{}, false
}
