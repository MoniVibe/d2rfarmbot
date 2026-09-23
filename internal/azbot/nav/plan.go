package nav

import (
	"container/heap"
	"fmt"
	"math"
)

// Options tune the planner. Zero values take the defaults; a negative ClearW
// disables the clearance cost.
type Options struct {
	ClearR    int // clearance at/below which a cell costs extra; default 3
	ClearW    int // extra cost per missing clearance unit (a straight step is 10); default 8
	MaxExpand int // node-expansion budget; default 1,000,000
	SnapR     int // how far a walled start/goal may be moved to walkable ground; default 4
	Obstacles []Obstacle
}

func (o Options) withDefaults() Options {
	if o.ClearR <= 0 {
		o.ClearR = 3
	}
	if o.ClearW == 0 {
		o.ClearW = 8
	} else if o.ClearW < 0 {
		o.ClearW = 0
	}
	if o.MaxExpand <= 0 {
		o.MaxExpand = 1_000_000
	}
	if o.SnapR <= 0 {
		o.SnapR = 4
	}
	return o
}

// Plan is the planner's honest verdict.
type Plan struct {
	Found    bool
	Path     []Pos // raw 8-connected cells, start..goal (start may be a wall cell: step-out)
	Goal     Pos   // the goal actually planned to (differs from the ask when Snapped)
	Snapped  bool
	Reason   string // why not Found
	Expanded int
}

const (
	costStraight = 10
	costDiagonal = 14
	stepOutCost  = 4 // multiplier on distance travelled through walls when the start is walled
)

// nearestWalkable: the closest walkable cell within r (Euclidean), ties to higher
// clearance then scan order — deterministic.
func (g *Grid) nearestWalkable(p Pos, r int) (Pos, bool) {
	best, bestD, bestC, found := Pos{}, 1<<30, -1, false
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			q := Pos{X: p.X + dx, Y: p.Y + dy}
			d := dx*dx + dy*dy
			if d > r*r || !g.Walkable(q) {
				continue
			}
			c := g.Clearance(q)
			if d < bestD || (d == bestD && c > bestC) {
				best, bestD, bestC, found = q, d, c, true
			}
		}
	}
	return best, found
}

type node struct {
	f, h int32
	seq  uint32
	i    int32
}

type openSet []node

func (s openSet) Len() int { return len(s) }
func (s openSet) Less(a, b int) bool {
	if s[a].f != s[b].f {
		return s[a].f < s[b].f
	}
	if s[a].h != s[b].h {
		return s[a].h < s[b].h
	}
	return s[a].seq < s[b].seq
}
func (s openSet) Swap(a, b int) { s[a], s[b] = s[b], s[a] }
func (s *openSet) Push(x any)   { *s = append(*s, x.(node)) }
func (s *openSet) Pop() any     { o := *s; n := o[len(o)-1]; *s = o[:len(o)-1]; return n }

func octile(a, b Pos) int32 {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx < dy {
		dx, dy = dy, dx
	}
	return int32(costStraight*(dx-dy) + costDiagonal*dy)
}

// Plan runs A* over 8-neighbours on RAW walkability. A non-walkable cell is never
// entered, diagonals never cut a wall corner, and cells near walls cost extra so
// the route keeps off them where there is room — yet a 1-wide doorway still routes
// (the old hard-inflated grid made anything under 5 wide unroutable).
func (g *Grid) Plan(start, goal Pos, opt Options) Plan {
	o := opt.withDefaults()
	res := Plan{Goal: goal}
	if !g.In(start) {
		res.Reason = fmt.Sprintf("start (%d,%d) outside grid", start.X, start.Y)
		return res
	}
	if !g.Walkable(goal) {
		sg, ok := g.nearestWalkable(goal, o.SnapR)
		if !ok {
			res.Reason = fmt.Sprintf("goal (%d,%d) walled with no walkable cell within %d", goal.X, goal.Y, o.SnapR)
			return res
		}
		res.Goal, res.Snapped = sg, true
	}
	// A walled start (a stale read, a grid seam) steps out: every walkable cell within
	// SnapR seeds the search at a steep distance cost (crossing wall cells must stay
	// the shortest option), so the step-out lands on the side that leads to the goal.
	var seeds []int
	if g.Walkable(start) {
		i, _ := g.idx(start)
		seeds = []int{i}
	} else {
		for dy := -o.SnapR; dy <= o.SnapR; dy++ {
			for dx := -o.SnapR; dx <= o.SnapR; dx++ {
				q := Pos{X: start.X + dx, Y: start.Y + dy}
				if dx*dx+dy*dy <= o.SnapR*o.SnapR && g.Walkable(q) {
					i, _ := g.idx(q)
					seeds = append(seeds, i)
				}
			}
		}
		if len(seeds) == 0 {
			res.Reason = fmt.Sprintf("start (%d,%d) walled with no walkable cell within %d", start.X, start.Y, o.SnapR)
			return res
		}
	}

	var pen map[int32]int32
	if len(o.Obstacles) > 0 {
		pen = map[int32]int32{}
		for _, ob := range o.Obstacles {
			for dy := -ob.Radius; dy <= ob.Radius; dy++ {
				for dx := -ob.Radius; dx <= ob.Radius; dx++ {
					if i, ok := g.idx(Pos{X: ob.At.X + dx, Y: ob.At.Y + dy}); ok && int32(ob.Penalty) > pen[int32(i)] {
						pen[int32(i)] = int32(ob.Penalty)
					}
				}
			}
		}
	}

	gi, _ := g.idx(res.Goal)
	n := g.W * g.H
	gs := make([]int32, n)
	par := make([]int32, n)
	closed := make([]bool, n)
	for i := range gs {
		gs[i] = math.MaxInt32
		par[i] = -1
	}
	var seq uint32
	open := &openSet{}
	for _, si := range seeds {
		g0 := stepOutCost * octile(start, g.at(si))
		gs[si] = g0
		h := octile(g.at(si), res.Goal)
		seq++
		heap.Push(open, node{f: g0 + h, h: h, seq: seq, i: int32(si)})
	}
	found := false
	for open.Len() > 0 {
		cur := heap.Pop(open).(node)
		ci := int(cur.i)
		if closed[ci] {
			continue
		}
		closed[ci] = true
		res.Expanded++
		if ci == gi {
			found = true
			break
		}
		if res.Expanded >= o.MaxExpand {
			res.Reason = fmt.Sprintf("expansion budget %d exhausted", o.MaxExpand)
			return res
		}
		cx, cy := ci%g.W, ci/g.W
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				nx, ny := cx+dx, cy+dy
				if nx < 0 || ny < 0 || nx >= g.W || ny >= g.H {
					continue
				}
				ni := ny*g.W + nx
				if !g.walk[ni] || closed[ni] {
					continue
				}
				step := int32(costStraight)
				if dx != 0 && dy != 0 {
					// No corner cutting: both orthogonal neighbours must be open.
					if !g.walk[cy*g.W+nx] || !g.walk[ny*g.W+cx] {
						continue
					}
					step = costDiagonal
				}
				if c := int(g.clr[ni]); c <= o.ClearR {
					step += int32(o.ClearW * (o.ClearR + 1 - c))
				}
				if pen != nil {
					step += pen[int32(ni)]
				}
				ng := gs[ci] + step
				if ng < gs[ni] {
					gs[ni] = ng
					par[ni] = int32(ci)
					h := octile(g.at(ni), res.Goal)
					seq++
					heap.Push(open, node{f: ng + h, h: h, seq: seq, i: int32(ni)})
				}
			}
		}
	}
	if !found {
		res.Reason = fmt.Sprintf("no route from (%d,%d) to (%d,%d) (%d cells searched)", start.X, start.Y, res.Goal.X, res.Goal.Y, res.Expanded)
		return res
	}
	var rev []Pos
	for i := gi; i != -1; i = int(par[i]) {
		rev = append(rev, g.at(i))
	}
	path := make([]Pos, 0, len(rev)+1)
	if !g.Walkable(start) {
		path = append(path, start)
	}
	for k := len(rev) - 1; k >= 0; k-- {
		path = append(path, rev[k])
	}
	res.Path, res.Found = path, true
	return res
}

// walkLine visits every cell the straight segment a->b touches (dense sampling;
// a diagonal step also visits both corner cells, so a line never slips through a
// wall corner). Stops and returns false at the first cell ok rejects.
func walkLine(a, b Pos, ok func(Pos) bool) bool {
	dx, dy := b.X-a.X, b.Y-a.Y
	n := absInt(dx)
	if absInt(dy) > n {
		n = absInt(dy)
	}
	n *= 2
	if n == 0 {
		return ok(a)
	}
	prev := a
	if !ok(a) {
		return false
	}
	for s := 1; s <= n; s++ {
		t := float64(s) / float64(n)
		p := Pos{X: a.X + int(math.Round(float64(dx)*t)), Y: a.Y + int(math.Round(float64(dy)*t))}
		if p == prev {
			continue
		}
		if p.X != prev.X && p.Y != prev.Y {
			if !ok(Pos{X: p.X, Y: prev.Y}) || !ok(Pos{X: prev.X, Y: p.Y}) {
				return false
			}
		}
		if !ok(p) {
			return false
		}
		prev = p
	}
	return true
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// simplifyClearCap caps the clearance a shortcut must keep: a route running down
// the middle of a huge hall may cut across at 4, it need not stay at 20.
const simplifyClearCap = 4

// Simplify string-pulls a raw A* path into straight segments. A shortcut is taken
// only if it stays at least as far from walls (and as far from obstacles) as the
// stretch of path it replaces — the old puller happily re-hugged every wall the
// planner had paid to avoid.
func (g *Grid) Simplify(path []Pos, obs []Obstacle) []Pos {
	if len(path) < 3 {
		return append([]Pos(nil), path...)
	}
	out := []Pos{path[0]}
	i := 0
	for i < len(path)-1 {
		best := i + 1
		if g.Walkable(path[i]) {
			minC := minInt(g.Clearance(path[i]), g.Clearance(path[i+1]))
			maxP := maxInt(obstaclePenalty(obs, path[i]), obstaclePenalty(obs, path[i+1]))
			for j := i + 2; j < len(path) && j <= i+64; j++ {
				minC = minInt(minC, g.Clearance(path[j]))
				maxP = maxInt(maxP, obstaclePenalty(obs, path[j]))
				need := minInt(minC, simplifyClearCap)
				if walkLine(path[i], path[j], func(p Pos) bool {
					return g.Walkable(p) && g.Clearance(p) >= need && (obs == nil || obstaclePenalty(obs, p) <= maxP)
				}) {
					best = j
				}
			}
		}
		out = append(out, path[best])
		i = best
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
