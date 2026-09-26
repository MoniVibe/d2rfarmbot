package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func TestEscapeTrapStandsBetweenHerAndTheChasers(t *testing.T) {
	me := data.Position{X: 100, Y: 100}
	en := []percept.EnemyRef{{Pos: data.Position{X: 108, Y: 100}}, {Pos: data.Position{X: 108, Y: 102}}, {Pos: data.Position{X: 90, Y: 90}, Walled: true}}
	p, ok := escapeTrapPoint(me, en)
	if !ok || p.X != 102 || chebyshev(me, p) != 2 {
		t.Fatalf("got %v %v: two tiles toward the chasers (east)", p, ok)
	}
	if _, ok := escapeTrapPoint(me, nil); ok {
		t.Fatal("no chasers: no escape sentry")
	}
}

func TestHealNearPicksBottlesInReach(t *testing.T) {
	s := &percept.Snapshot{Valid: true}
	s.Me.Pos = data.Position{X: 100, Y: 100}
	s.Items = []percept.ItemRef{
		{ID: 1, Pos: data.Position{X: 103, Y: 100}, Class: 14},  // a club: not a bottle
		{ID: 2, Pos: data.Position{X: 120, Y: 100}, Class: 602}, // a red, too far
		{ID: 3, Pos: data.Position{X: 102, Y: 101}, Class: 602}, // a red in reach
	}
	it, ok := healNear(s, time.Now())
	if !ok || it.ID != 3 {
		t.Fatalf("got %v %v: the red in reach", it, ok)
	}
	healGrabBan[3] = time.Now().Add(time.Minute)
	defer delete(healGrabBan, 3)
	if _, ok := healNear(s, time.Now()); ok {
		t.Fatal("a bottle just tried is skipped for a while")
	}
}
