package activity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/coverage"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

func covGrid(offX, offY int, rows []string) *game.Grid {
	raw := make([][]game.CollisionType, len(rows))
	for y, r := range rows {
		raw[y] = make([]game.CollisionType, len(r))
		for x := range r {
			switch r[x] {
			case '#':
				raw[y][x] = game.CollisionTypeNonWalkable
			case '?':
				raw[y][x] = game.CollisionTypeLowPriority // unstreamed: optimistic
			default:
				raw[y][x] = game.CollisionTypeWalkable
			}
		}
	}
	return game.NewGrid(raw, offX, offY)
}

func covSnap(a area.ID, town bool, x, y int) *percept.Snapshot {
	s := &percept.Snapshot{Valid: true}
	s.Me.Area, s.Me.InTown, s.Me.Pos = a, town, data.Position{X: x, Y: y}
	return s
}

// A west cave room (explored on the first visit) joined by a 3-wide passage
// to an east room she never reached.
var covCave = []string{
	"##################################################",
	"#..........#######################...............#",
	"#..........#######################...............#",
	"#................................................#",
	"#................................................#",
	"#................................................#",
	"#..........#######################...............#",
	"#..........#######################...............#",
	"##################################################",
}

// THE OWNER'S COMPLAINT, end to end through the tracker and the WAL store:
// explore the west, portal to town, come back — and separately restart the
// process — the picker must send her EAST, never back over the west.
func TestCovTrackerTownTripAndRestartDoNotReExplore(t *testing.T) {
	// Not t.TempDir: the store has no Close, and Windows refuses to delete an
	// open WAL — the cleanup would fail the test, not the code.
	dir, err := os.MkdirTemp("", "covtrack")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	st, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const ox, oy = 5000, 7000
	grid := covGrid(ox, oy, covCave)
	cave := area.DenOfEvil
	newTracker := func(st *memory.Store) *CovTracker {
		c := NewCovTracker(nil, st, nil)
		c.seed = func() uint { return 42 }
		c.Dir = dir
		return c
	}
	c := newTracker(st)
	for _, q := range [][2]int{{2, 2}, {9, 2}, {9, 6}, {2, 6}, {14, 4}} {
		c.Tick(covSnap(cave, false, ox+q[0], oy+q[1]), grid, cave)
	}
	seen := c.m.SeenCount()
	if seen == 0 {
		t.Fatal("nothing seen on the first visit")
	}
	// The portal home: area exit flushes the map and draws the picture.
	c.Tick(covSnap(area.RogueEncampment, true, 100, 100), nil, 0)
	if _, err := os.Stat(filepath.Join(dir, "coverage_8_42.png")); err != nil {
		t.Fatalf("area exit picture: %v", err)
	}
	var snap coverage.Snapshot
	if !st.GetJSON(CovKey(42, cave), &snap) {
		t.Fatal("area exit did not persist the map at seed scope")
	}
	if f, _ := st.Get(CovKey(42, cave)); f.Scope != memory.ScopeSeed {
		t.Fatalf("scope %d, want seed", f.Scope)
	}
	if pk, _, ok := c.Next(data.Position{X: 100, Y: 100}, coverage.Bias{}, "explore"); ok {
		t.Fatalf("town has no coverage goal, got %v", pk.Goal)
	}

	back := func(c *CovTracker, who string) {
		t.Helper()
		c.Tick(covSnap(cave, false, ox+2, oy+2), grid, cave)
		if c.m.SeenCount() != seen {
			t.Fatalf("%s: seen %d, want the %d from the first visit", who, c.m.SeenCount(), seen)
		}
		pk, status, ok := c.Next(data.Position{X: ox + 2, Y: oy + 2}, coverage.Bias{}, "explore")
		if !ok || status != coverage.Exploring {
			t.Fatalf("%s: no goal (%v %v)", who, ok, status)
		}
		if pk.Goal.X-ox < 20 {
			t.Fatalf("%s: goal %v re-explores the west; want the unexplored east", who, pk.Goal)
		}
	}
	back(c, "town trip")

	// The restart: a fresh store handle replays the WAL.
	stop := make(chan struct{})
	close(stop)
	st.Scribe(stop) // flush the WAL now
	st2, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	back(newTracker(st2), "restart")
}

func TestLiveTerrainReadsGridAndRoomGraph(t *testing.T) {
	grid := covGrid(100, 200, []string{
		"##########??????????",
		"#........#??????????",
		"#........#??????????",
		"#.........??????????",
		"##########??????????",
	})
	ter := liveTerrain(grid, nil, area.DenOfEvil)
	if ter.At(data.Position{X: 100, Y: 200}) != coverage.Wall {
		t.Fatal("NonWalkable is wall")
	}
	if ter.At(data.Position{X: 103, Y: 202}) != coverage.Floor {
		t.Fatal("floor (LowPriority near a wall) must read floor")
	}
	if ter.At(data.Position{X: 116, Y: 202}) != coverage.Unknown {
		t.Fatal("LowPriority far from any wall is unstreamed ground")
	}
	// With the room graph: tiles outside every room are void; an unloaded
	// room is unknown whatever the grid says.
	graph := &game.LiveRoomGraph{LevelID: area.DenOfEvil, Rooms: map[uintptr]game.LiveRoom{
		1: {LevelID: area.DenOfEvil, Rect: game.TileRect{X: 20, Y: 40, W: 2, H: 1}, Room1Ptr: 0x10},
		2: {LevelID: area.DenOfEvil, Rect: game.TileRect{X: 22, Y: 40, W: 1, H: 1}, Room1Ptr: 0},
	}}
	ter = liveTerrain(grid, graph, area.DenOfEvil)
	if ter.At(data.Position{X: 103, Y: 202}) != coverage.Floor {
		t.Fatal("loaded room keeps the grid's truth")
	}
	if ter.At(data.Position{X: 112, Y: 202}) != coverage.Unknown {
		t.Fatal("unloaded room reads unknown")
	}
	if ter.At(data.Position{X: 117, Y: 202}) != coverage.Void {
		t.Fatal("outside every room is void")
	}
}

// A seam flicker is the same visit (its blacklist survives), and a grid that
// does not hold her (the re-align failed) never paints another area's map.
func TestCovTrackerFlickerKeepsVisitAndStaleGridIgnored(t *testing.T) {
	const ox, oy = 5000, 7000
	grid := covGrid(ox, oy, covCave)
	c := NewCovTracker(nil, nil, nil)
	c.seed = func() uint { return 7 }
	c.Dir = t.TempDir()
	c.Tick(covSnap(area.DenOfEvil, false, ox+2, oy+2), grid, area.DenOfEvil)
	if _, _, ok := c.Next(data.Position{X: ox + 2, Y: oy + 2}, coverage.Bias{}, "explore"); !ok {
		t.Fatal("no goal")
	}
	c.Fail("explore", "test")
	pol := c.pol
	c.Tick(covSnap(area.BloodMoor, false, ox+2, oy+2), grid, area.DenOfEvil) // the flicker: the grid is still Den's
	if c.m != nil {
		t.Fatal("another area's grid painted the Blood Moor map")
	}
	c.Tick(covSnap(area.DenOfEvil, false, ox+2, oy+2), grid, area.DenOfEvil)
	if c.pol != pol || len(c.pol.Blacklisted()) != 1 {
		t.Fatal("a flicker back must resume the same visit and its blacklist")
	}
	// A stale grid that does not hold her is ignored.
	far := covGrid(0, 0, covCave)
	c2 := NewCovTracker(nil, nil, nil)
	c2.Tick(covSnap(area.DenOfEvil, false, ox+2, oy+2), far, area.DenOfEvil)
	if c2.m != nil {
		t.Fatal("a grid that does not hold her must not seed a map")
	}
}
