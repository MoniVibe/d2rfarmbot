package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// R47: an old own portal to a finished leg is not the road forward.
func TestPortalLeadsBack(t *testing.T) {
	a := &Advance{Itinerary: []Leg{{area.MaggotLairLevel3, 26}, {area.HaremLevel1, 29}, {area.PalaceCellarLevel1, 29}}}
	s := &percept.Snapshot{}
	s.Me.Level = 30
	if !a.portalLeadsBack(s, percept.PortalRef{Dest: area.MaggotLairLevel3}, 1) {
		t.Fatal("a portal to the finished Maggot Lair leg leads back")
	}
	if a.portalLeadsBack(s, percept.PortalRef{Dest: area.HaremLevel1}, 1) {
		t.Fatal("a portal to the next leg is the road")
	}
	if a.portalLeadsBack(s, percept.PortalRef{}, 1) {
		t.Fatal("unknown destination: the old rule")
	}
	s.Me.Level = 20
	if a.portalLeadsBack(s, percept.PortalRef{Dest: area.MaggotLairLevel3}, 1) {
		t.Fatal("next leg above his level: back to the camp is fine")
	}
}
