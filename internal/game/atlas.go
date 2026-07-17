package game

// Atlas is the cartographer: it permanently accumulates live room collision (the mod-accurate
// truth read from D2R memory) as the bot walks, so over time we own a mod-accurate map of
// everywhere visited. The live grid only knows ~2 rooms around the player at any instant —
// everything outside loaded rooms reads as blocked because unknown-is-blocked is the safe
// default — so merging must be masked to loaded room rectangles or we would permanently
// record "blocked" for terrain we simply haven't seen yet.
//
// Storage choice: each (seed, area) overlay is a dense byte-per-cell array over the growing
// bounding box of observed rooms. Areas cap at ~500x500 subtiles so the worst case is ~250KB
// in memory — cheap — while staying O(1) per cell lookup, which matters because OverlayOnto
// runs on every pathfinding grid rebuild. On disk cells pack to 2 bits (unknown/walkable/
// blocked) since three states fit and atlas files accumulate for every seed ever played.
//
// Not thread-safe by design: the single game-loop goroutine is the only caller.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hectorgimenez/d2go/pkg/data"
)

type atlasCellState uint8

const (
	atlasUnknown atlasCellState = iota
	atlasWalkable
	atlasBlocked
)

const (
	atlasMagic   uint32 = 0x4C54414B // "KATL"
	atlasVersion uint16 = 1
	// Level frames are rejected above 4000 subtiles in BuildLiveGrid; anything larger here
	// means corrupted room data and must not drive an allocation.
	atlasMaxDim = 4096
)

type atlasKey struct {
	seed uint
	area int
}

// AtlasOverlay is the accumulated knowledge for one (seed, area). Origin/Width/Height frame
// the cells in world subtiles, same addressing as Grid.OffsetX/OffsetY, so translation between
// the two is pure offset arithmetic.
type AtlasOverlay struct {
	OriginX, OriginY int
	Width, Height    int
	cells            []atlasCellState // row-major Width*Height; nil until first merge
	known            int              // cells != atlasUnknown, kept incrementally for cheap KnownCells
	dirty            bool
}

type Atlas struct {
	dir      string
	overlays map[atlasKey]*AtlasOverlay
}

// NewAtlas creates an atlas rooted at dir. Nothing is read up front: overlays load lazily on
// first touch of each (seed, area) so startup cost doesn't scale with cache size.
func NewAtlas(dir string) *Atlas {
	return &Atlas{
		dir:      dir,
		overlays: make(map[atlasKey]*AtlasOverlay),
	}
}

func (a *Atlas) filePath(seed uint, area int) string {
	return filepath.Join(a.dir, fmt.Sprintf("atlas_%d_%d.bin", seed, area))
}

func (a *Atlas) overlay(seed uint, area int) *AtlasOverlay {
	k := atlasKey{seed: seed, area: area}
	if ov, ok := a.overlays[k]; ok {
		return ov
	}
	ov := loadOverlayFile(a.filePath(seed, area), seed, area)
	a.overlays[k] = ov
	return ov
}

// FrontierNear returns the known-walkable cell nearest to `from` that touches UNKNOWN space —
// the natural "go somewhere new" target for exploration: walking to a frontier loads the rooms
// beyond it, which grows the atlas, which moves the frontier. Returns false when the overlay is
// empty or fully enclosed (no unknown-adjacent walkables = the area is completely mapped).
func (a *Atlas) FrontierNear(seed uint, area int, from data.Position) (data.Position, bool) {
	if a == nil {
		return data.Position{}, false
	}
	ov := a.overlay(seed, area)
	if ov == nil || ov.cells == nil {
		return data.Position{}, false
	}
	best := 1 << 30
	var bp data.Position
	for y := 0; y < ov.Height; y++ {
		for x := 0; x < ov.Width; x++ {
			if ov.cells[y*ov.Width+x] != atlasWalkable {
				continue
			}
			frontier := false
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || ny < 0 || nx >= ov.Width || ny >= ov.Height {
					frontier = true // overlay edge — the world continues beyond what we know
					break
				}
				if ov.cells[ny*ov.Width+nx] == atlasUnknown {
					frontier = true
					break
				}
			}
			if !frontier {
				continue
			}
			w := data.Position{X: x + ov.OriginX, Y: y + ov.OriginY}
			dd := max(absInt(w.X-from.X), absInt(w.Y-from.Y))
			// Prefer frontiers worth traveling to — very near ones are usually the wall we're
			// standing next to; weight distance but skip the trivial ring.
			if dd < 15 {
				continue
			}
			if dd < best {
				best, bp = dd, w
			}
		}
	}
	return bp, best < 1<<30
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// MergeLiveGrid stamps the live grid's collision into the (seed, area) overlay, restricted to
// cells inside a loaded room's world-subtile rectangle. Rooms without a streamed-in Room1 are
// skipped even if passed: their cells in the live grid are the unknown-is-blocked default, not
// observations. Last-write-wins so terrain that changes between visits converges to the latest
// truth. Room rects are TILE units (see roomgraph.go), hence the ×5.
func (a *Atlas) MergeLiveGrid(seed uint, area int, live *Grid, rooms []LiveRoom) {
	if a == nil || live == nil || len(live.CollisionGrid) == 0 || len(rooms) == 0 {
		return
	}
	ov := a.overlay(seed, area)
	for _, room := range rooms {
		if room.Room1Ptr == 0 {
			continue
		}
		rx0, ry0 := room.Rect.X*5, room.Rect.Y*5
		rx1, ry1 := rx0+room.Rect.W*5, ry0+room.Rect.H*5
		// Clip to the live frame: a room rect can extend past the grid the level frame produced.
		x0, y0 := rgMax(rx0, live.OffsetX), rgMax(ry0, live.OffsetY)
		x1, y1 := rgMin(rx1, live.OffsetX+live.Width), rgMin(ry1, live.OffsetY+live.Height)
		if x0 >= x1 || y0 >= y1 {
			continue
		}
		if !ov.ensure(x0, y0, x1, y1) {
			continue
		}
		for wy := y0; wy < y1; wy++ {
			row := live.CollisionGrid[wy-live.OffsetY]
			for wx := x0; wx < x1; wx++ {
				// Inside a loaded room every cell is a real observation. NonWalkable = blocked;
				// anything else (Walkable, and LowPriority which NewGrid derives from Walkable
				// near walls) is walkable terrain.
				s := atlasBlocked
				if row[wx-live.OffsetX] != CollisionTypeNonWalkable {
					s = atlasWalkable
				}
				ov.set(wx, wy, s)
			}
		}
		ov.dirty = true
	}
}

// Overlay returns the accumulated overlay for (seed, area), loading it from disk on first
// touch. Never nil for a non-nil Atlas, so callers can chain .OverlayOnto unconditionally.
func (a *Atlas) Overlay(seed uint, area int) *AtlasOverlay {
	if a == nil {
		return nil
	}
	return a.overlay(seed, area)
}

// KnownCells reports how many cells the atlas has observed for (seed, area) — the coverage
// metric worth logging because it only ever grows.
func (a *Atlas) KnownCells(seed uint, area int) int {
	if a == nil {
		return 0
	}
	return a.overlay(seed, area).known
}

// Save writes every overlay modified since load. Failures return the first error but a partial
// save is harmless: files are self-describing and simply reflect an older snapshot.
func (a *Atlas) Save() error {
	if a == nil {
		return nil
	}
	var firstErr error
	for k, ov := range a.overlays {
		if !ov.dirty || len(ov.cells) == 0 {
			continue
		}
		if err := a.saveOverlay(k.seed, k.area, ov); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ov.dirty = false
	}
	return firstErr
}

// ensure grows the overlay so [x0,x1)x[y0,y1) fits, preserving existing cells. Growth is a
// bounding-box union: rooms stream in anywhere in the area, so early observations at one edge
// must survive later observations at the opposite edge. Returns false (merge skipped) when the
// union would exceed the sanity cap — that only happens with corrupted room rects.
func (ov *AtlasOverlay) ensure(x0, y0, x1, y1 int) bool {
	if len(ov.cells) == 0 {
		w, h := x1-x0, y1-y0
		if w > atlasMaxDim || h > atlasMaxDim {
			return false
		}
		ov.OriginX, ov.OriginY, ov.Width, ov.Height = x0, y0, w, h
		ov.cells = make([]atlasCellState, w*h)
		return true
	}
	nx0, ny0 := rgMin(ov.OriginX, x0), rgMin(ov.OriginY, y0)
	nx1 := rgMax(ov.OriginX+ov.Width, x1)
	ny1 := rgMax(ov.OriginY+ov.Height, y1)
	nw, nh := nx1-nx0, ny1-ny0
	if nw == ov.Width && nh == ov.Height {
		return true
	}
	if nw > atlasMaxDim || nh > atlasMaxDim {
		return false
	}
	cells := make([]atlasCellState, nw*nh)
	for y := 0; y < ov.Height; y++ {
		src := ov.cells[y*ov.Width : (y+1)*ov.Width]
		dstOff := (y+ov.OriginY-ny0)*nw + (ov.OriginX - nx0)
		copy(cells[dstOff:dstOff+ov.Width], src)
	}
	ov.OriginX, ov.OriginY, ov.Width, ov.Height = nx0, ny0, nw, nh
	ov.cells = cells
	return true
}

func (ov *AtlasOverlay) set(wx, wy int, s atlasCellState) {
	idx := (wy-ov.OriginY)*ov.Width + (wx - ov.OriginX)
	prev := ov.cells[idx]
	if prev == s {
		return
	}
	if prev == atlasUnknown {
		ov.known++
	}
	ov.cells[idx] = s
}

func (ov *AtlasOverlay) at(wx, wy int) atlasCellState {
	lx, ly := wx-ov.OriginX, wy-ov.OriginY
	if lx < 0 || ly < 0 || lx >= ov.Width || ly >= ov.Height {
		return atlasUnknown
	}
	return ov.cells[ly*ov.Width+lx]
}

// OverlayOnto patches dst with atlas knowledge wherever the two frames overlap. Blocked always
// wins — the atlas saw a wall the static map doesn't know about. Walkable only lifts cells dst
// considers NonWalkable: overwriting Walkable/LowPriority would erase NewGrid's wall-margin
// softening for cells both sources already agree are passable. Unknown cells never touch dst.
func (ov *AtlasOverlay) OverlayOnto(dst *Grid) {
	if ov == nil || dst == nil || len(ov.cells) == 0 || len(dst.CollisionGrid) == 0 {
		return
	}
	x0, y0 := rgMax(ov.OriginX, dst.OffsetX), rgMax(ov.OriginY, dst.OffsetY)
	x1 := rgMin(ov.OriginX+ov.Width, dst.OffsetX+dst.Width)
	y1 := rgMin(ov.OriginY+ov.Height, dst.OffsetY+dst.Height)
	for wy := y0; wy < y1; wy++ {
		row := dst.CollisionGrid[wy-dst.OffsetY]
		base := (wy - ov.OriginY) * ov.Width
		for wx := x0; wx < x1; wx++ {
			switch ov.cells[base+wx-ov.OriginX] {
			case atlasWalkable:
				if row[wx-dst.OffsetX] == CollisionTypeNonWalkable {
					row[wx-dst.OffsetX] = CollisionTypeWalkable
				}
			case atlasBlocked:
				row[wx-dst.OffsetX] = CollisionTypeNonWalkable
			}
		}
	}
}

// Disk format (little-endian): header {magic, version, reserved, seed, area, originX, originY,
// width, height} then width*height cells packed 4-per-byte (2 bits each, row-major, LSB first).
// The header carries seed/area redundantly with the filename so a renamed or misplaced file
// can't poison the wrong area.
type atlasFileHeader struct {
	Magic    uint32
	Version  uint16
	Reserved uint16
	Seed     uint64
	Area     int32
	OriginX  int32
	OriginY  int32
	Width    int32
	Height   int32
}

func (a *Atlas) saveOverlay(seed uint, area int, ov *AtlasOverlay) error {
	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		return err
	}
	hdr := atlasFileHeader{
		Magic:   atlasMagic,
		Version: atlasVersion,
		Seed:    uint64(seed),
		Area:    int32(area),
		OriginX: int32(ov.OriginX),
		OriginY: int32(ov.OriginY),
		Width:   int32(ov.Width),
		Height:  int32(ov.Height),
	}
	buf := &bytes.Buffer{}
	if err := binary.Write(buf, binary.LittleEndian, hdr); err != nil {
		return err
	}
	packed := make([]byte, (len(ov.cells)+3)/4)
	for i, c := range ov.cells {
		packed[i/4] |= byte(c) << uint((i%4)*2)
	}
	buf.Write(packed)
	return os.WriteFile(a.filePath(seed, area), buf.Bytes(), 0o644)
}

// loadOverlayFile returns the stored overlay, or an empty one on ANY problem — missing file,
// short read, bad magic, version bump, seed/area mismatch, implausible dims. Cache corruption
// must never take the bot down; the worst case is re-learning terrain we already walked.
func loadOverlayFile(path string, seed uint, area int) *AtlasOverlay {
	empty := &AtlasOverlay{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var hdr atlasFileHeader
	r := bytes.NewReader(raw)
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil {
		return empty
	}
	if hdr.Magic != atlasMagic || hdr.Version != atlasVersion {
		return empty
	}
	if hdr.Seed != uint64(seed) || hdr.Area != int32(area) {
		return empty
	}
	w, h := int(hdr.Width), int(hdr.Height)
	if w <= 0 || h <= 0 || w > atlasMaxDim || h > atlasMaxDim {
		return empty
	}
	need := (w*h + 3) / 4
	packed := make([]byte, need)
	if _, err := io.ReadFull(r, packed); err != nil {
		return empty
	}
	ov := &AtlasOverlay{
		OriginX: int(hdr.OriginX),
		OriginY: int(hdr.OriginY),
		Width:   w,
		Height:  h,
		cells:   make([]atlasCellState, w*h),
	}
	for i := range ov.cells {
		c := atlasCellState(packed[i/4] >> uint((i%4)*2) & 0b11)
		if c > atlasBlocked {
			// 0b11 is unused; a set high pair means bit rot — drop the whole file rather than
			// trust its neighbors.
			return empty
		}
		if c != atlasUnknown {
			ov.known++
		}
		ov.cells[i] = c
	}
	return ov
}
