package activity

import (
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/route"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

// arrivalDoor is where she landed on entering Area, coming from From: the
// door there leads BACK to From.
type arrivalDoor struct {
	Area area.ID
	From area.ID
	Pos  data.Position
}

// wrongDoor is a door that landed in To when another area was wanted.
type wrongDoor struct {
	Pos data.Position
	To  area.ID
}

func wrongDoorKey(seed uint, from area.ID) string {
	return fmt.Sprintf("wrongdoor.%d.%d", seed, int(from))
}

// noteArrival records the ARRIVAL door on every area entry (the first leg
// included — the trace's Far Oasis door had no record because the first
// entry wrote none). A portal/waypoint landing is not a door: only an entry
// from an adjacent level is recorded.
func (a *Advance) noteArrival(ctx *Ctx, cur area.ID, pos data.Position, inTown bool) {
	if cur == 0 || cur == a.seenArea {
		return
	}
	prev := a.seenArea
	a.seenArea = cur
	if prev == 0 || inTown {
		return
	}
	adjacent := false
	if ad, ok := ctx.GR.GetData().Areas[cur]; ok {
		for _, lv := range ad.AdjacentLevels {
			if lv.Area == prev {
				adjacent = true
				break
			}
		}
	}
	if !adjacent {
		return
	}
	a.arrival = arrivalDoor{Area: cur, From: prev, Pos: pos}
	if ctx.Mem != nil {
		key := BorderKey(ctx.GR.MapSeed(), cur, prev)
		var p data.Position
		if !ctx.Mem.GetJSON(key, &p) || p.X == 0 {
			ctx.Mem.PutJSON(key, memory.ScopeSeed, memory.Provenance{Source: "measured",
				Evidence: fmt.Sprintf("arrival door: entered %d from %d at (%d,%d)", int(cur), int(prev), pos.X, pos.Y)}, pos)
		}
	}
	ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: a.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("arrival door recorded in %d at (%d,%d) leadsTo=%d", int(cur), pos.X, pos.Y, int(prev))})
}

// recordWrongDoor: an entrance that landed in the wrong area is a fact at once
// — never retaken for that goal (or any goal but the area it leads to).
func (a *Advance) recordWrongDoor(ctx *Ctx, from, landed area.ID, pos data.Position) {
	if ctx.Mem == nil || from == 0 || landed == 0 {
		return
	}
	seed := ctx.GR.MapSeed()
	var list []wrongDoor
	ctx.Mem.GetJSON(wrongDoorKey(seed, from), &list)
	for _, w := range list {
		if w.To == landed && chebyshev(w.Pos, pos) <= route.FactRadius {
			return
		}
	}
	list = append(list, wrongDoor{Pos: pos, To: landed})
	ctx.Mem.PutJSON(wrongDoorKey(seed, from), memory.ScopeSeed, memory.Provenance{Source: "measured",
		Evidence: fmt.Sprintf("entrance at (%d,%d) in %d landed %d", pos.X, pos.Y, int(from), int(landed))}, list)
	key := BorderKey(seed, from, landed)
	var p data.Position
	if !ctx.Mem.GetJSON(key, &p) || p.X == 0 {
		ctx.Mem.PutJSON(key, memory.ScopeSeed, memory.Provenance{Source: "measured",
			Evidence: fmt.Sprintf("wrong landing: door (%d,%d) in %d leads to %d", pos.X, pos.Y, int(from), int(landed))}, pos)
	}
}

// itinIdx is the itinerary position of ar, if it is on the itinerary.
func (a *Advance) itinIdx(ar area.ID) (int, bool) {
	for i, lg := range a.Itinerary {
		if lg.Area == ar {
			return i, true
		}
	}
	return 0, false
}

// doorGoal gathers every destination fact for the doors of the current area:
// the arrival door, learned border facts to each neighbour, wrong landings —
// and whether the hop lies deeper or shallower on the itinerary.
func (a *Advance) doorGoal(ctx *Ctx, d game.Data, hop area.ID) route.DoorGoal {
	cur := d.PlayerUnit.Area
	g := route.DoorGoal{Hop: int(hop)}
	if ic, ok := a.itinIdx(cur); ok {
		if ih, ok := a.itinIdx(hop); ok && ih != ic {
			g.Deeper = 1
			if ih < ic {
				g.Deeper = -1
			}
		}
	}
	if a.arrival.Area == cur && a.arrival.From != 0 {
		g.Facts = append(g.Facts, route.DoorFact{Pos: a.arrival.Pos, LeadTo: int(a.arrival.From), Why: "arrival"})
	}
	if ctx.Mem == nil {
		return g
	}
	seed := ctx.GR.MapSeed()
	if ad, ok := d.Areas[cur]; ok {
		for _, al := range ad.AdjacentLevels {
			var p data.Position
			if ctx.Mem.GetJSON(BorderKey(seed, cur, al.Area), &p) && p.X != 0 {
				g.Facts = append(g.Facts, route.DoorFact{Pos: p, LeadTo: int(al.Area), Why: "fact"})
			}
		}
	}
	var list []wrongDoor
	ctx.Mem.GetJSON(wrongDoorKey(seed, cur), &list)
	for _, w := range list {
		g.Facts = append(g.Facts, route.DoorFact{Pos: w.Pos, LeadTo: int(w.To), Why: "wrong-landing"})
	}
	return g
}

// entranceCands lifts the live entrance units into the route package's shape.
func entranceCands(es data.Entrances) []route.EntranceCand {
	out := make([]route.EntranceCand, 0, len(es))
	for _, e := range es {
		out = append(out, route.EntranceCand{ID: e.ID, Name: e.Name, Pos: e.Position})
	}
	return out
}
