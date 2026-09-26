package activity

import (
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
	"github.com/hectorgimenez/koolo/internal/game"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
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

// The Sanctuary clock asks for one relog after the budget, and NewWorld resets it.
func TestSanctuaryClock(t *testing.T) {
	resetSanctuaryClock()
	defer resetSanctuaryClock()
	TakeRelogRequest()
	s := &percept.Snapshot{Valid: true}
	s.Me.Area = area.ArcaneSanctuary
	t0 := time.Now()
	for d := time.Duration(0); d <= sanctuaryBudget+2*time.Second; d += time.Second {
		sanctuaryClock(s, t0.Add(d))
	}
	if TakeRelogRequest() == "" {
		t.Fatal("past the budget: a relog is requested")
	}
	sanctuaryClock(s, t0.Add(sanctuaryBudget+3*time.Second))
	if TakeRelogRequest() != "" {
		t.Fatal("once per game")
	}
}

// Owner 2026-09-25: "we can begin running act 3" — Kurast Docks starts the Act 3 spine.
func TestAct3Itinerary(t *testing.T) {
	it := CampaignItinerary(area.KurastDocks)
	if len(it) < 10 || it[0].Area != area.KurastDocks || it[len(it)-1].Area != area.DuranceOfHateLevel3 { // through Travincal to Mephisto
		t.Fatalf("Act 3 spine: got %v", it)
	}
	for _, ar := range []area.ID{area.SpiderCavern, area.FlayerDungeonLevel3, area.SewersLevel2Act3} {
		if _, ok := questLegs[ar]; !ok {
			t.Errorf("area %d holds a Khalim relic leg", ar)
		}
	}
}

func TestHopCountPrefersTheNearestOnRamp(t *testing.T) {
	lv := func(to ...area.ID) game.AreaData {
		var ad game.AreaData
		for _, a := range to {
			ad.AdjacentLevels = append(ad.AdjacentLevels, data.Level{Area: a})
		}
		return ad
	}
	var d game.Data
	d.Areas = map[area.ID]game.AreaData{
		area.DryHills:             lv(area.RockyWaste, area.FarOasis, area.HallsOfTheDeadLevel1),
		area.HallsOfTheDeadLevel1: lv(area.DryHills, area.HallsOfTheDeadLevel2),
		area.HallsOfTheDeadLevel2: lv(area.HallsOfTheDeadLevel1, area.HallsOfTheDeadLevel3),
		area.FarOasis:             lv(area.DryHills),
	}
	if h := hopCount(d, area.DryHills, area.FarOasis); h != 1 {
		t.Fatalf("Dry Hills -> Far Oasis: %d hops, want 1", h)
	}
	if h := hopCount(d, area.HallsOfTheDeadLevel2, area.FarOasis); h != 3 {
		t.Fatalf("Halls 2 -> Far Oasis: %d hops, want 3", h)
	}
	if h := hopCount(d, area.FarOasis, area.Harrogath); h != -1 {
		t.Fatal("unknown: -1")
	}
}

func TestMonsterFreeLegInheritsTheGate(t *testing.T) {
	db, err := gamedata.Load(gamedata.DefaultRoot)
	if err != nil || db.Level(int(area.HaremLevel1)) == nil {
		t.Skip("mod tables not installed")
	}
	gamedata.Set(db)
	defer gamedata.Set(nil)
	legs := gateByMonsterLevel(Act2Itinerary())
	var cv2, harem int
	for _, l := range legs {
		switch l.Area {
		case area.ClawViperTempleLevel2:
			cv2 = l.MinLevel
		case area.HaremLevel1:
			harem = l.MinLevel
		}
	}
	if db.Level(int(area.HaremLevel1)).MonLvl[0] == 0 && harem > cv2 {
		t.Fatalf("Harem 1 (no monsters) gate %d above the Claw Viper leg's %d", harem, cv2)
	}
}
