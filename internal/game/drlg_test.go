package game

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
)

func TestLiveNPCsReplaceMap(t *testing.T) {
	ll := LiveLevel{Presets: []LivePreset{
		{Type: PresetMonster, Txt: int(npc.Akara), Pos: data.Position{X: 5532, Y: 4686}},
		{Type: PresetObject, Txt: 119, Pos: data.Position{X: 5499, Y: 4689}},
	}}
	mapNPCs := data.NPCs{
		{ID: npc.Akara, Positions: []data.Position{{X: 4532, Y: 4606}}},
		{ID: npc.Kashya, Positions: []data.Position{{X: 1, Y: 1}}},
	}
	got := liveNPCs(ll, mapNPCs)
	a, ok := got.FindOne(npc.Akara)
	if !ok || len(a.Positions) != 1 || a.Positions[0] != (data.Position{X: 5532, Y: 4686}) {
		t.Fatalf("Akara %+v: the live preset must replace the map", a)
	}
	if _, ok := got.FindOne(npc.Kashya); !ok {
		t.Fatal("a map-only NPC is kept")
	}
}

func TestLiveObjectsSkipLiveUnits(t *testing.T) {
	ll := LiveLevel{Presets: []LivePreset{
		{Type: PresetObject, Txt: 119, Pos: data.Position{X: 5499, Y: 4689}},
		{Type: PresetObject, Txt: 267, Pos: data.Position{X: 5466, Y: 4704}},
	}}
	live := []data.Object{{ID: 7, Name: object.Name(119), Position: data.Position{X: 5500, Y: 4690}}}
	got := liveObjects(ll, live)
	if len(got) != 1 || got[0].Name != object.Name(267) || got[0].ID != 0 {
		t.Fatalf("got %+v: the waypoint is already live; only the stash preset is added (ID 0)", got)
	}
}
