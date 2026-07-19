// Package activity: azbot's behaviors as explicit state machines with honest verdicts.
// Every activity BIDS (a Demand) each cycle and, when granted, runs ONE bounded Step.
// There are no behaviors outside this contract (LAW 1).
package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
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
}

func (f *Flee) Name() string { return "flee" }

func (f *Flee) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 {
		return nil
	}
	// The trigger ALIGNS with Fight's stand-down floor (35, or 50 dry): below it Fight
	// refuses to bid, and the old narrow Flee (HP<32, contact≤14) left a dead band
	// where NOTHING bid — she stood mid-moor eating archer fire (the owner: "it stands
	// idle despite monsters being nearby"). If she's too hurt to fight and teeth are
	// within 25 tiles, she leaves. Range counts: archers at 20 are pressure too.
	floor := 35
	if s.Me.HealPots == 0 {
		floor = 50
	}
	near := 0
	for _, e := range s.Enemies {
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
	if near >= crowdBar {
		return &arbiter.Demand{Who: f.Name(), Class: arbiter.ClassSurvive,
			Urgency: 1.35,
			Commit:  arbiter.Commitment{MinHold: 2 * time.Second}}
	}
	// P-1.12: the lone chaser is Fight's, at any blood — a retreat from one
	// enemy is a chase, and she loses chases (the single-zombie death, 09:18).
	if s.Me.HPPct < floor && near >= 2 {
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
	if s.Me.HealPots == 0 && len(s.Portals) > 0 {
		best, bd := s.Portals[0], chebyshev(s.Me.Pos, s.Portals[0].Pos)
		for _, pt := range s.Portals[1:] {
			if d := chebyshev(s.Me.Pos, pt.Pos); d < bd {
				best, bd = pt, d
			}
		}
		if bd > 20 {
			slideStride(ctx, best.Pos, 1500*time.Millisecond, 1, f.Name())
		} else {
			verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos, Desperate: true}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
		}
		return Running
	}
	if s.Me.HealPots == 0 && ctx.Cap != nil && ctx.Cap.TownTP != nil &&
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
		if chebyshev(s.Me.Pos, en.Pos) <= 18 {
			near++
		}
	}
	// P-2.2: the eject seat arms at 6 surrounding below half blood — four
	// nuisances under a scratch kept her porting out of winnable fights all
	// morning (the 57-HP sortie loop, 11:06).
	critical := s.Me.HPPct < 24
	surrounded := near >= 6 && s.Me.HPPct < 50
	trapped := s.Me.HPPct < 45 && s.Me.HealPots == 0 && near >= 3
	// COMMITMENT: an engaged escape keeps bidding while its portal stands — one
	// potion tick dropping 'surrounded' must not strand a half-used exit.
	if b.engaged && len(s.Portals) > 0 && !s.Me.InTown {
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
		return Done // through the portal — safe; town services take the wheel
	}
	near := 0
	for _, en := range s.Enemies {
		if chebyshev(s.Me.Pos, en.Pos) <= 18 {
			near++
		}
	}
	if b.engaged && len(s.Portals) == 0 {
		b.engaged = false // the portal fell out of the world (rooms unloaded / expired)
	}
	if near == 0 && s.Me.HPPct >= 40 && !b.engaged {
		return Done // broke out and clear — and no half-used exit standing
	}

	// A PORTAL DOWN IS A DECISION ALREADY MADE — no second gate. Breakout stepping AT
	// ALL means its demand fired: critical, surrounded, or trapped. The old use-bar
	// (HP<35) contradicted the cast-bar (near>=5): at HP 52 inside a 30-strong swarm
	// she cast the exit and then stood BESIDE it ring-fighting until the bar armed —
	// by then the label was buried under bodies and she died pinned at (5843,4847),
	// 65->0 in 11s (run 24). Emergency + portal = ENTER, desperately.
	hardFloor := s.Me.HPPct < 18
	if len(s.Portals) > 0 {
		b.engaged = true // a standing portal + Breakout stepping = the escape is OWNED
		best, bd := s.Portals[0], chebyshev(s.Me.Pos, s.Portals[0].Pos)
		for _, pt := range s.Portals[1:] {
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

	// PREPARE THE EXIT: no portal down yet and things look grim → cast one now.
	// It persists; fighting continues beside it. This is the "both". THREE casts with
	// no portal appearing = the tome is EMPTY (0 gold, 0 scrolls — the poverty
	// spiral); stop pretending and fight the gap instead (measured: pinned 0x0
	// re-casting into nothing while the ring closed).
	if len(s.Portals) == 0 && ctx.Cap != nil && ctx.Cap.TownTP != nil && b.castTries < 3 &&
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
	// March is Advance's live door hint. P-5.8: within 12 of the march door the
	// REACH TOOL holds — the clinch swap is suppressed and the volley fires
	// point-blank; the funnel rewards the pierce, not the poke.
	March func() (data.Position, bool)
}

func NewFight() *Fight { return &Fight{blacklist: map[data.UnitID]time.Time{}} }

func (f *Fight) Name() string { return "fight" }

func (f *Fight) Demand(s *percept.Snapshot) *arbiter.Demand {
	// Stop committing to a fight while wounded — hand the tick to Flee/EscapeTP early,
	// not at 5% (the bleed-out). With no potions the bar is higher: retreat sooner.
	floor := 35
	if s.Me.HealPots == 0 {
		floor = 50
	}
	if !s.Valid || s.Me.InTown {
		return nil
	}
	// P-1.12 THE LONE TOOTH IS FOUGHT AT ANY BLOOD: the wounded stand-down
	// applies to PRESSURE, never to a single enemy — she died to one zombie
	// without hitting it back while Flee owned every wounded moment (09:18).
	// The count uses Flee's own 25-tile band: one enemy there is Fight's at
	// any blood, two or more are Flee's — no dead band between the rules.
	if s.Me.HPPct < floor {
		near25 := 0
		for _, e := range s.Enemies {
			if chebyshev(s.Me.Pos, e.Pos) <= 25 {
				near25++
			}
		}
		if near25 != 1 {
			return nil // packs are Flee's; the lone chaser is a target
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
		radius = 10
	}
	best := radius + 1
	for _, e := range s.Enemies {
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
		radius := 45
		if !ExpWorthwhile(s.Me.Level, s.Me.Area) {
			radius = 12
		}
		// PACK-AWARE pick: score = distance + 3×(bodies within 8 of the candidate).
		// Nearest-first used to elect the CENTER of a 20-stack and she charged it
		// (the owner: "charging headlong into 20+ stacks"); a straggler at 20 tiles
		// now beats a horde at 10. Distance still gates on the hunt radius.
		var best, bestClear percept.EnemyRef
		bScore, cScore := 1<<30, 1<<30
		haveAny, haveClear := false, false
		for _, e := range s.Enemies {
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
			if sc < bScore {
				best, bScore, haveAny = e, sc, true
			}
			if sc < cScore && losClear(ctx.Grid, s.Me.Pos, e.Pos) {
				bestClear, cScore, haveClear = e, sc, true
			}
		}
		switch {
		case haveClear:
			f.target, f.targetPos = bestClear.ID, bestClear.Pos
		case haveAny:
			f.target, f.targetPos = best.ID, best.Pos // all walled: the journey decides
		default:
			return Done // nothing worth fighting
		}
	}

	d := chebyshev(s.Me.Pos, f.targetPos)
	contact := 1 << 30
	var contactPos data.Position
	for _, e := range s.Enemies {
		if dd := chebyshev(s.Me.Pos, e.Pos); dd < contact {
			contact, contactPos = dd, e.Pos
		}
	}

	// ---- THE BOWZON DANCE (owner's doctrine, verbatim: basic arrow as the workhorse,
	// don't spam magic arrow dry, W-swap to javelin for contact, and the HIGHER priority
	// is getting far enough to swap back to the bow and shoot at range). ----
	// P-5.8: at the march door the dance inverts — the door mouth is shot open.
	nearDoor := false
	if f.March != nil {
		if mt, ok := f.March(); ok && chebyshev(s.Me.Pos, mt) <= 12 {
			nearDoor = true
		}
	}
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
		manaBar := 25
		if s.Me.ManaPots > 0 {
			manaBar = 15
		}
		var key byte
		if !f.rangedDead && ctx.Cap != nil && ctx.Cap.Reach != nil && s.Me.MPPct >= manaBar {
			key = ctx.Cap.Reach.Key
		}
		// P-1.13 THE STACK EATS THE SKILL: 4+ bodies bunched at the aim point
		// make a skill shot — the pierce pays for its mana in bodies. Above a
		// swallow of mana (5%), the stack overrides the thrift bar.
		if key == 0 && !f.rangedDead && ctx.Cap != nil && ctx.Cap.Reach != nil && s.Me.MPPct >= 5 {
			stack := 0
			for _, e := range s.Enemies {
				if chebyshev(f.targetPos, e.Pos) <= 6 || (contact <= 6 && chebyshev(contactPos, e.Pos) <= 6) {
					stack++
				}
			}
			if stack >= 4 {
				key = ctx.Cap.Reach.Key
			}
		}
		if contact <= 3 {
			// In the clinch the javelin is still the better tool — ask for the swap —
			// but until it lands, SHOOT the tooth point-blank. Never a mute cycle.
			// P-5.8: at the door mouth the swap is SUPPRESSED — the bow holds and
			// pierces the funnel; the poke was measured useless at the bridge.
			if !nearDoor {
				f.trySwap(ctx)
			}
			f.strike(ctx, f.nearestID(s), contactPos, key)
			return Running
		}
		if contact <= 6 {
			// P-1.8 STAND AND LOOSE: healthy blood holds its ground and shoots —
			// clearing the pack IS the defense. Kite only wounded AND pressed
			// (the OR cowered her along walls: 2 fallen within 7 outran the bow).
			press := 0
			for _, e := range s.Enemies {
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
		// Javelin set: stab whatever is in reach — but the HIGHER priority is getting
		// clear enough to return to the bow. The strike key is the OWNER-DECLARED
		// melee skill when given (Jab — config beats inference on a scrambled mod).
		var mk byte
		if ctx.Cap != nil && ctx.Cap.Contact != nil {
			mk = ctx.Cap.Contact.Key
		}
		// A dry bow set is no bow at all: while Arrows==0 the javelins ARE the build —
		// chase and stab instead of kiting toward a weapon that whiffs at air.
		dryBow := s.Me.Arrows == 0
		// P-5.8: holding the CONTACT TOOL at the door mouth, she swaps back to the
		// REACH TOOL at once — and stabs while the swap pends. Never a mute cycle.
		if nearDoor && !dryBow {
			f.trySwap(ctx)
			if contact <= 4 {
				f.strike(ctx, f.nearestID(s), contactPos, mk)
			}
			return Running
		}
		// SWAP BACK AT >4 (the owner, twice: "she uses the javelin more than the bow —
		// should be the other way around"): Jab's reach is ~4 — a contact she cannot
		// stab is a contact she should be SHOOTING. In at contact<=3, out at >4; the
		// 1.5s swap rate-limit is the flutter guard.
		if contact > 4 {
			if !dryBow {
				f.trySwap(ctx) // clear — back to the bow
				return Running
			}
			verbs.Stride{To: f.targetPos, Hold: 900 * time.Millisecond}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
			return Running
		}
		if d > 4 {
			// The chosen target is far but something else is in contact — stab THAT.
			if contact <= 4 {
				f.strike(ctx, f.nearestID(s), contactPos, mk)
				return Running
			}
			if dryBow { // melee is all she has: close the gap
				verbs.Stride{To: f.targetPos, Hold: 700 * time.Millisecond}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
				return Running
			}
			// Contact at 5: shove the gap open HARD — wall-aware; cornered = stab.
			if !kiteAway(ctx, s, contactPos, 1300*time.Millisecond) {
				f.strike(ctx, f.nearestID(s), contactPos, mk)
			}
			return Running
		}
		f.strike(ctx, f.target, f.targetPos, mk)
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
		if dd := chebyshev(s.Me.Pos, e.Pos); dd < bd {
			best, bd = e.ID, dd
		}
	}
	return best
}

func (f *Fight) assess(o verbs.Outcome) {
	if o.Result == verbs.ResDone {
		f.noEvid = 0
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
	case it.Quality >= 6: // rare/set/unique: the drops the whole grind is FOR
		if gearRoom {
			return 0.8
		}
	case contains(n, "Potion") || contains(n, "Herb"):
		if s.Me.BeltHP+s.Me.BeltMana < s.Me.BeltSlots || s.Me.InvFree >= 1 {
			return 0.55
		}
	case n == "Gold":
		return 0.4 // gold has no footprint
	case it.Quality >= 4: // magic+
		if gearRoom {
			return 0.5
		}
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
	// thresholds or the arbiter churns.
	for _, e := range s.Enemies {
		if chebyshev(s.Me.Pos, e.Pos) <= 8 {
			return nil
		}
	}
	if it, score, ok := l.pick(s); ok {
		// THE TREASURE GRAB: ClassFight starves ClassLoot whenever anything hostile
		// is within 45 — in the moor that is ALWAYS, so a rare short bow lay ignored
		// while she volleyed trash (the owner: "she didn't care at all"). Rare+ finds
		// and ammo-for-a-dry-quiver bid IN the fight class: urgency does the risk
		// arithmetic — a fight with teeth close still outbids (Fight at contact 5 is
		// ~0.9), a fight against distant stragglers loses to treasure.
		treasure := it.Quality >= 6 ||
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
	return &arbiter.Demand{Who: x.Name(), Class: arbiter.ClassExplore, Urgency: 0.1,
		Commit: arbiter.Commitment{MinHold: 4 * time.Second}}
}

func (x *Explore) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	CarryReach(ctx) // P-5.9: the wander walks with the bow out too
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
	if !s.Valid || !s.Me.InTown || s.Me.HPPct < 30 || len(t.Road) == 0 {
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
