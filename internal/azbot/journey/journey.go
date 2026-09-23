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

	gr      *game.MemoryReader
	holder  string
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

// navGrid adapts the game's collision grid to the nav core (walls = NonWalkable).
func navGrid(g *game.Grid) *nav.Grid {
	return nav.NewGrid(g.OffsetX, g.OffsetY, g.Width, g.Height, func(x, y int) bool {
		row := g.CollisionGrid[y]
		return x < len(row) && row[x] != game.CollisionTypeNonWalkable
	})
}

func New(gr *game.MemoryReader, grid *game.Grid, goal data.Position, holder string) *Journey {
	j := &Journey{
		Goal: goal, Arrive: 5,
		gr: gr, holder: holder, grid: navGrid(grid),
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
	pl := j.grid.Plan(me, j.Goal, nav.Options{Obstacles: j.obs})
	if !pl.Found {
		return false, "planner: " + pl.Reason
	}
	j.snapped = pl.Snapped
	j.f.SetPath(j.grid.Simplify(pl.Path, j.obs), me, now)
	return true, ""
}

// Step advances the journey by ONE bounded stride. The caller holds a RoleSteer lease.
func (j *Journey) Step(m *motor.Motor, p *percept.Perceptor, led *verbs.Ledger) Status {
	s := p.Capture()
	if !s.Valid {
		return Status{State: Moving, Note: "perception gap"}
	}
	j.led = led
	me := s.Me.Pos
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
			return j.stride(m, p, led, cmd.Target, 300, "final approach")
		case nav.Move:
			return j.stride(m, p, led, cmd.Target, cmd.HoldMs, cmd.Reason)
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
func (j *Journey) stride(m *motor.Motor, p *percept.Perceptor, led *verbs.Ledger, to data.Position, holdMs int, why string) Status {
	if to == (data.Position{}) {
		return Status{State: Moving, Note: "refused: unset target"} // never walk toward the world origin
	}
	hold := time.Duration(holdMs) * time.Millisecond
	if hold <= 0 {
		hold = 600 * time.Millisecond
	}
	holder := j.holder
	if j.f.State() == nav.Escaping {
		holder += "/escape"
	}
	verbs.Stride{To: to, Hold: hold, MinGain: 1, Planned: true}.Do(m, j.gr, p, led, holder)
	return Status{State: Moving, Note: why}
}
