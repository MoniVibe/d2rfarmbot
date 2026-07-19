// Package activity: azbot's behaviors as explicit state machines with honest verdicts.
// Every activity BIDS (a Demand) each cycle and, when granted, runs ONE bounded Step.
// There are no behaviors outside this contract (LAW 1).
package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
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

// ---------------------------------------------------------------- Flee (ClassSurvive)

// Flee triggers on low HP with contact pressure; strides away from the enemy centroid
// until the pool recovers or the pressure is gone. The Sentinel keeps drinking in
// parallel — this is escape, not medicine.
type Flee struct{}

func (f *Flee) Name() string { return "flee" }

func (f *Flee) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct <= 0 {
		return nil
	}
	near := 0
	for _, e := range s.Enemies {
		if chebyshev(s.Me.Pos, e.Pos) <= 14 {
			near++
		}
	}
	if s.Me.HPPct < 32 && near > 0 {
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
	if s.Me.HPPct > 45 {
		return Done
	}
	cx, cy, n := 0, 0, 0
	for _, e := range s.Enemies {
		if chebyshev(s.Me.Pos, e.Pos) <= 25 {
			cx += e.Pos.X
			cy += e.Pos.Y
			n++
		}
	}
	if n == 0 {
		return Done
	}
	away := data.Position{X: s.Me.Pos.X + (s.Me.Pos.X - cx/n), Y: s.Me.Pos.Y + (s.Me.Pos.Y - cy/n)}
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
	critical := s.Me.HPPct < 24
	surrounded := near >= 4 && s.Me.HPPct < 60
	trapped := s.Me.HPPct < 45 && s.Me.HealPots == 0 && near >= 3
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
		return Done // through the portal — safe
	}
	near := 0
	for _, en := range s.Enemies {
		if chebyshev(s.Me.Pos, en.Pos) <= 18 {
			near++
		}
	}
	if near == 0 && s.Me.HPPct >= 40 {
		return Done // broke out and clear
	}

	// A PORTAL DOWN IS A DECISION ALREADY MADE: if the exit exists and the situation
	// is still bad — wounded, dry, or critical — USE it. The owner watched her cast a
	// portal and then stand pondering beside it ("we want it to be active"): the ponder
	// was a hard floor set at 18% while the cast fired at 45%. A portal must be CLICKED —
	// standing on one does nothing; approach until clickable (~20 tiles), then the
	// proven hover-confirm entry.
	hardFloor := s.Me.HPPct < 18
	if len(s.Portals) > 0 && (hardFloor || s.Me.HPPct < 35 || s.Me.HealPots == 0) {
		best, bd := s.Portals[0], chebyshev(s.Me.Pos, s.Portals[0].Pos)
		for _, pt := range s.Portals[1:] {
			if d := chebyshev(s.Me.Pos, pt.Pos); d < bd {
				best, bd = pt, d
			}
		}
		if bd > 20 {
			verbs.Stride{To: best.Pos, Hold: 1500 * time.Millisecond, MinGain: 1}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
		} else {
			verbs.EnterPortal{Target: best.ID, TargetPos: best.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
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

	if gapCount == 0 {
		// Open gap: stride through it hard (sliding off any wall on the line).
		dir := sectorDir[gap]
		out := data.Position{X: s.Me.Pos.X + dir.X*22, Y: s.Me.Pos.Y + dir.Y*22}
		slideStride(ctx, out, 2*time.Second, 3, b.Name())
		return Running
	}
	// FIGHT FOR THE WAY OUT: strike the blocker holding the thinnest sector — VOLLEY
	// pace (the per-shot evidence wait was the old slow-shot disease; it had survived
	// here inside Breakout). A whiffed hover falls through to a push stride: always
	// DOING something, never pondering.
	if hasNear[gap] {
		if time.Since(b.lastStrikeAt) >= 350*time.Millisecond {
			blocker := nearest[gap]
			var key byte
			if ctx.Cap != nil && ctx.Cap.Throw != nil {
				key = ctx.Cap.Throw.Key
			} else if ctx.Cap != nil && ctx.Cap.Melee != nil {
				key = ctx.Cap.Melee.Key
			}
			o := verbs.HoverStrike{Target: blocker.ID, TargetPos: blocker.Pos, SelectKey: key, Volley: true}.
				Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
			b.lastStrikeAt = time.Now()
			if o.Result != verbs.ResDone {
				// Couldn't confirm the blocker under the cursor: shove into the sector
				// anyway — displacement beats a standing sweep.
				dir := sectorDir[gap]
				out := data.Position{X: s.Me.Pos.X + dir.X*10, Y: s.Me.Pos.Y + dir.Y*10}
				slideStride(ctx, out, 700*time.Millisecond, 1, b.Name())
			}
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
	if !s.Valid || s.Me.InTown || s.Me.HPPct < floor {
		return nil
	}
	// Naked with a corpse holding her gear: punching the moor is denial, not combat —
	// stand down and let Reclaim (higher class) own every moment until the bow is back.
	if s.Me.WeaponKind == "none" && s.Me.CorpseFound {
		return nil
	}
	best := 46
	for _, e := range s.Enemies {
		if until, bl := f.blacklist[e.ID]; bl && time.Now().Before(until) {
			continue
		}
		if d := chebyshev(s.Me.Pos, e.Pos); d < best {
			best = d
		}
	}
	if best > 45 {
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
		best, bd := percept.EnemyRef{}, 46
		for _, e := range s.Enemies {
			if until, bl := f.blacklist[e.ID]; bl && time.Now().Before(until) {
				continue
			}
			if d := chebyshev(s.Me.Pos, e.Pos); d < bd {
				best, bd = e, d
			}
		}
		if bd > 45 {
			return Done // nothing worth fighting
		}
		f.target, f.targetPos = best.ID, best.Pos
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
	switch s.Me.WeaponKind {
	case "bow":
		if contact <= 4 {
			// Something reached her: swap to the javelin set and stab. The swap is
			// closed-loop — WeaponKind flips on the next capture, or it didn't happen.
			f.trySwap(ctx)
			return Running
		}
		if d < 8 { // uncomfortably close for archery — open the gap first
			away := data.Position{X: s.Me.Pos.X + (s.Me.Pos.X - contactPos.X),
				Y: s.Me.Pos.Y + (s.Me.Pos.Y - contactPos.Y)}
			verbs.Stride{To: away, Hold: 900 * time.Millisecond, MinGain: 2}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
			return Running
		}
		if d > 25 { // out of bow range — close a little
			verbs.Stride{To: f.targetPos, Hold: 900 * time.Millisecond}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
			return Running
		}
		// SHOOT. Basic arrow (plain attack, zero mana) is the workhorse; the bow skill
		// only while the pool is comfortable — never spammed dry, and never after the
		// evidence audit demoted it.
		var key byte
		if !f.rangedDead && ctx.Cap != nil && ctx.Cap.RangedCast != nil && s.Me.MPPct >= 50 {
			key = ctx.Cap.RangedCast.Key
		}
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
		// clear enough to return to the bow.
		if contact > 7 {
			f.trySwap(ctx) // clear — back to the bow
			return Running
		}
		if d > 4 {
			// The chosen target is far but something else is in contact — stab THAT.
			if contact <= 4 {
				f.strike(ctx, f.nearestID(s), contactPos, 0)
				return Running
			}
			// Nothing in reach: step back rather than chase — range is the win condition.
			away := data.Position{X: s.Me.Pos.X + (s.Me.Pos.X - contactPos.X),
				Y: s.Me.Pos.Y + (s.Me.Pos.Y - contactPos.Y)}
			verbs.Stride{To: away, Hold: 800 * time.Millisecond}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
			return Running
		}
		f.strike(ctx, f.target, f.targetPos, 0)
		return Running
	}

	// ---- No bow in the picture: the pre-bowzon paths (throw kiting, then melee). ----
	canThrow := ctx.Cap != nil && ctx.Cap.Throw != nil
	if canThrow && d >= 9 && d <= 22 {
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
	} else if ctx.Cap != nil && ctx.Cap.Melee != nil {
		key = ctx.Cap.Melee.Key
	}
	f.strike(ctx, f.target, f.targetPos, key)
	return Running
}

// strike fires one volley shot: paced to the attack animation (~350ms), aim-hinted
// from the last confirmed sweep offset, selection skipped when the same key was
// proven moments ago. No evidence wait — Step's snapshot read is the evidence path.
func (f *Fight) strike(ctx *Ctx, target data.UnitID, pos data.Position, key byte) {
	if time.Since(f.lastStrikeAt) < 350*time.Millisecond {
		return // the animation is still playing; clicking now buys nothing
	}
	skip := key != 0 && key == f.lastKey && time.Since(f.lastStrikeAt) < 2*time.Second
	o := verbs.HoverStrike{Target: target, TargetPos: pos, SelectKey: key,
		Volley: true, HintDX: f.aimDX, HintDY: f.aimDY, SkipSelect: skip}.
		Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
	f.lastKey, f.lastStrikeAt = key, time.Now()
	if o.Result != verbs.ResDone {
		f.assess(o) // whiff/refused: the old no-evidence ladder (4 strikes -> blacklist)
		return
	}
	f.aimDX, f.aimDY = o.AimDX, o.AimDY
	f.volleys++
	if f.volleys >= 8 {
		// Eight landed clicks and the target never once flinched in the stream:
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
}

func NewLoot() *Loot { return &Loot{failures: map[data.UnitID]int{}, ban: map[data.UnitID]time.Time{}} }

func (l *Loot) Name() string { return "loot" }

// wanted scores a ground item for THIS character's needs (the self-model speaking):
// weapons dominate while weaponless; potions and gold always matter a little.
func (l *Loot) wanted(s *percept.Snapshot, it percept.ItemRef) float64 {
	n := it.Name
	switch {
	case !s.Me.Armed && (contains(n, "Dagger") || contains(n, "Sword") || contains(n, "Axe") ||
		contains(n, "Club") || contains(n, "Javelin") || contains(n, "Spear") || contains(n, "Wand") ||
		contains(n, "Mace") || contains(n, "Scepter")):
		return 0.95
	case contains(n, "Potion") || contains(n, "Herb"):
		return 0.55
	case n == "Gold":
		return 0.4
	case it.Quality >= 4: // magic+
		return 0.5
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
	// No looting with contact pressure — that is how pickups become deaths.
	for _, e := range s.Enemies {
		if chebyshev(s.Me.Pos, e.Pos) <= 6 {
			return nil
		}
	}
	if _, score, ok := l.pick(s); ok {
		return &arbiter.Demand{Who: l.Name(), Class: arbiter.ClassLoot, Urgency: score,
			Commit: arbiter.Commitment{MinHold: 2 * time.Second, SwitchMargin: 0.3}}
	}
	return nil
}

func (l *Loot) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	// Yield under pressure mid-approach: enemies closing in flip the priority back to
	// Fight/Flee naturally — pressing on toward a bauble is how pickups become deaths.
	for _, e := range s.Enemies {
		if chebyshev(s.Me.Pos, e.Pos) <= 8 {
			return Done
		}
	}
	it, _, ok := l.pick(s)
	if !ok {
		return Done
	}
	d := chebyshev(s.Me.Pos, it.Pos)
	if d > 5 {
		verbs.Stride{To: it.Pos}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, l.Name())
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
	if !s.Valid || s.Me.InTown || s.Me.HPPct < 50 {
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
