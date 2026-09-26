package phase

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time       { return c.t }
func (c *fakeClock) Tick(d time.Duration) { c.t = c.t.Add(d) }

type shopPhase uint8

const (
	Idle shopPhase = iota
	Walk
	Open
	Sell
)

func (s shopPhase) String() string {
	return [...]string{"Idle", "Walk", "Open", "Sell"}[s]
}

func newPhaser() (*Phaser[shopPhase], *fakeClock, *[]string, *time.Duration) {
	c := &fakeClock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	var lines []string
	var held time.Duration
	p := &Phaser[shopPhase]{Act: "fence", Clock: c,
		Log:  func(l string) { lines = append(lines, l) },
		Held: func() time.Duration { return held }}
	return p, c, &lines, &held
}

func TestTransitionsLogged(t *testing.T) {
	p, c, lines, held := newPhaser()
	p.To(Walk, "docket: sell")
	c.Tick(800 * time.Millisecond)
	*held = 800 * time.Millisecond
	p.To(Open, "at vendor")
	p.To(Open, "retry") // same phase: no line, no reset
	want := []string{
		`phase act=fence from=Idle to=Walk why="docket: sell" inPhase=0.0s`,
		`phase act=fence from=Walk to=Open why="at vendor" inPhase=0.8s`,
	}
	if len(*lines) != len(want) {
		t.Fatalf("lines = %q", *lines)
	}
	for i := range want {
		if (*lines)[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, (*lines)[i], want[i])
		}
	}
	last, ok := p.Last()
	if !ok || last.From != Walk || last.To != Open || last.InPhase != 800*time.Millisecond ||
		last.Held != 800*time.Millisecond || last.Why != "at vendor" {
		t.Errorf("Last = %+v", last)
	}
	if p.Phase() != Open || p.Transitions() != 2 {
		t.Errorf("phase=%v n=%d", p.Phase(), p.Transitions())
	}
}

func TestBudgetOverrun(t *testing.T) {
	p, c, _, held := newPhaser()
	p.Budget(Open, 2*time.Second)
	p.To(Walk, "go")
	*held = 30 * time.Second // Walk has no budget
	if over, _ := p.Overrun(*held); over {
		t.Fatal("unbudgeted phase overran")
	}
	p.To(Open, "arrived")
	c.Tick(time.Minute) // wall time alone (preempted/blocked) never overruns
	if over, _ := p.Overrun(*held + 2*time.Second); over {
		t.Fatal("overran at exactly the budget")
	}
	over, ph := p.Overrun(*held + 2100*time.Millisecond)
	if !over || ph != Open {
		t.Fatalf("Overrun = %v %v, want true Open", over, ph)
	}
	p.Budget(Open, 0)
	if over, _ := p.Overrun(*held + time.Hour); over {
		t.Fatal("removed budget still enforced")
	}
}

func TestBudgetWithoutHeldSource(t *testing.T) {
	p := &Phaser[shopPhase]{Act: "fence", Clock: &fakeClock{}}
	p.Budget(Sell, time.Second)
	p.Overrun(5 * time.Second) // Step reports held before the transition
	p.To(Sell, "open")
	if over, _ := p.Overrun(5500 * time.Millisecond); over {
		t.Fatal("budget measured from zero instead of phase entry")
	}
	if over, _ := p.Overrun(6100 * time.Millisecond); !over {
		t.Fatal("no overrun past budget")
	}
}

func TestReset(t *testing.T) {
	p, c, lines, held := newPhaser()
	p.Budget(Walk, time.Second)
	p.To(Walk, "go")
	c.Tick(time.Second)
	*held = 5 * time.Second
	p.Reset()
	if p.Phase() != Idle || p.InPhase() != 0 || p.Transitions() != 0 {
		t.Fatalf("after Reset phase=%v inPhase=%v n=%d", p.Phase(), p.InPhase(), p.Transitions())
	}
	if _, ok := p.Last(); ok {
		t.Error("Last survived Reset")
	}
	p.To(Walk, "again")
	if (*lines)[len(*lines)-1] != `phase act=fence from=Idle to=Walk why="again" inPhase=0.0s` {
		t.Errorf("line after reset = %q", (*lines)[len(*lines)-1])
	}
	if over, _ := p.Overrun(5500 * time.Millisecond); over {
		t.Error("budget lost its anchor across Reset")
	}
	if over, _ := p.Overrun(6100 * time.Millisecond); !over {
		t.Error("budgets did not survive Reset")
	}
}

func TestEnumStrings(t *testing.T) {
	if Abandoned.String() != "abandoned" || !Done.Terminal() || Wait.Terminal() {
		t.Error("verdict")
	}
	for r := NoReason; r <= Judged; r++ {
		if r.String() == "?" {
			t.Errorf("reason %d has no name", r)
		}
	}
	if UIWedge.String() != "ui-wedge" || Timebox.String() != "timebox" {
		t.Error("reason names")
	}
}
