package nav

import (
	"math/rand"
	"strings"
	"testing"
)

const offX, offY = 5000, 3000

func P(x, y int) Pos { return Pos{X: x + offX, Y: y + offY} }

func room(w, h int) []string {
	rows := make([]string, h)
	for y := range rows {
		if y == 0 || y == h-1 {
			rows[y] = strings.Repeat("#", w)
		} else {
			rows[y] = "#" + strings.Repeat(".", w-2) + "#"
		}
	}
	return rows
}

func setCell(rows []string, x, y int, c byte) {
	b := []byte(rows[y])
	b[x] = c
	rows[y] = string(b)
}

// checkPath: every step is 8-adjacent, enters only walkable cells, never cuts a corner.
func checkPath(t *testing.T, g *Grid, path []Pos) {
	t.Helper()
	for i, p := range path {
		if i == 0 {
			continue
		}
		if !g.Walkable(p) {
			t.Fatalf("path cell %d %v is a wall", i, p)
		}
		q := path[i-1]
		if cheb(p, q) != 1 && !(i == 1 && !g.Walkable(q)) {
			t.Fatalf("path jump %v -> %v", q, p)
		}
		if p.X != q.X && p.Y != q.Y && g.Walkable(q) {
			if !g.Walkable(Pos{X: p.X, Y: q.Y}) || !g.Walkable(Pos{X: q.X, Y: p.Y}) {
				t.Fatalf("diagonal %v -> %v cuts a wall corner", q, p)
			}
		}
	}
}

func checkSegments(t *testing.T, g *Grid, pts []Pos) {
	t.Helper()
	for i := 1; i < len(pts); i++ {
		a := pts[i-1]
		if !walkLine(a, pts[i], func(p Pos) bool { return p == a || g.Walkable(p) }) {
			t.Fatalf("simplified segment %v -> %v crosses a wall", a, pts[i])
		}
	}
}

func TestClearanceField(t *testing.T) {
	g := ParseGrid(offX, offY, []string{
		".....",
		".....",
		"..#..",
		".....",
		".....",
	})
	cases := map[Pos]int{P(2, 2): 0, P(1, 1): 1, P(0, 0): 1, P(2, 0): 1, P(-1, 0): 0, P(9, 9): 0}
	for p, want := range cases {
		if got := g.Clearance(p); got != want {
			t.Errorf("clearance %v = %d want %d", p, got, want)
		}
	}
	g = ParseGrid(offX, offY, room(11, 11))
	if c := g.Clearance(P(5, 5)); c != 5 {
		t.Fatalf("room centre clearance %d want 5", c)
	}
}

// Randomised mazes: whatever the planner returns never touches a wall, and a
// path is found exactly when a flood fill says the goal is reachable.
func TestPlanNeverCrossesWalls(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 60; trial++ {
		w, h := 40, 30
		rows := room(w, h)
		for k := 0; k < 250; k++ {
			setCell(rows, 1+rng.Intn(w-2), 1+rng.Intn(h-2), '#')
		}
		for k := 0; k < 4; k++ { // a few long walls
			x, y := 1+rng.Intn(w-2), 1+rng.Intn(h-2)
			for d := 0; d < 15; d++ {
				if k%2 == 0 && x+d < w-1 {
					setCell(rows, x+d, y, '#')
				} else if y+d < h-1 {
					setCell(rows, x, y+d, '#')
				}
			}
		}
		setCell(rows, 2, 2, '.')
		setCell(rows, w-3, h-3, '.')
		g := ParseGrid(offX, offY, rows)
		pl := g.Plan(P(2, 2), P(w-3, h-3), Options{})
		if pl.Found != reachable(g, P(2, 2), P(w-3, h-3)) {
			t.Fatalf("trial %d: found=%v but flood-fill disagrees (%s)", trial, pl.Found, pl.Reason)
		}
		if !pl.Found {
			continue
		}
		checkPath(t, g, pl.Path)
		checkSegments(t, g, g.Simplify(pl.Path, nil))
	}
}

// reachable: 8-connected flood fill with the same no-corner-cut rule.
func reachable(g *Grid, a, b Pos) bool {
	seen := map[Pos]bool{a: true}
	q := []Pos{a}
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		if p == b {
			return true
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				n := Pos{X: p.X + dx, Y: p.Y + dy}
				if seen[n] || !g.Walkable(n) {
					continue
				}
				if dx != 0 && dy != 0 && (!g.Walkable(Pos{X: n.X, Y: p.Y}) || !g.Walkable(Pos{X: p.X, Y: n.Y})) {
					continue
				}
				seen[n] = true
				q = append(q, n)
			}
		}
	}
	return false
}

func TestUnreachableIsNoPath(t *testing.T) {
	rows := room(30, 12)
	for y := 0; y < 12; y++ {
		setCell(rows, 15, y, '#')
	}
	g := ParseGrid(offX, offY, rows)
	pl := g.Plan(P(3, 5), P(26, 5), Options{})
	if pl.Found || pl.Reason == "" {
		t.Fatalf("sealed wall: want honest NoPath, got found=%v path=%v", pl.Found, pl.Path)
	}
	// Diagonal-only gap: two walls touching at a corner are sealed (no corner cutting).
	g = ParseGrid(offX, offY, []string{
		"#####",
		"#.#.#",
		"##.##",
		"#.#.#",
		"#####",
	})
	if pl := g.Plan(P(1, 1), P(3, 3), Options{}); pl.Found {
		t.Fatalf("corner-only connection must not route: %v", pl.Path)
	}
}

func TestOneWideDoorwayRoutes(t *testing.T) {
	rows := room(40, 20)
	for y := 0; y < 20; y++ {
		if y != 10 {
			setCell(rows, 20, y, '#')
		}
	}
	g := ParseGrid(offX, offY, rows)
	pl := g.Plan(P(5, 4), P(35, 16), Options{})
	if !pl.Found {
		t.Fatalf("1-wide doorway must route: %s", pl.Reason)
	}
	checkPath(t, g, pl.Path)
	through := false
	for _, p := range pl.Path {
		if p == P(20, 10) {
			through = true
		}
	}
	if !through {
		t.Fatal("route did not use the doorway")
	}
	checkSegments(t, g, g.Simplify(pl.Path, nil))

	// A 1-wide corridor several tiles long.
	rows = room(40, 20)
	for x := 14; x <= 26; x++ {
		for y := 0; y < 20; y++ {
			if y != 9 {
				setCell(rows, x, y, '#')
			}
		}
	}
	g = ParseGrid(offX, offY, rows)
	if pl := g.Plan(P(5, 9), P(35, 9), Options{}); !pl.Found {
		t.Fatalf("1-wide corridor must route: %s", pl.Reason)
	}
}

// In a wide room the route (raw and simplified) keeps off the walls, even when
// start and goal sit near the same wall — the straight line along it is cheaper
// in distance but the clearance cost pulls the route out.
func TestWideRoomKeepsClearance(t *testing.T) {
	g := ParseGrid(offX, offY, room(60, 24))
	start, goal := P(4, 3), P(55, 3) // both at clearance 3 near the north wall
	pl := g.Plan(start, goal, Options{})
	if !pl.Found {
		t.Fatal(pl.Reason)
	}
	for _, p := range pl.Path {
		if g.Clearance(p) < 2 {
			t.Fatalf("raw path hugs a wall at %v (clearance %d)", p, g.Clearance(p))
		}
	}
	sp := g.Simplify(pl.Path, nil)
	for i := 1; i < len(sp); i++ {
		walkLine(sp[i-1], sp[i], func(p Pos) bool {
			if g.Clearance(p) < 2 {
				t.Fatalf("simplified segment %v->%v passes clearance %d at %v", sp[i-1], sp[i], g.Clearance(p), p)
			}
			return true
		})
	}
	if len(sp) > 6 {
		t.Fatalf("open-room route should be a few legs, got %d: %v", len(sp), sp)
	}
}

// A shortcut must never pass closer to walls than the stretch it replaces.
func TestSimplifyNeverLowersClearance(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for trial := 0; trial < 40; trial++ {
		rows := room(50, 30)
		for k := 0; k < 12; k++ {
			x, y := 2+rng.Intn(44), 2+rng.Intn(24)
			for dx := 0; dx < 3; dx++ {
				for dy := 0; dy < 3; dy++ {
					setCell(rows, x+dx, y+dy, '#')
				}
			}
		}
		setCell(rows, 2, 2, '.')
		setCell(rows, 47, 27, '.')
		g := ParseGrid(offX, offY, rows)
		pl := g.Plan(P(2, 2), P(47, 27), Options{})
		if !pl.Found {
			continue
		}
		sp := g.Simplify(pl.Path, nil)
		// map simplified vertices back to raw indices
		k := 0
		for i := 1; i < len(sp); i++ {
			a := k
			for pl.Path[k] != sp[i] {
				k++
			}
			minRaw := 1 << 30
			for j := a; j <= k; j++ {
				minRaw = minInt(minRaw, g.Clearance(pl.Path[j]))
			}
			if k == a+1 {
				continue // an unshortened raw step
			}
			need := minInt(minRaw, simplifyClearCap)
			walkLine(sp[i-1], sp[i], func(p Pos) bool {
				if g.Clearance(p) < need {
					t.Fatalf("trial %d: shortcut %v->%v at %v clearance %d < raw stretch %d", trial, sp[i-1], sp[i], p, g.Clearance(p), need)
				}
				return true
			})
		}
	}
}

func TestGoalInWallSnaps(t *testing.T) {
	rows := room(30, 20)
	for x := 10; x < 15; x++ {
		for y := 8; y < 12; y++ {
			setCell(rows, x, y, '#')
		}
	}
	g := ParseGrid(offX, offY, rows)
	pl := g.Plan(P(3, 3), P(12, 8), Options{}) // goal on the pillar's edge
	if !pl.Found || !pl.Snapped {
		t.Fatalf("walled goal should snap: found=%v snapped=%v %s", pl.Found, pl.Snapped, pl.Reason)
	}
	if !g.Walkable(pl.Goal) || cheb(pl.Goal, P(12, 8)) > 4 || pl.Path[len(pl.Path)-1] != pl.Goal {
		t.Fatalf("bad snapped goal %v", pl.Goal)
	}
	// Deep inside a big wall block: nothing within the snap radius.
	rows = room(40, 40)
	for x := 10; x < 30; x++ {
		for y := 10; y < 30; y++ {
			setCell(rows, x, y, '#')
		}
	}
	g = ParseGrid(offX, offY, rows)
	if pl := g.Plan(P(3, 3), P(20, 20), Options{}); pl.Found {
		t.Fatal("goal deep in a wall must be NoPath")
	}
}

func TestStartInWallStepsOut(t *testing.T) {
	rows := room(30, 20)
	for y := 0; y < 20; y++ {
		setCell(rows, 10, y, '#')
	}
	g := ParseGrid(offX, offY, rows)
	pl := g.Plan(P(10, 5), P(25, 15), Options{}) // standing "in" the wall (stale read)
	if !pl.Found {
		t.Fatal(pl.Reason)
	}
	if pl.Path[0] != P(10, 5) || pl.Path[1].X != P(11, 0).X {
		t.Fatalf("should step out to the goal side: %v", pl.Path[:3])
	}
	checkPath(t, g, pl.Path)
}

func TestBudgetIsHonestNoPath(t *testing.T) {
	g := ParseGrid(offX, offY, room(200, 200))
	pl := g.Plan(P(2, 2), P(197, 197), Options{MaxExpand: 50})
	if pl.Found || !strings.Contains(pl.Reason, "budget") {
		t.Fatalf("budget: found=%v reason=%q", pl.Found, pl.Reason)
	}
}

func TestPlanDeterministic(t *testing.T) {
	rows := room(50, 30)
	for x := 10; x < 40; x += 7 {
		for y := 3; y < 27; y++ {
			if y%9 != 0 {
				setCell(rows, x, y, '#')
			}
		}
	}
	g := ParseGrid(offX, offY, rows)
	a := g.Plan(P(2, 2), P(47, 27), Options{})
	for i := 0; i < 5; i++ {
		b := g.Plan(P(2, 2), P(47, 27), Options{})
		if len(a.Path) != len(b.Path) {
			t.Fatal("nondeterministic length")
		}
		for k := range a.Path {
			if a.Path[k] != b.Path[k] {
				t.Fatal("nondeterministic path")
			}
		}
	}
}

func TestObstacleIsAvoidedWhenThereIsRoom(t *testing.T) {
	g := ParseGrid(offX, offY, room(40, 30))
	obs := []Obstacle{{At: P(20, 15), Radius: 3, Penalty: 40}}
	pl := g.Plan(P(5, 15), P(35, 15), Options{Obstacles: obs})
	if !pl.Found {
		t.Fatal(pl.Reason)
	}
	for _, p := range pl.Path {
		if cheb(p, P(20, 15)) <= 3 {
			t.Fatalf("route passes through the obstacle bubble at %v", p)
		}
	}
	for i, p := range g.Simplify(pl.Path, obs) {
		if i > 0 {
			walkLine(g.Simplify(pl.Path, obs)[i-1], p, func(q Pos) bool {
				if cheb(q, P(20, 15)) <= 3 {
					t.Fatalf("simplified route cuts through the obstacle at %v", q)
				}
				return true
			})
		}
	}
}
