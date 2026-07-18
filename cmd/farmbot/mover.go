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

// wedges are ACTUATION-TRUTH obstacles: spots where the game refused passage the grid
// claimed was walkable (river banks with bad collision, invisible fence pockets). Recorded
// by the stall watchdogs, fed into every plan and the direct-line check — the map prior
// lies, the game's refusal doesn't.
type Mover struct {
	wedges []data.Position
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

	// CLICK GAIT (2026-07-18): the game's OWN pathfinder, via a plain ground click.
	// The 1/4-arrivals refutation that exiled click-to-move predates the DPI aim fix —
	// retested on 3.2: two for two, exact arrivals, zero thrash. When the carrot is far,
	// clicking it lets D2R walk (smooth, truth-aware collision, pets yield); force-move
	// stays for the last tiles and for callers that need continuous steering.
	clickMove    func(sx, sy int)
	lastClickAt  time.Time
	lastClickTgt data.Position
}

// SetClickMove installs the click actuator; nil disables the click gait.
func (m *Mover) SetClickMove(f func(sx, sy int)) { m.clickMove = f }

// clickStep fires the click gait at tgt if it qualifies (far carrot, rate-limited,
// re-clicks early when the carrot moved). Reports whether the tick is handled.
func (m *Mover) clickStep(me, tgt data.Position) bool {
	if m.clickMove == nil || chebyshev(me, tgt) < 10 {
		return false
	}
	if time.Since(m.lastClickAt) > 1600*time.Millisecond || chebyshev(tgt, m.lastClickTgt) > 8 {
		sx, sy := m.toScreen(me, tgt.X-me.X, tgt.Y-me.Y)
		m.clickMove(sx, sy)
		m.lastClickAt, m.lastClickTgt = time.Now(), tgt
	}
	return true
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

// SetWedges replaces the refusal list (see wedges field doc).
func (m *Mover) SetWedges(w []data.Position) { m.wedges = w }

// lineHitsWedge reports whether the segment a->b passes within 2 of a recorded refusal.
func (m *Mover) lineHitsWedge(a, b data.Position) bool {
	steps := max(abs(b.X-a.X), abs(b.Y-a.Y))
	for _, w := range m.wedges {
		for i := 0; i <= steps; i++ {
			p := a
			if steps > 0 {
				p = data.Position{X: a.X + (b.X-a.X)*i/steps, Y: a.Y + (b.Y-a.Y)*i/steps}
			}
			if chebyshev(p, w) <= 2 {
				return true
			}
		}
	}
	return false
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

// losThin is losClear without the clearance requirement — plain cell walkability along the
// segment. Only safe for following an A*-planned path (its cells are walkable by construction).
func losThin(g *game.Grid, a, b data.Position) bool {
	if g == nil {
		return false
	}
	steps := max(abs(b.X-a.X), abs(b.Y-a.Y))
	for i := 0; i <= steps; i++ {
		if steps == 0 {
			break
		}
		p := data.Position{X: a.X + (b.X-a.X)*i/steps, Y: a.Y + (b.Y-a.Y)*i/steps}
		if !g.IsWalkable(p) {
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
	// (Unless the line crosses a recorded refusal — the grid lies there.)
	if losClear(grid, me, dest) && !m.lineHitsWedge(me, dest) {
		if m.clickStep(me, dest) {
			return MoveMoving
		}
		sx, sy := m.toScreen(me, dest.X-me.X, dest.Y-me.Y)
		m.walkToHold(sx, sy, 300)
		return MoveMoving
	}

	if !nv.havePlan {
		nv.SetObstacles(m.wedges) // refusals block planning even when the grid disagrees
	}
	if !nv.havePlan && !nv.BuildPlan(me, dest, time.Now()) {
		// This planner can't reach it — try the other before giving the caller a carrot shrug.
		alt := m.full
		if nv == m.full {
			alt = m.live
		}
		if alt != nil && !alt.havePlan {
			alt.SetObstacles(m.wedges)
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
	// WALL-HUG ESCAPE: alongside a wall the fat ray fails even for near points, degenerating
	// the carrot to the adjacent waypoint — 1-tile creep with a replan every step (measured at
	// the Cold Plains border fence: pos advancing ~1 tile/s with div=true churn). The A* path
	// itself is walkable, so when the fat carrot is that short, take the farthest THIN-ray
	// (walkability only) plan point instead, capped at 14 tiles so we never commit blind-far.
	if chebyshev(me, tgt) < 6 {
		for i := len(nv.pts) - 1; i >= 0; i-- {
			if chebyshev(me, nv.pts[i]) > 14 {
				continue
			}
			if losThin(grid, me, nv.pts[i]) {
				if chebyshev(me, nv.pts[i]) > chebyshev(me, tgt) {
					tgt = nv.pts[i]
				}
				break
			}
		}
	}
	if m.clickStep(me, tgt) {
		return MoveMoving
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
