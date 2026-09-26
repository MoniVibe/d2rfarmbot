package activity

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/game"
)

// ---------------------------------------------------------------- the map's walkable border
//
// With the seed read fixed (2026-09-26) the map's collision grids are TRUE,
// but koolo-map's walk-through exit points are not: 1->2 sat inside Blood
// Moor's far side, 2->3 outside both levels, and the door picker rightly
// rejected them — so Cold Plains was sought by exploration and she wandered
// into the Den of Evil. The border is where two level frames touch; the gap
// through it is where both grids are walkable across that edge.

// mapEdgeExit returns the center of the walkable run along the shared edge of
// cur and hop that is nearest me, a couple of cells inside cur.
func mapEdgeExit(cur, hop *game.Grid, me data.Position) (data.Position, bool) {
	if cur == nil || hop == nil || cur.Width == 0 || hop.Width == 0 {
		return data.Position{}, false
	}
	cx0, cy0, cx1, cy1 := cur.OffsetX, cur.OffsetY, cur.OffsetX+cur.Width, cur.OffsetY+cur.Height
	hx0, hy0, hx1, hy1 := hop.OffsetX, hop.OffsetY, hop.OffsetX+hop.Width, hop.OffsetY+hop.Height
	// in: the cell on cur's side of the edge; out: its neighbour across (in hop).
	type edge struct {
		cells    func(i int) (in, out data.Position)
		from, to int
		inward   data.Position // a step deeper into cur
	}
	var edges []edge
	if lo, hi := max(cy0, hy0), min(cy1, hy1); lo < hi {
		if cx1 == hx0 { // hop is east
			edges = append(edges, edge{func(y int) (data.Position, data.Position) {
				return data.Position{X: cx1 - 1, Y: y}, data.Position{X: hx0, Y: y}
			}, lo, hi, data.Position{X: -1}})
		}
		if cx0 == hx1 { // hop is west
			edges = append(edges, edge{func(y int) (data.Position, data.Position) {
				return data.Position{X: cx0, Y: y}, data.Position{X: hx1 - 1, Y: y}
			}, lo, hi, data.Position{X: 1}})
		}
	}
	if lo, hi := max(cx0, hx0), min(cx1, hx1); lo < hi {
		if cy1 == hy0 { // hop is south
			edges = append(edges, edge{func(x int) (data.Position, data.Position) {
				return data.Position{X: x, Y: cy1 - 1}, data.Position{X: x, Y: hy0}
			}, lo, hi, data.Position{Y: -1}})
		}
		if cy0 == hy1 { // hop is north
			edges = append(edges, edge{func(x int) (data.Position, data.Position) {
				return data.Position{X: x, Y: cy0}, data.Position{X: x, Y: hy1 - 1}
			}, lo, hi, data.Position{Y: 1}})
		}
	}
	best, bestD, found := data.Position{}, 1<<30, false
	for _, e := range edges {
		runStart := -1
		flush := func(end int) {
			if runStart < 0 || end-runStart < 3 { // a gap narrower than 3 cells is noise
				runStart = -1
				return
			}
			mid, _ := e.cells((runStart + end - 1) / 2)
			p := data.Position{X: mid.X + 3*e.inward.X, Y: mid.Y + 3*e.inward.Y}
			if d := chebyshev(me, p); d < bestD {
				best, bestD, found = p, d, true
			}
			runStart = -1
		}
		for i := e.from; i < e.to; i++ {
			in, out := e.cells(i)
			if cur.IsWalkable(in) && hop.IsWalkable(out) {
				if runStart < 0 {
					runStart = i
				}
				continue
			}
			flush(i)
		}
		flush(e.to)
	}
	return best, found
}
