package journey

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
)

// R55: the cell ahead of a stuck follow is learned, costs the planner, and ages out.
func TestLearnedWalls(t *testing.T) {
	me := data.Position{X: 10007, Y: 8746}
	path := []data.Position{{X: 10007, Y: 8746}, {X: 10004, Y: 8753}}
	at, ok := blockedAhead(me, path)
	if !ok || at != (data.Position{X: 10005, Y: 8748}) {
		t.Fatalf("blocked ahead = %v %v", at, ok)
	}
	if _, ok := blockedAhead(me, []data.Position{{X: 10008, Y: 8747}}); ok {
		t.Fatal("a route point under his feet is not a wall ahead")
	}
	t0 := time.Now()
	ar := area.PalaceCellarLevel3
	LearnWall(ar, at, t0)
	LearnWall(ar, at, t0) // once
	if obs := wallObstacles(ar, t0.Add(time.Minute)); len(obs) != 1 || obs[0].Penalty != wallPenalty {
		t.Fatalf("one learned wall, got %v", obs)
	}
	if obs := wallObstacles(area.HaremLevel1, t0); len(obs) != 0 {
		t.Fatal("walls belong to their area")
	}
	if obs := wallObstacles(ar, t0.Add(wallMemory+time.Second)); len(obs) != 0 {
		t.Fatal("a learned wall ages out")
	}
}
