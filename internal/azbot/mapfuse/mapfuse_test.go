package mapfuse

import (
	"bytes"
	"image/png"
	"testing"
)

// layerFrom parses rows: '.' walk, '#' block, anything else unknown.
func layerFrom(offX, offY int, rows ...string) *Layer {
	l := NewLayer(offX, offY, len(rows[0]), len(rows))
	for y, r := range rows {
		for x, ch := range r {
			switch ch {
			case '.':
				l.Cells[y*l.W+x] = Walk
			case '#':
				l.Cells[y*l.W+x] = Block
			}
		}
	}
	return l
}

func TestFusePrecedence(t *testing.T) {
	// One row, one column per case:
	//   0: live walk, atlas block, prior block  -> live wins (walk)
	//   1: live block, atlas walk               -> live wins (block)
	//   2: live ?, atlas walk, prior block      -> atlas wins (walk)
	//   3: live ?, atlas block, prior walk      -> atlas wins (block)
	//   4: live ?, atlas ?, prior walk          -> prior (trusted) / unknown (untrusted)
	//   5: live ?, atlas ?, prior block         -> prior block (trusted) / unknown+wall (untrusted)
	//   6: live ?, atlas ?, prior ?             -> unknown
	live := layerFrom(100, 50, ".#?????")
	atlas := layerFrom(100, 50, "#..#???")
	prior := layerFrom(100, 50, "##?#.#?")

	want := func(f *Fused, cls ...Class) {
		t.Helper()
		for x, c := range cls {
			if got := f.ClassAt(100+x, 50); got != c {
				t.Errorf("trusted=%t col %d: got %s, want %s", f.Trust.Trusted, x, got, c)
			}
		}
	}
	trusted := Fuse(live, atlas, prior, Trust{Trusted: true})
	want(trusted, ClassLiveWalk, ClassLiveBlock, ClassAtlasWalk, ClassAtlasBlock, ClassPriorWalk, ClassPriorBlock, ClassUnknown)

	untrusted := Fuse(live, atlas, prior, Trust{Trusted: false})
	want(untrusted, ClassLiveWalk, ClassLiveBlock, ClassAtlasWalk, ClassAtlasBlock, ClassUnknown, ClassUnknownPriorWall, ClassUnknown)

	// The untrusted prior's wall is a softer penalty, never a hard block.
	if !ClassUnknownPriorWall.Walkable() || ClassUnknownPriorWall.StepCost() <= ClassUnknown.StepCost() {
		t.Errorf("untrusted prior wall must be walkable and pricier than plain unknown")
	}
	if ClassPriorBlock.Walkable() || ClassLiveBlock.Walkable() || ClassAtlasBlock.Walkable() {
		t.Errorf("blocked classes must not be walkable")
	}
	if ClassLiveWalk.StepCost() != 0 || ClassAtlasWalk.StepCost() != 0 {
		t.Errorf("observed ground must be free")
	}
	if ClassPriorBlock.Relaxed() != ClassUnknownPriorWall || ClassLiveBlock.Relaxed() != ClassLiveBlock {
		t.Errorf("relaxing touches only trusted-prior walls")
	}
	// Disagreement: col 0 (live walk vs prior block) disagrees; col 1 and col 3
	// (block vs block) agree; col 2 has no prior opinion to disagree with.
	if !untrusted.Disagree[0] || untrusted.Disagree[1] || untrusted.Disagree[2] || untrusted.Disagree[3] {
		t.Errorf("disagree flags = %v", untrusted.Disagree[:4])
	}
}

func TestFuseReadsPriorThroughShift(t *testing.T) {
	live := layerFrom(0, 0, "???")
	prior := layerFrom(0, 0, "#..") // prior displaced one cell LEFT of the world
	f := Fuse(live, nil, prior, Trust{Trusted: true, Shift: Shift{X: 1}})
	// world x=1 reads prior x=0 ('#').
	if f.ClassAt(1, 0) != ClassPriorBlock || f.ClassAt(2, 0) != ClassPriorWalk || f.ClassAt(0, 0) != ClassUnknown {
		t.Errorf("shifted prior read wrong: %v", f.Class)
	}
}

// synth builds a deterministic "level" of rooms and corridors.
func synth(w, h int) *Layer {
	l := NewLayer(1000, 2000, w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := Walk
			if x%13 < 3 || y%17 < 2 || (x*7+y*3)%29 == 0 {
				c = Block
			}
			l.Cells[y*w+x] = c
		}
	}
	return l
}

// observe copies the truth into a live layer within [x0,x1)x[y0,y1) (world-relative
// to the layer's own frame), unknown elsewhere.
func observe(truth *Layer, x0, y0, x1, y1 int) *Layer {
	l := NewLayer(truth.OffX, truth.OffY, truth.W, truth.H)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			l.Cells[y*l.W+x] = truth.Cells[y*truth.W+x]
		}
	}
	return l
}

func TestMeasureAgreeingPriorIsTrusted(t *testing.T) {
	truth := synth(120, 120)
	live := observe(truth, 10, 10, 60, 60) // 2500 observed cells
	tr := Measure(live, nil, truth)
	if !tr.Trusted || tr.Agree < 0.999 || tr.Shift != (Shift{}) || tr.N != 2500 {
		t.Fatalf("identical prior: %s", tr)
	}
}

func TestMeasureDisagreeingPriorIsNotTrusted(t *testing.T) {
	truth := synth(120, 120)
	live := observe(truth, 10, 10, 60, 60)
	// The mod rebuilt this level: the prior is a different layout (rotated pattern).
	prior := NewLayer(truth.OffX, truth.OffY, truth.W, truth.H)
	for y := 0; y < truth.H; y++ {
		for x := 0; x < truth.W; x++ {
			prior.Cells[y*truth.W+x] = truth.Cells[x*truth.W+y]
		}
	}
	tr := Measure(live, nil, prior)
	if tr.Trusted || tr.Agree >= TrustAgree {
		t.Fatalf("rebuilt level must not be trusted: %s", tr)
	}
}

func TestMeasureFindsShiftedPrior(t *testing.T) {
	truth := synth(120, 120)
	live := observe(truth, 20, 20, 80, 80)
	// The prior is the same level displaced by (+2,-1): prior cell (px,py) describes
	// world (px+2, py-1), so prior(px,py) = truth(px+2, py-1).
	prior := NewLayer(truth.OffX, truth.OffY, truth.W, truth.H)
	for y := 0; y < truth.H; y++ {
		for x := 0; x < truth.W; x++ {
			wx, wy := truth.OffX+x+2, truth.OffY+y-1
			prior.Cells[y*truth.W+x] = truth.At(wx, wy)
		}
	}
	tr := Measure(live, nil, prior)
	if !tr.Trusted || tr.Shift != (Shift{X: 2, Y: -1}) {
		t.Fatalf("shifted prior: %s, want shift (2,-1) trusted", tr)
	}
	// And fusion reads it through the shift: an unobserved cell matches the truth.
	f := Fuse(live, nil, prior, tr)
	for _, p := range []Pos{{X: truth.OffX + 100, Y: truth.OffY + 100}, {X: truth.OffX + 5, Y: truth.OffY + 90}} {
		want := ClassPriorWalk
		if truth.At(p.X, p.Y) == Block {
			want = ClassPriorBlock
		}
		if got := f.ClassAt(p.X, p.Y); got != want {
			t.Errorf("fused %v = %s, want %s", p, got, want)
		}
	}
}

func TestMeasureNeedsEnoughCells(t *testing.T) {
	truth := synth(60, 60)
	live := observe(truth, 0, 0, 15, 15) // 225 < 400
	if tr := Measure(live, nil, truth); tr.Trusted || tr.N != 225 {
		t.Fatalf("too few cells must not earn trust: %s", tr)
	}
	// The atlas counts too: earlier visits push it over the bar.
	atlas := observe(truth, 30, 30, 50, 50)
	if tr := Measure(live, atlas, truth); !tr.Trusted || tr.N != 625 {
		t.Fatalf("live+atlas: %s", tr)
	}
}

func TestMeasureWallBlindPriorIsNotTrusted(t *testing.T) {
	// Open field with a few mod-added walls: an all-walkable prior agrees ~95%
	// overall yet sees none of the walls.
	live := NewLayer(0, 0, 100, 40)
	for i := range live.Cells {
		live.Cells[i] = Walk
		if i%20 == 0 {
			live.Cells[i] = Block
		}
	}
	prior := NewLayer(0, 0, 100, 40)
	for i := range prior.Cells {
		prior.Cells[i] = Walk
	}
	tr := Measure(live, nil, prior)
	if tr.Agree < TrustAgree || tr.Trusted {
		t.Fatalf("wall-blind prior: %s — overall agreement high, must still be untrusted", tr)
	}
}

func TestTrackerRemeasuresAsRoomsStream(t *testing.T) {
	truth := synth(120, 120)
	tk := NewTracker()
	k := Key{Seed: 7, Area: 3}
	live := observe(truth, 0, 0, 30, 30)
	if _, fresh := tk.Update(k, live, nil, truth); !fresh {
		t.Fatal("first sight must measure")
	}
	if _, fresh := tk.Update(k, live, nil, truth); fresh {
		t.Fatal("nothing new streamed: no re-measure")
	}
	live = observe(truth, 0, 0, 40, 40) // +700 cells
	tr, fresh := tk.Update(k, live, nil, truth)
	if !fresh || tr.Known != 1600 {
		t.Fatalf("grown observation must re-measure: fresh=%t %s", fresh, tr)
	}
	if got, _ := tk.Get(k); got != tr {
		t.Fatalf("Get = %s", got)
	}
	if _, ok := tk.Get(Key{Seed: 7, Area: 4}); ok {
		t.Fatal("trust is per area")
	}
}

func TestRenderDrawsPlanAndDisagreement(t *testing.T) {
	live := layerFrom(0, 0,
		"....",
		".#..",
		"????")
	prior := layerFrom(0, 0,
		"#...",
		".#..",
		"..#.")
	f := Fuse(live, nil, prior, Trust{})
	img := Render(f, []Pos{{X: 0, Y: 2}, {X: 3, Y: 2}}, Pos{})
	if img.Bounds().Dx() != 4 || img.Bounds().Dy() != 3 {
		t.Fatalf("crop = %v", img.Bounds())
	}
	if img.RGBAAt(0, 0) != colDisagree {
		t.Errorf("disagreement not highlighted: %v", img.RGBAAt(0, 0))
	}
	if img.RGBAAt(1, 1) != colLiveBlock || img.RGBAAt(3, 0) != colLiveWalk {
		t.Errorf("live cells miscolored")
	}
	if img.RGBAAt(1, 2) != colPlan || img.RGBAAt(2, 2) != colPlan {
		t.Errorf("plan line missing")
	}
	var buf bytes.Buffer
	if err := WritePNG(&buf, f, nil, Pos{}); err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(&buf); err != nil {
		t.Fatal(err)
	}
}
