package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
)

// R51: object-door legs route through the level that holds the door.
func TestRouteVia(t *testing.T) {
	cases := []struct{ cur, to, want area.ID }{
		{area.PalaceCellarLevel1, area.ArcaneSanctuary, area.PalaceCellarLevel3},
		{area.PalaceCellarLevel3, area.ArcaneSanctuary, area.ArcaneSanctuary},
		{area.PalaceCellarLevel2, area.CanyonOfTheMagi, area.PalaceCellarLevel3},
		{area.ArcaneSanctuary, area.CanyonOfTheMagi, area.CanyonOfTheMagi},
		{area.CanyonOfTheMagi, area.CanyonOfTheMagi, area.CanyonOfTheMagi},
		{area.CanyonOfTheMagi, area.TalRashasTomb3, area.TalRashasTomb3},
		{area.PalaceCellarLevel1, area.TalRashasTomb3, area.PalaceCellarLevel3},
		{area.HaremLevel1, area.HaremLevel2, area.HaremLevel2},
	}
	for _, c := range cases {
		if got := routeVia(c.cur, c.to); got != c.want {
			t.Errorf("routeVia(%d,%d) = %d, want %d", c.cur, c.to, got, c.want)
		}
	}
}

// R53: a merged map preset (unit ID 0) is not a live object.
func TestFindLiveNeedsUnit(t *testing.T) {
	obs := []data.Object{{Name: object.YetAnotherTome}, {Name: object.ArcaneSanctuaryPortal, ID: 7}}
	if _, ok := findLive(obs, object.YetAnotherTome); ok {
		t.Fatal("a preset without a unit ID is not live")
	}
	if ob, ok := findLive(obs, object.ArcaneSanctuaryPortal); !ok || ob.ID != 7 {
		t.Fatal("the live portal is found")
	}
}

// R57: the Canyon leg from Palace Cellar 3 hops the Sanctuary portal first.
func TestRouteViaFeedsPortalHop(t *testing.T) {
	if got := routeVia(area.PalaceCellarLevel3, area.CanyonOfTheMagi); got != area.ArcaneSanctuary {
		t.Fatalf("Canyon from Cellar 3 goes via the Sanctuary, got %d", got)
	}
}
