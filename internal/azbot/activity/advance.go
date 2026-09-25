// Advance: the campaign leg-walker — azbot's cross-area march. "Rampage across Act 1"
// decomposes into legs: in each area, resolve WHERE the door to the next stop is, journey
// there, cross, verify the area changed. Fight and Loot outrank Advance by CLASS, so the
// rampage emerges from the arbiter: she kills whatever the march walks her into.
//
// THE CROSSING EPISTEMOLOGY (measured 2026-07-19): koolo-map's world placement LIES on
// this mod — it put Blood Moor's gate EAST of town while the hand-piloted road proves it
// WEST. So: TOPOLOGY (which areas connect) comes from map data; GEOMETRY (where the door
// is) comes only from live truth, in three layers:
//  1. Learned seed facts (border.<from>.<to> in the WAL) — the cartographer records
//     every crossing she ever makes, both sides of the door, however she made it.
//  2. The live room graph's cross-level border rooms (walkable borders, readable from
//     anywhere in the area).
//  3. Nothing known → SEARCH: walk the coverage frontier (the one coverage model,
//     coverage.go — nearest-by-path unseen ground, leaning toward the projected exit)
//     and through unknown entrance units on sight — the wrong cave teaches its own
//     way back.
package activity

import (
	"fmt"
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/coverage"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/route"
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

// RoadKey: the breadcrumb trail of a PROVEN walk to this door (the owner,
// 01:47: "a more complex set of waypoints rather than a single priority move
// order"). Recorded by the executive's road recorder on every real crossing.
func RoadKey(seed uint, from, to area.ID) string {
	return fmt.Sprintf("road.%d.%d.%d", seed, int(from), int(to))
}

// LitKey: the per-CHARACTER waypoint activation ledger (the owner, 05:05:
// "aware of its waypoints instead of wasting 30 seconds on the pad"). The
// panel's list is broken on this mod; the ledger learns from honest sources:
// a TOUCH proves that pad lit, a successful RIDE proves the landing lit.
// Unknown is not lit. Activation is permanent and per character.
func LitKey(char string, ar area.ID) string {
	return fmt.Sprintf("wplit.%s.%d", char, int(ar))
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

	// Act 2 normal. These are intentionally conservative levels: they keep the
	// march moving at the character's pace, while ExpWorthwhile remains the
	// separate oracle that decides whether to fight or simply pass through.
	area.SewersLevel1Act2: 13, area.SewersLevel2Act2: 13, area.SewersLevel3Act2: 14,
	area.RockyWaste: 14, area.DryHills: 15,
	area.HallsOfTheDeadLevel1: 16, area.HallsOfTheDeadLevel2: 16, area.HallsOfTheDeadLevel3: 17,
	area.FarOasis: 17, area.MaggotLairLevel1: 17, area.MaggotLairLevel2: 17, area.MaggotLairLevel3: 18,
	area.LostCity: 19, area.ValleyOfSnakes: 19,
	area.ClawViperTempleLevel1: 19, area.ClawViperTempleLevel2: 20,
	area.HaremLevel1: 13, area.HaremLevel2: 13,
	area.PalaceCellarLevel1: 17, area.PalaceCellarLevel2: 17, area.PalaceCellarLevel3: 18,
	area.ArcaneSanctuary: 19, area.CanyonOfTheMagi: 20,
}

// ExpWorthwhile is the EXP ORACLE (the owner: "so it knows which area it should go
// for and not waste time on low level monsters"): fighting here still pays. Gap ≤3
// (tightened from 4 at the owner's 05:30 "prioritize travel and progress" — the
// level-5 barbarian brawled a level-1 moor wall to wall because gap-4 still called
// it paying ground). Outgrown ground is corridor: radius 10, march doubled.
func ExpWorthwhile(clvl int, ar area.ID) bool {
	ml, ok := areaMlvl[ar]
	if !ok {
		// The mod's levels.txt knows every area (owner, R44: "it then went to rampage
		// on the map" — Act 2 areas were missing from the table, so every Act 2 fight
		// read as worth the 45-tile hunt at clvl 30 against mlvl 16-18).
		if lv := gamedata.Get().Level(int(ar)); lv != nil && lv.MonLvl[0] > 0 {
			ml, ok = lv.MonLvl[0], true
		}
	}
	if !ok {
		return true // unknown ground: assume it pays
	}
	return clvl-ml <= 3
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

// Act2Itinerary is the quest-chain spine after Act 1. It deliberately stops at
// Canyon of the Magi: the final tomb is selected by the Horadric quest symbol,
// not by a fixed area, so treating all seven tombs as a linear route would send
// the character into the wrong dungeon. Quest-state/tomb selection can be added
// on top of this spine without teaching the marcher a false door order.
func Act2Itinerary() []Leg {
	return []Leg{
		{area.LutGholein, 1},
		{area.SewersLevel1Act2, 22}, {area.SewersLevel2Act2, 22}, {area.SewersLevel3Act2, 22},
		{area.RockyWaste, 23}, {area.DryHills, 24},
		{area.HallsOfTheDeadLevel1, 24}, {area.HallsOfTheDeadLevel2, 25}, {area.HallsOfTheDeadLevel3, 25},
		{area.FarOasis, 25},
		{area.MaggotLairLevel1, 26}, {area.MaggotLairLevel2, 26}, {area.MaggotLairLevel3, 27},
		{area.LostCity, 27}, {area.ValleyOfSnakes, 28},
		{area.ClawViperTempleLevel1, 28}, {area.ClawViperTempleLevel2, 29},
		{area.HaremLevel1, 29}, {area.HaremLevel2, 29},
		{area.PalaceCellarLevel1, 29}, {area.PalaceCellarLevel2, 30}, {area.PalaceCellarLevel3, 30},
		{area.ArcaneSanctuary, 30}, {area.CanyonOfTheMagi, 30},
	}
}

// CampaignItinerary chooses the act from the live starting area. Act 2 is
// supported now; later acts intentionally fall back to Act 1 until their quest
// gates and area-specific route are modeled instead of being guessed.
func CampaignItinerary(start area.ID) []Leg {
	if start.Act() == 2 {
		return Act2Itinerary()
	}
	return Act1Itinerary()
}

type Advance struct {
	// quest legs (quest.go): per seed.area, when the hold began and whether it ended.
	questSince   map[string]time.Time
	questDone    map[string]bool
	questForever map[area.ID]bool // quest legs ended in this save (persisted: quest.go)
	relogAsked   map[area.ID]bool // a new game was requested for this quest area
	questSeed    uint             // map seed the quest maps belong to (set in Step)
	questCap     int              // first unfinished quest leg index at or behind the frontier; -1 none (refreshed each Step)
	Itinerary    []Leg
	campaignAct  int // saved frontier namespace; prevents Act 1 indices leaking into Act 2

	idx int // current position on the itinerary (highest adopted)
	// frontier: THE CAMPAIGN'S FRONT LINE (the owner, 02:16: "should we have
	// a specific goal rather than just rampage constantly?") — the deepest
	// leg EVER reached, persisted per character and act (campaign.<char>.actN). A fresh
	// process in Cold Plains still knows the war stands at Black Marsh: the
	// target is the frontier's next leg, and the march ROUTES there through
	// learned doors instead of re-adopting wherever it happens to stand.
	frontier     int
	frontierRead bool
	tgtFromMap   bool // current border target came from the LYING map oracle (strike accounting)
	// cov: search's walker on THE ONE COVERAGE MODEL (coverage.go) — the
	// 20-box least-visited ledger it replaced lived here (the same-spots
	// loop, 21:50); seen tiles now persist per seed and area in the store.
	cov       covWalker
	burstAt   time.Time // one-shot door burst rate limit (22:44)
	huntLogAt time.Time // unfiltered-hover naming rate limit (23:00)
	legStart  data.Position
	grid      *game.Grid // regrown grid (rooms stream in as she walks)
	regridAt  time.Time
	bestDist  int
	bestAt    time.Time
	contactAt time.Time
	clickTry  int
	// memoryEntranceAt/ID/N bound the memory-first entrance click.  The live
	// entrance unit is the selector; a failed click is allowed to fall through
	// to the older hover ritual after a few attempts, but it must not spam a
	// stale unit every executive tick.
	memoryEntranceAt time.Time
	memoryEntranceID data.UnitID
	memoryEntranceN  int
	// clicks: the per-entrance click budget, reset per leg (hop) — the trace's
	// cap of 3 never reset and a door "deaf" at 1.4s was a door forever.
	clicks route.ClickBudget
	// seenArea/arrival: THE ARRIVAL DOOR (trace of 2ad968d: the stairs back up
	// to Lair L1 were taken for the L3 exit; the Far Oasis door walked into by
	// the search) — every area entry records where she landed and from where.
	seenArea area.ID
	arrival  arrivalDoor
	pickNote string // last door-pick evidence logged (dedupe)
	heading  int    // search-mode tour bearing
	// border-room cache (the room-graph read is a few hundred RPMs; 2s is plenty fresh)
	extRooms map[area.ID][]game.TileRect
	extAt    time.Time
	// Ribbon defense (run 16, measured: crossed 04:00:14, bounced back 04:00:16,
	// orbited the gate 15s): the gate zone FLICKERS area reads 1<->2, and adopting a
	// leg on one flickered read whipsawed the itinerary at the door.
	lastArea    area.ID       // area seen by the previous Step (the crossing's from-side)
	pendArea    area.ID       // area change waiting to be believed
	pendN       int           // consecutive reads agreeing on pendArea
	clearing    bool          // adopted a crossing; pushing clear of the ribbon
	clearDoor   data.Position // where the crossing fired
	clearDir    data.Position // the crossing's direction — onward is THROUGH, not back
	marchGoal   data.Position // the door currently walked at — P-5.7's forward-flee hint
	marchGoalAt time.Time
	// P-10 waypoint state: wpAt cools failed/spent ride attempts; wpWalkAt
	// bounds the walk-to-the-pad detour so an unreachable pad cannot own the
	// march forever; wpTouched cools the field TOUCH ritual per area.
	wpAt           time.Time
	wpWalkAt       time.Time
	wpHoldUntil    time.Time // failed town ride quarantines the gate march until a retry is due
	wpFailN        int
	wpTouched      map[area.ID]time.Time
	wpNoted        area.ID                // the area whose waypoint objective was last announced
	staffNoted     bool                   // the Horadric Staff was seen held (the artifact legs are over)
	townNoted      bool                   // the leaving-town diagnostic was logged this visit
	hopClickAt     time.Time              // the last portal-hop click (a 2s beat)
	hopNoted       string                 // the portal-hop objective last announced
	journalTried   map[data.Position]bool // map guesses for the journal already stood at
	journalWalkFor data.Position
	journalWalkAt  time.Time
	wingCenter     data.Position // the Sanctuary pad the wing search radiates from
	wingIdx        int
	wingAt         time.Time
	wingDone       map[int]bool // wings searched on this seed (persisted)
	arcaneSeed     uint
	// P-5.3a THE CROSSING DRIVE: between the door facts the area read is
	// NOISE — while driving, geometry is the only truth.
	driving   bool
	driveTgt  data.Position
	driveFrom data.Position
	driveAt   time.Time
	// P-5.2b ARC-LENGTH ROAD PROGRESS (the advisor, 02:40: nearest-crumb
	// targeting is a dance generator — project onto the polyline, progress
	// MONOTONICALLY, aim a lookahead point; never let ordinary navigation
	// reduce routeS).
	roadKey string
	roadS   int
	// P-10.2 THE REROUTE: field-side network ride — behind the front line
	// with a deeper pad lit, TP home and ride instead of walking conquered
	// ground. began marks an active reroute (45 s to reach the portal);
	// cool bars re-attempts; castAt/tries are the empty-tome detector.
	rerouteBegan  time.Time
	rerouteCool   time.Time
	rerouteCastAt time.Time
	rerouteTries  int
	clearAt       time.Time // clearing gets a deadline (audit finding 4)
	// far-stall conviction (audit finding 3): consecutive wasted legs at a
	// map target he never even got NEAR — the maze-interior phantom detector.
	farStallKey  string
	farStallN    int
	driveArmedAt time.Time // re-arm cooldown so a flickering seam can't ping-pong the drive
	// THE MAZE SEARCH RELAY (02:54, the UP loop: far journey NoPath through
	// unstreamed rooms, fallback clicks judged by the LYING map grid, orbit,
	// breaker, town — forever): repeated far-journey refusals hand the march
	// to the coverage search, which walks REAL streamed ground until rooms
	// arrive and the planner can route.
	farBlockN   int
	searchUntil time.Time
	// THE REROUTE JUDGES ITSELF (02:57: it TP'd him out of the UP promising
	// a deep ride, the ride delivered STONY — behind where he left — and the
	// loop got a second engine): after each completed reroute, the next field
	// area adopted must sit DEEPER than the one he left, or the reroute is
	// benched for the session.
	rerouteFromIdx int
	rerouteJudge   bool
	// intent: THE ROUTE INTENT (intent.go) — the committed destination that
	// kills the town<->gate flip. Owned here, selected through intentLeg.
	// Inert unless the deliberate flag is armed.
	intent RouteIntent
	// escalation ladder (escalate.go) — how Advance CONSUMES repeated
	// stalls / watchdog verdicts for one committed intent, climbing to a
	// different remedy each time instead of just being cooled. Flag-gated.
	lad        route.Ladder
	ladPending bool // climbed but not yet acted on (applyRung)
	// disbelief: doors the ladder convicted, each with an expiry — never
	// session-long, so a hint gets a second look as rooms stream in.
	disbelief route.Disbelief
	// door bookkeeping for the ledger: the source last marched, implausible
	// hints already reported this area, and the exploration bias a lying map
	// hint still offers (its relative place in the level).
	doorSrc  string
	doorPos  data.Position
	rejSeen  map[string]bool
	exitBias data.Position
	biasHop  area.ID
	extGrid  *game.Grid // the grid extRooms was read against; a regrow re-reads now
}

// FrontierFor is the P-5F hint: the itinerary leg the march owns at this
// level — the next leg once the level lawfully opens it, else the current.
// Exploration exists only there.
func (a *Advance) FrontierFor(level int) area.ID {
	if len(a.Itinerary) == 0 {
		return 0
	}
	ni := minInt(a.campIdx()+1, len(a.Itinerary)-1)
	if level >= a.Itinerary[ni].MinLevel {
		return a.Itinerary[ni].Area
	}
	return a.Itinerary[minInt(a.idx, len(a.Itinerary)-1)].Area
}

// MarchGoal is the door the march is walking at right now — the FORCED MARCH
// hint (P-5.7) that lets Flee retreat forward instead of orbiting the moor.
// Stale after 30 s: a hint from a dead leg must not steer a retreat.
func (a *Advance) MarchGoal() (data.Position, bool) {
	if a.marchGoal == (data.Position{}) || time.Since(a.marchGoalAt) > 30*time.Second {
		return data.Position{}, false
	}
	return a.marchGoal, true
}

func NewAdvance(legs []Leg) *Advance {
	act := 1
	if len(legs) > 0 && legs[0].Area != 0 {
		act = legs[0].Area.Act()
	}
	return &Advance{Itinerary: legs, campaignAct: act, bestDist: 1 << 30, bestAt: time.Now(), questCap: -1}
}

func (a *Advance) Name() string { return "advance" }

func (a *Advance) resetLeg(at data.Position) {
	forgetMove(a.Name()) // the next march step plans afresh
	a.grid = nil
	a.bestDist, a.bestAt = 1<<30, time.Now()
	a.contactAt, a.clickTry = time.Time{}, 0
	a.memoryEntranceAt, a.memoryEntranceID, a.memoryEntranceN = time.Time{}, 0, 0
	a.clicks.Reset()
	a.pickNote = ""
	a.legStart = at
	a.extRooms, a.extAt = nil, time.Time{}
	// NIGHT-2 AUDIT FINDING 6: roadS was monotonic per key and never reset —
	// after one traversal every later attempt at the same leg started at the
	// road's END and the proven crumb trail was skipped for the process's
	// whole life. A fresh leg replays its road from the trailhead.
	a.roadKey, a.roadS = "", 0
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
	sanctuaryClock(s, time.Now())
	if !s.Valid || len(a.Itinerary) < 2 {
		return nil
	}
	// A quest area with its quest unfinished: bid, so Step runs the quest hook
	// (R17: the capped march "reached" Maggot Lair 3 and never bid again).
	if !s.Me.InTown && s.Me.HPPct >= 50 && a.questWanted(s.Me.Area) {
		return &arbiter.Demand{Who: a.Name(), Class: arbiter.ClassTravel, Urgency: 0.4,
			Commit: arbiter.Commitment{MinHold: 2 * time.Second}}
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
	if !s.Me.InTown {
		a.townNoted = false
	}
	if s.Me.InTown {
		if ServicesPending(s) || s.Me.HPPct < 30 {
			return nil
		}
		// R59 left town with the TP tome at 0 and no "scrolls" docket line: say
		// what the docket saw each time the march takes the town (once per visit).
		if !a.townNoted {
			a.townNoted = true
			townLog(fmt.Sprintf("leaving town: tp=%d id=%d gold=%d scrollsWork=%v cooled=%v free=%d",
				s.Me.TPScrolls, s.Me.IDScrolls, s.Me.Gold, scrollWorks.Load(), servicesCooled(), s.Me.InvFree))
		}
		// A waypoint panel that stood but did not transition is a control-plane
		// failure, not permission to walk out through Blood Moor. Keep Advance's
		// travel grant alive while the bounded retry timer runs; Step holds the
		// character at the pad/town and never falls through to the gate road.
		if time.Now().Before(a.wpHoldUntil) {
			return &arbiter.Demand{Who: a.Name(), Class: arbiter.ClassTravel,
				Urgency: 0.85, Commit: arbiter.Commitment{MinHold: 2 * time.Second, SwitchMargin: 0.05}}
		}
	} else if s.Me.HPPct < 50 {
		return nil
	}
	idx := a.place(s.Me.Area)
	if a.frontier > idx {
		idx = a.frontier // the campaign's front line outranks his feet (02:16)
	}
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
	// THE MARCH HINT (owner, night 2: "driven by progress rather than run
	// around killing randomly" — the ENTIRE act): whenever the march is
	// level-lawful and bidding, Fight contracts to the 10-tile corridor
	// everywhere, not just on outleveled ground. The 45-tile hunt exists
	// only when grinding IS the mission (under-leveled: this Demand returns
	// nil above and the hint goes stale in 2 s).
	marchLawfulUntil = time.Now().Add(2 * time.Second)
	// THE RIDE OUTRANKS THE ROAD (03:38: 'she's running blood moor again' —
	// while Advance cooled between ride attempts, Travel 0.15/Return 0.35 won
	// ticks and marched her out the gate on foot). In town with a plausible
	// ride, Advance owns the exit.
	if s.Me.InTown && time.Since(a.wpAt) > 90*time.Second {
		urg = 0.45
		// Once the pad approach has begun, keep the same-class travel grant
		// stable until arrival or the bounded 60s approach deadline. Survival,
		// recovery, and combat still preempt through the arbiter's class law.
		if !a.wpWalkAt.IsZero() {
			urg = 0.75
		}
	}
	hold := 4 * time.Second
	if !a.wpWalkAt.IsZero() {
		hold = 12 * time.Second
	}
	return &arbiter.Demand{Who: a.Name(), Class: arbiter.ClassTravel,
		Urgency: urg,
		Commit:  arbiter.Commitment{MinHold: hold, SwitchMargin: 0.10}}
}

// campIdx: the campaign index — the deeper of where he STANDS and where the
// war has REACHED. Targets derive from this; routing still starts from his feet.
func (a *Advance) campIdx() int {
	c := a.idx
	if a.frontier > c {
		c = a.frontier
	}
	// An unfinished quest leg caps the campaign (quest.go): a frontier saved past
	// Maggot Lair 3 must not skip the Staff of Kings chest.
	// Targets are campIdx+1, so the cap sits one BEFORE the quest leg.
	if a.questCap >= 0 && a.questCap-1 < c {
		c = a.questCap - 1
		if c < 0 {
			c = 0
		}
	}
	return c
}

func (a *Advance) campaignKey(char string) string {
	act := a.campaignAct
	if act <= 0 {
		act = 1
	}
	return fmt.Sprintf("campaign.%s.act%d", char, act)
}

func (a *Advance) syncFrontier(ctx *Ctx) {
	if ctx.Mem == nil {
		return
	}
	ch := ctx.GR.GetData().PlayerUnit.Name
	if !a.frontierRead {
		a.frontierRead = true
		// Act 2 must never inherit the old Act 1 index: an Act 1 frontier of
		// 19 would otherwise point past the Act 2 list and suppress Advance.
		// Keep a one-time compatibility read for existing Act 1 saves.
		if !ctx.Mem.GetJSON(a.campaignKey(ch), &a.frontier) && a.campaignAct == 1 {
			ctx.Mem.GetJSON(fmt.Sprintf("campaign.%s", ch), &a.frontier)
		}
		if a.frontier < 0 || a.frontier >= len(a.Itinerary) {
			a.frontier = 0
		}
	}
	// NIGHT-2 AUDIT FINDING 9: the ratchet must be LEVEL-LAWFUL — a mis-ride
	// or row misclick into a too-deep area used to set the front line there
	// forever, and Demand's MinLevel gate then muted Advance entirely while
	// the reroute kept declaring him "behind the front".
	if a.idx > a.frontier && ctx.Snap != nil && ctx.Snap.Valid &&
		ctx.Snap.Me.Level >= a.Itinerary[a.idx].MinLevel {
		a.frontier = a.idx
		ctx.Mem.PutJSON(a.campaignKey(ch), memory.ScopeForever,
			memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("front line advanced to leg %d (%d)", a.idx, int(a.Itinerary[a.idx].Area))}, a.frontier)
	}
}

func (a *Advance) Step(ctx *Ctx) Verdict {
	if qc := a.questCapFor(ctx); qc != a.questCap {
		a.questCap = qc
		a.logQuestCap(ctx)
	}
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	a.syncFrontier(ctx)
	if s.Me.InTown && time.Now().Before(a.wpHoldUntil) {
		return Running
	}
	CarryReach(ctx) // P-5.9: the march walks with the bow out
	if a.lastArea == 0 {
		a.lastArea = s.Me.Area
	}
	a.noteArrival(ctx, s.Me.Area, s.Me.Pos, s.Me.InTown)
	// P-5.3a THE CROSSING DRIVE: between the door facts the AREA READ IS
	// NOISE — the seam flickers faster than any push escapes it, and every
	// read-driven reaction becomes an oscillator (00:58: push-pong, path 233,
	// three engines). While driving, geometry is the only truth: run at a
	// point beyond the far fact, ignore the flicker entirely; 14 tiles past
	// the near fact the reads are stable and the adopt believes at leisure.
	if a.driving {
		me := s.Me.Pos
		dot := (me.X-a.driveFrom.X)*(a.driveTgt.X-a.driveFrom.X) +
			(me.Y-a.driveFrom.Y)*(a.driveTgt.Y-a.driveFrom.Y)
		switch {
		case time.Since(a.driveAt) > 25*time.Second:
			a.driving = false // the door won this round; the leg clock judges
		case chebyshev(me, a.driveFrom) >= 14 && dot > 0:
			a.driving = false // geometrically THROUGH — reads can settle now
		default:
			crossingBracketUntil = time.Now().Add(3 * time.Second)
			NavDebug(ctx, a.driveTgt, "drive")
			moveTo(ctx, a.driveTgt, marchOpts(ctx, a.Name(), 900*time.Millisecond))
			return Running
		}
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
			return Running // hold the grant; believe nothing yet (the DRIVE
			// owns seam motion now — a read-driven push here was the east
			// engine of the 00:58 push-pong oscillator)
		}
		prev := a.lastArea
		if a.rerouteJudge && !s.Me.InTown {
			a.rerouteJudge = false
			if i <= a.rerouteFromIdx {
				// The ride delivered him AT or BEHIND where the reroute took
				// him from: the network cannot reach past his feet — every
				// future reroute would be the same backward trade. Benched.
				a.rerouteCool = time.Now().Add(60 * time.Minute)
				ctx.Led.Append(verbs.Outcome{Verb: "reroute", Holder: a.Name(), Result: verbs.ResRefused,
					Evidence: fmt.Sprintf("ride landed leg %d, left leg %d — the network rides BACKWARD; reroute benched 60m", i, a.rerouteFromIdx)})
			}
		}
		a.idx = i
		a.resetLeg(s.Me.Pos)
		// A crossing is progress: the ladder, the convictions and the door
		// bookkeeping all belonged to the area left behind.
		a.lad, a.ladPending = route.Ladder{}, false
		a.disbelief.Clear()
		a.doorSrc, a.doorPos, a.rejSeen = "", data.Position{}, nil
		a.exitBias, a.biasHop = data.Position{}, 0
		a.lastArea, a.pendN = s.Me.Area, 0
		// CROSSING IS NOT ARRIVAL (Travel's ribbon law, finally ported): before the
		// next leg gets a thought, push CLEAR of the door — onward, in the crossing's
		// own measured direction (from the far side's fact through to here).
		a.clearing, a.clearDoor, a.clearAt = true, s.Me.Pos, time.Now()
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
		// NIGHT-2 AUDIT FINDING 4: no deadline + default-east clearDir meant a
		// walled east side push-strode forever (waypoint landings have no
		// border fact, so the fallback direction fired on every ride). Five
		// seconds is every honest clear; after that the leg clock judges.
		if time.Since(a.clearAt) > 5*time.Second {
			a.clearing = false
			return Done
		}
		me := s.Me.Pos
		crossingBracketUntil = time.Now().Add(3 * time.Second) // P-5.10: the push-clear is part of the crossing
		// SIGNED forward clearance, not euclidean distance (the advisor: a
		// crossing is not clear because the area ID changed once, and plain
		// distance from the door is satisfied by running back through it
		// sideways). dot((me-door), clearDir) — forward only counts.
		if (me.X-a.clearDoor.X)*a.clearDir.X+(me.Y-a.clearDoor.Y)*a.clearDir.Y >= 12 {
			a.clearing = false
			return Done // adopted AND geometrically clear — the next leg re-bids fresh
		}
		out := data.Position{X: me.X + a.clearDir.X*14, Y: me.Y + a.clearDir.Y*14}
		// item 4: the planner knows the fences the raw click walks into.
		a.push(ctx, out, 1200*time.Millisecond)
		return Running
	}
	// A quest leg holds its area until the artifact is taken (quest.go).
	if a.questHold(ctx) {
		return Running
	}
	if a.idx >= len(a.Itinerary)-1 && s.Me.Area == a.Itinerary[a.idx].Area {
		return Done
	}
	// P-10.2 THE REROUTE (owner, 02:35 night 2: "its our bot. it should do
	// what we want it to do. not walk into blood moor while it has
	// waypoints"): in the field, BEHIND the front line, with a deeper pad
	// lit — walking conquered ground is a bug, not a journey. Cast the town
	// portal, step through; the staging ride carries him to the deepest lit
	// pad and the march resumes at the front. Calm-gated (no cast under
	// pressure), 3-minute cooldown on failure, empty-tome detector ported
	// from Withdraw.
	if s.Me.InTown && !a.rerouteBegan.IsZero() {
		// the portal carried him: reroute complete. Cool it so the stale state
		// can never cast him home again the moment he rides back out — and arm
		// the judge: the next field landing testifies for or against the ride.
		a.rerouteBegan = time.Time{}
		a.rerouteCool = time.Now().Add(3 * time.Minute)
		a.rerouteJudge = true
	}
	// NIGHT-2 AUDIT FINDING 7: rerouting while the town ride is cooling burns
	// a TP charge to bounce town→portal→field and marches anyway — the reroute
	// only makes sense when the ride it feeds is actually available.
	if !s.Me.InTown && ctx.Cap != nil && ctx.Cap.TownTP != nil && time.Now().After(a.rerouteCool) &&
		time.Since(a.wpAt) > 90*time.Second {
		if a.rerouteBegan.IsZero() {
			cur := a.place(s.Me.Area)
			deeper := area.ID(0)
			if cur < a.campIdx() && ctx.Mem != nil {
				charName := ctx.GR.GetData().PlayerUnit.Name
				for i := a.campIdx(); i > cur; i-- {
					lit := false
					ctx.Mem.GetJSON(LitKey(charName, a.Itinerary[i].Area), &lit)
					if lit {
						deeper = a.Itinerary[i].Area
						break
					}
				}
			}
			calm := true
			for _, e := range s.Enemies {
				if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 12 {
					calm = false
					break
				}
			}
			if deeper != 0 && calm {
				a.rerouteBegan = time.Now()
				a.rerouteFromIdx = cur
				a.rerouteCastAt, a.rerouteTries = time.Time{}, 0
				ctx.Led.Append(verbs.Outcome{Verb: "reroute", Holder: a.Name(), Result: verbs.ResDone,
					Evidence: fmt.Sprintf("behind the front (leg %d < %d) with pad %d lit — riding the network home", cur, a.campIdx(), int(deeper))})
			}
		}
		if !a.rerouteBegan.IsZero() {
			if time.Since(a.rerouteBegan) > 45*time.Second {
				// the portal never carried him: stand down, march on foot a while
				a.rerouteBegan = time.Time{}
				a.rerouteCool = time.Now().Add(3 * time.Minute)
			} else {
				var best percept.PortalRef
				bd := 1 << 30
				for _, pt := range s.Portals {
					if verbs.IsDeadDoor(pt.ID) {
						continue
					}
					if d := chebyshev(s.Me.Pos, pt.Pos); d < bd {
						best, bd = pt, d
					}
				}
				if bd < 1<<30 {
					if bd > 20 {
						moveTo(ctx, best.Pos, moveto.Opts{Holder: a.Name(), Purpose: moveto.Travel, MaxHold: 1200 * time.Millisecond})
					} else {
						verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
					}
					return Running
				}
				if time.Since(a.rerouteCastAt) > 2500*time.Millisecond {
					if !a.rerouteCastAt.IsZero() {
						a.rerouteTries++
					}
					if a.rerouteTries >= 3 {
						// tome proven empty — the walk it is, for a while
						a.rerouteBegan = time.Time{}
						a.rerouteCool = time.Now().Add(3 * time.Minute)
					} else {
						verbs.CastSelf{Key: ctx.Cap.TownTP.Key}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
						a.rerouteCastAt = time.Now()
					}
				}
				return Running
			}
		}
	}

	// THE ONE DESTINATION GATE (intent.go): the raw candidate is the deepest
	// level-lawful campaign leg; intentLeg holds a committed target against a
	// shallower recomputation. Flag OFF → rawIdx unchanged (legacy path).
	rawIdx := minInt(a.campIdx()+1, len(a.Itinerary)-1)
	next := a.Itinerary[a.intentLeg(ctx, s, rawIdx)]

	// P-10 THE NETWORK BEATS THE ROAD (the owner, 23:35: "she's not taking
	// the waypoint to cold plains"): in town, before any gate march, ride the
	// pad — the deepest level-lawful itinerary stop the panel shows lit. The
	// panel's own list is the activation oracle; a spent or failed attempt
	// cools 90 s and the gate march resumes unharmed.
	// PORTAL FIRST (trace 2ad968d: after the unstick TP the return rode the
	// waypoint and left her own portal standing): a live own portal in town
	// goes back to where she was — the waypoint is only the fallback.
	if s.Me.InTown && a.rerouteBegan.IsZero() && s.Me.HPPct >= 70 && !committedSeamHold() &&
		!(s.Me.WeaponKind == "none" && s.Me.CorpseFound) { // Return's own guards
		var best percept.PortalRef
		bd := 1 << 30
		fwd := minInt(a.campIdx()+1, len(a.Itinerary)-1)
		for _, pt := range s.Portals {
			if verbs.IsDeadDoor(pt.ID) || a.portalLeadsBack(s, pt, fwd) {
				continue
			}
			if dd := chebyshev(s.Me.Pos, pt.Pos); dd < bd {
				best, bd = pt, dd
			}
		}
		if route.PortalFirst(true, bd != 1<<30, time.Now().Before(hotPortalUntil), ServicesPending(s), false) {
			if bd > 20 {
				moveTo(ctx, best.Pos, moveto.Opts{Holder: a.Name(), Purpose: moveto.Travel, MaxHold: 1200 * time.Millisecond})
				return Running
			}
			ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: a.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("portal-first: own portal id=%d at %d to area %d — entering before any waypoint", int(best.ID), bd, int(best.Dest))})
			verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
			return Running
		}
	}
	if s.Me.InTown && time.Since(a.wpAt) > 90*time.Second {
		dd := ctx.GR.GetData()
		// THE MAP ORACLE NAMES THE PAD (00:04: run 88 never rode — live
		// objects stream ~2 screens and the hub cannot see the pad; the seed
		// server has known its position since attach).
		var padPos data.Position
		padDist := 1 << 30
		if ad, ok := dd.Areas[s.Me.Area]; ok {
			for _, ob := range ad.Objects {
				if ob.IsWaypoint() {
					if pd := chebyshev(s.Me.Pos, ob.Position); pd < padDist {
						padDist, padPos = pd, ob.Position
					}
				}
			}
		}
		for _, ob := range dd.Objects { // live sighting refines the map prior
			if ob.IsWaypoint() && ob.ID != 0 {
				if pd := chebyshev(s.Me.Pos, ob.Position); pd < padDist {
					padDist, padPos = pd, ob.Position
				}
			}
		}
		if padDist == 1<<30 && false {
			// P-6.2: a silent failure is a failure twice — run 89 left town on
			// foot and the log could not say why the ride never fired.
			mapObjs := 0
			if ad, ok := dd.Areas[s.Me.Area]; ok {
				mapObjs = len(ad.Objects)
			}
			ctx.Led.Append(verbs.Outcome{Verb: "waypoint", Holder: a.Name(), Result: verbs.ResRefused,
				Evidence: fmt.Sprintf("no pad known in area %d: map objects=%d, live objects=%d", int(s.Me.Area), mapObjs, len(dd.Objects))})
		}
		if padDist < 1<<30 {
			charName := ctx.GR.GetData().PlayerUnit.Name
			// KNOWN-LIT destinations always ride. UNKNOWN ones get ONE probe per
			// cooldown to BOOTSTRAP the ledger (10:26: the ledger starts empty,
			// so a pure known-lit gate never rides and he walks Blood Moor
			// forever — the ledger needs a way to fill itself). A probe that
			// opens the panel records the pad lit; a whiff just cools 90s.
			var wants, probe []area.ID
			for i := len(a.Itinerary) - 1; i > a.campIdx(); i-- {
				if s.Me.Level >= a.Itinerary[i].MinLevel {
					lit := false
					if ctx.Mem != nil {
						ctx.Mem.GetJSON(LitKey(charName, a.Itinerary[i].Area), &lit)
					}
					if lit {
						wants = append(wants, a.Itinerary[i].Area)
					} else {
						probe = append(probe, a.Itinerary[i].Area)
					}
				}
			}
			// THE STAGING RIDE (00:59, the owner: "can you tell why he went to
			// blood moor?"): wants only looked AHEAD, but the target (UP1) has
			// no waypoint and Dark Wood is unlit — so no want, no ride, and he
			// walked the whole overland trek while STONY'S LIT PAD sat one
			// ride away. The deepest lit pad at-or-behind the current leg is
			// the on-ramp to an unlit target; ride it, then march.
			for i := a.campIdx(); i >= 0; i-- {
				lit := false
				if ctx.Mem != nil {
					ctx.Mem.GetJSON(LitKey(charName, a.Itinerary[i].Area), &lit)
				}
				if lit && a.Itinerary[i].Area != s.Me.Area {
					// OFF-ROUTE PAD (R48: the Harem is next and opens from Lut
					// Gholein itself; the staging ride took him to Lost City and
					// the march turned around). The on-ramp must lie on the road:
					// its first hop from here matches the target's.
					if th, ph := nextHop(dd, s.Me.Area, next.Area), nextHop(dd, s.Me.Area, a.Itinerary[i].Area); th != 0 && ph != 0 && th != ph {
						break
					}
					wants = append(wants, a.Itinerary[i].Area)
					break
				}
			}
			// An unknown destination is not permission to click its row. On this
			// mod the panel can show a dark/lying list, and repeated blind rows
			// trap the character in town when the next quest leg has no waypoint
			// (Sewers L1 is the first Act 2 example). Touch the current pad once
			// to make its activation durable, then let the normal door march take
			// over. A ride is attempted only for a destination already proven lit
			// by a successful landing or a verified panel observation.
			// The touch must happen AT the pad (2026-07-22 act-2 log: the probe
			// fired from 49 tiles every 90 s, the verb refused "no pad within
			// 30" each time, and Lut Gholein's pad was never lit). Far → walk.
			if len(wants) == 0 && len(probe) > 0 && padDist > 8 {
				if a.walkToPad(ctx, padPos) {
					return Running
				}
			}
			if len(wants) == 0 && len(probe) > 0 && time.Since(a.wpAt) > 90*time.Second {
				a.wpAt, a.wpWalkAt = time.Now(), time.Time{}
				verbs.UseWaypoint{Want: nil}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
				return Running
			}
			if len(wants) > 0 {
				if padDist > 8 {
					if a.walkToPad(ctx, padPos) {
						return Running
					}
				} else {
					a.wpAt, a.wpWalkAt = time.Now(), time.Time{}
					o := verbs.UseWaypoint{Want: wants}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
					if ctx.Mem != nil && time.Since(verbs.LastPanelOpenAt) < 30*time.Second {
						// the panel STOOD: this pad is lit, ride or no ride (10:16)
						ctx.Mem.PutJSON(LitKey(charName, s.Me.Area), memory.ScopeForever,
							memory.Provenance{Source: "measured", Evidence: "panel stood at this pad"}, true)
					}
					if o.Result == verbs.ResDone {
						a.wpHoldUntil, a.wpFailN = time.Time{}, 0
						if ctx.Mem != nil { // the ride PROVES the landing lit
							landed := area.ID(ctx.GR.GetData().PlayerUnit.Area)
							ctx.Mem.PutJSON(LitKey(charName, landed), memory.ScopeForever,
								memory.Provenance{Source: "measured", Evidence: "rode to it"}, true)
						}
						return Running // a new area: the adopt logic takes it from here
					}
					// A failed ride is quarantined. The old code resumed the gate
					// march here, which is exactly how a dead row became a Blood Moor
					// walk. Retry from town after a short, increasing quiet window;
					// never reinterpret a panel/click failure as a valid road choice.
					a.wpFailN++
					cool := 10 * time.Second
					if o.Result == verbs.ResDeaf {
						cool = 15 * time.Second
					}
					if a.wpFailN >= 3 {
						cool = 30 * time.Second
					}
					a.wpHoldUntil = time.Now().Add(cool)
					// Make the next retry eligible as soon as the hold expires.
					a.wpAt = time.Now().Add(-91 * time.Second)
					a.wpWalkAt = time.Time{}
					ctx.Led.Append(verbs.Outcome{Verb: "waypoint-hold", Holder: a.Name(), Result: verbs.ResRefused,
						Evidence: fmt.Sprintf("ride %s; town held %s before retry (failures=%d)", o.Result.String(), cool, a.wpFailN)})
					return Running
				}
			}
		}
	}

	// P-10 ACTIVATE ON ARRIVAL (the owner, 23:45: she skipped the Stony Field
	// pad into a swarm): a pad within 25 on the field march is TOUCHED before
	// the march proceeds — the ritual is seconds, the network is forever.
	// Fight still preempts by class; this fires only while the march holds.
	if !s.Me.InTown && time.Since(a.wpTouched[s.Me.Area]) > 10*time.Minute &&
		time.Since(a.wpAt) > 60*time.Second { // she just RODE here — the pad is provably lit (03:29)
		dd := ctx.GR.GetData()
		// The map oracle names the pad here too — a 40-tile detour to light a
		// permanent network node pays for itself forever.
		mapPads := dd.Objects
		if ad, ok := dd.Areas[s.Me.Area]; ok {
			mapPads = append(append([]data.Object{}, dd.Objects...), ad.Objects...)
		}
		litHere := false
		if ctx.Mem != nil {
			ctx.Mem.GetJSON(LitKey(ctx.GR.GetData().PlayerUnit.Name, s.Me.Area), &litHere)
		}
		// THE PAD'S OWN FLAME OUTRANKS THE LEDGER (owner, 2026-09-25: "it did not
		// take the waypoint in arcane, the level literally starts on it"): the old
		// proximity witness ledgered the Sanctuary pad lit on arrival, unclicked.
		// A live pad reading anything but Opened is unlit, whatever the ledger says.
		for _, ob := range dd.Objects {
			if ob.IsWaypoint() && ob.ID != 0 && ob.Mode == mode.ObjectModeOpened {
				notePadLit(s.Me.Area, ob.Position) // the road home by waypoint (Unload)
			}
		}
		if lit, seen := padLitLive(dd.Objects); seen && !lit && litHere {
			litHere = false
			if a.wpNoted != s.Me.Area {
				ctx.Led.Append(verbs.Outcome{Verb: "waypoint", Holder: a.Name(), Result: verbs.ResRefused,
					Evidence: fmt.Sprintf("ledger said area %d lit, but the pad reads unlit — touching it", int(s.Me.Area))})
			}
		}
		// WAYPOINT OBJECTIVE (the owner, 2026-09-25: "we're missing wp so we need
		// the bot to be aware of wp taking as we progress" — Dry Hills, Halls 2,
		// Sewers 2 and the Palace Cellar were walked unlit): an area whose mod
		// levels.txt row carries a waypoint and which the ledger does not know as
		// lit makes its pad an OBJECTIVE — any distance on the map, a longer walk
		// budget — not a 70-tile convenience.
		wpObjective := !litHere && areaHasWaypoint(s.Me.Area)
		reach, budget := 70, 60*time.Second
		if wpObjective {
			reach, budget = 1<<30, 150*time.Second
			if a.wpNoted != s.Me.Area {
				a.wpNoted = s.Me.Area
				ctx.Led.Append(verbs.Outcome{Verb: "waypoint", Holder: a.Name(), Result: verbs.ResDone,
					Evidence: fmt.Sprintf("objective: area %d has an unlit waypoint — touching it before the march", int(s.Me.Area))})
			}
		}
		for _, ob := range mapPads {
			if litHere {
				break // in the ledger — no ritual needed, ever again
			}
			if ob.IsWaypoint() && chebyshev(s.Me.Pos, ob.Position) <= reach {
				// Radius 40→70, budget 30s→60s (the owner, 13:14: "it also
				// didn't take the stony waypoint, had enough time to do that"
				// — he died in the moor an hour later and had to WALK back;
				// the network node was worth any 70-tile detour that day).
				if a.wpTouched == nil {
					a.wpTouched = map[area.ID]time.Time{}
				}
				if chebyshev(s.Me.Pos, ob.Position) > 6 {
					// Bounded approach: 60 s of not reaching the pad concedes it
					// (a fenced pad must not own the march — the corner lesson).
					if a.wpWalkAt.IsZero() {
						a.wpWalkAt = time.Now()
					}
					if time.Since(a.wpWalkAt) > budget {
						a.wpTouched[s.Me.Area] = time.Now()
						a.wpWalkAt = time.Time{}
						ctx.Led.Append(verbs.Outcome{Verb: "waypoint", Holder: a.Name(), Result: verbs.ResRefused,
							Evidence: fmt.Sprintf("area %d: pad at %d tiles not reached in %s — conceded for 10 min", int(s.Me.Area), chebyshev(s.Me.Pos, ob.Position), budget)})
					} else {
						NavDebug(ctx, ob.Position, "wp-touch")
						o := marchOpts(ctx, a.Name(), 1200*time.Millisecond)
						o.AllowLeap = true // the pad is worth a leap too
						moveTo(ctx, ob.Position, o)
						return Running
					}
					break
				}
				a.wpTouched[s.Me.Area] = time.Now()
				a.wpWalkAt = time.Time{}
				// P-10 (the owner, 00:54: "use them if it could shorten the
				// travel — coming across a cold plains waypoint and teleporting
				// to stony field"): the field pad carries the same wants as the
				// town ride — the open is a RIDE when something deeper is lit,
				// and remains a touch when nothing is.
				// NIGHT-2 AUDIT FINDING 5: unfiltered deepest-first wants met
				// UseWaypoint's 3-row truncation — three padless/unlit deep
				// areas crowded out the actually-lit destination and the field
				// ride silently never fired. Lit-ledger first, like the town.
				var wants []area.ID
				for i := len(a.Itinerary) - 1; i > a.campIdx(); i-- {
					if s.Me.Level >= a.Itinerary[i].MinLevel {
						lit := false
						if ctx.Mem != nil {
							ctx.Mem.GetJSON(LitKey(ctx.GR.GetData().PlayerUnit.Name, a.Itinerary[i].Area), &lit)
						}
						if lit {
							wants = append(wants, a.Itinerary[i].Area)
						}
					}
				}
				verbs.UseWaypoint{Want: wants}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
				if ctx.Mem != nil && time.Since(verbs.LastPanelOpenAt) < 30*time.Second {
					// the panel STOOD: lit forever, ride or no ride (10:16 — a
					// deaf verdict must never eat a proven activation)
					ctx.Mem.PutJSON(LitKey(ctx.GR.GetData().PlayerUnit.Name, s.Me.Area), memory.ScopeForever,
						memory.Provenance{Source: "measured", Evidence: "panel stood at this pad"}, true)
					ctx.Led.Append(verbs.Outcome{Verb: "waypoint", Holder: a.Name(), Result: verbs.ResDone,
						Evidence: fmt.Sprintf("lit: area %d pad activated (panel stood) — in the ledger forever", int(s.Me.Area))})
				}
				return Running
			}
		}
	}

	// PORTAL HOPS (owner, 2026-09-25: "harem, arcane sanctuary, tomb thing,
	// duriel"): two Act 2 roads are objects, not level warps — the Sanctuary's
	// portal in Palace Cellar 3, and the red portal Horazon's Journal opens
	// once the Summoner is dead (Imbibe reads the journal; Fight kills him).
	if v, ok := a.portalHop(ctx, routeVia(s.Me.Area, next.Area)); ok {
		return v
	}
	d := ctx.GR.GetData()
	hop := nextHop(d, s.Me.Area, routeVia(s.Me.Area, next.Area))
	if hop == 0 {
		return Abandoned // no topological route — honest refusal
	}
	me := s.Me.Pos
	if deliberate {
		a.applyRung(ctx) // a watchdog verdict climbed the ladder while we were cooled
	}

	// THE ROAD REPLAY (P-5.2a): a proven walk outranks every derivation —
	// find the furthest crumb we can still see ourselves near, then walk the
	// chain crumb by crumb (click gait; the game handles the ground truth).
	if ctx.Mem != nil {
		rk := RoadKey(ctx.GR.MapSeed(), s.Me.Area, hop)
		var road []data.Position
		if ctx.Mem.GetJSON(rk, &road) && len(road) >= 3 {
			if a.roadKey != rk {
				a.roadKey, a.roadS = rk, 0
			}
			// Project onto the polyline: the arc position of the nearest
			// on-road point — but routeS is MONOTONIC (the advisor's law):
			// a projection behind current progress is measurement noise or
			// a dance about to happen; never adopt it.
			arc, bestArc, bestD := 0, -1, 1<<30
			for i := 0; i < len(road); i++ {
				if i > 0 {
					arc += chebyshev(road[i-1], road[i])
				}
				if dd := chebyshev(me, road[i]); dd < bestD {
					bestD, bestArc = dd, arc
				}
			}
			total := arc
			if bestD <= 25 { // on or near the road at all
				if bestArc > a.roadS {
					a.roadS = bestArc // forward progress only
				}
				if a.roadS >= total-4 {
					// trailhead reached: the drive/cross owns the seam now
				} else {
					// LOOKAHEAD: the point ~12 tiles of arc ahead of progress.
					look, acc := road[len(road)-1], 0
					for i := 1; i < len(road); i++ {
						acc += chebyshev(road[i-1], road[i])
						if acc >= a.roadS+12 {
							look = road[i]
							break
						}
					}
					NavDebug(ctx, look, "road")
					// P-2.11(4): the road is the best possible runway — leap
					// along it every 6s and walk the gaps (11:20: "run there
					// and leap it").
					o := marchOpts(ctx, a.Name(), 1100*time.Millisecond)
					o.AllowLeap = true
					moveTo(ctx, look, o)
					return Running
				}
			}
		}
	}

	tgt, known := a.borderTarget(ctx, d, hop, me)
	if !known {
		// UNKNOWN DOOR (relay R1, run w): never march a zero or a phantom —
		// explore. The search walks the coverage frontier and any unknown
		// entrance on sight; a lying map hint still biases which frontier.
		a.seedSearchBias(ctx, me, hop)
		a.search(ctx, d, me, hop)
		return Running
	}
	a.marchGoal, a.marchGoalAt = tgt, time.Now() // P-5.7: the retreat may lean on this
	if deliberate && a.intent.Active() {
		a.intent.Goal = tgt // refresh the committed door hint as she walks
	}
	ed := chebyshev(me, tgt)
	NavDebug(ctx, tgt, "march") // the owner's window (logs/nav.png + nav line)
	// P-5.10 THE CROSSING BRACKET: holding a door contracts the hunt to
	// contact — lingerers at the mouth cannot bid the actuator away. Re-armed
	// each Step near the door; lapses 3 s after the march lets go.
	if ed <= 15 {
		crossingBracketUntil = time.Now().Add(3 * time.Second)
	}

	// Leg progress clock: closest-approach must improve or the leg is stuck (wall
	// pocket, unwalkable door). 60s of no progress → hand the grant back honestly.
	if ed < a.bestDist-3 {
		a.bestDist, a.bestAt = ed, time.Now()
	}
	if time.Since(a.bestAt) > 60*time.Second {
		// THE ESCALATION LADDER (escalate.go, item 3): a committed intent that
		// eats a leg climbs rungs instead of repeating the same plan. Rung 0
		// (REPLAN) throws away the stale journey and keeps the grant — one fresh
		// plan before conceding. Rungs 1+ fall through to the proven strike /
		// hand-back below (ALTERNATE door → PORTAL reroute → the next itinerary
		// option), so the hard-won crossing epistemology stays the backstop.
		if deliberate && a.intent.Active() {
			r := a.escalate(ctx.Led, a.ladderKey(), fmt.Sprintf("60s stall at (%d,%d)", tgt.X, tgt.Y))
			a.applyRung(ctx)
			if r == route.Replan {
				a.bestAt = time.Now() // one fresh plan gets its own clock
				return Running
			}
			// rung ALTERNATE and above: acted on, then the strike + Abandon path
			// below still runs as the backstop.
		}
		// A WASTED LEG IS A STRIKE (the owner, 21:17: "trying to traverse
		// dark wood from a wrong place, just stuck there"): the map oracle
		// names a position, known=true, he marches the lie, the 60s leash
		// resets the leg, and he marches the SAME lie again — forever, while
		// the live-entrance search never gets its turn. A map-sourced target
		// that eats a full leg with zero progress earns a breaker-grade
		// strike; two strikes and CursedNear disbelieves the exit, handing
		// the march to search() and the real stairs.
		if a.tgtFromMap && a.bestDist <= 30 {
			// A strike requires ARRIVAL (01:23: the rule convicted the honest
			// Black Marsh exit for a blocked ROUTE — overworld doors validated
			// truthful; only grinding NEAR the door testifies against the
			// door itself. A far stall is the journey's failure: reset the
			// leg, convict nobody).
			NoteBreakerSite(a.marchGoal)
			// Strikes persist to the WAL (21:52: the forced swap reset the
			// in-process count and the disbelief had to be re-earned — a
			// proven lie must stay proven across processes).
			if ctx.Mem != nil {
				n := 0
				key := fmt.Sprintf("badexit.%d.%d.%d", ctx.GR.MapSeed(), int(s.Me.Area), int(hop))
				ctx.Mem.GetJSON(key, &n)
				n++
				ctx.Mem.PutJSON(key, memory.ScopeSeed,
					memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("wasted 60s leg #%d at (%d,%d)", n, a.marchGoal.X, a.marchGoal.Y)}, n)
			}
			ctx.Led.Append(verbs.Outcome{Verb: "nav", Holder: a.Name(), Result: verbs.ResDeaf,
				Evidence: fmt.Sprintf("map-oracle exit (%d,%d) ate a 60s leg — strike recorded", a.marchGoal.X, a.marchGoal.Y)})
		} else if a.tgtFromMap {
			// NIGHT-2 AUDIT FINDING 3, LIVE IN THE UP AT 02:47: a maze
			// interior's map target sits 478 tiles from truth — he can never
			// ARRIVE at it, so the arrival-gated strike never fires and he
			// marches the same phantom forever (stall → abandon → shuffle →
			// repeat). Three consecutive far-stalls at the same pair = one
			// arrival-grade strike: the phantom is barred and the coverage
			// search finally gets its turn to find the REAL door.
			fk := fmt.Sprintf("%d.%d", int(s.Me.Area), int(hop))
			if fk != a.farStallKey {
				a.farStallKey, a.farStallN = fk, 0
			}
			a.farStallN++
			if a.farStallN >= 3 {
				a.farStallN = 0
				NoteBreakerSite(a.marchGoal)
				if ctx.Mem != nil {
					n := 0
					key := fmt.Sprintf("badexit.%d.%d.%d", ctx.GR.MapSeed(), int(s.Me.Area), int(hop))
					ctx.Mem.GetJSON(key, &n)
					n += 2 // straight to conviction: three whole legs is proof enough
					ctx.Mem.PutJSON(key, memory.ScopeSeed,
						memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("3 far-stall legs at phantom (%d,%d) — maze-interior lie convicted", a.marchGoal.X, a.marchGoal.Y)}, n)
				}
				ctx.Led.Append(verbs.Outcome{Verb: "nav", Holder: a.Name(), Result: verbs.ResDeaf,
					Evidence: fmt.Sprintf("map-oracle exit (%d,%d) ate 3 far legs — phantom convicted, the search owns this door", a.marchGoal.X, a.marchGoal.Y)})
			}
		}
		a.resetLeg(me)
		return Abandoned
	}

	// FAR: journey to the door. The live grid marks unloaded rooms blocked, so a far
	// door often has NO plan yet — blind-stride toward it (rooms stream in on approach)
	// and regrow the grid every 8s until the planner finds the route.
	// THE DOOR BAND IS PLANNED BY NOBODY (P-5.5a, measured 00:41: path 147,
	// net 1 — the orbit): within 25 of a border target the live grid LIES
	// (the unstreamed far side reads as wall), each regrid shifts the clamped
	// goal, and every re-plan walks a fresh circle. The mouth is strode at
	// directly — cross() owns the band; the wall-slide handles the posts.
	if ed > 12 {
		// THE MAZE SEARCH RELAY: while armed, the coverage search owns the
		// march — real streamed ground, the unseen frontier, no map lies.
		if time.Now().Before(a.searchUntil) {
			a.search(ctx, d, me, hop)
			return Running
		}
		// P-5.5b RETIRED AT 01:45 (nav.png: the map grid does not even COVER
		// her position — koolo-map world placement LIES on this mod, exactly
		// as this file's header has always said: topology only, geometry
		// never). The live grid marches; it lies too, but only about the
		// unstreamed, and rooms stream in on approach.
		if a.grid == nil {
			a.grid = ctx.Grid
		}
		// P-2.11(4): the march leaps too — ~12 tiles along the route to the door
		// every 6s when the pool affords it; the journey walks the gaps. With no
		// grid at all (build failed) MoveTo walks by dead reckoning; a door off
		// the grid's frame is planned to the frame's edge.
		st := moveToOn(ctx, a.grid, tgt, moveto.Opts{Holder: a.Name(), Purpose: moveto.Travel, Arrive: 5,
			AllowLeap: true, MaxHold: 1200 * time.Millisecond})
		if a.grid == nil {
			return Running
		}
		if stalled(st) {
			a.farBlockN++
			if a.farBlockN >= 4 {
				// Four refusals through unstreamed rooms: the planner is blind
				// here (a maze). 20s of coverage search streams the rooms in.
				a.farBlockN = 0
				a.searchUntil = time.Now().Add(20 * time.Second)
				ctx.Led.Append(verbs.Outcome{Verb: "nav", Holder: a.Name(), Result: verbs.ResRefused,
					Evidence: fmt.Sprintf("far journey blind at (%d,%d) — the maze search takes the march 20s", tgt.X, tgt.Y)})
				a.search(ctx, d, me, hop)
				return Running
			}
			if time.Since(a.regridAt) > 8*time.Second && ctx.Regrid != nil {
				a.grid = ctx.Regrid()
				a.regridAt = time.Now()
				forgetMove(a.Name())
			}
			// No route (yet): the click gait — the game's pathfinder walks it.
			moveToOn(ctx, a.grid, tgt, marchOpts(ctx, a.Name(), 1200*time.Millisecond))
		} else {
			a.farBlockN = 0
		}
		return Running
	}

	// NEAR the door: an entrance UNIT nearby means a warp (ritual); none means a
	// walkable border (push through). Live units decide — never a map-data flag.
	a.cross(ctx, d, me, tgt, hop)
	return Running
}

// mapWalk reports whether p is walkable in either area's MAP grid — the seed
// server's complete geometry, the only truth that spans a border seam (the
// live grid lies there: unstreamed rooms read as wall).
func mapWalk(d game.Data, a1, a2 area.ID, p data.Position) bool {
	for _, ar := range []area.ID{a1, a2} {
		if ad, ok := d.Areas[ar]; ok && ad.Grid != nil {
			rp := ad.Grid.RelativePosition(p)
			if rp.X >= 0 && rp.Y >= 0 && rp.X < ad.Grid.Width && rp.Y < ad.Grid.Height &&
				ad.Grid.CollisionGrid[rp.Y][rp.X] == game.CollisionTypeWalkable {
				return true
			}
		}
	}
	return false
}

// borderTarget resolves the door toward hop: learned fact, map hint (or the live
// entrance beside it), then live border rooms — the first one that is PLAUSIBLE
// for the level she stands in (route.PickDoor). None plausible is "unknown", and
// the caller explores. Relay R1, run w: the 42->56 hint was (15080,6580), 9450
// tiles outside Dry Hills, and nothing checked it — the march pinned for 20 min.
// Evaluated every call, never cached: a hint rejected now is re-judged next tick,
// and the border rooms are re-read as soon as the grid regrows.
func (a *Advance) borderTarget(ctx *Ctx, d game.Data, hop area.ID, me data.Position) (data.Position, bool) {
	a.tgtFromMap = false // stamped true only when a map-sourced door wins
	cur := d.PlayerUnit.Area
	var cands []route.Candidate
	if ctx.Mem != nil {
		var p data.Position
		if ctx.Mem.GetJSON(BorderKey(ctx.GR.MapSeed(), cur, hop), &p) && p.X != 0 {
			cands = append(cands, route.Candidate{Src: "fact", Pos: p})
		}
	}
	// THE MAP NAMES EVERY EXIT — entrances included (01:10: she toured the
	// Stony border wall-hugging in search of stairs the seed server had
	// named since attach). Mapped exits lie by a few tiles (the farmbot
	// law), but the door band's drive and the entrance ritual absorb that.
	var mapHint data.Position
	goal := a.doorGoal(ctx, d, hop) // destination facts: arrival door, neighbours, wrong landings
	if ad, ok := d.Areas[cur]; ok {
		for _, lv := range ad.AdjacentLevels {
			if lv.Area == hop && (lv.Position.X != 0 || lv.Position.Y != 0) {
				if CursedNear(lv.Position) {
					break // the map exit cost two portals here — a proven lie
					// on this seed; the live rooms or the search find truth
				}
				if ctx.Mem != nil { // durable strikes (21:52): a proven lie stays proven
					n := 0
					ctx.Mem.GetJSON(fmt.Sprintf("badexit.%d.%d.%d", ctx.GR.MapSeed(), int(cur), int(hop)), &n)
					if n >= 2 {
						break
					}
				}
				// A map hint that sits on a learned door to a DIFFERENT neighbor is
				// not a plausible target for this hop. This catches the Sewer 3
				// return-stairs collision: the map put 49->50 on the exact fact
				// already measured for 49->48, so marching there simply ascended.
				if a.mapHintConflictsWithKnownDoor(ctx, d, hop, lv.Position) || goal.NearForeignFact(lv.Position) {
					break
				}
				mapHint = lv.Position
				// The map position is a topology hint, not a clickable doorway.
				// On this mod the entrance unit can be tens of tiles away from
				// that hint (Jail 1 -> Jail 2 measured 63 tiles away).  If the
				// live snapshot already exposes an entrance close to the hint,
				// steer to that unit so the planner does not pin itself against
				// the false map point and never reach cross().
				// DESTINATION-AWARE (trace 2ad968d, Lair L2): the entrance nearest
				// a false hint was the stairs BACK UP — never pick a door known or
				// suspected to lead anywhere but the hop.
				ent, note, ok := route.ChooseEntrance(entranceCands(d.Entrances), lv.Position, 96, goal)
				if ok {
					cands = append(cands, route.Candidate{Src: "map-entrance", Pos: ent.Pos})
				}
				if note != a.pickNote {
					a.pickNote = note
					ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: a.Name(), Result: verbs.ResDone,
						Evidence: fmt.Sprintf("%d->%d %s", int(cur), int(hop), note)})
				}
				cands = append(cands, route.Candidate{Src: "map", Pos: lv.Position})
				break
			}
		}
	}
	live := liveFrame(ctx.Grid)
	now := time.Now()
	doubted := func(p data.Position) bool { return a.disbelief.Has(p, now) }
	pick := route.PickDoor(cands, me, live, doubted)
	if !pick.Known {
		// The live room graph is a fallback for walkable borders when no learned
		// fact or trustworthy map hint exists. Its coordinates are frame-sensitive
		// on some streamed rooms, so it must not outrank a measured door/map point.
		if ctx.Grid != a.extGrid {
			a.extGrid, a.extAt = ctx.Grid, time.Time{} // rooms streamed in: re-read now
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
			rp := route.PickDoor([]route.Candidate{{Src: "rooms", Pos: best}}, me, live, doubted)
			rp.Rejected = append(pick.Rejected, rp.Rejected...)
			pick = rp
		}
	}
	a.noteRejects(ctx, d, hop, me, live, pick.Rejected, mapHint)
	if !pick.Known {
		a.doorSrc, a.doorPos = "", data.Position{}
		return data.Position{}, false
	}
	a.tgtFromMap = pick.Door.Src == "map" || pick.Door.Src == "map-entrance"
	if pick.Door.Src != a.doorSrc || chebyshev(pick.Door.Pos, a.doorPos) > 8 {
		a.doorSrc, a.doorPos = pick.Door.Src, pick.Door.Pos
		ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: a.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("%d->%d src=%s at (%d,%d), %d from me", int(cur), int(hop),
				pick.Door.Src, pick.Door.Pos.X, pick.Door.Pos.Y, chebyshev(me, pick.Door.Pos))})
	}
	return pick.Door.Pos, true
}

// liveFrame is the current level's live DrlgLevel placement — the executive's
// grid is built on it, so its bounds are the frame (Empty when there is none).
func liveFrame(g *game.Grid) route.Rect {
	if g == nil {
		return route.Rect{}
	}
	return route.Rect{X: g.OffsetX, Y: g.OffsetY, W: g.Width, H: g.Height}
}

func mapFrame(d game.Data, ar area.ID) route.Rect {
	if ad, ok := d.Areas[ar]; ok && ad.Grid != nil {
		return route.Rect{X: ad.Grid.OffsetX, Y: ad.Grid.OffsetY, W: ad.Grid.Width, H: ad.Grid.Height}
	}
	return route.Rect{}
}

// noteRejects ledgers each implausible door hint once per area — with where it
// actually falls (the current level's map frame, the hop's, or neither), so a
// laptop log says WHICH lie it was — and keeps a lying map hint's relative
// place in the level as the exploration bias.
func (a *Advance) noteRejects(ctx *Ctx, d game.Data, hop area.ID, me data.Position, live route.Rect, rej []route.Candidate, mapHint data.Position) {
	cur := d.PlayerUnit.Area
	for _, c := range rej {
		mf, hf := mapFrame(d, cur), mapFrame(d, hop)
		if c.Src == "map" && c.Pos == mapHint && live.Contains(me) {
			if b, ok := route.Project(c.Pos, mf, live); ok {
				a.exitBias = b
			}
		}
		k := fmt.Sprintf("%d.%s.%d.%d", int(hop), c.Src, c.Pos.X, c.Pos.Y)
		if a.rejSeen[k] {
			continue
		}
		if a.rejSeen == nil {
			a.rejSeen = map[string]bool{}
		}
		a.rejSeen[k] = true
		ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: a.Name(), Result: verbs.ResRefused,
			Evidence: fmt.Sprintf("%d->%d src=%s (%d,%d) implausible: %d from me, live frame %s; in map %d %s=%v, in map %d %s=%v — unknown door, exploring",
				int(cur), int(hop), c.Src, c.Pos.X, c.Pos.Y, chebyshev(me, c.Pos), live,
				int(cur), mf, mf.Contains(c.Pos), int(hop), hf, hf.Contains(c.Pos))})
	}
}

// seedSearchBias points the coverage search's bearing once per hop at the
// projected map exit — the "which side of the level" a lying hint still knows.
func (a *Advance) seedSearchBias(ctx *Ctx, me data.Position, hop area.ID) {
	if a.exitBias == (data.Position{}) || a.biasHop == hop || a.legStart == me {
		return
	}
	a.biasHop = hop
	a.heading = bearingFrom(me, a.exitBias) + len(bearings) // non-zero: search reads 0 as unset
	ctx.Led.Append(verbs.Outcome{Verb: "door", Holder: a.Name(), Result: verbs.ResDone,
		Evidence: fmt.Sprintf("exploring for %d toward projected map exit (%d,%d), bearing %d",
			int(hop), a.exitBias.X, a.exitBias.Y, a.heading%len(bearings))})
}

// mapHintConflictsWithKnownDoor rejects a topology hint whose geometry is
// already occupied by a measured border to another adjacent area. It is a
// narrow anti-reversal guard, not a general map veto: a hint is still allowed
// when no contradictory memory fact exists.
func (a *Advance) mapHintConflictsWithKnownDoor(ctx *Ctx, d game.Data, hop area.ID, hint data.Position) bool {
	if ctx.Mem == nil || hint == (data.Position{}) {
		return false
	}
	// d.Areas is the first candidate list, but the mod's map graph can omit the
	// very reverse stair we are defending against. Include the campaign spine
	// as a second list so a learned 49->48 fact is still compared while routing
	// toward the nominal 49->50 hop.
	candidates := make(map[area.ID]struct{}, len(d.Areas)+len(a.Itinerary))
	if ad, ok := d.Areas[d.PlayerUnit.Area]; ok {
		for _, al := range ad.AdjacentLevels {
			candidates[al.Area] = struct{}{}
		}
	}
	for _, lg := range a.Itinerary {
		candidates[lg.Area] = struct{}{}
	}
	for other := range candidates {
		if other == hop || other == d.PlayerUnit.Area {
			continue
		}
		var learned data.Position
		if ctx.Mem.GetJSON(BorderKey(ctx.GR.MapSeed(), d.PlayerUnit.Area, other), &learned) &&
			learned.X != 0 && chebyshev(learned, hint) <= 20 {
			return true
		}
	}
	return false
}

// search tours the area for an unknown door: walk through any UNKNOWN entrance unit on
// sight (crossings teach the cartographer both sides), otherwise hold a persistent
// heading, turning 45° on walls — Explore's law, pointed at discovery.
func (a *Advance) search(ctx *Ctx, d game.Data, me data.Position, hop area.ID) {
	// Opportunistic door: the nearest entrance unit not yet explained by a fact.
	var ent *data.Entrance
	bd := 40
	goal := a.doorGoal(ctx, d, hop)
	for i := range d.Entrances {
		e := &d.Entrances[i]
		if a.knownDoor(ctx, d, e.Position) {
			continue // already learned where this one goes (and it wasn't the hop)
		}
		// Never the way back (trace 2ad968d: the search walked out to Far
		// Oasis through an unrecorded arrival door) nor a wrong landing.
		if skip, _ := route.SearchSkip(e.Name, e.Position, goal); skip {
			continue
		}
		if dd := chebyshev(me, e.Position); dd < bd {
			ent, bd = e, dd
		}
	}
	if ent != nil {
		a.cross(ctx, d, me, ent.Position, hop) // unknown door: a wrong landing is still recorded
		return
	}
	if a.legStart == me || a.heading == 0 && a.legStart != (data.Position{}) {
		// initial bearing: away from where the leg began — outward, not backtracking
		a.heading = bearingFrom(a.legStart, me)
	}
	// THE ONE COVERAGE MODEL (the owner: "In the cave it goes to already-
	// explored bits"): the search walks the same frontier picker Explore does
	// — nearest-by-path unexplored ground, leaning toward the projected exit
	// (or the tour bearing a FAIL-LEG turned) — instead of its old 20-box
	// least-visited ledger. Coverage survives the town trip and the restart.
	if st, ok := a.cov.step(ctx, a.searchBias(me), a.Name()); ok && st == coverage.Exploring {
		return
	}
	// No coverage knowledge (no grid, no tracker) or no reachable frontier
	// left: hold the heading, one 45° turn per wall — the doors seen so far
	// were all crossed above, so keep walking the level's rim.
	o := bearings[a.heading%len(bearings)]
	st := moveTo(ctx, data.Position{X: me.X + o.X, Y: me.Y + o.Y}, moveto.Opts{Holder: a.Name(), Purpose: moveto.Travel, MinGain: 2})
	if wallTurned(st) {
		a.heading++ // walled: one 45° turn, then hold the new line
	}
}

// searchBias is the search's lean for the coverage picker: the projected map
// exit when a lying hint still knows the side, else the tour bearing (set
// outward at the leg start, turned 135° by each FAIL-LEG) as a far point.
func (a *Advance) searchBias(me data.Position) coverage.Bias {
	if a.exitBias != (data.Position{}) {
		return coverage.Bias{At: a.exitBias, Key: fmt.Sprintf("exit.%d", int(a.biasHop)), OK: true}
	}
	if a.heading != 0 {
		o := bearings[a.heading%len(bearings)]
		return coverage.Bias{At: data.Position{X: me.X + o.X*4, Y: me.Y + o.Y*4},
			Key: fmt.Sprintf("bearing.%d", a.heading%len(bearings)), OK: true}
	}
	return coverage.Bias{}
}

// ExploreBias is Explore's lean (the wander on the frontier leg): the
// projected exit if known, else the fresh march goal. Keys change when the
// hint does, which re-picks the frontier goal.
func (a *Advance) ExploreBias() (data.Position, string, bool) {
	if a.exitBias != (data.Position{}) {
		return a.exitBias, fmt.Sprintf("exit.%d", int(a.biasHop)), true
	}
	if g, ok := a.MarchGoal(); ok {
		return g, fmt.Sprintf("march.%d.%d", g.X/20, g.Y/20), true
	}
	return data.Position{}, "", false
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

// memoryEntranceClick is the entrance equivalent of the background pickup path:
// choose a live unit by its memory ID, refresh that same unit immediately before
// acting, project its fresh memory position, click once, and judge only the area
// postcondition. It deliberately does not read HoverData or an entrance's
// IsHovered flag. D2R still needs a mouse input to activate a doorway, but the
// cursor is now only the actuator; it is no longer the target selector.
//
// The attempt is bounded to three memory-derived points and one attempt per
// cooldown. A false click never falls through to a screen-wide sweep in the same
// tick, which keeps misses from turning into random ground clicks or focus-stealing
// hardware clicks. It returns attempted=true when it consumed this tick.
func (a *Advance) memoryEntranceClick(ctx *Ctx, start, hop area.ID, targetID data.UnitID, target data.Position) (attempted, changed bool) {
	if targetID == 0 || target == (data.Position{}) {
		return false, false
	}
	// The budget is per leg (hop) and per entrance: a new leg starts fresh.
	if !a.clicks.Allow(int(hop), targetID) || time.Since(a.memoryEntranceAt) < 1200*time.Millisecond {
		return false, false
	}
	dd := ctx.GR.GetData()
	if dd.PlayerUnit.Area != start {
		return false, dd.PlayerUnit.Area != 0
	}
	// Unit identity is the guard against a recycled/stale position. The live
	// position, not the snapshot passed into cross, is the only point projected.
	ent, ok := dd.Entrances.FindByID(targetID)
	if !ok || ent.Position == (data.Position{}) || chebyshev(ent.Position, target) > 20 {
		return false, false
	}
	me := dd.PlayerUnit.Position
	bx := int(float32((ent.Position.X-me.X)-(ent.Position.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((ent.Position.X-me.X)+(ent.Position.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2
	// Entrance art is taller than its base unit. Each retry uses a fixed,
	// memory-derived offset; this is a tiny label-depth compensation, not a
	// cursor hunt over arbitrary screen space.
	offsets := []data.Position{{X: 0, Y: -28}, {X: 0, Y: -56}, {X: -18, Y: -28}}
	off := offsets[a.clicks.N%len(offsets)]
	a.clicks.N++
	a.memoryEntranceID, a.memoryEntranceN = targetID, a.clicks.N
	a.memoryEntranceAt = time.Now()
	cx, cy := bx+off.X, by+off.Y
	if !verbs.ClickableLogical(ctx.GR, cx, cy) {
		ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResRefused,
			Evidence: fmt.Sprintf("memory entrance id=%d projected outside game area (%d,%d)", int(targetID), cx, cy)})
		return true, false
	}
	// ClickLeft is the same background-safe world click used by Pickup. It sends
	// the message to D2R and never calls FocusGame/RealMenuClick.
	ctx.M.ClickLeft(cx, cy)
	// THE VERDICT IS 3s (trace: a click logged deaf at 1.4s changed area ~2s
	// later): poll the area until it changes or the window lapses.
	clickAt := time.Now()
	for {
		time.Sleep(140 * time.Millisecond)
		nowArea := ctx.GR.GetData().PlayerUnit.Area
		done, _ := route.DoorVerdict(clickAt, time.Now(), nowArea != 0 && nowArea != start)
		if !done {
			continue
		}
		if nowArea == 0 || nowArea == start {
			break
		}
		if hop != 0 && nowArea != hop {
			// A transition is still real, but naming the wrong-way landing makes
			// the route defect diagnosable instead of calling it a successful hop.
			ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResRefused,
				Evidence: fmt.Sprintf("memory entrance id=%d landed wrong area %d (wanted %d) — recorded, never retaken", int(targetID), int(nowArea), int(hop))})
			a.recordWrongDoor(ctx, start, nowArea, ent.Position)
		} else {
			ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("memory entrance id=%d transitioned %d -> %d", int(targetID), int(start), int(nowArea))})
		}
		return true, true
	}
	ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResDeaf,
		Evidence: fmt.Sprintf("memory entrance click deaf id=%d name=%d pos=(%d,%d) offset=(%d,%d) hop=%d",
			int(targetID), int(ent.Name), ent.Position.X, ent.Position.Y, off.X, off.Y, int(hop))})
	return true, false
}

// cross executes the door: contact-push first (walk-throughs and most warps transition
// on contact), then a memory-first entrance click and finally the bounded legacy
// hover ritual. Ported from the machinery farmbot proved on this laptop: entrance
// units sit ~30 subtiles off mapped points, clicks from range never close, and a
// bounce must not zero the ritual timer.
func (a *Advance) cross(ctx *Ctx, d game.Data, me data.Position, tgt data.Position, hop area.ID) {
	// Steer at the LIVE entrance unit when one is near the target; else the target.
	var memoryID data.UnitID
	for i := range d.Entrances {
		if chebyshev(d.Entrances[i].Position, tgt) <= 15 {
			tgt = d.Entrances[i].Position
			memoryID = d.Entrances[i].ID
			break
		}
	}
	if td := chebyshev(me, tgt); !route.ContactReady(td, false) {
		if td > 12 { // generous: a BOUNCE off the mouth must not zero the ritual timer
			a.contactAt, a.clickTry = time.Time{}, 0
		}
		// item 4: walk the APPROACH to the door through the planner — the
		// contact push below (warp ritual) is a separate step. THE DEAD ZONE
		// (trace 2ad968d: pinned 19s, 0 cross lines): a stairs unit's own tile
		// is unwalkable and the planner reports Arrived at the nearest walkable
		// 4-5 tiles off — that IS contact; fall through to the push/click.
		st := moveTo(ctx, tgt, moveto.Opts{Holder: a.Name(), Purpose: moveto.Travel, Arrive: 3, MaxHold: 500 * time.Millisecond, Fallback: true})
		if st.State != moveto.Arrived {
			return
		}
		me = ctx.GR.GetData().PlayerUnit.Position
		if !route.ContactReady(chebyshev(me, tgt), true) {
			return
		}
		ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("arrived %d from door (%d,%d) (%s) — contact", chebyshev(me, tgt), tgt.X, tgt.Y, st.Why)})
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
			// A WARP'S FAR SIDE LIVES IN ANOTHER COORDINATE FRAME (22:55, the
			// seven-defeat endgame: the UP cabin's far fact sits at (79xx,83xx)
			// — 2,700 tiles from the door — and the drive committed runs toward
			// warp-space garbage every approach; for walkable borders the same
			// math is exactly right, which is why it survived this long). Only
			// a SAME-FRAME far fact (≤100) may steer the push.
			if ctx.Mem.GetJSON(BorderKey(ctx.GR.MapSeed(), hop, d.PlayerUnit.Area), &far) && far.X != 0 &&
				chebyshev(far, tgt) <= 100 {
				through, haveFar = far, true
			}
		}
		if haveFar && time.Since(a.driveArmedAt) > 20*time.Second {
			// P-5.3a: a known far side arms THE DRIVE — one committed run at a
			// point beyond it, deaf to the flickering reads, instead of 300ms
			// read-reactive pushes that the seam turns into an oscillator.
			// NIGHT-2 AUDIT FINDING 1: this branch set only driveAt and armed
			// NOTHING — the drive was dead code and every learned door idled.
			// THE RE-ARM COOLDOWN (05:19, owner asleep, the Monastery-Gate pin:
			// at a flickering 7<->26 seam the drive RE-ARMED on every 4s area
			// flip and ping-ponged him across the boundary for minutes, WAL
			// pollution and zero progress. One arm per 20s; a seam that flips
			// faster falls through to the journey + maze-search relay, which
			// walks him OFF the seam on real streamed ground.
			me := d.PlayerUnit.Position
			dir := stepDir(me, through)
			a.driving = true
			a.driveFrom = me
			a.driveTgt = data.Position{X: through.X + dir.X*10, Y: through.Y + dir.Y*10}
			a.driveAt = time.Now()
			a.driveArmedAt = time.Now()
			return
		}
		if !haveFar {
			from := a.legStart
			if g := a.grid; g != nil {
				from = data.Position{X: g.OffsetX + g.Width/2, Y: g.OffsetY + g.Height/2}
			} else if ctx.Grid != nil {
				from = data.Position{X: ctx.Grid.OffsetX + ctx.Grid.Width/2, Y: ctx.Grid.OffsetY + ctx.Grid.Height/2}
			}
			dir := stepDir(from, tgt)
			// THE OWNER'S FOOTSTEPS OUTRANK THE CENTER GUESS: the road fact's
			// last leg is the PROVEN approach vector through this doorway —
			// the owner walked it twice; push through along the same line.
			if hop != 0 && ctx.Mem != nil {
				var road []data.Position
				if ctx.Mem.GetJSON(RoadKey(ctx.GR.MapSeed(), d.PlayerUnit.Area, hop), &road) && len(road) >= 2 {
					dir = stepDir(road[len(road)-2], road[len(road)-1])
				}
			}
			through = data.Position{X: tgt.X + dir.X*8, Y: tgt.Y + dir.Y*8}
		}
		// MinGain 1: a 300ms push covers 2-3 tiles by design — the default 4 branded
		// every honest push "blocked" (cosmetic, but the log must not lie). Through
		// MoveTo: a far side beyond the grid's frame is one stride off its edge;
		// a walled line takes the best clear step instead of rubbing the wall.
		moveTo(ctx, through, moveto.Opts{Holder: a.Name(), Purpose: moveto.Travel, Arrive: 1, MaxHold: 300 * time.Millisecond, Fallback: true})
		return
	}
	// SPIRAL HOVER-CLICK for the rare click-to-open stairs — STAIRS ONLY: a
	// walkable border has nothing to click, and the spiral's ground clicks
	// were walking her in circles at the seam (the orbit's second engine).
	hasEnt := false
	for i := range d.Entrances {
		if chebyshev(d.Entrances[i].Position, tgt) <= 15 {
			hasEnt = true
			break
		}
	}
	// A LEARNED DOOR IS AN ENTRANCE, whatever d.Entrances says (22:35, the
	// FIVE-DEFEAT autopsy: the UP stairs never appear in the entrance unit
	// list on this mod, so the click ritual — the ONE method proven at cave
	// mouths — was gated off at this door every single time; all he ever did
	// there was seam-leap and re-arm. The unit list lies by omission; the
	// owner's own crossing at this spot is stronger evidence than its silence.)
	learnedDoor := false
	if hop != 0 && ctx.Mem != nil {
		var p data.Position
		if ctx.Mem.GetJSON(BorderKey(ctx.GR.MapSeed(), d.PlayerUnit.Area, hop), &p) && p.X != 0 &&
			chebyshev(p, tgt) <= 8 {
			hasEnt, learnedDoor = true, true
		}
	}
	if !hasEnt {
		// P-2.11(5) THE SEAM LEAP (11:20: "it kind of missed the passage to
		// it, just hugs the walls"): a walkable border's far side is
		// unstreamed and the live grid calls it wall — the MAP grid is the
		// only truth that spans the seam (mapWalk's whole purpose). Leap a
		// few tiles PAST the border line where the map vouches; the wall-hug
		// dies mid-air.
		if canVault(ctx, ctx.Snap) && time.Since(travelVaultAt) >= 6*time.Second {
			if dd := chebyshev(me, tgt); dd >= 1 {
				land := data.Position{X: tgt.X + (tgt.X-me.X)*5/dd, Y: tgt.Y + (tgt.Y-me.Y)*5/dd}
				if mapWalk(d, d.PlayerUnit.Area, hop, land) {
					travelVaultAt = time.Now()
					verbs.Vault{To: land, Key: ctx.Cap.Vault.Key, SkillID: int(ctx.Cap.Vault.Skill)}.
						Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
				}
			}
		}
		a.contactAt = time.Time{} // re-arm the contact push instead
		return
	}
	ctx.M.MoveStop()
	me2 := ctx.GR.GetData().PlayerUnit.Position
	if memoryID != 0 && chebyshev(me2, tgt) <= 10 {
		if attempted, changed := a.memoryEntranceClick(ctx, d.PlayerUnit.Area, hop, memoryID, tgt); attempted {
			if changed {
				return
			}
			// One bounded memory click per executive turn. Let the fresh read
			// decide whether the next attempt is due; do not immediately launch
			// a hover sweep after a background click.
			return
		}
	}
	bx := int(float32((tgt.X-1-me2.X)-(tgt.Y-1-me2.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((tgt.X-1-me2.X)+(tgt.Y-1-me2.Y))*9.9) + ctx.GR.GameAreaSizeY/2
	// THE ONE-SHOT ENTRY (22:44: Fight steals the actuator at the door — the
	// drip of one spiral probe per grant never reached burst try 2 in six
	// approaches). At a LEARNED warp door the whole hardware burst runs NOW,
	// inline, once per 20s: nine real clicks base-to-arch inside one held
	// step, judged by the area like everything else.
	if learnedDoor && chebyshev(me2, tgt) <= 10 && time.Since(a.burstAt) > 20*time.Second {
		a.burstAt = time.Now()
		// THE WIDE HOVER HUNT (22:49 screenshot: the UP "mouth" is a CABIN —
		// the clickable violet doorway renders ~300px from our projection of
		// the learned fact, which marks where the area FLIPPED, not where the
		// door DRAWS. Nine blind clicks decorated the wrong wall. The game
		// itself names the door on hover: sweep a wide grid, believe only an
		// entrance-type hover, click THAT — posted first, hardware on deafness).
		found := false
		// ±320 wide, -200 high (22:53: the screenshot's doorway sat ~295px
		// LEFT of the projection; the first sweep capped at ±200 and missed
		// by width alone).
		for _, dy := range []int{-40, -80, 0, -120, -160, -200, 40} {
			for dx := -320; dx <= 320 && !found; dx += 32 {
				cx, cy := bx+dx, by+dy
				if !verbs.ClickableLogical(ctx.GR, cx, cy) {
					continue
				}
				ctx.M.AimPhysical(cx, cy)
				time.Sleep(45 * time.Millisecond)
				// THE ENTRANCE'S OWN FLAG (23:03, the last oracle: entrances
				// carry per-unit IsHovered like items do — the pickup lesson —
				// and their POSITION field can lie while the flag tells truth;
				// HoverData never named this door because entrance hover lives
				// HERE, position-blind).
				for ei := range ctx.GR.GetData().Entrances {
					if ctx.GR.GetData().Entrances[ei].IsHovered {
						found = true
						ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResDone,
							Evidence: fmt.Sprintf("entrance flag HOVERED at offset (%d,%d) — clicking the door", dx, dy)})
						ctx.M.BareClick(cx, cy)
						time.Sleep(1600 * time.Millisecond)
						if ctx.GR.GetData().PlayerUnit.Area != d.PlayerUnit.Area {
							return
						}
						realWorldClick(ctx, cx, cy)
						time.Sleep(1400 * time.Millisecond)
						if ctx.GR.GetData().PlayerUnit.Area != d.PlayerUnit.Area {
							return
						}
					}
				}
				if found {
					break
				}
				hd := ctx.GR.GetData().HoverData
				if hd.IsHovered && hd.UnitType != 5 && hd.UnitType != 2 && time.Since(a.huntLogAt) > 3*time.Second {
					// NAME EVERY HOVER (23:00: the sweep found "nothing" — or
					// found the door under a unit type we refuse; the filter
					// must not hide what the cursor actually sees).
					a.huntLogAt = time.Now()
					ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResRefused,
						Evidence: fmt.Sprintf("hunt hovered UNFILTERED unit type=%d id=%d at offset (%d,%d)", hd.UnitType, int(hd.UnitID), dx, dy)})
				}
				if hd.IsHovered && (hd.UnitType == 5 || hd.UnitType == 2) {
					found = true
					ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResDone,
						Evidence: fmt.Sprintf("hover hunt FOUND the door graphic at offset (%d,%d) type=%d", dx, dy, hd.UnitType)})
					ctx.M.BareClick(cx, cy)
					time.Sleep(1500 * time.Millisecond)
					if ctx.GR.GetData().PlayerUnit.Area != d.PlayerUnit.Area {
						return // THE DOOR OPENED
					}
					realWorldClick(ctx, cx, cy) // posted click deaf: one hardware click at the PROVEN spot
					time.Sleep(1200 * time.Millisecond)
					if ctx.GR.GetData().PlayerUnit.Area != d.PlayerUnit.Area {
						return
					}
				}
			}
			if found {
				break
			}
		}
		if !found {
			// THE OWNER'S ACTUAL INPUT (23:00, "back and forth — something is
			// wrong"): they don't force-walk in — they CLICK, and the GAME'S
			// pathfinder (which knows the true walkability our grid doesn't)
			// carries them through the doorway. Plain ground clicks at the
			// door and just past it, exactly their gesture; the game does the
			// walking.
			// The 23:01 screenshot, HIM standing ON the fact: the fact projects
			// onto HIMSELF (every click aimed at his own feet) while the cabin
			// doorway renders ~290px WEST — the fact marks where the area
			// FLIPS, past the visual door. Click the DOORWAY, not the fact.
			for _, off := range []data.Position{{X: -290, Y: -10}, {X: -260, Y: -30},
				{X: -310, Y: 10}, {X: 0, Y: 0}, {X: -20, Y: 10}} {
				// Blind ground clicks up to ~310px out: pulled back along the
				// ray from her until they are world, never HUD (step 11).
				cx, cy, ok := verbs.ClampClickLogical(ctx.GR, bx+off.X, by+off.Y)
				if !ok {
					continue
				}
				ctx.M.BareClick(cx, cy)
				time.Sleep(1400 * time.Millisecond)
				if ctx.GR.GetData().PlayerUnit.Area != d.PlayerUnit.Area {
					return // WALKED IN — the game pathed him through
				}
			}
			ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResDeaf,
				Evidence: fmt.Sprintf("hover hunt at learned door (%d,%d): no entrance hover, 3 ground clicks deaf — arming the walk-through push", tgt.X, tgt.Y)})
			// Grid-blind force pushes as the last rung (the grid veto autopsy).
			a.contactAt = time.Now()
			return
		}
	}
	sp := spiral(a.clickTry)
	a.clickTry++
	spOK := verbs.ClickableLogical(ctx.GR, bx+sp.X, by+sp.Y) // the spiral never probes the HUD
	if spOK {
		ctx.M.AimPhysical(bx+sp.X, by+sp.Y)
	}
	time.Sleep(120 * time.Millisecond)
	hd := ctx.GR.GetData().HoverData
	if spOK && hd.IsHovered && (hd.UnitType == 5 || hd.UnitType == 2) {
		ctx.M.BareClick(bx+sp.X, by+sp.Y)
		time.Sleep(1000 * time.Millisecond) // the click starts a walk-and-enter
	}
	// P-2.11 AT THE DOOR (the owner, 2026-07-20: "jump through doors or
	// something"): when the spiral grinds without an entry, one leap AT the
	// doorstep — clutter between us and the mouth is scenery to a leap, and
	// the landing re-rolls the approach angle for every click that follows.
	if a.clickTry == 12 && canVault(ctx, ctx.Snap) {
		land := tgt
		if land.X > me2.X {
			land.X -= 2
		} else if land.X < me2.X {
			land.X += 2
		}
		if land.Y > me2.Y {
			land.Y -= 2
		} else if land.Y < me2.Y {
			land.Y += 2
		}
		verbs.Vault{To: land, Key: ctx.Cap.Vault.Key, SkillID: int(ctx.Cap.Vault.Skill)}.
			Do(ctx.M, ctx.GR, ctx.P, ctx.Led, a.Name())
	}
	// THE STATIC-CLICK LAW, finally challenged (03:31: three tiles from the
	// Underground Passage, a minute of stucks, a breaker TP from the very
	// doorstep — the farmbot era FENCED every cave because id=0 stairs never
	// answered posted clicks). The mod's menus read hardware input only;
	// its warp mouths may too. Every 15th spiral try: ONE focused real
	// click at the mouth, judged like everything else by the area change.
	// Bursts at 2,10,18,26,34 (22:24: the door breaker's 10-stuck leash fired
	// BEFORE try 6 ever came — the PROVEN entry method lost the race to the
	// rescue four times today; the owner hand-entered three of them).
	if a.clickTry%8 == 2 {
		// ACROSS THE ARCH, not one pixel (03:42: center-projection hardware
		// clicks fired and nothing entered — a cave's clickable region often
		// lives in the arch above the base). Nine real clicks, base to arch.
		for _, off := range []data.Position{{X: 0, Y: 0}, {X: 0, Y: -30}, {X: 0, Y: -60},
			{X: -30, Y: -30}, {X: 30, Y: -30}, {X: -30, Y: 0}, {X: 30, Y: 0},
			{X: -20, Y: -55}, {X: 20, Y: -55}} {
			fired, focused := realWorldClick(ctx, bx+off.X, by+off.Y)
			if !focused {
				// The RealEsc law (20:54): with the owner at the desktop these
				// hardware clicks were landing in THEIR windows — the "deaf
				// mouth" was partly clicks that never reached the game. Waits
				// in writing; the burst re-fires on a later spiral pass.
				ctx.Led.Append(verbs.Outcome{Verb: "cross", Holder: a.Name(), Result: verbs.ResRefused,
					Evidence: "arch burst: foreground refused — the owner holds the desktop"})
				break
			}
			if !fired {
				continue // this arch point is HUD, not world
			}
			time.Sleep(700 * time.Millisecond)
			if ctx.GR.GetData().PlayerUnit.Area != d.PlayerUnit.Area {
				return // THE CAVE OPENED — the adopt logic takes it from here
			}
		}
	}
	if a.clickTry > 40 { // a full spiral with no confirmed hover: restart the ritual
		a.contactAt, a.clickTry = time.Time{}, 0
	}
}

// realWorldClick fires ONE hardware click (RealMenuClick) at a LOGICAL world-aim
// point — the verbs' projection space. RealMenuClick takes SCREENSHOT px and
// divides by the display scale; the door rituals used to hand it logical px, so
// every "hardware click at the PROVEN spot" landed at ~80% of the aim (1/1.25,
// toward the client's top-left) — nowhere near the door. verbs.ShotOfLogical
// applies the same map the posted click's cursor goes through. A point on the
// HUD is never fired. fired: the click went out; focused: false only when the
// game could not be foregrounded (the RealEsc law — the caller stops).
func realWorldClick(ctx *Ctx, x, y int) (fired, focused bool) {
	if !verbs.ClickableLogical(ctx.GR, x, y) {
		return false, true
	}
	sx, sy := verbs.ShotOfLogical(ctx.GR, x, y)
	if !ctx.M.RealMenuClick(sx, sy) {
		return false, false
	}
	return true, true
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

// nearestLiveEntrance refines a map-sourced border hint with the game's own
// entrance unit when one is already visible.  Map coordinates identify the
// neighboring area reliably, but their geometry can be offset on the modded
// client; the live unit is the only useful click/approach point.
func nearestLiveEntrance(entrances data.Entrances, target data.Position, maxDist int) (data.Entrance, bool) {
	best := data.Entrance{}
	bestDist := maxDist + 1
	for _, ent := range entrances {
		if ent.ID == 0 || ent.Position == (data.Position{}) {
			continue
		}
		if d := chebyshev(ent.Position, target); d < bestDist {
			best, bestDist = ent, d
		}
	}
	return best, bestDist <= maxDist
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

// push routes a short travel push (the post-crossing clear) through MoveTo
// (item 4: the raw clickStride/verbs.Stride in clearing and cross bypassed
// Journey and walked into the mod's fences). The deliberate marcher walks it
// by planned strides; otherwise by the click gait on the route. A planner
// refusal still pushes: the click (or nav's best clear step) carries it.
func (a *Advance) push(ctx *Ctx, tgt data.Position, hold time.Duration) moveto.Status {
	if !deliberate {
		return moveTo(ctx, tgt, marchOpts(ctx, a.Name(), hold))
	}
	st := moveTo(ctx, tgt, moveto.Opts{Holder: a.Name(), Purpose: moveto.Travel, MaxHold: hold, Fallback: true})
	if stalled(st) && !st.Issued {
		st = moveTo(ctx, tgt, marchOpts(ctx, a.Name(), hold))
	}
	return st
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
	if !s.Valid || !s.Me.InTown || s.Me.HPPct < 70 {
		return nil
	}
	// THE SEAM HYSTERESIS (item 2): a committed beyond-town march plus a fresh
	// crossing means the deliberate marcher owns the seam right now — Return's
	// portal ride back to the field must not undo a crossing inside the window
	// (the town<->gate bounce). It resumes the moment the window lapses.
	if committedSeamHold() {
		return nil
	}
	// P-2.4: a dead door is ABSENT — never bid on a portal that provably
	// does not open, or Return spins in town clicking a refusal forever.
	live := false
	for _, pt := range s.Portals {
		if !verbs.IsDeadDoor(pt.ID) {
			live = true
			break
		}
	}
	if !live {
		return nil
	}
	// P-2.10: a HOT portal aims back at the jaws that forced the breakout —
	// let it expire; the march re-enters by the gate on its own ground.
	if time.Now().Before(hotPortalUntil) {
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
	var best percept.PortalRef
	bd := 1 << 30
	for _, pt := range s.Portals {
		if verbs.IsDeadDoor(pt.ID) {
			continue // P-2.4: absent
		}
		if d := chebyshev(s.Me.Pos, pt.Pos); d < bd {
			best, bd = pt, d
		}
	}
	if bd == 1<<30 {
		return Abandoned // it closed while we walked to it — or died as a door
	}
	if bd > 20 {
		moveTo(ctx, best.Pos, moveto.Opts{Holder: r.Name(), Purpose: moveto.Travel, MaxHold: 1200 * time.Millisecond})
		return Running
	}
	verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, r.Name())
	return Running
}

// walkToPad walks the town pad BY PLANNER (P-5.5 — the bare slide ground 50 s
// into the town fence at 00:37): journey first, slide only as the stall
// fallback. Bounded: 60 s of no arrival concedes (returns false, wpAt cools)
// and hands the march back to the gate.
func (a *Advance) walkToPad(ctx *Ctx, padPos data.Position) bool {
	if a.wpWalkAt.IsZero() {
		a.wpWalkAt = time.Now()
	}
	if time.Since(a.wpWalkAt) > 60*time.Second {
		a.wpAt, a.wpWalkAt = time.Now(), time.Time{}
		return false
	}
	// By planner first (MoveTo drops the trip on a refusal); a refused plan —
	// or no grid at all — walks by the click gait.
	o := moveto.Opts{Holder: a.Name(), Purpose: moveto.Travel, Arrive: 4, MaxHold: 1200 * time.Millisecond}
	if ctx.Grid == nil {
		o = marchOpts(ctx, a.Name(), 1200*time.Millisecond)
		o.Arrive = 4
	}
	if st := moveTo(ctx, padPos, o); stalled(st) {
		o = marchOpts(ctx, a.Name(), 1200*time.Millisecond)
		o.Arrive = 4
		moveTo(ctx, padPos, o)
	}
	return true
}

// areaHasWaypoint: the mod's levels.txt gives the area a waypoint (towns excluded:
// their pads are lit by walking in). Without the tables: unknown, no objective.
func areaHasWaypoint(ar area.ID) bool {
	db := gamedata.Get()
	if db == nil {
		return false
	}
	lv := db.Level(int(ar))
	return lv != nil && lv.Waypoint >= 0 && !lv.IsTown
}

// portalLeadsBack: an own portal whose destination is an itinerary leg behind
// the next one he may lawfully march to (R47: the owner's old portal stood in
// town to Maggot Lair 3 — the finished staff leg — while the Harem was next;
// portal-first rode it straight back into 298 maggots). Unknown destinations,
// and any portal while the next leg is above his level (the camp grind), keep
// the old rule.
func (a *Advance) portalLeadsBack(s *percept.Snapshot, pt percept.PortalRef, fwd int) bool {
	if pt.Dest == 0 || fwd < 0 || fwd >= len(a.Itinerary) || s.Me.Level < a.Itinerary[fwd].MinLevel {
		return false
	}
	for i := 0; i < fwd; i++ {
		if a.Itinerary[i].Area == pt.Dest {
			return true
		}
	}
	return false
}

// padLitLive reads the live pad's own state: an activated waypoint stands
// Opened (mode 2 — measured on Lut Gholein's lit pad); an unlit one idles.
// seen=false: no live pad (unit ID set) in the object list.
func padLitLive(obs []data.Object) (lit, seen bool) {
	for _, ob := range obs {
		if ob.IsWaypoint() && ob.ID != 0 {
			return ob.Mode == mode.ObjectModeOpened, true
		}
	}
	return false, false
}

// townLog: a Demand-time diagnostic line through the loot brain's log sink (the
// executive wires it at startup; nil = silent).
func townLog(msg string) {
	theLoot.mu.Lock()
	defer theLoot.mu.Unlock()
	theLoot.say("town", "why", msg)
}
