package game

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// flatGrid builds a Grid literal directly instead of via NewGrid: NewGrid rewrites Walkable
// cells near NonWalkable ones to LowPriority, which would make cell-exact assertions depend on
// that softening pass rather than on atlas behavior.
func flatGrid(offX, offY, w, h int, fill CollisionType) *Grid {
	cg := make([][]CollisionType, h)
	for y := range cg {
		cg[y] = make([]CollisionType, w)
		for x := range cg[y] {
			cg[y][x] = fill
		}
	}
	return &Grid{OffsetX: offX, OffsetY: offY, Width: w, Height: h, CollisionGrid: cg}
}

// loadedRoom returns a LiveRoom with a fake streamed-in Room1 pointer covering the given TILE
// rect (world subtiles = tiles*5, matching roomgraph.go's convention).
func loadedRoom(tx, ty, tw, th int) LiveRoom {
	return LiveRoom{Room1Ptr: 1, Rect: TileRect{X: tx, Y: ty, W: tw, H: th}}
}

func TestAtlasMergeRespectsRoomMask(t *testing.T) {
	a := NewAtlas(t.TempDir())
	// Live grid claims the whole 20x20 frame is walkable, but only one 2x2-tile room
	// (subtiles [0,10)x[0,10)) is actually loaded: everything else is unobserved.
	live := flatGrid(0, 0, 20, 20, CollisionTypeWalkable)
	a.MergeLiveGrid(7, 1, live, []LiveRoom{loadedRoom(0, 0, 2, 2)})

	if got := a.KnownCells(7, 1); got != 100 {
		t.Fatalf("KnownCells = %d, want 100 (only the loaded room)", got)
	}

	dst := flatGrid(0, 0, 20, 20, CollisionTypeNonWalkable)
	a.Overlay(7, 1).OverlayOnto(dst)
	if dst.CollisionGrid[5][5] != CollisionTypeWalkable {
		t.Errorf("cell inside loaded room not patched walkable")
	}
	if dst.CollisionGrid[15][15] != CollisionTypeNonWalkable {
		t.Errorf("cell outside loaded room was patched; must stay unknown")
	}
	if dst.CollisionGrid[5][15] != CollisionTypeNonWalkable || dst.CollisionGrid[15][5] != CollisionTypeNonWalkable {
		t.Errorf("cells outside room rect were patched")
	}
}

func TestAtlasSkipsUnloadedRooms(t *testing.T) {
	a := NewAtlas(t.TempDir())
	live := flatGrid(0, 0, 20, 20, CollisionTypeWalkable)
	unloaded := LiveRoom{Room1Ptr: 0, Rect: TileRect{X: 0, Y: 0, W: 2, H: 2}}
	a.MergeLiveGrid(7, 1, live, []LiveRoom{unloaded})
	if got := a.KnownCells(7, 1); got != 0 {
		t.Fatalf("KnownCells = %d, want 0: unloaded room cells are not observations", got)
	}
}

func TestAtlasRecordsBlockedAndLastWriteWins(t *testing.T) {
	a := NewAtlas(t.TempDir())
	live := flatGrid(0, 0, 10, 10, CollisionTypeWalkable)
	live.CollisionGrid[3][4] = CollisionTypeNonWalkable
	// LowPriority is walkable terrain (NewGrid derives it from Walkable near walls).
	live.CollisionGrid[3][5] = CollisionTypeLowPriority
	a.MergeLiveGrid(1, 2, live, []LiveRoom{loadedRoom(0, 0, 2, 2)})

	dst := flatGrid(0, 0, 10, 10, CollisionTypeWalkable)
	a.Overlay(1, 2).OverlayOnto(dst)
	if dst.CollisionGrid[3][4] != CollisionTypeNonWalkable {
		t.Errorf("blocked observation must override walkable static cell")
	}
	if dst.CollisionGrid[3][5] == CollisionTypeNonWalkable {
		t.Errorf("LowPriority live cell must be recorded walkable")
	}

	// Re-merge with the cell now walkable: last write wins.
	live.CollisionGrid[3][4] = CollisionTypeWalkable
	a.MergeLiveGrid(1, 2, live, []LiveRoom{loadedRoom(0, 0, 2, 2)})
	dst2 := flatGrid(0, 0, 10, 10, CollisionTypeNonWalkable)
	a.Overlay(1, 2).OverlayOnto(dst2)
	if dst2.CollisionGrid[3][4] != CollisionTypeWalkable {
		t.Errorf("last-write-wins: re-observed walkable cell still blocked")
	}
	if got := a.KnownCells(1, 2); got != 100 {
		t.Fatalf("KnownCells = %d, want 100 after re-merge of same room", got)
	}
}

func TestAtlasOverlayTranslation(t *testing.T) {
	a := NewAtlas(t.TempDir())
	// Live grid frame origin (100,200); room at tile (20,40) covers subtiles [100,105)x[200,205).
	live := flatGrid(100, 200, 30, 30, CollisionTypeWalkable)
	live.CollisionGrid[3][2] = CollisionTypeNonWalkable // world (102, 203)
	a.MergeLiveGrid(9, 3, live, []LiveRoom{loadedRoom(20, 40, 1, 1)})

	if got := a.KnownCells(9, 3); got != 25 {
		t.Fatalf("KnownCells = %d, want 25", got)
	}

	// Destination grid uses a DIFFERENT offset; the overlay must land on the same world cells.
	dst := flatGrid(95, 195, 20, 20, CollisionTypeNonWalkable)
	a.Overlay(9, 3).OverlayOnto(dst)
	// World (100,200) -> dst local (5,5): walkable observation lifts the blocked static cell.
	if dst.CollisionGrid[5][5] != CollisionTypeWalkable {
		t.Errorf("world (100,200) not patched at dst local (5,5)")
	}
	// World (102,203) -> dst local (7,8): observed blocked.
	if dst.CollisionGrid[8][7] != CollisionTypeNonWalkable {
		t.Errorf("world (102,203) should remain blocked at dst local (7,8)")
	}
	// World (99,200) is outside the room: dst local (4,5) untouched.
	if dst.CollisionGrid[5][4] != CollisionTypeNonWalkable {
		t.Errorf("world (99,200) outside room was patched")
	}
	// A dst that doesn't overlap the overlay at all must be untouched.
	far := flatGrid(1000, 1000, 5, 5, CollisionTypeNonWalkable)
	a.Overlay(9, 3).OverlayOnto(far)
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			if far.CollisionGrid[y][x] != CollisionTypeNonWalkable {
				t.Fatalf("non-overlapping dst modified at (%d,%d)", x, y)
			}
		}
	}
}

func TestAtlasOverlayGrowth(t *testing.T) {
	a := NewAtlas(t.TempDir())
	// First observation near the area origin, second far away: the overlay's bounding box must
	// grow to hold both without losing the first.
	live1 := flatGrid(0, 0, 10, 10, CollisionTypeWalkable)
	a.MergeLiveGrid(4, 5, live1, []LiveRoom{loadedRoom(0, 0, 2, 2)})
	live2 := flatGrid(400, 400, 10, 10, CollisionTypeWalkable)
	live2.CollisionGrid[0][0] = CollisionTypeNonWalkable // world (400,400)
	a.MergeLiveGrid(4, 5, live2, []LiveRoom{loadedRoom(80, 80, 2, 2)})

	if got := a.KnownCells(4, 5); got != 200 {
		t.Fatalf("KnownCells = %d, want 200 across both rooms", got)
	}
	ov := a.Overlay(4, 5)
	if ov.Width < 410 || ov.Height < 410 {
		t.Fatalf("overlay did not grow: %dx%d at (%d,%d)", ov.Width, ov.Height, ov.OriginX, ov.OriginY)
	}
	dst := flatGrid(0, 0, 500, 500, CollisionTypeNonWalkable)
	ov.OverlayOnto(dst)
	if dst.CollisionGrid[5][5] != CollisionTypeWalkable {
		t.Errorf("first room lost after growth")
	}
	if dst.CollisionGrid[405][405] != CollisionTypeWalkable {
		t.Errorf("second room not recorded")
	}
	if dst.CollisionGrid[400][400] != CollisionTypeNonWalkable {
		t.Errorf("blocked cell in second room lost after growth")
	}
	if dst.CollisionGrid[200][200] != CollisionTypeNonWalkable {
		t.Errorf("gap between rooms must stay unknown")
	}
}

func TestAtlasSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := NewAtlas(dir)
	live := flatGrid(100, 200, 30, 30, CollisionTypeWalkable)
	live.CollisionGrid[3][2] = CollisionTypeNonWalkable
	a.MergeLiveGrid(42, 8, live, []LiveRoom{loadedRoom(20, 40, 1, 1)})
	if err := a.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Fresh atlas over the same dir: lazy load must reproduce the exact overlay.
	b := NewAtlas(dir)
	if got := b.KnownCells(42, 8); got != 25 {
		t.Fatalf("KnownCells after reload = %d, want 25", got)
	}
	dst := flatGrid(100, 200, 30, 30, CollisionTypeNonWalkable)
	b.Overlay(42, 8).OverlayOnto(dst)
	if dst.CollisionGrid[0][0] != CollisionTypeWalkable {
		t.Errorf("walkable cell lost in round-trip")
	}
	if dst.CollisionGrid[3][2] != CollisionTypeNonWalkable {
		t.Errorf("blocked cell lost in round-trip")
	}
	if dst.CollisionGrid[10][10] != CollisionTypeNonWalkable {
		t.Errorf("unknown cell gained state in round-trip")
	}
	// A different seed must not pick up this file.
	if got := b.KnownCells(43, 8); got != 0 {
		t.Fatalf("seed 43 read seed 42's data: KnownCells = %d", got)
	}
}

func TestAtlasCorruptFileTolerated(t *testing.T) {
	dir := t.TempDir()
	a := NewAtlas(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pure garbage.
	if err := os.WriteFile(a.filePath(5, 1), []byte("not an atlas file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Truncated: valid header, missing payload.
	hdr := atlasFileHeader{Magic: atlasMagic, Version: atlasVersion, Seed: 5, Area: 2, Width: 100, Height: 100}
	f, err := os.Create(a.filePath(5, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(f, binary.LittleEndian, hdr); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// Wrong version.
	hdr3 := hdr
	hdr3.Area = 3
	hdr3.Version = atlasVersion + 1
	hdr3.Width, hdr3.Height = 1, 1
	f3, err := os.Create(a.filePath(5, 3))
	if err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(f3, binary.LittleEndian, hdr3); err != nil {
		t.Fatal(err)
	}
	f3.Write([]byte{0})
	f3.Close()

	for _, area := range []int{1, 2, 3} {
		if got := a.KnownCells(5, area); got != 0 {
			t.Errorf("area %d: corrupt file yielded KnownCells = %d, want 0", area, got)
		}
	}
	// Corrupt cache must not block accumulating fresh data over it.
	live := flatGrid(0, 0, 10, 10, CollisionTypeWalkable)
	a.MergeLiveGrid(5, 1, live, []LiveRoom{loadedRoom(0, 0, 1, 1)})
	if got := a.KnownCells(5, 1); got != 25 {
		t.Fatalf("merge over corrupt file: KnownCells = %d, want 25", got)
	}
	if err := a.Save(); err != nil {
		t.Fatalf("Save over corrupt file: %v", err)
	}
	if got := NewAtlas(dir).KnownCells(5, 1); got != 25 {
		t.Fatalf("rewritten file unreadable: KnownCells = %d, want 25", got)
	}
}

func TestAtlasNilSafety(t *testing.T) {
	// None of these may panic.
	var nilAtlas *Atlas
	nilAtlas.MergeLiveGrid(1, 1, flatGrid(0, 0, 2, 2, CollisionTypeWalkable), []LiveRoom{loadedRoom(0, 0, 1, 1)})
	if nilAtlas.KnownCells(1, 1) != 0 {
		t.Error("nil atlas KnownCells != 0")
	}
	if err := nilAtlas.Save(); err != nil {
		t.Errorf("nil atlas Save: %v", err)
	}
	nilAtlas.Overlay(1, 1).OverlayOnto(flatGrid(0, 0, 2, 2, CollisionTypeWalkable))

	a := NewAtlas(filepath.Join(t.TempDir(), "sub"))
	a.MergeLiveGrid(1, 1, nil, []LiveRoom{loadedRoom(0, 0, 1, 1)})
	a.MergeLiveGrid(1, 1, flatGrid(0, 0, 2, 2, CollisionTypeWalkable), nil)
	a.MergeLiveGrid(1, 1, &Grid{}, []LiveRoom{loadedRoom(0, 0, 1, 1)})
	if got := a.KnownCells(1, 1); got != 0 {
		t.Errorf("degenerate merges recorded cells: %d", got)
	}
	a.Overlay(1, 1).OverlayOnto(nil)
	a.Overlay(1, 1).OverlayOnto(&Grid{})
	if err := a.Save(); err != nil {
		t.Errorf("Save with nothing dirty: %v", err)
	}
}
