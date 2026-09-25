package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data/area"
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
