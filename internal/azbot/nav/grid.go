// Package nav is azbot's pure movement core: a walkability grid with a clearance
// field, an A* planner that never enters a wall, a clearance-safe string-puller, a
// path follower with an explicit state machine, and the screen-carrot projection.
// No game, motor or Windows imports — everything here is proven by unit tests.
package nav

import "github.com/hectorgimenez/d2go/pkg/data"

// Pos is a world tile. Aliased so callers pass game positions straight through.
type Pos = data.Position

// Grid is a walkability bitmap anchored at a world offset, plus its clearance field.
type Grid struct {
	OffX, OffY int
	W, H       int
	walk       []bool
	clr        []int32 // Chebyshev distance to the nearest non-walkable cell (0 = wall)
}

// NewGrid builds a grid; walkable is asked in RELATIVE (x,y), row-major.
func NewGrid(offX, offY, w, h int, walkable func(x, y int) bool) *Grid {
	g := &Grid{OffX: offX, OffY: offY, W: w, H: h, walk: make([]bool, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			g.walk[y*w+x] = walkable(x, y)
		}
	}
	g.buildClearance()
	return g
}

// ParseGrid builds a grid from rows of '.' (walkable) and anything else (wall).
// Test/debug helper.
func ParseGrid(offX, offY int, rows []string) *Grid {
	w := 0
	for _, r := range rows {
		if len(r) > w {
			w = len(r)
		}
	}
	return NewGrid(offX, offY, w, len(rows), func(x, y int) bool {
		return x < len(rows[y]) && rows[y][x] == '.'
	})
}

// buildClearance: multi-source BFS from every wall cell; the outside of the grid
// is wall too, so border cells never read as open ground.
func (g *Grid) buildClearance() {
	const inf = int32(1 << 30)
	g.clr = make([]int32, g.W*g.H)
	q := make([]int32, 0, g.W*g.H/4+16)
	for i, ok := range g.walk {
		if ok {
			g.clr[i] = inf
		} else {
			q = append(q, int32(i))
		}
	}
	// Border cells are 1 from the out-of-bounds wall. Appended after the walls so
	// the queue stays in nondecreasing distance order.
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			if x != 0 && y != 0 && x != g.W-1 && y != g.H-1 {
				continue
			}
			i := y*g.W + x
			if g.clr[i] > 1 {
				g.clr[i] = 1
				q = append(q, int32(i))
			}
		}
	}
	for head := 0; head < len(q); head++ {
		i := int(q[head])
		x, y, d := i%g.W, i/g.W, g.clr[i]
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx, ny := x+dx, y+dy
				if (dx == 0 && dy == 0) || nx < 0 || ny < 0 || nx >= g.W || ny >= g.H {
					continue
				}
				j := ny*g.W + nx
				if g.clr[j] > d+1 {
					g.clr[j] = d + 1
					q = append(q, int32(j))
				}
			}
		}
	}
}

func (g *Grid) idx(p Pos) (int, bool) {
	x, y := p.X-g.OffX, p.Y-g.OffY
	if x < 0 || y < 0 || x >= g.W || y >= g.H {
		return 0, false
	}
	return y*g.W + x, true
}

func (g *Grid) at(i int) Pos { return Pos{X: i%g.W + g.OffX, Y: i/g.W + g.OffY} }

// In reports whether p lies inside the grid.
func (g *Grid) In(p Pos) bool { _, ok := g.idx(p); return ok }

// Walkable: out-of-bounds is never walkable.
func (g *Grid) Walkable(p Pos) bool {
	i, ok := g.idx(p)
	return ok && g.walk[i]
}

// Clearance of p (0 for walls and out-of-bounds).
func (g *Grid) Clearance(p Pos) int {
	i, ok := g.idx(p)
	if !ok {
		return 0
	}
	return int(g.clr[i])
}

// Obstacle is a soft cost bubble around a live object/unit the static grid does
// not know about. The planner pays Penalty per cell within Radius (Chebyshev); the
// follower treats the object's body (radius 1) as solid for line of sight.
type Obstacle struct {
	At      Pos
	Radius  int
	Penalty int
}

const obstacleBody = 1

func obstaclePenalty(obs []Obstacle, p Pos) int {
	pen := 0
	for _, o := range obs {
		if cheb(p, o.At) <= o.Radius && o.Penalty > pen {
			pen = o.Penalty
		}
	}
	return pen
}

func inObstacleBody(obs []Obstacle, p Pos) bool {
	for _, o := range obs {
		if cheb(p, o.At) <= obstacleBody {
			return true
		}
	}
	return false
}

func cheb(a, b Pos) int {
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
