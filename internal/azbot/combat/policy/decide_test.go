package policy

import (
	"strings"
	"testing"

	"github.com/hectorgimenez/koolo/internal/azbot/combat/learn"
)

func pt(x, y int) learn.Pt { return learn.Pt{X: x, Y: y} }

var learnOn = Config{Learn: true, Explore: 0.1}

func TestDecideSingleMeleeTargetIsCarnage(t *testing.T) {
	in := carnage()
	in.HPPct, in.Me, in.Target, in.Dist = 100, pt(0, 0), pt(2, 0), 2
	in.Enemies = []learn.Pt{in.Target}
	d := Decide(in, learn.NewModel(), learnOn, 0.99)
	if d.Kind != Left || d.Skill != learn.Carnage {
		t.Fatalf("single melee target: got %v (%s), want carnage; %s", d.Kind, d.Skill, d.Line())
	}
	if !(d.Melee.U > d.Leap.U) {
		t.Fatalf("utility order: u_carn=%.2f must beat u_leap=%.2f", d.Melee.U, d.Leap.U)
	}
	if d.Why != "prior" {
		t.Fatalf("no samples yet: why=%s, want prior", d.Why)
	}
}

func TestDecideTightPackLeapsIntoCentroid(t *testing.T) {
	in := pack(carnage(), 5, 6)
	in.HPPct = 100
	d := Decide(in, learn.NewModel(), learnOn, 0.99)
	if d.Kind != Leap {
		t.Fatalf("pack of 6 at 5 tiles: got %v, want leap; %s", d.Kind, d.Line())
	}
	if d.Leap.Cover != 6 {
		t.Fatalf("the landing must cover the whole pack: cover=%d", d.Leap.Cover)
	}
	// The pack's centroid is (5.17, 0.17): the landing is on it.
	if learn.Cheb(d.Aim, pt(5, 0)) > 1 {
		t.Fatalf("aim %v is not at the pack's centroid", d.Aim)
	}
	if d.Leap.C != learn.ClusterBucket(6) || d.Leap.D != learn.DistBucket(5) {
		t.Fatalf("leap bucket = %d/%d", d.Leap.C, d.Leap.D)
	}
	if !strings.Contains(d.Line(), "skill=leap bucket=4-6/4-5 ") || !strings.Contains(d.Line(), "cover=6 r=3") {
		t.Fatalf("decision line: %s", d.Line())
	}
}

func TestDecideLowManaNeverLeaps(t *testing.T) {
	in := pack(carnage(), 5, 6)
	in.HPPct, in.MPPct, in.LeapReady = 100, 8, false
	if d := Decide(in, nil, learnOn, 0); d.Kind != Left || d.Why != "forced" || d.Leap.Veto != "mana" {
		t.Fatalf("low mana: got %v why=%s veto=%q, want forced carnage", d.Kind, d.Why, d.Leap.Veto)
	}
	in.LeftBenched, in.MPPct = true, 30
	if d := Decide(in, nil, learnOn, 0); d.Kind != Swing {
		t.Fatalf("low mana, carnage benched: got %v, want double swing", d.Kind)
	}
	in.MPPct = 5
	if d := Decide(in, nil, learnOn, 0); d.Kind != Basic {
		t.Fatalf("dry, carnage benched: got %v, want basic", d.Kind)
	}
}

func TestDecideWalledNeverLeaps(t *testing.T) {
	in := pack(carnage(), 5, 6)
	in.HPPct = 100
	in.Walled = func(a, b learn.Pt) bool { return true }
	d := Decide(in, nil, learnOn, 0.99)
	if d.Kind == Leap || d.Leap.OK || !strings.Contains(d.Leap.Veto, "no landing") {
		t.Fatalf("walled: got %v leap.OK=%t veto=%q", d.Kind, d.Leap.OK, d.Leap.Veto)
	}
	// A wall on one side only: the landing moves to the open side.
	in.Enemies = append(in.Enemies, pt(-5, 0), pt(-5, 1), pt(-6, 0), pt(-4, 0), pt(-5, -1), pt(-6, 1), pt(-4, 1))
	in.Walled = func(a, b learn.Pt) bool { return b.X > 0 }
	d = Decide(in, nil, learnOn, 0.99)
	if d.Kind != Leap || d.Aim.X >= 0 {
		t.Fatalf("one wall: got %v aim %v, want a leap west", d.Kind, d.Aim)
	}
}

func TestDecideAvoidsHazardLanding(t *testing.T) {
	in := pack(carnage(), 5, 6)
	in.HPPct = 100
	in.Hazard = func(p learn.Pt) bool { return learn.Cheb(p, pt(5, 0)) <= 1 }
	d := Decide(in, nil, learnOn, 0.99)
	if d.Leap.OK && learn.Cheb(d.Leap.Aim, pt(5, 0)) <= 1 {
		t.Fatalf("landed in the hazard: %v", d.Leap.Aim)
	}
}

func TestDecideExploration(t *testing.T) {
	in := carnage()
	in.Me, in.Target, in.Dist = pt(0, 0), pt(2, 0), 2
	in.Enemies = []learn.Pt{in.Target}
	in.HPPct = 80
	if d := Decide(in, learn.NewModel(), learnOn, 0.01); d.Kind != Leap || d.Why != "explore" {
		t.Fatalf("healthy, rnd<ε: got %v why=%s, want an exploring leap", d.Kind, d.Why)
	}
	in.HPPct = 40
	if d := Decide(in, learn.NewModel(), learnOn, 0.0); d.Kind != Left || d.Why == "explore" {
		t.Fatalf("HP 40%%: got %v why=%s — never explore wounded", d.Kind, d.Why)
	}
	in.HPPct = 80
	if d := Decide(in, learn.NewModel(), Config{Learn: false, Explore: 0.1}, 0.0); d.Why == "explore" {
		t.Fatal("-combatlearn=off must never explore")
	}
	if d := Decide(in, learn.NewModel(), Config{Learn: true, Explore: 0}, 0.0); d.Why == "explore" {
		t.Fatal("-explore=0 must never explore")
	}
	if ExploreRate(0.1, 0) != 0.1 || ExploreRate(0.1, ExploreHalf) != 0.05 {
		t.Fatalf("ε decay: %v %v", ExploreRate(0.1, 0), ExploreRate(0.1, ExploreHalf))
	}
}

// The model overrides the priors: a pack where leaps have measured zero
// kills in 60 tries goes back to Carnage, and the answer says why=model.
func TestDecideModelOverridesPrior(t *testing.T) {
	in := pack(carnage(), 5, 6)
	in.HPPct = 100
	m := learn.NewModel()
	for i := 0; i < 60; i++ {
		m.Observe(learn.Sample{Skill: learn.Leap, Cluster: 6, Dist: 5, ActionSec: 0.9, Mana: 25})
	}
	d := Decide(in, m, learnOn, 0.99)
	if d.Kind != Left || d.Why != "model" {
		t.Fatalf("measured-useless leaps: got %v why=%s; %s", d.Kind, d.Why, d.Line())
	}
	// -combatlearn=off ignores the model: deterministic priors.
	if d := Decide(in, m, Config{}, 0.99); d.Kind != Leap || d.Why != "prior" {
		t.Fatalf("learning off: got %v why=%s, want the prior leap", d.Kind, d.Why)
	}
}

func TestUtilityOrdering(t *testing.T) {
	var m *learn.Model
	u := func(skill string, c, d, hp, mp int) float64 { return Utility(m.Estimate(skill, c, d), hp, mp) }
	// Leap climbs with the cluster; Carnage falls with the walk-in.
	for c := 1; c < learn.NCluster; c++ {
		if !(u(learn.Leap, c, 1, 100, 100) > u(learn.Leap, c-1, 1, 100, 100)) {
			t.Fatalf("leap utility must climb with cluster (c=%d)", c)
		}
	}
	for d := 1; d < learn.NDist; d++ {
		if !(u(learn.Carnage, 0, d, 100, 100) < u(learn.Carnage, 0, d-1, 100, 100)) {
			t.Fatalf("carnage utility must fall with the walk-in (d=%d)", d)
		}
	}
	// Scarce mana and blood cost more.
	if !(u(learn.Leap, 2, 1, 100, 30) < u(learn.Leap, 2, 1, 100, 100)) {
		t.Fatal("a thin pool must make the leap dearer")
	}
	if !(u(learn.Leap, 2, 1, 30, 100) < u(learn.Leap, 2, 1, 100, 100)) {
		t.Fatal("low life must make HP loss dearer")
	}
	// The owner's shape: melee lone target → carnage; 7+ swarm → leap.
	if !(u(learn.Carnage, 0, 0, 100, 100) > u(learn.Leap, 0, 0, 100, 100)) {
		t.Fatal("single melee target must favour carnage")
	}
	if !(u(learn.Leap, 3, 0, 100, 100) > u(learn.Carnage, 3, 0, 100, 100)) {
		t.Fatal("a 7+ swarm in melee must favour leap")
	}
}

func TestPickLeapAimGeometry(t *testing.T) {
	me := pt(0, 0)
	// Three at 4 tiles east, five at 7 tiles north, one far (12) west.
	en := []learn.Pt{pt(4, 0), pt(4, 1), pt(5, 0),
		pt(0, 7), pt(1, 7), pt(0, 8), pt(-1, 7), pt(0, 6),
		pt(-12, 0)}
	aim, cover, ok := PickLeapAim(me, en, 3, nil, nil)
	if !ok || cover != 5 || learn.Cheb(aim, pt(0, 7)) > 1 {
		t.Fatalf("the bigger pack wins: aim=%v cover=%d ok=%t", aim, cover, ok)
	}
	// A radius of 1 still finds the densest point.
	if _, cover, _ := PickLeapAim(me, en, 1, nil, nil); cover != 5 {
		t.Fatalf("r=1 cover=%d, want 5 (the plus-shaped pack)", cover)
	}
	// Out of range only: no landing.
	if _, _, ok := PickLeapAim(me, []learn.Pt{pt(-12, 0), pt(-12, 1)}, 3, nil, nil); ok {
		t.Fatal("a pack beyond LeapRange must not be a landing")
	}
	// At our feet only: no landing (a hop under LeapMinHop is no leap).
	if _, _, ok := PickLeapAim(me, []learn.Pt{pt(1, 0)}, 3, nil, nil); ok {
		t.Fatal("a landing inside LeapMinHop must be refused")
	}
	// A midpoint covers two bodies neither position covers at r=1.
	aim, cover, ok = PickLeapAim(me, []learn.Pt{pt(4, 0), pt(6, 0)}, 1, nil, nil)
	if !ok || cover != 2 || aim != pt(5, 0) {
		t.Fatalf("midpoint landing: aim=%v cover=%d", aim, cover)
	}
}
