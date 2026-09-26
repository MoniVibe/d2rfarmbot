package activity

import (
	"time"

	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/d2go/pkg/data/object"
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

// Owner 2026-09-25: a pad is lit only when its own flame says so (mode Opened).
func TestPadLitLive(t *testing.T) {
	if _, seen := padLitLive(nil); seen {
		t.Fatal("no pad: not seen")
	}
	idle := []data.Object{{ID: 13, Name: object.Name(156), Mode: mode.ObjectModeIdle}}
	if lit, seen := padLitLive(idle); !seen || lit {
		t.Fatal("an idle live pad is unlit")
	}
	open := []data.Object{{ID: 13, Name: object.Name(156), Mode: mode.ObjectModeOpened}}
	if lit, _ := padLitLive(open); !lit {
		t.Fatal("an opened pad is lit")
	}
	preset := []data.Object{{Name: object.Name(156), Mode: mode.ObjectModeIdle}}
	if _, seen := padLitLive(preset); seen {
		t.Fatal("a map preset (unit 0) is not the live pad")
	}
}

func TestPadProgressIgnoresFightsAndRewardsGround(t *testing.T) {
	var p padProgress
	t0 := time.Now()
	if p.stalled(90, t0, 50*time.Second) {
		t.Fatal("first tick never stalls")
	}
	// closing in steadily: never stalled, however long it takes
	for i := 1; i <= 20; i++ {
		if p.stalled(90-4*i, t0.Add(time.Duration(i)*time.Second), 50*time.Second) {
			t.Fatalf("tick %d: still gaining ground", i)
		}
	}
	// a 3-minute fight (no approach ticks), then no progress for 40s: not yet
	t1 := t0.Add(20*time.Second + 3*time.Minute)
	for i := 0; i <= 40; i++ {
		if p.stalled(10, t1.Add(time.Duration(i)*time.Second), 50*time.Second) {
			t.Fatalf("the fight's time counted against the pad (at +%ds)", i)
		}
	}
	// stuck well past the window (still ticking every second): concede
	stalled := false
	for i := 41; i <= 60 && !stalled; i++ {
		stalled = p.stalled(10, t1.Add(time.Duration(i)*time.Second), 50*time.Second)
	}
	if !stalled {
		t.Fatal("no progress for 60s of approach: stalled")
	}
}
