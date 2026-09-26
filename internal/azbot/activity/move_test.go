package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
)

// A flee "away" point one tile off would read as arrived and freeze the
// retreat: reach stretches it along the same bearing.
func TestReachStretchesShortAwayPoints(t *testing.T) {
	me := data.Position{X: 100, Y: 100}
	if got := reach(me, data.Position{X: 101, Y: 100}, 8); got != (data.Position{X: 108, Y: 100}) {
		t.Fatalf("1 tile east stretched to 8: got %v", got)
	}
	if got := reach(me, data.Position{X: 98, Y: 99}, 8); got != (data.Position{X: 92, Y: 96}) {
		t.Fatalf("bearing kept: got %v", got)
	}
	far := data.Position{X: 120, Y: 90}
	if got := reach(me, far, 8); got != far {
		t.Fatalf("a far point is untouched: got %v", got)
	}
	if got := reach(me, me, 8); got != me {
		t.Fatalf("no bearing, no stretch: got %v", got)
	}
}

// The blind-heading walkers turn on a wall: a planner refusal or a
// route-less stride that gained nothing — never on a planned pulse's hiccup.
func TestWallTurned(t *testing.T) {
	cases := []struct {
		st   moveto.Status
		turn bool
	}{
		{moveto.Status{State: moveto.NoPath}, true},
		{moveto.Status{State: moveto.Stalled}, true},
		{moveto.Status{State: moveto.Moving, Mode: "reckon", Issued: true, Blocked: true}, true},
		{moveto.Status{State: moveto.Moving, Mode: "journey", Issued: true, Blocked: true}, false},
		{moveto.Status{State: moveto.Moving, Mode: "journey", Issued: true}, false},
	}
	for i, c := range cases {
		if got := wallTurned(c.st); got != c.turn {
			t.Fatalf("case %d %+v: wallTurned=%t want %t", i, c.st, got, c.turn)
		}
	}
}
