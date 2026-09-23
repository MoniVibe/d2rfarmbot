package nav

import (
	"fmt"
	"math"
	"time"
)

// State is the follower's explicit mode. Every change goes through set(), which
// reports it to OnTransition — the log line the owner reads when she wanders.
type State uint8

const (
	NoPlan    State = iota // needs a route: the caller must plan and SetPath
	Following              // tracking the path with a visibility-clamped carrot
	Arrived                // within ArriveR of the path's end
	Stuck                  // no along-path progress for StuckAfter (transient: picks an escape)
	Escaping               // an escape stride is out; the next Step asks for a replan
	Failed                 // escalation exhausted; sticky until something moves us away
)

func (s State) String() string {
	switch s {
	case NoPlan:
		return "noplan"
	case Following:
		return "following"
	case Arrived:
		return "arrived"
	case Stuck:
		return "stuck"
	case Escaping:
		return "escaping"
	default:
		return "failed"
	}
}

type CmdKind uint8

const (
	Move       CmdKind = iota // stride toward Target for HoldMs
	Replan                    // plan from here and SetPath, then Step again
	ArrivedCmd                // at the path's end (Target = that end)
	Fail                      // honest verdict; Reason says why
)

func (k CmdKind) String() string {
	switch k {
	case Move:
		return "move"
	case Replan:
		return "replan"
	case ArrivedCmd:
		return "arrived"
	default:
		return "fail"
	}
}

// Command is one tick's decision. A Move ALWAYS carries a real path-derived target.
type Command struct {
	Kind   CmdKind
	Target Pos
	HoldMs int
	Reason string
}

const (
	captureCross  = 6.0  // cross-track within which path progress is banked
	divergeCross  = 12.0 // beyond this we are not on the path any more: replan
	progressGain  = 1.5  // remaining-length drop that counts as progress
	anchorRadius  = 3    // a replan within this of the progress anchor keeps the stall clock
	stallClearDst = 15   // this far from the stall point, the escalation memory is spent
	triedRadius   = 3
	reviveDist    = 8 // a Failed follower moved this far (by someone else) may try again
	pauseGap      = 2 * time.Second
)

var escapeHolds = []int{300, 500, 800, 1200}

// Follower tracks one planned route. It never plans itself: it asks via Replan.
type Follower struct {
	ArriveR    float64       // default 5
	StuckAfter time.Duration // no-progress window; default 2.5s
	MaxEscapes int           // escapes from one stall before Fail; default len(escapeHolds)
	MaxReplans int           // divergence replans without progress before Fail; default 6
	// OnTransition, when set, hears every state change with its cause.
	OnTransition func(from, to State, why string)

	g     *Grid
	obs   []Obstacle
	state State

	pts        []Pos
	cumS       []float64
	committedS float64

	// Progress memory SURVIVES SetPath: a replan from the same spot must not restart
	// the stall clock (the old navigator reset it on every blocked stride's replan,
	// so its stuck check never fired).
	anchor   Pos
	anchorOK bool
	progAt   time.Time
	bestRem  float64

	// Escalation memory, keyed to the stall position.
	stalled  bool
	stallAt  Pos
	stallRem float64
	escapes  int
	tried    []Pos
	replans  int
	hx, hy   float64 // last commanded heading (unit), the obstruction's bearing when stuck
	failAt   Pos
	failWhy  string
	lastStep time.Time
}

func NewFollower(g *Grid, obs []Obstacle) *Follower {
	return &Follower{g: g, obs: obs}
}

func (f *Follower) State() State       { return f.state }
func (f *Follower) Path() []Pos        { return f.pts }
func (f *Follower) Escapes() int       { return f.escapes }
func (f *Follower) Committed() float64 { return f.committedS }

// SetObstacles swaps the live obstacle set (the caller replans on change).
func (f *Follower) SetObstacles(obs []Obstacle) { f.obs = obs }

func (f *Follower) defaults() {
	if f.ArriveR <= 0 {
		f.ArriveR = 5
	}
	if f.StuckAfter <= 0 {
		f.StuckAfter = 2500 * time.Millisecond
	}
	if f.MaxEscapes <= 0 {
		f.MaxEscapes = len(escapeHolds)
	}
	if f.MaxReplans <= 0 {
		f.MaxReplans = 6
	}
}

func (f *Follower) set(to State, why string) {
	if to == f.state {
		return
	}
	from := f.state
	f.state = to
	if f.OnTransition != nil {
		f.OnTransition(from, to, why)
	}
}

func fdist(a, b Pos) float64 { return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y)) }

func (f *Follower) total() float64 { return f.cumS[len(f.cumS)-1] }

// SetPath commits a (simplified) route planned from me.
func (f *Follower) SetPath(path []Pos, me Pos, now time.Time) {
	f.defaults()
	if len(path) == 0 {
		f.pts, f.cumS = nil, nil
		f.set(NoPlan, "empty path")
		return
	}
	f.pts = append([]Pos(nil), path...)
	f.cumS = make([]float64, len(f.pts))
	for i := 1; i < len(f.pts); i++ {
		f.cumS[i] = f.cumS[i-1] + fdist(f.pts[i-1], f.pts[i])
	}
	f.committedS = 0
	rem := f.total()
	if f.anchorOK && cheb(me, f.anchor) <= anchorRadius {
		f.bestRem = rem // same place, new yardstick — the clock keeps running
	} else {
		f.anchor, f.anchorOK, f.progAt, f.bestRem = me, true, now, rem
	}
	end := f.pts[len(f.pts)-1]
	f.set(Following, fmt.Sprintf("route %d legs %.0f tiles (%d,%d)->(%d,%d)", len(f.pts)-1, rem, me.X, me.Y, end.X, end.Y))
}

// Step decides this tick. It never returns a Move with an unset target.
func (f *Follower) Step(me Pos, now time.Time) Command {
	f.defaults()
	// Nobody stepped us for a while (a fight, a dodge, a menu took the actuator):
	// that time was not ours to progress in, so the stall clock pauses over it —
	// else every resumed journey opened with a spurious escape.
	if gap := now.Sub(f.lastStep); !f.lastStep.IsZero() && gap > pauseGap {
		f.progAt = f.progAt.Add(gap)
	}
	f.lastStep = now
	c := f.step(me, now)
	if c.Kind == Move && c.Target == (Pos{}) {
		return f.fail(me, "internal: move without a target")
	}
	return c
}

func (f *Follower) fail(me Pos, why string) Command {
	f.failAt, f.failWhy = me, why
	f.set(Failed, why)
	return Command{Kind: Fail, Reason: why}
}

func (f *Follower) replan(why string) Command {
	f.set(NoPlan, why)
	return Command{Kind: Replan, Reason: why}
}

func (f *Follower) resetEscalation() {
	f.stalled, f.escapes, f.tried, f.replans = false, 0, nil, 0
}

func (f *Follower) step(me Pos, now time.Time) Command {
	switch f.state {
	case Failed:
		if cheb(me, f.failAt) < reviveDist {
			return Command{Kind: Fail, Reason: f.failWhy}
		}
		f.resetEscalation()
		f.anchorOK = false
		return f.replan(fmt.Sprintf("revived: moved %d tiles since failure", cheb(me, f.failAt)))
	case Escaping:
		return f.replan(fmt.Sprintf("escape stride done at (%d,%d)", me.X, me.Y))
	}
	if f.state == NoPlan || len(f.pts) == 0 {
		return f.replan("no plan")
	}

	end := f.pts[len(f.pts)-1]
	if fdist(me, end) <= f.ArriveR {
		f.set(Arrived, fmt.Sprintf("at (%d,%d), end (%d,%d)", me.X, me.Y, end.X, end.Y))
		return Command{Kind: ArrivedCmd, Target: end}
	}
	if f.state == Arrived {
		f.set(Following, fmt.Sprintf("pushed off the end to (%d,%d)", me.X, me.Y))
	}

	rawS, cross := f.project(me)
	if cross <= captureCross && rawS > f.committedS && f.visible(me, f.pointAtS(rawS), false) {
		f.committedS = rawS // monotonic; the visibility test stops a U-turn around a thin wall banking the far leg
	}
	if cross > divergeCross {
		f.replans++
		if f.replans > f.MaxReplans {
			return f.fail(me, fmt.Sprintf("replan storm: %d divergences without progress at (%d,%d)", f.replans-1, me.X, me.Y))
		}
		return f.replan(fmt.Sprintf("diverged %.1f tiles off path at (%d,%d)", cross, me.X, me.Y))
	}

	rem := f.total() - f.committedS
	if rem < f.bestRem-progressGain {
		f.bestRem, f.progAt, f.anchor, f.anchorOK = rem, now, me, true
		f.replans = 0
		if f.stalled && (rem < f.stallRem-3 || cheb(me, f.stallAt) > stallClearDst) {
			f.resetEscalation()
		}
	} else if now.Sub(f.progAt) > f.StuckAfter {
		return f.stuck(me, now, rem)
	}

	tgt, hold := f.carrot(me)
	if d := fdist(me, tgt); d > 0.5 {
		f.hx, f.hy = float64(tgt.X-me.X)/d, float64(tgt.Y-me.Y)/d
	}
	return Command{Kind: Move, Target: tgt, HoldMs: hold}
}

func (f *Follower) stuck(me Pos, now time.Time, rem float64) Command {
	if !f.stalled {
		f.stalled, f.stallAt, f.stallRem = true, me, rem
	}
	f.set(Stuck, fmt.Sprintf("no progress %.1fs at (%d,%d)", now.Sub(f.progAt).Seconds(), me.X, me.Y))
	if f.escapes >= f.MaxEscapes {
		return f.fail(me, fmt.Sprintf("stuck at (%d,%d): %d escapes exhausted", me.X, me.Y, f.escapes))
	}
	esc, ok := f.escapeTarget(me)
	if !ok {
		return f.fail(me, fmt.Sprintf("stuck at (%d,%d): no untried escape", me.X, me.Y))
	}
	f.tried = append(f.tried, esc)
	hold := escapeHolds[minInt(f.escapes, len(escapeHolds)-1)]
	f.escapes++
	f.progAt, f.anchor, f.anchorOK = now, me, true // the escape gets its own progress window
	why := fmt.Sprintf("escape %d/%d to (%d,%d) clr=%d hold=%dms", f.escapes, f.MaxEscapes, esc.X, esc.Y, f.g.Clearance(esc), hold)
	f.set(Escaping, why)
	return Command{Kind: Move, Target: esc, HoldMs: hold, Reason: why}
}

// escapeTarget: a short straight primitive (the only thing the actuator executes)
// toward OPEN ground and away from whatever stopped us, never one already tried
// from this stall. The old ladder used fixed cardinal 14-tile offsets.
func (f *Follower) escapeTarget(me Pos) (Pos, bool) {
	hx, hy := f.hx, f.hy
	if hx == 0 && hy == 0 {
		if p := f.pointAtS(f.committedS + 3); p != me {
			d := fdist(me, p)
			hx, hy = float64(p.X-me.X)/d, float64(p.Y-me.Y)/d
		}
	}
	best, bestScore, found := Pos{}, math.Inf(-1), false
	for k := 0; k < 16; k++ {
		a := float64(k) * math.Pi / 8
		ca, sa := math.Cos(a), math.Sin(a)
		for _, r := range []float64{3, 5, 8} {
			end := Pos{X: me.X + int(math.Round(r*ca)), Y: me.Y + int(math.Round(r*sa))}
			if end == me || !f.g.Walkable(end) || inObstacleBody(f.obs, end) || !f.visible(me, end, false) {
				continue
			}
			near := false
			for _, t := range f.tried {
				if cheb(end, t) <= triedRadius {
					near = true
					break
				}
			}
			if near {
				continue
			}
			clr := float64(minInt(f.g.Clearance(end), 8))
			into := math.Max(0, ca*hx+sa*hy) // bearing back into the obstruction
			s, _ := f.project(end)
			gain := math.Max(-5, math.Min(5, s-f.committedS))
			score := 4*clr - 12*into + gain + 0.3*r
			if score > bestScore {
				best, bestScore, found = end, score, true
			}
		}
	}
	return best, found
}

// carrot: a lookahead point on the path scaled by clearance, clamped back until the
// straight shot is clear — on a mildly inflated grid (no wall-adjacent cells) so the
// shot doesn't skim walls, falling back to raw walkability when we stand in or the
// route runs through wall-adjacent space (a 1-wide door).
func (f *Follower) carrot(me Pos) (Pos, int) {
	clr := f.g.Clearance(me)
	L := math.Max(3, math.Min(14, 2+1.5*float64(clr)))
	total := f.total()
	lo := math.Min(f.committedS+1.5, total)
	hi := math.Min(f.committedS+L, total)
	pick := func(inflated bool) (Pos, bool) {
		for s := hi; s >= lo-1e-9; s-- {
			if p := f.pointAtS(s); f.visible(me, p, inflated) {
				return p, true
			}
		}
		return Pos{}, false
	}
	tgt, ok := Pos{}, false
	if clr >= 2 {
		tgt, ok = pick(true)
	}
	if !ok {
		tgt, ok = pick(false)
	}
	if !ok || tgt == me {
		tgt = f.pointAtS(lo) // best effort; stall detection judges it
		if tgt == me {
			tgt = f.pts[len(f.pts)-1]
		}
	}
	hold := 300
	if clr <= 3 {
		hold = 100
	} else if clr <= 7 {
		hold = 190
	}
	return tgt, hold
}

// visible: the straight shot a->b (excluding a, which may be a wall cell we stand
// in) crosses only walkable cells outside obstacle bodies; inflated also demands
// clearance >= 2.
func (f *Follower) visible(a, b Pos, inflated bool) bool {
	return walkLine(a, b, func(p Pos) bool {
		if p == a {
			return true
		}
		if !f.g.Walkable(p) || inObstacleBody(f.obs, p) {
			return false
		}
		return !inflated || f.g.Clearance(p) >= 2
	})
}

// project: arc length and cross-track distance of me's nearest point on the path,
// searched in a local window around the committed segment.
func (f *Follower) project(me Pos) (float64, float64) {
	if len(f.pts) == 1 {
		return 0, fdist(me, f.pts[0])
	}
	seg := 0
	for seg < len(f.cumS)-2 && f.cumS[seg+1] < f.committedS {
		seg++
	}
	lo, hi := maxInt(seg-1, 0), minInt(seg+8, len(f.pts)-2)
	bestS, bestCross := f.committedS, math.Inf(1)
	for i := lo; i <= hi; i++ {
		a, b := f.pts[i], f.pts[i+1]
		l := fdist(a, b)
		if l < 0.01 {
			continue
		}
		t := (float64(me.X-a.X)*float64(b.X-a.X) + float64(me.Y-a.Y)*float64(b.Y-a.Y)) / (l * l)
		t = math.Max(0, math.Min(1, t))
		px, py := float64(a.X)+t*float64(b.X-a.X), float64(a.Y)+t*float64(b.Y-a.Y)
		if cr := math.Hypot(float64(me.X)-px, float64(me.Y)-py); cr < bestCross {
			bestCross, bestS = cr, f.cumS[i]+t*l
		}
	}
	if math.IsInf(bestCross, 1) {
		bestCross = fdist(me, f.pointAtS(f.committedS))
	}
	return bestS, bestCross
}

func (f *Follower) pointAtS(s float64) Pos {
	if s <= 0 || len(f.pts) == 1 {
		return f.pts[0]
	}
	if s >= f.total() {
		return f.pts[len(f.pts)-1]
	}
	i := 0
	for i < len(f.cumS)-2 && f.cumS[i+1] < s {
		i++
	}
	l := f.cumS[i+1] - f.cumS[i]
	t := 0.0
	if l > 0.01 {
		t = (s - f.cumS[i]) / l
	}
	a, b := f.pts[i], f.pts[i+1]
	return Pos{X: a.X + int(math.Round(t*float64(b.X-a.X))), Y: a.Y + int(math.Round(t*float64(b.Y-a.Y)))}
}
