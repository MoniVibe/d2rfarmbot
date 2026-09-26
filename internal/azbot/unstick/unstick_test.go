package unstick

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/nav"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time      { return c.t }
func (c *clock) Add(d time.Duration) { c.t = c.t.Add(d) }

type rig struct {
	m     *Machine
	clk   *clock
	held  time.Duration
	lines []string
}

func newRig() *rig {
	r := &rig{clk: &clock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}}
	r.m = New(r.clk, func(l string) { r.lines = append(r.lines, l) })
	return r
}

// step advances time (held and wall) by d, then steps.
func (r *rig) step(d time.Duration, o Obs) Decision {
	r.clk.Add(d)
	r.held += d
	o.Held = r.held
	return r.m.Step(o)
}

func stuckRx(site data.Position) Rx {
	return Rx{Kind: "stuck", Site: site, Area: 3, Evidence: "holder=advance pinned in 0x0 box for 19s"}
}

// A dead-end corridor (y 17..23) closed at x=17 and open to the east: the only
// long clear shot from (20,20) is east.
func pocketGrid() *nav.Grid {
	rows := []string{}
	for y := 0; y < 41; y++ {
		row := []byte(strings.Repeat("#", 61))
		if y >= 17 && y <= 23 {
			for x := 18; x < 61; x++ {
				row[x] = '.'
			}
		}
		rows = append(rows, string(row))
	}
	return nav.ParseGrid(0, 0, rows)
}

func TestStrideDisplacedIsDone(t *testing.T) {
	r := newRig()
	site := data.Position{X: 20, Y: 20}
	r.m.Start(stuckRx(site))
	if r.m.Phase() != Stride {
		t.Fatalf("stride rx starts in %s", r.m.Phase())
	}
	d := r.step(40*time.Millisecond, Obs{Pos: site, Area: 3, Grid: pocketGrid()})
	if d.Act.Kind != ActStride || !d.Act.Planned || d.Act.To.X <= site.X || d.Act.To.Y != site.Y {
		t.Fatalf("want a planned stride east, got %+v", d.Act)
	}
	if d.Act.Hold != StrideHold || d.Act.MinGain != StrideGain {
		t.Fatalf("stride shape %+v", d.Act)
	}
	d = r.step(StrideHold, Obs{Pos: data.Position{X: 38, Y: 20}, Area: 3})
	if d.V != phase.Done || d.Why != phase.Completed || !strings.Contains(d.Evidence, "displaced 18") {
		t.Fatalf("got %+v", d)
	}
	joined := strings.Join(r.lines, "\n")
	for _, want := range []string{"act=unstick from=Idle to=Stride", "to=VerifyDisplacement", "to=Done"} {
		if !strings.Contains(joined, want) {
			t.Errorf("phase log missing %q:\n%s", want, joined)
		}
	}
}

func TestStrideRefutedAfterThreeNovelBearings(t *testing.T) {
	r := newRig()
	site := data.Position{X: 100, Y: 100}
	r.m.Start(stuckRx(site))
	var bearings []int
	for i := 0; i < 10; i++ {
		d := r.step(40*time.Millisecond, Obs{Pos: site, Area: 3})
		if d.V.Terminal() {
			if d.V != phase.Abandoned || d.Why != phase.Unreachable || !strings.Contains(d.Evidence, "footwork refuted") {
				t.Fatalf("got %+v", d)
			}
			break
		}
		if d.Act.Kind == ActStride {
			if d.Act.Planned {
				t.Fatal("no grid: the stride must be blind")
			}
			bearings = append(bearings, r.m.Tried()[len(r.m.Tried())-1])
			r.step(StrideHold, Obs{Pos: site, Area: 3}) // verify: not displaced
		}
	}
	if len(bearings) != MaxStrides {
		t.Fatalf("strides = %v", bearings)
	}
	seen := map[int]bool{}
	for _, b := range bearings {
		if seen[b] {
			t.Fatalf("bearing repeated: %v", bearings)
		}
		seen[b] = true
	}
}

// The next prescription at the same pocket starts from bearings not yet tried.
func TestTriedBearingsSurviveAtTheSamePocket(t *testing.T) {
	r := newRig()
	site := data.Position{X: 20, Y: 20}
	g := pocketGrid()
	r.m.Start(stuckRx(site))
	d := r.step(40*time.Millisecond, Obs{Pos: site, Area: 3, Grid: g})
	first := d.Act.To
	r.step(StrideHold, Obs{Pos: data.Position{X: 30, Y: 20}, Area: 3}) // done
	r.m.Start(stuckRx(data.Position{X: 22, Y: 20}))
	r.held = 0
	d = r.step(40*time.Millisecond, Obs{Pos: data.Position{X: 22, Y: 20}, Area: 3, Grid: g})
	if d.Act.Kind != ActStride || d.Act.To == first || len(r.m.Tried()) != 2 {
		t.Fatalf("second episode repeated the first bearing: %+v tried=%v", d.Act, r.m.Tried())
	}
	// A far pocket forgets.
	r.m.Start(stuckRx(data.Position{X: 500, Y: 500}))
	if len(r.m.Tried()) != 0 {
		t.Fatalf("far pocket kept %v", r.m.Tried())
	}
}

func portalRx() Rx {
	return Rx{Kind: "pinned", Portal: true, Site: data.Position{X: 50, Y: 50}, Area: 3}
}

func TestPortalRideHome(t *testing.T) {
	r := newRig()
	r.m.Start(portalRx())
	if r.m.Phase() != CastTP {
		t.Fatalf("portal rx starts in %s", r.m.Phase())
	}
	here := data.Position{X: 50, Y: 50}
	d := r.step(40*time.Millisecond, Obs{Pos: here, Area: 3, CanPortal: true})
	if d.Act.Kind != ActCast || !d.Act.First {
		t.Fatalf("want first cast, got %+v", d)
	}
	d = r.step(300*time.Millisecond, Obs{Pos: here, Area: 3, CanPortal: true})
	if d.V != phase.Wait || d.WakeAt.IsZero() || r.m.Phase() != AwaitPortal {
		t.Fatalf("want wait for the portal, got %+v in %s", d, r.m.Phase())
	}
	tp := Portal{ID: 77, Pos: data.Position{X: 52, Y: 51}}
	r.step(300*time.Millisecond, Obs{Pos: here, Area: 3, CanPortal: true, Portals: []Portal{tp}})
	if r.m.Phase() != EnterPortal {
		t.Fatalf("portal seen, phase %s", r.m.Phase())
	}
	d = r.step(40*time.Millisecond, Obs{Pos: here, Area: 3, CanPortal: true, Portals: []Portal{tp}})
	if d.Act.Kind != ActEnter || d.Act.Portal != tp || r.m.Phase() != AwaitArea {
		t.Fatalf("want enter, got %+v in %s", d, r.m.Phase())
	}
	d = r.step(4*time.Second, Obs{Pos: data.Position{X: 5000, Y: 5000}, Area: 1, InTown: true})
	if d.V != phase.Done || !strings.Contains(d.Evidence, "area 3 -> 1") {
		t.Fatalf("got %+v", d)
	}
}

func TestPortalStandingIsUsed(t *testing.T) {
	r := newRig()
	r.m.Start(portalRx())
	tp := Portal{ID: 9, Pos: data.Position{X: 55, Y: 50}}
	d := r.step(40*time.Millisecond, Obs{Pos: data.Position{X: 50, Y: 50}, Area: 3, CanPortal: true, Portals: []Portal{tp}})
	if d.Act.Kind != ActNone || r.m.Phase() != EnterPortal {
		t.Fatalf("a standing portal must be entered, not recast: %+v %s", d, r.m.Phase())
	}
}

func TestNoPortalAfterTwoCastsIsRefused(t *testing.T) {
	r := newRig()
	r.m.Start(portalRx())
	here := Obs{Pos: data.Position{X: 50, Y: 50}, Area: 3, CanPortal: true}
	casts := 0
	for i := 0; i < 40; i++ {
		d := r.step(200*time.Millisecond, here)
		if d.Act.Kind == ActCast {
			casts++
			if d.Act.First != (casts == 1) {
				t.Fatalf("cast %d First=%v", casts, d.Act.First)
			}
		}
		if d.V.Terminal() {
			if d.V != phase.Abandoned || d.Why != phase.Refused || casts != MaxCasts {
				t.Fatalf("got %+v after %d casts", d, casts)
			}
			return
		}
	}
	t.Fatal("never gave up on an empty tome")
}

func TestDeafPortalIsAbandoned(t *testing.T) {
	r := newRig()
	r.m.Start(portalRx())
	tp := Portal{ID: 5, Pos: data.Position{X: 51, Y: 50}}
	here := Obs{Pos: data.Position{X: 50, Y: 50}, Area: 3, CanPortal: true, Portals: []Portal{tp}}
	enters := 0
	for i := 0; i < 200; i++ {
		d := r.step(250*time.Millisecond, here)
		if d.Act.Kind == ActEnter {
			enters++
		}
		if d.V.Terminal() {
			if d.V != phase.Abandoned || d.Why != phase.Deaf || enters != MaxEnters {
				t.Fatalf("got %+v after %d clicks", d, enters)
			}
			return
		}
	}
	t.Fatal("clicked a dead portal forever")
}

func TestNoBindingIsPrecondition(t *testing.T) {
	r := newRig()
	r.m.Start(portalRx())
	d := r.step(40*time.Millisecond, Obs{Pos: data.Position{X: 50, Y: 50}, Area: 3})
	if d.V != phase.Abandoned || d.Why != phase.Precondition {
		t.Fatalf("got %+v", d)
	}
}

func TestPhaseBudgetIsTimebox(t *testing.T) {
	r := newRig()
	r.m.Start(portalRx())
	here := Obs{Pos: data.Position{X: 50, Y: 50}, Area: 3, CanPortal: true}
	r.step(40*time.Millisecond, here) // cast → AwaitPortal
	// A verb that blocked far past the phase's budget: held time says overrun.
	d := r.step(PortalWait+3*time.Second, here)
	if d.V != phase.Abandoned || d.Why != phase.Timebox || !strings.Contains(d.Evidence, "AwaitPortal") {
		t.Fatalf("got %+v", d)
	}
}

func TestUpgradeMidStride(t *testing.T) {
	r := newRig()
	r.m.Start(stuckRx(data.Position{X: 50, Y: 50}))
	if !r.m.Upgrade(portalRx()) || r.m.Phase() != CastTP || !r.m.Rx().Portal {
		t.Fatalf("upgrade failed: %s", r.m.Phase())
	}
	if r.m.Upgrade(portalRx()) {
		t.Fatal("upgrading a portal episode again")
	}
	if r.m.Rx().Site != (data.Position{X: 50, Y: 50}) {
		t.Fatal("upgrade moved the site")
	}
}

func TestLeavingTheAreaCountsAsDisplaced(t *testing.T) {
	r := newRig()
	r.m.Start(stuckRx(data.Position{X: 50, Y: 50}))
	r.step(40*time.Millisecond, Obs{Pos: data.Position{X: 50, Y: 50}, Area: 3})
	d := r.step(StrideHold, Obs{Pos: data.Position{X: 51, Y: 50}, Area: 4})
	if d.V != phase.Done {
		t.Fatalf("got %+v", d)
	}
}
