package coverage

import (
	"encoding/json"
	"testing"
	"time"
)

func P(x, y int) Pos { return Pos{X: x, Y: y} }

// A wall column splits the room; she stands left of it.
func TestLOSWallsBlockSight(t *testing.T) {
	ter := ParseTerrain(100, 200, []string{
		"...........",
		"...........",
		".....#.....",
		".....#.....",
		".....#.....",
		"...........",
		"...........",
	})
	m := NewFor(ter)
	me := P(101, 203) // (1,3) relative
	if n := m.Update(ter, me, 14); n == 0 {
		t.Fatal("nothing seen")
	}
	if !m.Seen(P(105, 203)) {
		t.Fatal("the blocking wall itself must be seen")
	}
	if m.Seen(P(109, 203)) || m.Seen(P(108, 203)) {
		t.Fatal("tiles straight behind the wall must stay unseen")
	}
	if !m.Seen(P(109, 200)) {
		t.Fatal("tiles around the wall's end are in sight")
	}
	if !m.Seen(P(103, 203)) || !m.Seen(me) {
		t.Fatal("open ground in front must be seen")
	}
	// Standing still is free: no second pass.
	if n := m.Update(ter, me, 14); n != 0 {
		t.Fatalf("a repeat update at the same tile marked %d", n)
	}
}

func TestLOSRadiusAndUnknownIsOpaque(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{
		"......??....",
		"............",
	})
	m := NewFor(ter)
	m.Update(ter, P(0, 0), 4)
	if m.Seen(P(5, 1)) {
		t.Fatal("beyond the sight radius")
	}
	if !m.Seen(P(4, 0)) {
		t.Fatal("radius edge must be seen")
	}
	m2 := NewFor(ter)
	m2.Update(ter, P(0, 0), 14)
	if m2.Seen(P(6, 0)) || m2.Seen(P(7, 0)) {
		t.Fatal("unstreamed ground is never marked seen")
	}
	if m2.Seen(P(9, 0)) {
		t.Fatal("unknown ground blocks the line behind it")
	}
}

// Corridor: she saw the west half; the frontier is the seam to the east half.
func TestFrontierExtraction(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{
		"##############################",
		"#............................#",
		"#............................#",
		"#............................#",
		"##############################",
	})
	m := NewFor(ter)
	m.Update(ter, P(2, 2), 8)
	cl := m.Frontier(ter, 0)
	if len(cl) != 1 {
		t.Fatalf("clusters = %d, want 1", len(cl))
	}
	c := cl[0]
	if c.Size != 3 {
		t.Fatalf("frontier size = %d, want the corridor's 3-tile cross-section", c.Size)
	}
	for _, q := range c.Cells {
		if !m.Seen(q) || ter.At(q) != Floor {
			t.Fatalf("frontier cell %v must be seen floor", q)
		}
		if m.Seen(P(q.X+1, q.Y)) {
			t.Fatalf("frontier cell %v must touch unseen ground", q)
		}
	}
	if !m.Seen(c.Centroid) || c.Centroid.Y != 2 {
		t.Fatalf("centroid %v must be the middle member", c.Centroid)
	}
	// Walls never make frontier: fully seen corridor has none.
	m.Update(ter, P(15, 2), 20)
	m.Update(ter, P(27, 2), 20)
	if cl := m.Frontier(ter, 0); len(cl) != 0 {
		t.Fatalf("fully seen corridor still has %d clusters", len(cl))
	}
	if cov := m.CoverageAll(ter); cov != 1 {
		t.Fatalf("coverage = %.2f, want 1", cov)
	}
}

func TestClusteringEightConnectedAndChunked(t *testing.T) {
	// Two separate unseen pockets → two clusters; diagonal members join.
	ter := ParseTerrain(0, 0, []string{
		"...................",
		"...................",
		"...................",
		"...................",
		"...................",
	})
	m := New(0, 0, ter.W, ter.H)
	// Seen: columns 3..15 everywhere; unseen: 0..2 and 16..18.
	for y := 0; y < ter.H; y++ {
		for x := 3; x <= 15; x++ {
			i := y*m.W + x
			m.seen.set(i)
			m.nSeen++
		}
	}
	m.ver++
	cl := m.Frontier(ter, 0)
	if len(cl) != 2 {
		t.Fatalf("clusters = %d, want 2 (west and east seams)", len(cl))
	}
	if cl[0].Size != 5 || cl[1].Size != 5 {
		t.Fatalf("sizes = %d,%d want 5,5", cl[0].Size, cl[1].Size)
	}
	if cl[0].Centroid != P(3, 2) || cl[1].Centroid != P(15, 2) {
		t.Fatalf("centroids = %v %v", cl[0].Centroid, cl[1].Centroid)
	}
	// A chunk of 2 splits each 5-tall seam into 3 pieces.
	if n := len(m.Frontier(ter, 2)); n != 6 {
		t.Fatalf("chunked clusters = %d, want 6", n)
	}
	// Diagonal-only connection still clusters (8-connected).
	d := ParseTerrain(0, 0, []string{
		"....",
		"....",
		"....",
	})
	dm := New(0, 0, 4, 3)
	for _, q := range []Pos{P(0, 0), P(1, 1), P(2, 2)} {
		dm.seen.set(q.Y*4 + q.X)
	}
	dm.ver++
	if n := len(dm.Frontier(d, 0)); n != 1 {
		t.Fatalf("diagonal chain = %d clusters, want 1", n)
	}
}

// The wall trap: a frontier 4 tiles away straight-line but 40+ by path, and
// one 12 tiles away down the open corridor. Nearest-by-PATH wins.
func TestScoringPicksNearestByPathNotStraightLine(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{
		"#########################",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#...........#...........#",
		"#.......................#",
		"#########################",
	})
	m := NewFor(ter)
	// Seen: the west room rows 1..6 and the east room rows 1..6 (she looked
	// over earlier) — frontier at row 6/7 on both sides of the wall.
	for y := 1; y <= 6; y++ {
		for x := 1; x < ter.W-1; x++ {
			if ter.At(P(x, y)) == Floor {
				m.seen.set(y*m.W + x)
			}
		}
	}
	m.ver++
	me := P(11, 2) // hugging the wall's west face
	var p Policy
	p.P = Params{SizeW: 0.01, Chunk: -1}
	pk, st, changed := p.Next(m, ter, me, Bias{}, time.Unix(0, 0))
	if st != Exploring || !changed {
		t.Fatalf("status %v changed %v", st, changed)
	}
	if pk.Goal.X > 11 {
		t.Fatalf("picked %v across the wall (straight-line bait); want the west seam", pk.Goal)
	}
	// Straight line says east is as close, but the path there is long.
	if pk.Cost > 12 {
		t.Fatalf("path cost %d, want the short west route", pk.Cost)
	}
}

func TestSmallClustersIgnoredAndBlacklist(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{
		"##########",
		"#........#",
		"#........#",
		"##########",
	})
	m := NewFor(ter)
	for y := 1; y <= 2; y++ {
		for x := 1; x <= 7; x++ {
			m.seen.set(y*m.W + x)
		}
	}
	m.ver++
	var p Policy
	// the 2-tile seam at x=7 is below MinCluster 3: nothing to explore
	if _, st, _ := p.Next(m, ter, P(2, 1), Bias{}, time.Unix(0, 0)); st != NoFrontier {
		t.Fatalf("status = %v, want no-frontier (2-cell cluster ignored)", st)
	}
	p.P.MinCluster = 2
	pk, st, _ := p.Next(m, ter, P(2, 1), Bias{}, time.Unix(0, 0))
	if st != Exploring {
		t.Fatalf("status = %v", st)
	}
	p.Fail() // planner NoPath
	if _, st, _ := p.Next(m, ter, P(2, 1), Bias{}, time.Unix(1, 0)); st != NoFrontier {
		t.Fatalf("blacklisted %v still picked (%v)", pk.Goal, st)
	}
}

func TestNoProgressBlacklistsAfter30s(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{
		"...................................",
		"...................................",
		"...................................",
	})
	m := NewFor(ter)
	m.Update(ter, P(0, 1), 8)
	var p Policy
	t0 := time.Unix(1000, 0)
	pk, _, _ := p.Next(m, ter, P(0, 1), Bias{}, t0)
	if _, _, ch := p.Next(m, ter, P(0, 1), Bias{}, t0.Add(29*time.Second)); ch {
		t.Fatal("re-picked before the 30s conviction")
	}
	_, st, _ := p.Next(m, ter, P(0, 1), Bias{}, t0.Add(31*time.Second))
	if len(p.Blacklisted()) != 1 || p.Blacklisted()[0] != pk.Goal {
		t.Fatalf("no-progress goal not blacklisted: %v", p.Blacklisted())
	}
	if st != NoFrontier {
		t.Fatalf("status after the only goal is barred = %v", st)
	}
}

func TestGoalBiasPrefersTheExitSide(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{
		"#################################",
		"#...............................#",
		"#...............................#",
		"#...............................#",
		"#################################",
	})
	m := NewFor(ter)
	m.Update(ter, P(16, 2), 6) // a seen island in the middle; frontier west and east
	var p Policy
	p.P.Chunk = -1
	west := Bias{At: P(1, 2), Key: "exit", OK: true}
	pk, _, _ := p.Next(m, ter, P(16, 2), west, time.Unix(0, 0))
	if pk.Goal.X >= 16 {
		t.Fatalf("west bias picked %v", pk.Goal)
	}
	east := Bias{At: P(31, 2), Key: "exit2", OK: true}
	pk, _, ch := p.Next(m, ter, P(16, 2), east, time.Unix(1, 0))
	if !ch || pk.Goal.X <= 16 {
		t.Fatalf("a changed bias must re-pick east, got %v changed=%v", pk.Goal, ch)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	ter := ParseTerrain(-50, 70, []string{
		"##########################",
		"#......#.................#",
		"#......#.................#",
		"#........................#",
		"##########################",
	})
	m := NewFor(ter)
	m.Update(ter, P(-48, 72), 14)
	snap := m.Snapshot()
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var back Snapshot
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	r, err := FromSnapshot(back)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Fits(ter) || r.SeenCount() != m.SeenCount() {
		t.Fatalf("restored %d seen, want %d", r.SeenCount(), m.SeenCount())
	}
	for i := range m.seen {
		if m.seen[i] != r.seen[i] || m.wall[i] != r.wall[i] {
			t.Fatal("bitsets differ after the round trip")
		}
	}
	if len(r.Frontier(ter, DefaultChunk)) != len(m.Frontier(ter, DefaultChunk)) {
		t.Fatal("frontier differs after the round trip")
	}
	if _, err := FromSnapshot(Snapshot{W: 3, H: 3, Seen: "!!", Wall: ""}); err == nil {
		t.Fatal("garbage must be an error")
	}
	if _, err := FromSnapshot(Snapshot{W: 3, H: 3, Seen: packBits(newBitset(900)), Wall: packBits(newBitset(9))}); err == nil {
		t.Fatal("a frame mismatch must be an error")
	}
	if len(b) > 400 {
		t.Fatalf("snapshot is %d bytes — not compact", len(b))
	}
}

// THE OWNER'S COMPLAINT: explore half a cave, TP to town, come back — the
// fresh process-side state (new Policy, map restored from the store) must
// send her to the UNEXPLORED half, never back over what she saw.
func TestReturnAfterTPDoesNotReExplore(t *testing.T) {
	// West cave room (explored) —3-wide passage— east cave room (not yet).
	ter := ParseTerrain(0, 0, []string{
		"##################################################",
		"#..........#######################...............#",
		"#..........#######################...............#",
		"#................................................#",
		"#................................................#",
		"#................................................#",
		"#..........#######################...............#",
		"#..........#######################...............#",
		"##################################################",
	})
	m := NewFor(ter)
	// First visit: she swept the west room and poked into the passage.
	for _, q := range []Pos{P(2, 2), P(9, 2), P(9, 6), P(2, 6), P(14, 4)} {
		m.Update(ter, q, 14)
	}
	seenBefore := m.SeenCount()
	store := m.Snapshot() // flushed on area exit (the TP)

	// Back through the portal (lands near the entrance, west): new process state.
	back, err := FromSnapshot(store)
	if err != nil {
		t.Fatal(err)
	}
	var p Policy
	me := P(2, 2) // the portal drops her back where she cast it
	if n := back.Update(ter, me, 14); n != 0 {
		t.Fatalf("standing where she already looked marked %d new tiles", n)
	}
	if back.SeenCount() != seenBefore {
		t.Fatal("restored map lost coverage")
	}
	pk, st, _ := p.Next(back, ter, me, Bias{}, time.Unix(0, 0))
	if st != Exploring {
		t.Fatalf("status %v", st)
	}
	if back.Seen(P(pk.Goal.X+1, pk.Goal.Y)) && back.Seen(P(pk.Goal.X, pk.Goal.Y+1)) &&
		back.Seen(P(pk.Goal.X-1, pk.Goal.Y)) && back.Seen(P(pk.Goal.X, pk.Goal.Y-1)) {
		t.Fatalf("goal %v is inside explored ground", pk.Goal)
	}
	if pk.Goal.X < 20 {
		t.Fatalf("goal %v is in the explored west; want the passage's unexplored east end", pk.Goal)
	}
	// And the no-memory control: a fresh map would have started over at her feet.
	fresh := NewFor(ter)
	fresh.Update(ter, me, 14)
	if fresh.SeenCount() >= seenBefore {
		t.Fatal("control: a fresh map should know less")
	}
}

func TestArrivalWithoutRevealSettlesThenBlacklists(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{
		"...........??????",
		"...........??????",
		"...........??????",
	})
	m := NewFor(ter)
	m.Update(ter, P(9, 1), 14)
	// Unknown is walkable-unseen: the seam at x=10 is frontier.
	var p Policy
	t0 := time.Unix(0, 0)
	pk, st, _ := p.Next(m, ter, P(9, 1), Bias{}, t0)
	if st != Exploring || pk.Goal.X != 10 {
		t.Fatalf("pick %v %v", pk.Goal, st)
	}
	if _, _, ch := p.Next(m, ter, P(10, 1), Bias{}, t0.Add(5*time.Second)); ch {
		t.Fatal("arrival must settle (rooms stream in), not re-pick at once")
	}
	p.Next(m, ter, P(10, 1), Bias{}, t0.Add(6*time.Second))
	_, st, _ = p.Next(m, ter, P(10, 1), Bias{}, t0.Add(15*time.Second))
	if st != NoFrontier || len(p.Blacklisted()) == 0 {
		t.Fatalf("an unresolvable seam must be blacklisted after Settle: %v %v", st, p.Blacklisted())
	}
}

func TestUnloadedWallsStayWalls(t *testing.T) {
	loaded := ParseTerrain(0, 0, []string{
		".....#....",
		".....#....",
	})
	m := NewFor(loaded)
	m.Update(loaded, P(0, 0), 14)
	unloaded := ParseTerrain(0, 0, []string{
		".....?????",
		".....?????",
	})
	if m.passable(unloaded, 5) {
		t.Fatal("a wall she saw must not resurrect as ground when its room unloads")
	}
	if !m.passable(unloaded, 7) {
		t.Fatal("never-seen unloaded ground stays optimistic")
	}
}

func TestCoverageRect(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{
		"..........",
		"..........",
	})
	m := NewFor(ter)
	m.Update(ter, P(0, 0), 4)
	if c := m.Coverage(ter, Rect{X: 0, Y: 0, W: 3, H: 2}); c != 1 {
		t.Fatalf("near rect coverage %.2f", c)
	}
	if c := m.Coverage(ter, Rect{X: 6, Y: 0, W: 4, H: 2}); c != 0 {
		t.Fatalf("far rect coverage %.2f", c)
	}
	all := m.CoverageAll(ter)
	if all <= 0 || all >= 1 {
		t.Fatalf("area coverage %.2f", all)
	}
}

func TestCoverageDoneStatus(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{"........"})
	m := NewFor(ter)
	m.Update(ter, P(0, 0), 14)
	var p Policy
	if _, st, _ := p.Next(m, ter, P(0, 0), Bias{}, time.Unix(0, 0)); st != CoverageDone {
		t.Fatalf("status %v, want covered", st)
	}
}

func TestRenderDimensions(t *testing.T) {
	ter := ParseTerrain(10, 10, []string{
		"#####",
		"#...#",
		"#####",
	})
	m := NewFor(ter)
	m.Update(ter, P(12, 11), 14)
	g := P(13, 11)
	img := Render(m, ter, m.Frontier(ter, DefaultChunk), Overlay{Me: P(12, 11), Goal: &g, Route: []Pos{P(12, 11), P(13, 11)}})
	s := RenderScale(5, 3)
	if b := img.Bounds(); b.Dx() != 5*s || b.Dy() != 3*s {
		t.Fatalf("image %dx%d, want %dx%d", b.Dx(), b.Dy(), 5*s, 3*s)
	}
	if RenderScale(1000, 10) != 1 || RenderScale(500, 10) != 2 {
		t.Fatal("render scale steps")
	}
	big := New(0, 0, 950, 20)
	if b := Render(big, nil, nil, Overlay{}).Bounds(); b.Dx() != 950 || b.Dy() != 20 {
		t.Fatalf("big image %v", b)
	}
}

func TestRebaseKeepsOverlap(t *testing.T) {
	a := ParseTerrain(0, 0, []string{"......", "......"})
	m := NewFor(a)
	m.Update(a, P(0, 0), 2)
	b := ParseTerrain(-2, 0, []string{"........", "........"})
	m.Rebase(b)
	if !m.Fits(b) || !m.Seen(P(0, 0)) || !m.Seen(P(1, 1)) || m.Seen(P(-1, 0)) {
		t.Fatal("rebase lost or invented coverage")
	}
}

func TestCostFieldNoCornerCutting(t *testing.T) {
	ter := ParseTerrain(0, 0, []string{
		".#",
		"#.",
	})
	m := NewFor(ter)
	if d := m.Costs(ter, P(0, 0), nil).Dist(P(1, 1)); d != Unreachable {
		t.Fatalf("diagonal through two walls cost %d", d)
	}
	open := ParseTerrain(0, 0, []string{"...", "...", "..."})
	om := NewFor(open)
	cf := om.Costs(open, P(0, 0), nil)
	if cf.Dist(P(2, 2)) != 28 || cf.Dist(P(2, 0)) != 20 {
		t.Fatalf("octile costs %d %d", cf.Dist(P(2, 2)), cf.Dist(P(2, 0)))
	}
	if p := cf.Path(P(2, 2)); len(p) != 3 || p[0] != P(0, 0) || p[2] != P(2, 2) {
		t.Fatalf("path %v", p)
	}
}
