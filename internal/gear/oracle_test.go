package gear

import (
	"strings"
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
)

// buildScenario is the synthetic equipped/inventory fixture used by the
// FeasibleUpgrades tests: a summoner wearing a plain cap, carrying
//   - a +1 necro skills helm (equip upgrade),
//   - a socketed cap with an open socket + a Perfect Amethyst (socket step),
//   - three Chipped Amethysts (deterministic cube recipe),
//   - a magic amulet + Orb of Corruption (gamble cube recipe).
func buildScenario(t *testing.T) (equipped, inventory []data.Item) {
	equipped = []data.Item{
		newTestItem(t, "cap", "Old Cap", item.QualityNormal),
	}
	skillHelm := newTestItem(t, "cap", "Summoner Cap", item.QualityMagic,
		stat.Data{ID: stat.AddClassSkills, Value: 1, Layer: 2})
	sockCap := newTestItem(t, "cap", "Socketed Cap", item.QualityNormal,
		stat.Data{ID: stat.NumSockets, Value: 1})
	inventory = []data.Item{
		skillHelm,
		sockCap,
		newTestItem(t, "gpv", "Perfect Amethyst", item.QualityNormal),
		newTestItem(t, "gcv", "Chipped Amethyst", item.QualityNormal),
		newTestItem(t, "gcv", "Chipped Amethyst", item.QualityNormal),
		newTestItem(t, "gcv", "Chipped Amethyst", item.QualityNormal),
		newTestItem(t, "amu", "Shiny Amulet", item.QualityMagic),
		newTestItem(t, "ka3", "Orb of Corruption", item.QualityNormal),
	}
	return equipped, inventory
}

func findSuggestion(sugs []Suggestion, kind SuggestionKind, substr string) *Suggestion {
	for i := range sugs {
		if sugs[i].Kind == kind && strings.Contains(sugs[i].What, substr) {
			return &sugs[i]
		}
	}
	return nil
}

func TestFeasibleUpgrades(t *testing.T) {
	tb := loadFixtureTables(t)
	w := DefaultSummonerWeights()
	equipped, inventory := buildScenario(t)

	sugs := tb.FeasibleUpgrades(equipped, inventory, 0, w)
	if len(sugs) == 0 {
		t.Fatal("no suggestions produced")
	}

	// Ranked by gain, descending.
	for i := 1; i < len(sugs); i++ {
		if sugs[i].Gain > sugs[i-1].Gain {
			t.Fatalf("suggestions not sorted by Gain: %f after %f", sugs[i].Gain, sugs[i-1].Gain)
		}
	}

	// EQUIP: the +1 necro skills helm over the plain cap.
	eq := findSuggestion(sugs, SuggestEquip, "Summoner Cap")
	if eq == nil {
		t.Fatalf("missing equip suggestion, got: %v", whats(sugs))
	}
	if eq.Gain <= 0 || !strings.Contains(eq.What, "Old Cap") {
		t.Errorf("equip suggestion wrong: %+v", eq)
	}

	// SOCKET: Perfect Amethyst into the socketed cap (helm context: +10 str).
	so := findSuggestion(sugs, SuggestSocket, "Perfect Amethyst")
	if so == nil {
		t.Fatalf("missing socket suggestion, got: %v", whats(sugs))
	}
	if so.Gain != 10 { // +10 str * 1.0 weight (str gap is 20, so full value)
		t.Errorf("socket gain = %.1f, want 10: %+v", so.Gain, so)
	}

	// CUBE deterministic: 3x Chipped Amethyst -> Flawed Amethyst.
	cu := findSuggestion(sugs, SuggestCube, "Flawed Amethyst")
	if cu == nil {
		t.Fatalf("missing cube suggestion, got: %v", whats(sugs))
	}
	if cu.Gamble {
		t.Errorf("3-chipped recipe should be deterministic: %+v", cu)
	}
	if !strings.Contains(cu.What, "Chipped Amethyst + Chipped Amethyst + Chipped Amethyst") {
		t.Errorf("cube What should list all consumed inputs: %q", cu.What)
	}

	// CUBE gamble: magic amulet + orb -> useitem corruption.
	gam := findSuggestion(sugs, SuggestCube, "Shiny Amulet")
	if gam == nil {
		t.Fatalf("missing corruption suggestion, got: %v", whats(sugs))
	}
	if !gam.Gamble || !strings.Contains(gam.What, "gamble") {
		t.Errorf("corruption should be flagged gamble: %+v", gam)
	}
}

func TestFeasibleUpgradesNothingOwned(t *testing.T) {
	tb := loadFixtureTables(t)
	w := DefaultSummonerWeights()

	// Only two chipped gems: recipe needs three, nothing equippable, no
	// socket targets -> no suggestions at all.
	inventory := []data.Item{
		newTestItem(t, "gcv", "Chipped Amethyst", item.QualityNormal),
		newTestItem(t, "gcv", "Chipped Amethyst", item.QualityNormal),
	}
	sugs := tb.FeasibleUpgrades(nil, inventory, 0, w)
	if len(sugs) != 0 {
		t.Errorf("expected no suggestions, got: %v", whats(sugs))
	}
}

func TestCubeDoesNotReuseItems(t *testing.T) {
	tb := loadFixtureTables(t)
	w := DefaultSummonerWeights()

	// qty=3 must require three DISTINCT items; one gem cannot satisfy it.
	inventory := []data.Item{
		newTestItem(t, "gcv", "Chipped Amethyst", item.QualityNormal),
	}
	sugs := tb.FeasibleUpgrades(nil, inventory, 0, w)
	if s := findSuggestion(sugs, SuggestCube, "Flawed"); s != nil {
		t.Errorf("recipe satisfied with too few items: %+v", s)
	}
}

func TestEquipPrefersWeakestPairedSlot(t *testing.T) {
	tb := loadFixtureTables(t)
	w := DefaultSummonerWeights()

	goodRing := newTestItem(t, "rin", "Good Ring", item.QualityMagic,
		stat.Data{ID: stat.MaxLife, Value: 40})
	badRing := newTestItem(t, "rin", "Bad Ring", item.QualityNormal)
	newRing := newTestItem(t, "rin", "New Ring", item.QualityMagic,
		stat.Data{ID: stat.MaxLife, Value: 20})

	sugs := tb.FeasibleUpgrades([]data.Item{goodRing, badRing}, []data.Item{newRing}, 0, w)
	eq := findSuggestion(sugs, SuggestEquip, "New Ring")
	if eq == nil {
		t.Fatalf("missing ring equip suggestion: %v", whats(sugs))
	}
	if !strings.Contains(eq.What, "Bad Ring") {
		t.Errorf("should replace the weakest ring, got: %q", eq.What)
	}
}

func whats(sugs []Suggestion) []string {
	out := make([]string, len(sugs))
	for i, s := range sugs {
		out[i] = string(s.Kind) + ": " + s.What
	}
	return out
}
