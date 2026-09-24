// Package journey is azbot's SINGLE movement authority (design §5). The pure nav core
// plans (A* on raw walkability, soft clearance cost) and follows with an explicit state
// machine that owns the one escalation ladder with attempt memory:
// carrot → replan → recorded escapes → Fail. Journey only executes its commands and
// turns them into honest verdicts; no second authority second-guesses it.
package journey

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/mapfuse"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/nav"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

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

type JState uint8

const (
	Moving JState = iota
	Arrived
	NoPath
	Stalled // ladder exhausted; the caller decides (different goal, violence, abandon)
)

func (s JState) String() string {
	switch s {
	case Moving:
		return "moving"
	case Arrived:
		return "arrived"
	case NoPath:
		return "nopath"
	default:
		return "stalled"
	}
}

type Status struct {
	State JState
	Note  string
	// Issued: this Step sent input (a stride went out). Out is that stride's
	// outcome — information only; the follower's stall clock judges blockage.
	Issued bool
	Out    verbs.Outcome
}

// Legs is the body a journey walks with: where it stands, and one planned
// stride. The game's body is GameLegs; tests walk a fake one.
type Legs interface {
	Here() (data.Position, bool)
	Stride(to data.Position, hold time.Duration, minGain int, holder string) verbs.Outcome
}

// GameLegs is the live body: the perceptor's position and one verbs.Stride at
// a target the planner already checked for sight.
type GameLegs struct {
	M   *motor.Motor
	GR  *game.MemoryReader
	P   *percept.Perceptor
	Led *verbs.Ledger
}

func (g GameLegs) Here() (data.Position, bool) {
	s := g.P.Capture()
	return s.Me.Pos, s.Valid
}

func (g GameLegs) Stride(to data.Position, hold time.Duration, minGain int, holder string) verbs.Outcome {
	return verbs.Stride{To: to, Hold: hold, MinGain: minGain, Planned: true}.Do(g.M, g.GR, g.P, g.Led, holder)
}

// Object bubbles: the route keeps ~3 tiles off live objects when there is room,
// but may still squeeze past one standing in a doorway (soft, not a wall).
const (
	obstacleRadius  = 3
	obstaclePenalty = 60
	// Backstop over the follower's own ladder: no closest approach to the goal in
	// this long means something outside its model (knockback loops) — give up.
	approachBackstop = 60 * time.Second
)

// Journey walks one goal to completion or an honest verdict. One instance per goal.
type Journey struct {
	Goal   data.Position
	Arrive int // arrival radius; default 5
	// MaxHold caps one pulse (0 = the follower's own pulse). Budget caps the
	// planner's node expansions (0 = nav's default) — a hurried mover (flee)
	// plans small and falls back rather than stall a survival reaction.
	MaxHold time.Duration
	Budget  int
	// MinHold lengthens every pulse to at least this (0 = the follower's own):
	// an escape commits to its carrot — the stride still ends within 2 tiles
	// of it (Stride's arrived-early), so a long hold never flies past it.
	MinHold time.Duration
	// NoAdopt: keep the grid the route was planned on (ReplanOnWall off).
	NoAdopt bool

	gr      *game.MemoryReader
	holder  string
	src     *game.Grid // the fused grid the nav grid was built from (Source swaps it)
	grid    *nav.Grid
	f       *nav.Follower
	obs     []nav.Obstacle
	replan  bool // obstacles changed: route again on the next Step
	snapped bool // the goal was walled; the route ends at the nearest walkable cell
	led     *verbs.Ledger

	bestDist int
	bestAt   time.Time
	lastStep time.Time
}

// Source, when set, returns the executive's CURRENT fused grid. Every Step adopts a
// newer grid of the same area: rooms streamed in (or the prior earned trust) since
// the plan was made. A new wall across the route ahead replans at once instead of
// walking into it; otherwise the route stands and future replans see the new map.
var Source func() *game.Grid

// OnPlan, when set, hears every committed route (the map-fusion debug picture).
var OnPlan func(path []data.Position)

// NavGrid adapts the game's collision grid to the nav core (walls = NonWalkable),
// clearance field included — for callers that judge bearings, not routes.
func NavGrid(g *game.Grid) *nav.Grid { return navGrid(g) }

// Conversions are memoized per source grid: the executive regrids every few
// seconds and every journey of that period shares one nav grid (the clearance
// field over a whole level is the expensive part).
// Two slots: a caller walking a grid it kept (Advance's leg grid) beside the
// executive's current one must not rebuild the clearance field every tick.
var memo struct {
	src, relaxSrc *game.Grid
	ng, relaxed   *nav.Grid
	src2          *game.Grid
	ng2           *nav.Grid
}

// navGrid adapts the (fused) game grid to the nav core: walls block; unknown and
// prior-only cells are walkable at mapfuse's per-step surcharge.
func navGrid(g *game.Grid) *nav.Grid {
	if g == memo.src && memo.ng != nil {
		return memo.ng
	}
	if g == memo.src2 && memo.ng2 != nil {
		memo.src, memo.src2 = memo.src2, memo.src
		memo.ng, memo.ng2 = memo.ng2, memo.ng
		return memo.ng
	}
	ng := buildNav(g, false)
	memo.src2, memo.ng2 = memo.src, memo.ng
	memo.src, memo.ng = g, ng
	return ng
}

// relaxedNavGrid is the NoPath fallback: a trusted prior's walls become heavily
// penalized guesses — the prior earned trust, not infallibility.
func relaxedNavGrid(g *game.Grid) *nav.Grid {
	if g == memo.relaxSrc && memo.relaxed != nil {
		return memo.relaxed
	}
	ng := buildNav(g, true)
	memo.relaxSrc, memo.relaxed = g, ng
	return ng
}

func cellClass(c game.CollisionType, relaxed bool) mapfuse.Class {
	switch c {
	case game.CollisionTypeNonWalkable:
		return mapfuse.ClassLiveBlock
	case game.CollisionTypeUnknown:
		return mapfuse.ClassUnknown
	case game.CollisionTypeUnknownWall:
		return mapfuse.ClassUnknownPriorWall
	case game.CollisionTypePriorWalk:
		return mapfuse.ClassPriorWalk
	case game.CollisionTypePriorBlocked:
		if relaxed {
			return mapfuse.ClassPriorBlock.Relaxed()
		}
		return mapfuse.ClassPriorBlock
	}
	return mapfuse.ClassLiveWalk
}

func buildNav(g *game.Grid, relaxed bool) *nav.Grid {
	return nav.NewGridCost(g.OffsetX, g.OffsetY, g.Width, g.Height, func(x, y int) bool {
		row := g.CollisionGrid[y]
		return x < len(row) && cellClass(row[x], relaxed).Walkable()
	}, func(x, y int) int32 {
		row := g.CollisionGrid[y]
		if x >= len(row) {
			return 0
		}
		return cellClass(row[x], relaxed).StepCost()
	})
}

func hasPriorWalls(g *game.Grid) bool {
	for _, row := range g.CollisionGrid {
		for _, c := range row {
			if c == game.CollisionTypePriorBlocked {
				return true
			}
		}
	}
	return false
}

func New(gr *game.MemoryReader, grid *game.Grid, goal data.Position, holder string) *Journey {
	j := &Journey{
		Goal: goal, Arrive: 5,
		gr: gr, holder: holder, src: grid, grid: navGrid(grid),
		bestDist: 1 << 30, bestAt: time.Now(),
	}
	j.f = nav.NewFollower(j.grid, nil)
	// Every follower state change is a ledger line: "nav: following -> stuck (...)".
	j.f.OnTransition = func(from, to nav.State, why string) {
		if j.led == nil {
			return
		}
		res := verbs.ResDone
		if to == nav.Stuck || to == nav.Failed {
			res = verbs.ResBlocked
		}
		j.led.Append(verbs.Outcome{Verb: "nav", Holder: j.holder, Result: res,
			Target:   fmt.Sprintf("(%d,%d)", j.Goal.X, j.Goal.Y),
			Evidence: fmt.Sprintf("nav: %s -> %s (%s)", from, to, why)})
	}
	return j
}

// SetObstacles feeds the versioned obstacle set (wedges + colliding objects) as soft
// cost bubbles; a changed set reroutes on the next Step.
func (j *Journey) SetObstacles(obs []data.Position) {
	next := make([]nav.Obstacle, len(obs))
	for i, o := range obs {
		next[i] = nav.Obstacle{At: o, Radius: obstacleRadius, Penalty: obstaclePenalty}
	}
	same := len(next) == len(j.obs)
	for i := 0; same && i < len(next); i++ {
		same = next[i] == j.obs[i]
	}
	if same {
		return
	}
	j.obs = next
	j.f.SetObstacles(next)
	j.replan = true
}

// State exposes the follower's mode (debug overlays, logs).
func (j *Journey) State() nav.State { return j.f.State() }

func (j *Journey) plan(me data.Position, now time.Time) (bool, string) {
	g := j.grid
	opt := nav.Options{Obstacles: j.obs, MaxExpand: j.Budget}
	pl := g.Plan(me, j.Goal, opt)
	if !pl.Found && j.src != nil && j.Budget == 0 && hasPriorWalls(j.src) {
		// Every route crosses a trusted prior's wall: plan again with those walls
		// as expensive guesses. Observed walls stay hard. (A budgeted planner is
		// in a hurry: it falls back instead of paying for a second search.)
		g = relaxedNavGrid(j.src)
		if rp := g.Plan(me, j.Goal, opt); rp.Found {
			pl = rp
			j.note("planner: trusted prior walled every route — planned through its walls as guesses")
		}
	}
	if !pl.Found {
		return false, "planner: " + pl.Reason
	}
	j.snapped = pl.Snapped
	path := g.Simplify(pl.Path, j.obs)
	j.f.SetPath(path, me, now)
	if OnPlan != nil {
		OnPlan(path)
	}
	return true, ""
}

func (j *Journey) note(ev string) {
	if j.led == nil {
		return
	}
	j.led.Append(verbs.Outcome{Verb: "nav", Holder: j.holder, Result: verbs.ResDone,
		Target: fmt.Sprintf("(%d,%d)", j.Goal.X, j.Goal.Y), Evidence: ev})
}

// adopt swaps in a newer fused grid of the SAME frame (a different frame is a
// different area — the caller's business, not this journey's). It reports the
// cell where a newly known wall crosses the route ahead, and arms the replan.
func (j *Journey) adopt(g *game.Grid) (data.Position, bool) {
	if g == nil || g == j.src {
		return data.Position{}, false
	}
	ng := navGrid(g)
	if !ng.SameFrame(j.grid) {
		return data.Position{}, false
	}
	old := j.grid
	j.src, j.grid = g, ng
	j.f.SetGrid(ng)
	at, hit := j.f.NewlyBlocked(old, ng)
	if hit {
		j.replan = true
	}
	return at, hit
}

// Regoal moves the goal without resetting the follower's progress and stall
// memory (a moving target: the monster walked, the item was nudged). A goal
// that moved 2+ tiles arms a replan; a smaller drift keeps the route.
func (j *Journey) Regoal(goal data.Position) {
	if goal == j.Goal {
		return
	}
	if chebyshev(goal, j.Goal) >= 2 {
		j.replan = true
	}
	j.Goal = goal
}

// Invalidate arms a replan from wherever the body stands next (someone else
// moved it: a click walk, a leap). Stall and escalation memory survive.
func (j *Journey) Invalidate() { j.replan = true }

// Frame reports whether g covers the same world rectangle as the grid this
// journey plans on (a different frame is a different area).
func (j *Journey) Frame(g *game.Grid) bool {
	return g != nil && j.src != nil && g.OffsetX == j.src.OffsetX && g.OffsetY == j.src.OffsetY &&
		g.Width == j.src.Width && g.Height == j.src.Height
}

// Route returns the committed route from me, planning first when there is
// none (or a replan is armed). ok=false carries the planner's reason.
func (j *Journey) Route(me data.Position, led *verbs.Ledger) ([]data.Position, bool, string) {
	j.led = led
	j.adoptSource()
	if j.f.State() == nav.Failed {
		return nil, false, "follower failed here"
	}
	// The route is read from its start: one planned from elsewhere (she walked
	// on since) is re-planned from here.
	if pts := j.f.Path(); j.replan || len(pts) == 0 || j.f.State() == nav.NoPlan || chebyshev(pts[0], me) > 3 {
		j.replan = false
		if ok, why := j.plan(me, time.Now()); !ok {
			return nil, false, why
		}
	}
	return j.f.Path(), true, ""
}

func (j *Journey) adoptSource() {
	if Source == nil || j.NoAdopt {
		return
	}
	if at, hit := j.adopt(Source()); hit {
		j.note(fmt.Sprintf("map: new wall on the route at (%d,%d) — replanning", at.X, at.Y))
	}
}

// Step advances the journey by ONE bounded stride. The caller holds a RoleSteer lease.
func (j *Journey) Step(m *motor.Motor, p *percept.Perceptor, led *verbs.Ledger) Status {
	return j.StepLegs(GameLegs{M: m, GR: j.gr, P: p, Led: led}, led)
}

// StepLegs is Step over any body (the seam MoveTo and the tests walk).
func (j *Journey) StepLegs(legs Legs, led *verbs.Ledger) Status {
	me, valid := legs.Here()
	if !valid {
		return Status{State: Moving, Note: "perception gap"}
	}
	j.led = led
	d := chebyshev(me, j.Goal)
	if d <= j.Arrive {
		return Status{State: Arrived}
	}
	now := time.Now()
	if gap := now.Sub(j.lastStep); !j.lastStep.IsZero() && gap > 2*time.Second {
		j.bestAt = j.bestAt.Add(gap) // time spent fighting/dodging is not ours to approach in
	}
	j.lastStep = now
	if d < j.bestDist-1 {
		j.bestDist, j.bestAt = d, now
	} else if now.Sub(j.bestAt) > approachBackstop {
		return Status{State: Stalled, Note: fmt.Sprintf("no approach in %s at (%d,%d) best=%d", approachBackstop, me.X, me.Y, j.bestDist)}
	}
	j.f.ArriveR = float64(max(j.Arrive, 1))
	j.adoptSource()
	if j.replan && j.f.State() != nav.Failed {
		j.replan = false
		if ok, why := j.plan(me, now); !ok {
			return Status{State: NoPath, Note: why}
		}
	}

	// A Replan is answered at once and the follower asked again; bounded so a
	// planner/follower disagreement can never spin inside one Step.
	for k := 0; k < 3; k++ {
		cmd := j.f.Step(me, now)
		switch cmd.Kind {
		case nav.Replan:
			if ok, why := j.plan(me, now); !ok {
				return Status{State: NoPath, Note: why}
			}
		case nav.Fail:
			return Status{State: Stalled, Note: cmd.Reason}
		case nav.ArrivedCmd:
			if j.snapped {
				return Status{State: Arrived, Note: fmt.Sprintf("goal walled; at nearest walkable (%d,%d)", cmd.Target.X, cmd.Target.Y)}
			}
			return j.stride(legs, cmd.Target, 300, "final approach")
		case nav.Move:
			return j.stride(legs, cmd.Target, cmd.HoldMs, cmd.Reason)
		}
	}
	return Status{State: Moving, Note: "replan did not settle"}
}

// stride executes one planned pulse. HONOR THE FOLLOWER'S PULSE: 100-300ms holds by
// clearance (short taps in tight rooms, longer in the open) — the old default-1.6s
// stride committed a straight line far past the carrot, cutting corners into walls.
// MinGain 1: a 150ms pulse covers ~1-2 tiles. A blocked stride is information only —
// the follower's along-path stall clock judges real blockage (the old replan-on-block
// reset that clock every time, so "stuck" never fired from the same spot).
func (j *Journey) stride(legs Legs, to data.Position, holdMs int, why string) Status {
	if to == (data.Position{}) {
		return Status{State: Moving, Note: "refused: unset target"} // never walk toward the world origin
	}
	hold := time.Duration(holdMs) * time.Millisecond
	if hold <= 0 {
		hold = 600 * time.Millisecond
	}
	if hold < j.MinHold {
		hold = j.MinHold
	}
	if j.MaxHold > 0 && hold > j.MaxHold {
		hold = j.MaxHold
	}
	holder := j.holder
	if j.f.State() == nav.Escaping {
		holder += "/escape"
	}
	o := legs.Stride(to, hold, 1, holder)
	return Status{State: Moving, Note: why, Issued: true, Out: o}
}
