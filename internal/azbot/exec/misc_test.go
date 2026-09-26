package exec

import (
	"math/rand"
	"testing"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

type bidder struct {
	who string
	cls arbiter.Class
	u   float64
}

func (b bidder) bid() *arbiter.Demand {
	if b.who == "" {
		return nil
	}
	return &arbiter.Demand{Who: b.who, Class: b.cls, Urgency: b.u}
}

func TestCollectStampsRegistryOrder(t *testing.T) {
	reg := []bidder{{"zeta", arbiter.ClassLoot, 1}, {}, {"alpha", arbiter.ClassLoot, 1}}
	ds := Collect(reg, bidder.bid)
	if len(ds) != 2 || ds[0].Who != "zeta" || ds[0].Order != 0 || ds[1].Who != "alpha" || ds[1].Order != 2 {
		t.Fatalf("collect: %+v", ds)
	}
	// An exact tie goes to the earlier registration, not the alphabet.
	g, _ := (&arbiter.Arbiter{}).Decide(ds)
	if g.Demand.Who != "zeta" {
		t.Fatalf("tie went to %s", g.Demand.Who)
	}
}

func TestCollectDeterministic(t *testing.T) {
	reg := []bidder{{"a", arbiter.ClassFight, 1}, {"b", arbiter.ClassFight, 1}, {"c", arbiter.ClassFight, 1},
		{"d", arbiter.ClassLoot, 2}, {"e", arbiter.ClassFight, 1}}
	first := ""
	for i := 0; i < 50; i++ {
		// The old map-ranged registry handed Decide a random arrival order; with
		// Order stamped, arrival order no longer matters.
		ds := Collect(reg, bidder.bid)
		rand.Shuffle(len(ds), func(a, b int) { ds[a], ds[b] = ds[b], ds[a] })
		g, _ := (&arbiter.Arbiter{}).Decide(ds)
		if first == "" {
			first = g.Demand.Who
		} else if g.Demand.Who != first {
			t.Fatalf("run %d granted %s, first run %s", i, g.Demand.Who, first)
		}
	}
	if first != "a" {
		t.Fatalf("tie winner %s, want the first registered (a)", first)
	}
}

func TestModeSet(t *testing.T) {
	w := ModeOf(screen.World)
	if !w.Allows(screen.World) || w.Allows(screen.Dead) || w.String() != "world" {
		t.Fatalf("world set: %s", w)
	}
	if !AnyMode.Allows(screen.Dead) || AnyMode.String() != "any" {
		t.Fatal("any")
	}
	if s := ModeOf(screen.Dead, screen.World).String(); s != "world|dead" {
		t.Fatalf("set string %q", s)
	}
}
