package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func TestTrapAimPicksDensestPack(t *testing.T) {
	me := data.Position{X: 100, Y: 100}
	en := []percept.EnemyRef{
		{Pos: data.Position{X: 103, Y: 100}},
		{Pos: data.Position{X: 110, Y: 100}}, {Pos: data.Position{X: 111, Y: 101}}, {Pos: data.Position{X: 110, Y: 102}},
		{Pos: data.Position{X: 100, Y: 108}, Walled: true},
	}
	aim, n, ok := trapAim(me, en)
	if !ok || n != 3 || chebyshev(aim, data.Position{X: 110, Y: 101}) > 1 {
		t.Fatalf("aim %v pack %d ok %v, want the 3-pack", aim, n, ok)
	}
	if _, _, ok := trapAim(me, en[:1]); ok {
		t.Fatal("a lone monster is not worth a trap")
	}
}

func TestTrapCenterStaysInReach(t *testing.T) {
	me := data.Position{X: 100, Y: 100}
	if c := trapCenter(me, data.Position{X: 105, Y: 100}); c.X != 105 {
		t.Fatalf("near pack: center on it, got %v", c)
	}
	if c := trapCenter(me, data.Position{X: 114, Y: 100}); chebyshev(me, c) != trapNear {
		t.Fatalf("far pack: center pulled to %d, got %v", trapNear, c)
	}
}

// "summon 5 traps ... and continue on": five of hers standing = no more casts.
func TestTrapsStopAtFive(t *testing.T) {
	SetTrapBinding(&combat.Binding{Key: 0x71, Skill: 264})
	defer SetTrapBinding(nil)
	tr := NewTraps()
	s := &percept.Snapshot{Valid: true}
	s.Me.HPPct, s.Me.MPPct = 100, 100
	s.Me.Pos = data.Position{X: 100, Y: 100}
	s.Enemies = []percept.EnemyRef{{Pos: data.Position{X: 106, Y: 100}}, {Pos: data.Position{X: 107, Y: 101}}}
	if tr.Demand(s) == nil {
		t.Fatal("a pack of 2 in reach, no traps standing: lay one")
	}
	s.Me.OwnTraps = 5
	if tr.Demand(s) != nil {
		t.Fatal("five standing: continue on")
	}
}
