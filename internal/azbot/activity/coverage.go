// coverage: THE ONE COVERAGE MODEL (the owner: "In the cave it goes to
// already-explored bits; it should try unexplored areas."). The room tour
// counted a room visited only when she stood INSIDE its rectangle, forgot
// everything on any area change (a town trip wiped it) and on restart; the
// search kept its own 20-box ledger. Both now ask this tracker: a seen bitmap
// per (seed, area) marked by line of sight on the live collision grid, a
// clustered frontier, and a path-cost goal picker — persisted in the WAL
// store at seed scope, so the portal home and the process restart come back
// to what she already saw.
package activity

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/coverage"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

// Cov is the executive's coverage tracker. Nil (tests, replay, drills): the
// explorers fall back to their blind heading walk.
var Cov *CovTracker

const (
	covFlushEvery = 10 * time.Second
	covLogEvery   = 30 * time.Second
	covPNGEvery   = 60 * time.Second
)

// CovKey is the store key of one (seed, area) coverage map.
func CovKey(seed uint, a area.ID) string { return fmt.Sprintf("coverage.%d.%d", seed, int(a)) }

type covKey struct {
	seed uint
	area area.ID
}

type covVisit struct {
	pol    *coverage.Policy
	leftAt time.Time
}

const covVisitGap = 30 * time.Second

// CovTracker owns the coverage maps and the per-visit goal policy.
type CovTracker struct {
	mu    sync.Mutex
	seed  func() uint                         // the map seed (MemoryReader.MapSeed)
	graph func() (*game.LiveRoomGraph, error) // the level's room graph (void/unloaded masks)
	mem   *memory.Store
	log   func(msg string, kv ...any)
	Sight int    // sight radius in tiles; default coverage.DefaultSight
	Dir   string // debug picture directory; default "logs"

	maps map[covKey]*coverage.Map // this process's maps (the store backs restarts)
	// visits: each area's policy and when she last left it. A seam flicker
	// (the area read flapping at a border) is the SAME visit — its blacklist
	// survives; a real return after covVisitGap starts a fresh one.
	visits map[covKey]*covVisit

	key     covKey
	inField bool
	m       *coverage.Map
	ter     *coverage.Terrain
	terFrom *game.Grid
	pol     *coverage.Policy
	me      data.Position
	status  coverage.Status
	doneLog bool
	dirty   bool
	flushAt time.Time
	logAt   time.Time
	pngAt   time.Time
	pngSeen int // seen count at the last picture: an exit with nothing new draws none
}

// NewCovTracker wires the tracker; mem and log may be nil.
func NewCovTracker(gr *game.MemoryReader, mem *memory.Store, log func(msg string, kv ...any)) *CovTracker {
	if log == nil {
		log = func(string, ...any) {}
	}
	c := &CovTracker{mem: mem, log: log, Sight: coverage.DefaultSight, Dir: "logs",
		maps: map[covKey]*coverage.Map{}, visits: map[covKey]*covVisit{}}
	if gr != nil {
		c.seed, c.graph = gr.MapSeed, gr.ReadCurrentRoomGraph
	}
	return c
}

// Tick runs once per executive tick: follow the area, rebuild the terrain when
// the live grid regrew, mark what she sees (a no-op unless she moved), and keep
// the flush / log / picture clocks. Cheap by construction.
// gridArea is the area the executive built grid for: a grid from another
// area (a failed re-align, a seam flicker) is never painted into this one.
func (c *CovTracker) Tick(s *percept.Snapshot, grid *game.Grid, gridArea area.ID) {
	if c == nil || s == nil || !s.Valid || s.Me.Area == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	var seed uint
	if c.seed != nil {
		seed = c.seed()
	}
	k := covKey{seed: seed, area: s.Me.Area}
	if k != c.key {
		c.leave(now)
		c.enter(k, !s.Me.InTown)
	}
	c.me = s.Me.Pos
	if !c.inField || grid == nil || gridArea != s.Me.Area {
		return
	}
	// A grid that does not hold her is another area's (the executive's
	// re-align failed this tick): never paint one area's terrain into another.
	if p := s.Me.Pos; p.X < grid.OffsetX || p.Y < grid.OffsetY ||
		p.X >= grid.OffsetX+grid.Width || p.Y >= grid.OffsetY+grid.Height {
		return
	}
	if grid != c.terFrom {
		var graph *game.LiveRoomGraph
		if c.graph != nil {
			if g, err := c.graph(); err == nil && g != nil && g.LevelID == s.Me.Area {
				graph = g
			}
		}
		c.ter, c.terFrom = liveTerrain(grid, graph, s.Me.Area), grid
		if c.m == nil {
			c.m = c.load(k, c.ter)
		}
	}
	if c.m == nil || c.ter == nil {
		return
	}
	if c.m.Update(c.ter, s.Me.Pos, c.Sight) > 0 {
		c.dirty = true
	}
	if c.dirty && now.Sub(c.flushAt) >= covFlushEvery {
		c.flush(now)
	}
	if now.Sub(c.logAt) >= covLogEvery {
		c.logAt = now
		c.log("coverage", "area", int(k.area), "cov", c.pct(), "frontier", len(c.frontier()), "seen", c.m.SeenCount())
	}
	if now.Sub(c.pngAt) >= covPNGEvery {
		c.picture(now)
	}
}

// Next is the ONE frontier picker Explore and Advance's search share. ok=false:
// no coverage knowledge here (no grid yet, town, no tracker) — the caller walks
// its blind fallback. who names the caller in the pick log.
func (c *CovTracker) Next(me data.Position, b coverage.Bias, who string) (pk coverage.Pick, st coverage.Status, ok bool) {
	if c == nil {
		return pk, 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inField || c.m == nil || c.ter == nil || c.pol == nil {
		return pk, 0, false
	}
	pk, st, changed := c.pol.Next(c.m, c.ter, me, b, time.Now())
	c.status = st
	if changed {
		c.doneLog = false
		c.log("explore", "verb", "explore", "holder", who,
			"goal", fmt.Sprintf("(%d,%d)", pk.Goal.X, pk.Goal.Y), "cluster", pk.Cluster, "size", pk.Size,
			"cost", pk.Cost, "cov", c.pct(), "why", pk.Why)
	} else if st != coverage.Exploring && !c.doneLog {
		c.doneLog = true
		c.log("explore", "verb", "explore", "holder", who, "goal", "none", "cov", c.pct(),
			"why", "area done: "+st.String())
	}
	return pk, st, true
}

// Fail convicts the current goal (the planner found no path, or the walk
// stalled): blacklisted for this area visit.
func (c *CovTracker) Fail(who, why string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pol == nil || c.pol.Current() == nil {
		return
	}
	g := c.pol.Current().Goal
	c.pol.Fail()
	c.log("explore", "verb", "explore", "holder", who, "goal", fmt.Sprintf("(%d,%d)", g.X, g.Y),
		"cov", c.pct(), "why", "blacklisted: "+why)
}

// Flush persists the current map now (the executive's clean exit).
func (c *CovTracker) Flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flush(time.Now())
}

func (c *CovTracker) enter(k covKey, field bool) {
	c.key, c.inField = k, field
	c.m, c.ter, c.terFrom = nil, nil, nil
	// The blacklist belongs to one visit; a flicker back within 30 s is the
	// same visit.
	v := c.visits[k]
	if v == nil || time.Since(v.leftAt) > covVisitGap {
		v = &covVisit{pol: &coverage.Policy{}}
		c.visits[k] = v
	}
	c.pol = v.pol
	c.dirty, c.doneLog = false, false
	c.logAt, c.pngAt, c.pngSeen = time.Now(), time.Now(), -1
	c.status = coverage.Exploring
}

// leave closes a field visit: flush and a last picture.
func (c *CovTracker) leave(now time.Time) {
	if v := c.visits[c.key]; v != nil {
		v.leftAt = now
	}
	if !c.inField || c.m == nil {
		return
	}
	c.flush(now)
	if c.m.SeenCount() != c.pngSeen { // a seam flicker with nothing new seen draws no picture
		c.picture(now)
	}
	c.log("coverage", "area", int(c.key.area), "cov", c.pct(), "frontier", len(c.frontier()),
		"seen", c.m.SeenCount(), "why", "area exit")
}

// load: this process's map, else the store's, else a fresh one.
func (c *CovTracker) load(k covKey, t *coverage.Terrain) *coverage.Map {
	if m, ok := c.maps[k]; ok {
		m.Rebase(t)
		return m
	}
	var m *coverage.Map
	if c.mem != nil {
		var snap coverage.Snapshot
		if c.mem.GetJSON(CovKey(k.seed, k.area), &snap) {
			if r, err := coverage.FromSnapshot(snap); err == nil {
				r.Rebase(t)
				m = r
				c.log("coverage", "area", int(k.area), "why", "restored from the store", "seen", r.SeenCount())
			}
		}
	}
	if m == nil {
		m = coverage.NewFor(t)
	}
	c.maps[k] = m
	return m
}

func (c *CovTracker) flush(now time.Time) {
	c.flushAt = now
	if !c.dirty || c.m == nil || c.mem == nil {
		return
	}
	c.dirty = false
	c.mem.PutJSON(CovKey(c.key.seed, c.key.area), memory.ScopeSeed,
		memory.Provenance{Source: "measured", Evidence: "line of sight on the live grid"}, c.m.Snapshot())
}

func (c *CovTracker) frontier() []coverage.Cluster {
	if c.m == nil || c.ter == nil {
		return nil
	}
	return c.m.Frontier(c.ter, coverage.DefaultChunk)
}

func (c *CovTracker) pct() string {
	if c.m == nil || c.ter == nil {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", 100*c.m.CoverageAll(c.ter))
}

// picture writes logs/coverage_<area>_<seed>.png.
func (c *CovTracker) picture(now time.Time) {
	c.pngAt = now
	if c.m == nil || c.ter == nil {
		return
	}
	c.pngSeen = c.m.SeenCount()
	ov := coverage.Overlay{Me: c.me}
	if c.pol != nil {
		if cur := c.pol.Current(); cur != nil {
			g := cur.Goal
			ov.Goal, ov.Route = &g, cur.Path
		}
		ov.Black = c.pol.Blacklisted()
	}
	img := coverage.Render(c.m, c.ter, c.frontier(), ov)
	dir := c.Dir
	if dir == "" {
		dir = "logs"
	}
	path := filepath.Join(dir, fmt.Sprintf("coverage_%d_%d.png", int(c.key.area), c.key.seed))
	if f, err := os.Create(path); err == nil {
		_ = png.Encode(f, img)
		f.Close()
	}
}

// liveTerrain adapts the live collision grid (+ the level's room graph, when it
// read) into coverage terrain. Outside every room of the level: Void. A room
// not streamed in: Unknown. Loaded cells: the grid's truth — except that the
// grid folds unknown ground and floor near walls into one LowPriority value,
// so LowPriority reads Floor only within 2 of a real wall (exactly the band
// NewGrid lowers) and Unknown elsewhere.
func liveTerrain(g *game.Grid, graph *game.LiveRoomGraph, lvl area.ID) *coverage.Terrain {
	W, H := g.Width, g.Height
	nearWall := make([]bool, W*H)
	for y := 0; y < H; y++ {
		row := g.CollisionGrid[y]
		for x := 0; x < W && x < len(row); x++ {
			if row[x] != game.CollisionTypeNonWalkable {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					nx, ny := x+dx, y+dy
					if nx >= 0 && ny >= 0 && nx < W && ny < H {
						nearWall[ny*W+nx] = true
					}
				}
			}
		}
	}
	// room membership: 0 = no room (void), 1 = unloaded room, 2 = loaded room
	var rooms []uint8
	if graph != nil {
		rooms = make([]uint8, W*H)
		for _, r := range graph.Rooms {
			if r.LevelID != lvl {
				continue
			}
			v := uint8(1)
			if r.Room1Ptr != 0 {
				v = 2
			}
			x0, y0 := r.Rect.X*5-g.OffsetX, r.Rect.Y*5-g.OffsetY
			for y := maxInt(y0, 0); y < minInt(y0+r.Rect.H*5, H); y++ {
				for x := maxInt(x0, 0); x < minInt(x0+r.Rect.W*5, W); x++ {
					if rooms[y*W+x] < v {
						rooms[y*W+x] = v
					}
				}
			}
		}
		n := 0
		for _, v := range rooms {
			if v != 0 {
				n++
			}
		}
		if n == 0 {
			rooms = nil // the graph did not line up with this frame: trust the grid alone
		}
	}
	return coverage.NewTerrain(g.OffsetX, g.OffsetY, W, H, func(x, y int) coverage.Cell {
		i := y*W + x
		if rooms != nil {
			switch rooms[i] {
			case 0:
				return coverage.Void
			case 1:
				return coverage.Unknown
			}
		}
		row := g.CollisionGrid[y]
		if x >= len(row) {
			return coverage.Void
		}
		switch row[x] {
		case game.CollisionTypeNonWalkable:
			return coverage.Wall
		case game.CollisionTypeLowPriority:
			if !nearWall[i] {
				return coverage.Unknown
			}
		}
		return coverage.Floor
	})
}

// covWalker walks coverage goals for one activity through MoveTo; the
// planner's verdicts feed the picker's blacklist.
type covWalker struct {
	goal     data.Position
	regridAt time.Time
}

// step asks the shared picker for a goal and walks it one slice. ok=false: no
// coverage knowledge here — the caller walks its blind fallback. A status
// other than Exploring means the area is done (nothing was walked).
func (w *covWalker) step(ctx *Ctx, b coverage.Bias, who string) (st coverage.Status, ok bool) {
	s := ctx.Snap
	if s == nil || !s.Valid {
		return coverage.Exploring, false
	}
	pk, st, ok := Cov.Next(s.Me.Pos, b, who)
	if !ok {
		return st, false
	}
	if st != coverage.Exploring {
		return st, true
	}
	NavDebug(ctx, pk.Goal, who)
	if w.goal != pk.Goal {
		forgetMove(who) // a new frontier goal is a new trip
		w.goal = pk.Goal
	}
	// No grid: MoveTo walks by dead reckoning (the frontier came from a
	// tracker that saw the ground; the grid only failed to build).
	switch res := moveTo(ctx, pk.Goal, moveto.Opts{Holder: who, Purpose: moveto.Travel, Arrive: 2, MaxHold: 1500 * time.Millisecond}); res.State {
	case moveto.NoPath:
		Cov.Fail(who, "planner NoPath")
	case moveto.Stalled:
		Cov.Fail(who, "walk stalled")
	case moveto.Arrived:
		// At the frontier: the rooms beyond stream in now — regrow the grid so
		// the picker sees them instead of waiting out the executive's clock.
		if ctx.Regrid != nil && time.Since(w.regridAt) > 3*time.Second {
			w.regridAt = time.Now()
			ctx.Regrid()
		}
	}
	return st, true
}
