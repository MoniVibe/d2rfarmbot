package coverage

// DefaultSight is how far (tiles, Euclidean) she is credited with seeing.
const DefaultSight = 14

type bitset []uint64

func newBitset(n int) bitset    { return make(bitset, (n+63)/64) }
func (b bitset) get(i int) bool { return b[i>>6]&(1<<(uint(i)&63)) != 0 }
func (b bitset) set(i int)      { b[i>>6] |= 1 << (uint(i) & 63) }
func (b bitset) clear(i int)    { b[i>>6] &^= 1 << (uint(i) & 63) }

// Map is the coverage memory of one (seed, area): which tiles she has SEEN,
// and which seen tiles were walls (so a room that later unloads — its cells
// reading Unknown again — does not resurrect its walls as fresh ground).
type Map struct {
	OffX, OffY, W, H int
	seen             bitset
	wall             bitset
	nSeen            int
	ver              uint64 // bumped on every change: the frontier cache key

	lastPos Pos
	lastTer *Terrain

	fc struct { // frontier cache
		ver   uint64
		ter   *Terrain
		chunk int
		out   []Cluster
	}
}

// New makes an empty map over a frame.
func New(offX, offY, w, h int) *Map {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return &Map{OffX: offX, OffY: offY, W: w, H: h, seen: newBitset(w * h), wall: newBitset(w * h)}
}

// NewFor makes an empty map over a terrain's frame.
func NewFor(t *Terrain) *Map { return New(t.OffX, t.OffY, t.W, t.H) }

// Fits: the map and the terrain share one frame (indices align).
func (m *Map) Fits(t *Terrain) bool {
	return t != nil && m.OffX == t.OffX && m.OffY == t.OffY && m.W == t.W && m.H == t.H
}

// Rebase re-anchors the map onto a terrain's frame, keeping every overlapping
// bit (a level frame should never move, but a map from disk must not be
// trusted blindly).
func (m *Map) Rebase(t *Terrain) {
	if m.Fits(t) {
		return
	}
	n := New(t.OffX, t.OffY, t.W, t.H)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			i := y*m.W + x
			if !m.seen.get(i) && !m.wall.get(i) {
				continue
			}
			nx, ny := x+m.OffX-n.OffX, y+m.OffY-n.OffY
			if nx < 0 || ny < 0 || nx >= n.W || ny >= n.H {
				continue
			}
			j := ny*n.W + nx
			if m.seen.get(i) {
				n.seen.set(j)
				n.nSeen++
			}
			if m.wall.get(i) {
				n.wall.set(j)
			}
		}
	}
	*m = *n
	m.ver++
}

func (m *Map) idx(p Pos) (int, bool) {
	x, y := p.X-m.OffX, p.Y-m.OffY
	if x < 0 || y < 0 || x >= m.W || y >= m.H {
		return 0, false
	}
	return y*m.W + x, true
}

// Seen reports whether world p has been seen.
func (m *Map) Seen(p Pos) bool {
	i, ok := m.idx(p)
	return ok && m.seen.get(i)
}

// SeenCount is the number of seen tiles (walls included).
func (m *Map) SeenCount() int { return m.nSeen }

// passable: walkable ground for coverage purposes. Loaded floor is truth; an
// unloaded cell is ground unless she once SAW it as a wall. Requires Fits.
func (m *Map) passable(t *Terrain, i int) bool {
	switch t.C[i] {
	case Floor:
		return true
	case Unknown:
		return !m.wall.get(i)
	}
	return false
}

// opaque: blocks sight. Walls, void and unstreamed ground (nobody can see what
// is not loaded).
func opaque(c Cell) bool { return c != Floor }

// Update marks every tile within radius of pos that has a clear line of sight
// (Bresenham over the terrain; walls, void and unknown block) as seen — the
// blocking wall itself included. Cheap to call every tick: it no-ops unless she
// moved or the terrain was rebuilt, and the pass is bounded to the radius.
// Returns the number of newly seen tiles.
func (m *Map) Update(t *Terrain, pos Pos, radius int) int {
	if t == nil {
		return 0
	}
	if !m.Fits(t) {
		m.Rebase(t)
	}
	if pos == m.lastPos && t == m.lastTer {
		return 0
	}
	m.lastPos, m.lastTer = pos, t
	if radius <= 0 {
		radius = DefaultSight
	}
	added := 0
	changed := false
	r2 := radius * radius
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx*dx+dy*dy > r2 {
				continue
			}
			q := Pos{X: pos.X + dx, Y: pos.Y + dy}
			i, ok := t.idx(q)
			if !ok {
				continue
			}
			c := t.C[i]
			if c == Void || c == Unknown {
				continue // nothing to see there (yet)
			}
			if !lineClear(t, pos, q) {
				continue
			}
			if !m.seen.get(i) {
				m.seen.set(i)
				m.nSeen++
				added++
				changed = true
			}
			if c == Wall && !m.wall.get(i) {
				m.wall.set(i)
				changed = true
			} else if c == Floor && m.wall.get(i) {
				m.wall.clear(i)
				changed = true
			}
		}
	}
	if changed {
		m.ver++
	}
	return added
}

// lineClear walks Bresenham from a to b; every cell strictly between them must
// be transparent. The endpoints never block (she may stand on a seam; the
// target wall is itself visible).
func lineClear(t *Terrain, a, b Pos) bool {
	dx, dy := absInt(b.X-a.X), -absInt(b.Y-a.Y)
	sx, sy := 1, 1
	if a.X > b.X {
		sx = -1
	}
	if a.Y > b.Y {
		sy = -1
	}
	err := dx + dy
	x, y := a.X, a.Y
	for {
		if x == b.X && y == b.Y {
			return true
		}
		if !(x == a.X && y == a.Y) && opaque(t.At(Pos{X: x, Y: y})) {
			return false
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x += sx
		}
		if e2 <= dx {
			err += dx
			y += sy
		}
	}
}

// Coverage is the seen share of the passable ground inside r (0..1). A rect
// with no ground reads 1 (nothing left to see).
func (m *Map) Coverage(t *Terrain, r Rect) float64 {
	if t == nil || !m.Fits(t) {
		return 0
	}
	x0, y0 := maxInt(r.X-m.OffX, 0), maxInt(r.Y-m.OffY, 0)
	x1, y1 := minInt(r.X+r.W-m.OffX, m.W), minInt(r.Y+r.H-m.OffY, m.H)
	total, seen := 0, 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := y*m.W + x
			if !m.passable(t, i) {
				continue
			}
			total++
			if m.seen.get(i) {
				seen++
			}
		}
	}
	if total == 0 {
		return 1
	}
	return float64(seen) / float64(total)
}

// CoverageAll is Coverage over the whole area.
func (m *Map) CoverageAll(t *Terrain) float64 {
	return m.Coverage(t, Rect{X: m.OffX, Y: m.OffY, W: m.W, H: m.H})
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
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

func cheb(a, b Pos) int {
	return maxInt(absInt(a.X-b.X), absInt(a.Y-b.Y))
}
