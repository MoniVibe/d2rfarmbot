// Advance: the campaign leg-walker — azbot's cross-area march. "Rampage across Act 1"
// decomposes into legs: in each area, resolve WHERE the door to the next stop is, journey
// there, cross, verify the area changed. Fight and Loot outrank Advance by CLASS, so the
// rampage emerges from the arbiter: she kills whatever the march walks her into.
//
// THE CROSSING EPISTEMOLOGY (measured 2026-07-19): koolo-map's world placement LIES on
// this mod — it put Blood Moor's gate EAST of town while the hand-piloted road proves it
// WEST. So: TOPOLOGY (which areas connect) comes from map data; GEOMETRY (where the door
// is) comes only from live truth, in three layers:
//   1. Learned seed facts (border.<from>.<to> in the WAL) — the cartographer records
//      every crossing she ever makes, both sides of the door, however she made it.
//   2. The live room graph's cross-level border rooms (walkable borders, readable from
//      anywhere in the area).
//   3. Nothing known → SEARCH: tour the area on a persistent heading and walk through
//      unknown entrance units on sight — the wrong cave teaches its own way back.
package activity

import (
	"fmt"
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

// BorderKey is the WAL fact key for a learned crossing: the position on the FROM side
// where the transition to TO fired. Written by the executive's cartographer on every
// area change; consumed here as layer-1 door knowledge. SEED-NAMESPACED: the map seed
// re-rolls every new game (measured 2026-07-19, relog drill: 466817790 → 1502982702),
// so door geometry from one world must never steer another.
func BorderKey(seed uint, from, to area.ID) string {
	return fmt.Sprintf("border.%d.%d.%d", seed, int(from), int(to))
}

// Leg is one stop on an itinerary: the area, and the character level that makes entering
// it worthwhile. Under-leveled → Advance stops bidding and she grinds where she stands
// (deaths are acceptable; futility is not).
type Leg struct {
	Area     area.ID
	MinLevel int
}

// FarmItinerary: the proven farm circuit (Blood Moor → Cold Plains → Stony Field).
func FarmItinerary() []Leg {
	return []Leg{{area.BloodMoor, 1}, {area.ColdPlains, 3}, {area.StonyField, 6}}
}

// areaMlvl: approximate monster levels, Act 1 normal — the EXP ORACLE's table.
// The rule of the game: experience collapses when clvl outruns mlvl by more than ~5.
var areaMlvl = map[area.ID]int{
	area.BloodMoor: 1, area.DenOfEvil: 1, area.ColdPlains: 2, area.BurialGrounds: 3,
	area.Crypt: 3, area.Mausoleum: 3, area.StonyField: 4, area.UndergroundPassageLevel1: 4,
	area.UndergroundPassageLevel2: 4, area.DarkWood: 5, area.BlackMarsh: 6, area.HoleLevel1: 5,
	area.HoleLevel2: 5, area.ForgottenTower: 7, area.TamoeHighland: 8, area.PitLevel1: 7, area.PitLevel2: 7,
	area.MonasteryGate: 8, area.OuterCloister: 9, area.Barracks: 9, area.JailLevel1: 10,
	area.JailLevel2: 10, area.JailLevel3: 10, area.InnerCloister: 10, area.Cathedral: 11,
	area.CatacombsLevel1: 11, area.CatacombsLevel2: 11, area.CatacombsLevel3: 12,
	area.CatacombsLevel4: 12,
}

// ExpWorthwhile is the EXP ORACLE (the owner: "so it knows which area it should go
// for and not waste time on low level monsters"): fighting here still pays. Gap ≤4 —
// at 5 the moor kept a level-7 amazon busy with gray trash (the owner: "she still
// kills in the blood moor and its barely xp, she should try to push for stony field").
func ExpWorthwhile(clvl int, ar area.ID) bool {
	ml, ok := areaMlvl[ar]
	if !ok {
		return true // unknown ground: assume it pays
	}
	return clvl-ml <= 4
}

// Act1Itinerary: the full march to Andariel's chamber. Level gates are mild — a rampage,
// not a crawl.
func Act1Itinerary() []Leg {
	return []Leg{
		{area.BloodMoor, 1}, {area.ColdPlains, 3}, {area.StonyField, 6},
		{area.UndergroundPassageLevel1, 9}, {area.DarkWood, 10}, {area.BlackMarsh, 12},
		{area.TamoeHighland, 14}, {area.MonasteryGate, 16}, {area.OuterCloister, 17},
		{area.Barracks, 18}, {area.JailLevel1, 19}, {area.JailLevel2, 19}, {area.JailLevel3, 20},
		{area.InnerCloister, 21}, {area.Cathedral, 21}, {area.CatacombsLevel1, 22},
		{area.CatacombsLevel2, 23}, {area.CatacombsLevel3, 24}, {area.CatacombsLevel4, 25},
	}
}

type Advance struct {
	Itinerary []Leg

	idx       int // current position on the itinerary (highest adopted)
	legStart  data.Position
	j         *journey.Journey
	grid      *game.Grid // regrown grid (rooms stream in as she walks)
	regridAt  time.Time
	bestDist  int
	bestAt    time.Time
	contactAt time.Time
	clickTry  int
	heading   int // search-mode tour bearing
	// border-room cache (the room-graph read is a few hundred RPMs; 2s is plenty fresh)
	extRooms map[area.ID][]game.TileRect
	extAt    time.Time
	// Ribbon defense (run 16, measured: crossed 04:00:14, bounced back 04:00:16,
	// orbited the gate 15s): the gate zone FLICKERS area reads 1<->2, and adopting a
	// leg on one flickered read whipsawed the itinerary at the door.
	lastArea area.ID       // area seen by the previous Step (the crossing's from-side)
	pendArea area.ID       // area change waiting to be believed
	pendN    int           // consecutive reads agreeing on pendArea
	clearing bool          // adopted a crossing; pushing clear of the ribbon
	clearDoor data.Position // where the crossing fired
	clearDir  data.Position // the crossing's direction — onward is THROUGH, not back
}

func NewAdvance(legs []Leg) *Advance {
	return &Advance{Itinerary: legs, bestDist: 1 << 30, bestAt: time.Now()}
}

func (a *Advance) Name() string { return "advance" }

func (a *Advance) resetLeg(at data.Position) {
	a.j, a.grid = nil, nil
	a.bestDist, a.bestAt = 1<<30, time.Now()
	a.contactAt, a.clickTry = time.Time{}, 0
	a.legStart = at
	a.extRooms, a.extAt = nil, time.Time{}
}

// place peeks at the itinerary position for an area without mutating state.
func (a *Advance) place(ar area.ID) int {
	for i, lg := range a.Itinerary {
		if lg.Area == ar {
			return i
		}
	}
	return a.idx // off-itinerary (side cave): keep the last known leg
}

func (a *Advance) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || len(a.Itinerary) < 2 {
		return nil
	}
	// A naked amazon with a corpse out there has ONE job and it is not marching —
	// the recovery activities own her until the gear is back (run 24's near-miss).
	if s.Me.WeaponKind == "none" && s.Me.CorpseFound {
		return nil
	}
	// In town Advance IS the way out (the live border oracle reads the gate from
	// anywhere — seed-independent, unlike the hand-piloted road): bid unless the town
	// errands are waiting. Town is safe, so the HP floor drops (no regen in town; a
	// 58%-HP amazon once idled at the well forever waiting to feel better).
	if s.Me.InTown {
		if ServicesPending(s) || s.Me.HPPct < 30 {
			return nil
		}
	} else if s.Me.HPPct < 50 {
		return nil
	}
	idx := a.place(s.Me.Area)
	if idx >= len(a.Itinerary)-1 && s.Me.Area == a.Itinerary[len(a.Itinerary)-1].Area {
		return nil // the march is complete — grind the summit
	}
	if idx >= len(a.Itinerary)-1 {
		idx = len(a.Itinerary) - 2 // off-itinerary at the last leg: still route back
	}
	if s.Me.Level < a.Itinerary[idx+1].MinLevel {
		return nil // under-leveled for the next leg: grind here (Fight/Loot/Explore bid on)
	}
	urg := 0.2 // above Explore's wander; Fight preempts by class — that IS the rampage
	if !s.Me.InTown && !ExpWorthwhile(s.Me.Level, s.Me.Area) {
		urg = 0.4 // outleveled ground pays nothing: the march itself is the best exp here
	}
	return &arbiter.Demand{Who: a.Name(), Class: arbiter.ClassTravel,
		Urgency: urg,
		Commit:  arbiter.Commitment{MinHold: 4 * time.Second}}
}

func (a *Advance) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if a.lastArea == 0 {
		a.lastArea = s.Me.Area
	}
	// Adopt where the world says we are (crossings, deaths, portals all land here) —
	// but only an area STABLE for 3 consecutive reads. The ribbon flickers a single
	// read; a real crossing holds. Town is just another node: the BFS routes
	// town→BloodMoor→... like any other hop.
	if i := a.place(s.Me.Area); i != a.idx {
		if s.Me.Area == a.pendArea {
			a.pendN++
		} else {
			a.pendArea, a.pendN = s.Me.Area, 1
		}
		if a.pendN < 3 {
			return Running // hold the grant; believe nothing yet
		}
		prev := a.lastArea
		a.idx = i
		a.resetLeg(s.Me.Pos)
		a.lastArea, a.pendN = s.Me.Area, 0
		// CROSSING IS NOT ARRIVAL (Travel's ribbon law, finally ported): before the
		// next leg gets a thought, push CLEAR of the door — onward, in the crossing's
		// own measured direction (from the far side's fact through to here).
		a.clearing, a.clearDoor = true, s.Me.Pos
		a.clearDir = data.Position{X: 1}
		if ctx.Mem != nil && prev != 0 {
			var far data.Position
			if ctx.Mem.GetJSON(BorderKey(ctx.GR.MapSeed(), prev, s.Me.Area), &far) && far.X != 0 {
				a.clearDir = stepDir(far, s.Me.Pos)
			}
		}
		return Running
	}
	a.pendN = 0
	a.lastArea = s.Me.Area
	if a.clearing {
		me := s.Me.Pos
		if chebyshev(me, a.clearDoor) >= 12 {
			a.clearing = false
			return Done // adopted AND clear of the ribbon — the next leg re-bids fresh
		}
		out := data.Position{X: me.X + a.clearDir.X*14, Y: me.Y + a.clearDir.Y*14}
		slideStride(ctx, out, 1200*time.Millisecond, 1, a.Name())
		return Running
	}
	if a.idx >= len(a.Itinerary)-1 && s.Me.Area == a.Itinerary[a.idx].Area {
		return Done
	}
	next := a.Itinerary[minInt(a.idx+1, len(a.Itinerary)-1)]

	d := ctx.GR.GetData()
	hop := nextHop(d, s.Me.Area, next.Area)
	if hop == 0 {
		return Abandoned // no topological route — honest refusal
	}
	me := s.Me.Pos

	tgt, known := a.borderTarget(ctx, d, hop, me)
	if !known {
		a.search(ctx, d, me)
		return Running
	}
	ed := chebyshev(me, tgt)

	// Leg progress clock: closest-approach must improve or the leg is stuck (wall
	// pocket, unwalkable door). 60s of no progress → hand the grant back honestly.
	if ed < a.bestDist-3 {
		a.bestDist, a.bestAt = ed, time.Now()
	}
	if time.Since(a.bestAt) > 60*time.Second {
		a.resetLeg(me)
		return Abandoned
	}

	// FAR: journey to the door. The live grid marks unloaded rooms blocked, so a far
	// door often has NO plan yet — blind-stride toward it (rooms stream in on approach)
	// and regrow the grid every 8s until the planner finds the route.
	if ed > 12 {
		if a.grid == nil {
			a.grid = ctx.Grid
		}
		if a.grid == nil { // no grid at all (build failed): walk by dead reckoning
			verbs.Stride{To: tgt, Hold: 1200 * time.Millisecond, MinGain: 1}.
				Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
			return Running
		}
		goal := clampToGrid(tgt, a.grid)
		if a.j == nil || chebyshev(a.j.Goal, goal) > 8 {
			a.j = journey.New(ctx.GR, a.grid, goal, a.Name())
		}
		st := a.j.Step(ctx.M, ctx.P, ctx.Led)
		if st.State == journey.NoPath || st.State == journey.Stalled {
			if time.Since(a.regridAt) > 8*time.Second && ctx.Regrid != nil {
				a.grid = ctx.Regrid()
				a.regridAt = time.Now()
				a.j = journey.New(ctx.GR, a.grid, clampToGrid(tgt, a.grid), a.Name())
			}
			slideStride(ctx, tgt, 1200*time.Millisecond, 1, a.Name())
		}
		return Running
	}

	// NEAR the door: an entrance UNIT nearby means a warp (ritual); none means a
	// walkable border (push through). Live units decide — never a map-data flag.
	a.cross(ctx, d, me, tgt, hop)
	return Running
}

// borderTarget resolves the door toward hop: learned fact, then live border rooms.
func (a *Advance) borderTarget(ctx *Ctx, d game.Data, hop area.ID, me data.Position) (data.Position, bool) {
	if ctx.Mem != nil {
		var p data.Position
		if ctx.Mem.GetJSON(BorderKey(ctx.GR.MapSeed(), d.PlayerUnit.Area, hop), &p) && p.X != 0 {
			return p, true
		}
	}
	if time.Since(a.extAt) > 2*time.Second {
		if ext, err := ctx.GR.AdjacentLevelRooms(); err == nil {
			a.extRooms, a.extAt = ext, time.Now()
		}
	}
	if rects := a.extRooms[hop]; len(rects) > 0 {
		best, bd := data.Position{}, 1<<30
		for _, r := range rects {
			p := nearestInRect(me, r)
			if dd := chebyshev(me, p); dd < bd {
				best, bd = p, dd
			}
		}
		return best, true
	}
	return data.Position{}, false
}

// search tours the area for an unknown door: walk through any UNKNOWN entrance unit on
// sight (crossings teach the cartographer both sides), otherwise hold a persistent
// heading, turning 45° on walls — Explore's law, pointed at discovery.
func (a *Advance) search(ctx *Ctx, d game.Data, me data.Position) {
	// Opportunistic door: the nearest entrance unit not yet explained by a fact.
	var ent *data.Entrance
	bd := 40
	for i := range d.Entrances {
		e := &d.Entrances[i]
		if a.knownDoor(ctx, d, e.Position) {
			continue // already learned where this one goes (and it wasn't the hop)
		}
		if dd := chebyshev(me, e.Position); dd < bd {
			ent, bd = e, dd
		}
	}
	if ent != nil {
		a.cross(ctx, d, me, ent.Position, 0) // unknown door: no far-side fact to aim at
		return
	}
	if a.legStart == me || a.heading == 0 && a.legStart != (data.Position{}) {
		// initial bearing: away from where the leg began — outward, not backtracking
		a.heading = bearingFrom(a.legStart, me)
	}
	o := bearings[a.heading%len(bearings)]
	res := verbs.Stride{To: data.Position{X: me.X + o.X, Y: me.Y + o.Y}, MinGain: 2}.
		Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
	if res.Result != verbs.ResDone {
		a.heading++ // walled: one 45° turn, then hold the new line
	}
}

// knownDoor reports whether a fact already explains the door at pos — i.e. we recorded
// a crossing from this area within 8 tiles of it. Known doors are skipped in search:
// the point of search is the door we have NOT walked yet.
func (a *Advance) knownDoor(ctx *Ctx, d game.Data, pos data.Position) bool {
	if ctx.Mem == nil {
		return false
	}
	ad, ok := d.Areas[d.PlayerUnit.Area]
	if !ok {
		return false
	}
	for _, al := range ad.AdjacentLevels {
		var p data.Position
		if ctx.Mem.GetJSON(BorderKey(ctx.GR.MapSeed(), d.PlayerUnit.Area, al.Area), &p) && p.X != 0 &&
			chebyshev(p, pos) <= 8 {
			return true
		}
	}
	return false
}

// cross executes the door: contact-push first (walk-throughs and most warps transition
// on contact), then the spiral hover-click ritual (koolo's entrance recipe — click only
// on a confirmed entrance-class hover, UnitType 5 or 2). Ported from the machinery
// farmbot proved on this laptop: entrance units sit ~30 subtiles off mapped points,
// clicks from range never close, and a bounce must not zero the ritual timer.
func (a *Advance) cross(ctx *Ctx, d game.Data, me data.Position, tgt data.Position, hop area.ID) {
	// Steer at the LIVE entrance unit when one is near the target; else the target.
	for i := range d.Entrances {
		if chebyshev(d.Entrances[i].Position, tgt) <= 15 {
			tgt = d.Entrances[i].Position
			break
		}
	}
	if td := chebyshev(me, tgt); td > 3 {
		if td > 12 { // generous: a BOUNCE off the mouth must not zero the ritual timer
			a.contactAt, a.clickTry = time.Time{}, 0
		}
		verbs.Stride{To: tgt, Hold: 500 * time.Millisecond, MinGain: 1}.
			Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
		return
	}
	if a.contactAt.IsZero() {
		a.contactAt = time.Now()
	}
	if time.Since(a.contactAt) < 5*time.Second {
		// CONTACT PUSH — direction by knowledge ladder:
		//   1. MEASURED: the cartographer records BOTH sides of every door; if the far
		//      side of THIS door is on record, push at it — the one direction that
		//      cannot be wrong. (Run 15: the center guess aimed SOUTH-west at the west
		//      gate while the recorded far side sits NORTH-west; she rubber-banded on
		//      the fence beside the opening, back and forth, until the owner pulled F10.)
		//   2. GUESS: away from the grid's bounding-box center — doors sit on the
		//      level's edge, so outward is USUALLY across (the 03:12 fix). Fragile:
		//      streamed-in rooms drift the center.
		var through data.Position
		haveFar := false
		if hop != 0 && ctx.Mem != nil {
			var far data.Position
			if ctx.Mem.GetJSON(BorderKey(ctx.GR.MapSeed(), hop, d.PlayerUnit.Area), &far) && far.X != 0 {
				through, haveFar = far, true
			}
		}
		if !haveFar {
			from := a.legStart
			if g := a.grid; g != nil {
				from = data.Position{X: g.OffsetX + g.Width/2, Y: g.OffsetY + g.Height/2}
			} else if ctx.Grid != nil {
				from = data.Position{X: ctx.Grid.OffsetX + ctx.Grid.Width/2, Y: ctx.Grid.OffsetY + ctx.Grid.Height/2}
			}
			dir := stepDir(from, tgt)
			through = data.Position{X: tgt.X + dir.X*6, Y: tgt.Y + dir.Y*6}
		}
		// MinGain 1: a 300ms push covers 2-3 tiles by design — the default 4 branded
		// every honest push "blocked" (cosmetic, but the log must not lie).
		verbs.Stride{To: through, Hold: 300 * time.Millisecond, MinGain: 1}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
		return
	}
	// SPIRAL HOVER-CLICK for the rare click-to-open stairs.
	ctx.M.MoveStop()
	me2 := ctx.GR.GetData().PlayerUnit.Position
	bx := int(float32((tgt.X-1-me2.X)-(tgt.Y-1-me2.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((tgt.X-1-me2.X)+(tgt.Y-1-me2.Y))*9.9) + ctx.GR.GameAreaSizeY/2
	sp := spiral(a.clickTry)
	a.clickTry++
	ctx.M.AimPhysical(bx+sp.X, by+sp.Y)
	time.Sleep(120 * time.Millisecond)
	hd := ctx.GR.GetData().HoverData
	if hd.IsHovered && (hd.UnitType == 5 || hd.UnitType == 2) {
		ctx.M.BareClick(bx+sp.X, by+sp.Y)
		time.Sleep(1000 * time.Millisecond) // the click starts a walk-and-enter
	}
	if a.clickTry > 40 { // a full spiral with no confirmed hover: restart the ritual
		a.contactAt, a.clickTry = time.Time{}, 0
	}
}

// nextHop BFSes the map-data area graph for the first hop on the route cur → to.
// TOPOLOGY ONLY — positions in that data lie on this mod. Returns `to` when directly
// adjacent; 0 when no route is known.
func nextHop(d game.Data, cur, to area.ID) area.ID {
	if cur == to || len(d.Areas) == 0 {
		return 0
	}
	for _, al := range d.AdjacentLevels {
		if al.Area == to {
			return to
		}
	}
	prev := map[area.ID]area.ID{cur: cur}
	queue := []area.ID{cur}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		ad, ok := d.Areas[c]
		if !ok {
			continue
		}
		for _, al := range ad.AdjacentLevels {
			if _, seen := prev[al.Area]; seen {
				continue
			}
			prev[al.Area] = c
			if al.Area == to {
				h := to
				for prev[h] != cur {
					h = prev[h]
				}
				return h
			}
			queue = append(queue, al.Area)
		}
	}
	return 0
}

// nearestInRect clamps p into r — the closest point of the border room to her.
func nearestInRect(p data.Position, r game.TileRect) data.Position {
	x, y := p.X, p.Y
	if x < r.X {
		x = r.X
	} else if x > r.X+r.W-1 {
		x = r.X + r.W - 1
	}
	if y < r.Y {
		y = r.Y
	} else if y > r.Y+r.H-1 {
		y = r.Y + r.H - 1
	}
	return data.Position{X: x, Y: y}
}

// clampToGrid pulls a goal just inside the navigable frame so the planner can accept it;
// the through-push covers the last tiles beyond the frame.
func clampToGrid(p data.Position, g *game.Grid) data.Position {
	if g == nil {
		return p
	}
	x, y := p.X, p.Y
	if x < g.OffsetX+2 {
		x = g.OffsetX + 2
	} else if x > g.OffsetX+g.Width-3 {
		x = g.OffsetX + g.Width - 3
	}
	if y < g.OffsetY+2 {
		y = g.OffsetY + 2
	} else if y > g.OffsetY+g.Height-3 {
		y = g.OffsetY + g.Height - 3
	}
	return data.Position{X: x, Y: y}
}

// stepDir reduces a→b to a unit step {-1,0,1} per axis — the leg's travel direction.
func stepDir(a, b data.Position) data.Position {
	d := data.Position{}
	if b.X > a.X {
		d.X = 1
	} else if b.X < a.X {
		d.X = -1
	}
	if b.Y > a.Y {
		d.Y = 1
	} else if b.Y < a.Y {
		d.Y = -1
	}
	if d.X == 0 && d.Y == 0 {
		d.X = 1
	}
	return d
}

// bearingFrom picks the bearings index that points from a AWAY through b (outward).
func bearingFrom(a, b data.Position) int {
	dx, dy := b.X-a.X, b.Y-a.Y
	best, bd := 0, math.MaxFloat64
	for i, o := range bearings {
		d := math.Hypot(float64(o.X-dx*2), float64(o.Y-dy*2))
		if d < bd {
			best, bd = i, d
		}
	}
	return best
}

// spiral is koolo's archimedean pointer spiral (~3px/turn, /3 like upstream) — the
// per-attempt pixel offset for entrance hover probing.
func spiral(try int) data.Position {
	trad := float64(try*40) * math.Pi / 180.0
	x := int((4.0-2.0*trad)*math.Cos(trad)) / 3
	y := int((4.0-2.0*trad)*math.Sin(trad)) / 3
	return data.Position{X: x, Y: y}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------- Return (ClassTravel)

// Return re-enters her own town portal: the fast road back to wherever the march was
// interrupted. Bids in town only when the services are settled (belt stocked, gear
// sound) — never dive back into the fight with an empty belt.
type Return struct{}

func (r *Return) Name() string { return "return" }

func (r *Return) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.HPPct < 70 || len(s.Portals) == 0 {
		return nil
	}
	// NEVER portal a naked amazon back into the swarm that killed her (run 24: relog's
	// exit click missed and Return was next in line with the death-portal standing).
	// The corpse run — relog or reclaim — owns every moment until the gear is back.
	if s.Me.WeaponKind == "none" && s.Me.CorpseFound {
		return nil
	}
	if ServicesPending(s) {
		return nil // restock/repair first — the whole point of coming home
	}
	return &arbiter.Demand{Who: r.Name(), Class: arbiter.ClassTravel,
		Urgency: 0.35, // beats Travel's road walk: the portal IS the road
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (r *Return) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if !s.Me.InTown {
		return Done // through — the field-side activities take over
	}
	if len(s.Portals) == 0 {
		return Abandoned // it closed while we walked to it
	}
	best, bd := s.Portals[0], chebyshev(s.Me.Pos, s.Portals[0].Pos)
	for _, pt := range s.Portals[1:] {
		if d := chebyshev(s.Me.Pos, pt.Pos); d < bd {
			best, bd = pt, d
		}
	}
	if bd > 20 {
		verbs.Stride{To: best.Pos, Hold: 1200 * time.Millisecond, MinGain: 1}.
			Do(ctx.M, ctx.GR, ctx.P, ctx.Led, r.Name())
		return Running
	}
	verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, r.Name())
	return Running
}
