package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// The wide hunt must be asked for: a stale march hint alone (Advance mute —
// wounded, quest bid, town pause) is the corridor, not a rampage.
func TestHuntRadiusNeedsGrindHint(t *testing.T) {
	defer func() { grindUntil, marchLawfulUntil = time.Time{}, time.Time{} }()
	s := &percept.Snapshot{Valid: true}
	s.Me.Level, s.Me.Area = 1, area.BloodMoor // paying ground

	grindUntil, marchLawfulUntil = time.Time{}, time.Time{}
	if r := huntRadius(s); r != 10 {
		t.Fatalf("mute Advance, no grind hint: radius %d, want 10", r)
	}
	grindUntil = time.Now().Add(2 * time.Second)
	if r := huntRadius(s); r != 45 {
		t.Fatalf("grinding is the mission: radius %d, want 45", r)
	}
	marchLawfulUntil = time.Now().Add(2 * time.Second)
	if r := huntRadius(s); r != 10 {
		t.Fatalf("march bidding outranks grind: radius %d, want 10", r)
	}
	marchLawfulUntil = time.Time{}
	s.Me.Level = 30 // outlevelled ground pays nothing
	if r := huntRadius(s); r != 10 {
		t.Fatalf("outlevelled ground: radius %d, want 10", r)
	}
}
