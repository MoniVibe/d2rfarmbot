package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
)

// The waypoint objective reads the mod's levels.txt (skips without the mod).
func TestAreaHasWaypointFromLevels(t *testing.T) {
	if areaHasWaypoint(area.DryHills) {
		t.Fatal("no tables: no objective")
	}
	db, err := gamedata.Load(gamedata.DefaultRoot)
	if err != nil || db.Level(int(area.DryHills)) == nil {
		t.Skip("mod tables not installed")
	}
	gamedata.Set(db)
	defer gamedata.Set(nil)
	for _, ar := range []area.ID{area.DryHills, area.HallsOfTheDeadLevel2, area.SewersLevel2Act2, area.PalaceCellarLevel1, area.FarOasis, area.LostCity} {
		if !areaHasWaypoint(ar) {
			t.Errorf("area %d has a waypoint", ar)
		}
	}
	for _, ar := range []area.ID{area.LutGholein, area.RockyWaste, area.HallsOfTheDeadLevel3, area.MaggotLairLevel1} {
		if areaHasWaypoint(ar) {
			t.Errorf("area %d has no field waypoint", ar)
		}
	}
}
