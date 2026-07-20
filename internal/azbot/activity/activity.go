// Package activity: azbot's behaviors as explicit state machines with honest verdicts.
// Every activity BIDS (a Demand) each cycle and, when granted, runs ONE bounded Step.
// There are no behaviors outside this contract (LAW 1).
package activity

import (
	"fmt"
	"sort"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

type Verdict uint8

const (
	Running Verdict = iota
	Done
	Abandoned
)

type Ctx struct {
	M    *motor.Motor
	GR   *game.MemoryReader
	P    *percept.Perceptor
	Led  *verbs.Ledger
	Grid *game.Grid
	Cap  *combat.Capability
	Snap *percept.Snapshot
	// SwapKey: the weapon-swap key (W) — the bowzon dance's hinge.
	SwapKey byte
	// InvKey: the inventory-panel key (I) — the Equip service's door.
	InvKey byte
	// Regrid rebuilds the live navigation grid mid-area (rooms stream in as she walks;
	// a grid built at the border knows nothing of the far exit). Owned by the executive.
	Regrid func() *game.Grid
	// Mem: the WAL fact store — Advance reads the cartographer's border facts here.
	Mem *memory.Store
}

type Activity interface {
	Name() string
	// Demand returns nil when the activity has nothing to bid this cycle.
	Demand(s *percept.Snapshot) *arbiter.Demand
	// Step runs one bounded slice while holding the grant.
	Step(ctx *Ctx) Verdict
}

// losClear walks the ARROW'S line across the live grid: any known wall cell on the
// segment blocks the shot (the owner: "she tries to shoot through walls"). Unknown
// terrain (LowPriority) stays shootable — only loaded, real walls refuse.
// LosClear is the exported face of losClear for the executive's per-tick
// Walled stamp (WARNING 10).
func LosClear(g *game.Grid, a, b data.Position) bool { return losClear(g, a, b) }

// raisers: the RESURRECTING families (P-1.15 — the raiser dies first). World
// knowledge by npc ID — stable under the mod's name scrambling, and not class
// knowledge, so it lives here rather than priors.go. Extend as acts open up.
var raisers = map[npc.ID]bool{
	npc.FallenShaman:      true,
	npc.CarverShaman:      true,
	npc.CarverShaman2:     true,
	npc.DevilkinShaman:    true,
	npc.DevilkinShaman2:   true,
	npc.DarkShaman:        true,
	npc.DarkShaman2:       true,
	npc.WarpedShaman:      true,
	npc.HollowOne:         true, // the mummy lords raise their dead too
	npc.Guardian:          true,
	npc.Unraveler:         true,
	npc.Unraveler2:        true,
	npc.HoradrimAncient:   true,
	npc.RatManShaman:      true,
	npc.FetishShaman:      true,
	npc.FlayerShaman:      true,
	npc.FlayerShaman2:     true,
	npc.SoulKillerShaman:  true,
	npc.SoulKillerShaman2: true,
	npc.StygianDollShaman: true,
	npc.StygianDollShaman2: true,
}

func losClear(g *game.Grid, a, b data.Position) bool {
	if g == nil {
		return true
	}
	steps := chebyshev(a, b)
	for i := 1; i < steps; i++ {
		p := data.Position{X: a.X + (b.X-a.X)*i/steps, Y: a.Y + (b.Y-a.Y)*i/steps}
		rp := g.RelativePosition(p)
		if rp.X < 0 || rp.Y < 0 || rp.X >= g.Width || rp.Y >= g.Height {
			continue
		}
		if g.CollisionGrid[rp.Y][rp.X] == game.CollisionTypeNonWalkable {
			return false
		}
	}
	return true
}

// clickStride is THE FISH CURE for travel movement: click the carrot, let the
// game's own pathfinder walk — the only mover that knows the mod's invented
// fences (panels exist in NO grid; the whole night of 2026-07-20 fought walls
// the game routes around free). Force-slide remains the fallback for a
// refused or dead click. Travel contexts only — combat footwork keeps the
// force-move edge (a click near a monster is an attack).
func clickStride(ctx *Ctx, to data.Position, hold time.Duration, who string) {
	// P-5.9 for the brawler (the owner, 04:33): travel by RIGHT-click with
	// the melee skill selected — the walk itself engages what it meets.
	key := byte(0)
	if brawlerMode && !ctx.Snap.Me.InTown && ctx.Cap != nil && ctx.Cap.Contact != nil && ctx.Snap.Me.MPPct > 10 {
		key = ctx.Cap.Contact.Key // FIELD ONLY: a Double Swing near Charsi is not a greeting (04:36)
	}
	o := verbs.ClickMove{To: to, Hold: hold, CombatKey: key}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, who)
	if o.Result != verbs.ResDone {
		slideStride(ctx, to, hold, 1, who)
	}
}

// slideStride is a stride that refuses to rub walls: a blocked line retries once
// rotated +45°, then −45° — the wall-slide. For the PLANLESS strides (escapes,
// sidesteps, blind pushes); planned movement belongs to Journey. The owner: "it
// tries to walk through walls often, one of the main reasons it stops."
func slideStride(ctx *Ctx, to data.Position, hold time.Duration, minGain int, who string) verbs.Outcome {
	me := ctx.Snap.Me.Pos
	o := verbs.Stride{To: to, Hold: hold, MinGain: minGain}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, who)
	if o.Result != verbs.ResBlocked {
		return o
	}
	dx, dy := to.X-me.X, to.Y-me.Y
	for _, r := range []data.Position{
		{X: me.X + (dx-dy)*7/10, Y: me.Y + (dx+dy)*7/10}, // rotated +45°
		{X: me.X + (dx+dy)*7/10, Y: me.Y + (dy-dx)*7/10}, // rotated −45°
	} {
		o = verbs.Stride{To: r, Hold: hold, MinGain: minGain}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, who+"/slide")
		if o.Result != verbs.ResBlocked {
			return o
		}
	}
	return o
}

// gridWalkable: one cell's truth from the live grid (optimistic on unknown ground,
// same posture as the planner).
func gridWalkable(g *game.Grid, p data.Position) bool {
	if g == nil {
		return true
	}
	rp := g.RelativePosition(p)
	if rp.X < 0 || rp.Y < 0 || rp.X >= g.Width || rp.Y >= g.Height {
		return true
	}
	return g.CollisionGrid[rp.Y][rp.X] != game.CollisionTypeNonWalkable
}

// kiteAway opens the gap away from `from`, preferring bearings the GRID says are
// open: straight anti-vector, then ±45°. Returns false when CORNERED — the caller
// must fight, not donate more strides to the wall (the owner: "she ran to a corner
// and it's taking hits, not doing much" — the blind anti-vector kite herded her
// into masonry and kept pushing).
func kiteAway(ctx *Ctx, s *percept.Snapshot, from data.Position, hold time.Duration) bool {
	me := s.Me.Pos
	dx, dy := me.X-from.X, me.Y-from.Y
	cands := []data.Position{
		{X: me.X + dx*2, Y: me.Y + dy*2},
		{X: me.X + (dx - dy), Y: me.Y + (dx + dy)}, // rotated +45°
		{X: me.X + (dx + dy), Y: me.Y + (dy - dx)}, // rotated -45°
	}
	for _, c := range cands {
		if gridWalkable(ctx.Grid, c) {
			verbs.Stride{To: c, Hold: hold, MinGain: 2}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, "kite")
			return true
		}
	}
	return false
}

func chebyshev(a, b data.Position) int {
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

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// carryReachAt rate-limits the march's swap-back (shared across holders: the
// walk is one walk whoever holds it).
var carryReachAt time.Time

// hotPortalUntil — P-2.10 THE PORTAL REMEMBERS THE JAWS: a breakout entered
// with a CROWD at the mouth marks the standing portal HOT; Return leaves hot
// portals alone and the march re-enters by the gate on its own ground.
var hotPortalUntil time.Time

// CrossingHot: P-5.10's bracket, readable by the executive — while the march
// holds a door, the watchdog's fixed-vector escape flings are the pendulum
// (03:26 nav: NW fling + walk back, forever); footwork belongs to the drive.
func CrossingHot() bool { return time.Now().Before(crossingBracketUntil) }

// MarkPortalHot: P-2.10 for callers outside the package (the pocket breaker,
// 02:28: it TP'd her out of the pen and Return rode the standing portal
// straight back INTO the pen — "pops a tp, goes in, immediately comes back").
func MarkPortalHot(d time.Duration) { hotPortalUntil = time.Now().Add(d) }

// potionCoolUntil: P-4.2a escape clause — an abandoned potion trip cools its
// docket line so a dead vendor cell cannot gate the march (the 23:55 idle).
var potionCoolUntil time.Time

// crossingBracketUntil: P-5.10 — while the march holds a door, the hunt
// contracts to CONTACT (5): passage lingerers cannot bid the actuator away
// from the crossing. Advance re-arms this each Step near its door.
var crossingBracketUntil time.Time

// brawlerMode: P-2.-2 THE BRAWLER'S CREED — capability truth set by the
// executive after every calibration: no REACH and no THROW tool means
// footwork is denial. Demands read it because their signature sees only
// the snapshot.
var brawlerMode bool

// SetBrawler is called by the executive after every capability calibration.
func SetBrawler(b bool) { brawlerMode = b }

// breakerSites: CURSED GROUND (the owner, 05:40: "TP every time it reaches
// that place... refrain from rerunning the same exact path"). Every pocket-
// breaker firing marks the spot; a MAP-sourced door target near two firings
// is disbelieved — the map exit is the documented liar on this mod, and a
// lie that has cost two portals is retired in favor of the search tour,
// which finds the REAL border by walking and records it as measured fact.
var breakerSites []data.Position

// NoteBreakerSite is called by the executive on every pocket-breaker firing.
func NoteBreakerSite(p data.Position) {
	breakerSites = append(breakerSites, p)
	if len(breakerSites) > 24 {
		breakerSites = breakerSites[len(breakerSites)-24:]
	}
}

// CursedNear: two or more breaker firings within 20 of p.
func CursedNear(p data.Position) bool {
	n := 0
	for _, b := range breakerSites {
		if chebyshev(b, p) <= 20 {
			n++
		}
	}
	return n >= 2
}

// MenuSanctionUntil: the MENU SENTRY's one exemption (the owner, 00:52: "an
// aware state that de-escs unless there's a good reason like relogging").
// Relog sanctions the quit menu while its ritual lives there; everyone else's
// standing quit menu is a wedge and the sentry closes it.
var MenuSanctionUntil time.Time

// worldGhosts — P-4.8a: a wedged vendor outlives every retry. Ghost verdicts
// accumulate per world; at three the world is POISONED and Relog cures it.
var worldGhosts int

// NoteGhost records one ghost/deaf-vendor verdict against this world.
func NoteGhost() { worldGhosts++ }

// WorldPoisoned reports whether this world has earned its relog (P-4.8a).
func WorldPoisoned() bool { return worldGhosts >= 3 }

// NewWorld re-arms every retired belief and clears cross-seed state — a relog
// re-rolls the world (WARNING 8), and a belief retired by one bad frame in
// game 1 must not silence a whole service for every game after (the
// reviewer's finding 2: one ghost window benched the equip service forever).
func NewWorld() {
	healerHeals.Store(true)
	equipWorks.Store(true)
	identifyWorks.Store(true)
	scrollWorks.Store(true)
	spendWorks.Store(true)
	skillSpendWorks.Store(true)
	refusedEquips = map[int]bool{}
	hotPortalUntil = time.Time{}
	carryReachAt = time.Time{}
	fleeFatigueUntil = time.Time{}
	bloodRing = nil // a load-screen gap would read as a phantom drop rate
	worldGhosts = 0 // the poison died with the world it poisoned (P-4.8a)
}

// CarryReach — P-5.9 THE MARCH CARRIES THE REACH TOOL (the owner, at the
// corner: "she's not pulling her bow out"): walking with no enemy within 8,
// a REACH set that exists and is not dry is the set in hand. One rate-limited
// swap press; the next snapshot's WeaponKind is the verification (a deaf or
// wrong swap self-corrects on the following call — perception, not hope).
func CarryReach(ctx *Ctx) {
	s := ctx.Snap
	if s == nil || !s.Valid || s.Me.WeaponKind != "melee" || !s.Me.HasBow || s.Me.Arrows == 0 {
		return
	}
	for _, e := range s.Enemies {
		if chebyshev(s.Me.Pos, e.Pos) <= 8 {
			return // contact is P-1's moment, not the march's
		}
	}
	if time.Since(carryReachAt) < 2*time.Second {
		return
	}
	ctx.M.KeyLane().Press(ctx.SwapKey)
	carryReachAt = time.Now()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------- Flee (ClassSurvive)

// Flee triggers on low HP with contact pressure; strides away from the enemy centroid
// until the pool recovers or the pressure is gone. The Sentinel keeps drinking in
// parallel — this is escape, not medicine.
type Flee struct {
	castAt time.Time
	// March is Advance's live door hint (P-5.7 FORCED MARCH): on unpaying ground
	// a retreat that backtracks refunds nothing — flee FORWARD when the march
	// direction is not into the crowd. Nil-safe: no hint, classic away-flee.
	March func() (data.Position, bool)
	// P-2.9 pin detection: a retreat that gains no ground is a fight, not a
	// stride. pinRef/pinAt track net progress; lastStrikeAt paces the answer.
	pinRef       data.Position
	pinAt        time.Time
	lastStrikeAt time.Time
	// P-2.0 density confirmation: the 20+ backstop must hold two consecutive
	// reads — sight-lines through a corridor mouth flap the count (Flavie's
	// pass, 23:12) and a flickering horde is no horde.
	denseN int
	// P-2.11 flee-fatigue bookkeeping: distinct crowd-flee episodes on the
	// same ground. The third inside the window declares the retreat a lie.
	epCount      int
	epPos        data.Position
	lastCrowdBid time.Time
}

// fleeFatigueUntil — P-2.11 THE THIRD RETREAT IS A LIE: while it holds, Flee
// stands down (above the death floor) and Fight's wounded stand-down is
// suspended — she blasts instead of orbiting. Reset per world (NewWorld).
var fleeFatigueUntil time.Time

// ---------------------------------------------------------------- Stand (ClassSurvive)

type posAt struct {
	at  time.Time
	pos data.Position
}

// Stand — P-1.14 THE CORNERED VERDICT (the owner, 11:45: "if she remains in
// spot for more than 3 seconds and enemies are nearby... shift to maximum
// killing"): held ground with teeth nearby belongs to the arrows, whatever
// any other activity thinks it is doing. Outbids every retreat except the
// critical dive — a girl who cannot move cannot flee.
type Stand struct {
	ring         []posAt
	lastStrikeAt time.Time
}

func (st *Stand) Name() string { return "stand" }

func (st *Stand) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 {
		st.ring = nil
		return nil
	}
	if s.Me.WeaponKind == "none" && s.Me.CorpseFound {
		return nil // recovery owns the naked girl (WARNING 3)
	}
	now := time.Now()
	st.ring = append(st.ring, posAt{now, s.Me.Pos})
	for len(st.ring) > 0 && now.Sub(st.ring[0].at) > 3500*time.Millisecond {
		st.ring = st.ring[1:]
	}
	if len(st.ring) < 4 || now.Sub(st.ring[0].at) < 2800*time.Millisecond {
		return nil // not yet 3 seconds of held ground
	}
	anchor := st.ring[len(st.ring)-1].pos
	for _, p := range st.ring {
		if chebyshev(p.pos, anchor) >= 3 {
			return nil // she moves; the ground is not held
		}
	}
	near := 0
	for _, e := range s.Enemies {
		if e.Walled { // WARNING 10: standing near a fence is not being pressed
			continue
		}
		if chebyshev(s.Me.Pos, e.Pos) <= 12 {
			near++
		}
	}
	if near == 0 {
		return nil
	}
	return &arbiter.Demand{Who: st.Name(), Class: arbiter.ClassSurvive,
		Urgency: 1.5, // over flee (1.35) and the engaged breakout (1.45); under the critical dive
		Commit:  arbiter.Commitment{MinHold: 2 * time.Second}}
}

func (st *Stand) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	best, bd := data.Position{}, 1<<30
	for _, e := range s.Enemies {
		if d := chebyshev(s.Me.Pos, e.Pos); d < bd {
			best, bd = e.Pos, d
		}
	}
	if bd > 14 {
		return Done // the ground opened — the ordinary doctrine resumes
	}
	// MAXIMUM KILLING: the skill per P-1.13 (above the 10% swallow), the
	// contact strike otherwise, the bare fist as the last resort.
	var key byte
	switch {
	case s.Me.WeaponKind == "bow" && ctx.Cap != nil && ctx.Cap.Reach != nil && s.Me.MPPct > 10:
		key = ctx.Cap.Reach.Key
	case ctx.Cap != nil && ctx.Cap.Contact != nil:
		key = ctx.Cap.Contact.Key
	}
	if time.Since(st.lastStrikeAt) >= 350*time.Millisecond {
		volleyAt(ctx, best, key, false)
		st.lastStrikeAt = time.Now()
	}
	return Running
}

// ---------------------------------------------------------------- Blood oracle (P-2.0)

type bloodSample struct {
	at time.Time
	hp int
}

var bloodRing []bloodSample

// ObserveBlood feeds the oracle one snapshot — the executive calls it every
// cycle so every Demand judges from ONE truth.
func ObserveBlood(s *percept.Snapshot) {
	if s == nil || !s.Valid || s.Me.HPPct <= 0 {
		return
	}
	now := time.Now()
	bloodRing = append(bloodRing, bloodSample{now, s.Me.HPPct})
	for len(bloodRing) > 0 && now.Sub(bloodRing[0].at) > 5*time.Second {
		bloodRing = bloodRing[1:]
	}
}

// TimeToDie — P-2.0 THE BLOOD ORACLE's verdict: seconds until the hard floor
// (18) at the measured drop rate, with 40 blood of runway per belt heal.
// 999 = the runway holds (stable, rising, or no window yet — never retreat
// on a guess; the density backstop covers the burst that outruns the window).
func TimeToDie(s *percept.Snapshot) float64 {
	if len(bloodRing) < 3 {
		return 999
	}
	first, last := bloodRing[0], bloodRing[len(bloodRing)-1]
	dt := last.at.Sub(first.at).Seconds()
	if dt < 1.5 {
		return 999
	}
	rate := float64(first.hp-last.hp) / dt // blood pct per second
	if rate <= 0.5 {
		return 999
	}
	runway := float64(s.Me.HPPct-18) + float64(s.Me.HealPots)*40
	if runway < 0 {
		runway = 0
	}
	return runway / rate
}

func (f *Flee) Name() string { return "flee" }

func (f *Flee) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 {
		return nil
	}
	// P-2.0: the retreat triggers below judge by the BLOOD ORACLE's runway
	// (TimeToDie), not by static HP floors — Fight's own stand-down uses the
	// same oracle, so no dead band opens between the rules.
	near := 0
	for _, e := range s.Enemies {
		if e.Walled { // WARNING 10: a fenced camp is not a crowd
			continue
		}
		if chebyshev(s.Me.Pos, e.Pos) <= 25 {
			near++
		}
	}
	// P-2.1: a CROWD is fled — at 12 wounded, at 16 healthy (the owner, 11:15:
	// "shoot more than moving"; run 41's eighty-four remain the floor of this
	// law, not its ceiling). Blood is a lagging indicator inside a horde.
	crowdBar := 16
	if s.Me.HPPct < 75 {
		crowdBar = 12
	}
	// WARNING 3 (the reviewer's finding 5): a naked girl with a corpse out
	// there has ONE job — fleeing away from her own gear is how she loops
	// flee→die→respawn forever. Recovery owns her; Flee stands down.
	if s.Me.WeaponKind == "none" && s.Me.CorpseFound {
		return nil
	}
	// P-2.-2 THE BRAWLER'S CREED (the owner, 04:08): no ranged game means
	// footwork is denial — he fights at ANY blood. Flee exists only to
	// refuse ENCIRCLEMENT: 6+ in true contact closing the ring, or the
	// 20+ density backstop. Breakout keeps the critical eject.
	if brawlerMode {
		// BLOOD JOINS THE RING (11:43 audit: 65 flee grants in barb31's
		// sixteen minutes, bidding at 96 blood — radius-45 aggression walks
		// him INTO packs and this branch yanked him straight back out: the
		// charge-flee oscillator IS the "runs around instead of killing" the
		// owner watched, wearing Survive-class priority). A brawler leaves a
		// ring only when his blood argues too (<55); above that the ring is
		// just customers in a queue. Breakout keeps the critical eject
		// (TimeToDie, trapped, hard floor) at any count.
		if s.Me.HPPct >= 55 {
			return nil
		}
		ring := 0
		for _, e := range s.Enemies {
			if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 4 {
				ring++
			}
		}
		if ring >= 6 || near >= 20 {
			return &arbiter.Demand{Who: f.Name(), Class: arbiter.ClassSurvive,
				Urgency: 0.9,
				Commit:  arbiter.Commitment{MinHold: 2 * time.Second}}
		}
		return nil
	}
	// P-2.-1 THE FLEE FLOOR (the owner, 23:20): flee does not exist at 33
	// blood or above — no crowd bar, no density backstop, no runway math.
	// The bow answers crowds; Breakout alone keeps the eject seat.
	if s.Me.HPPct >= 33 {
		f.denseN = 0
		return nil
	}
	// P-2.0: DENSITY BACKSTOP first — 20+ is fled at any oracle verdict and
	// through any fatigue (density kills before HP moves; run 41). The count
	// must hold TWO consecutive reads: corridor sight-lines flap it (Flavie's
	// pass thrash, 23:12), and a flickering horde is no horde.
	if near >= 20 {
		f.denseN++
		if f.denseN >= 2 {
			return &arbiter.Demand{Who: f.Name(), Class: arbiter.ClassSurvive,
				Urgency: 1.35,
				Commit:  arbiter.Commitment{MinHold: 2 * time.Second}}
		}
	} else {
		f.denseN = 0
	}
	ttd := TimeToDie(s)
	if near >= crowdBar && ttd < 10 {
		// P-2.11 THE THIRD RETREAT IS A LIE: count distinct episodes on the
		// same ground; the third inside 90 s fatigues Flee for 60 s. The death
		// floor (30) is exempt — fatigue never binds a dying girl.
		if time.Since(f.lastCrowdBid) > 15*time.Second {
			if time.Since(f.lastCrowdBid) > 90*time.Second || chebyshev(s.Me.Pos, f.epPos) > 30 {
				f.epCount = 0 // fresh ground or a stale window: new ledger
			}
			f.epCount++
			f.epPos = s.Me.Pos
			if f.epCount >= 3 {
				fleeFatigueUntil = time.Now().Add(60 * time.Second)
				f.epCount = 0
			}
		}
		f.lastCrowdBid = time.Now()
		if time.Now().Before(fleeFatigueUntil) && s.Me.HPPct >= 30 {
			return nil // stand and blast — Fight owns the ground (P-2.11)
		}
		return &arbiter.Demand{Who: f.Name(), Class: arbiter.ClassSurvive,
			Urgency: 1.35,
			Commit:  arbiter.Commitment{MinHold: 2 * time.Second}}
	}
	// P-1.12 + P-2.0: the wounded retreat fires on a SHORT RUNWAY with real
	// pressure — never on head-count alone, never from one chaser.
	if ttd < 8 && near >= 2 {
		if time.Now().Before(fleeFatigueUntil) && s.Me.HPPct >= 30 {
			return nil // P-2.11: the fatigue suspends the wounded retreat too
		}
		return &arbiter.Demand{Who: f.Name(), Class: arbiter.ClassSurvive,
			Urgency: 1.0 - float64(s.Me.HPPct)/100,
			Commit:  arbiter.Commitment{MinHold: 2 * time.Second}}
	}
	return nil
}

func (f *Flee) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	// Done only above the SAME floor that triggers the bid (plus margin) — an exit bar
	// below the entry bar re-bids the moment it releases: the thrash generator.
	// The density retreat exits at crowd<8 (in at 12): healthy blood alone is not
	// clearance from a horde.
	crowd := 0
	for _, e := range s.Enemies {
		if e.Walled { // WARNING 10
			continue
		}
		if chebyshev(s.Me.Pos, e.Pos) <= 25 {
			crowd++
		}
	}
	clear := 45
	if s.Me.HealPots == 0 {
		clear = 55
	}
	if s.Me.HPPct > clear && crowd < 8 {
		return Done
	}
	cx, cy, n, closest := 0, 0, 0, 1<<30
	var closestPos data.Position
	for _, e := range s.Enemies {
		if e.Walled { // WARNING 10: flee FROM what can reach her, not from fences
			continue
		}
		d := chebyshev(s.Me.Pos, e.Pos)
		if d < closest {
			closest, closestPos = d, e.Pos
		}
		if d <= 25 {
			cx += e.Pos.X
			cy += e.Pos.Y
			n++
		}
	}
	if n == 0 {
		return Done
	}
	// P-2.9 A PINNED RETREAT IS A FIGHT (measured 08:12: flee and breakout
	// rotated 15s stuck-cooldowns striding into a wall, blood 51→24, no swing
	// answered): no net ground for 3s with a TOOTH on her → strike it between
	// strides. The wall has declared CORNERED for her. Never a mute cycle.
	if f.pinAt.IsZero() || chebyshev(s.Me.Pos, f.pinRef) >= 3 {
		f.pinRef, f.pinAt = s.Me.Pos, time.Now()
	}
	if time.Since(f.pinAt) > 3*time.Second && closest <= 3 &&
		time.Since(f.lastStrikeAt) >= 350*time.Millisecond {
		var key byte
		switch {
		case s.Me.WeaponKind == "melee" && ctx.Cap != nil && ctx.Cap.Contact != nil:
			key = ctx.Cap.Contact.Key
		case s.Me.WeaponKind == "bow" && ctx.Cap != nil && ctx.Cap.Reach != nil:
			key = ctx.Cap.Reach.Key
		}
		volleyAt(ctx, closestPos, key, false)
		f.lastStrikeAt = time.Now()
		return Running
	}
	// PLANT THE EXIT ON THE RUN (measured 04:14: flee↔travel thrashed at the gate for
	// 40s while Withdraw — ClassTravel — starved under Flee's survive class; the one
	// activity able to cast never got its 2.5s). A dry belt means this flee ends in
	// town or in a corpse: the moment the gap opens, Flee itself casts the portal —
	// and USES it. Fleeing is not a lifestyle; it has a destination.
	if s.Me.HealPots == 0 && s.Me.HPPct < 55 && len(s.Portals) > 0 {
		// P-2.3: THE RIDE REQUIRES A WOUND — a full-blooded dry belt flees on
		// foot; town holds nothing for a penniless girl at full blood and the
		// round trip is an orbit, not a service (the owner watched the loop,
		// 22:55). Below the clear bar the free heal at Akara pays the trip.
		// P-2.4: dead doors are ABSENT — a flee must never bind to a portal
		// that provably does not open (the 21:35 death).
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
				slideStride(ctx, best.Pos, 1500*time.Millisecond, 1, f.Name())
			} else {
				verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos, Desperate: true}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
			}
			return Running
		}
		// every door is dead: fall through — cast a new one or flee on foot
	}
	if s.Me.HealPots == 0 && s.Me.HPPct < 55 && ctx.Cap != nil && ctx.Cap.TownTP != nil &&
		closest > 10 && time.Since(f.castAt) > 2500*time.Millisecond {
		verbs.CastSelf{Key: ctx.Cap.TownTP.Key}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
		f.castAt = time.Now()
		return Running
	}
	away := data.Position{X: s.Me.Pos.X + (s.Me.Pos.X - cx/n), Y: s.Me.Pos.Y + (s.Me.Pos.Y - cy/n)}
	// P-5.7 FORCED MARCH: on ground that does not pay, the moor is crossed, not
	// orbited. When the march door is known and its direction is within 90° of
	// the away vector (dot ≥ 0 — never INTO the crowd), retreat along the
	// bisector of away and march: distance from the pack AND ground gained.
	if f.March != nil && !ExpWorthwhile(s.Me.Level, s.Me.Area) {
		if mt, ok := f.March(); ok {
			avx, avy := s.Me.Pos.X-cx/n, s.Me.Pos.Y-cy/n
			mvx, mvy := mt.X-s.Me.Pos.X, mt.Y-s.Me.Pos.Y
			am := maxInt(absInt(avx), absInt(avy))
			mm := maxInt(absInt(mvx), absInt(mvy))
			if am > 0 && mm > 0 && avx*mvx+avy*mvy >= 0 {
				away = data.Position{
					X: s.Me.Pos.X + 8*avx/am + 8*mvx/mm,
					Y: s.Me.Pos.Y + 8*avy/am + 8*mvy/mm,
				}
			}
		}
	}
	slideStride(ctx, away, 2*time.Second, 3, f.Name())
	return Running
}

// ---------------------------------------------------------------- Breakout (ClassSurvive)

// Breakout is the owner's "fight for a way out, or TP away — or both": when surrounded
// or wounded, read the encirclement as a ring of 8 sectors, and choose intelligently:
//   1. A GAP exists (a sector with ≤1 enemy): fight for it — strike the blocker in the
//      gap, stride through, done when clear. Purposeful violence, not panic.
//   2. NO gap, or potions gone: PREPARE THE EXIT — cast the town portal immediately
//      (it persists), then keep fighting the thinnest sector from throw range.
//   3. HP hits the hard floor: step into the portal. Escape is a fallback she is
//      standing next to, never a hope.
type Breakout struct {
	castAt       time.Time
	lastStrikeAt time.Time
	vaultAt      time.Time // P-2.11: leap cooldown — a deaf leap falls through to the old doctrine
	castTries    int // casts that never produced a portal (empty tome — the poverty spiral)
	// engaged: Breakout has committed to a portal (cast it or fought its doormen).
	// Rule zero evicted it mid-rescue when one potion tick dropped 'surrounded' below
	// threshold — she fought at the mouth and then marched AWAY from her own portal
	// (run 35, the owner: "opens a portal, fights a little bit, then runs away from
	// it?"). A started escape is finished: through the portal, heal, dress, return.
	engaged bool
	// P-2.9 pin detection: the "open gap" sector can point into camp furniture
	// the grid has never heard of — striding there forever is the mute cycle
	// that bled her 51→24 at 08:12.
	pinRef data.Position
	pinAt  time.Time
}

func (b *Breakout) Name() string { return "breakout" }

// canVault: the ONE mana gate for every leap site (11:20: three whiffs, all
// mana-dry). A tiny pool must hold the leap's cost (~half of 4); a real pool
// asks only for scraps.
//
// vaultHungerUntil — MANA HUNGER (13:39: mana-per-hit lands 4 points and
// Double Swing drinks them the same instant; the leap's gate never opens —
// the two skills race for one starved pool and the swing always wins). When
// a leap site is READY but mana-short, the hunger window makes the contact
// skill stand down for a few seconds so the pool builds to the jump.
var vaultHungerUntil time.Time

// fightCoolUntil — THE SIGHT DIVERGENCE cool (21:43): Demand's Walled filter
// is looser than the pick's losClear, so a pack can be biddable yet
// untargetable; without this cool the arbiter churns grant/done while the
// march starves.
var fightCoolUntil time.Time

func canVault(ctx *Ctx, s *percept.Snapshot) bool {
	if ctx.Cap == nil || ctx.Cap.Vault == nil {
		return false
	}
	need := 10
	if s.Me.MaxMana < 8 {
		need = 50
	}
	if s.Me.MPPct >= need {
		return true
	}
	vaultHungerUntil = time.Now().Add(4 * time.Second)
	return false
}

// travelVaultAt: one clock for the travel gait — leaps spent on distance never
// starve the combat sites (they run their own cooldowns).
var travelVaultAt time.Time

// TravelVault — P-2.11(4) (the owner, 11:20: "if it's trying to get somewhere
// it could run there and leap it"): one leap along the march every 6s when the
// pool affords it, landing ~12 tiles toward the goal on grid-vouched ground.
// The walk continues underneath either way; a false return costs nothing.
func TravelVault(ctx *Ctx, toward data.Position) bool {
	s := ctx.Snap
	if s == nil || !s.Valid || s.Me.InTown || !canVault(ctx, s) ||
		time.Since(travelVaultAt) < 6*time.Second {
		return false
	}
	d := chebyshev(s.Me.Pos, toward)
	if d < 10 {
		return false // walking is faster than winding up a jump
	}
	hop := 12
	if d-2 < hop {
		hop = d - 2
	}
	land := data.Position{
		X: s.Me.Pos.X + (toward.X-s.Me.Pos.X)*hop/d,
		Y: s.Me.Pos.Y + (toward.Y-s.Me.Pos.Y)*hop/d,
	}
	if !vaultLandable(ctx, land) {
		return false
	}
	travelVaultAt = time.Now()
	verbs.Vault{To: land, Key: ctx.Cap.Vault.Key, SkillID: int(ctx.Cap.Vault.Skill)}.
		Do(ctx.M, ctx.GR, ctx.P, ctx.Led, "travelvault")
	return true
}

// vaultLandable: a leap landing must be ground the grid vouches for — leaping
// into unknown terrain trades a known ring for an unknown wall (dodge is
// optimistic about unknowns; a leap is not: it cannot be steered mid-air).
func vaultLandable(ctx *Ctx, p data.Position) bool {
	g := ctx.Grid
	if g == nil {
		return false
	}
	rp := g.RelativePosition(p)
	if rp.X < 0 || rp.Y < 0 || rp.X >= g.Width || rp.Y >= g.Height {
		return false
	}
	return g.CollisionGrid[rp.Y][rp.X] != game.CollisionTypeNonWalkable
}

func (b *Breakout) ringSectors(s *percept.Snapshot) (counts [8]int, nearest [8]percept.EnemyRef, hasNear [8]bool) {
	for _, en := range s.Enemies {
		d := chebyshev(s.Me.Pos, en.Pos)
		if d > 20 {
			continue
		}
		dx, dy := en.Pos.X-s.Me.Pos.X, en.Pos.Y-s.Me.Pos.Y
		sec := sectorOf(dx, dy)
		w := 1
		if d <= 12 {
			w = 2
		}
		counts[sec] += w
		if !hasNear[sec] || d < chebyshev(s.Me.Pos, nearest[sec].Pos) {
			nearest[sec], hasNear[sec] = en, true
		}
	}
	return
}

func sectorOf(dx, dy int) int {
	// 8 sectors by dominant axis mix — cheap and adequate for ring reading.
	switch {
	case dx >= 0 && dy < 0 && -dy >= dx:
		return 0 // N
	case dx > 0 && dy < 0:
		return 1 // NE
	case dx > 0 && dy >= 0 && dx > dy:
		return 2 // E
	case dx > 0:
		return 3 // SE
	case dx <= 0 && dy > 0 && dy >= -dx:
		return 4 // S
	case dx < 0 && dy > 0:
		return 5 // SW
	case dx < 0 && -dx >= -dy:
		return 6 // W
	default:
		return 7 // NW
	}
}

var sectorDir = [8]data.Position{{X: 0, Y: -1}, {X: 1, Y: -1}, {X: 1, Y: 0}, {X: 1, Y: 1},
	{X: 0, Y: 1}, {X: -1, Y: 1}, {X: -1, Y: 0}, {X: -1, Y: -1}}

func (b *Breakout) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 {
		return nil
	}
	near := 0
	for _, en := range s.Enemies {
		if en.Walled { // WARNING 10: a fenced camp does not surround anyone
			continue
		}
		if chebyshev(s.Me.Pos, en.Pos) <= 18 {
			near++
		}
	}
	// P-2.2 + P-2.0: the eject seat arms at 6 surrounding with a COLLAPSING
	// runway — four nuisances under a scratch kept her porting out of
	// winnable fights all morning (the 57-HP sortie loop, 11:06).
	critical := s.Me.HPPct < 24
	surrounded := near >= 6 && TimeToDie(s) < 12
	trapped := s.Me.HPPct < 45 && s.Me.HealPots == 0 && near >= 3
	// COMMITMENT: an engaged escape keeps bidding while its portal stands — one
	// potion tick dropping 'surrounded' must not strand a half-used exit.
	// A DEAD DOOR does not sustain the commitment (P-2.4).
	livePortal := false
	for _, pt := range s.Portals {
		if !verbs.IsDeadDoor(pt.ID) {
			livePortal = true
			break
		}
	}
	if b.engaged && livePortal && !s.Me.InTown {
		return &arbiter.Demand{Who: b.Name(), Class: arbiter.ClassSurvive,
			Urgency: 1.45,
			Commit:  arbiter.Commitment{MinHold: 3 * time.Second}}
	}
	if critical || surrounded || trapped {
		return &arbiter.Demand{Who: b.Name(), Class: arbiter.ClassSurvive,
			Urgency: 2.0 - float64(s.Me.HPPct)/100, // outranks Flee (max ~1.0)
			Commit:  arbiter.Commitment{MinHold: 3 * time.Second}}
	}
	return nil
}

func (b *Breakout) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if s.Me.InTown {
		b.engaged = false
		b.castTries = 0 // the poverty spiral ends where the shopping starts
		return Done // through the portal — safe; town services take the wheel
	}
	near := 0
	for _, en := range s.Enemies {
		if en.Walled { // WARNING 10
			continue
		}
		if chebyshev(s.Me.Pos, en.Pos) <= 18 {
			near++
		}
	}
	// P-2.4: A DEAD DOOR UN-MAKES THE DECISION — a portal with three deaf
	// entries is ABSENT to every decision here (measured 21:35: twenty
	// desperate dives into a portal that never transitioned, 84→0 with 119
	// at the mouth). Fight the ring, cast a NEW portal, never re-commit.
	livePortals := s.Portals[:0:0]
	for _, pt := range s.Portals {
		if !verbs.IsDeadDoor(pt.ID) {
			livePortals = append(livePortals, pt)
		}
	}
	if b.engaged && len(livePortals) == 0 {
		b.engaged = false // the portal fell out of the world — or died as a door
	}
	if near == 0 && s.Me.HPPct >= 40 && !b.engaged {
		// A capped cast counter must not survive the emergency it counted:
		// hours later, tome restocked, a new encirclement found the cast block
		// dead forever (the reviewer's finding 1) — reset on every clean exit.
		b.castTries = 0
		return Done // broke out and clear — and no half-used exit standing
	}

	// A PORTAL DOWN IS A DECISION ALREADY MADE — no second gate. Breakout stepping AT
	// ALL means its demand fired: critical, surrounded, or trapped. The old use-bar
	// (HP<35) contradicted the cast-bar (near>=5): at HP 52 inside a 30-strong swarm
	// she cast the exit and then stood BESIDE it ring-fighting until the bar armed —
	// by then the label was buried under bodies and she died pinned at (5843,4847),
	// 65->0 in 11s (run 24). Emergency + portal = ENTER, desperately.
	hardFloor := s.Me.HPPct < 18
	if len(livePortals) > 0 {
		b.engaged = true // a standing LIVE portal + Breakout stepping = the escape is OWNED
		best, bd := livePortals[0], chebyshev(s.Me.Pos, livePortals[0].Pos)
		for _, pt := range livePortals[1:] {
			if d := chebyshev(s.Me.Pos, pt.Pos); d < bd {
				best, bd = pt, d
			}
		}
		// CLEAR THE DOOR FIRST (the owner: "clear the portal and then do it... it's
		// imperative that it doesn't idle while monsters are nearby"): the entry
		// ritual is STATIONARY — with doormen crowding the mouth every attempt is a
		// beating with no answer. Volley the nearest doorman until the mouth thins;
		// at the hard floor stop discriminating and dive (the blind desperate click).
		if bd <= 20 && !hardFloor {
			guards, gd := 0, 1<<30
			var doorman percept.EnemyRef
			for _, e := range s.Enemies {
				if chebyshev(best.Pos, e.Pos) <= 4 {
					guards++
					if md := chebyshev(s.Me.Pos, e.Pos); md < gd {
						doorman, gd = e, md
					}
				}
			}
			if guards >= 2 {
				if time.Since(b.lastStrikeAt) >= 350*time.Millisecond {
					var key byte
					if ctx.Cap != nil && ctx.Cap.Throw != nil {
						key = ctx.Cap.Throw.Key
					} else if ctx.Cap != nil && ctx.Cap.Contact != nil {
						key = ctx.Cap.Contact.Key
					}
					volleyAt(ctx, doorman.Pos, key, false) // walk-proof, sweep-free
					b.lastStrikeAt = time.Now()
				}
				return Running
			}
		}
		if bd > 20 {
			verbs.Stride{To: best.Pos, Hold: 1500 * time.Millisecond, MinGain: 1}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
		} else {
			// P-2.10: a portal entered with a CROWD at the mouth is HOT — Return
			// must not feed her back into the same jaws (the sortie loop,
			// 11:03-11:08: out by portal, in by portal, out by portal).
			near := 0
			for _, e := range s.Enemies {
				if chebyshev(s.Me.Pos, e.Pos) <= 25 {
					near++
				}
			}
			if near >= 8 {
				hotPortalUntil = time.Now().Add(3 * time.Minute)
			}
			verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos, Desperate: true}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
		}
		return Running
	}

	// THE LEAP OUTRANKS THE PORTAL (the 13:03 death: pinned in a 0x0 box by
	// 18 bodies for 19 seconds, blood 6→0, and the ring vault NEVER fired —
	// it sat below the portal-casting branch, and its single landing
	// candidate refused silently, a rule-zero violation twice over). When no
	// portal stands, the leap goes FIRST: it is instant, needs no tome, and
	// lands him outside the jaws instead of diving back through them. Every
	// sector is tried best-gap-first at three ranges; only a fully-refused
	// board falls through to the portal, and the refusal is WRITTEN.
	if len(livePortals) == 0 && canVault(ctx, s) && time.Since(b.vaultAt) > 8*time.Second {
		counts, _, _ := b.ringSectors(s)
		order := make([]int, 8)
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, bb int) bool { return counts[order[a]] < counts[order[bb]] })
		leapt := false
		for _, sec := range order {
			for _, rng := range []int{10, 13, 7} {
				dir := sectorDir[sec]
				land := data.Position{X: s.Me.Pos.X + dir.X*rng, Y: s.Me.Pos.Y + dir.Y*rng}
				if vaultLandable(ctx, land) {
					b.vaultAt = time.Now()
					verbs.Vault{To: land, Key: ctx.Cap.Vault.Key, SkillID: int(ctx.Cap.Vault.Skill)}.
						Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
					leapt = true
					break
				}
			}
			if leapt {
				break
			}
		}
		if leapt {
			return Running
		}
		ctx.Led.Append(verbs.Outcome{Verb: "vault", Holder: b.Name(), Result: verbs.ResRefused,
			Evidence: "no walkable landing in any sector at 7/10/13 — walled pocket, falling through to portal"})
	}

	// PREPARE THE EXIT: no portal down yet and things look grim → cast one now.
	// It persists; fighting continues beside it. This is the "both". THREE casts with
	// no portal appearing = the tome is EMPTY (0 gold, 0 scrolls — the poverty
	// spiral); stop pretending and fight the gap instead (measured: pinned 0x0
	// re-casting into nothing while the ring closed).
	// THE DESPERATION LADDER NEVER RUNS DRY (13:03: castTries exhausted
	// earlier in the fight, then the ring closed and the cast branch was
	// dead FOREVER while he bled 100→0 through ten drinks). At the hard
	// floor the counter resets — a dying man re-tries his tome every cycle
	// of the ladder; the poverty spiral is bounded by the 2.5s pacing alone.
	if hardFloor && b.castTries >= 3 && time.Since(b.castAt) > 5*time.Second {
		b.castTries = 0
	}
	if len(livePortals) == 0 && ctx.Cap != nil && ctx.Cap.TownTP != nil && b.castTries < 3 &&
		(hardFloor || s.Me.HealPots == 0 || near >= 5) &&
		time.Since(b.castAt) > 2500*time.Millisecond {
		if !b.castAt.IsZero() {
			b.castTries++ // the previous cast had 2.5s to materialize a portal and didn't
		}
		verbs.CastSelf{Key: ctx.Cap.TownTP.Key}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
		b.castAt = time.Now()
		return Running
	}

	// RING READ: find the thinnest sector.
	counts, nearest, hasNear := b.ringSectors(s)
	gap, gapCount := 0, 1<<30
	for i, c := range counts {
		if c < gapCount {
			gap, gapCount = i, c
		}
	}

	// P-2.9: pin detection across Steps — no net ground for 3s means the chosen
	// escape line is stone (furniture is in NO grid); the answer is violence.
	if b.pinAt.IsZero() || chebyshev(s.Me.Pos, b.pinRef) >= 3 {
		b.pinRef, b.pinAt = s.Me.Pos, time.Now()
	}
	pinned := time.Since(b.pinAt) > 3*time.Second
	// P-2.11 THE VAULT (the owner, 2026-07-20: "i gave our barb leap — get
	// through problematic situations"): a closed or pinned ring is the leap's
	// whole reason to exist — jump THROUGH the thinnest sector to walkable
	// ground and the ring becomes scenery. One try per 8s; a whiff (dry mana,
	// bad landing) falls through to the shove-and-fight doctrine below.
	if canVault(ctx, s) && (gapCount > 0 || pinned) &&
		time.Since(b.vaultAt) > 8*time.Second {
		dir := sectorDir[gap]
		land := data.Position{X: s.Me.Pos.X + dir.X*10, Y: s.Me.Pos.Y + dir.Y*10}
		if vaultLandable(ctx, land) {
			b.vaultAt = time.Now()
			verbs.Vault{To: land, Key: ctx.Cap.Vault.Key, SkillID: int(ctx.Cap.Vault.Skill)}.
				Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
			return Running
		}
	}
	if gapCount == 0 && !pinned {
		// Open gap: stride through it hard (sliding off any wall on the line).
		dir := sectorDir[gap]
		out := data.Position{X: s.Me.Pos.X + dir.X*22, Y: s.Me.Pos.Y + dir.Y*22}
		slideStride(ctx, out, 2*time.Second, 3, b.Name())
		return Running
	}
	if gapCount == 0 && pinned {
		// The "open" sector is a wall. Strike the nearest enemy instead —
		// CORNERED = FIGHT, and a kill is the only door left.
		best, bd := data.Position{}, 1<<30
		for _, e := range s.Enemies {
			if d := chebyshev(s.Me.Pos, e.Pos); d < bd {
				best, bd = e.Pos, d
			}
		}
		if bd <= 4 && time.Since(b.lastStrikeAt) >= 350*time.Millisecond {
			var key byte
			if ctx.Cap != nil && ctx.Cap.Contact != nil {
				key = ctx.Cap.Contact.Key
			}
			volleyAt(ctx, best, key, false)
			b.lastStrikeAt = time.Now()
		} else if bd > 4 {
			slideStride(ctx, best, 1200*time.Millisecond, 1, b.Name()) // walk AT the enemy: off the wall
		}
		return Running
	}
	// FIGHT FOR THE WAY OUT: strike the blocker holding the thinnest sector — VOLLEY
	// pace, walk-proof and sweep-free (the hover sweep was seconds of standing still
	// inside the ring — the exact "not doing anything while taking hits").
	if hasNear[gap] {
		if time.Since(b.lastStrikeAt) >= 350*time.Millisecond {
			blocker := nearest[gap]
			var key byte
			if ctx.Cap != nil && ctx.Cap.Throw != nil {
				key = ctx.Cap.Throw.Key
			} else if ctx.Cap != nil && ctx.Cap.Contact != nil {
				key = ctx.Cap.Contact.Key
			}
			volleyAt(ctx, blocker.Pos, key, false)
			b.lastStrikeAt = time.Now()
		}
		return Running
	}
	// Degenerate: no readable ring — back away from the mass.
	cx, cy, n := 0, 0, 0
	for _, e := range s.Enemies {
		cx += e.Pos.X
		cy += e.Pos.Y
		n++
	}
	if n > 0 {
		away := data.Position{X: s.Me.Pos.X + (s.Me.Pos.X - cx/n), Y: s.Me.Pos.Y + (s.Me.Pos.Y - cy/n)}
		slideStride(ctx, away, 1500*time.Millisecond, 0, b.Name())
	}
	return Running
}

// ---------------------------------------------------------------- Fight (ClassFight)

type Fight struct {
	target    data.UnitID
	targetPos data.Position
	j         *journey.Journey
	noEvid    int
	swapAt    time.Time
	blacklist map[data.UnitID]time.Time
	// Volley state: shots are fire-and-track (no per-shot evidence wait — that wait
	// WAS the pause between shots). Evidence arrives passively: the target's flinch
	// or death shows up in the snapshot stream the Executive already captures.
	volleys      int       // shots at the current target since the last observed flinch
	aimDX, aimDY int       // sweep offset that confirmed hover last shot (next shot's hint)
	lastKey      byte      // skill key selected by the previous strike
	lastStrikeAt time.Time // paces the volley to the attack animation (~350ms)
	// Evidence audit of the calibrated ranged skill: the mod scrambles tables, so the
	// "bow skill" the calibrator proved may be IDENTIFY wearing an attack's ID (the
	// owner watched her arm it). A skill that never makes anyone flinch is demoted —
	// plain attack always works.
	rangedShots  int
	rangedFlinch int
	rangedDead   bool
	// Mutual-veto watchdog (P-2.10): when the lock changed and when we last
	// ISSUED an attack input. An in-reach lock that produces no input starves
	// Travel while feeding nothing — the advisor's "tiny bureaucratic collapse".
	watchTarget data.UnitID
	watchSince  time.Time
	vaultAt     time.Time // P-2.11(3): leap-to-the-raiser cooldown
	// March is Advance's live door hint. P-5.8: within 12 of the march door the
	// REACH TOOL holds — the clinch swap is suppressed and the volley fires
	// point-blank; the funnel rewards the pierce, not the poke.
	March func() (data.Position, bool)
}

func NewFight() *Fight { return &Fight{blacklist: map[data.UnitID]time.Time{}} }

// Recalibrated clears the ranged-skill audit — P-7.1 PROBEs after RECLAIM
// because the weapons changed hands, and a demotion measured on the OLD hands
// proves nothing about the new (the reviewer's finding 3: a once-demoted bow
// stayed plain-arrow forever across every reclaim).
func (f *Fight) Recalibrated() {
	f.rangedDead, f.rangedShots, f.rangedFlinch = false, 0, 0
}

func (f *Fight) Name() string { return "fight" }

func (f *Fight) Demand(s *percept.Snapshot) *arbiter.Demand {
	// Stop committing to a fight while wounded — hand the tick to Flee/EscapeTP early,
	// not at 5% (the bleed-out). With no potions the bar is higher: retreat sooner.
	if !s.Valid || s.Me.InTown || time.Now().Before(fightCoolUntil) {
		return nil // the sight divergence cool: the pick proved nobody sighted (21:43)
	}
	// P-2.0 + P-1.12: the stand-down judges by the BLOOD ORACLE — she fights
	// any pack she is out-sustaining, and only a genuinely collapsing runway
	// (TTD < 8 s) with real pressure hands the moment to Flee. The lone
	// chaser is fought at any blood; fatigue (P-2.11) suspends the stand-down
	// entirely so she blasts instead of orbiting.
	// P-2.-1: the stand-down honors the FLEE FLOOR — above 33 blood Flee
	// cannot bid, so Fight never hands it the moment (a dead band otherwise).
	// P-2.-2: it NEVER binds a brawler — a brawler who stops swinging is dead.
	if !brawlerMode && TimeToDie(s) < 8 && s.Me.HPPct < 33 && !time.Now().Before(fleeFatigueUntil) {
		near25 := 0
		for _, e := range s.Enemies {
			if e.Walled { // WARNING 10
				continue
			}
			if chebyshev(s.Me.Pos, e.Pos) <= 25 {
				near25++
			}
		}
		if near25 != 1 {
			return nil // a collapsing runway in a pack is Flee's moment
		}
	}
	// Naked with a corpse holding her gear: punching the moor is denial, not combat —
	// stand down and let Reclaim (higher class) own every moment until the bow is back.
	if s.Me.WeaponKind == "none" && s.Me.CorpseFound {
		return nil
	}
	// THE EXP ORACLE shrinks the hunt: on outleveled ground (mlvl 5+ below her),
	// killing pays nothing — P-5.7 FORCED MARCH, refined 11:15 (the owner:
	// "shoot more than moving if enemies are nearby"): anything within 10 is
	// SHOT — nearby aggro dies, the far field is ignored, and the march owns
	// the ground between camps.
	radius := 45
	if !ExpWorthwhile(s.Me.Level, s.Me.Area) {
		// THE CORRIDOR LAW (owner, 02:35 night 2: "killing only monsters in
		// its way, but otherwise prioritizing progressing the map"): on
		// outleveled ground the corridor binds EVERYONE, brawler included.
		// The old brawler exemption was tuned when Cold Plains was food — at
		// level 16 the 45-tile eyesight is a leash: every pack preempts the
		// march by class and he farms nothing for hours. Aggression on worthy
		// ground stays 45; conquered ground belongs to the march.
		radius = 10
	}
	if time.Now().Before(crossingBracketUntil) {
		// P-5.10 refined (10:34 screenshot: THIRTY at the mouth are not
		// lingerers): the bracket holds against few; a door CAMP is a fight.
		horde := 0
		for _, e := range s.Enemies {
			if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 10 {
				horde++
			}
		}
		if horde < 6 {
			radius = 5
		}
	}
	best := radius + 1
	for _, e := range s.Enemies {
		if e.Walled { // WARNING 10 (owner, 00:40): walled enemies don't exist, period
			continue
		}
		if until, bl := f.blacklist[e.ID]; bl && time.Now().Before(until) {
			continue
		}
		if d := chebyshev(s.Me.Pos, e.Pos); d < best {
			best = d
		}
	}
	if best > radius {
		return nil
	}
	return &arbiter.Demand{Who: f.Name(), Class: arbiter.ClassFight,
		Urgency: 1.0 - float64(best)/50,
		Commit:  arbiter.Commitment{MinHold: 3 * time.Second, SwitchMargin: 0.2}}
}

func (f *Fight) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	// (Re)select target: keep the current one while it lives and is un-blacklisted.
	// A flinch observed here IS the volley's evidence — no strike ever waits for it.
	alive := false
	for _, e := range s.Enemies {
		if e.ID == f.target {
			alive = true
			f.targetPos = e.Pos
			if e.Mode == uint32(mode.NpcGettingHit) {
				if f.lastKey != 0 {
					f.rangedFlinch++ // the calibrated skill provably hurts things
				}
				f.volleys, f.noEvid = 0, 0
			}
			break
		}
	}
	if !alive || f.target == 0 {
		f.target, f.j, f.noEvid = 0, nil, 0
		f.volleys, f.aimDX, f.aimDY = 0, 0, 0
		// TARGET BY SIGHT FIRST (the owner: "prioritize enemies outside, only go
		// inside when it can"): an enemy in a cabin reads '8 tiles away' THROUGH the
		// wall and wins a nearest-first pick — then the approach dances on the wall.
		// Anything with a clear arrow line outranks everything walled, at any range.
		// The EXP ORACLE's radius applies here too — no chasing trash on old ground.
		// ONE radius with Demand (10, reviewer 10): a target that never earned
		// the bid must never win the selection.
		radius := 45
		if !ExpWorthwhile(s.Me.Level, s.Me.Area) {
			radius = 10 // THE CORRIDOR LAW: conquered ground belongs to the march
		}
		if time.Now().Before(crossingBracketUntil) {
			horde := 0
			for _, e := range s.Enemies {
				if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 10 {
					horde++
				}
			}
			if horde < 6 { // few: cross through. A camp: fight it (10:34).
				radius = 5
			}
		}
		// PACK-AWARE pick: score = distance + 3×(bodies within 8 of the candidate).
		// Nearest-first used to elect the CENTER of a 20-stack and she charged it
		// (the owner: "charging headlong into 20+ stacks"); a straggler at 20 tiles
		// now beats a horde at 10. Distance still gates on the hunt radius.
		// WARNING 10 (the owner closed the cabin door, 00:40: "monsters behind
		// walls don't exist"): only SIGHTED enemies may be targets — no journey
		// ever chases what no arrow can reach. The map tour walks the rooms;
		// whatever steps into the line dies.
		var bestClear percept.EnemyRef
		cScore := 1 << 30
		haveClear := false
		for _, e := range s.Enemies {
			if e.Walled {
				continue
			}
			if until, bl := f.blacklist[e.ID]; bl && time.Now().Before(until) {
				continue
			}
			d := chebyshev(s.Me.Pos, e.Pos)
			if d > radius {
				continue
			}
			pack := 0
			for _, o := range s.Enemies {
				if chebyshev(e.Pos, o.Pos) <= 8 {
					pack++
				}
			}
			sc := d + 3*pack
			if raisers[e.NPC] {
				// P-1.15 THE RAISER DIES FIRST (the owner: "we don't want her
				// killing the same fallen again and again"): a raising family
				// outranks every non-raiser at any distance in the radius —
				// the bonus dwarfs every d+pack sum a 45-tile world can make.
				sc -= 1000
			}
			if sc < cScore && losClear(ctx.Grid, s.Me.Pos, e.Pos) {
				bestClear, cScore, haveClear = e, sc, true
			}
		}
		if !haveClear {
			// THE SIGHT DIVERGENCE (21:43: Demand bids on unwalled enemies
			// while this stricter losClear pick had nobody — grant/done churn
			// and 20s mute-fight windows the owner watched as "bouts of
			// idleness"). No sighted target = Fight COOLS 2s in writing so
			// the march owns the wheel instead of the churn.
			fightCoolUntil = time.Now().Add(2 * time.Second)
			ctx.Led.Append(verbs.Outcome{Verb: "fight", Holder: f.Name(), Result: verbs.ResRefused,
				Evidence: "no sighted target within radius — fight cools 2s, the march proceeds"})
			return Done
		}
		f.target, f.targetPos = bestClear.ID, bestClear.Pos
	}

	d := chebyshev(s.Me.Pos, f.targetPos)
	contact := 1 << 30
	var contactPos data.Position
	for _, e := range s.Enemies {
		if e.Walled { // WARNING 10: a cabin dweller is not in contact — the
			continue // point-blank volley into the logs was this line's absence
		}
		if dd := chebyshev(s.Me.Pos, e.Pos); dd < contact {
			contact, contactPos = dd, e.Pos
		}
	}

	// ---- THE BOW DOCTRINE (P-1.7 as of 11:50: the javelin CQB switch is
	// DROPPED — the bow holds at every range, the skill on every shot, and
	// the javelin set serves only a dry quiver). ----
	switch s.Me.WeaponKind {
	case "bow":
		if s.Me.Arrows == 0 {
			// The quiver ran dry and vanished: every "shot" from here is a whiff at
			// air. The javelin set is a real weapon — swap and fight with the truth.
			f.trySwap(ctx)
			return Running
		}
		// The shot decision, made ONCE up here: the bow has NO minimum range and the
		// clinch is not a cease-fire (the owner: "attack using the bow even if the
		// enemies are close" — she used to stand mute for up to 1.5s waiting on the
		// swap cooldown while something chewed her).
		// P-1.13 THE SKILL IS THE WORKHORSE (the owner: "mana arrows are cheap
		// and we have mana stolen per hit and per kill"): the skill fires on
		// EVERY shot above 10% mana — the basic arrow is the reserve below
		// that line, not the default. The steal refills the pool.
		var key byte
		if ctx.Cap != nil && ctx.Cap.Reach != nil {
			switch {
			case s.Me.MPPct > 75:
				// P-1.13 ABOVE 75% THE SKILL IS LAW (the owner, 23:00): a full
				// pool fires the skill on every shot — a demotion-benched skill
				// re-arms here and re-proves, never mutes a full pool for a run.
				if f.rangedDead {
					f.rangedDead, f.rangedShots, f.rangedFlinch = false, 0, 0
				}
				key = ctx.Cap.Reach.Key
			case !f.rangedDead && s.Me.MPPct > 10:
				key = ctx.Cap.Reach.Key
			}
		}
		if contact <= 3 {
			// P-1.7 (the owner, 11:50: "drop her javelin cqb switch, her bow is
			// superior for now"): the bow HOLDS in the clinch — point-blank
			// volleys, no swap. The javelin serves only a dry quiver now.
			f.strike(ctx, f.nearestID(s), contactPos, key)
			return Running
		}
		if contact <= 6 {
			// P-1.8 STAND AND LOOSE: healthy blood holds its ground and shoots —
			// clearing the pack IS the defense. Kite only wounded AND pressed
			// (the OR cowered her along walls: 2 fallen within 7 outran the bow).
			press := 0
			for _, e := range s.Enemies {
				if e.Walled { // WARNING 10
					continue
				}
				if chebyshev(s.Me.Pos, e.Pos) <= 7 {
					press++
				}
			}
			if press >= 2 && s.Me.HPPct < 50 {
				if !kiteAway(ctx, s, contactPos, 1000*time.Millisecond) {
					// CORNERED: no open bearing — the wall wins the footwork, so
					// win the fight instead: shoot the nearest tooth point-blank.
					f.strike(ctx, f.nearestID(s), contactPos, key)
				}
				return Running
			}
			f.strike(ctx, f.nearestID(s), contactPos, key)
			return Running
		}
		if d > 25 {
			// HALF-STEP the approach — ~10 tiles per stride, reassess between (the
			// owner: "charging headlong into 20+ stacks"). The reflexes (dodge, flee,
			// breakout) get a bid between every step of a long approach now.
			step := 10
			mid := data.Position{X: s.Me.Pos.X + (f.targetPos.X-s.Me.Pos.X)*step/d,
				Y: s.Me.Pos.Y + (f.targetPos.Y-s.Me.Pos.Y)*step/d}
			verbs.Stride{To: mid, Hold: 700 * time.Millisecond}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
			return Running
		}
		if !losClear(ctx.Grid, s.Me.Pos, f.targetPos) {
			// A wall owns the arrow's line. This target won the pick, so EVERY target
			// is walled (sight-first selection) — enter properly or not at all: the
			// planner walks the door ("only go inside when it can"); a wall-slide here
			// was the cabin back-and-forth the owner watched. No route, or arrived
			// still blind → blacklist and move on.
			if f.j == nil || chebyshev(f.j.Goal, f.targetPos) > 6 {
				f.j = journey.New(ctx.GR, ctx.Grid, f.targetPos, f.Name())
			}
			st := f.j.Step(ctx.M, ctx.P, ctx.Led)
			if st.State == journey.Stalled || st.State == journey.NoPath ||
				(st.State == journey.Arrived && !losClear(ctx.Grid, s.Me.Pos, f.targetPos)) {
				f.blacklist[f.target] = time.Now().Add(30 * time.Second)
				f.target, f.j = 0, nil
			}
			return Running
		}
		// SHOOT. Basic arrow (plain attack, zero mana) is the workhorse; the bow skill
		// whenever the pool can afford a cast (key computed once at the case top) —
		// never spammed bone-dry, never after the evidence audit demoted it.
		f.strike(ctx, f.target, f.targetPos, key)
		if key != 0 {
			f.rangedShots++
			if f.rangedShots >= 15 && f.rangedFlinch == 0 {
				f.rangedDead = true // fifteen "shots", zero flinches ever: that is no weapon
			}
		}
		return Running

	case "melee":
		// The strike key is the OWNER-DECLARED melee skill (config beats
		// inference on a scrambled mod) — and P-1.13 applies to CONTACT too
		// (the owner, 04:16: "double swing is f3, use it like magic arrows
		// but melee"): the skill on every swing while mana holds above 10%,
		// plain attack as the reserve below, no exceptions above 75%.
		var mk byte
		if ctx.Cap != nil && ctx.Cap.Contact != nil {
			// TINY-POOL DOCTRINE (11:20: three vault whiffs, all mana-dry —
			// Double Swing was drinking the 4-point pool and FIZZLING below
			// its cost, silent do-nothing right-clicks): on a tiny pool the
			// skill fires only at a full tank; the pool belongs to the LEAP.
			// Plain attack is the bread — it costs nothing and always swings.
			// Cutoff 8, not 20 (11:41: the honest read is 19 base — a REAL
			// pool that missed the old gate by one; the doctrine was written
			// for the truly-4 case, and mana-per-hit sustains everything above it).
			if s.Me.MaxMana >= 8 {
				if s.Me.MPPct > 10 {
					mk = ctx.Cap.Contact.Key
				}
			} else if s.Me.MPPct >= 95 && (ctx.Cap == nil || ctx.Cap.Vault == nil) {
				mk = ctx.Cap.Contact.Key // no leap to save for: full tank may swing
			}
			if time.Now().Before(vaultHungerUntil) {
				mk = 0 // the pool is spoken for: the leap eats first (13:39)
			}
		}
		// A dry bow set is no bow at all: while Arrows==0 the javelins ARE the build —
		// chase and stab instead of kiting toward a weapon that whiffs at air.
		// A char with NO bow at all is definitionally dry (04:07: the barbarian
		// reads arrows=-1, dryBow read false, and the melee path KITED him
		// along the river like a javelin-zon waiting on a bow that will never
		// exist — melee is all he has; close the gap and swing).
		dryBow := s.Me.Arrows == 0 || !s.Me.HasBow
		// P-1.7 (the owner, 11:50: the javelin CQB switch is DROPPED): holding
		// the javelins with a live quiver, she swaps back to the bow at ONCE —
		// any range, any contact — and stabs only while the swap pends. Never
		// a mute cycle.
		if !dryBow && s.Me.HasBow {
			f.trySwap(ctx)
			if contact <= 4 {
				f.strike(ctx, f.nearestID(s), contactPos, mk)
			}
			return Running
		}
		// P-2.10 THE MUTUAL-VETO DETECTOR (the advisor, 2026-07-20: "Travel
		// says combat is active; combat says I cannot attack this target yet;
		// nobody acts — a tiny bureaucratic collapse"): an IN-REACH lock that
		// has produced no issued input for 1.2s is not a fight, it is a
		// hostage-taking of the actuator. Quarantine it briefly and pick
		// another victim; if none remains the Demand dies and the march
		// resumes. Liveness = the newer of lock-acquisition and last issued
		// click (strike() and assess(ResDone) both stamp it), so a fresh
		// engagement gets its full 1.2s before judgment.
		if f.watchTarget != f.target {
			f.watchTarget, f.watchSince = f.target, time.Now()
		}
		liveAt := f.lastStrikeAt
		if f.watchSince.After(liveAt) {
			liveAt = f.watchSince
		}
		if d <= 8 && time.Since(liveAt) > 1200*time.Millisecond {
			// P-2.10 writes its quarantines (rule zero, 21:43 batch): silent
			// retargeting was indistinguishable from statue-mode in the log.
			ctx.Led.Append(verbs.Outcome{Verb: "fight", Holder: f.Name(), Result: verbs.ResRefused,
				Evidence: fmt.Sprintf("P-2.10 quarantine: target %d silent 1.2s at d=%d — retargeting", int(f.target), d)})
			f.blacklist[f.target] = time.Now().Add(2 * time.Second)
			f.target, f.j = 0, nil
			return Running
		}
		// THE ATTACK COMMAND IS THE CHASE (the owner, 04:12: "make it a
		// priority for him to attack — he just runs around monsters"): a
		// melee char clicks the MONSTER, not the ground beside it — the
		// game's own attack command pursues and swings, tracking the target
		// (HoverStrike, the M4 verb, finally reliable under the hover pump).
		// Plain strides only close truly long gaps.
		if d > 10 && contact > 10 {
			verbs.Stride{To: f.targetPos, Hold: 700 * time.Millisecond}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
			return Running
		}
		// THE RING IS THE QUEUE (the owner, 04:22: "a small delay after
		// striking a monster down — can we have a queue so it keeps going?"):
		// in true contact, precision is wasted motion — a positional swing
		// hits whatever stands in the arc, no hover, no sweep, 350ms cadence.
		// When one body drops, the next is already in range: the transition
		// disappears. HoverStrike remains the PURSUIT verb only.
		lockIsRaiser := false
		for _, e := range s.Enemies {
			if e.ID == f.target && raisers[e.NPC] {
				lockIsRaiser = true
				break
			}
		}
		if contact <= 3 && !lockIsRaiser {
			// THE RING YIELDS TO THE NECROMANCER (10:38: he ground the same
			// fallen forever while the shaman rezzed behind — P-1.15 bypassed
			// by the ring-queue shortcut). A raiser lock is PURSUED through
			// the ring; ordinary rings get the sweepless positional swings.
			f.strike(ctx, f.nearestID(s), contactPos, mk)
			return Running
		}
		// P-2.11(3) THE LEAP TO THE NECROMANCER (the owner, 11:12: "the leap
		// may also be used to get to resurrectors easily — so he wouldn't be
		// blocked by their resurrecting minions"): the minion wall is the
		// raiser's whole defense, and a vault makes it scenery — land beside
		// the shaman and the ring-yield rule above finishes the sentence.
		// Grid-vouched landing 2 tiles short; one try per 8s, a whiff falls
		// through to the walking pursuit.
		if lockIsRaiser && d >= 5 && d <= 16 && canVault(ctx, s) &&
			time.Since(f.vaultAt) > 8*time.Second {
			land := f.targetPos
			if land.X > s.Me.Pos.X {
				land.X -= 2
			} else if land.X < s.Me.Pos.X {
				land.X += 2
			}
			if land.Y > s.Me.Pos.Y {
				land.Y -= 2
			} else if land.Y < s.Me.Pos.Y {
				land.Y += 2
			}
			if vaultLandable(ctx, land) {
				f.vaultAt = time.Now()
				verbs.Vault{To: land, Key: ctx.Cap.Vault.Key, SkillID: int(ctx.Cap.Vault.Skill)}.
					Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
				return Running
			}
		}
		// THE LOCK AND THE BITER (the owner, 04:19: "meaningfully attack any
		// creature dumb enough to approach, rather than run back and forth"):
		// re-picking nearest EVERY step ping-ponged him between half-pursuits.
		// The brawler LOCKS one victim until it dies; only a creature that
		// closes to true contact while the lock stands clearly farther earns
		// the new lock (hysteresis: no flapping between similar ranges).
		// A RAISER LOCK IS NEVER STOLEN BY A BITER (11:12 audit): adopting the
		// minion that bit him mid-pursuit is the resurrection loop wearing a
		// different hat — the shaman rezzes the biter's brothers while he
		// turns his back on it.
		if contact <= 5 && d > contact+3 && !lockIsRaiser {
			if nid := f.nearestID(s); nid != 0 {
				f.target, f.targetPos = nid, contactPos // the biter IS the fight now
			}
		}
		o := verbs.HoverStrike{Target: f.target, TargetPos: f.targetPos, SelectKey: mk, Volley: true}.
			Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
		if o.Result == verbs.ResWhiff && time.Since(f.lastStrikeAt) >= 350*time.Millisecond {
			// THE WHIFF STILL SWINGS (the owner, 11:15: "still kind of runs
			// around instead of killing"): 31 hover whiffs in barb29's ten
			// minutes, each a cycle with NO click issued — he walked beside
			// monsters looking busy. A missed hover downgrades to a
			// march-swing AT the target's position (the right-click melee
			// walk: the game closes and swings); the aimed strike resumes
			// the moment hover confirms.
			volleyAt(ctx, f.targetPos, mk, false)
			f.lastStrikeAt = time.Now()
		}
		f.assess(o)
		return Running
	}

	// ---- No bow in the picture: the pre-bowzon paths (throw kiting, then melee). ----
	canThrow := ctx.Cap != nil && ctx.Cap.Throw != nil
	if canThrow && d >= 9 && d <= 22 && losClear(ctx.Grid, s.Me.Pos, f.targetPos) {
		f.strike(ctx, f.target, f.targetPos, ctx.Cap.Throw.Key)
		return Running
	}
	if d > 4 {
		if f.j == nil || chebyshev(f.j.Goal, f.targetPos) > 6 {
			f.j = journey.New(ctx.GR, ctx.Grid, f.targetPos, f.Name())
		}
		st := f.j.Step(ctx.M, ctx.P, ctx.Led)
		if st.State == journey.Stalled || st.State == journey.NoPath {
			f.blacklist[f.target] = time.Now().Add(30 * time.Second)
			f.target, f.j = 0, nil
		}
		return Running
	}
	var key byte
	if canThrow {
		key = ctx.Cap.Throw.Key
	} else if ctx.Cap != nil && ctx.Cap.Contact != nil {
		key = ctx.Cap.Contact.Key
	}
	f.strike(ctx, f.target, f.targetPos, key)
	return Running
}

// volleyAt fires one WALK-PROOF shot at the projected world position — no hover
// sweep. The sweep was up to 800ms of standing still per attempt and whiffed on
// every fast mover (the owner's "bouts of not doing anything while taking hits").
// A skill right-click casts at the cursor and can never walk; a SHIFT+left attack
// swings/fires toward the cursor and can never walk. Precision is the projectile's
// job. The tome readback guard survives: right-clicks never fire a book.
func volleyAt(ctx *Ctx, pos data.Position, key byte, skipSelect bool) {
	d := ctx.GR.GetData()
	me := d.PlayerUnit.Position
	bx := int(float32((pos.X-me.X)-(pos.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((pos.X-me.X)+(pos.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2
	// Clamp INSIDE the window preserving direction — the arrow flies the line anyway.
	if bx < 20 {
		bx = 20
	} else if bx > ctx.GR.GameAreaSizeX-20 {
		bx = ctx.GR.GameAreaSizeX - 20
	}
	if by < 20 {
		by = 20
	} else if by > ctx.GR.GameAreaSizeY-20 {
		by = ctx.GR.GameAreaSizeY - 20
	}
	ctx.M.MoveStop()
	// EVERY VOLLEY WRITES (rule zero at death scale, 13:03: six Survive
	// grants, 26 seconds, ZERO ledger outcomes — whether he was fighting
	// mutely or standing mutely was UNKNOWABLE from the log). One line per
	// swing; the post-mortem reads the fight instead of guessing it.
	ctx.Led.Append(verbs.Outcome{Verb: "volley", Holder: "volley", Result: verbs.ResDone,
		Evidence: fmt.Sprintf("at (%d,%d) key=%d", pos.X, pos.Y, key)})
	if key != 0 {
		if !skipSelect {
			ctx.M.PressKey(key)
			time.Sleep(50 * time.Millisecond)
		}
		rs := ctx.GR.GetData().PlayerUnit.RightSkill
		if rs == skill.TomeOfIdentify || rs == skill.ScrollOfIdentify ||
			rs == skill.TomeOfTownPortal || rs == skill.ScrollOfTownPortal {
			ctx.M.PressKey(key)
			time.Sleep(80 * time.Millisecond)
			rs = ctx.GR.GetData().PlayerUnit.RightSkill
		}
		if rs != skill.TomeOfIdentify && rs != skill.ScrollOfIdentify &&
			rs != skill.TomeOfTownPortal && rs != skill.ScrollOfTownPortal {
			ctx.M.ClickRight(bx, by)
			return
		}
		// A tome refuses to disarm: fall through to the plain attack below.
	}
	ctx.M.AttackClick(bx, by)
}

// strike fires one volley shot: paced to the attack animation (~350ms), selection
// skipped when the same key was proven moments ago. Evidence arrives passively via
// the snapshot stream (flinches); eight silent volleys blacklist the target.
func (f *Fight) strike(ctx *Ctx, target data.UnitID, pos data.Position, key byte) {
	if time.Since(f.lastStrikeAt) < 350*time.Millisecond {
		return // the animation is still playing; clicking now buys nothing
	}
	skip := key != 0 && key == f.lastKey && time.Since(f.lastStrikeAt) < 2*time.Second
	volleyAt(ctx, pos, key, skip)
	f.lastKey, f.lastStrikeAt = key, time.Now()
	f.volleys++
	if f.volleys >= 8 {
		// Eight fired volleys and the target never once flinched in the stream:
		// phantom or unhittable — stop feeding it arrows.
		f.blacklist[target] = time.Now().Add(30 * time.Second)
		f.target, f.j, f.noEvid, f.volleys = 0, nil, 0, 0
	}
}

// trySwap presses the weapon-swap key (rate-limited); verification is the next
// snapshot's WeaponKind — the closed loop lives in perception, not hope.
func (f *Fight) trySwap(ctx *Ctx) {
	if time.Since(f.swapAt) < 1500*time.Millisecond {
		return
	}
	ctx.M.KeyLane().Press(ctx.SwapKey)
	f.swapAt = time.Now()
	// The right-skill is PER WEAPON SET: a key proven seconds ago on the old set
	// proves nothing about the new one — SkipSelect must never span a swap.
	f.lastKey = 0
}

func (f *Fight) nearestID(s *percept.Snapshot) data.UnitID {
	best, bd := data.UnitID(0), 1<<30
	for _, e := range s.Enemies {
		if e.Walled { // WARNING 10: never strike what the wall owns
			continue
		}
		if dd := chebyshev(s.Me.Pos, e.Pos); dd < bd {
			best, bd = e.ID, dd
		}
	}
	return best
}

func (f *Fight) assess(o verbs.Outcome) {
	if o.Result == verbs.ResDone {
		f.noEvid = 0
		f.lastStrikeAt = time.Now() // an issued click IS liveness — P-2.10 feeds on this
		return
	}
	f.noEvid++
	if f.noEvid >= 4 {
		f.blacklist[f.target] = time.Now().Add(30 * time.Second)
		f.target, f.j, f.noEvid = 0, nil, 0
	}
}

// ---------------------------------------------------------------- Loot (ClassLoot)

type Loot struct {
	target   data.UnitID
	failures map[data.UnitID]int
	ban      map[data.UnitID]time.Time
	j        *journey.Journey
}

func NewLoot() *Loot { return &Loot{failures: map[data.UnitID]int{}, ban: map[data.UnitID]time.Time{}} }

func (l *Loot) Name() string { return "loot" }

// wanted scores a ground item for THIS character's needs (the self-model speaking):
// weapons dominate while weaponless; a bowzon running dry hungers for arrows;
// potions and gold always matter a little. SPACE GATES THE WANT: a full bag turns
// every gear pickup into a 3-fail ban cycle (the owner watched it) — gold always
// fits, potions ride the belt, gear needs real cells.
func (l *Loot) wanted(s *percept.Snapshot, it percept.ItemRef) float64 {
	n := it.Name
	gearRoom := s.Me.InvFree >= 8 // the largest footprints are 2x4
	switch {
	case !s.Me.Armed && (contains(n, "Dagger") || contains(n, "Sword") || contains(n, "Axe") ||
		contains(n, "Club") || contains(n, "Javelin") || contains(n, "Spear") || contains(n, "Wand") ||
		contains(n, "Mace") || contains(n, "Scepter")):
		if gearRoom {
			return 0.95
		}
	case s.Me.Arrows == 0 && (contains(n, "Arrow") || contains(n, "Quiver")):
		// Quivers SELF-REPLENISH on this mod (owner-confirmed) — ammo only matters
		// when the quiver itself is GONE (vanished/never had one). No stockpiling.
		if s.Me.InvFree >= 4 {
			return 0.9
		}
	case it.Quality >= 7: // UNIQUE (and crafted) — the only quality tier picked
		// UNIQUES ONLY (the owner, 13:08: "it also picked some weird items,
		// like some whites and rares — make it so only uniques are picked for
		// now"). The set/rare tier (5-6) and the magic tier (4) are OFF the
		// docket until the owner re-opens them; the naked-rearm and dry-quiver
		// cases above survive (a weapon in the hand outranks loot doctrine).
		if s.Me.InvFree >= 2 {
			return 0.85
		}
	case n == "Gold":
		return 0.4 // gold has no footprint
	}
	return 0
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func (l *Loot) pick(s *percept.Snapshot) (percept.ItemRef, float64, bool) {
	var best percept.ItemRef
	bestScore := 0.0
	for _, it := range s.Items {
		if until, banned := l.ban[it.ID]; banned && time.Now().Before(until) {
			continue
		}
		w := l.wanted(s, it)
		if w <= 0 || chebyshev(s.Me.Pos, it.Pos) > 30 {
			continue
		}
		// GUARDED TREASURE (the cabin death): danger is assessed around the ITEM,
		// not just around her. A bauble with a welcoming committee is not loot.
		guards := 0
		for _, e := range s.Enemies {
			if chebyshev(it.Pos, e.Pos) <= 15 {
				guards++
			}
		}
		if guards >= 3 {
			continue
		}
		if w > bestScore {
			best, bestScore = it, w
		}
	}
	return best, bestScore, bestScore > 0
}

func (l *Loot) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.HPPct < 40 {
		return nil
	}
	// No looting with contact pressure — that is how pickups become deaths. The bid
	// bar (8) sits OUTSIDE Step's yield bar (6): run 26 logged ~40 grant/done flips
	// in one second from an enemy standing at 7 — bid thresholds must enclose yield
	// thresholds or the arbiter churns. EXCEPT TREASURE (04:14, the brawler:
	// "he's skipping uniques" — his kills drop where the next enemies stand,
	// so an all-loot pressure veto means a brawler never loots): set+ finds
	// bid through pressure; Step's yield bar still guards the actual grab.
	pressed := false
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 8 {
			pressed = true
			break
		}
	}
	if it, score, ok := l.pick(s); ok {
		if pressed && it.Quality < 7 { // uniques-only doctrine (13:08)
			return nil // ordinary goods can wait out the pressure
		}
		// THE TREASURE GRAB: ClassFight starves ClassLoot whenever anything hostile
		// is within 45 — in the moor that is ALWAYS, so a rare short bow lay ignored
		// while she volleyed trash (the owner: "she didn't care at all"). Rare+ finds
		// and ammo-for-a-dry-quiver bid IN the fight class: urgency does the risk
		// arithmetic — a fight with teeth close still outbids (Fight at contact 5 is
		// ~0.9), a fight against distant stragglers loses to treasure.
		treasure := it.Quality >= 7 || // uniques-only doctrine (13:08)
			(s.Me.Arrows == 0 && (contains(it.Name, "Arrow") || contains(it.Name, "Quiver")))
		if treasure {
			return &arbiter.Demand{Who: l.Name(), Class: arbiter.ClassFight,
				Urgency: 0.85,
				Commit:  arbiter.Commitment{MinHold: 2 * time.Second, SwitchMargin: 0.3}}
		}
		return &arbiter.Demand{Who: l.Name(), Class: arbiter.ClassLoot, Urgency: score,
			Commit: arbiter.Commitment{MinHold: 2 * time.Second, SwitchMargin: 0.3}}
	}
	return nil
}

func (l *Loot) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	// Yield under pressure mid-approach: enemies closing in flip the priority back to
	// Fight/Flee naturally — pressing on toward a bauble is how pickups become deaths.
	// Yield bar (6) sits INSIDE the bid bar (8): hysteresis, not churn.
	for _, e := range s.Enemies {
		if chebyshev(s.Me.Pos, e.Pos) <= 6 {
			return Done
		}
	}
	it, _, ok := l.pick(s)
	if !ok {
		l.j = nil
		return Done
	}
	d := chebyshev(s.Me.Pos, it.Pos)
	if d > 3 {
		// JOURNEY, not a blind stride: an item inside a house reads as "5 tiles away"
		// through the wall — she flicker-hovered it and moved on (the owner's report).
		// The planner walks the door; proximity is not reachability.
		if ctx.Grid != nil {
			if l.j == nil || chebyshev(l.j.Goal, it.Pos) > 4 {
				l.j = journey.New(ctx.GR, ctx.Grid, it.Pos, l.Name())
				l.j.Arrive = 2
			}
			st := l.j.Step(ctx.M, ctx.P, ctx.Led)
			if st.State == journey.Stalled || st.State == journey.NoPath {
				l.ban[it.ID] = time.Now().Add(60 * time.Second)
				l.j = nil
			}
		} else {
			verbs.Stride{To: it.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, l.Name())
		}
		return Running
	}
	o := verbs.Pickup{Target: it.ID, TargetPos: it.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, l.Name())
	if o.Result != verbs.ResDone {
		l.failures[it.ID]++
		if l.failures[it.ID] >= 3 {
			l.ban[it.ID] = time.Now().Add(60 * time.Second)
			delete(l.failures, it.ID)
		}
	}
	return Running
}

// ---------------------------------------------------------------- Explore (ClassExplore)

// Explore v2: a PERSISTENT HEADING, not a circle (the owner watched v1's rotating
// offsets orbit in place — a rotation around the current position IS a circle
// generator). Hold one bearing while strides succeed; on a wall, turn 45° and
// remember it; the heading survives across grants. The atlas frontier replaces
// this in M8 with real coverage knowledge.
type Explore struct {
	heading int  // index into the 8 bearings
	set     bool // heading initialized from the road's outward direction
	// Frontier is Advance's hint (P-5F): the itinerary leg the march owns for
	// this level. Exploration exists only there — behind it, ground is
	// corridor and the march owns every idle moment. Nil-safe: no itinerary,
	// classic wander.
	Frontier func(level int) area.ID
	// P-5F map tour: the maphack knows every room (the owner, 23:58: "aint
	// it weird that she needs to explore despite having a maphack?") — the
	// wander is a nearest-first tour of unvisited rooms, not a wall-bounce.
	tourArea area.ID
	visited  map[int]bool
	tourIdx  int
	goalAt   time.Time
}

var bearings = []data.Position{{X: 35, Y: 0}, {X: 25, Y: 25}, {X: 0, Y: 35}, {X: -25, Y: 25},
	{X: -35, Y: 0}, {X: -25, Y: -25}, {X: 0, Y: -35}, {X: 25, Y: -25}}

func (x *Explore) Name() string { return "explore" }

func (x *Explore) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown {
		return nil
	}
	// HP never regenerates: with potions the 50% bar waits for a drink, but with a dry
	// belt AND a dead tome (Withdraw cooling) waiting is FOREVER — keep hunting
	// carefully above 25% rather than standing in a field that will never heal her.
	if s.Me.HPPct < 50 && !(s.Me.HealPots == 0 && s.Me.HPPct >= 25) {
		return nil
	}
	// P-5F THE FRONTIER LAW: the wander belongs to the frontier alone —
	// behind it the march owns every idle moment (the Stony→Cold drift).
	if x.Frontier != nil {
		if fr := x.Frontier(s.Me.Level); fr != 0 && fr != s.Me.Area {
			return nil
		}
	}
	return &arbiter.Demand{Who: x.Name(), Class: arbiter.ClassExplore, Urgency: 0.1,
		Commit: arbiter.Commitment{MinHold: 4 * time.Second}}
}

func (x *Explore) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	CarryReach(ctx) // P-5.9: the wander walks with the bow out too

	// P-5F THE MAP TOUR: the maphack already knows every room of this area —
	// walking blind past a map oracle is absurd (the owner, 23:58). Tour the
	// unvisited rooms nearest-first; a room unreached in 45 s is skipped; a
	// fully toured area hands the moment back to the march.
	if ad, ok := ctx.GR.GetData().Areas[s.Me.Area]; ok && len(ad.Rooms) > 0 {
		if x.tourArea != s.Me.Area {
			x.tourArea, x.visited, x.tourIdx = s.Me.Area, map[int]bool{}, -1
		}
		for i, r := range ad.Rooms {
			if !x.visited[i] &&
				s.Me.Pos.X >= r.Position.X && s.Me.Pos.X < r.Position.X+r.Width &&
				s.Me.Pos.Y >= r.Position.Y && s.Me.Pos.Y < r.Position.Y+r.Height {
				x.visited[i] = true // standing in it = streamed = seen
			}
		}
		if x.tourIdx >= 0 && time.Since(x.goalAt) > 45*time.Second {
			x.visited[x.tourIdx] = true // unreachable room: skipped, not besieged
			x.tourIdx = -1
		}
		if x.tourIdx < 0 || x.visited[x.tourIdx] {
			best, bd := -1, 1<<30
			for i, r := range ad.Rooms {
				if x.visited[i] {
					continue
				}
				c := data.Position{X: r.Position.X + r.Width/2, Y: r.Position.Y + r.Height/2}
				if dd := chebyshev(s.Me.Pos, c); dd < bd {
					best, bd = i, dd
				}
			}
			if best < 0 {
				return Done // the area is toured — the march decides what's next
			}
			x.tourIdx, x.goalAt = best, time.Now()
		}
		r := ad.Rooms[x.tourIdx]
		c := data.Position{X: r.Position.X + r.Width/2, Y: r.Position.Y + r.Height/2}
		if chebyshev(s.Me.Pos, c) <= 6 {
			x.visited[x.tourIdx] = true
			x.tourIdx = -1
			return Running
		}
		slideStride(ctx, c, 1500*time.Millisecond, 1, x.Name())
		return Running
	}

	// No map data for this area: the blind heading walk survives as fallback.
	if !x.set {
		x.heading, x.set = 5, true // southwest-ish: away from the town gate into the moor
	}
	o := bearings[x.heading%len(bearings)]
	tgt := data.Position{X: s.Me.Pos.X + o.X, Y: s.Me.Pos.Y + o.Y}
	res := verbs.Stride{To: tgt}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, x.Name())
	if res.Result != verbs.ResDone {
		x.heading++ // walled: one 45° turn, then HOLD the new line
	}
	return Running
}

// ---------------------------------------------------------------- Respawn (ClassRecover)

// Respawn handles the death screen: press esc (the proven respawn key on this build,
// key lane — no cursor), then verify life returned. Its Done is the executive's cue to
// recalibrate capability (a corpse holds the weapons; selections change).
type Respawn struct {
	pressedAt time.Time
}

func (r *Respawn) Name() string { return "respawn" }

func (r *Respawn) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid {
		return nil
	}
	if s.Me.HPPct <= 0 || s.Me.Mode == mode.Death || s.Me.Mode == mode.Dead {
		return &arbiter.Demand{Who: r.Name(), Class: arbiter.ClassRecover, Urgency: 1.0,
			Commit: arbiter.Commitment{MinHold: 2 * time.Second}}
	}
	return nil
}

func (r *Respawn) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if s.Valid && s.Me.HPPct > 0 && s.Me.Mode != mode.Death && s.Me.Mode != mode.Dead {
		return Done // alive again
	}
	if time.Since(r.pressedAt) > 2500*time.Millisecond {
		ctx.M.KeyLane().Press(0x1B) // esc — the proven respawn input
		r.pressedAt = time.Now()
	}
	return Running
}

// ---------------------------------------------------------------- Travel (ClassTravel)

// Travel walks the remembered town road out to the hunting grounds. It bids only in
// town when healthy; Done the moment the area changes. The road comes from the memory
// store (hand-piloted, ScopeSeed) — knowledge, not code.
type Travel struct {
	Road []data.Position
	wp   int
}

func (t *Travel) Name() string { return "travel" }

func (t *Travel) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.HPPct < 30 || len(t.Road) < 2 {
		return nil // Step indexes Road[len-2]: a one-point road would panic (reviewer 8)
	}
	if s.Me.WeaponKind == "none" && s.Me.CorpseFound {
		return nil // naked with a body out there: recovery owns her, not the road
	}
	if ServicesPending(s) {
		return nil // errands first — Travel outranks Service by class and would starve them
	}
	// 0.15: LEGACY fallback below Advance (0.2) — the hand road belongs to one seed;
	// Advance's live border oracle works in every world.
	return &arbiter.Demand{Who: t.Name(), Class: arbiter.ClassTravel, Urgency: 0.15,
		Commit: arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (t *Travel) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if !s.Me.InTown {
		// BORDER RIBBON (observed: the gate zone flickers area 1<->2 and Travel/Explore
		// traded the grant every two seconds): crossing is not arrival. Push onward
		// along the road's own outward direction until well clear of the ribbon.
		last := t.Road[len(t.Road)-1]
		if chebyshev(s.Me.Pos, last) < 15 {
			prev := t.Road[len(t.Road)-2]
			inward := data.Position{X: last.X + (last.X-prev.X)*3, Y: last.Y + (last.Y-prev.Y)*3}
			verbs.Stride{To: inward, Hold: 2 * time.Second}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, t.Name())
			return Running
		}
		t.wp = 0
		return Done // crossed AND clear of the ribbon
	}
	// Seek the nearest waypoint on (re)entry, advance within 6 — the road walker's law.
	if t.wp == 0 {
		best := 1 << 30
		for i, p := range t.Road {
			if d := chebyshev(s.Me.Pos, p); d < best {
				best, t.wp = d, i
			}
		}
	}
	for t.wp < len(t.Road)-1 && chebyshev(s.Me.Pos, t.Road[t.wp]) <= 6 {
		t.wp++
	}
	verbs.Stride{To: t.Road[t.wp]}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, t.Name())
	return Running
}

// String helper for logging grants.
func DemandString(d *arbiter.Demand) string {
	if d == nil {
		return "nil"
	}
	return fmt.Sprintf("%s/%s u=%.2f", d.Class.String(), d.Who, d.Urgency)
}
