package coverage

// DefaultChunk bounds a cluster's extent (tiles): one long rim of frontier
// (open ground seen from its middle) becomes several goals, not one goal
// whose centroid lands back at her feet.
const DefaultChunk = 16

// Cluster is one connected piece of frontier.
type Cluster struct {
	ID       int // scan order within this extraction
	Size     int
	Centroid Pos // the member nearest the members' mean: always seen, walkable ground
	Cells    []Pos
}

// isFrontier: seen, passable, and 4-adjacent to passable ground never seen.
func (m *Map) isFrontier(t *Terrain, i int) bool {
	if !m.seen.get(i) || !m.passable(t, i) {
		return false
	}
	x, y := i%m.W, i/m.W
	for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		nx, ny := x+d[0], y+d[1]
		if nx < 0 || ny < 0 || nx >= m.W || ny >= m.H {
			continue
		}
		j := ny*m.W + nx
		if !m.seen.get(j) && m.passable(t, j) {
			return true
		}
	}
	return false
}

// Frontier extracts the frontier clustered 8-connected, each cluster confined
// to one chunk×chunk bin of the frame (chunk <= 0: no split). Deterministic:
// clusters come in scan order of their first cell. Cached per (map version,
// terrain, chunk) — calling it every tick costs one scan only after a change.
func (m *Map) Frontier(t *Terrain, chunk int) []Cluster {
	if t == nil || !m.Fits(t) {
		return nil
	}
	if m.fc.out != nil && m.fc.ver == m.ver && m.fc.ter == t && m.fc.chunk == chunk {
		return m.fc.out
	}
	n := m.W * m.H
	front := newBitset(n)
	any := false
	for i := 0; i < n; i++ {
		if m.seen.get(i) && m.isFrontier(t, i) {
			front.set(i)
			any = true
		}
	}
	out := []Cluster{}
	if any {
		done := newBitset(n)
		bin := func(i int) (int, int) {
			if chunk <= 0 {
				return 0, 0
			}
			return (i % m.W) / chunk, (i / m.W) / chunk
		}
		var q []int
		for i := 0; i < n; i++ {
			if !front.get(i) || done.get(i) {
				continue
			}
			bx, by := bin(i)
			q = append(q[:0], i)
			done.set(i)
			var cells []Pos
			sx, sy := 0, 0
			for h := 0; h < len(q); h++ {
				c := q[h]
				cx, cy := c%m.W, c/m.W
				cells = append(cells, Pos{X: cx + m.OffX, Y: cy + m.OffY})
				sx += cx
				sy += cy
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						nx, ny := cx+dx, cy+dy
						if (dx == 0 && dy == 0) || nx < 0 || ny < 0 || nx >= m.W || ny >= m.H {
							continue
						}
						j := ny*m.W + nx
						if !front.get(j) || done.get(j) {
							continue
						}
						if jx, jy := bin(j); jx != bx || jy != by {
							continue
						}
						done.set(j)
						q = append(q, j)
					}
				}
			}
			// Snap the mean to the nearest member: a centroid is a real,
			// seen, walkable tile she can be sent to.
			mx, my := float64(sx)/float64(len(cells))+float64(m.OffX), float64(sy)/float64(len(cells))+float64(m.OffY)
			best, bd := cells[0], 1e18
			for _, c := range cells {
				ddx, ddy := float64(c.X)-mx, float64(c.Y)-my
				if d := ddx*ddx + ddy*ddy; d < bd {
					best, bd = c, d
				}
			}
			out = append(out, Cluster{ID: len(out), Size: len(cells), Centroid: best, Cells: cells})
		}
	}
	m.fc.ver, m.fc.ter, m.fc.chunk, m.fc.out = m.ver, t, chunk, out
	return out
}
