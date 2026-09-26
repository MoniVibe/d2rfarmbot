package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func resetBosses() {
	bosses.Lock()
	bosses.char, bosses.down = "", nil
	bosses.Unlock()
}

func TestBossWitnessPersistsPerCharacter(t *testing.T) {
	resetBosses()
	defer resetBosses()
	st, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &percept.Snapshot{Valid: true}
	s.Me.Area = area.CatacombsLevel4
	ObserveBosses(s, st, "Killary")
	if BossDown(1) {
		t.Fatal("nothing seen dead yet")
	}
	s.Me.BossesDead = []npc.ID{npc.Andariel}
	ObserveBosses(s, st, "Killary")
	if !BossDown(1) {
		t.Fatal("Andariel seen dead: act 1 boss down")
	}
	resetBosses() // a new process reads it back
	s.Me.BossesDead = nil
	ObserveBosses(s, st, "Killary")
	if !BossDown(1) {
		t.Fatal("the witness must survive a restart")
	}
	resetBosses()
	ObserveBosses(s, st, "Other")
	if BossDown(1) {
		t.Fatal("another character has its own campaign")
	}
}

func TestStandingInLaterActProvesEarlierBosses(t *testing.T) {
	resetBosses()
	defer resetBosses()
	st, _ := memory.Open(t.TempDir())
	defer st.Close()
	s := &percept.Snapshot{Valid: true}
	s.Me.Area = area.KurastDocks
	ObserveBosses(s, st, "Fable")
	if !BossDown(1) || !BossDown(2) || BossDown(3) {
		t.Fatal("in act 3: acts 1 and 2 are done, act 3 is not")
	}
}

func TestVoyageDemandGates(t *testing.T) {
	resetBosses()
	defer resetBosses()
	defer SetCampaignMode(false)
	SetCampaignMode(true)
	st, _ := memory.Open(t.TempDir())
	defer st.Close()
	v := NewVoyage()
	s := &percept.Snapshot{Valid: true}
	s.Me.Area, s.Me.InTown, s.Me.HPPct = area.RogueEncampment, true, 100
	ObserveBosses(s, st, "Killary")
	if v.demand(s) != nil {
		t.Fatal("Andariel alive: no voyage")
	}
	s.Me.BossesDead = []npc.ID{npc.Andariel}
	ObserveBosses(s, st, "Killary")
	if !servicesCooled() && ServicesPending(s) {
		t.Skip("docket pending in this synthetic snapshot")
	}
	if d := v.demand(s); d == nil || v.leg.npc != npc.Warriv {
		t.Fatalf("boss down in the act 1 town: Warriv voyage bid, got %+v leg %+v", d, v.leg)
	}
}

func TestFollowActSwapsItinerary(t *testing.T) {
	defer SetCampaignMode(false)
	SetCampaignMode(true)
	a := NewAdvance(Act1Itinerary())
	s := &percept.Snapshot{Valid: true}
	s.Me.Area, s.Me.InTown = area.LutGholein, true
	a.followAct(s)
	if a.campaignAct != 2 || a.Itinerary[0].Area != area.LutGholein {
		t.Fatalf("act %d first leg %d: arriving in Lut Gholein loads Act 2", a.campaignAct, int(a.Itinerary[0].Area))
	}
	s.Me.Area = area.Harrogath
	a.followAct(s)
	if a.campaignAct != 5 || a.Itinerary[len(a.Itinerary)-1].Area != area.TheWorldstoneChamber {
		t.Fatal("Harrogath loads Act 5 ending in the Worldstone Chamber")
	}
}

func TestQuestPresetIgnoresLiveUnits(t *testing.T) {
	obs := []data.Object{
		{ID: 55, Name: 405, Position: data.Position{X: 1, Y: 1}},
		{ID: 0, Name: 405, Position: data.Position{X: 7000, Y: 7100}},
	}
	p, ok := questPreset(obs, 405)
	if !ok || p.X != 7000 {
		t.Fatalf("got %v %v: the preset (unit 0) is the walk target", p, ok)
	}
	if _, ok := questPreset(obs, 406); ok {
		t.Fatal("no preset for another chest")
	}
}
