package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func TestUnloadGoesHomeWhenFullAndCalm(t *testing.T) {
	u := NewUnload()
	s := townSnap()
	s.Me.InTown = false
	s.Me.InvFree, s.Me.JunkCount = 3, 5
	if d := u.Demand(s); d == nil {
		t.Fatal("full bag with junk in a calm field: Unload must bid")
	}
	s.Enemies = []percept.EnemyRef{{Pos: data.Position{X: s.Me.Pos.X + 5, Y: s.Me.Pos.Y}}}
	if u.Demand(s) != nil {
		t.Fatal("an enemy within 12: no portal cast (Fight first)")
	}
	s.Enemies = nil
	s.Me.JunkCount, s.Me.StashCount = 0, 0
	if u.Demand(s) != nil {
		t.Fatal("full of keepers only: nothing to clear in town")
	}
	s.Me.InvFree, s.Me.JunkCount = 30, 5
	if u.Demand(s) != nil {
		t.Fatal("room left: keep working")
	}
}
