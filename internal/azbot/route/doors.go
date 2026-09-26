package route

// Destination-aware doors (trace of the 2ad968d run): arriving in Maggot Lair
// L2 the next leg picked the entrance nearest a false map hint — the stairs
// BACK UP to L1 — because no entrance was ever checked for where it leads.
// The one destination fact the snapshot carries is the entrance unit's
// txtFileNo (data.Entrance.Name), which indexes lvlwarp.txt: its row name says
// "Up"/"Down"/"to Town". Everything else comes from recorded door facts: the
// arrival door of this area (it leads back to where we came from), learned
// border facts to other neighbours, and doors that already landed wrong.

import (
	"fmt"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/entrance"
)

// Warp direction read from lvlwarp.txt.
const (
	LeadsUnknown = ""
	LeadsUp      = "up"   // shallower level (the way back in a dungeon)
	LeadsDown    = "down" // deeper level
	LeadsTown    = "town"
)

// WarpLeads names where an entrance unit leads by its lvlwarp row.
func WarpLeads(name entrance.Name) string {
	d, ok := entrance.Desc[int(name)]
	if !ok {
		return LeadsUnknown
	}
	n := " " + d.Name + " "
	switch {
	case strings.Contains(n, " to Town "):
		return LeadsTown
	case strings.Contains(n, " Up "):
		return LeadsUp
	case strings.Contains(n, " Down "):
		return LeadsDown
	}
	return LeadsUnknown
}

// DoorFact is a recorded door in the current area: where it is and the area
// it is known (or suspected) to lead to.
type DoorFact struct {
	Pos    data.Position
	LeadTo int
	Why    string // "arrival", "fact", "wrong-landing"
}

// DoorGoal is what the exit choice is for.
type DoorGoal struct {
	Hop    int // the area we want
	Deeper int // +1 hop is deeper on the itinerary, -1 shallower, 0 unknown
	Facts  []DoorFact
}

// FactRadius: an entrance within this many tiles of a door fact is that door.
// Arrival positions sit a few tiles off the entrance unit.
const FactRadius = 8

// Leads says where an entrance leads for this goal and why: from recorded
// facts first (measured), then the lvlwarp direction (-1 = "the way back",
// -2 = "further on"), else unknown (0).
func (g DoorGoal) Leads(name entrance.Name, pos data.Position) (leadTo int, why string) {
	for _, f := range g.Facts {
		if cheb(f.Pos, pos) <= FactRadius && f.LeadTo == g.Hop && f.LeadTo != 0 {
			return f.LeadTo, f.Why // a fact FOR the hop outranks a neighbour's
		}
	}
	for _, f := range g.Facts {
		if cheb(f.Pos, pos) <= FactRadius && f.LeadTo != 0 {
			return f.LeadTo, f.Why
		}
	}
	switch WarpLeads(name) {
	case LeadsUp, LeadsTown:
		return -1, "lvlwarp=" + WarpLeads(name)
	case LeadsDown:
		return -2, "lvlwarp=down"
	}
	return 0, "unknown"
}

// Excluded reports whether an entrance must not be taken for this goal: it is
// known to lead somewhere other than the hop (the arrival door, a learned
// neighbour, a wrong landing), or its warp direction contradicts the hop's.
func (g DoorGoal) Excluded(name entrance.Name, pos data.Position) (bool, string) {
	lt, why := g.Leads(name, pos)
	switch {
	case lt > 0 && lt == g.Hop:
		return false, why
	case lt > 0:
		return true, fmt.Sprintf("leads to %d not %d (%s)", lt, g.Hop, why)
	case lt == -1 && g.Deeper > 0:
		return true, "leads back (" + why + ")"
	case lt == -2 && g.Deeper < 0:
		return true, "leads deeper (" + why + ")"
	}
	return false, why
}

// EntranceCand is one live entrance unit.
type EntranceCand struct {
	ID   data.UnitID
	Name entrance.Name
	Pos  data.Position
}

// ChooseEntrance picks the entrance for the goal nearest the hint (within
// maxDist), never one Excluded. The returned note is the ledger evidence:
// `pick=(x,y) leadsTo=.. why=..` or the reason nothing qualified.
func ChooseEntrance(cands []EntranceCand, hint data.Position, maxDist int, g DoorGoal) (EntranceCand, string, bool) {
	var best EntranceCand
	bd, found := maxDist+1, false
	var skipped []string
	for _, c := range cands {
		if ex, why := g.Excluded(c.Name, c.Pos); ex {
			skipped = append(skipped, fmt.Sprintf("(%d,%d):%s", c.Pos.X, c.Pos.Y, why))
			continue
		}
		if d := cheb(c.Pos, hint); d < bd {
			best, bd, found = c, d, true
		}
	}
	if !found {
		return best, fmt.Sprintf("pick=none leadsTo=- why=no-qualifying-entrance skipped=%v", skipped), false
	}
	lt, why := g.Leads(best.Name, best.Pos)
	lts := "?"
	switch {
	case lt > 0:
		lts = fmt.Sprint(lt)
	case lt == -2:
		lts = "deeper"
	case lt == -1:
		lts = "back"
	}
	return best, fmt.Sprintf("pick=(%d,%d) leadsTo=%s why=%s hint-dist=%d skipped=%v", best.Pos.X, best.Pos.Y, lts, why, bd, skipped), true
}

// NearForeignFact reports whether p sits on a door fact leading elsewhere than
// the hop — a map hint landing there names the wrong door.
func (g DoorGoal) NearForeignFact(p data.Position) bool {
	for _, f := range g.Facts {
		if f.LeadTo != 0 && f.LeadTo != g.Hop && cheb(f.Pos, p) <= FactRadius {
			return true
		}
	}
	return false
}

// SearchSkip: the coverage search walks into unrecorded entrances on sight
// (crossings teach both sides) — but never one known or suspected to lead back
// to the previous area or a shallower level, or one that already landed wrong.
func SearchSkip(name entrance.Name, pos data.Position, g DoorGoal) (bool, string) {
	lt, why := g.Leads(name, pos)
	if lt > 0 && lt != g.Hop {
		return true, fmt.Sprintf("leads to %d (%s)", lt, why)
	}
	return g.Excluded(name, pos)
}

// ContactRadius: within this many tiles of the entrance unit the crossing is a
// contact job (push/hover-confirmed click), never another approach walk.
const ContactRadius = 6

// ContactReady: the approach is over when we stand within ContactRadius, or the
// mover reports Arrived (the planner's "goal walled; at nearest walkable" sits
// 4-5 tiles off a stairs unit whose own tile is unwalkable) near the door.
func ContactReady(dist int, arrived bool) bool {
	return dist <= ContactRadius || (arrived && dist <= ContactRadius+4)
}

// DoorVerdictWindow: a door click succeeded if the area changes within this
// window (measured: a click judged deaf at 1.4s changed area ~2s later).
const DoorVerdictWindow = 3 * time.Second

// DoorVerdict judges a click fired at `at`: done once the area changed or the
// window lapsed; success only for a change.
func DoorVerdict(at, now time.Time, changed bool) (done, success bool) {
	if changed {
		return true, true
	}
	return now.Sub(at) >= DoorVerdictWindow, false
}

// ClickCap is the per-entrance click budget; it resets per leg (hop).
const ClickCap = 3

// ClickBudget counts entrance clicks per (hop, entrance) and forgets when the
// leg or the entrance changes.
type ClickBudget struct {
	Hop int
	ID  data.UnitID
	N   int
}

// Allow reports whether another click at id is allowed for hop.
func (b *ClickBudget) Allow(hop int, id data.UnitID) bool {
	if b.Hop != hop || b.ID != id {
		b.Hop, b.ID, b.N = hop, id, 0
	}
	return b.N < ClickCap
}

// Reset forgets the count (a new leg started).
func (b *ClickBudget) Reset() { *b = ClickBudget{} }

// PortalFirst: back in town after a town portal (unstick/withdraw/recall) with
// our own portal standing, the return goes through it before any waypoint.
func PortalFirst(inTown, livePortal, hot, servicesPending, rerouting bool) bool {
	return inTown && livePortal && !hot && !servicesPending && !rerouting
}

func cheb(a, b data.Position) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}
