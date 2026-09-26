package coverage

// Path costs match the nav planner's metric: 10 per straight step, 14 per
// diagonal, 8-connected, a diagonal never cuts a blocked corner.
const (
	costStraight = 10
	costDiagonal = 14
	// Unreachable marks a cell the cost field never reached.
	Unreachable = int32(1<<31 - 1)
)

// CostField is a single-source shortest-path field from one start: every
// frontier goal's PATH cost from one Dijkstra, instead of a planner call per
// cluster.
type CostField struct {
	m      *Map
	dist   []int32
	parent []int32
	start  int
}

type cfItem struct {
	d int32
	i int32
}

// cfHeap is a typed binary min-heap (container/heap's interface calls cost 2×
// on a full-area field).
type cfHeap []cfItem

func (h cfHeap) less(a, b int) bool {
	return h[a].d < h[b].d || (h[a].d == h[b].d && h[a].i < h[b].i)
}

func (h *cfHeap) push(it cfItem) {
	*h = append(*h, it)
	s := *h
	for c := len(s) - 1; c > 0; {
		p := (c - 1) / 2
		if !s.less(c, p) {
			break
		}
		s[c], s[p] = s[p], s[c]
		c = p
	}
}

func (h *cfHeap) pop() cfItem {
	s := *h
	top := s[0]
	n := len(s) - 1
	s[0] = s[n]
	s = s[:n]
	for p := 0; ; {
		l, r, m := 2*p+1, 2*p+2, p
		if l < n && s.less(l, m) {
			m = l
		}
		if r < n && s.less(r, m) {
			m = r
		}
		if m == p {
			break
		}
		s[p], s[m] = s[m], s[p]
		p = m
	}
	*h = s
	return top
}

// Costs runs Dijkstra from `from` over the map's passable ground (loaded floor,
// plus unloaded ground not remembered as wall — optimistic, like the live
// planner). A start off the ground steps out to passable cells within 4 at a
// steep price. When targets is non-empty the search stops once every target is
// settled. Returns nil when no start cell exists.
func (m *Map) Costs(t *Terrain, from Pos, targets []Pos) *CostField {
	if t == nil || !m.Fits(t) {
		return nil
	}
	n := m.W * m.H
	cf := &CostField{m: m, dist: make([]int32, n), parent: make([]int32, n), start: -1}
	for i := range cf.dist {
		cf.dist[i] = Unreachable
		cf.parent[i] = -1
	}
	pass := make([]bool, n)
	for i := range pass {
		pass[i] = m.passable(t, i)
	}
	h := &cfHeap{}
	if si, ok := m.idx(from); ok && pass[si] {
		cf.dist[si], cf.start = 0, si
		h.push(cfItem{0, int32(si)})
	} else {
		const snap = 4
		for dy := -snap; dy <= snap; dy++ {
			for dx := -snap; dx <= snap; dx++ {
				j, ok := m.idx(Pos{X: from.X + dx, Y: from.Y + dy})
				if !ok || !pass[j] {
					continue
				}
				d := int32(maxInt(absInt(dx), absInt(dy)) * costStraight * 4)
				if d < cf.dist[j] {
					cf.dist[j] = d
					h.push(cfItem{d, int32(j)})
				}
			}
		}
		if si, ok := m.idx(from); ok {
			cf.start = si
		}
	}
	if len(*h) == 0 {
		return nil
	}
	pending := map[int]bool{}
	for _, p := range targets {
		if i, ok := m.idx(p); ok && pass[i] {
			pending[i] = true
		}
	}
	early := len(pending) > 0
	for len(*h) > 0 {
		it := h.pop()
		i := int(it.i)
		if it.d != cf.dist[i] {
			continue
		}
		if early {
			delete(pending, i)
			if len(pending) == 0 {
				break
			}
		}
		x, y := i%m.W, i/m.W
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx, ny := x+dx, y+dy
				if (dx == 0 && dy == 0) || nx < 0 || ny < 0 || nx >= m.W || ny >= m.H {
					continue
				}
				j := ny*m.W + nx
				if !pass[j] {
					continue
				}
				step := int32(costStraight)
				if dx != 0 && dy != 0 {
					if !pass[y*m.W+nx] || !pass[ny*m.W+x] {
						continue // no corner cutting
					}
					step = costDiagonal
				}
				if nd := it.d + step; nd < cf.dist[j] {
					cf.dist[j] = nd
					cf.parent[j] = int32(i)
					h.push(cfItem{nd, int32(j)})
				}
			}
		}
	}
	return cf
}

// Dist is the path cost to p (Unreachable when the field never reached it).
func (cf *CostField) Dist(p Pos) int32 {
	if cf == nil {
		return Unreachable
	}
	i, ok := cf.m.idx(p)
	if !ok {
		return Unreachable
	}
	return cf.dist[i]
}

// Path returns the cells from the start region to p (nil if unreachable).
func (cf *CostField) Path(p Pos) []Pos {
	if cf.Dist(p) == Unreachable {
		return nil
	}
	i, _ := cf.m.idx(p)
	var rev []Pos
	for guard := 0; i >= 0 && guard < len(cf.dist); guard++ {
		rev = append(rev, Pos{X: i%cf.m.W + cf.m.OffX, Y: i/cf.m.W + cf.m.OffY})
		i = int(cf.parent[i])
	}
	for a, b := 0, len(rev)-1; a < b; a, b = a+1, b-1 {
		rev[a], rev[b] = rev[b], rev[a]
	}
	return rev
}
