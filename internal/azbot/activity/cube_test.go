package activity

import (
	"testing"

	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// Without the mod tables no code resolves: the recipe book stays shut (the
// errand never opens panels on a guess).
func TestTransmuteNeedsReadableCodes(t *testing.T) {
	tr := NewTransmute()
	s := &percept.Snapshot{Valid: true}
	s.Me.HPPct = 100
	s.Bag = []percept.BagItem{{ID: 1}, {ID: 2}}
	if _, ok := tr.ready(s); ok {
		t.Fatal("no gamedata: no recipe is ready")
	}
	if tr.demand(s, s.At) != nil {
		t.Fatal("no recipe: no bid")
	}
}

func TestCubeRecipesAreTheCampaignOnes(t *testing.T) {
	want := map[string]int{"hst": 2, "qf2": 4}
	for _, r := range cubeRecipes {
		if want[r.output] != len(r.inputs) {
			t.Fatalf("%s needs %d inputs, got %v", r.output, want[r.output], r.inputs)
		}
	}
}
