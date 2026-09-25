package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func TestNearestLiveEntranceReturnsMemoryIdentity(t *testing.T) {
	target := data.Position{X: 100, Y: 100}
	entrances := data.Entrances{
		{ID: 7, Position: data.Position{X: 130, Y: 100}},
		{ID: 9, Position: data.Position{X: 104, Y: 98}},
	}

	ent, ok := nearestLiveEntrance(entrances, target, 12)
	if !ok {
		t.Fatal("nearest live entrance should be found")
	}
	if ent.ID != 9 || ent.Position != (data.Position{X: 104, Y: 98}) {
		t.Fatalf("nearest entrance = id %d pos %+v, want id 9 at (104,98)", ent.ID, ent.Position)
	}
}

func TestNearestLiveEntranceRejectsIDlessOrFarUnits(t *testing.T) {
	entrances := data.Entrances{
		{ID: 0, Position: data.Position{X: 100, Y: 100}},
		{ID: 11, Position: data.Position{X: 150, Y: 150}},
	}
	if _, ok := nearestLiveEntrance(entrances, data.Position{X: 100, Y: 100}, 12); ok {
		t.Fatal("ID-less and far entrances must not become a memory target")
	}
}

func TestMeleeAttackKeyPrefersLeapAttackAndFallsBackToDoubleSwing(t *testing.T) {
	withLeapFirst(t, false)
	leap := &combat.Binding{Key: 0x70, Skill: skill.ID(143)}
	double := &combat.Binding{Key: 0x71, Skill: skill.ID(133)}
	ctx := &Ctx{Cap: &combat.Capability{LeapAttack: leap, DoubleSwing: double, Combat: leap}}
	s := &percept.Snapshot{Valid: true}
	s.Me.MaxMana, s.Me.MPPct = 40, 50
	s.Me.Pos = data.Position{X: 100, Y: 100}
	// A pack at leap range is the leap's (the AoE); a lone body is walked.
	for i, o := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {-1, 0}, {0, -1}} {
		s.Enemies = append(s.Enemies, percept.EnemyRef{ID: data.UnitID(10 + i), Pos: data.Position{X: 106 + o[0], Y: 100 + o[1]}})
	}
	if got := meleeAttackKey(ctx, s, 5); got != leap.Key {
		t.Fatalf("healthy, pack at leap range: key = %#x, want Leap Attack %#x", got, leap.Key)
	}
	s.Enemies = nil
	if got := meleeAttackKey(ctx, s, 6); got != double.Key {
		t.Fatalf("healthy, lone target at 6: key = %#x, want Double Swing %#x (walk in)", got, double.Key)
	}
	s.Me.MPPct = 15
	if got := meleeAttackKey(ctx, s, 6); got != double.Key {
		t.Fatalf("low-mana contact key = %#x, want Double Swing %#x", got, double.Key)
	}
	if got := meleeAttackKey(ctx, s, 2); got != double.Key {
		t.Fatalf("point-blank contact key = %#x, want Double Swing %#x", got, double.Key)
	}
}

func TestBreakoutDemandClearsEscapeCommitmentInTown(t *testing.T) {
	b := &Breakout{
		engaged:   true,
		castTries: 2,
		pinAt:     time.Now(),
	}
	s := &percept.Snapshot{Valid: true}
	s.Me.InTown = true
	s.Me.HPPct = 100
	if got := b.Demand(s); got != nil {
		t.Fatalf("town breakout demand = %#v, want nil", got)
	}
	if b.engaged || b.castTries != 0 || !b.pinAt.IsZero() {
		t.Fatalf("town reset left breakout state engaged=%v casts=%d pinAt=%v", b.engaged, b.castTries, b.pinAt)
	}
}
