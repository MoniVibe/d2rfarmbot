package journey

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/game"
)

// fgrid parses rows into a fused game grid: '.' walkable, '#' wall, '?' unknown,
// 'P' trusted-prior wall.
func fgrid(rows ...string) *game.Grid {
	cg := make([][]game.CollisionType, len(rows))
	for y, r := range rows {
		cg[y] = make([]game.CollisionType, len(r))
		for x, ch := range r {
			switch ch {
			case '.':
				cg[y][x] = game.CollisionTypeWalkable
			case '#':
				cg[y][x] = game.CollisionTypeNonWalkable
			case 'P':
				cg[y][x] = game.CollisionTypePriorBlocked
			default:
				cg[y][x] = game.CollisionTypeUnknown
			}
		}
	}
	return &game.Grid{Width: len(rows[0]), Height: len(rows), CollisionGrid: cg}
}

func TestNewWallOnRouteReplansImmediately(t *testing.T) {
	g1 := fgrid(
		"....????????????....",
		"....????????????....",
		"....????????????....",
	)
	j := New(nil, g1, data.Position{X: 19, Y: 1}, "test")
	me := data.Position{X: 1, Y: 1}
	if ok, why := j.plan(me, time.Now()); !ok {
		t.Fatal(why)
	}
	// Rooms streamed in: the unknown middle is a wall with a gap only at the top.
	g2 := fgrid(
		"............#.......",
		"....#########.......",
		"....#########.......",
	)
	g2.CollisionGrid[0][12] = game.CollisionTypeWalkable
	if _, hit := j.adopt(g2); !hit || !j.replan {
		t.Fatal("a wall streamed across the route must arm an immediate replan")
	}
	// The replan routes on the new truth — through the gap.
	if ok, why := j.plan(me, time.Now()); !ok {
		t.Fatal(why)
	}
	for _, p := range j.f.Path() {
		if !g2.IsWalkable(p) {
			t.Fatalf("replanned route enters a wall at %v", p)
		}
	}
	// A newer grid that changes nothing on the route: adopted silently.
	j.replan = false
	g3 := fgrid(
		"....................",
		"....#########.......",
		"....#########......#",
	)
	g3.CollisionGrid[0][12] = game.CollisionTypeWalkable
	if _, hit := j.adopt(g3); hit || j.replan || j.src != g3 {
		t.Fatalf("off-route change: hit=%t replan=%t adopted=%t", hit, j.replan, j.src == g3)
	}
	// Another area's grid (different frame) is not this journey's business.
	other := fgrid("....")
	if _, hit := j.adopt(other); hit || j.src == other {
		t.Fatal("a different frame must be ignored")
	}
}

func TestUnknownIsPricedAndTrustedPriorWallRelaxesOnNoPath(t *testing.T) {
	ng := navGrid(fgrid("..??P#"))
	if ng.CellCost(data.Position{X: 2, Y: 0}) == 0 || ng.CellCost(data.Position{X: 0, Y: 0}) != 0 {
		t.Fatal("unknown must cost, observed ground must not")
	}
	if ng.Walkable(data.Position{X: 4, Y: 0}) || ng.Walkable(data.Position{X: 5, Y: 0}) {
		t.Fatal("trusted prior wall and observed wall must block")
	}
	// The only way to the goal crosses a trusted-prior wall: plan through it as a
	// guess rather than give up. An observed wall never relaxes.
	g := fgrid(
		"...P...",
	)
	j := New(nil, g, data.Position{X: 6, Y: 0}, "test")
	if ok, why := j.plan(data.Position{X: 0, Y: 0}, time.Now()); !ok {
		t.Fatalf("relaxed plan expected: %s", why)
	}
	hard := New(nil, fgrid("...#..."), data.Position{X: 6, Y: 0}, "test")
	if ok, _ := hard.plan(data.Position{X: 0, Y: 0}, time.Now()); ok {
		t.Fatal("an observed wall must stay a wall")
	}
}
