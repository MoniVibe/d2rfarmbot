// Package mapfuse fuses azbot's three map sources into ONE planning grid. Pure: no
// game, memory or Windows imports — every rule here is proven by unit tests on Linux.
//
// The sources, strongest first:
//
//  1. LIVE   — collision of the rooms D2R has streamed in right now (mod-accurate truth,
//     but only ~2 rooms around the player).
//  2. ATLAS  — live collision recorded on earlier passes of this seed (the cartographer).
//  3. PRIOR  — koolo-map's vanilla-generated full-level grid. Complete, but WRONG where
//     the mod altered the area, so it is used only once it has EARNED trust: its
//     agreement with what live+atlas actually observed (see Measure).
//  4. UNKNOWN — optimistic: plannable at a per-step penalty so far goals still route
//     through unloaded space; walls materialize as rooms stream in and the plan
//     re-routes. When the prior is untrusted, unknown cells it calls wall cost more
//     (a hint, never a hard block).
package mapfuse

import "github.com/hectorgimenez/d2go/pkg/data"

// Pos is a world subtile.
type Pos = data.Position

// Cell is one source's opinion of a cell.
type Cell uint8

const (
	Unknown Cell = iota
	Walk
	Block
)

// Layer is one source: a row-major opinion grid anchored at a world offset.
// A nil *Layer (or out-of-range cell) is Unknown everywhere.
type Layer struct {
	OffX, OffY int
	W, H       int
	Cells      []Cell
}

// NewLayer allocates an all-Unknown layer.
func NewLayer(offX, offY, w, h int) *Layer {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return &Layer{OffX: offX, OffY: offY, W: w, H: h, Cells: make([]Cell, w*h)}
}

// At is the cell at world (x,y); Unknown outside the layer.
func (l *Layer) At(x, y int) Cell {
	if l == nil {
		return Unknown
	}
	lx, ly := x-l.OffX, y-l.OffY
	if lx < 0 || ly < 0 || lx >= l.W || ly >= l.H {
		return Unknown
	}
	return l.Cells[ly*l.W+lx]
}

// Set writes world (x,y); ignored outside the layer.
func (l *Layer) Set(x, y int, c Cell) {
	if l == nil {
		return
	}
	lx, ly := x-l.OffX, y-l.OffY
	if lx < 0 || ly < 0 || lx >= l.W || ly >= l.H {
		return
	}
	l.Cells[ly*l.W+lx] = c
}

// Known counts cells that are not Unknown.
func (l *Layer) Known() int {
	if l == nil {
		return 0
	}
	n := 0
	for _, c := range l.Cells {
		if c != Unknown {
			n++
		}
	}
	return n
}

// Class is the fused verdict for a cell, with its provenance.
type Class uint8

const (
	ClassUnknown          Class = iota // optimistic: walkable at CostUnknown
	ClassUnknownPriorWall              // untrusted prior says wall: walkable at CostUnknown+CostPriorWall
	ClassLiveWalk
	ClassLiveBlock
	ClassAtlasWalk
	ClassAtlasBlock
	ClassPriorWalk  // trusted prior: walkable at CostPriorWalk
	ClassPriorBlock // trusted prior: blocked
)

// Per-step surcharges (a straight step costs 10 in nav). Unknown space is not free:
// a known corridor beats an equal-length guess through unloaded rooms.
const (
	CostPriorWalk = 2
	CostUnknown   = 5
	CostPriorWall = 15
)

// Walkable reports whether the planner may enter a cell of this class.
func (c Class) Walkable() bool {
	switch c {
	case ClassLiveBlock, ClassAtlasBlock, ClassPriorBlock:
		return false
	}
	return true
}

// StepCost is the extra per-step cost of entering a cell of this class.
func (c Class) StepCost() int32 {
	switch c {
	case ClassUnknown:
		return CostUnknown
	case ClassUnknownPriorWall:
		return CostUnknown + CostPriorWall
	case ClassPriorWalk:
		return CostPriorWalk
	}
	return 0
}

// Known reports whether the class comes from observation (live or atlas).
func (c Class) Known() bool {
	switch c {
	case ClassLiveWalk, ClassLiveBlock, ClassAtlasWalk, ClassAtlasBlock:
		return true
	}
	return false
}

func (c Class) String() string {
	switch c {
	case ClassUnknown:
		return "unknown"
	case ClassUnknownPriorWall:
		return "unknown/prior-wall"
	case ClassLiveWalk:
		return "live-walk"
	case ClassLiveBlock:
		return "live-block"
	case ClassAtlasWalk:
		return "atlas-walk"
	case ClassAtlasBlock:
		return "atlas-block"
	case ClassPriorWalk:
		return "prior-walk"
	case ClassPriorBlock:
		return "prior-block"
	}
	return "?"
}

// Fused is the planning grid: the live level frame, one Class per cell.
type Fused struct {
	OffX, OffY int
	W, H       int
	Class      []Class
	// Disagree marks observed cells where the (shifted) prior says the opposite —
	// the debug picture's evidence for why the prior is or isn't trusted.
	Disagree []bool
	Trust    Trust
	Counts   [8]int // cells per Class
}

// ClassAt is the fused class at world (x,y); ClassUnknown outside the frame.
func (f *Fused) ClassAt(x, y int) Class {
	lx, ly := x-f.OffX, y-f.OffY
	if f == nil || lx < 0 || ly < 0 || lx >= f.W || ly >= f.H {
		return ClassUnknown
	}
	return f.Class[ly*f.W+lx]
}

// Fuse builds the planning grid over live's frame. Precedence per cell:
// live known > atlas known > prior (only when tr.Trusted, read through tr.Shift) >
// optimistic unknown (with a prior-wall surcharge when an untrusted prior says wall).
// live must be non-nil (it defines the frame); atlas and prior may be nil.
func Fuse(live, atlas, prior *Layer, tr Trust) *Fused {
	f := &Fused{OffX: live.OffX, OffY: live.OffY, W: live.W, H: live.H,
		Class: make([]Class, live.W*live.H), Disagree: make([]bool, live.W*live.H), Trust: tr}
	for ly := 0; ly < live.H; ly++ {
		wy := ly + live.OffY
		for lx := 0; lx < live.W; lx++ {
			wx := lx + live.OffX
			i := ly*live.W + lx
			var c Class
			obs := live.Cells[i]
			liveSrc := obs != Unknown
			if !liveSrc {
				obs = atlas.At(wx, wy)
			}
			pr := Unknown
			if prior != nil {
				pr = prior.At(wx-tr.Shift.X, wy-tr.Shift.Y)
			}
			switch {
			case obs == Walk && liveSrc:
				c = ClassLiveWalk
			case obs == Block && liveSrc:
				c = ClassLiveBlock
			case obs == Walk:
				c = ClassAtlasWalk
			case obs == Block:
				c = ClassAtlasBlock
			case tr.Trusted && pr == Walk:
				c = ClassPriorWalk
			case tr.Trusted && pr == Block:
				c = ClassPriorBlock
			case pr == Block:
				c = ClassUnknownPriorWall
			default:
				c = ClassUnknown
			}
			if obs != Unknown && pr != Unknown && obs != pr {
				f.Disagree[i] = true
			}
			f.Class[i] = c
			f.Counts[c]++
		}
	}
	return f
}

// Relaxed is the fallback reading when a trusted prior walls every route: prior
// walls become strongly-penalized unknown instead of hard blocks. Observed cells
// are untouched.
func (c Class) Relaxed() Class {
	if c == ClassPriorBlock {
		return ClassUnknownPriorWall
	}
	return c
}
