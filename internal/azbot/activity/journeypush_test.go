package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
)

// journeyPush must PRESERVE the legacy stride fallback in exactly the cases the
// brief names: the flag is off, or there is no grid. In both it returns false
// so the caller runs its original click/force stride. (The planner-owned path
// needs a live game grid + reader and is exercised in the live run, not here.)
func TestJourneyPushKeepsStrideFallbackWithoutGridOrFlag(t *testing.T) {
	a := NewAdvance(Act2Itinerary())
	tgt := data.Position{X: 100, Y: 100}

	SetDeliberate(false)
	if a.journeyPush(&Ctx{Grid: nil}, tgt) {
		t.Fatal("flag OFF must never take the planner path")
	}

	SetDeliberate(true)
	defer SetDeliberate(false)
	if a.journeyPush(&Ctx{Grid: nil}, tgt) {
		t.Fatal("Grid==nil must fall back to the raw stride (the only kept fallback)")
	}
}
