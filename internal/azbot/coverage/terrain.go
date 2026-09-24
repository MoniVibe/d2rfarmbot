// Package coverage is azbot's exploration memory: per (map seed, area) a SEEN
// bitmap over the area's walkable ground, marked by line of sight from where she
// actually stood, a frontier (seen ground touching unseen ground) clustered into
// goals, and the policy that picks the next goal by PATH cost — not by room
// rectangles she happened to stand in. The owner: "In the cave it goes to
// already-explored bits; it should try unexplored areas."
//
// Pure: no game, motor or Windows imports. The executive adapts the live
// collision grid + the level's room graph into a Terrain; everything here is
// proven by unit tests on Linux.
package coverage

import "github.com/hectorgimenez/d2go/pkg/data"

// Pos is a world tile (aliased so game positions pass straight through).
type Pos = data.Position

// Cell is one tile of terrain as the executive knows it right now.
type Cell uint8

const (
	// Void: outside every room of the level — never ground, never frontier.
	Void Cell = iota
	// Wall: loaded collision says blocked (truth).
	Wall
	// Floor: loaded collision says walkable (truth).
	Floor
	// Unknown: inside a level room that has not streamed in. Plannable (the
	// live planner is optimistic too) but OPAQUE: nobody has seen it yet.
	Unknown
)

// Terrain is a snapshot of the level frame's cells, anchored at a world offset
// like nav.Grid. Rebuilt by the executive whenever the live grid regrows.
type Terrain struct {
	OffX, OffY, W, H int
	C                []Cell // row-major W*H
}

// NewTerrain builds a terrain; f is asked in RELATIVE (x,y).
func NewTerrain(offX, offY, w, h int, f func(x, y int) Cell) *Terrain {
	t := &Terrain{OffX: offX, OffY: offY, W: w, H: h, C: make([]Cell, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t.C[y*w+x] = f(x, y)
		}
	}
	return t
}

// ParseTerrain builds a terrain from rows: '.' floor, '#' wall, '?' unknown,
// anything else void. Test/debug helper.
func ParseTerrain(offX, offY int, rows []string) *Terrain {
	w := 0
	for _, r := range rows {
		if len(r) > w {
			w = len(r)
		}
	}
	return NewTerrain(offX, offY, w, len(rows), func(x, y int) Cell {
		if x >= len(rows[y]) {
			return Void
		}
		switch rows[y][x] {
		case '.':
			return Floor
		case '#':
			return Wall
		case '?':
			return Unknown
		}
		return Void
	})
}

func (t *Terrain) idx(p Pos) (int, bool) {
	x, y := p.X-t.OffX, p.Y-t.OffY
	if x < 0 || y < 0 || x >= t.W || y >= t.H {
		return 0, false
	}
	return y*t.W + x, true
}

// At returns the cell at world p (Void outside the frame).
func (t *Terrain) At(p Pos) Cell {
	i, ok := t.idx(p)
	if !ok {
		return Void
	}
	return t.C[i]
}

func (t *Terrain) pos(i int) Pos { return Pos{X: i%t.W + t.OffX, Y: i/t.W + t.OffY} }

// Rect is a world-tile rectangle [X, X+W) × [Y, Y+H).
type Rect struct{ X, Y, W, H int }
