package arbiter

import (
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time       { return c.t }
func (c *fakeClock) Tick(d time.Duration) { c.t = c.t.Add(d) }
func newArb() (*Arbiter, *fakeClock) {
	c := &fakeClock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	return &Arbiter{Clock: c}, c
}

func dem(who string, c Class, u float64) Demand {
	return Demand{Who: who, Class: c, Urgency: u, Commit: Commitment{MinHold: 2 * time.Second, SwitchMargin: 0.2}}
}

func holder(g *Grant) string {
	if g == nil {
		return ""
	}
	return g.Demand.Who
}

func TestPreemptResumeKeepsHeld(t *testing.T) {
	a, c := newArb()
	fight, flee := dem("fight", ClassFight, 0.9), dem("flee", ClassSurvive, 0.5)

	g, ch := a.Decide([]Demand{fight})
	if holder(g) != "fight" || ch.Kind != Fresh {
		t.Fatalf("first grant = %s %v", holder(g), ch)
	}
	c.Tick(3 * time.Second)
	g, ch = a.Decide([]Demand{fight, flee})
	if holder(g) != "flee" || ch.Kind != Preempt || ch.Reason != "survive > fight" {
		t.Fatalf("preempt = %s %v", holder(g), ch)
	}
	if got := ch.String(); got != "fight -> flee (preempt: survive > fight)" {
		t.Errorf("Change.String = %q", got)
	}
	c.Tick(5 * time.Second) // flee holds; fight's ledger must not move
	if got := a.Held("fight"); got != 3*time.Second {
		t.Errorf("fight held while preempted = %v, want 3s", got)
	}
	a.Release() // flee done
	g, ch = a.Decide([]Demand{fight})
	if holder(g) != "fight" || ch.Kind != Fresh || ch.From != "flee" {
		t.Fatalf("resume = %s %v", holder(g), ch)
	}
	c.Tick(time.Second)
	if got := a.Held("fight"); got != 4*time.Second {
		t.Errorf("fight held after resume = %v, want 4s", got)
	}
	if got := g.Held(); got != 4*time.Second {
		t.Errorf("Grant.Held = %v, want 4s", got)
	}
	if got := a.Stint(); got != time.Second {
		t.Errorf("Stint = %v, want 1s (new grant)", got)
	}
	if a.Held("flee") != 0 {
		t.Errorf("released flee kept ledger %v", a.Held("flee"))
	}
}

func TestBlockedTimeNotCounted(t *testing.T) {
	a, c := newArb()
	d := dem("identify", ClassService, 0.5)
	a.Decide([]Demand{d})
	c.Tick(time.Second)
	a.SetBlocked(true)
	c.Tick(10 * time.Second)
	if got := a.Held("identify"); got != time.Second {
		t.Errorf("held while blocked = %v, want 1s", got)
	}
	a.SetBlocked(false)
	c.Tick(2 * time.Second)
	if got := a.Held("identify"); got != 3*time.Second {
		t.Errorf("held after unblock = %v, want 3s", got)
	}
	// A preemption while blocked must not credit the blocked span either.
	a.SetBlocked(true)
	c.Tick(4 * time.Second)
	a.Decide([]Demand{d, dem("flee", ClassSurvive, 1)})
	if got := a.Held("identify"); got != 3*time.Second {
		t.Errorf("held after blocked preempt = %v, want 3s", got)
	}
	if a.Blocked() {
		t.Error("new holder inherited the blocked flag")
	}
}

func TestDeterministicTies(t *testing.T) {
	x := Demand{Who: "restock", Class: ClassService, Urgency: 0.6}
	y := Demand{Who: "heal", Class: ClassService, Urgency: 0.6}
	z := Demand{Who: "repair", Class: ClassService, Urgency: 0.6}
	orders := [][]Demand{{x, y, z}, {z, y, x}, {y, z, x}, {y, x, z}}

	// Equal Order: the name decides.
	for _, ds := range orders {
		a, _ := newArb()
		if g, _ := a.Decide(ds); holder(g) != "heal" {
			t.Errorf("order %v: got %s, want heal", names(ds), holder(g))
		}
	}
	// Registration index beats the name.
	x.Order, y.Order, z.Order = 2, 1, 0
	orders = [][]Demand{{x, y, z}, {z, y, x}, {y, z, x}, {y, x, z}}
	for _, ds := range orders {
		a, _ := newArb()
		if g, _ := a.Decide(ds); holder(g) != "repair" {
			t.Errorf("order %v: got %s, want repair", names(ds), holder(g))
		}
	}
	// Class and urgency still dominate the index.
	a, _ := newArb()
	w := Demand{Who: "zz", Class: ClassService, Urgency: 0.7, Order: 99}
	if g, _ := a.Decide([]Demand{x, y, z, w}); holder(g) != "zz" {
		t.Errorf("urgency lost to index: %s", holder(g))
	}
}

func names(ds []Demand) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Who)
	}
	return out
}

func TestHysteresis(t *testing.T) {
	a, c := newArb()
	fight, loot := dem("fight", ClassFight, 0.5), dem("strike", ClassFight, 0.9)
	a.Decide([]Demand{fight})

	c.Tick(time.Second) // before MinHold: a much better rival still waits
	g, ch := a.Decide([]Demand{fight, loot})
	if holder(g) != "fight" || ch.Kind != Keep || !strings.Contains(ch.Reason, "minhold") {
		t.Fatalf("before MinHold = %s %v", holder(g), ch)
	}
	c.Tick(2 * time.Second) // past MinHold but inside margin
	g, ch = a.Decide([]Demand{fight, dem("strike", ClassFight, 0.65)})
	if holder(g) != "fight" || ch.Kind != Keep || !strings.Contains(ch.Reason, "margin") {
		t.Fatalf("inside margin = %s %v", holder(g), ch)
	}
	g, ch = a.Decide([]Demand{fight, dem("strike", ClassFight, 0.7)})
	if holder(g) != "strike" || ch.Kind != Outbid {
		t.Fatalf("past MinHold and margin = %s %v", holder(g), ch)
	}
	if ch.Reason != "fight 0.70 >= 0.50+0.20 after 3s" {
		t.Errorf("outbid reason = %q", ch.Reason)
	}
	// Lower class never evicts, however long the hold.
	c.Tick(time.Minute)
	g, ch = a.Decide([]Demand{dem("strike", ClassFight, 0.1), dem("loot", ClassLoot, 1)})
	if holder(g) != "strike" || ch.Kind != Keep {
		t.Fatalf("lower class = %s %v", holder(g), ch)
	}
}

func TestStoppedBiddingRelease(t *testing.T) {
	a, c := newArb()
	a.Decide([]Demand{dem("breakout", ClassSurvive, 1)})
	c.Tick(time.Second)
	g, ch := a.Decide([]Demand{dem("respawn", ClassRecover, 1)})
	if holder(g) != "respawn" || ch.Kind != Released || ch.From != "breakout" ||
		ch.Reason != "breakout stopped bidding" {
		t.Fatalf("release = %s %v", holder(g), ch)
	}
	if a.Held("breakout") != 0 {
		t.Errorf("released holder kept ledger")
	}
	g, ch = a.Decide(nil)
	if g != nil || ch.Kind != Released || ch.To != "" || !ch.Changed() {
		t.Fatalf("release to nobody = %s %v", holder(g), ch)
	}
	g, ch = a.Decide(nil)
	if g != nil || ch.Changed() {
		t.Fatalf("idle = %s %v", holder(g), ch)
	}
}

func TestWithdrawnBidderForgetsLedger(t *testing.T) {
	a, c := newArb()
	fight := dem("fight", ClassFight, 0.9)
	a.Decide([]Demand{fight})
	c.Tick(3 * time.Second)
	a.Decide([]Demand{fight, dem("flee", ClassSurvive, 1)})
	a.Decide([]Demand{dem("flee", ClassSurvive, 1)}) // fight's enemies gone
	if got := a.Held("fight"); got != 0 {
		t.Errorf("withdrawn fight kept %v", got)
	}
}

func TestBench(t *testing.T) {
	a, c := newArb()
	adv, exp := dem("advance", ClassTravel, 0.9), dem("explore", ClassExplore, 0.5)
	a.Bench("advance", c.Now().Add(10*time.Second), "unreachable")
	if why, ok := a.Benched("advance"); !ok || why != "unreachable" {
		t.Fatalf("Benched = %q %v", why, ok)
	}
	g, _ := a.Decide([]Demand{adv, exp})
	if holder(g) != "explore" {
		t.Fatalf("benched advance granted: %s", holder(g))
	}
	c.Tick(10 * time.Second)
	if _, ok := a.Benched("advance"); ok {
		t.Fatal("bench did not expire")
	}
	g, ch := a.Decide([]Demand{adv, exp})
	if holder(g) != "advance" || ch.Kind != Preempt {
		t.Fatalf("after expiry = %s %v", holder(g), ch)
	}
	// Benching the holder releases it with the bench's reason.
	a.Bench("advance", c.Now().Add(5*time.Second), "watchdog")
	g, ch = a.Decide([]Demand{adv, exp})
	if holder(g) != "explore" || ch.Kind != Released || ch.Reason != "advance benched: watchdog" {
		t.Fatalf("benched holder = %s %v", holder(g), ch)
	}
	// Everyone benched: nobody.
	a.Bench("explore", c.Now().Add(5*time.Second), "idle")
	if g, ch = a.Decide([]Demand{adv, exp}); g != nil || ch.Kind != Released {
		t.Fatalf("all benched = %s %v", holder(g), ch)
	}
}

func TestFreshNamesEndedHolder(t *testing.T) {
	a, _ := newArb()
	g, ch := a.Decide([]Demand{dem("loot", ClassLoot, 1)})
	if ch.Kind != Fresh || ch.From != "" || ch.Reason != "no holder" {
		t.Fatalf("first = %v", ch)
	}
	a.End(g.Demand.Who, "done")
	_, ch = a.Decide([]Demand{dem("travel", ClassTravel, 1)})
	if ch.String() != "loot -> travel (fresh: loot done)" {
		t.Errorf("fresh after End = %q", ch.String())
	}
}

func TestMute(t *testing.T) {
	a, c := newArb()
	f := dem("fight", ClassFight, 1)
	a.Decide([]Demand{f})
	c.Tick(3 * time.Second)
	if !a.Mute("fight", 2*time.Second) {
		t.Error("3s silent holder not mute at 2s bar")
	}
	a.MarkProgress("fight")
	c.Tick(time.Second)
	if a.Mute("fight", 2*time.Second) {
		t.Error("mute right after progress")
	}
	// Blocked and preempted time are not silence.
	a.SetBlocked(true)
	c.Tick(10 * time.Second)
	a.Decide([]Demand{f, dem("flee", ClassSurvive, 1)})
	c.Tick(10 * time.Second)
	if a.Mute("fight", 2*time.Second) {
		t.Error("non-holder reported mute")
	}
	a.Release()
	a.Decide([]Demand{f})
	if got := a.Silence("fight"); got != time.Second {
		t.Errorf("silence after resume = %v, want 1s", got)
	}
	c.Tick(1500 * time.Millisecond)
	if !a.Mute("fight", 2*time.Second) {
		t.Error("2.5s held silence not mute")
	}
}
