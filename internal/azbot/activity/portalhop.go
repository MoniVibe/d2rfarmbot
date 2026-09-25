package activity

import (
	"fmt"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/coverage"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
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
	a.loadArcaneSearch(ctx)
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
			a.saveArcaneSearch(ctx)
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
	// THE WINGS (owner, 2026-09-25: "it went to the wrong arcane wing"): the map's
	// guesses do not match this game's Sanctuary. The Summoner stands at the far
	// end of one of four wings off the central pad: walk each wing's end in turn
	// (N, E, S, W from the pad) until the journal or he streams in.
	if v, ok := a.walkWings(ctx); ok {
		return v
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

// wingReach: how far past the central pad a wing's end is sought (the planner
// snaps to the nearest walkable cell; R52's west wing end sat ~75 tiles out).
const wingReach = 160

// wingBudget: one wing's walk (R62: 90s cut the ~430-tile wings short, and
// two unreached wings were counted searched).
const wingBudget = 4 * time.Minute

// walkWings: the next unwalked wing end from the Sanctuary's central pad.
// A wing is done when he arrives, or 90s pass, or the route fails; the
// Summoner seen live ends the search (Fight takes him; the journal follows).
func (a *Advance) walkWings(ctx *Ctx) (Verdict, bool) {
	s := ctx.Snap
	dd := ctx.GR.GetData()
	for _, m := range dd.Monsters {
		if m.Name == npc.Summoner {
			if chebyshev(s.Me.Pos, m.Position) > 6 {
				a.notePortalHop(ctx, "the Summoner (seen live)", true, m.Position)
				moveTo(ctx, m.Position, marchOpts(ctx, a.Name(), 1200*time.Millisecond))
			}
			return Running, true
		}
	}
	if a.wingCenter == (data.Position{}) {
		var pad data.Object
		ok := false
		for _, ob := range dd.Objects {
			if ob.IsWaypoint() {
				pad, ok = ob, true
				break
			}
		}
		if !ok {
			return 0, false
		}
		a.wingCenter = pad.Position
	}
	dirs := [4][2]int{{0, -1}, {1, 0}, {0, 1}, {-1, 0}}
	for a.wingIdx < len(dirs) {
		if a.wingDone[a.wingIdx] {
			a.wingIdx++
			continue
		}
		d := dirs[a.wingIdx]
		end := wingEnd(ctx.Grid, a.wingCenter, d)
		if a.wingAt.IsZero() {
			a.wingAt = time.Now()
			ctx.Led.Append(verbs.Outcome{Verb: "quest", Holder: a.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("searching Sanctuary wing %d/4 toward (%d,%d)", a.wingIdx+1, end.X, end.Y)})
		}
		st := moveTo(ctx, end, marchOpts(ctx, a.Name(), 1200*time.Millisecond))
		if time.Since(a.wingAt) > wingBudget || stalled(st) || st.State == moveto.Arrived {
			if a.wingDone == nil {
				a.wingDone = map[int]bool{}
			}
			a.wingDone[a.wingIdx] = true
			a.saveArcaneSearch(ctx)
			a.wingIdx++
			a.wingAt = time.Time{}
			continue
		}
		return Running, true
	}
	return 0, false
}

// wingEnd: the farthest walkable cell from the pad along a wing's axis (within a
// narrow band either side of it) — R61: a fixed point 160 tiles out sat off the
// map (NoPath in 1s), and the west wing's end proved ~430 tiles from the pad.
// No grid: the old fixed reach.
func wingEnd(g *game.Grid, c data.Position, d [2]int) data.Position {
	best := data.Position{X: c.X + d[0]*wingReach, Y: c.Y + d[1]*wingReach}
	if g == nil {
		return best
	}
	bestT := 0
	for t := 10; t < 700; t++ {
		for off := -12; off <= 12; off++ {
			p := data.Position{X: c.X + d[0]*t + d[1]*off, Y: c.Y + d[1]*t + d[0]*off}
			if g.IsWalkable(p) && t > bestT {
				best, bestT = p, t
			}
		}
	}
	return best
}

// ---------------------------------------------------------------- the Sanctuary clock
//
// Three D2R crashes (2026-09-25 06:03, 12:43, 13:13), all in the Arcane
// Sanctuary: the game's own log fills with "particle limit for an animated mesh
// particle effect exceeded (limit 128)" from seconds after entry, the count
// climbs all game (town trips do not reset it) to ~260-290, then the renderer
// dies. A NEW GAME should start it over (owner: "do it, we will try"): after
// sanctuaryBudget of accumulated time in the Sanctuary this game, request a
// relog; the lit Sanctuary pad brings him straight back.

// R64: D2R died 10 min in (the count climbs with the Sanctuary loaded, not only
// with time) — six minutes is one wing per game, the search persists per seed.
const sanctuaryBudget = 6 * time.Minute

var sanctuary struct {
	sync.Mutex
	spent  time.Duration
	lastAt time.Time
	asked  bool
}

// sanctuaryClock accumulates Sanctuary time per game (called every tick).
func sanctuaryClock(s *percept.Snapshot, now time.Time) {
	sanctuary.Lock()
	defer sanctuary.Unlock()
	if !s.Valid || s.Me.Area != area.ArcaneSanctuary {
		sanctuary.lastAt = time.Time{}
		return
	}
	if !sanctuary.lastAt.IsZero() {
		if d := now.Sub(sanctuary.lastAt); d < 5*time.Second {
			sanctuary.spent += d
		}
	}
	sanctuary.lastAt = now
	if sanctuary.spent >= sanctuaryBudget && !sanctuary.asked {
		sanctuary.asked = true
		RequestRelog(fmt.Sprintf("%s in the Arcane Sanctuary this game — a new game resets the particle build-up that crashed D2R three times", sanctuaryBudget))
	}
}

// resetSanctuaryClock: a new game (NewWorld) starts the count over.
func resetSanctuaryClock() {
	sanctuary.Lock()
	sanctuary.spent, sanctuary.lastAt, sanctuary.asked = 0, time.Time{}, false
	sanctuary.Unlock()
}

// The Sanctuary search remembers itself per map seed (owner, 2026-09-25: "it
// tries the same wings though, it should go the other ones" — the layout is
// the seed's and survives restarts and relogs; the search did not).
func wingKey(seed uint) string  { return fmt.Sprintf("arcane.wings.%d", seed) }
func guessKey(seed uint) string { return fmt.Sprintf("arcane.guesses.%d", seed) }

// loadArcaneSearch restores the searched wings and dead guesses once per seed.
func (a *Advance) loadArcaneSearch(ctx *Ctx) {
	seed := uint(ctx.GR.MapSeed())
	if a.arcaneSeed == seed || ctx.Mem == nil {
		return
	}
	a.arcaneSeed = seed
	var wings []int
	if ctx.Mem.GetJSON(wingKey(seed), &wings) {
		a.wingDone = map[int]bool{}
		for _, w := range wings {
			a.wingDone[w] = true
		}
	}
	var guesses []data.Position
	if ctx.Mem.GetJSON(guessKey(seed), &guesses) {
		if a.journalTried == nil {
			a.journalTried = map[data.Position]bool{}
		}
		for _, g := range guesses {
			a.journalTried[g] = true
		}
	}
}

func (a *Advance) saveArcaneSearch(ctx *Ctx) {
	if ctx.Mem == nil {
		return
	}
	seed := uint(ctx.GR.MapSeed())
	var wings []int
	for w := range a.wingDone {
		wings = append(wings, w)
	}
	var guesses []data.Position
	for g := range a.journalTried {
		guesses = append(guesses, g)
	}
	prov := memory.Provenance{Source: "measured", Evidence: "Sanctuary searched: no Summoner, no journal"}
	ctx.Mem.PutJSON(wingKey(seed), memory.ScopeForever, prov, wings)
	ctx.Mem.PutJSON(guessKey(seed), memory.ScopeForever, prov, guesses)
}
