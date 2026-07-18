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
}

type Activity interface {
	Name() string
	// Demand returns nil when the activity has nothing to bid this cycle.
	Demand(s *percept.Snapshot) *arbiter.Demand
	// Step runs one bounded slice while holding the grant.
	Step(ctx *Ctx) Verdict
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
	verbs.Stride{To: away, Hold: 2 * time.Second, MinGain: 3}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
	return Running
}

// ---------------------------------------------------------------- Fight (ClassFight)

type Fight struct {
	target    data.UnitID
	targetPos data.Position
	j         *journey.Journey
	noEvid    int
	blacklist map[data.UnitID]time.Time
}

func NewFight() *Fight { return &Fight{blacklist: map[data.UnitID]time.Time{}} }

func (f *Fight) Name() string { return "fight" }

func (f *Fight) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.HPPct < 40 {
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
	alive := false
	for _, e := range s.Enemies {
		if e.ID == f.target {
			alive = true
			f.targetPos = e.Pos
			break
		}
	}
	if !alive || f.target == 0 {
		f.target, f.j, f.noEvid = 0, nil, 0
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
	if ctx.Cap != nil && ctx.Cap.Melee != nil {
		key = ctx.Cap.Melee.Key
	}
	o := verbs.HoverStrike{Target: f.target, TargetPos: f.targetPos, SelectKey: key}.
		Do(ctx.M, ctx.GR, ctx.P, ctx.Led, f.Name())
	switch o.Result {
	case verbs.ResDone:
		f.noEvid = 0
	default:
		f.noEvid++
		if f.noEvid >= 4 {
			f.blacklist[f.target] = time.Now().Add(30 * time.Second)
			f.target, f.j, f.noEvid = 0, nil, 0
		}
	}
	return Running
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
	if !s.Valid || !s.Me.InTown || s.Me.HPPct < 60 || len(t.Road) == 0 {
		return nil
	}
	return &arbiter.Demand{Who: t.Name(), Class: arbiter.ClassTravel, Urgency: 0.3,
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
