package moveto

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/nav"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

// fgrid parses rows into a fused game grid: '.' walkable, '#' wall.
func fgrid(rows ...string) *game.Grid {
	cg := make([][]game.CollisionType, len(rows))
	for y, r := range rows {
		cg[y] = make([]game.CollisionType, len(r))
		for x, ch := range r {
			if ch == '#' {
				cg[y][x] = game.CollisionTypeNonWalkable
			} else {
				cg[y][x] = game.CollisionTypeWalkable
			}
		}
	}
	return &game.Grid{Width: len(rows[0]), Height: len(rows), CollisionGrid: cg}
}

// fakeBody walks the grid: each stride moves up to 2 tiles toward its target,
// stopping at walls — enough physics to judge what MoveTo asks for.
type fakeBody struct {
	pos     data.Position
	g       *game.Grid
	strides []data.Position
	holds   []time.Duration
	clicks  []data.Position
	clickOK bool
}

func (b *fakeBody) Here() (data.Position, bool) { return b.pos, true }

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

func (b *fakeBody) walkable(p data.Position) bool {
	if p.X < 0 || p.Y < 0 || p.Y >= len(b.g.CollisionGrid) || p.X >= len(b.g.CollisionGrid[p.Y]) {
		return true // off the frame: unloaded ground
	}
	return b.g.CollisionGrid[p.Y][p.X] != game.CollisionTypeNonWalkable
}

func (b *fakeBody) Stride(to data.Position, hold time.Duration, minGain int, holder string) verbs.Outcome {
	b.strides = append(b.strides, to)
	b.holds = append(b.holds, hold)
	moved := 0
	for i := 0; i < 2 && b.pos != to; i++ {
		n := data.Position{X: b.pos.X + sign(to.X-b.pos.X), Y: b.pos.Y + sign(to.Y-b.pos.Y)}
		if !b.walkable(n) {
			break
		}
		b.pos = n
		moved++
	}
	o := verbs.Outcome{Verb: "stride", Holder: holder, Result: verbs.ResDone}
	if moved < minGain {
		o.Result = verbs.ResBlocked
	}
	return o
}

func (b *fakeBody) Click(to data.Position, hold time.Duration, key byte, holder string) verbs.Outcome {
	b.clicks = append(b.clicks, to)
	if b.clickOK {
		return verbs.Outcome{Verb: "clickmove", Result: verbs.ResDone}
	}
	return verbs.Outcome{Verb: "clickmove", Result: verbs.ResBlocked}
}

func env(b *fakeBody, g *game.Grid) Env {
	return Env{Body: b, Led: verbs.NewLedger(256), Grid: g}
}

func open(w, h int) *game.Grid {
	rows := make([]string, h)
	for i := range rows {
		rows[i] = strings.Repeat(".", w)
	}
	return fgrid(rows...)
}

// A moving target re-goals the SAME journey (stuck memory kept); a new goal, a
// new purpose, a new area frame or a long idle opens a fresh one.
func TestMoveToReusesJourneyAcrossTicks(t *testing.T) {
	g := open(60, 30)
	b := &fakeBody{pos: data.Position{X: 2, Y: 10}, g: g}
	mv := New()
	now := time.Unix(1000, 0)
	mv.Now = func() time.Time { return now }
	e := env(b, g)
	o := Opts{Holder: "fight", Purpose: Approach}

	if st := mv.MoveTo(e, data.Position{X: 40, Y: 10}, o); st.State != Moving || st.Mode != "journey" || !st.Issued {
		t.Fatalf("far goal: want an issued journey step, got %+v", st)
	}
	j0 := mv.trips["fight"].j
	now = now.Add(200 * time.Millisecond)
	mv.MoveTo(e, data.Position{X: 43, Y: 12}, o) // the monster walked 3 tiles
	if mv.trips["fight"].j != j0 {
		t.Fatal("a goal that drifted 3 tiles must re-goal the same journey")
	}
	if mv.trips["fight"].j.Goal != (data.Position{X: 43, Y: 12}) {
		t.Fatalf("re-goal did not move the goal: %v", mv.trips["fight"].j.Goal)
	}
	now = now.Add(200 * time.Millisecond)
	mv.MoveTo(e, data.Position{X: 55, Y: 25}, o) // a different target
	if mv.trips["fight"].j == j0 {
		t.Fatal("a goal that jumped past the regoal radius must start a fresh journey")
	}
	j1 := mv.trips["fight"].j
	mv.MoveTo(e, data.Position{X: 55, Y: 25}, Opts{Holder: "fight", Purpose: Travel})
	if mv.trips["fight"].j == j1 {
		t.Fatal("a new purpose must start a fresh journey")
	}
	j2 := mv.trips["fight"].j
	now = now.Add(idleReset + time.Second)
	mv.MoveTo(e, data.Position{X: 55, Y: 25}, Opts{Holder: "fight", Purpose: Travel})
	if mv.trips["fight"].j == j2 {
		t.Fatal("a trip idle past the reset must start fresh")
	}
	j3 := mv.trips["fight"].j
	g2 := open(61, 30) // another area: another frame
	mv.MoveTo(env(b, g2), data.Position{X: 55, Y: 25}, Opts{Holder: "fight", Purpose: Travel})
	if mv.trips["fight"].j == j3 {
		t.Fatal("a new grid frame must start a fresh journey")
	}
}

func TestReuseRule(t *testing.T) {
	g := open(10, 10)
	now := time.Unix(0, 0)
	tr := &trip{j: nil, goal: data.Position{X: 5, Y: 5}, purpose: Loot, at: now}
	if reuse(nil, g, Loot, tr.goal, now) {
		t.Fatal("no trip: nothing to reuse")
	}
	b := &fakeBody{pos: data.Position{X: 1, Y: 1}, g: g}
	mv := New()
	mv.Now = func() time.Time { return now }
	tr = mv.tripFor(env(b, g), Opts{Holder: "x", Purpose: Loot}, data.Position{X: 5, Y: 5}, 2)
	if !reuse(tr, g, Loot, data.Position{X: 5 + RegoalRadius, Y: 5}, now) {
		t.Fatal("a goal at the regoal radius is the same trip")
	}
	if reuse(tr, g, Loot, data.Position{X: 5 + RegoalRadius + 1, Y: 5}, now) {
		t.Fatal("a goal past the regoal radius is a new trip")
	}
}

// Short hop: ≤3 tiles on a clear line is ONE stride, no journey opened.
// Through a wall it is not a hop — the planner routes it.
func TestShortHopRule(t *testing.T) {
	g := fgrid(
		"..........",
		"..........",
		"....#.....",
		"....#.....",
		"....#.....",
		"..........",
	)
	b := &fakeBody{pos: data.Position{X: 1, Y: 1}, g: g}
	mv := New()
	st := mv.MoveTo(env(b, g), data.Position{X: 3, Y: 1}, Opts{Holder: "loot", Purpose: Loot, Arrive: 1})
	if st.Mode != "hop" || !st.Issued || len(b.strides) != 1 || b.strides[0] != (data.Position{X: 3, Y: 1}) {
		t.Fatalf("clear 2-tile hop: got %+v strides=%v", st, b.strides)
	}
	if _, ok := mv.trips["loot"]; ok {
		t.Fatal("a short hop must not open a journey")
	}
	b2 := &fakeBody{pos: data.Position{X: 3, Y: 3}, g: g}
	st = mv.MoveTo(env(b2, g), data.Position{X: 6, Y: 3}, Opts{Holder: "loot2", Purpose: Loot, Arrive: 1})
	if st.Mode == "hop" {
		t.Fatalf("a 3-tile line through a wall is no hop: %+v", st)
	}
	if !st.Issued {
		t.Fatalf("the planner should walk it at once: %+v", st)
	}
	for _, s := range b2.strides {
		if !g.IsWalkable(s) {
			t.Fatalf("a planned stride aimed into the wall: %v", s)
		}
	}
	// Arrived: inside the radius nothing is issued.
	b3 := &fakeBody{pos: data.Position{X: 1, Y: 1}, g: g}
	if st := mv.MoveTo(env(b3, g), data.Position{X: 2, Y: 2}, Opts{Holder: "z"}); st.State != Arrived || st.Issued {
		t.Fatalf("within the radius: want Arrived, no input, got %+v", st)
	}
}

// FLEE LATENCY: the first MoveTo of a flee issues input in the same call —
// on a clear line, around a wall (small-budget plan), and toward a goal no
// route reaches (nav's best clear step, never a line into the wall).
func TestFleeIssuesInputOnTheFirstCall(t *testing.T) {
	g := fgrid(
		"....................",
		"....................",
		"..........#.........",
		"..........#.........",
		"..........#.........",
		"..........#.........",
		"..........#.........",
		"....................",
	)
	mv := New()
	// Clear line: committed at once, no plan.
	b := &fakeBody{pos: data.Position{X: 2, Y: 4}, g: g}
	st := mv.MoveTo(env(b, g), data.Position{X: 8, Y: 4}, Opts{Holder: "flee", Purpose: Flee})
	if !st.Issued || st.Mode != "commit" || len(b.strides) != 1 {
		t.Fatalf("clear flee line: got %+v strides=%v", st, b.strides)
	}
	// Through a wall: planned (budgeted) and walked in the same call.
	b = &fakeBody{pos: data.Position{X: 8, Y: 4}, g: g}
	st = mv.MoveTo(env(b, g), data.Position{X: 14, Y: 4}, Opts{Holder: "flee2", Purpose: Flee})
	if !st.Issued || len(b.strides) != 1 {
		t.Fatalf("walled flee line: want one stride in the first call, got %+v strides=%v", st, b.strides)
	}
	if !g.IsWalkable(b.strides[0]) {
		t.Fatalf("flee stride aimed into the wall: %v", b.strides[0])
	}
}

func TestFleeFallbackWhenNoRoute(t *testing.T) {
	// The goal sits inside a sealed pocket: no route exists.
	g := fgrid(
		"....................",
		"....................",
		"..........#####.....",
		"..........#...#.....",
		"..........#...#.....",
		"..........#####.....",
		"....................",
	)
	goal := data.Position{X: 12, Y: 3}
	mv := New()
	b := &fakeBody{pos: data.Position{X: 3, Y: 3}, g: g}
	st := mv.MoveTo(env(b, g), goal, Opts{Holder: "flee", Purpose: Flee})
	if !st.Issued || st.Mode != "fallback" || st.State != Moving {
		t.Fatalf("no route: a flee must still step, got %+v", st)
	}
	step := b.strides[0]
	ng := navOf(g)
	if !ng.Walkable(step) || !ng.OpenLine(data.Position{X: 3, Y: 3}, step) {
		t.Fatalf("fallback step %v is walled or crosses a wall", step)
	}
	// A patient mover gets the honest verdict and no input.
	b2 := &fakeBody{pos: data.Position{X: 3, Y: 3}, g: g}
	st = mv.MoveTo(env(b2, g), goal, Opts{Holder: "travel", Purpose: Travel})
	if st.State != NoPath || st.Issued || len(b2.strides) != 0 {
		t.Fatalf("no route, travel: want NoPath and no input, got %+v strides=%v", st, b2.strides)
	}
	if _, ok := mv.trips["travel"]; ok {
		t.Fatal("a NoPath verdict drops the trip (the next call plans fresh)")
	}
	// ...unless it asked for the fallback.
	st = mv.MoveTo(env(b2, g), goal, Opts{Holder: "travel", Purpose: Travel, Fallback: true})
	if st.State != NoPath || !st.Issued || st.Mode != "fallback" {
		t.Fatalf("Fallback: want NoPath with a step, got %+v", st)
	}
}

// Beyond the frame: planned to the edge, then one stride off the map.
func TestGoalBeyondTheFrame(t *testing.T) {
	g := open(20, 10)
	mv := New()
	b := &fakeBody{pos: data.Position{X: 16, Y: 5}, g: g}
	goal := data.Position{X: 30, Y: 5}
	st := mv.MoveTo(env(b, g), goal, Opts{Holder: "push", Purpose: Travel})
	if st.Mode != "edge" || !st.Issued || b.strides[0] != goal {
		t.Fatalf("at the edge: want one stride at the off-frame goal, got %+v strides=%v", st, b.strides)
	}
	b = &fakeBody{pos: data.Position{X: 2, Y: 5}, g: g}
	st = mv.MoveTo(env(b, g), goal, Opts{Holder: "push2", Purpose: Travel})
	if st.Mode != "journey" || !st.Issued {
		t.Fatalf("far from the edge: want a journey to it, got %+v", st)
	}
	if tg, _ := mv.Goal("push2"); !g.IsWalkable(tg) || tg.X != 17 {
		t.Fatalf("the planning goal must be the frame-clamped point, got %v", tg)
	}
}

// Click gait: the click lands on the ROUTE (not the straight line into the
// wall); a dead click falls through to a planned stride the same tick.
func TestClickGaitClicksTheRoute(t *testing.T) {
	g := fgrid(
		"..............................",
		"..............................",
		"..........#...................",
		"..........#...................",
		"..........#...................",
		"..........#...................",
		"..........#...................",
		"..........#...................",
	)
	mv := New()
	b := &fakeBody{pos: data.Position{X: 3, Y: 7}, g: g, clickOK: true}
	goal := data.Position{X: 20, Y: 7}
	st := mv.MoveTo(env(b, g), goal, Opts{Holder: "march", Purpose: Travel, Click: true})
	if st.Mode != "click" || len(b.clicks) != 1 || len(b.strides) != 0 {
		t.Fatalf("want one click, got %+v clicks=%v strides=%v", st, b.clicks, b.strides)
	}
	ng := navOf(g)
	if c := b.clicks[0]; !ng.Walkable(c) || !ng.OpenLine(b.pos, c) {
		t.Fatalf("the click waypoint %v is not on open ground in sight", c)
	}
	b.clickOK = false
	st = mv.MoveTo(env(b, g), goal, Opts{Holder: "march", Purpose: Travel, Click: true})
	if st.Mode != "journey" || len(b.strides) != 1 {
		t.Fatalf("dead click: want the planned stride in the same call, got %+v", st)
	}
}

// Leap: the landing rides the route, not the straight line through a wall.
func TestLeapLandsAlongTheRoute(t *testing.T) {
	g := fgrid(
		"..............................",
		"..............................",
		"..........#...................",
		"..........#...................",
		"..........#...................",
		"..........#...................",
		"..........#...................",
		"..........#...................",
	)
	mv := New()
	b := &fakeBody{pos: data.Position{X: 3, Y: 7}, g: g}
	var land data.Position
	e := env(b, g)
	e.Leap = func(p data.Position) bool { land = p; return true }
	st := mv.MoveTo(e, data.Position{X: 25, Y: 7}, Opts{Holder: "march", Purpose: Travel, AllowLeap: true})
	if st.Mode != "leap" || land == (data.Position{}) {
		t.Fatalf("want a leap, got %+v", st)
	}
	ng := navOf(g)
	if !ng.Walkable(land) || !ng.OpenLine(data.Position{X: 3, Y: 7}, land) {
		t.Fatalf("leap landing %v is walled or behind a wall", land)
	}
}

// verb=move is logged on status changes only, never every tick.
func TestMoveLogsOnStatusChangeOnly(t *testing.T) {
	g := open(60, 10)
	mv := New()
	var lines []string
	mv.Log = func(l string) { lines = append(lines, l) }
	b := &fakeBody{pos: data.Position{X: 2, Y: 5}, g: g}
	for i := 0; i < 5; i++ {
		mv.MoveTo(env(b, g), data.Position{X: 50, Y: 5}, Opts{Holder: "a", Purpose: Travel})
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "verb=move purpose=travel goal=(50,5) status=moving") {
		t.Fatalf("five moving ticks: want one line, got %q", lines)
	}
	b.pos = data.Position{X: 49, Y: 5}
	mv.MoveTo(env(b, g), data.Position{X: 50, Y: 5}, Opts{Holder: "a", Purpose: Travel})
	if len(lines) != 2 || !strings.Contains(lines[1], "status=arrived") {
		t.Fatalf("arrival must speak once: %q", lines)
	}
}

func navOf(g *game.Grid) *nav.Grid {
	return nav.NewGrid(g.OffsetX, g.OffsetY, g.Width, g.Height, func(x, y int) bool {
		return g.CollisionGrid[y][x] != game.CollisionTypeNonWalkable
	})
}
