package game

import (
	"os"
	"strings"
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
)

func layerOf(offX, offY int, rows ...string) *LiveLayer {
	l := &LiveLayer{OffX: offX, OffY: offY, W: len(rows[0]), H: len(rows)}
	l.Cells = make([]uint8, l.W*l.H)
	for y, r := range rows {
		for x, ch := range r {
			switch ch {
			case '.':
				l.Cells[y*l.W+x] = LiveWalk
			case '#':
				l.Cells[y*l.W+x] = LiveBlock
			}
		}
	}
	return l
}

func TestAtlasMergeLiveLayerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := NewAtlas(dir)
	// First visit: the west rooms streamed in.
	n := a.MergeLiveLayer(9, 4, layerOf(1000, 500,
		"..#?????",
		".##?????",
		"???????."))
	if n != 7 || a.KnownCells(9, 4) != 7 {
		t.Fatalf("merged %d, known %d, want 7/7 (unknown cells never recorded)", n, a.KnownCells(9, 4))
	}
	// Later: the east streams in and the west unloads — the west is remembered.
	a.MergeLiveLayer(9, 4, layerOf(1000, 500,
		"?????...",
		"?????.#.",
		"????????"))
	if got := a.KnownCells(9, 4); got != 13 {
		t.Fatalf("known = %d, want 13", got)
	}
	// A refusal is sticky against a live "walkable".
	a.MarkRefused(9, 4, data.Position{X: 1006, Y: 500}, 0, data.Position{})
	a.MergeLiveLayer(9, 4, layerOf(1006, 500, "."))

	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("atomic save left a temp file: %s", e.Name())
		}
	}
	// A fresh process (new Atlas) reads it back.
	b := NewAtlas(dir)
	ox, oy, w, h, cells := b.Overlay(9, 4).Cells()
	if cells == nil {
		t.Fatal("nothing persisted")
	}
	at := func(x, y int) uint8 {
		lx, ly := x-ox, y-oy
		if lx < 0 || ly < 0 || lx >= w || ly >= h {
			return LiveUnknown
		}
		return cells[ly*w+lx]
	}
	for _, c := range []struct {
		x, y int
		want uint8
	}{
		{1000, 500, LiveWalk}, {1002, 500, LiveBlock}, {1001, 501, LiveBlock},
		{1007, 502, LiveWalk}, {1006, 501, LiveBlock}, {1003, 500, LiveUnknown},
		{1006, 500, LiveBlock}, // refused reads as blocked
	} {
		if got := at(c.x, c.y); got != c.want {
			t.Errorf("(%d,%d) = %d, want %d", c.x, c.y, got, c.want)
		}
	}
	// A second save over the existing file (rename-over) still round-trips.
	b.MergeLiveLayer(9, 4, layerOf(1003, 500, "#"))
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	if got := NewAtlas(dir).KnownCells(9, 4); got != 14 {
		t.Fatalf("after re-save known = %d, want 14", got)
	}
}

func TestLiveLayerGridKeepsLegacyView(t *testing.T) {
	g := layerOf(10, 20, ".#?").Grid()
	if g.OffsetX != 10 || g.OffsetY != 20 || g.Width != 3 {
		t.Fatalf("frame %+v", g)
	}
	if g.CollisionGrid[0][1] != CollisionTypeNonWalkable || g.CollisionGrid[0][2] != CollisionTypeLowPriority {
		t.Fatalf("legacy view: %v", g.CollisionGrid[0])
	}
	if !CollisionTypeUnknown.Walkable() || CollisionTypePriorBlocked.Walkable() || CollisionTypeNonWalkable.Walkable() {
		t.Fatal("fused types: unknown walkable, walls not")
	}
}
