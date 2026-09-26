package nav

import "math"

// OpenLine reports whether the straight shot a->b (excluding a, which may be a
// wall cell we stand in) crosses only open ground: walkable cells inside the
// grid, and anything beyond its frame (unloaded ground is unknown, and unknown
// is walkable — the fused map's optimistic posture). The MoveTo short-hop and
// committed-stride rules ask this before issuing a stride without a route.
func (g *Grid) OpenLine(a, b Pos) bool {
	return walkLine(a, b, func(p Pos) bool {
		return p == a || !g.In(p) || g.Walkable(p)
	})
}

// BestStep is the planner-free fallback for a hurried mover (flee, escape) when
// no route came back within its budget: the best short, clear straight step from
// me. Candidates are 16 bearings at 2/3/5/8 tiles whose end is open ground and
// whose line is clear (OpenLine) — so the step never runs blind into a wall.
// Score: room at the end (clearance, capped) gained over room here, plus ground
// won toward goal, plus a little length. ok=false: boxed in, nothing clear.
func (g *Grid) BestStep(me, goal Pos) (Pos, bool) {
	const capC = 8
	here := math.Min(float64(g.Clearance(me)), capC)
	d0 := fdist(me, goal)
	best, bestScore, found := Pos{}, math.Inf(-1), false
	for k := 0; k < 16; k++ {
		a := float64(k) * math.Pi / 8
		ca, sa := math.Cos(a), math.Sin(a)
		for _, r := range []float64{2, 3, 5, 8} {
			end := Pos{X: me.X + int(math.Round(r*ca)), Y: me.Y + int(math.Round(r*sa))}
			if end == me || !g.Walkable(end) || !g.OpenLine(me, end) {
				continue
			}
			clr := math.Min(float64(g.Clearance(end)), capC)
			score := 3*(clr-here) + 1.5*(d0-fdist(end, goal)) + 0.2*r
			if score > bestScore {
				best, bestScore, found = end, score, true
			}
		}
	}
	return best, found
}

// AlongPath walks a polyline from its start and returns the farthest point
// within maxArc tiles of arc length that ok accepts (sampled every tile, the
// vertices included). ok=false: no sampled point qualified. The leap landing
// rides the planned route this way instead of a straight line through walls.
func AlongPath(path []Pos, maxArc float64, ok func(Pos) bool) (Pos, bool) {
	if len(path) == 0 {
		return Pos{}, false
	}
	best, found := Pos{}, false
	arc := 0.0
	for i := 1; i < len(path) && arc < maxArc; i++ {
		a, b := path[i-1], path[i]
		l := fdist(a, b)
		if l < 0.01 {
			continue
		}
		for s := 1.0; ; s++ {
			t := s / l
			if t > 1 {
				t = 1
			}
			if arc+t*l > maxArc {
				break
			}
			p := Pos{X: a.X + int(math.Round(t*float64(b.X-a.X))), Y: a.Y + int(math.Round(t*float64(b.Y-a.Y)))}
			if ok(p) {
				best, found = p, true
			}
			if t >= 1 {
				break
			}
		}
		arc += l
	}
	return best, found
}
