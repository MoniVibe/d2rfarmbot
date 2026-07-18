package journey

import (
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/pather/astar"
)

// Navigator: a persistent, clearance-aware polyline follower with monotonic path progress
// (advisor design in nav1.md). It fixes the "hug the wall, get dragged back" failure by:
//  1. planning ONCE against a clearance-INFLATED grid (no per-tick replan, no wall-skimming),
//  2. simplifying the route to a straight-segment polyline,
//  3. tracking progress as monotonic arc length along the path (never regress),
//  4. steering to a visibility-clamped lookahead carrot,
//  5. detecting "stuck" from lack of ALONG-PATH progress (not raw displacement),
//  6. recovering with a straight-primitive local search — never heading-rotation/centroid.
const (
	navHardRadius = 2   // subtiles: cells this close to a wall are unavailable for planning
	navCapture    = 6.0 // cross-track tolerance to bank path progress
	navArrive     = 5.0 // consider goal reached within this
	navMinPlanMs  = 1200
)

type Navigator struct {
	grid  *game.Grid // aligned collision grid (raw)
	infl  *game.Grid // hard-inflated planning grid (static walls only)
	planG *game.Grid // planning grid = infl + live object obstacles (inflated)
	execG *game.Grid // executor grid = raw + live object obstacles (thin)
	clr   [][]int     // clearance = chebyshev distance to nearest blocked cell

	pts        []data.Position // simplified world-coord polyline
	cumS       []float64       // cumulative arc length to each point
	committedS float64
	goal       data.Position
	havePlan   bool
	planAt     time.Time

	progS      float64 // last committedS at which we banked progress
	progAt     time.Time
	stuckHits  int
}

func fdist(a, b data.Position) float64 {
	dx, dy := float64(a.X-b.X), float64(a.Y-b.Y)
	return math.Sqrt(dx*dx + dy*dy)
}

// NewNavigator builds the clearance field + inflated planning grid once for a level's grid.
func NewNavigator(g *game.Grid) *Navigator {
	n := &Navigator{grid: g}
	n.buildClearance()
	n.planG, n.execG = n.infl, n.grid
	return n
}

// SetObstacles marks live object/unit positions as blocked (they aren't in the static tile grid
// but physically stop movement — chests, stalls, the well, NPCs). Rebuilds the plan/exec grids.
func (n *Navigator) SetObstacles(obs []data.Position) {
	if len(obs) == 0 {
		n.planG, n.execG = n.infl, n.grid
		return
	}
	pg, eg := n.infl.Copy(), n.grid.Copy()
	block := func(g *game.Grid, p data.Position, r int) {
		rp := g.RelativePosition(p)
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				x, y := rp.X+dx, rp.Y+dy
				if x >= 0 && y >= 0 && x < g.Width && y < g.Height {
					g.CollisionGrid[y][x] = game.CollisionTypeNonWalkable
				}
			}
		}
	}
	for _, o := range obs {
		block(pg, o, 3) // keep the ROUTE well clear of objects
		block(eg, o, 1) // keep straight force-moves from clipping an object
	}
	n.planG, n.execG = pg, eg
}

func (n *Navigator) buildClearance() {
	h, w := n.grid.Height, n.grid.Width
	const INF = 1 << 30
	dist := make([][]int, h)
	type pt struct{ x, y int }
	q := make([]pt, 0, w*h/4)
	for y := 0; y < h; y++ {
		dist[y] = make([]int, w)
		for x := 0; x < w; x++ {
			if n.grid.CollisionGrid[y][x] == game.CollisionTypeNonWalkable {
				dist[y][x] = 0
				q = append(q, pt{x, y})
			} else {
				dist[y][x] = INF
			}
		}
	}
	for head := 0; head < len(q); head++ {
		p := q[head]
		d := dist[p.y][p.x]
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				nx, ny := p.x+dx, p.y+dy
				if nx < 0 || ny < 0 || nx >= w || ny >= h {
					continue
				}
				if dist[ny][nx] > d+1 {
					dist[ny][nx] = d + 1
					q = append(q, pt{nx, ny})
				}
			}
		}
	}
	n.clr = dist
	// Inflated grid: cells within hardRadius of a wall become non-walkable for planning.
	n.infl = n.grid.Copy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if dist[y][x] <= navHardRadius {
				n.infl.CollisionGrid[y][x] = game.CollisionTypeNonWalkable
			}
		}
	}
}

func (n *Navigator) clearanceAt(p data.Position) int {
	r := n.grid.RelativePosition(p)
	if r.X < 0 || r.Y < 0 || r.X >= n.grid.Width || r.Y >= n.grid.Height {
		return 0
	}
	return n.clr[r.Y][r.X]
}

// segClear reports whether the straight world segment a->b crosses only walkable cells of grid g.
// Planning/simplify use the INFLATED grid (keep the route off walls); the executor uses the RAW
// grid (so we can still move when standing in inflated space next to a building).
func segClear(g *game.Grid, a, b data.Position) bool {
	steps := chebyshev(a, b)
	if steps == 0 {
		return g.IsWalkable(a)
	}
	for s := 0; s <= steps; s++ {
		p := data.Position{X: a.X + (b.X-a.X)*s/steps, Y: a.Y + (b.Y-a.Y)*s/steps}
		if !g.IsWalkable(p) {
			return false
		}
	}
	return true
}

// simplify string-pulls the raw A* nodes into the furthest-reachable straight segments.
func (n *Navigator) simplify(path []data.Position) []data.Position {
	if len(path) < 2 {
		return path
	}
	out := []data.Position{path[0]}
	i := 0
	for i < len(path)-1 {
		best := i + 1
		for j := i + 2; j < len(path); j++ {
			if !segClear(n.planG, path[i], path[j]) { // simplify keeps the route off walls+objects
				break
			}
			best = j
		}
		out = append(out, path[best])
		i = best
	}
	return out
}

// BuildPlan computes and commits a route from me to goal. Plans on the inflated grid; if the
// player is currently inside inflated space (hugging a wall), falls back to the raw grid so a
// route still exists (the executor's clearance-seeking gets us back to open ground).
func (n *Navigator) BuildPlan(me, goal data.Position, now time.Time) bool {
	pg := n.planG
	if !n.planG.IsWalkable(me) {
		pg = n.execG // player is inside inflated/obstacle space — plan on the thinner grid
	}
	start := pg.RelativePosition(me)
	g := pg.RelativePosition(goal)
	if start.X < 0 || start.Y < 0 || start.X >= pg.Width || start.Y >= pg.Height ||
		g.X < 0 || g.Y < 0 || g.X >= pg.Width || g.Y >= pg.Height {
		return false
	}
	path, _, found := astar.CalculatePath(pg, start, g)
	if !found || len(path) < 2 {
		return false
	}
	world := make([]data.Position, len(path))
	for i, p := range path {
		world[i] = data.Position{X: p.X + pg.OffsetX, Y: p.Y + pg.OffsetY}
	}
	sp := n.simplify(world)
	n.pts = sp
	n.cumS = make([]float64, len(sp))
	for i := 1; i < len(sp); i++ {
		n.cumS[i] = n.cumS[i-1] + fdist(sp[i-1], sp[i])
	}
	n.committedS = 0
	n.goal = goal
	n.havePlan = true
	n.planAt = now
	n.progS = 0
	n.progAt = now
	n.stuckHits = 0
	return true
}

// project returns arc length + cross-track error of me's nearest point on the path (local window).
func (n *Navigator) project(me data.Position) (arcS, cross float64) {
	// find current committed segment index
	segIdx := 0
	for segIdx < len(n.cumS)-1 && n.cumS[segIdx+1] < n.committedS {
		segIdx++
	}
	bestS, bestCross := n.committedS, math.Inf(1)
	lo, hi := segIdx-1, segIdx+8
	if lo < 0 {
		lo = 0
	}
	if hi >= len(n.pts)-1 {
		hi = len(n.pts) - 2
	}
	for i := lo; i <= hi; i++ {
		a, b := n.pts[i], n.pts[i+1]
		segLen := fdist(a, b)
		if segLen < 0.01 {
			continue
		}
		// projection parameter t of me onto segment a->b
		t := (float64(me.X-a.X)*float64(b.X-a.X) + float64(me.Y-a.Y)*float64(b.Y-a.Y)) / (segLen * segLen)
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		px := float64(a.X) + t*float64(b.X-a.X)
		py := float64(a.Y) + t*float64(b.Y-a.Y)
		cr := math.Hypot(float64(me.X)-px, float64(me.Y)-py)
		s := n.cumS[i] + t*segLen
		if cr < bestCross {
			bestCross, bestS = cr, s
		}
	}
	return bestS, bestCross
}

func (n *Navigator) pointAtS(s float64) data.Position {
	if s <= 0 {
		return n.pts[0]
	}
	total := n.cumS[len(n.cumS)-1]
	if s >= total {
		return n.pts[len(n.pts)-1]
	}
	i := 0
	for i < len(n.cumS)-1 && n.cumS[i+1] < s {
		i++
	}
	segLen := n.cumS[i+1] - n.cumS[i]
	t := 0.0
	if segLen > 0.01 {
		t = (s - n.cumS[i]) / segLen
	}
	a, b := n.pts[i], n.pts[i+1]
	return data.Position{X: a.X + int(t*float64(b.X-a.X)), Y: a.Y + int(t*float64(b.Y-a.Y))}
}

// NavStep is one tick's decision for the caller to execute.
type NavStep struct {
	Target   data.Position // world point to force-move toward
	HoldMs   int           // force-move pulse duration
	Arrived  bool          // at goal
	Diverged bool          // pushed far off path — caller should replan
	Stuck    bool          // path progress stalled — recovery target provided
}

// Step advances the follower one tick and returns where to move.
func (n *Navigator) Step(me data.Position, now time.Time) NavStep {
	if !n.havePlan {
		return NavStep{Diverged: true}
	}
	rawS, cross := n.project(me)
	if cross <= navCapture && rawS > n.committedS {
		n.committedS = rawS
	}
	// arrived?
	if fdist(me, n.goal) <= navArrive || n.committedS >= n.cumS[len(n.cumS)-1]-navArrive {
		return NavStep{Target: n.goal, Arrived: true}
	}
	// diverged? (far off the path for real)
	if cross > 12 {
		return NavStep{Diverged: true}
	}
	// path-conditioned stuck detection: did committedS advance meaningfully lately?
	if n.committedS > n.progS+1.5 {
		n.progS, n.progAt, n.stuckHits = n.committedS, now, 0
	} else if now.Sub(n.progAt) > 2500*time.Millisecond {
		n.stuckHits++
		n.progAt = now
		if tgt, ok := n.localEscape(me); ok {
			return NavStep{Target: tgt, HoldMs: 80, Stuck: true}
		}
		return NavStep{Diverged: true} // escape found nothing — replan (or retire crossing)
	}
	// dynamic lookahead by clearance
	clr := float64(n.clearanceAt(me))
	L := 2.0 + 1.5*clr
	if L < 3 {
		L = 3
	} else if L > 14 {
		L = 14
	}
	targetS := n.committedS + L
	// clamp back until the straight shot is clear
	for targetS > n.committedS+1.5 {
		if segClear(n.execG, me, n.pointAtS(targetS)) { // executor: raw grid + object obstacles
			break
		}
		targetS -= 1.0
	}
	hold := 300
	if clr <= 3 {
		hold = 100
	} else if clr <= 7 {
		hold = 190
	}
	return NavStep{Target: n.pointAtS(targetS), HoldMs: hold}
}

// localEscape searches short straight primitives (the only actions the actuator can execute) and
// returns the endpoint that best improves along-path progress without regressing — deterministic,
// unlike heading rotation. Endpoints are scored by path-progress gain, clearance, cross-track.
func (n *Navigator) localEscape(me data.Position) (data.Position, bool) {
	baseS, _ := n.project(me)
	bestScore := -1e18
	var best data.Position
	found := false
	for k := 0; k < 32; k++ {
		ang := float64(k) / 32.0 * 2 * math.Pi
		for _, r := range []int{2, 4, 6} {
			end := data.Position{X: me.X + int(float64(r)*math.Cos(ang)), Y: me.Y + int(float64(r)*math.Sin(ang))}
			if !segClear(n.execG, me, end) { // escape probes raw walkability + object obstacles
				continue
			}
			s, cross := n.project(end)
			dS := s - baseS
			score := 20*dS + 3*float64(n.clearanceAt(end)) - 5*cross
			if dS < -1 {
				score -= 100
			}
			if score > bestScore {
				bestScore, best, found = score, end, true
			}
		}
	}
	return best, found
}
