package nav

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// sim is a kinematic stand-in for the game: a stride walks straight toward the
// target at speed tiles per step and stops at the first cell the TRUE world blocks
// (which may hold walls the planner's grid does not know about).
type sim struct {
	g     *Grid          // planner's view
	world func(Pos) bool // truth; nil = same as g
	speed int
}

func (s sim) walkable(p Pos) bool {
	if s.world != nil {
		return s.world(p)
	}
	return s.g.Walkable(p)
}

func (s sim) stride(me, to Pos) Pos {
	cur := me
	n := 0
	walkLine(me, to, func(p Pos) bool {
		if p == me {
			return true
		}
		if !s.walkable(p) || n >= s.speed {
			return false
		}
		cur = p
		n++
		return true
	})
	return cur
}

// drive runs journey-like ticks: Replan → plan+SetPath, Move → stride.
type drive struct {
	t        *testing.T
	s        sim
	f        *Follower
	goal     Pos
	now      time.Time
	log      []string
	replans  int
	commands []Command
}

func newDrive(t *testing.T, s sim, goal Pos) *drive {
	d := &drive{t: t, s: s, goal: goal, now: t0}
	d.f = NewFollower(s.g, nil)
	d.f.OnTransition = func(from, to State, why string) {
		d.log = append(d.log, fmt.Sprintf("nav: %s -> %s (%s)", from, to, why))
	}
	return d
}

func (d *drive) plan(me Pos) bool {
	pl := d.s.g.Plan(me, d.goal, Options{})
	if !pl.Found {
		return false
	}
	d.f.SetPath(d.s.g.Simplify(pl.Path, nil), me, d.now)
	return true
}

// tick: one journey Step. Returns the command acted on and the new position.
func (d *drive) tick(me Pos) (Command, Pos) {
	d.now = d.now.Add(300 * time.Millisecond)
	for k := 0; k < 3; k++ {
		c := d.f.Step(me, d.now)
		d.commands = append(d.commands, c)
		switch c.Kind {
		case Replan:
			if c.Target != (Pos{}) {
				d.t.Fatalf("replan carries a target %v", c.Target)
			}
			d.replans++
			if !d.plan(me) {
				d.t.Fatalf("replan from %v failed", me)
			}
			continue
		case Move:
			if c.Target == (Pos{}) {
				d.t.Fatal("Move with zero target")
			}
			return c, d.s.stride(me, c.Target)
		default:
			return c, me
		}
	}
	d.t.Fatalf("replan loop at %v", me)
	return Command{}, me
}

func TestFollowerArrivesThroughDoorways(t *testing.T) {
	rows := room(60, 30)
	for y := 0; y < 30; y++ {
		if y != 22 {
			setCell(rows, 20, y, '#')
		}
		if y != 4 && y != 5 {
			setCell(rows, 40, y, '#')
		}
	}
	g := ParseGrid(offX, offY, rows)
	goal := P(55, 25)
	d := newDrive(t, sim{g: g, speed: 3}, goal)
	me := P(4, 4)
	if !d.plan(me) {
		t.Fatal("no plan")
	}
	for i := 0; i < 300; i++ {
		c, next := d.tick(me)
		if c.Kind == ArrivedCmd {
			if fdist(me, goal) > 5 {
				t.Fatalf("arrived %v far from goal", me)
			}
			if d.f.State() != Arrived {
				t.Fatalf("state %s after arrival", d.f.State())
			}
			if l := strings.Join(d.log, "\n"); strings.Contains(l, "stuck") || d.replans > 0 {
				t.Fatalf("clean run should need no stall or replan:\n%s", l)
			}
			t.Logf("arrived in %d ticks\n%s", i, strings.Join(d.log, "\n"))
			return
		}
		if c.Kind == Fail {
			t.Fatalf("failed: %s\n%s", c.Reason, strings.Join(d.log, "\n"))
		}
		if !g.Walkable(c.Target) {
			t.Fatalf("carrot %v is a wall", c.Target)
		}
		if next == me && c.Kind == Move {
			t.Logf("no movement toward %v at %v", c.Target, me)
		}
		me = next
	}
	t.Fatalf("never arrived; at %v\n%s", me, strings.Join(d.log, "\n"))
}

// A carrot is never a straight shot through a wall.
func TestCarrotIsVisible(t *testing.T) {
	rows := room(40, 30)
	for x := 0; x < 30; x++ {
		setCell(rows, x, 15, '#') // wall with the gap at the east end: a U-turn
	}
	g := ParseGrid(offX, offY, rows)
	d := newDrive(t, sim{g: g, speed: 2}, P(5, 25))
	me := P(5, 5)
	d.plan(me)
	for i := 0; i < 400; i++ {
		c, next := d.tick(me)
		if c.Kind == ArrivedCmd {
			return
		}
		if c.Kind == Move && c.Reason == "" && !d.f.visible(me, c.Target, false) {
			t.Fatalf("carrot %v not visible from %v", c.Target, me)
		}
		me = next
	}
	t.Fatalf("never arrived; at %v", me)
}

func TestDivergenceAsksForReplanNeverZeroTarget(t *testing.T) {
	g := ParseGrid(offX, offY, room(60, 40))
	f := NewFollower(g, nil)
	var log []string
	f.OnTransition = func(from, to State, why string) { log = append(log, from.String()+" -> "+to.String()+": "+why) }

	// No plan yet: Replan, never Move.
	if c := f.Step(P(5, 5), t0); c.Kind != Replan || c.Target != (Pos{}) {
		t.Fatalf("unplanned follower: got %v", c)
	}
	pl := g.Plan(P(5, 20), P(55, 20), Options{})
	f.SetPath(g.Simplify(pl.Path, nil), P(5, 20), t0)
	if c := f.Step(P(6, 20), t0.Add(time.Second)); c.Kind != Move || c.Target == (Pos{}) {
		t.Fatalf("on path: want Move with a target, got %+v", c)
	}
	// Knocked far off the path.
	c := f.Step(P(20, 37), t0.Add(2*time.Second))
	if c.Kind != Replan {
		t.Fatalf("diverged: want Replan, got %+v", c)
	}
	if f.State() != NoPlan {
		t.Fatalf("state %s", f.State())
	}
	// Until someone replans, it keeps asking — it never strides at a stale or zero target.
	for i := 0; i < 5; i++ {
		if c := f.Step(P(20, 37), t0.Add(time.Duration(3+i)*time.Second)); c.Kind != Replan {
			t.Fatalf("without a plan: want Replan, got %+v", c)
		}
	}
	if !strings.Contains(strings.Join(log, "\n"), "following -> noplan: diverged") {
		t.Fatalf("transition log missing divergence:\n%s", strings.Join(log, "\n"))
	}
}

// The old navigator's stuck clock restarted on every replan, and journey replanned
// after every blocked stride — so from one spot it never declared stuck.
func TestStuckDetectedDespiteReplans(t *testing.T) {
	g := ParseGrid(offX, offY, room(60, 30))
	d := newDrive(t, sim{g: g, speed: 0}, P(50, 15)) // speed 0: pinned in place
	me := P(5, 15)
	d.plan(me)
	for i := 0; i < 12; i++ { // 3.6s simulated
		if i%2 == 1 {
			d.plan(me) // the caller replans from the same spot, repeatedly
		}
		c, _ := d.tick(me)
		if c.Kind == Move && c.Reason != "" {
			if d.now.Sub(t0) > 3200*time.Millisecond {
				t.Fatalf("stuck detected late: %v", d.now.Sub(t0))
			}
			if !strings.Contains(strings.Join(d.log, "\n"), "following -> stuck (no progress") {
				t.Fatalf("transition log:\n%s", strings.Join(d.log, "\n"))
			}
			return
		}
	}
	t.Fatalf("never detected stuck:\n%s", strings.Join(d.log, "\n"))
}

// A fight took the actuator for 10s: the resumed journey is not "stuck".
func TestPauseDoesNotCountAsStall(t *testing.T) {
	g := ParseGrid(offX, offY, room(60, 30))
	f := NewFollower(g, nil)
	me := P(5, 15)
	pl := g.Plan(me, P(50, 15), Options{})
	f.SetPath(g.Simplify(pl.Path, nil), me, t0)
	f.Step(me, t0.Add(300*time.Millisecond))
	c := f.Step(me, t0.Add(10*time.Second))
	if c.Kind != Move || c.Reason != "" || f.State() != Following {
		t.Fatalf("after a pause: want a plain follow Move, got %+v state=%s", c, f.State())
	}
	// ...but the clock still runs once stepping resumes.
	now := t0.Add(10 * time.Second)
	for i := 0; i < 12; i++ {
		now = now.Add(300 * time.Millisecond)
		if c := f.Step(me, now); c.Reason != "" {
			return
		}
	}
	t.Fatal("stall never detected after resuming")
}

// Pinned forever: escalating, distinct escapes toward open ground, then an honest Fail.
func TestEscapeThenFail(t *testing.T) {
	rows := room(60, 30)
	g := ParseGrid(offX, offY, rows)
	d := newDrive(t, sim{g: g, speed: 0}, P(50, 15))
	me := P(3, 15) // near the west wall: escapes should head east-ish / to open ground
	d.plan(me)
	var escapes []Command
	for i := 0; i < 200; i++ {
		c, _ := d.tick(me)
		if c.Kind == Move && c.Reason != "" {
			escapes = append(escapes, c)
		}
		if c.Kind == Fail {
			if !strings.Contains(c.Reason, "escapes exhausted") {
				t.Fatalf("fail reason %q", c.Reason)
			}
			if len(escapes) != 4 {
				t.Fatalf("want 4 escapes before failing, got %d", len(escapes))
			}
			for k := 1; k < len(escapes); k++ {
				if escapes[k].HoldMs <= escapes[k-1].HoldMs {
					t.Fatalf("holds must escalate: %v", escapes)
				}
				for j := 0; j < k; j++ {
					if cheb(escapes[k].Target, escapes[j].Target) <= triedRadius {
						t.Fatalf("escape %v repeats %v", escapes[k].Target, escapes[j].Target)
					}
				}
			}
			for _, e := range escapes {
				if g.Clearance(e.Target) < 2 {
					t.Fatalf("escape %v into wall-adjacent ground", e.Target)
				}
			}
			// Sticky: Fail again while nothing moved us.
			if c2 := d.f.Step(me, d.now.Add(time.Second)); c2.Kind != Fail {
				t.Fatalf("failed follower should stay failed, got %+v", c2)
			}
			// Moved away by someone else: it may try again.
			if c3 := d.f.Step(P(20, 15), d.now.Add(2*time.Second)); c3.Kind != Replan {
				t.Fatalf("revive: got %+v", c3)
			}
			return
		}
	}
	t.Fatalf("never failed:\n%s", strings.Join(d.log, "\n"))
}

// A gate the grid does not know about: the bot walks up, stalls, escapes back,
// walks up again... The escalation memory is keyed to the stall position, so it
// ends in an honest Fail instead of looping forever.
func TestHiddenBarrierEndsInFail(t *testing.T) {
	rows := room(60, 11)
	g := ParseGrid(offX, offY, rows)
	world := func(p Pos) bool { return g.Walkable(p) && p.X != P(30, 0).X }
	d := newDrive(t, sim{g: g, world: world, speed: 3}, P(55, 5))
	me := P(4, 5)
	d.plan(me)
	for i := 0; i < 2000; i++ {
		c, next := d.tick(me)
		if c.Kind == Fail {
			t.Logf("failed after %d ticks: %s\n%s", i, c.Reason, strings.Join(d.log, "\n"))
			return
		}
		if c.Kind == ArrivedCmd {
			t.Fatal("arrived through a wall?")
		}
		me = next
	}
	t.Fatalf("looped forever at %v:\n%s", me, strings.Join(d.log[len(d.log)-20:], "\n"))
}

func TestScreenCarrotPreservesAngle(t *testing.T) {
	worst := 0.0
	for deg := 0.0; deg < 360; deg += 0.5 {
		a := deg * math.Pi / 180
		for _, r := range []float64{1, 7, 40} {
			dx, dy := r*math.Cos(a), r*math.Sin(a)
			sx, sy := ScreenCarrot(dx, dy, IsoX, IsoY, CarrotX, CarrotY)
			if math.Abs(float64(sx)) > CarrotX+0.5 || math.Abs(float64(sy)) > CarrotY+0.5 {
				t.Fatalf("carrot (%d,%d) leaves the box", sx, sy)
			}
			if math.Abs(float64(sx)) < CarrotX-1 && math.Abs(float64(sy)) < CarrotY-1 {
				t.Fatalf("carrot (%d,%d) is not on the box edge (wasted reach)", sx, sy)
			}
			// back to world: dx-dy = sx/IsoX, dx+dy = sy/IsoY
			u, v := float64(sx)/IsoX, float64(sy)/IsoY
			wx, wy := (u+v)/2, (v-u)/2
			diff := math.Abs(math.Remainder(math.Atan2(wy, wx)-a, 2*math.Pi)) * 180 / math.Pi
			worst = math.Max(worst, diff)
			if diff > 1 {
				t.Fatalf("heading %.1f° came back %.2f° off", deg, diff)
			}
		}
	}
	t.Logf("worst world-heading error %.3f°", worst)
	if x, y := ScreenCarrot(0, 0, IsoX, IsoY, CarrotX, CarrotY); x != 0 || y != 0 {
		t.Fatal("zero delta")
	}

	// The old ellipse carrot, for the record: along a world axis it bent the heading.
	dx, dy := 1.0, 0.0
	sx, sy := (dx-dy)*IsoX, (dx+dy)*IsoY
	ang := math.Atan2(sy, sx)
	ox, oy := 300*math.Cos(ang), 140*math.Sin(ang)
	u, v := ox/IsoX, oy/IsoY
	if bent := math.Abs(math.Atan2((v-u)/2, (u+v)/2)) * 180 / math.Pi; bent < 10 {
		t.Fatalf("expected the ellipse to bend a world-axis heading, got %.1f°", bent)
	}
}
