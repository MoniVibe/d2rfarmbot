// Package moveto is azbot's ONE mover (docs/AZBOT_V3_FOUNDATIONS.md, A3): every
// locomotion request in the bot — fight approach, loot, errands, reclaim, flee,
// dodge, unstick, withdraw, the march — is MoveTo(goal, opts). It owns a
// journey per holder, reuses it across ticks while the goal drifts only a few
// tiles (a moving target re-goals without losing its stuck memory), plans on
// the fused grid (mapfuse via the executive's Grid), follows with nav, and
// answers with a typed Status.
//
// The only strides that skip the route are the ones a route could not improve:
// a short hop (≤3 tiles on a clear line), a hurried (flee/escape) commit along
// a clear line, dead reckoning with no grid at all, and the step off the edge
// of a frame toward a goal beyond it. A hurried mover plans with a small
// budget and, rather than stall a survival reaction, falls back to nav's best
// clear step — never a blind straight line into a wall.
package moveto

import (
	"fmt"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/nav"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

// Purpose says why she moves; it picks the planner's hurry and the log line.
type Purpose uint8

const (
	Travel   Purpose = iota // the march, roads, pads, portals
	Approach                // closing on a target (fight, NPC band, object)
	Flee                    // opening a gap from a threat — hurried
	Errand                  // town services
	Escape                  // unstick, breakout lines, stepping off a body — hurried
	Loot                    // walking to a drop or a corpse
)

func (p Purpose) String() string {
	switch p {
	case Travel:
		return "travel"
	case Approach:
		return "approach"
	case Flee:
		return "flee"
	case Errand:
		return "errand"
	case Escape:
		return "escape"
	default:
		return "loot"
	}
}

// Hurried purposes plan small, commit clear lines and always fall back to a
// clear step: a survival reaction never waits on the planner.
func (p Purpose) Hurried() bool { return p == Flee || p == Escape }

// Opts shapes one MoveTo call.
type Opts struct {
	Holder  string
	Purpose Purpose
	// Arrive: chebyshev radius that counts as there (0 = 2; hurried 1).
	Arrive int
	// MaxHold caps one pulse; for a stride without a route it IS the pulse
	// (0 = the mode's default).
	MaxHold time.Duration
	// MinGain is a direct stride's postcondition (0 = 1 tile).
	MinGain int
	// AllowLeap: a far goal may be leapt toward (Env.Leap) — the landing is
	// picked along the planned route, never a straight line through walls.
	AllowLeap bool
	// NoWallReplan turns ReplanOnWall off (it is on by default): the route
	// keeps its grid instead of adopting walls that streamed in.
	NoWallReplan bool
	// Click: travel by clicking a waypoint along the route and letting the
	// game's pathfinder walk it (the fish cure — the only walker that knows
	// the mod's invented fences). CombatKey right-clicks with that skill
	// selected (the march swings). A dead click falls to a planned stride.
	Click     bool
	CombatKey byte
	// Fallback: on NoPath/Stalled take nav's best clear step toward the goal
	// (implied by a hurried purpose).
	Fallback bool
}

// State is MoveTo's typed verdict.
type State uint8

const (
	Moving State = iota
	Arrived
	NoPath
	Stalled
)

func (s State) String() string {
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

// Status is one call's verdict. Issued: input went out in this call. Blocked:
// that input moved nothing (the stride's postcondition failed).
type Status struct {
	State   State
	Why     string
	Mode    string // hop | commit | reckon | edge | journey | click | leap | fallback | ""
	Issued  bool
	Blocked bool
}

// Body is what MoveTo drives: journey's legs plus the click gait.
type Body interface {
	journey.Legs
	Click(to data.Position, hold time.Duration, key byte, holder string) verbs.Outcome
}

// Env is the world one call moves in.
type Env struct {
	Body Body
	Led  *verbs.Ledger
	GR   *game.MemoryReader // journeys keep it for their own Step; may be nil
	Grid *game.Grid         // the fused planning grid; nil = dead reckoning
	// LeapReady/Leap: the travel leap (TravelVault). Ready is asked before any
	// landing is computed; Leap reports whether a leap went out.
	LeapReady func() bool
	Leap      func(land data.Position) bool
}

// gameBody is the live body.
type gameBody struct{ journey.GameLegs }

func (b gameBody) Click(to data.Position, hold time.Duration, key byte, holder string) verbs.Outcome {
	return verbs.ClickMove{To: to, Hold: hold, CombatKey: key}.Do(b.M, b.GR, b.P, b.Led, holder)
}

// Game builds the live Env.
func Game(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *verbs.Ledger, grid *game.Grid) Env {
	return Env{Body: gameBody{journey.GameLegs{M: m, GR: gr, P: p, Led: led}}, Led: led, GR: gr, Grid: grid}
}

const (
	// RegoalRadius: a goal within this of the previous one re-goals the same
	// journey (stuck memory kept); farther is a new trip.
	RegoalRadius = 6
	// HopTiles: a clear line this short is one stride, no route.
	HopTiles = 3
	// FleeBudget: node expansions a hurried planner may spend (~a 140² room).
	FleeBudget = 20000
	// idleReset: a trip untouched this long starts fresh.
	idleReset = 20 * time.Second
	// clickReach: how far along the route a click waypoint may sit.
	clickReach = 22
	// leapHop: the travel leap's reach along the route.
	leapHop = 12
)

type trip struct {
	j       *journey.Journey
	goal    data.Position // the planning goal last asked (frame-clamped)
	purpose Purpose
	at      time.Time
}

type said struct {
	state State
	at    time.Time
}

// Mover holds the trips. One per process (Default); tests make their own.
type Mover struct {
	mu    sync.Mutex
	trips map[string]*trip
	said  map[string]said
	// Now is the clock (tests pin it). Log, when set, hears every status line.
	Now func() time.Time
	Log func(line string)
}

func New() *Mover {
	return &Mover{trips: map[string]*trip{}, said: map[string]said{}, Now: time.Now}
}

// Default is the executive's mover.
var Default = New()

// Forget drops a holder's trip: its next MoveTo starts a fresh journey.
func (mv *Mover) Forget(holder string) {
	mv.mu.Lock()
	delete(mv.trips, holder)
	mv.mu.Unlock()
}

// Goal reports the holder's current trip goal (debug overlays).
func (mv *Mover) Goal(holder string) (data.Position, bool) {
	mv.mu.Lock()
	defer mv.mu.Unlock()
	if t := mv.trips[holder]; t != nil {
		return t.goal, true
	}
	return data.Position{}, false
}

// MoveTo advances the holder ONE bounded step toward goal. The caller holds the
// actuator (a RoleSteer grant); every input it issues is a planned stride, a
// click on a route waypoint, or a leap along the route.
func (mv *Mover) MoveTo(env Env, goal data.Position, o Opts) Status {
	st := mv.move(env, goal, o)
	mv.say(env, goal, o, st)
	return st
}

func (mv *Mover) move(env Env, goal data.Position, o Opts) Status {
	me, ok := env.Body.Here()
	if !ok {
		return Status{State: Moving, Why: "perception gap"}
	}
	if goal == (data.Position{}) {
		return Status{State: NoPath, Why: "refused: unset goal"} // never walk toward the world origin
	}
	arrive := o.Arrive
	if arrive <= 0 {
		arrive = 2
		if o.Purpose.Hurried() {
			arrive = 1
		}
	}
	d := chebyshev(me, goal)
	if d <= arrive {
		return Status{State: Arrived, Why: fmt.Sprintf("within %d", arrive)}
	}
	if env.Grid == nil {
		// No grid at all: nothing to plan on, and unknown ground is walkable.
		return mv.reckon(env, o, goal, "no grid: dead reckoning")
	}
	ng := journey.NavGrid(env.Grid)
	if !ng.In(me) {
		// Standing outside the grid's frame (a seam read flipped the area and
		// the grid with it): no route starts here — walk it like no grid.
		return mv.reckon(env, o, goal, "standing off the grid's frame: dead reckoning")
	}

	// SHORT HOP: a clear line this short is one planned stride — no journey.
	if d <= HopTiles && ng.OpenLine(me, goal) {
		return mv.stride(env, o, goal, hold(o, 500*time.Millisecond), "hop", "short hop on a clear line")
	}
	// HURRIED COMMIT: flee/escape along a line the grid calls clear goes at
	// once — no planning between the threat and the first input.
	if o.Purpose.Hurried() && ng.In(goal) && ng.OpenLine(me, goal) {
		return mv.stride(env, o, goal, hold(o, 1200*time.Millisecond), "commit", "clear line")
	}

	// A goal beyond the frame (another area's side of a seam) is planned to
	// the frame's edge; at the edge the last stride runs off the map.
	pgoal := goal
	if !ng.In(goal) {
		pgoal = clampInto(env.Grid, goal)
		if chebyshev(me, pgoal) <= HopTiles {
			if ng.OpenLine(me, goal) {
				return mv.stride(env, o, goal, hold(o, 1200*time.Millisecond), "edge", "off the frame toward the goal")
			}
			return mv.fallback(env, o, ng, me, goal, NoPath, "goal beyond the frame, the edge line is walled")
		}
	}

	t := mv.tripFor(env, o, pgoal, arrive)

	if o.AllowLeap && env.Leap != nil && d >= 10 && (env.LeapReady == nil || env.LeapReady()) {
		if path, ok, _ := t.j.Route(me, env.Led); ok {
			hop := float64(min(leapHop, d-2))
			land, found := nav.AlongPath(path, hop, func(p data.Position) bool {
				return chebyshev(me, p) >= 6 && ng.Walkable(p) && ng.OpenLine(me, p)
			})
			if found && env.Leap(land) {
				t.j.Invalidate()
				return Status{State: Moving, Mode: "leap", Issued: true, Why: fmt.Sprintf("leap along the route to (%d,%d)", land.X, land.Y)}
			}
		}
	}

	if o.Click {
		key := o.CombatKey
		var way data.Position
		why := ""
		if path, ok, _ := t.j.Route(me, env.Led); ok {
			// The farthest route point in sight: the game walks a straight,
			// known-open line and rounds only what no grid knows (fences).
			way, _ = nav.AlongPath(path, clickReach, func(p data.Position) bool {
				return chebyshev(me, p) <= clickReach && ng.OpenLine(me, p)
			})
			why = fmt.Sprintf("click the route at (%d,%d)", way.X, way.Y)
		}
		if way == (data.Position{}) || way == me {
			way, why = goal, "no route: the game's pathfinder walks it"
		}
		if out := env.Body.Click(way, hold(o, 1400*time.Millisecond), key, o.Holder); out.Result == verbs.ResDone {
			t.j.Invalidate()
			return Status{State: Moving, Mode: "click", Issued: true, Why: why}
		}
		// refused or dead click: the planned stride below carries this tick
	}

	js := t.j.StepLegs(env.Body, env.Led)
	switch js.State {
	case journey.Arrived:
		if pgoal != goal {
			if ng.OpenLine(me, goal) {
				return mv.stride(env, o, goal, hold(o, 1200*time.Millisecond), "edge", "off the frame toward the goal")
			}
			return mv.fallback(env, o, ng, me, goal, NoPath, "goal beyond the frame, the edge line is walled")
		}
		return Status{State: Arrived, Mode: "journey", Why: js.Note}
	case journey.NoPath:
		mv.Forget(o.Holder)
		return mv.fallback(env, o, ng, me, goal, NoPath, js.Note)
	case journey.Stalled:
		mv.Forget(o.Holder)
		return mv.fallback(env, o, ng, me, goal, Stalled, js.Note)
	}
	if !js.Issued && o.Purpose.Hurried() {
		// A hurried mover never spends a tick without input (a perception gap
		// excepted: there is no position to step from).
		if js.Note != "perception gap" {
			return mv.fallback(env, o, ng, me, goal, Moving, js.Note)
		}
	}
	return Status{State: Moving, Mode: "journey", Issued: js.Issued,
		Blocked: js.Issued && js.Out.Result == verbs.ResBlocked, Why: js.Note}
}

// tripFor finds or opens the holder's trip toward pgoal: the same journey
// while the goal drifts ≤ RegoalRadius (stuck memory kept), a fresh one on a
// new goal, purpose, area frame, or after a long idle.
func (mv *Mover) tripFor(env Env, o Opts, pgoal data.Position, arrive int) *trip {
	now := mv.Now()
	mv.mu.Lock()
	defer mv.mu.Unlock()
	t := mv.trips[o.Holder]
	if !reuse(t, env.Grid, o.Purpose, pgoal, now) {
		t = &trip{j: journey.New(env.GR, env.Grid, pgoal, o.Holder), purpose: o.Purpose}
		mv.trips[o.Holder] = t
		if len(mv.trips) > 64 {
			for k, v := range mv.trips {
				if now.Sub(v.at) > time.Minute {
					delete(mv.trips, k)
				}
			}
		}
	} else {
		t.j.Regoal(pgoal)
	}
	t.goal, t.at = pgoal, now
	t.j.Arrive = arrive
	t.j.MaxHold = o.MaxHold
	t.j.NoAdopt = o.NoWallReplan
	t.j.Budget, t.j.MinHold = 0, 0
	if o.Purpose.Hurried() {
		t.j.Budget = FleeBudget
	}
	if o.Purpose == Escape {
		// An escape COMMITS: each planned pulse is the full hold toward its
		// visible carrot (the unstick rung is judged by displacement after
		// one act). A flee keeps the follower's short, re-aiming pulses.
		t.j.MinHold = o.MaxHold
	}
	return t
}

// reuse is the trip-reuse rule, pure for the tests.
func reuse(t *trip, g *game.Grid, p Purpose, goal data.Position, now time.Time) bool {
	return t != nil && t.purpose == p && t.j.Frame(g) && now.Sub(t.at) <= idleReset &&
		chebyshev(goal, t.goal) <= RegoalRadius
}

// reckon moves with nothing to plan on: the click gait clicks the goal (the
// game's pathfinder is the only map left), else one stride at it.
func (mv *Mover) reckon(env Env, o Opts, goal data.Position, why string) Status {
	if o.Click {
		if out := env.Body.Click(goal, hold(o, 1400*time.Millisecond), o.CombatKey, o.Holder); out.Result == verbs.ResDone {
			return Status{State: Moving, Mode: "click", Issued: true, Why: why + "; the game's pathfinder walks it"}
		}
	}
	return mv.stride(env, o, goal, hold(o, 1600*time.Millisecond), "reckon", why)
}

// stride issues one planned stride without a route (the caller checked the
// line, or there is nothing to check it against).
func (mv *Mover) stride(env Env, o Opts, to data.Position, h time.Duration, mode, why string) Status {
	mg := o.MinGain
	if mg <= 0 {
		mg = 1
	}
	out := env.Body.Stride(to, h, mg, o.Holder)
	return Status{State: Moving, Mode: mode, Issued: true, Blocked: out.Result == verbs.ResBlocked, Why: why}
}

// fallback: a hurried (or Fallback) mover that got no route still moves —
// nav's best clear step toward the goal. Everyone else gets the verdict.
func (mv *Mover) fallback(env Env, o Opts, ng *nav.Grid, me, goal data.Position, st State, why string) Status {
	if !o.Purpose.Hurried() && !o.Fallback {
		return Status{State: st, Why: why}
	}
	step, ok := ng.BestStep(me, goal)
	if !ok {
		return Status{State: st, Why: why + "; boxed in: no clear step"}
	}
	s := mv.stride(env, o, step, hold(o, 600*time.Millisecond), "fallback",
		fmt.Sprintf("%s; best clear step (%d,%d)", why, step.X, step.Y))
	s.State = st
	if st == Moving || o.Purpose.Hurried() {
		s.State = Moving // a hurried mover that stepped is moving; its caller re-aims next tick
	}
	return s
}

// say writes verb=move on a holder's status change (never every tick).
func (mv *Mover) say(env Env, goal data.Position, o Opts, st Status) {
	now := mv.Now()
	mv.mu.Lock()
	prev, seen := mv.said[o.Holder]
	change := !seen || prev.state != st.State || now.Sub(prev.at) > 30*time.Second
	if change {
		mv.said[o.Holder] = said{state: st.State, at: now}
	}
	mv.mu.Unlock()
	if !change {
		return
	}
	line := fmt.Sprintf("verb=move purpose=%s goal=(%d,%d) status=%s why=%s", o.Purpose, goal.X, goal.Y, st.State, st.Why)
	if st.Mode != "" {
		line += " mode=" + st.Mode
	}
	if env.Led != nil {
		res := verbs.ResDone
		switch {
		case st.State == NoPath || st.State == Stalled:
			res = verbs.ResBlocked
		case st.Blocked:
			res = verbs.ResBlocked
		}
		env.Led.Append(verbs.Outcome{Verb: "move", Holder: o.Holder, Target: fmt.Sprintf("(%d,%d)", goal.X, goal.Y),
			Result: res, Evidence: line})
	}
	if mv.Log != nil {
		mv.Log(line)
	}
}

func hold(o Opts, def time.Duration) time.Duration {
	if o.MaxHold > 0 {
		return o.MaxHold
	}
	return def
}

// clampInto pulls p inside g's frame (2 tiles in: the border row is wall-adjacent).
func clampInto(g *game.Grid, p data.Position) data.Position {
	x := max(g.OffsetX+2, min(p.X, g.OffsetX+g.Width-3))
	y := max(g.OffsetY+2, min(p.Y, g.OffsetY+g.Height-3))
	return data.Position{X: x, Y: y}
}

func chebyshev(a, b data.Position) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return max(dx, dy)
}
