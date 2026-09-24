package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func TestQuestLegHoldsUntilChestSpent(t *testing.T) {
	var d data.Data
	if _, _, _, pending := questState(d, area.FarOasis); pending {
		t.Fatal("no quest in Far Oasis")
	}
	if _, _, seen, pending := questState(d, area.MaggotLairLevel3); !pending || seen {
		t.Fatalf("Maggot Lair 3, chest not yet seen: want pending+unseen, got pending=%v seen=%v", pending, seen)
	}
	d.Objects = []data.Object{{ID: 7, Name: 356, Selectable: true, Position: data.Position{X: 10, Y: 10}}}
	if _, ch, seen, pending := questState(d, area.MaggotLairLevel3); !pending || !seen || ch.ID != 7 {
		t.Fatalf("closed Staff chest in sight: want pending+seen, got pending=%v seen=%v id=%d", pending, seen, ch.ID)
	}
	d.Objects[0].Selectable = false
	if _, _, _, pending := questState(d, area.MaggotLairLevel3); pending {
		t.Fatal("spent chest ends the leg")
	}
}

func TestQuestChestsAreRites(t *testing.T) {
	for _, n := range []int{354, 355, 356} {
		if !percept.IsRite(data.Object{Name: objName(n)}) || !isQuestChest(objName(n)) {
			t.Fatalf("object %d must be a quest rite", n)
		}
	}
}

func objName(n int) (o object.Name) { return object.Name(n) }

func TestQuestCapTargetsTheQuestLeg(t *testing.T) {
	a := NewAdvance(Act2Itinerary())
	a.frontier = 13
	for i, l := range a.Itinerary {
		if l.Area == area.HallsOfTheDeadLevel3 {
			a.questCap = i
		}
	}
	if got := a.Itinerary[a.campIdx()+1].Area; got != area.HallsOfTheDeadLevel3 {
		t.Fatalf("capped march must target the quest leg itself, targets %v", got)
	}
}
