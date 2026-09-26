package activity

import (
	"testing"

	"github.com/hectorgimenez/koolo/internal/azbot/combat/policy"
)

// withLeapFirst sets the leap-first rule for one test (the utility-weighing
// tests predate the owner's Leap-only spec and run with it off).
func withLeapFirst(t *testing.T, on bool) {
	old := combatCfg
	combatCfg.LeapFirst = on
	t.Cleanup(func() { combatCfg = old })
}

// Owner 2026-09-25: a feasible leap always wins over the plain attack.
func TestLeapFirstDecides(t *testing.T) {
	in := policy.Inputs{LeapProven: true, LeapReady: true, CombatProven: true, Dist: 5, MPPct: 100, HPPct: 100}
	in.Target.X, in.Me.X = 5, 0
	in.Enemies = append(in.Enemies, in.Target)
	if d := policy.Decide(in, nil, policy.Config{LeapFirst: true}, 1); d.Kind != policy.Leap {
		t.Fatalf("leap-first with a feasible landing: got %v (%s)", d.Kind, d.Why)
	}
}
