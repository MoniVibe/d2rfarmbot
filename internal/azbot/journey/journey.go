// Package journey is azbot's SINGLE movement authority (design §5). It adopts the
// paid-for Navigator wholesale and owns the one escalation ladder with attempt memory:
// carrot → replan → local escape (recorded) → Stalled. No second authority ever
// second-guesses it; the callers handle honest verdicts instead.
package journey

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
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

// Journey walks one goal to completion or an honest verdict. One instance per goal.
type Journey struct {
	Goal   data.Position
	Arrive int // arrival radius; default 5

	nav      *Navigator
	gr       *game.MemoryReader
	holder   string
	bestDist int
	bestAt   time.Time
	tried    []data.Position // escape bearings already attempted from a stall (attempt memory)
	escapes  int
}

func New(gr *game.MemoryReader, grid *game.Grid, goal data.Position, holder string) *Journey {
	return &Journey{
		Goal: goal, Arrive: 5,
		nav: NewNavigator(grid), gr: gr, holder: holder,
		bestDist: 1 << 30, bestAt: time.Now(),
	}
}

// SetObstacles feeds the versioned obstacle set (wedges + colliding objects).
func (j *Journey) SetObstacles(obs []data.Position) { j.nav.SetObstacles(obs) }

// Step advances the journey by ONE bounded stride. The caller holds a RoleSteer lease.
func (j *Journey) Step(m *motor.Motor, p *percept.Perceptor, led *verbs.Ledger) Status {
	s := p.Capture()
	if !s.Valid {
		return Status{State: Moving, Note: "perception gap"}
	}
	me := s.Me.Pos
	d := chebyshev(me, j.Goal)
	if d <= j.Arrive {
		return Status{State: Arrived}
	}
	if d < j.bestDist-1 {
		j.bestDist, j.bestAt = d, time.Now()
		j.tried = j.tried[:0] // progress resets the attempt memory
		j.escapes = 0
	}

	// Plan (once; the navigator replans internally only on divergence).
	if !j.nav.havePlan && !j.nav.BuildPlan(me, j.Goal, time.Now()) {
		return Status{State: NoPath, Note: "planner found no route"}
	}
	step := j.nav.Step(me, time.Now())
	target := step.Target
	if step.Arrived {
		j.nav.havePlan = false
		target = j.Goal
	}

	// THE LADDER: no closest-approach progress for 12s of stepping → recorded escape
	// bearings (never the same one twice from a stall) → Stalled after 4 escapes.
	if time.Since(j.bestAt) > 12*time.Second {
		if j.escapes >= 4 {
			return Status{State: Stalled, Note: fmt.Sprintf("ladder exhausted at (%d,%d) best=%d", me.X, me.Y, j.bestDist)}
		}
		esc, ok := j.pickEscape(me)
		if !ok {
			return Status{State: Stalled, Note: "no untried escape bearing"}
		}
		j.tried = append(j.tried, esc)
		j.escapes++
		j.nav.havePlan = false // escape moves us; the plan must rebuild after
		verbs.Stride{To: esc, Hold: 2 * time.Second, MinGain: 3}.Do(m, j.gr, p, led, j.holder+"/escape")
		j.bestAt = time.Now() // the escape gets its own progress window
		return Status{State: Moving, Note: fmt.Sprintf("escape %d to (%d,%d)", j.escapes, esc.X, esc.Y)}
	}

	o := verbs.Stride{To: target}.Do(m, j.gr, p, led, j.holder)
	if o.Result == verbs.ResBlocked {
		// One blocked stride is information, not a crisis: the navigator's own stall
		// detection plus our ladder decide; we just avoid replanning storms here.
		j.nav.havePlan = false
	}
	return Status{State: Moving}
}

// pickEscape proposes an escape bearing not yet tried from this stall: the navigator's
// clearance-aware local search first, then cardinal offsets by distance.
func (j *Journey) pickEscape(me data.Position) (data.Position, bool) {
	cands := []data.Position{}
	if e, ok := j.nav.localEscape(me); ok {
		cands = append(cands, e)
	}
	for _, off := range []data.Position{{X: 14, Y: 0}, {X: -14, Y: 0}, {X: 0, Y: 14}, {X: 0, Y: -14},
		{X: 10, Y: 10}, {X: -10, Y: -10}, {X: 10, Y: -10}, {X: -10, Y: 10}} {
		cands = append(cands, data.Position{X: me.X + off.X, Y: me.Y + off.Y})
	}
	for _, c := range cands {
		seen := false
		for _, t := range j.tried {
			if chebyshev(c, t) <= 4 {
				seen = true
				break
			}
		}
		if !seen {
			return c, true
		}
	}
	return data.Position{}, false
}
