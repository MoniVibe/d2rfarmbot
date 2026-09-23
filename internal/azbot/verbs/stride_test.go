package verbs

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
)

// The wall-hug fix: a heading into collision turns to the nearest open one, and a
// boxed-in stride refuses instead of pushing 1.6s into a wall for gain=0.
func TestSteerAround(t *testing.T) {
	defer func() { Walkable = nil }()
	me := data.Position{X: 100, Y: 100}
	east := data.Position{X: 120, Y: 100}

	Walkable = func(data.Position) bool { return true }
	if to, k, ok := steerAround(me, east); !ok || k != 0 || to != east {
		t.Fatalf("open field: got %v k=%d ok=%v, want straight", to, k, ok)
	}

	// A north-south wall two tiles east: east is walled, a steep turn is not.
	Walkable = func(p data.Position) bool { return p.X != 102 }
	to, k, ok := steerAround(me, east)
	if !ok || k == 0 {
		t.Fatalf("walled east: got k=%d ok=%v, want a detour", k, ok)
	}
	if to.X >= 102 && to.Y == 100 {
		t.Fatalf("detour target %v still points into the wall", to)
	}

	// Boxed in: nothing within ±112° of east is open.
	Walkable = func(p data.Position) bool { return p.X < me.X }
	if _, _, ok := steerAround(me, east); ok {
		t.Fatal("boxed in: want refusal")
	}
}
