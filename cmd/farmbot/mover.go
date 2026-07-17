package main

// The MOVER: single owner of locomotion, built on the movelab's measured winner — force-move
// carrots aimed at the FARTHEST plan point with clear line of sight (16.5s / 0.98 path
// efficiency vs 19.3s / 0.88 for near-waypoint carrots; click-move refuted at 1/4 arrivals).
//
// Design rules, each earned by a measured failure earlier in this project:
//   - Closed loop: every Step() compares realized movement against the command; silence is
//     never assumed to be progress.
//   - One authority: behaviors declare destinations; the mover owns HOW. It consults the live
//     planner first and the full-area map planner when the live grid can't reach (bridges,
//     unstreamed rooms), so callers never juggle two navigators again.
//   - Blocked means change STRATEGY, not retry harder: hostiles in reach are fought through by
//     the caller (the mover reports Blocked with the blocker), walls get tangent/clearance
//     escapes, and only then the burst.

import (
	"math"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/game"
)

type MoveStatus int

const (
	MoveMoving MoveStatus = iota
	MoveArrived
	MoveBlocked // no net progress and no plan improves it — caller decides (fight/abandon)
)

type Mover struct {
	gr   *game.MemoryReader
	live *Navigator // partial but mod-accurate
	full *Navigator // whole-area map prior (nil when unavailable/untrusted)

	dest       data.Position
	bestDist   int
	bestAt     time.Time
	lastPos    data.Position
	lastMoveAt time.Time
	preferFull bool

	// injected actuators (main.go closures own the input machinery)
	walkToHold func(sx, sy, holdMs int)
	toScreen   func(me data.Position, dx, dy int) (int, int)
	openBurst  func(me data.Position)
}

func NewMover(gr *game.MemoryReader, live, full *Navigator,
	walk func(sx, sy, hold int), toScreen func(me data.Position, dx, dy int) (int, int),
	burst func(me data.Position)) *Mover {
	return &Mover{
		gr: gr, live: live, full: full,
		walkToHold: walk, toScreen: toScreen, openBurst: burst,
		bestDist: 1 << 30, bestAt: time.Now(), lastMoveAt: time.Now(),
	}
}

// Rebind swaps the planners after an area re-alignment without losing actuators.
func (m *Mover) Rebind(live, full *Navigator) {
	m.live, m.full = live, full
	m.dest = data.Position{}
}

// losClear reports whether the straight segment a->b crosses only walkable cells of g.
func losClear(g *game.Grid, a, b data.Position) bool {
	if g == nil {
		return false
	}
	steps := max(abs(b.X-a.X), abs(b.Y-a.Y))
	if steps == 0 {
		return true
	}
	for i := 0; i <= steps; i++ {
		p := data.Position{X: a.X + (b.X-a.X)*i/steps, Y: a.Y + (b.Y-a.Y)*i/steps}
		// FAT ray: require a sliver of clearance around the line, or the executor threads
		// 1-cell gaps into wall creases the force-move can't actually follow (wall-hugging).
		if !g.IsWalkable(p) ||
			(!g.IsWalkable(data.Position{X: p.X + 1, Y: p.Y}) && !g.IsWalkable(data.Position{X: p.X - 1, Y: p.Y})) ||
			(!g.IsWalkable(data.Position{X: p.X, Y: p.Y + 1}) && !g.IsWalkable(data.Position{X: p.X, Y: p.Y - 1})) {
			return false
		}
	}
	return true
}

// Step advances toward dest by one tick. Callers treat MoveBlocked as "pick a new strategy":
// the mover has already tried planner fallback and clearance escape before saying it.
func (m *Mover) Step(me data.Position, dest data.Position) MoveStatus {
	if chebyshev(me, dest) <= 4 {
		return MoveArrived
	}
	if chebyshev(dest, m.dest) > 6 { // tolerance: a drifting target (chased monster) keeps state
		m.dest = dest
		m.bestDist, m.bestAt = 1<<30, time.Now()
		m.preferFull = false
		if m.live != nil {
			m.live.havePlan = false
		}
		if m.full != nil {
			m.full.havePlan = false
		}
	}
	if d := chebyshev(me, dest); d < m.bestDist-1 {
		m.bestDist, m.bestAt = d, time.Now()
	}
	if chebyshev(me, m.lastPos) > 2 {
		m.lastPos, m.lastMoveAt = me, time.Now()
	}

	// Escalation ladder. Physical stall (5s no movement) or approach stall (12s no closest-
	// approach improvement) -> planner flip once, clearance burst after; still nothing -> the
	// caller hears Blocked and brings different tools (usually violence).
	stalled := time.Since(m.lastMoveAt) > 5*time.Second
	orbiting := time.Since(m.bestAt) > 12*time.Second
	if stalled || orbiting {
		if !m.preferFull && m.full != nil {
			m.preferFull = true
			m.full.havePlan = false
			m.bestAt = time.Now()
			m.lastMoveAt = time.Now()
		} else {
			m.openBurst(me)
			m.bestAt = time.Now()
			m.lastMoveAt = time.Now()
			if time.Since(m.bestAt) > 30*time.Second { // burst didn't help across repeats
				return MoveBlocked
			}
		}
	}

	nv := m.live
	grid := (*game.Grid)(nil)
	if nv != nil {
		grid = nv.grid
	}
	if m.preferFull && m.full != nil {
		nv = m.full
		grid = nv.grid
	}
	if nv == nil {
		sx, sy := m.toScreen(me, dest.X-me.X, dest.Y-me.Y)
		m.walkToHold(sx, sy, 250)
		return MoveMoving
	}

	// Direct line? Skip planning entirely — the measured 0.98-efficiency straight walk.
	if losClear(grid, me, dest) {
		sx, sy := m.toScreen(me, dest.X-me.X, dest.Y-me.Y)
		m.walkToHold(sx, sy, 300)
		return MoveMoving
	}

	if !nv.havePlan && !nv.BuildPlan(me, dest, time.Now()) {
		// This planner can't reach it — try the other before giving the caller a carrot shrug.
		alt := m.full
		if nv == m.full {
			alt = m.live
		}
		if alt != nil && (alt.havePlan || alt.BuildPlan(me, dest, time.Now())) {
			nv = alt
			grid = nv.grid
		} else {
			sx, sy := m.toScreen(me, dest.X-me.X, dest.Y-me.Y)
			m.walkToHold(sx, sy, 250)
			return MoveMoving
		}
	}
	step := nv.Step(me, time.Now())
	if step.Arrived || step.Diverged {
		nv.havePlan = false
		return MoveMoving
	}
	// The movelab winner: aim at the FARTHEST planned point we can see, not the next waypoint.
	tgt := step.Target
	for i := len(nv.pts) - 1; i >= 0; i-- {
		if losClear(grid, me, nv.pts[i]) {
			tgt = nv.pts[i]
			break
		}
	}
	hold := step.HoldMs
	if hold <= 0 {
		hold = 250
	}
	if chebyshev(me, tgt) > 20 {
		hold = 300 // long visible runway — commit longer, re-decide less
	}
	sx, sy := m.toScreen(me, tgt.X-me.X, tgt.Y-me.Y)
	m.walkToHold(sx, sy, hold)
	return MoveMoving
}

// screenAngleCarrot converts a world direction into a mid-radius screen carrot — shared by
// escape behaviors that steer by direction rather than destination.
func screenAngleCarrot(cx, cy int, angle float64) (int, int) {
	return cx + int(300*math.Cos(angle)), cy + int(140*math.Sin(angle))
}
