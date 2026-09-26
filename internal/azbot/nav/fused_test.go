package nav

import "testing"

// costGrid parses rows: '.' observed walkable (free), '?' unknown (walkable at
// cost), anything else wall.
func costGrid(rows []string, unknownCost int32) *Grid {
	w := len(rows[0])
	return NewGridCost(0, 0, w, len(rows), func(x, y int) bool {
		return rows[y][x] == '.' || rows[y][x] == '?'
	}, func(x, y int) int32 {
		if rows[y][x] == '?' {
			return unknownCost
		}
		return 0
	})
}

func TestPlanPrefersObservedGroundOverUnknown(t *testing.T) {
	// A straight line through unknown space vs a slightly longer known detour.
	rows := []string{
		"..................",
		".################.",
		".################.",
		"..??????????????..",
	}
	start, goal := Pos{X: 0, Y: 3}, Pos{X: 17, Y: 3}
	free := costGrid(rows, 0).Plan(start, goal, Options{ClearW: -1})
	priced := costGrid(rows, 20).Plan(start, goal, Options{ClearW: -1})
	if !free.Found || !priced.Found {
		t.Fatalf("no route: %s / %s", free.Reason, priced.Reason)
	}
	throughUnknown := func(p Plan) bool {
		for _, c := range p.Path {
			if c.Y == 3 && c.X > 2 && c.X < 15 {
				return true
			}
		}
		return false
	}
	if !throughUnknown(free) {
		t.Errorf("without a price the short line through unknown should win")
	}
	if throughUnknown(priced) {
		t.Errorf("priced unknown: the known detour must win, got %v", priced.Path)
	}
	// Unknown stays plannable: with no known alternative the route goes through it.
	only := costGrid([]string{"..????????.."}, 20).Plan(Pos{X: 0, Y: 0}, Pos{X: 11, Y: 0}, Options{})
	if !only.Found {
		t.Fatalf("unknown must stay plannable: %s", only.Reason)
	}
}

func TestPricedFieldDoesNotFlood(t *testing.T) {
	// A big unknown field (every cell priced): exact A* would expand most of it
	// (the price outruns the octile estimate); the weighted estimate keeps it lean.
	w, h := 600, 600
	g := NewGridCost(0, 0, w, h, func(x, y int) bool { return !(x%97 == 50 && y%3 != 0) },
		func(x, y int) int32 { return 5 })
	p := g.Plan(Pos{X: 5, Y: 5}, Pos{X: 590, Y: 420}, Options{})
	if !p.Found || p.Expanded > w*h/8 {
		t.Fatalf("priced field: found=%t expanded=%d (budget %d)", p.Found, p.Expanded, w*h/8)
	}
	t.Logf("expanded %d of %d", p.Expanded, w*h)
}

func TestSimplifyDoesNotCutThroughPricierGround(t *testing.T) {
	// An L-shaped known corridor around an unknown block: the string-puller must
	// not shortcut the corner across the unknown cells.
	rows := []string{
		"..........",
		"??????????",
		"??????????",
		"??????????",
		"??????????",
		"..........",
	}
	g := costGrid(rows, 50)
	path := []Pos{}
	for x := 0; x <= 9; x++ {
		path = append(path, Pos{X: x, Y: 0})
	}
	for y := 1; y <= 5; y++ {
		path = append(path, Pos{X: 9, Y: y})
	}
	for _, p := range g.Simplify(path, nil)[1:] {
		if g.CellCost(p) > 0 && p.X != 9 {
			t.Fatalf("shortcut landed in unknown at %v", p)
		}
	}
}

func TestNewlyBlockedWallAheadAsksReplan(t *testing.T) {
	old := ParseGrid(0, 0, []string{
		"....................",
		"....................",
		"....................",
	})
	// A room streamed in: a wall across the corridor AHEAD of the player.
	next := ParseGrid(0, 0, []string{
		"............#.......",
		"............#.......",
		"............#.......",
	})
	f := NewFollower(old, nil)
	me := Pos{X: 1, Y: 1}
	f.SetPath([]Pos{me, {X: 19, Y: 1}}, me, t0)
	at, hit := f.NewlyBlocked(old, next)
	if !hit || at != (Pos{X: 12, Y: 1}) {
		t.Fatalf("new wall across the route: hit=%t at=%v", hit, at)
	}
	// A wall that streams in OFF the route changes nothing.
	side := ParseGrid(0, 0, []string{
		"....................",
		"....................",
		"#######.............",
	})
	if _, hit := f.NewlyBlocked(old, side); hit {
		t.Fatal("wall off the route must not replan")
	}
	// A wall BEHIND the committed progress is history, not a reason to replan.
	f2 := NewFollower(old, nil)
	f2.SetPath([]Pos{{X: 0, Y: 1}, {X: 6, Y: 1}, {X: 12, Y: 0}, {X: 19, Y: 0}}, Pos{X: 0, Y: 1}, t0)
	f2.committedS = f2.cumS[2] + 0.5
	behind := ParseGrid(0, 0, []string{
		"....................",
		"...#................",
		"....................",
	})
	if _, hit := f2.NewlyBlocked(old, behind); hit {
		t.Fatal("wall behind the committed segment must not replan")
	}
	// Once swapped in, the follower walks the new grid.
	f.SetGrid(next)
	if f.g != next {
		t.Fatal("SetGrid did not swap")
	}
}
