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
	if _, n, ok := trapAim(me, en[:1]); !ok || n != 1 {
		t.Fatal("a lone monster is a target too (bosses come alone)")
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

// "summon 5 traps ... and continue on": five of hers covering the pack = no
// more casts; sentries left at an old pack do not count; a lone target wants two.
func TestTrapsCountCoverageAtTheAim(t *testing.T) {
	SetTrapBinding(&combat.Binding{Key: 0x71, Skill: 264})
	defer SetTrapBinding(nil)
	tr := NewTraps()
	s := &percept.Snapshot{Valid: true}
	s.Me.HPPct, s.Me.MPPct = 100, 100
	s.Me.Pos = data.Position{X: 100, Y: 100}
	s.Enemies = []percept.EnemyRef{{Pos: data.Position{X: 106, Y: 100}}, {Pos: data.Position{X: 107, Y: 101}}}
	if tr.Demand(s) == nil {
		t.Fatal("a pack in reach, no traps: lay one")
	}
	near := data.Position{X: 105, Y: 100}
	s.Me.OwnTrapPos = []data.Position{near, near, near, near, near}
	if tr.Demand(s) != nil {
		t.Fatal("five covering the pack: continue on")
	}
	far := data.Position{X: 80, Y: 80}
	s.Me.OwnTrapPos = []data.Position{far, far, far, far, far}
	if tr.Demand(s) == nil {
		t.Fatal("five left at an old pack guard nothing here: lay more")
	}
	s.Enemies = s.Enemies[:1]
	s.Me.OwnTrapPos = []data.Position{near, near}
	if tr.Demand(s) != nil {
		t.Fatal("a lone target wants two")
	}
}
