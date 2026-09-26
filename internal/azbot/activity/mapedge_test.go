package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/game"
)

func testGrid(x, y, w, h int, walk func(x, y int) bool) *game.Grid {
	cg := make([][]game.CollisionType, h)
	for j := range cg {
		cg[j] = make([]game.CollisionType, w)
		for i := range cg[j] {
			if walk(x+i, y+j) {
				cg[j][i] = game.CollisionTypeWalkable
			} else {
				cg[j][i] = game.CollisionTypeNonWalkable
			}
		}
	}
	return game.NewGrid(cg, x, y)
}

func TestMapEdgeExitFindsTheGap(t *testing.T) {
	// Blood Moor (5600,4600 280x480) over Cold Plains (5400,5080 400x400):
	// they touch along y=5080 for x 5600..5800; the gap is x 5700..5709.
	gap := func(x, y int) bool { return x >= 5700 && x < 5710 }
	moor := testGrid(5600, 4600, 280, 480, func(x, y int) bool { return y < 5070 || gap(x, y) })
	plains := testGrid(5400, 5080, 400, 400, func(x, y int) bool { return y > 5090 || gap(x, y) })
	p, ok := mapEdgeExit(moor, plains, data.Position{X: 5650, Y: 4900})
	if !ok || p.X < 5700 || p.X > 5709 || p.Y != 5079-3 {
		t.Fatalf("got %v %v, want the gap's center just inside the moor", p, ok)
	}
	far := testGrid(9000, 9000, 50, 50, func(int, int) bool { return true })
	if _, ok := mapEdgeExit(moor, far, data.Position{}); ok {
		t.Fatal("frames that do not touch share no border")
	}
}
