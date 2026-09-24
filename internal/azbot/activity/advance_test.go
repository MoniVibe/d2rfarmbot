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
	leap := &combat.Binding{Key: 0x70, Skill: skill.ID(143)}
	double := &combat.Binding{Key: 0x71, Skill: skill.ID(133)}
	ctx := &Ctx{Cap: &combat.Capability{LeapAttack: leap, DoubleSwing: double, Combat: leap}}
	s := &percept.Snapshot{Valid: true}
	s.Me.MaxMana, s.Me.MPPct = 40, 50
	if got := meleeAttackKey(ctx, s, 5); got != leap.Key {
		t.Fatalf("healthy ranged contact key = %#x, want Leap Attack %#x", got, leap.Key)
	}
	s.Me.MPPct = 15
	if got := meleeAttackKey(ctx, s, 5); got != double.Key {
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
