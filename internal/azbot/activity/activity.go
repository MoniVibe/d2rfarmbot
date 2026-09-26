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
	"github.com/hectorgimenez/koolo/internal/azbot/combat/learn"
	"github.com/hectorgimenez/koolo/internal/azbot/combat/policy"
	"github.com/hectorgimenez/koolo/internal/azbot/coverage"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
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
	// Screen: the stable screen Reading the gate judged this tick (janitor ON
	// only; nil when the janitor is off or nothing has been captured yet).
	Screen *screen.Reading
	// Seen: the latest published screen observation (exec.Eye), janitor ON or
	// OFF. With the janitor off the town services close their own leftovers
	// by sight on it (tidy, servicelife.go); nil before the first capture.
	Seen *exec.Seen
	// Held: the arbiter's held-time ledger — phase budgets are spent in held
	// time, so preemption and gate blocks never burn them. nil = no budgets.
	Held func(who string) time.Duration
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
	npc.FallenShaman:       true,
	npc.CarverShaman:       true,
	npc.CarverShaman2:      true,
	npc.DevilkinShaman:     true,
	npc.DevilkinShaman2:    true,
	npc.DarkShaman:         true,
	npc.DarkShaman2:        true,
	npc.WarpedShaman:       true,
	npc.HollowOne:          true, // the mummy lords raise their dead too
	npc.Guardian:           true,
	npc.Unraveler:          true,
	npc.Unraveler2:         true,
	npc.HoradrimAncient:    true,
	npc.RatManShaman:       true,
	npc.FetishShaman:       true,
	npc.FlayerShaman:       true,
	npc.FlayerShaman2:      true,
	npc.SoulKillerShaman:   true,
	npc.SoulKillerShaman2:  true,
	npc.StygianDollShaman:  true,
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

// meleeAttackKey chooses the damage skill, keeping LeapAttack out of the travel
// gait and out of point-blank range where Double Swing is the steadier fallback.
// The live skill binding is selected by calibration; this helper only decides
// which already-proven key is safe to issue this tick.
//
// dist is the Chebyshev distance to what will be struck. The blind strike
// sites (Stand, Flee, Breakout) fire volleyAt, where key 0 is SHIFT+left —
// the owner's left skill in place — so Approach (policy.Choose's "out of
// reach, no hover") degrades to that in-place swing there.
func meleeAttackKey(ctx *Ctx, s *percept.Snapshot, dist int) byte {
	_, key := meleeStrike(ctx, s, dist, nil, false, true)
	return key
}

// leftAudit benches the left skill after a run of evidence-free left strikes
// (policy.LeftAudit); Fight feeds it, every strike site obeys it.
var leftAudit policy.LeftAudit

// leapClock is the leap cooldown (policy.LeapCooldown): stamped by every
// Leap Attack that goes out (noteLeap), read by every strike decision.
var leapClock policy.LeapClock

// noteLeap stamps the leap cooldown when key is the proven Leap Attack.
func noteLeap(ctx *Ctx, key byte) {
	if key != 0 && ctx != nil && ctx.Cap != nil && ctx.Cap.LeapAttack != nil && key == ctx.Cap.LeapAttack.Key {
		leapClock.Fired(time.Now())
	}
}

func leapAttackReady(s *percept.Snapshot) bool {
	if s == nil || s.Me.MaxMana <= 0 {
		return false
	}
	// Leap Attack's base cost is about ten mana on this build. Require that
	// amount plus a small buffer, expressed as a percentage because perception
	// exposes the live pool as MPPct rather than raw current mana.
	needPct := 25
	byPool := (10*100+s.Me.MaxMana-1)/s.Me.MaxMana + 5
	if byPool > needPct {
		needPct = byPool
	}
	return s.Me.MPPct >= needPct
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
// into masonry and kept pushing). The chosen bearing is walked by MoveTo (flee).
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
			st := moveTo(ctx, c, moveto.Opts{Holder: "kite", Purpose: moveto.Flee, MaxHold: hold, MinGain: 2})
			return st.Issued
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
	cainIDWorks.Store(true)
	stashWorks.Store(true)
	scrollWorks.Store(true)
	resetSanctuaryClock() // a new game resets the Sanctuary particle clock
	spendWorks.Store(true)
	skillSpendWorks.Store(true)
	refusedEquips = map[int]bool{}
	hotPortalUntil = time.Time{}
	carryReachAt = time.Time{}
	fleeFatigueUntil = time.Time{}
	bloodRing = nil // a load-screen gap would read as a phantom drop rate
	worldGhosts = 0 // the poison died with the world it poisoned (P-4.8a)
	// Night-2 audit finding 7: these globals survived relogs — a fresh world
	// inherited the old one's cursed ground, cools, and hunger windows, and a
	// relog taken to ESCAPE a poisoned map still disbelieved the new map's
	// real exit. Everything the old world learned about itself dies with it.
	breakerSites = nil
	fightCoolUntil = time.Time{}
	crossingBracketUntil = time.Time{}
	vaultHungerUntil = time.Time{}
	marchLawfulUntil = time.Time{}
	// The deliberate-routing seam signals belong to the old world too: a fresh
	// map re-rolls every door, so a crossing timestamp or a beyond-town
	// commitment window from the dead world must never gate the new one.
	lastSeamCrossAt = time.Time{}
	advanceCommitBeyondTownUntil = time.Time{}
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

// FleeFloor — P-2.-1 THE FLEE FLOOR (the owner, 23:20): at this HP% or above
// Flee does not exist (and Fight's wounded stand-down with it). Under it the
// session's wind-down stops riding for town and pauses the game (exec.Session
// FleeFloor, rung 3).
const FleeFloor = 33

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
	// stalemate watch (R36: 11 minutes swinging at idle monsters behind a closed door)
	holdAt  time.Time
	holdHP  int
	holdAt0 data.Position
}

func (st *Stand) Name() string { return "stand" }

// contactRange: an enemy this close is in the fight, walled stamp or not.
const contactRange = 3

// sighted: a legitimate target — clear line, or close enough that the wall
// stamp cannot be trusted over the teeth.
func sighted(s *percept.Snapshot, e percept.EnemyRef) bool {
	return !e.Walled || chebyshev(s.Me.Pos, e.Pos) <= contactRange
}

// contactDist: the nearest enemy, walled stamp ignored (1<<30 when none).
func contactDist(s *percept.Snapshot) int {
	best := 1 << 30
	for _, e := range s.Enemies {
		if d := chebyshev(s.Me.Pos, e.Pos); d < best {
			best = d
		}
	}
	return best
}

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
	// STALEMATE: 15s held, no blood lost, not a step taken — the teeth cannot reach
	// him and he cannot reach them (a closed door, a ledge). Those monsters read
	// as unreachable for a minute: the march (and Door) take over.
	if st.holdAt.IsZero() || chebyshev(s.Me.Pos, st.holdAt0) > 3 || s.Me.HPPct < st.holdHP-5 {
		st.holdAt, st.holdHP, st.holdAt0 = time.Now(), s.Me.HPPct, s.Me.Pos
	} else if time.Since(st.holdAt) > 15*time.Second {
		var ids []data.UnitID
		for _, e := range s.Enemies {
			if chebyshev(s.Me.Pos, e.Pos) <= 12 {
				ids = append(ids, e.ID)
			}
		}
		MarkUnreachable(ids, time.Minute)
		ctx.Led.Append(verbs.Outcome{Verb: "stand", Holder: st.Name(), Result: verbs.ResRefused,
			Evidence: fmt.Sprintf("stalemate: 15s held with no blood lost and no ground taken — %d monsters marked unreachable for 1m", len(ids))})
		st.holdAt = time.Time{}
		return Done
	}
	// MAXIMUM KILLING: the skill per P-1.13 (above the 10% swallow), the
	// contact strike otherwise, the bare fist as the last resort.
	var key byte
	switch {
	case s.Me.WeaponKind == "bow" && ctx.Cap != nil && ctx.Cap.Reach != nil && s.Me.MPPct > 10:
		key = ctx.Cap.Reach.Key
	default:
		key = meleeAttackKey(ctx, s, bd)
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
	if s.Me.HPPct >= FleeFloor {
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
		case s.Me.WeaponKind == "melee":
			key = meleeAttackKey(ctx, s, closest)
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
				moveTo(ctx, best.Pos, moveto.Opts{Holder: f.Name(), Purpose: moveto.Flee, MaxHold: 1500 * time.Millisecond})
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
	// Planned retreat (A3): a clear line commits at once; a walled one plans
	// small; no route at all takes nav's best clear step — never the wall.
	moveTo(ctx, reach(s.Me.Pos, away, 8), moveto.Opts{Holder: f.Name(), Purpose: moveto.Flee, MaxHold: 2 * time.Second, MinGain: 3})
	return Running
}

// ---------------------------------------------------------------- Breakout (ClassSurvive)

// Breakout is the owner's "fight for a way out, or TP away — or both": when surrounded
// or wounded, read the encirclement as a ring of 8 sectors, and choose intelligently:
//  1. A GAP exists (a sector with ≤1 enemy): fight for it — strike the blocker in the
//     gap, stride through, done when clear. Purposeful violence, not panic.
//  2. NO gap, or potions gone: PREPARE THE EXIT — cast the town portal immediately
//     (it persists), then keep fighting the thinnest sector from throw range.
//  3. HP hits the hard floor: step into the portal. Escape is a fallback she is
//     standing next to, never a hope.
type Breakout struct {
	castAt       time.Time
	lastStrikeAt time.Time
	vaultAt      time.Time // P-2.11: leap cooldown — a deaf leap falls through to the old doctrine
	castTries    int       // casts that never produced a portal (empty tome — the poverty spiral)
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

// marchLawfulUntil — THE MARCH HINT (owner, night 2: "driven by progress
// rather than run around killing randomly"): stamped by Advance.Demand every
// tick it lawfully bids. While fresh, Fight contracts to the 10-tile corridor
// on ALL ground — the wide hunt exists only when grinding is the mission.
var marchLawfulUntil time.Time

// grindUntil — THE GRIND HINT (owner, 2026-09-26: "it is unruly and sometimes
// goes on rampages unnecessarily"): the 45-tile hunt used to be the DEFAULT
// whenever the march hint went stale — and it went stale whenever Advance was
// mute for any reason: HP under 50 (a wounded barbarian hunted the whole
// screen), the quest-area and tomb bids (they return before the stamp), the
// town-errand pause. Now the wide hunt must be ASKED for: Advance stamps this
// only when grinding is the mission (under-levelled for the next leg, or the
// itinerary is complete). Everything else is the 10-tile corridor.
var grindUntil time.Time

// huntRadius: Fight's eyesight. 45 only on paying ground while grinding is the
// declared mission and the march is not bidding; otherwise the corridor.
func huntRadius(s *percept.Snapshot) int {
	now := time.Now()
	if now.Before(grindUntil) && !now.Before(marchLawfulUntil) && ExpWorthwhile(s.Me.Level, s.Me.Area) {
		return 45
	}
	return 10
}

// canVault is PURE now (night-2 audit finding 5: a side-effecting predicate
// probed every travel tick re-armed the 4s mana-hunger window perpetually and
// benched the contact skill for whole marches). Sites that genuinely stood
// down FOR a leap call noteVaultHunger themselves.
func canVault(ctx *Ctx, s *percept.Snapshot) bool {
	if ctx.Cap == nil || ctx.Cap.Vault == nil {
		return false
	}
	need := 10
	if s.Me.MaxMana < 8 {
		need = 50
	}
	return s.Me.MPPct >= need
}

// noteVaultHunger: a COMBAT leap site (ring escape, raiser vault) found the
// pool short — the contact skill stands down 4s so the pool refills for the
// leap. Travel gaits never call this: a march is not worth muting the swing.
func noteVaultHunger() { vaultHungerUntil = time.Now().Add(4 * time.Second) }

// travelVaultAt: one clock for the travel gait — leaps spent on distance never
// starve the combat sites (they run their own cooldowns).
var travelVaultAt time.Time

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
	if !s.Valid || s.Me.HPPct <= 0 {
		return nil
	}
	// Demand is not called in town once the arbiter has a service/travel
	// winner, so the old reset in Step could be skipped entirely. Clear the
	// escape commitment at the observation boundary; otherwise a portal ride
	// leaves Breakout "engaged" and it immediately re-enters the same portal
	// when the character reaches the field again.
	if s.Me.InTown {
		b.engaged = false
		b.castTries = 0
		b.pinAt = time.Time{}
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
	// R85: the Flayer Jungle landing tripped this at 93-99% HP (a pack plus a
	// jumpy time-to-die) and every breakout rode home — a loop. A healthy leaper
	// fights its way out; the eject needs real blood loss too.
	surrounded := near >= 6 && TimeToDie(s) < 12 && s.Me.HPPct < 70
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
	noteHotLanding(s.Me.Area)
	if s.Me.InTown {
		b.engaged = false
		b.castTries = 0 // the poverty spiral ends where the shopping starts
		return Done     // through the portal — safe; town services take the wheel
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
					} else {
						key = meleeAttackKey(ctx, s, gd)
					}
					volleyAt(ctx, doorman.Pos, key, false) // walk-proof, sweep-free
					b.lastStrikeAt = time.Now()
				}
				return Running
			}
		}
		if bd > 20 {
			moveTo(ctx, best.Pos, moveto.Opts{Holder: b.Name(), Purpose: moveto.Escape, MaxHold: 1500 * time.Millisecond})
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
		// Open gap: stride through it hard (a walled line is planned around).
		dir := sectorDir[gap]
		out := data.Position{X: s.Me.Pos.X + dir.X*22, Y: s.Me.Pos.Y + dir.Y*22}
		moveTo(ctx, out, moveto.Opts{Holder: b.Name(), Purpose: moveto.Escape, MaxHold: 2 * time.Second, MinGain: 3})
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
			key = meleeAttackKey(ctx, s, bd)
			volleyAt(ctx, best, key, false)
			b.lastStrikeAt = time.Now()
		} else if bd > 4 {
			// walk AT the enemy: off the wall
			moveTo(ctx, best, moveto.Opts{Holder: b.Name(), Purpose: moveto.Escape, MaxHold: 1200 * time.Millisecond, Arrive: 4})
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
			} else {
				key = meleeAttackKey(ctx, s, chebyshev(s.Me.Pos, blocker.Pos))
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
		moveTo(ctx, reach(s.Me.Pos, away, 8), moveto.Opts{Holder: b.Name(), Purpose: moveto.Flee, MaxHold: 1500 * time.Millisecond})
	}
	return Running
}

// ---------------------------------------------------------------- Fight (ClassFight)

type Fight struct {
	target    data.UnitID
	targetPos data.Position
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
	// THE BLIND BRAWLER (night 2, the 02:27 log: hundreds of hoverstrike
	// whiffs in Cold Plains — the mod's hover oracle goes dark for minutes
	// while positional volleys provably kill): a run of consecutive whiffs
	// benches the hover pump entirely and the fight runs on march-swings.
	hoverWhiffRun   int
	hoverBlindUntil time.Time
	rangedShots     int
	rangedFlinch    int
	rangedDead      bool
	// Mutual-veto watchdog (P-2.10): when the lock changed and when we last
	// ISSUED an attack input. An in-reach lock that produces no input starves
	// Travel while feeding nothing — the advisor's "tiny bureaucratic collapse".
	watchTarget data.UnitID
	watchSince  time.Time
	vaultAt     time.Time // P-2.11(3): leap-to-the-raiser cooldown
	// Strike telemetry (strikelog.go): issued strikes awaiting evidence, the
	// current lock's running account, and kills already credited.
	open       []strikeRec
	tally      fightTally
	deathTaken map[data.UnitID]bool
	// telPrefix: telemetry windows still to write their strike line
	// (strikelearn.go emitStrike), with the line's head.
	telPrefix map[*learn.Strike]string
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
	leftAudit = policy.LeftAudit{} // new hands: the left skill re-proves from zero
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
	if !brawlerMode && TimeToDie(s) < 8 && s.Me.HPPct < FleeFloor && !time.Now().Before(fleeFatigueUntil) {
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
	// EVASIVE MARCH (owner, 2026-09-25, after doing the Sanctuary by hand: "we
	// want bot to traverse using leap attack and ignore mobs unless he is
	// pressed to kill them — it was kind of easy doing it"): while the march is
	// lawful, only a pressed barbarian fights — wounded, or boxed in. Everything
	// else is leapt past.
	if time.Now().Before(marchLawfulUntil) && !pressed(s) {
		return nil
	}
	// THE CONTACT LAW (2026-09-23, the owner: "it hugs walls sometimes with higher
	// priority than attacking enemies"): whatever the exp oracle, the corridor,
	// or the wall stamp say, an enemy within contactRange is fought NOW. The
	// grid's wall stamp misreads ridges and ragged terrain, and a monster biting
	// him is not behind a wall.
	if c := contactDist(s); c <= contactRange {
		return &arbiter.Demand{Who: f.Name(), Class: arbiter.ClassFight,
			Urgency: 0.98, Commit: arbiter.Commitment{MinHold: 2 * time.Second, SwitchMargin: 0.2}}
	}
	// THE EXP ORACLE shrinks the hunt: on outleveled ground (mlvl 5+ below her),
	// killing pays nothing — P-5.7 FORCED MARCH, refined 11:15 (the owner:
	// "shoot more than moving if enemies are nearby"): anything within 10 is
	// SHOT — nearby aggro dies, the far field is ignored, and the march owns
	// the ground between camps.
	radius := huntRadius(s)
	// THE CORRIDOR LAW (owner, 02:35 night 2: "killing only monsters in
	// its way, but otherwise prioritizing progressing the map"; extended
	// act-wide the same night: "driven by progress rather than run
	// around killing randomly"): while the march lawfully bids, the
	// 10-tile corridor binds EVERYONE, everywhere, brawler included.
	// The 45-tile eyesight exists only when grinding IS the mission —
	// under-leveled for the next leg or the itinerary done (grindUntil).
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
	f.resolveStrikes(ctx, time.Now()) // last ticks' strikes meet this tick's evidence
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
	// MELEE ENGAGEMENT LAW (live 2026-09-23: the barbarian, a dozen jackals within
	// 5 tiles, walked toward a straggler while the crowd chewed him — the owner:
	// "it tries going for further ones with no success while taking hits"). A melee
	// fighter with teeth in reach fights what is IN reach: a far target is dropped.
	melee := s.Me.WeaponKind != "bow"
	engaged := false
	if melee {
		for _, e := range s.Enemies {
			if chebyshev(s.Me.Pos, e.Pos) <= contactRange {
				engaged = true
				break
			}
		}
		if engaged && alive && chebyshev(s.Me.Pos, f.targetPos) > 3 {
			alive = false
		}
	}
	// REVIVERS FIRST (owner, 2026-09-25: "bot should go for the rezzers and
	// healers, otherwise its gonna be a long fight"): a sighted reviver/healer
	// within 12 replaces a plain target — even in melee contact.
	curRev := false
	for _, e := range s.Enemies {
		if e.ID == f.target {
			curRev = e.Reviver
		}
	}
	if alive && !curRev {
		for _, e := range s.Enemies {
			if e.Reviver && sighted(s, e) && chebyshev(s.Me.Pos, e.Pos) <= 12 {
				alive = false // retarget: the reviver wins the pick below
				break
			}
		}
	}
	if !alive || f.target == 0 {
		f.target, f.noEvid = 0, 0
		f.volleys, f.aimDX, f.aimDY = 0, 0, 0
		// TARGET BY SIGHT FIRST (the owner: "prioritize enemies outside, only go
		// inside when it can"): an enemy in a cabin reads '8 tiles away' THROUGH the
		// wall and wins a nearest-first pick — then the approach dances on the wall.
		// Anything with a clear arrow line outranks everything walled, at any range.
		// The EXP ORACLE's radius applies here too — no chasing trash on old ground.
		// ONE radius with Demand (10, reviewer 10): a target that never earned
		// the bid must never win the selection.
		radius := huntRadius(s) // ONE radius with Demand
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
			if !sighted(s, e) {
				continue
			}
			if until, bl := f.blacklist[e.ID]; bl && time.Now().Before(until) {
				continue
			}
			d := chebyshev(s.Me.Pos, e.Pos)
			revNear := e.Reviver && d <= 12
			if !revNear && (d > radius || (engaged && d > 3)) {
				continue
			}
			// The pack penalty is BOW doctrine (don't charge the center of a
			// 20-stack from range). In melee the center of the stack is exactly
			// who is hitting you — nearest first.
			sc := d
			if revNear {
				sc -= 40 // revivers and healers first: a raised pack is a fight that never ends
			}
			if !melee {
				pack := 0
				for _, o := range s.Enemies {
					if chebyshev(e.Pos, o.Pos) <= 8 {
						pack++
					}
				}
				sc += 3 * pack
			}
			if raisers[e.NPC] {
				// P-1.15 THE RAISER DIES FIRST (the owner: "we don't want her
				// killing the same fallen again and again"): a raising family
				// outranks every non-raiser at any distance in the radius —
				// the bonus dwarfs every d+pack sum a 45-tile world can make.
				sc -= 1000
			}
			if sc < cScore && (d <= contactRange || losClear(ctx.Grid, s.Me.Pos, e.Pos)) {
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
			f.noteLock(ctx, time.Now()) // the fight ended: its summary line
			return Done
		}
		f.target, f.targetPos = bestClear.ID, bestClear.Pos
	}
	f.noteLock(ctx, time.Now())

	d := chebyshev(s.Me.Pos, f.targetPos)
	contact := 1 << 30
	var contactPos data.Position
	for _, e := range s.Enemies {
		if !sighted(s, e) { // WARNING 10: a cabin dweller is not in contact — the
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
			// HALF-STEP the approach — reassess between pulses (the owner: "charging
			// headlong into 20+ stacks"). The reflexes (dodge, flee, breakout) get a
			// bid between every planned pulse of a long approach (≤700ms each).
			moveTo(ctx, f.targetPos, moveto.Opts{Holder: f.Name(), Purpose: moveto.Approach, MaxHold: 700 * time.Millisecond, Arrive: 5})
			return Running
		}
		if !losClear(ctx.Grid, s.Me.Pos, f.targetPos) {
			// A wall owns the arrow's line. This target won the pick, so EVERY target
			// is walled (sight-first selection) — enter properly or not at all: the
			// planner walks the door ("only go inside when it can"); a wall-slide here
			// was the cabin back-and-forth the owner watched. No route, or arrived
			// still blind → blacklist and move on.
			st := moveTo(ctx, f.targetPos, moveto.Opts{Holder: f.Name(), Purpose: moveto.Approach, Arrive: 5})
			if stalled(st) || (st.State == moveto.Arrived && !losClear(ctx.Grid, s.Me.Pos, f.targetPos)) {
				f.blacklist[f.target] = time.Now().Add(30 * time.Second)
				f.target = 0
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
		// Leap Attack is the declared damage skill when it has enough mana; a
		// proven Double Swing key takes over for low-mana or point-blank swings.
		// The old Leap movement skill remains Vault and is never selected here.
		// THE LEFT HAND (the owner, 2026-09-24: Carnage mapped to the LEFT
		// button): combat/policy picks the hand. A proven left skill is the
		// primary strike — key 0, the left button, NO right-skill key press
		// before it; Leap Attack only closes a gap beyond reach; Double Swing
		// carries while the left audit benches a silent left skill. Without a
		// left skill the pre-Carnage order (Leap, Double Swing, plain) stands.
		// rd/rk is the IN-REACH strike (ring, clinch) — policy.Decide: Carnage
		// on the nearest body, or a Leap Attack into the densest landing when
		// the pack makes the splash pay (the owner: "Leap Attack does AoE so
		// it clears swarms more easily"). The pursuit decision is made below,
		// once the lock has settled. The movement leap's hunger still eats first.
		rd, rk := meleeDecide(ctx, s, contact, &contactPos, false, true, true)
		rk = hungerOverride(ctx, &rd, rk)
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
				f.fire(ctx, f.nearestID(s), contactPos, rk, &rd)
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
			f.target = 0
			return Running
		}
		// THE ATTACK COMMAND IS THE CHASE (the owner, 04:12: "make it a
		// priority for him to attack — he just runs around monsters"): a
		// melee char clicks the MONSTER, not the ground beside it — the
		// game's own attack command pursues and swings, tracking the target
		// (HoverStrike, the M4 verb, finally reliable under the hover pump).
		// Plain strides only close truly long gaps.
		if d > 10 && contact > 10 {
			moveTo(ctx, f.targetPos, moveto.Opts{Holder: f.Name(), Purpose: moveto.Approach, MaxHold: 700 * time.Millisecond, Arrive: 10})
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
			// With the left skill primary rk is 0: SHIFT+left, Carnage in
			// place — walk-proof without a hover confirmation.
			f.fire(ctx, f.nearestID(s), contactPos, rk, &rd)
			return Running
		}
		// THE LEAP IS NOT A WEAPON (the owner, 04:5x night 2: "its leaping
		// constantly against monsters instead of attacking them — leap doesnt
		// do damage"). The old raiser-leap (P-2.11(3)) fired at any target
		// this mod's SCRAMBLED npc IDs happened to match against the raisers
		// table — ordinary monsters read as shamans, and he vaulted at them
		// every 8s for zero damage instead of swinging. Combat leap is RETIRED:
		// raisers are chased and struck on foot like everything else. Leap
		// keeps its honest roles — travel gait and ring-escape (Breakout),
		// which are locomotion, never an attack.
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
			if nid := f.nearestID(s); nid != 0 && nid != f.target {
				f.target, f.targetPos = nid, contactPos // the biter IS the fight now
				// Audit finding 10: the old lock's evidence budget must not
				// convict the fresh one — counters are per-victim.
				f.noEvid, f.volleys = 0, 0
			}
		}
		// The PURSUIT strike, on the settled lock. Hover-confirmable → an aimed
		// left click on the monster (the game's attack command closes and
		// swings). Beyond reach with the hover dark and no leap ready →
		// Approach: an unconfirmed left click there is a walk (bare) or a
		// swing at air (SHIFT), so close on foot and swing in reach.
		d = chebyshev(s.Me.Pos, f.targetPos)
		hoverOK := ctx.M.HoverReady() && !time.Now().Before(f.hoverBlindUntil)
		dec, mk := meleeDecide(ctx, s, d, &f.targetPos, hoverOK, contact > 3, true)
		mk = hungerOverride(ctx, &dec, mk) // the pool is spoken for: the movement leap eats first
		ck := dec.Kind
		if ck == policy.Leap {
			// A pursuit leap lands on GROUND — the point covering the most
			// bodies within the splash — never a hover sweep for one unit.
			f.fire(ctx, f.target, f.targetPos, mk, &dec)
			return Running
		}
		if ck == policy.Approach {
			f.logChoice(ctx, &dec)
			f.approach(ctx)
			return Running
		}
		// THE BLIND BRAWLER: with the hover oracle benched, the fight runs
		// entirely on positional volleys — no pump, no whiff log spam, the
		// same 350ms cadence that provably kills (gold rose all through the
		// 02:27 whiff storm; the volleys were doing all the work anyway).
		if time.Now().Before(f.hoverBlindUntil) {
			f.fire(ctx, f.target, f.targetPos, mk, &dec)
			return Running
		}
		// BACKGROUND COMBAT: D2R's hover oracle is focus-gated, but the posted
		// positional attack path is not. Do not spend a cycle proving the same
		// impossible hover, then blacklist a real target on the resulting whiffs;
		// fire the walk-proof positional volley immediately and let the snapshot
		// stream provide hit/death evidence.
		if !ctx.M.HoverReady() {
			f.hoverWhiffRun = 0
			f.noEvid = 0
			f.fire(ctx, f.target, f.targetPos, mk, &dec)
			return Running
		}
		var accept map[data.UnitID]bool
		if s.Me.WeaponKind != "bow" { // melee: any live, sighted hostile in reach is as good
			// ...and any body no farther than the lock itself (relay R9: all 52
			// pursuit hovers whiffed, 39 of them with the cursor ON another
			// monster — the one standing between us and the lock, at 4-7 tiles,
			// outside the old 4-tile accept ring). A nearer body under the
			// cursor is exactly what melee should hit on the way.
			ring := 4
			if d > ring {
				ring = d
			}
			accept = map[data.UnitID]bool{}
			for _, e := range s.Enemies {
				if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= ring {
					accept[e.ID] = true
				}
			}
		}
		f.logChoice(ctx, &dec)
		o := verbs.HoverStrike{Target: f.target, TargetPos: f.targetPos, SelectKey: mk, Volley: true, Accept: accept}.
			Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
		if o.Result == verbs.ResWhiff {
			f.hoverWhiffRun++
			if f.hoverWhiffRun >= 20 {
				f.hoverWhiffRun = 0
				f.hoverBlindUntil = time.Now().Add(60 * time.Second)
				ctx.Led.Append(verbs.Outcome{Verb: "fight", Holder: f.Name(), Result: verbs.ResRefused,
					Evidence: "hover oracle dark 20 straight — BLIND BRAWLER 60s, positional volleys only"})
			}
		} else if o.Result == verbs.ResDone {
			// Decay, not amnesty (audit finding 9): a 15-whiff, 1-hit,
			// repeat storm must still reach the bench.
			f.hoverWhiffRun -= 3
			if f.hoverWhiffRun < 0 {
				f.hoverWhiffRun = 0
			}
		}
		switch {
		case o.Result == verbs.ResWhiff && time.Since(f.lastStrikeAt) >= 350*time.Millisecond:
			// THE WHIFF STILL SWINGS (the owner, 11:15: "still kind of runs
			// around instead of killing"): 31 hover whiffs in barb29's ten
			// minutes, each a cycle with NO click issued — he walked beside
			// monsters looking busy. A missed hover downgrades to a
			// march-swing AT the target's position (the right-click melee
			// walk: the game closes and swings); the aimed strike resumes
			// the moment hover confirms.
			// The LEFT hand cannot march-swing: an unconfirmed left strike
			// is SHIFT+left, which swings in place — at a target beyond
			// reach that is air. Close the gap instead.
			if ck == policy.Left && d > policy.MeleeReach {
				f.noteStrike(ctx, f.target, mk, false)
				f.approach(ctx)
				break
			}
			issued := volleyAt(ctx, f.targetPos, mk, false)
			f.lastStrikeAt = time.Now()
			f.noteStrike(ctx, f.target, mk, issued)
		case o.Result == verbs.ResWhiff:
			f.noteStrike(ctx, f.target, mk, false)
		case o.Result == verbs.ResDone:
			hit := f.target
			if o.Unit != 0 {
				hit = data.UnitID(o.Unit) // Accept landed on a neighbour
			}
			noteLeap(ctx, mk)
			f.noteStrike(ctx, hit, mk, true)
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
		// Arrive 4: the strike below owns d<=4 (the old radius 5 read "arrived"
		// at d=5 and walked nothing while the strike waited for 4).
		st := moveTo(ctx, f.targetPos, moveto.Opts{Holder: f.Name(), Purpose: moveto.Approach, Arrive: 4})
		if stalled(st) {
			f.blacklist[f.target] = time.Now().Add(30 * time.Second)
			f.target = 0
		}
		return Running
	}
	var key byte
	if canThrow {
		key = ctx.Cap.Throw.Key
	} else {
		key = meleeAttackKey(ctx, s, d)
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
// key 0 is the LEFT hand — SHIFT+left fires whatever skill the owner mapped
// there (Carnage, 2026-09-24) with no key press and no chance of a move order.
// Reports whether a click went out (false: no clickable world on the ray).
func volleyAt(ctx *Ctx, pos data.Position, key byte, skipSelect bool) bool {
	return volleyAtDY(ctx, pos, key, skipSelect, game.UnitAimDY()) // at the body, not the feet
}

// groundAt is volleyAt at the GROUND point itself (no body-height lift): a
// Leap Attack lands where the cursor is, so its aim is a tile, not a sprite.
func groundAt(ctx *Ctx, pos data.Position, key byte, skipSelect bool) bool {
	return volleyAtDY(ctx, pos, key, skipSelect, 0)
}

func volleyAtDY(ctx *Ctx, pos data.Position, key byte, skipSelect bool, dy int) bool {
	d := ctx.GR.GetData()
	me := d.PlayerUnit.Position
	bx := int(float32((pos.X-me.X)-(pos.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((pos.X-me.X)+(pos.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2 + dy
	// Clamp onto clickable WORLD along the ray from her — the arrow flies the
	// line anyway. The old per-axis clamp neither kept the direction nor knew
	// the HUD: a target south of the screen became a shift/right-click at 20px
	// from the bottom — on the skill buttons, the belt or the mini-menu (step 11).
	bx, by, ok := verbs.ClampClickLogical(ctx.GR, bx, by)
	if !ok {
		ctx.Led.Append(verbs.Outcome{Verb: "volley", Holder: "volley", Result: verbs.ResRefused,
			Evidence: fmt.Sprintf("at (%d,%d): no clickable world on the ray (HUD)", pos.X, pos.Y)})
		return false
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
			noteLeap(ctx, key)
			return true
		}
		// A tome refuses to disarm: fall through to the plain attack below.
	}
	ctx.M.AttackClick(bx, by)
	return true
}

// strike fires one volley shot: paced to the attack animation (~350ms), selection
// skipped when the same key was proven moments ago. Evidence arrives passively via
// the snapshot stream (flinches); eight silent volleys blacklist the target.
func (f *Fight) strike(ctx *Ctx, target data.UnitID, pos data.Position, key byte) {
	if time.Since(f.lastStrikeAt) < 350*time.Millisecond {
		return // the animation is still playing; clicking now buys nothing
	}
	skip := key != 0 && key == f.lastKey && time.Since(f.lastStrikeAt) < 2*time.Second
	issued := volleyAt(ctx, pos, key, skip)
	f.noteStrike(ctx, target, key, issued)
	f.lastKey, f.lastStrikeAt = key, time.Now()
	f.volleys++
	if f.volleys >= 8 {
		// Eight fired volleys and the target never once flinched in the stream:
		// phantom or unhittable — stop feeding it arrows.
		f.blacklist[target] = time.Now().Add(30 * time.Second)
		f.target, f.noEvid, f.volleys = 0, 0, 0
	}
}

// fire is strike carrying the decision that chose it: paced like strike, the
// decision written only when the strike actually goes out, and a Leap
// Attack clicked at its GROUND landing (policy.PickLeapAim) instead of at a
// body — its telemetry window watches the splash around that point.
func (f *Fight) fire(ctx *Ctx, target data.UnitID, pos data.Position, key byte, dec *policy.Decision) {
	if time.Since(f.lastStrikeAt) < 350*time.Millisecond {
		return // the animation is still playing; clicking now buys nothing
	}
	f.logChoice(ctx, dec)
	if dec == nil || dec.Kind != policy.Leap || key == 0 {
		f.strike(ctx, target, pos, key)
		return
	}
	aim := data.Position{X: dec.Aim.X, Y: dec.Aim.Y}
	// The verdict's unit: the body nearest the landing.
	if s := ctx.Snap; s != nil {
		best := 1 << 30
		for _, e := range s.Enemies {
			if dd := chebyshev(aim, e.Pos); dd < best {
				best, target = dd, e.ID
			}
		}
	}
	skip := key == f.lastKey && time.Since(f.lastStrikeAt) < 2*time.Second
	issued := groundAt(ctx, aim, key, skip)
	f.noteStrikeAt(ctx, target, key, issued, &aim)
	f.lastKey, f.lastStrikeAt = key, time.Now()
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
		f.target, f.noEvid = 0, 0
	}
}

// ---------------------------------------------------------------- Loot (ClassLoot)

// lootProgress is Loot's "progress or let go" clock, measured over a WINDOW
// that restarts at every click Loot itself makes (step 11, run u: two pickup
// probes at d=3, then d=6→10→9 and a 60s ban for "no progress in 4s" — the
// best distance was the d=3 the pickups had already reached, so walking back
// could never beat it). Distance lost to our own clicks is not a stall; only a
// window that fails to improve on its own starting distance is.
type lootProgress struct {
	best  int       // best distance since the window opened (<0: not yet sampled)
	since time.Time // when best last improved, or the window opened
}

// restart opens a fresh window at now: the next observation is its baseline.
func (p *lootProgress) restart(now time.Time) { p.best, p.since = -1, now }

// stuck observes distance d at now and reports whether the approach has made
// no progress for longer than patience while still walking (d > 3; at hand the
// pickup's own failure count rules).
func (p *lootProgress) stuck(d int, now time.Time, patience time.Duration) bool {
	if p.best < 0 || d < p.best {
		p.best, p.since = d, now
		return false
	}
	return d > 3 && now.Sub(p.since) > patience
}

type Loot struct {
	prog     lootProgress // approach clock; restarted per target and per pickup click
	target   data.UnitID
	failures map[data.UnitID]int
	ban      map[data.UnitID]time.Time
	strikes  map[data.UnitID]int // bans served per unit (the second is long)
	// streak: when the current run of loot Steps began and the last Step ran —
	// the march-first budget (lootStreakMax) reads it.
	streakAt, lastStep time.Time
	restUntil          time.Time
}

func NewLoot() *Loot {
	return &Loot{failures: map[data.UnitID]int{}, ban: map[data.UnitID]time.Time{}, strikes: map[data.UnitID]int{}}
}

func (l *Loot) Name() string { return "loot" }

// LootPotions gates bottle pickup (2026-09-23: OFF — with D2R behind another window
// item hover is dead (25/25 probes hovered nothing at d=3), every pickup whiffed and
// looting ate the run. Re-enable when pickup works unfocused.) Kept OFF on
// 2026-09-24: the relay runs (R1/R4, before and after the hover-only probe)
// landed 0 of 101 pickup attempts, and bottles are the commonest drop by far
// (3,602 of ~14,600 ground units) — they would dominate every pile the probe
// sweeps and every approach. The loot policy still plans bottles (tier
// "potion", belt room only) for the day a relay shows pickups landing.
var LootPotions = false

// wanted scores a ground item: the loot value model's plan (package loot,
// config/loot.yaml — lootpolicy.go). A TAKE scores its value (tier S ≈ 0.9,
// A ≈ 0.65); a SWAP scores only the approach (Discard makes the room at her
// feet); a haul, a skip, a discarded unit score nothing. The old rule was
// "strictly uniques, nothing else" with a 40-cell bag reading full in 67% of
// the recorded frames — the owner: "it skips cool inventory stuff".
func (l *Loot) wanted(s *percept.Snapshot, it percept.ItemRef) float64 {
	return theLoot.want(s, it)
}

// lootBlockedByHostile is the single safety gate shared by Demand and Step. A
// reachable hostile in the progress corridor owns the tick; Loot must not walk
// toward a drop while a monster is still close enough to engage. Walled enemies
// belong to the door planner, not the current combat space.
func lootBlockedByHostile(s *percept.Snapshot) bool {
	const radius = 10 // the same corridor radius used by Fight while marching
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= radius {
			return true
		}
	}
	return false
}

// acceptFor is the pickup probe's policy oracle: a ground item under the
// cursor is worth the click when the loot policy wants it and it is not
// banned — the exact target is not the only unique in a pile (step 11).
func (l *Loot) acceptFor(s *percept.Snapshot, now time.Time) func(data.Item) bool {
	return func(it data.Item) bool {
		if until, banned := l.ban[it.UnitID]; banned && now.Before(until) {
			return false
		}
		ref := percept.ItemRef{ID: it.UnitID, Pos: it.Position, Name: string(it.Name),
			Quality: int(it.Quality), Potion: percept.PotionKind(it, true), Class: int(it.ID)}
		return theLoot.takeable(s, ref) // a click lands only on a TAKE (room for it)
	}
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
		// MARCH FIRST (owner, 2026-09-25: "the first priority should be
		// advancing, taking wps, finishing quests, only then it should worry
		// about inventory stuff, unless it's a quest item"): while the march is
		// lawful, only a quest item is worth a detour — anything else must lie
		// roadside, and a resting loot (its streak budget spent) takes nothing
		// but quest items.
		if !lootQuestItem(it) && progressUrgent() {
			if time.Now().Before(l.restUntil) || chebyshev(s.Me.Pos, it.Pos) > lootRoadside {
				continue
			}
		}
		// GUARDED TREASURE (the cabin death): danger is assessed around the ITEM,
		// not just around her. A bauble with a welcoming committee is not loot.
		guards := 0
		for _, e := range s.Enemies {
			if !e.Walled && chebyshev(it.Pos, e.Pos) <= 15 {
				guards++
			}
		}
		if guards >= 3 {
			continue
		}
		// DECISIVENESS (live 2026-09-23, Rocky Waste: 122 drops on the floor, every
		// unique scored an identical 0.85, the tie fell to snapshot iteration order,
		// the target flipped each tick and every flip re-planned the journey — the
		// orbit detector caught 161 tiles walked for 7 net). Ties now break NEAREST
		// first, and the current target keeps a stickiness bonus so a new equal
		// candidate never steals it mid-walk.
		sc := w - float64(chebyshev(s.Me.Pos, it.Pos))*0.001
		if it.ID == l.target {
			sc += 0.05
		}
		if sc > bestScore {
			best, bestScore = it, sc
		}
	}
	return best, bestScore, bestScore > 0
}

func (l *Loot) Demand(s *percept.Snapshot) *arbiter.Demand {
	// Once per tick, before any gate: plan, log, census and catalog every item
	// in reach, and pick the pending room-making plan (Discard, Haul).
	theLoot.observe(s)
	noteTreasure(time.Now(), l.treasureInReach(s, time.Now()))
	if !s.Valid || s.Me.HPPct < 40 {
		return nil
	}
	// Loot is a post-combat phase. Keep the ten-tile corridor owned by Fight so a
	// unique cannot make us walk toward a drop while a reachable monster remains.
	if lootBlockedByHostile(s) {
		return nil
	}
	if _, score, ok := l.pick(s); ok {
		// The loot policy's tiering happens in wanted; every accepted drop stays in
		// ClassLoot so Fight remains the only owner of combat time.
		return &arbiter.Demand{Who: l.Name(), Class: arbiter.ClassLoot, Urgency: score,
			Commit: arbiter.Commitment{MinHold: 2 * time.Second, SwitchMargin: 0.3}}
	}
	return nil
}

func (l *Loot) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	// The streak budget: loot that has held lootStreakMax without a break while
	// the march waits rests for lootRest (quest items excepted, in pick).
	now := time.Now()
	if l.lastStep.IsZero() || now.Sub(l.lastStep) > 5*time.Second {
		l.streakAt = now
	}
	l.lastStep = now
	if progressUrgent() && now.Sub(l.streakAt) > lootStreakMax {
		l.restUntil, l.streakAt = now.Add(lootRest), now
		ctx.Led.Append(verbs.Outcome{Verb: "loot", Holder: l.Name(), Result: verbs.ResRefused,
			Evidence: fmt.Sprintf("march first: loot held %s — resting %s (quest items excepted)", lootStreakMax, lootRest)})
		l.target = 0
		return Done
	}
	// Demand and Step use the same ten-tile reachable-hostile gate. This prevents
	// grant/done churn and never walks toward a drop while a monster is still in the
	// progress corridor.
	if lootBlockedByHostile(s) {
		return Done
	}
	it, _, ok := l.pick(s)
	if !ok {
		l.target = 0
		return Done
	}
	if it.ID != l.target {
		l.prog.restart(time.Now())
	}
	l.target = it.ID
	d := chebyshev(s.Me.Pos, it.Pos)
	// PROGRESS OR LET GO (2026-09-23, Dry Hills: 19s "running" in place toward a
	// drop behind clutter the grid cannot see, three times in two minutes). The
	// distance must shrink within 4s or the drop is banned for a minute — 4s
	// measured from the last pickup click, against that window's own best.
	if l.prog.stuck(d, time.Now(), 4*time.Second) {
		l.ban[it.ID] = time.Now().Add(60 * time.Second)
		l.target = 0
		ctx.Led.Append(verbs.Outcome{Verb: "loot", Holder: l.Name(), Result: verbs.ResBlocked,
			Evidence: fmt.Sprintf("no progress to item %d in 4s (stuck at d=%d) — banned 60s", it.ID, d)})
		return Running
	}
	if d > 3 {
		// JOURNEY, not a blind stride: an item inside a house reads as "5 tiles away"
		// through the wall — she flicker-hovered it and moved on (the owner's report).
		// The planner walks the door; proximity is not reachability.
		if ctx.Grid != nil {
			st := moveTo(ctx, it.Pos, moveto.Opts{Holder: l.Name(), Purpose: moveto.Loot, Arrive: 2})
			if stalled(st) {
				l.ban[it.ID] = time.Now().Add(60 * time.Second)
			}
		} else {
			moveTo(ctx, it.Pos, moveto.Opts{Holder: l.Name(), Purpose: moveto.Loot, Arrive: 2}) // no grid: dead reckoning
		}
		return Running
	}
	// At hand, only a TAKE is clicked (a SWAP target waits for Discard's room).
	if !theLoot.takeable(s, it) {
		l.target = 0
		return Done
	}
	// The policy approved it (any tier it TAKEs), so the verb's uniques-only
	// guard is lifted; the live quality must still match the snapshot's.
	o := verbs.Pickup{Target: it.ID, TargetPos: it.Pos, TargetQuality: it.Quality,
		AllowBelow: true,
		Accept:     l.acceptFor(s, time.Now())}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, l.Name())
	// Whatever the pickup did to her position is ours: the approach clock
	// starts over from wherever she stands now.
	l.prog.restart(time.Now())
	if o.Result == verbs.ResDone {
		theLoot.picked(it, o.Evidence)
	}
	if o.Result != verbs.ResDone {
		l.failures[it.ID]++
		if l.failures[it.ID] >= 3 {
			// R56: a 60s ban re-armed the same deaf pickup ~40 times per unit. The
			// second strike on a unit bans it for 15 minutes.
			if l.strikes == nil {
				l.strikes = map[data.UnitID]int{}
			}
			l.strikes[it.ID]++
			ban := 60 * time.Second
			if l.strikes[it.ID] >= 2 {
				ban = 15 * time.Minute
			}
			l.ban[it.ID] = time.Now().Add(ban)
			delete(l.failures, it.ID)
		}
	}
	return Running
}

// ---------------------------------------------------------------- Explore (ClassExplore)

// Explore v3: COVERAGE, not a room tour (the owner: "In the cave it goes to
// already-explored bits; it should try unexplored areas"). The P-5F map tour
// counted a room visited only when she stood inside its rectangle and forgot
// everything on any area change — a town trip re-toured the cave from the
// door. Now the shared coverage tracker (coverage.go) knows every tile she has
// SEEN, per seed and area, across portals and restarts; Explore walks to the
// nearest-by-path frontier cluster until none reachable remains (or 95% is
// seen). The blind heading walk survives only for an area with no grid.
type Explore struct {
	heading int  // blind fallback: index into the 8 bearings
	set     bool // heading initialized from the road's outward direction
	// Frontier is Advance's hint (P-5F): the itinerary leg the march owns for
	// this level. Exploration exists only there — behind it, ground is
	// corridor and the march owns every idle moment. Nil-safe: no itinerary,
	// classic wander.
	Frontier func(level int) area.ID
	// Bias is the leg's exit hint (Advance.ExploreBias): frontier toward the
	// way on scores better. Nil or !ok: nearest-by-path alone.
	Bias func() (at data.Position, key string, ok bool)
	walk covWalker
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

	// THE FRONTIER WALK: the one coverage model (coverage.go) picks the
	// nearest-by-path unexplored frontier; an area with no reachable frontier
	// (or 95% seen) hands the moment back to the march.
	var b coverage.Bias
	if x.Bias != nil {
		if at, key, ok := x.Bias(); ok {
			b = coverage.Bias{At: at, Key: key, OK: true}
		}
	}
	if st, ok := x.walk.step(ctx, b, x.Name()); ok {
		if st != coverage.Exploring {
			return Done
		}
		return Running
	}

	// No coverage knowledge (no grid, no tracker): the blind heading walk.
	if !x.set {
		x.heading, x.set = 5, true // southwest-ish: away from the town gate into the moor
	}
	o := bearings[x.heading%len(bearings)]
	tgt := data.Position{X: s.Me.Pos.X + o.X, Y: s.Me.Pos.Y + o.Y}
	if st := moveTo(ctx, tgt, moveto.Opts{Holder: x.Name(), Purpose: moveto.Travel}); wallTurned(st) {
		x.heading++ // walled: one 45° turn, then HOLD the new line
	}
	return Running
}

// ---------------------------------------------------------------- Respawn (ClassRecover)

// Respawn handles the death screen: press esc (the proven respawn key on this build,
// key lane — no cursor), then verify life returned. Its Done is the executive's cue to
// recalibrate capability (a corpse holds the weapons; selections change).
type Respawn struct {
	since     time.Time // when this death's respawn began (the fallback ESC clock)
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
		r.since = time.Time{}
		return Done // alive again
	}
	if r.since.IsZero() {
		r.since = time.Now()
	}
	// R22: the death screen read Mode=Unknown and this waited 57 minutes on
	// "Press ESC to continue". After 8s the ESC goes anyway: on a live world it
	// only raises the pause menu (the world freezes; the janitor closes it).
	if JanitorOn && (ctx.Screen == nil || ctx.Screen.Mode != screen.Dead) && time.Since(r.since) < 8*time.Second {
		// v2: the respawn ESC fires only on a death screen the screen oracle
		// believes (docs/AZBOT_V2.md: "kept, only when Mode=Dead"); a blind
		// ESC on a live world raises the pause menu.
		return Running
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
	// THE SEAM HYSTERESIS (item 2): while the deliberate marcher holds a
	// committed beyond-town intent and a crossing just fired, the legacy road
	// walker must not grab the wheel and shove her back across the gate — that
	// re-crossing IS the Lut Gholein<->Rocky Waste bounce. Advance owns the
	// seam for the hysteresis window; the road resumes after it.
	if committedSeamHold() {
		return nil
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
			moveTo(ctx, inward, moveto.Opts{Holder: t.Name(), Purpose: moveto.Travel, MaxHold: 2 * time.Second, Fallback: true})
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
	moveTo(ctx, t.Road[t.wp], moveto.Opts{Holder: t.Name(), Purpose: moveto.Travel, Fallback: true})
	return Running
}

// String helper for logging grants.
func DemandString(d *arbiter.Demand) string {
	if d == nil {
		return "nil"
	}
	return fmt.Sprintf("%s/%s u=%.2f", d.Class.String(), d.Who, d.Urgency)
}

// Evasive-march pressure (owner, 2026-09-25).
const (
	pressedHP    = 65 // below this, stand and fight
	pressedCrowd = 4  // this many within pressedRange: leap into them (owner, R86: "it should leap attack when many mobs threaten it")
	pressedRange = 8
)

// pressed: wounded, or boxed in — the only reasons the march stops to fight.
func pressed(s *percept.Snapshot) bool {
	if s.Me.HPPct < pressedHP {
		return true
	}
	n := 0
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= pressedRange {
			n++
		}
	}
	return n >= pressedCrowd
}
