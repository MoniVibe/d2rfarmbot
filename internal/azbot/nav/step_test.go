package nav

import "testing"

func TestOpenLineTreatsOffFrameAsOpen(t *testing.T) {
	g := ParseGrid(0, 0, []string{
		".....",
		"..#..",
		".....",
	})
	if !g.OpenLine(Pos{X: 0, Y: 0}, Pos{X: 4, Y: 0}) {
		t.Fatal("clear row reads blocked")
	}
	if g.OpenLine(Pos{X: 0, Y: 1}, Pos{X: 4, Y: 1}) {
		t.Fatal("the wall at (2,1) must block the line")
	}
	// Beyond the frame is unloaded ground: open, like the fused map's unknown.
	if !g.OpenLine(Pos{X: 4, Y: 0}, Pos{X: 9, Y: 0}) {
		t.Fatal("off-frame ground must read open")
	}
}

// The flee fallback never takes a line into a wall: with the goal straight
// through a wall, it steps around it (clear line, open end) and still gains.
func TestBestStepNeverWalksIntoAWall(t *testing.T) {
	g := ParseGrid(0, 0, []string{
		"..............",
		"..............",
		"......#.......",
		"......#.......",
		"......#.......",
		"......#.......",
		"......#.......",
		"..............",
		"..............",
	})
	me, goal := Pos{X: 4, Y: 4}, Pos{X: 12, Y: 4}
	step, ok := g.BestStep(me, goal)
	if !ok {
		t.Fatal("open room: want a step")
	}
	if !g.Walkable(step) || !g.OpenLine(me, step) {
		t.Fatalf("step %v is walled or crosses a wall", step)
	}
	if fdist(step, goal) >= fdist(me, goal)+3 {
		t.Fatalf("step %v runs far from the goal %v", step, goal)
	}

	// Boxed in: nothing clear anywhere.
	box := ParseGrid(0, 0, []string{
		"###",
		"#.#",
		"###",
	})
	if _, ok := box.BestStep(Pos{X: 1, Y: 1}, Pos{X: 10, Y: 1}); ok {
		t.Fatal("boxed in: want no step")
	}
}

// Clearance matters: out of a wall-hugging spot the step prefers open room.
func TestBestStepPrefersRoom(t *testing.T) {
	g := ParseGrid(0, 0, []string{
		"###################",
		"#.................#",
		"#.................#",
		"#.................#",
		"#.................#",
		"#.................#",
		"#.................#",
		"#.................#",
		"###################",
	})
	me := Pos{X: 1, Y: 4} // against the west wall
	step, ok := g.BestStep(me, Pos{X: 1, Y: 7})
	if !ok {
		t.Fatal("want a step")
	}
	if g.Clearance(step) <= g.Clearance(me) {
		t.Fatalf("step %v (clr %d) gained no room over %v (clr %d)", step, g.Clearance(step), me, g.Clearance(me))
	}
}

func TestAlongPathFollowsTheBend(t *testing.T) {
	path := []Pos{{X: 0, Y: 0}, {X: 6, Y: 0}, {X: 6, Y: 10}}
	p, ok := AlongPath(path, 12, func(Pos) bool { return true })
	if !ok || p != (Pos{X: 6, Y: 6}) {
		t.Fatalf("12 tiles of arc = (6,6), got %v ok=%v", p, ok)
	}
	// ok rejects the far leg: the farthest accepted point wins.
	p, ok = AlongPath(path, 12, func(q Pos) bool { return q.Y == 0 })
	if !ok || p != (Pos{X: 6, Y: 0}) {
		t.Fatalf("want the corner (6,0), got %v ok=%v", p, ok)
	}
	if _, ok := AlongPath(path, 12, func(Pos) bool { return false }); ok {
		t.Fatal("nothing accepted: want ok=false")
	}
}
