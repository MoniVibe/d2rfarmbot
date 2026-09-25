package journey

import (
	"fmt"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/nav"
)

// ---------------------------------------------------------------- learned walls
//
// OWNER (2026-09-25, R55 Palace Cellar 3): "it tries going through walls, it
// should know pathfinding through doors and archways — harem and cellar are
// particularly nasty". The fused grid called a wall open there (a 1-leg,
// 11-tile route straight into it; 18 strides at gain 0). The static grid
// cannot be trusted everywhere, so the mover LEARNS: where a follow goes
// stuck, the cells just ahead become expensive for every journey in that
// area for wallMemory, and the route replans around them — through the
// door or archway the grid does know. Soft, not solid: a pack that body-
// blocked a corridor must not seal it forever.

const (
	wallMemory  = 10 * time.Minute
	wallRadius  = 1
	wallPenalty = 400 // per cell: dearer than any detour the planner would take
	wallCap     = 240 // per area; oldest dropped first
)

type learnedWall struct {
	at   data.Position
	seen time.Time
}

var walls = struct {
	sync.Mutex
	m map[area.ID][]learnedWall
}{m: map[area.ID][]learnedWall{}}

// LearnWall records a blocked cell in an area.
func LearnWall(ar area.ID, at data.Position, now time.Time) {
	walls.Lock()
	defer walls.Unlock()
	ws := walls.m[ar]
	for i := range ws {
		if ws[i].at == at {
			ws[i].seen = now
			return
		}
	}
	ws = append(ws, learnedWall{at: at, seen: now})
	if len(ws) > wallCap {
		ws = ws[len(ws)-wallCap:]
	}
	walls.m[ar] = ws
}

// wallObstacles: the area's live learned walls as planner obstacles.
func wallObstacles(ar area.ID, now time.Time) []nav.Obstacle {
	walls.Lock()
	defer walls.Unlock()
	var out []nav.Obstacle
	kept := walls.m[ar][:0]
	for _, w := range walls.m[ar] {
		if now.Sub(w.seen) > wallMemory {
			continue
		}
		kept = append(kept, w)
		out = append(out, nav.Obstacle{At: w.at, Radius: wallRadius, Penalty: wallPenalty})
	}
	walls.m[ar] = kept
	return out
}

// blockedAhead: the cell two steps from me toward the first route point that
// is not under my feet — what stopped the follow. ok=false: no route ahead.
func blockedAhead(me data.Position, path []data.Position) (data.Position, bool) {
	for _, p := range path {
		dx, dy := p.X-me.X, p.Y-me.Y
		if abs(dx) < 2 && abs(dy) < 2 {
			continue
		}
		return data.Position{X: me.X + 2*sign(dx), Y: me.Y + 2*sign(dy)}, true
	}
	return data.Position{}, false
}

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (j *Journey) area() (area.ID, data.Position, bool) {
	if j.gr == nil {
		return 0, data.Position{}, false
	}
	d := j.gr.GetData()
	return d.PlayerUnit.Area, d.PlayerUnit.Position, true
}

// learnStuck: the follow went stuck — the cell ahead is a wall the grid missed.
func (j *Journey) learnStuck(now time.Time) {
	ar, me, ok := j.area()
	if !ok {
		return
	}
	if at, ok := blockedAhead(me, j.path); ok {
		LearnWall(ar, at, now)
		j.note(fmt.Sprintf("learned wall at (%d,%d) in area %d — the route plans around it", at.X, at.Y, int(ar)))
		j.replan = true
	}
}

// learnedWalls: this area's learned walls, for the planner.
func (j *Journey) learnedWalls(now time.Time) []nav.Obstacle {
	ar, _, ok := j.area()
	if !ok {
		return nil
	}
	return wallObstacles(ar, now)
}
