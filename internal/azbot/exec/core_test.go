package exec

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time       { return c.t }
func (c *clock) Tick(d time.Duration) { c.t = c.t.Add(d) }

// ctx is the fake executive context: every lifecycle call is recorded on it.
type ctx struct{ calls []string }

type fake struct{ name string }

func (f fake) Name() string { return f.name }
func (f fake) Begin(c *ctx, resumed bool) {
	c.calls = append(c.calls, fmt.Sprintf("%s.begin(resumed=%v)", f.name, resumed))
}
func (f fake) Suspend(c *ctx, why phase.Reason) {
	c.calls = append(c.calls, fmt.Sprintf("%s.suspend(%s)", f.name, why))
}
func (f fake) End(c *ctx, v phase.Verdict, why phase.Reason) {
	c.calls = append(c.calls, fmt.Sprintf("%s.end(%s/%s)", f.name, v, why))
}

func newCore() (*Core[*ctx], *clock, *ctx, *[]string) {
	cl := &clock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	var lines []string
	c := &Core[*ctx]{
		Arb:   &arbiter.Arbiter{Clock: cl},
		Find:  func(who string) Life[*ctx] { return fake{who} },
		Trace: func(l string) { lines = append(lines, l) },
	}
	return c, cl, &ctx{}, &lines
}

var (
	fight = arbiter.Demand{Who: "fight", Class: arbiter.ClassFight, Urgency: 1, Order: 7}
	flee  = arbiter.Demand{Who: "flee", Class: arbiter.ClassSurvive, Urgency: 1, Order: 2}
	loot  = arbiter.Demand{Who: "loot", Class: arbiter.ClassLoot, Urgency: 1, Order: 8}
)

func step(c *Core[*ctx], x *ctx, ds ...arbiter.Demand) arbiter.Change {
	c.Tick++
	_, ch := c.Decide(x, ds)
	return ch
}

func want(t *testing.T, x *ctx, calls ...string) {
	t.Helper()
	if calls == nil {
		calls = []string{}
	}
	got := x.calls
	if got == nil {
		got = []string{}
	}
	if !reflect.DeepEqual(got, calls) {
		t.Fatalf("calls:\n got %q\nwant %q", got, calls)
	}
	x.calls = nil
}

func TestFreshBegins(t *testing.T) {
	c, _, x, lines := newCore()
	step(c, x, fight)
	want(t, x, "fight.begin(resumed=false)")
	step(c, x, fight)
	want(t, x) // keep: silent
	if len(*lines) != 1 || !strings.HasPrefix((*lines)[0], `T L=grant from=- to=fight why="fresh: no holder" tick=1`) {
		t.Fatalf("trace: %q", *lines)
	}
}

func TestPreemptSuspendsAndResumes(t *testing.T) {
	c, cl, x, lines := newCore()
	step(c, x, fight)
	want(t, x, "fight.begin(resumed=false)")
	cl.Tick(2 * time.Second)
	step(c, x, fight, flee)
	want(t, x, "fight.suspend(preempted)", "flee.begin(resumed=false)")
	cl.Tick(time.Second)
	// flee withdraws: rule zero releases it; fight comes back with its ledger.
	step(c, x, fight)
	want(t, x, "flee.end(abandoned/no-target)", "fight.begin(resumed=true)")
	if got := c.Arb.Held("fight"); got != 2*time.Second {
		t.Fatalf("fight held %v, want 2s (preempted time must not count)", got)
	}
	if got := (*lines)[1]; got != `T L=grant from=fight to=flee why="preempt: survive > fight" tick=2` {
		t.Fatalf("preempt line: %q", got)
	}
}

func TestSuspendedWithdrawnIsEnded(t *testing.T) {
	c, _, x, lines := newCore()
	step(c, x, fight)
	step(c, x, fight, flee)
	x.calls = nil
	step(c, x, flee) // fight stopped bidding while suspended
	want(t, x, "fight.end(abandoned/no-target)")
	last := (*lines)[len(*lines)-1]
	if !strings.HasPrefix(last, `T L=life act=fight ev=end why="abandoned/no-target: withdrawn while suspended"`) {
		t.Fatalf("life line: %q", last)
	}
	// A later bid is a brand-new episode.
	step(c, x) // flee ends too
	step(c, x, fight)
	want(t, x, "flee.end(abandoned/no-target)", "fight.begin(resumed=false)")
}

func TestOutbid(t *testing.T) {
	c, cl, x, _ := newCore()
	a := arbiter.Demand{Who: "fence", Class: arbiter.ClassService, Urgency: 1}
	b := arbiter.Demand{Who: "repair", Class: arbiter.ClassService, Urgency: 3}
	step(c, x, a)
	cl.Tick(time.Second)
	step(c, x, a, b)
	want(t, x, "fence.begin(resumed=false)", "fence.suspend(outbid)", "repair.begin(resumed=false)")
}

func TestEndThenFresh(t *testing.T) {
	c, _, x, lines := newCore()
	step(c, x, fight)
	c.End(x, "fight", phase.Done, phase.Completed, "")
	want(t, x, "fight.begin(resumed=false)", "fight.end(done/completed)")
	if c.Arb.Current() != nil {
		t.Fatal("End must release the grant")
	}
	step(c, x, loot)
	want(t, x, "loot.begin(resumed=false)") // no second End for fight
	got := (*lines)[len(*lines)-2:]
	if got[0] != `T L=grant from=fight to=- why="end: done/completed" tick=1` ||
		got[1] != `T L=grant from=fight to=loot why="fresh: fight done/completed" tick=2` {
		t.Fatalf("lines: %q", got)
	}
}

func TestMonitorEndKeepsDetail(t *testing.T) {
	c, _, x, lines := newCore()
	step(c, x, fight)
	c.End(x, "fight", phase.Abandoned, phase.Judged, "watchdog Stuck; cool 15s")
	if l := (*lines)[len(*lines)-1]; l != `T L=grant from=fight to=- why="end: abandoned/judged: watchdog Stuck; cool 15s" tick=1` {
		t.Fatalf("line: %q", l)
	}
	if ch, tick := c.Last(); ch.From != "fight" || tick != 1 {
		t.Fatalf("last: %v @%d", ch, tick)
	}
}

func TestBenchReleaseIsJudged(t *testing.T) {
	c, cl, x, _ := newCore()
	step(c, x, fight)
	c.Arb.Bench("fight", cl.t.Add(time.Minute), "mute")
	step(c, x, fight, loot)
	want(t, x, "fight.begin(resumed=false)", "fight.end(abandoned/judged)", "loot.begin(resumed=false)")
}

// Every Begin is closed by exactly one End, whatever the churn.
func TestBeginEndBalanced(t *testing.T) {
	c, cl, x, _ := newCore()
	seqs := [][]arbiter.Demand{{fight}, {fight, flee}, {flee, loot}, {loot}, {fight, loot}, {}, {flee}, {}}
	for i, ds := range seqs {
		cl.Tick(700 * time.Millisecond)
		step(c, x, ds...)
		if i == 4 {
			c.End(x, "fight", phase.Abandoned, phase.Mute, "stall")
		}
	}
	open := map[string]int{}
	for _, call := range x.calls {
		name, ev, _ := strings.Cut(call, ".")
		switch {
		case strings.HasPrefix(ev, "begin(resumed=false)"):
			if open[name] != 0 {
				t.Fatalf("%s begun twice without End: %q", name, x.calls)
			}
			open[name]++
		case strings.HasPrefix(ev, "end("):
			if open[name] != 1 {
				t.Fatalf("%s ended while not open: %q", name, x.calls)
			}
			open[name]--
		}
	}
	for n, v := range open {
		if v != 0 {
			t.Fatalf("%s left open: %q", n, x.calls)
		}
	}
}

func TestNilFindIsSilentLifecycle(t *testing.T) {
	c, _, x, _ := newCore()
	c.Find = func(string) Life[*ctx] { return nil }
	step(c, x, fight)
	step(c, x, flee)
	want(t, x)
}

// A Wait parks the holder: it keeps the grant, is not Stepped until WakeAt,
// and survival preemption still lands (and drops the park).
func TestWaitParksHolder(t *testing.T) {
	c, cl, x, _ := newCore()
	step(c, x, loot)
	want(t, x, "loot.begin(resumed=false)")
	c.Park("loot", Status{V: phase.Wait, WakeAt: cl.t.Add(500 * time.Millisecond)})
	if !c.Asleep("loot", cl.t) {
		t.Fatal("parked holder should be asleep")
	}
	cl.Tick(200 * time.Millisecond)
	step(c, x, loot)
	want(t, x) // keeps the grant, no lifecycle
	if !c.Asleep("loot", cl.t) {
		t.Fatal("still asleep before WakeAt")
	}
	cl.Tick(400 * time.Millisecond)
	if c.Asleep("loot", cl.t) {
		t.Fatal("awake at WakeAt")
	}
	// Park again, then survival preempts: the park goes with the seat.
	c.Park("loot", Status{V: phase.Wait, WakeAt: cl.t.Add(5 * time.Second)})
	step(c, x, loot, flee)
	want(t, x, "loot.suspend(preempted)", "flee.begin(resumed=false)")
	if c.Asleep("loot", cl.t) {
		t.Fatal("a suspended activity is not parked: its resume re-verifies at once")
	}
	// Running and a WakeAt-less Wait clear the park.
	c.Park("flee", Status{V: phase.Wait, WakeAt: cl.t.Add(time.Second)})
	c.Park("flee", Status{V: phase.Running})
	if c.Asleep("flee", cl.t) {
		t.Fatal("Running clears the park")
	}
	c.Park("flee", Status{V: phase.Wait})
	if c.Asleep("flee", cl.t) {
		t.Fatal("a Wait without WakeAt steps next tick")
	}
	// End drops it too.
	c.Park("flee", Status{V: phase.Wait, WakeAt: cl.t.Add(time.Second)})
	c.End(x, "flee", phase.Done, phase.Completed, "")
	if c.Asleep("flee", cl.t) {
		t.Fatal("End clears the park")
	}
}
