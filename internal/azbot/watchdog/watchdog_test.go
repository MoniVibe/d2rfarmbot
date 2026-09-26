package watchdog

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time           { return c.t }
func (c *fakeClock) Add(d time.Duration)      { c.t = c.t.Add(d) }
func newClock() *fakeClock                    { return &fakeClock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)} }
func pos(x, y int) data.Position              { return data.Position{X: x, Y: y} }
func field(h string) Context                  { return Context{Holder: h, CanPortal: true} }
func sample(p data.Position, h string) Sample { return Sample{Pos: p, Holder: h} }

// run feeds a trace at tick dt for dur: at(i) gives the sample and context for
// tick i. It returns every non-healthy verdict.
func run(w *Watchdog, clk *fakeClock, dur, dt time.Duration, at func(i int) (Sample, Context)) []Verdict {
	var out []Verdict
	for i := 0; time.Duration(i)*dt < dur; i++ {
		s, c := at(i)
		w.Observe(s)
		if v := w.Check(c); v.Pathology != Healthy {
			out = append(out, v)
		}
		clk.Add(dt)
	}
	return out
}

func newDog() (*Watchdog, *fakeClock) {
	clk := newClock()
	w := New()
	w.Clock = clk
	return w, clk
}

const tick = 250 * time.Millisecond

func TestStuckPinnedHolder(t *testing.T) {
	w, clk := newDog()
	vs := run(w, clk, 25*time.Second, tick, func(int) (Sample, Context) {
		return sample(pos(5000, 4000), "advance"), field("advance")
	})
	if len(vs) == 0 {
		t.Fatal("no verdict for a holder pinned 25s")
	}
	v := vs[0]
	if v.Pathology != Stuck || v.Culprit != "advance" || v.Remedy != RemedyStride || v.BenchFor != StuckBench {
		t.Fatalf("got %+v", v)
	}
	if !strings.Contains(v.Evidence, "pinned in 0x0 box") || !strings.Contains(v.Summary(), "stuck holder=advance") {
		t.Fatalf("evidence %q", v.Evidence)
	}
	if v.Site != pos(5000, 4000) {
		t.Fatalf("site %v", v.Site)
	}
}

func TestStuckNeedsTenure(t *testing.T) {
	w, clk := newDog()
	// 30s pinned, but the holder changes every 6s: nobody owns the box long enough.
	names := []string{"advance", "loot", "explore"}
	vs := run(w, clk, 30*time.Second, tick, func(i int) (Sample, Context) {
		h := names[(i/24)%3]
		return sample(pos(10, 10), h), Context{Holder: h}
	})
	for _, v := range vs {
		if v.Pathology == Stuck || v.Pathology == Orbit {
			t.Fatalf("convicted a holder with <8s tenure: %+v", v)
		}
	}
}

func TestStationaryExemption(t *testing.T) {
	w, clk := newDog()
	enemies := []data.Position{pos(5020, 4000)}
	vs := run(w, clk, 40*time.Second, tick, func(int) (Sample, Context) {
		me := pos(5000, 4000)
		return sample(me, "fight"), Context{Holder: "fight", StationaryOK: StationaryOK("fight", me, enemies)}
	})
	if len(vs) != 0 {
		t.Fatalf("a volleying fight was convicted: %+v", vs[0])
	}
	cases := []struct {
		h    string
		en   []data.Position
		want bool
	}{
		{"fight", []data.Position{pos(28, 0)}, true},
		{"stand", []data.Position{pos(3, 3)}, true},
		{"fight", []data.Position{pos(29, 0)}, false},
		{"fight", nil, false},
		{"advance", []data.Position{pos(1, 1)}, false},
	}
	for _, c := range cases {
		if got := StationaryOK(c.h, pos(0, 0), c.en); got != c.want {
			t.Errorf("StationaryOK(%s, %v) = %v, want %v", c.h, c.en, got, c.want)
		}
	}
}

func TestOrbit(t *testing.T) {
	w, clk := newDog()
	// Pacing 0..10..0 along x at one tile per tick: box 10 wide, path ~80, net small.
	vs := run(w, clk, 25*time.Second, tick, func(i int) (Sample, Context) {
		x := i % 20
		if x > 10 {
			x = 20 - x
		}
		return sample(pos(100+x, 100), "explore"), field("explore")
	})
	if len(vs) == 0 || vs[0].Pathology != Orbit {
		t.Fatalf("want orbit, got %+v", vs)
	}
	if vs[0].Remedy != RemedyStride || vs[0].Culprit != "explore" || !strings.Contains(vs[0].Evidence, "path=") {
		t.Fatalf("got %+v", vs[0])
	}
}

func TestWalkingIsHealthy(t *testing.T) {
	w, clk := newDog()
	vs := run(w, clk, 60*time.Second, tick, func(i int) (Sample, Context) {
		return sample(pos(100+i/2, 100), "advance"), field("advance")
	})
	if len(vs) != 0 {
		t.Fatalf("a straight walk was convicted: %+v", vs[0])
	}
}

func TestThrashBenchOnlyNamesThePair(t *testing.T) {
	w, clk := newDog()
	vs := run(w, clk, 30*time.Second, tick, func(i int) (Sample, Context) {
		h := "fight"
		if (i/8)%2 == 1 { // a handover every 2s
			h = "stand"
		}
		return sample(pos(100+i/3, 100), h), Context{Holder: h}
	})
	if len(vs) == 0 || vs[0].Pathology != Thrash {
		t.Fatalf("want thrash, got %+v", vs)
	}
	v := vs[0]
	if v.Remedy != RemedyNone || v.BenchFor != ThrashBench || v.Culprit == "" {
		t.Fatalf("got %+v", v)
	}
	if !strings.Contains(v.Evidence, "fight<->stand") && !strings.Contains(v.Evidence, "stand<->fight") {
		t.Fatalf("evidence must name the pair: %q", v.Evidence)
	}
}

func TestDodgeIsABlip(t *testing.T) {
	w, clk := newDog()
	vs := run(w, clk, 30*time.Second, tick, func(i int) (Sample, Context) {
		h := "fight"
		if i%4 == 0 {
			h = "dodge" // the arrow dance
		}
		return sample(pos(100+i/3, 100), h), Context{Holder: h}
	})
	if len(vs) != 0 {
		t.Fatalf("dodge<->fight read as pathology: %+v", vs[0])
	}
}

func TestUnstickIsImmune(t *testing.T) {
	w, clk := newDog()
	vs := run(w, clk, 100*time.Second, tick, func(int) (Sample, Context) {
		return sample(pos(5, 5), "unstick"), field("unstick")
	})
	if len(vs) != 0 {
		t.Fatalf("the remedy was convicted: %+v", vs[0])
	}
}

// Four stuck verdicts in one pocket refute footwork: the fourth prescribes the portal.
func TestPocketBreakerEscalatesToPortal(t *testing.T) {
	w, clk := newDog()
	vs := run(w, clk, 120*time.Second, tick, func(int) (Sample, Context) {
		return sample(pos(700, 700), "advance"), field("advance")
	})
	if len(vs) < 4 {
		t.Fatalf("want ≥4 verdicts in 120s pinned, got %d", len(vs))
	}
	for i := 0; i < 3; i++ {
		if vs[i].Remedy != RemedyStride {
			t.Fatalf("verdict %d: want stride, got %+v", i, vs[i])
		}
	}
	if vs[3].Remedy != RemedyPortal || !strings.Contains(vs[3].Evidence, "run=4") {
		t.Fatalf("verdict 3: want portal, got %+v", vs[3])
	}
	// The portal restarted every monitor: no Pinned right behind it, and the rate
	// limit keeps later verdicts on footwork.
	for _, v := range vs[4:] {
		if v.Remedy == RemedyPortal || v.Pathology == Pinned {
			t.Fatalf("second portal inside %s: %+v", PortalEvery, v)
		}
	}
}

func TestPocketBreakerWithoutPortalStaysOnFootwork(t *testing.T) {
	w, clk := newDog()
	vs := run(w, clk, 120*time.Second, tick, func(int) (Sample, Context) {
		return sample(pos(700, 700), "advance"), Context{Holder: "advance"} // town or no tome
	})
	for _, v := range vs {
		if v.Remedy != RemedyStride || v.Pathology == Pinned {
			t.Fatalf("no portal binding, got %+v", v)
		}
	}
}

func TestCrossingHotNoFootworkLongLeash(t *testing.T) {
	w, clk := newDog()
	vs := run(w, clk, 5*time.Minute, tick, func(int) (Sample, Context) {
		c := field("advance")
		c.CrossingHot = true
		return sample(pos(300, 300), "advance"), c
	})
	portalAt := -1
	for i, v := range vs {
		if v.Pathology == Pinned {
			continue // the deadman box answers whoever holds
		}
		if v.Remedy == RemedyStride {
			t.Fatalf("footwork at a held door: %+v", v)
		}
		if v.Remedy == RemedyPortal && portalAt < 0 {
			portalAt = i
		}
	}
	if portalAt < 0 {
		t.Fatal("the long leash never ended in a portal")
	}
}

func TestPinnedDeadmanWhoeverHolds(t *testing.T) {
	w, clk := newDog()
	enemies := []data.Position{pos(10, 0)}
	var got []Verdict
	got = run(w, clk, 80*time.Second, tick, func(i int) (Sample, Context) {
		me := pos(i%3, 0) // a 2-tile shuffle: inside the 6-box
		c := Context{Holder: "fight", StationaryOK: StationaryOK("fight", me, enemies), CanPortal: true}
		return sample(me, "fight"), c
	})
	if len(got) != 1 || got[0].Pathology != Pinned || got[0].Remedy != RemedyPortal || got[0].Culprit != "" {
		t.Fatalf("want one pinned verdict, got %+v", got)
	}
	if got[0].Holder != "fight" || !strings.Contains(got[0].Evidence, "6-box") {
		t.Fatalf("evidence %+v", got[0])
	}
	// Rate limit: another 80s in the box stays quiet until PortalEvery has passed.
	more := run(w, clk, 30*time.Second, tick, func(i int) (Sample, Context) {
		me := pos(i%3, 0)
		return sample(me, "fight"), Context{Holder: "fight", StationaryOK: true, CanPortal: true}
	})
	if len(more) != 0 {
		t.Fatalf("second portal inside the rate limit: %+v", more)
	}
}

func TestPinnedNotInTownOrDead(t *testing.T) {
	for _, s := range []Sample{{Pos: pos(1, 1), InTown: true}, {Pos: pos(1, 1), Dead: true}} {
		w, clk := newDog()
		vs := run(w, clk, 100*time.Second, tick, func(int) (Sample, Context) {
			return s, Context{CanPortal: true}
		})
		if len(vs) != 0 {
			t.Fatalf("%+v: got %+v", s, vs[0])
		}
	}
}

func TestPacerShuttle(t *testing.T) {
	w, clk := newDog()
	// Two pockets 20 apart, 4s in each: every box resets, the centroid does not move.
	vs := run(w, clk, 200*time.Second, tick, func(i int) (Sample, Context) {
		x := 0
		if (i/16)%2 == 1 {
			x = 20
		}
		return sample(pos(1000+x, 1000), ""), Context{CanPortal: true}
	})
	if len(vs) != 1 || vs[0].Pathology != Pacer || vs[0].Remedy != RemedyPortal {
		t.Fatalf("want one pacer verdict, got %+v", vs)
	}
	if !strings.Contains(vs[0].Evidence, "centroid") {
		t.Fatalf("evidence %q", vs[0].Evidence)
	}
}

func TestObservationGapResets(t *testing.T) {
	w, clk := newDog()
	// 14s pinned, a 12s blackout (disengaged), 5s more: never 15s of fresh history.
	run(w, clk, 14*time.Second, tick, func(int) (Sample, Context) {
		return sample(pos(5, 5), "advance"), field("advance")
	})
	clk.Add(12 * time.Second)
	vs := run(w, clk, 5*time.Second, tick, func(int) (Sample, Context) {
		return sample(pos(5, 5), "advance"), field("advance")
	})
	if len(vs) != 0 {
		t.Fatalf("stale history convicted: %+v", vs[0])
	}
}

func TestRemedyAndPathologyNames(t *testing.T) {
	if RemedyStride.String() != "unstick-stride" || RemedyPortal.String() != "unstick-portal" || RemedyNone.String() != "none" {
		t.Fatal("remedy names are a log contract")
	}
	for p, n := range map[Pathology]string{Stuck: "stuck", Orbit: "orbit", Thrash: "thrash", Pinned: "pinned", Pacer: "pacer", Healthy: "healthy"} {
		if p.String() != n {
			t.Errorf("%d: %q", p, p.String())
		}
	}
}

// Relay R10, 09:20:43: "holder=equip pinned in 0x0 box for 19s" benched the
// cursor parker mid-Dress with an item on the cursor; the next holder's gate
// dropped it on the town floor. A holder at its panel, or any holder with an
// item on the cursor, is never convicted of stillness — and the vouched time
// does not count once the vouching ends.
func TestServiceAndCursorItemNeverConvictedOfStillness(t *testing.T) {
	for _, c := range []struct {
		name string
		ctx  Context
		town bool
	}{
		{"equip dressing in town (claims the bag)", Context{Holder: "equip", InService: true}, true},
		{"spend at the char sheet (R10 08:45:40)", Context{Holder: "spend", InService: true}, true},
		{"cursor item, town", Context{Holder: "equip", CursorItem: true}, true},
		{"cursor item, field (no pinned portal either)", Context{Holder: "loot", CursorItem: true, CanPortal: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, clk := newDog()
			vs := run(w, clk, 100*time.Second, tick, func(int) (Sample, Context) {
				return Sample{Pos: pos(5091, 5030), Holder: c.ctx.Holder, InTown: c.town}, c.ctx
			})
			for _, v := range vs {
				if v.Pathology != Thrash {
					t.Fatalf("convicted: %+v", v)
				}
			}
			// The vouching ends: the same spot needs a full fresh window.
			plain := c.ctx
			plain.InService, plain.CursorItem = false, false
			vs = run(w, clk, 7*time.Second, tick, func(int) (Sample, Context) {
				return Sample{Pos: pos(5091, 5030), Holder: c.ctx.Holder, InTown: c.town}, plain
			})
			if len(vs) != 0 {
				t.Fatalf("the vouched time counted: %+v", vs[0])
			}
		})
	}
	// Control: the same stillness unvouched is Stuck.
	w, clk := newDog()
	vs := run(w, clk, 25*time.Second, tick, func(int) (Sample, Context) {
		return Sample{Pos: pos(5091, 5030), Holder: "equip", InTown: true}, Context{Holder: "equip"}
	})
	if len(vs) == 0 || vs[0].Pathology != Stuck {
		t.Fatalf("control: want stuck, got %+v", vs)
	}
}

// R37: a pile walk with pickups landing is not an orbit.
func TestProductiveExemption(t *testing.T) {
	w, clk := newDog()
	vs := run(w, clk, 25*time.Second, tick, func(i int) (Sample, Context) {
		x := i % 20
		if x > 10 {
			x = 20 - x
		}
		c := field("loot")
		c.Productive = true
		return sample(pos(100+x, 100), "loot"), c
	})
	for _, v := range vs {
		if v.Pathology == Orbit || v.Pathology == Stuck {
			t.Fatalf("a productive loot pile walk was convicted: %+v", v)
		}
	}
}
