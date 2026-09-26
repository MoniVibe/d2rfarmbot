package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/route"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

func TestEscalateClimbsForSameKeyAndResetsForNew(t *testing.T) {
	a := NewAdvance(Act2Itinerary())
	led := verbs.NewLedger(64)
	k := "intent.42"
	for i, want := range []route.Rung{route.Replan, route.Alternate, route.Portal, route.FailLeg} {
		if r := a.escalate(led, k, "again"); r != want {
			t.Fatalf("escalation %d = %s, want %s", i, r, want)
		}
	}
	// Past FAIL-LEG the ladder wraps to a new cycle instead of repeating it.
	if r := a.escalate(led, k, "again"); r != route.Replan || a.lad.Cycle != 1 {
		t.Fatalf("past FAIL-LEG = %s cycle %d, want REPLAN cycle 1", r, a.lad.Cycle)
	}
	// A new key (a different committed leg) resets the ladder.
	if r := a.escalate(led, "intent.99", "new leg"); r != route.Replan || a.lad.Cycle != 0 {
		t.Fatalf("a new key must reset to REPLAN cycle 0, got %s cycle %d", r, a.lad.Cycle)
	}
}

func TestFailLegDoubtsDoorsAndTurnsTheSearch(t *testing.T) {
	a := NewAdvance(Act2Itinerary())
	led := verbs.NewLedger(64)
	a.marchGoal = data.Position{X: 15080, Y: 6580}
	for i := 0; i < 4; i++ {
		a.escalate(led, "intent.56", "stall")
		a.applyRung(&Ctx{Led: led})
	}
	now := time.Now()
	if !a.disbelief.Has(data.Position{X: 1, Y: 1}, now) || !now.Before(a.searchUntil) {
		t.Fatal("FAIL-LEG must doubt every door and hand the march to the search")
	}
	if !a.disbelief.Has(a.marchGoal, now.Add(time.Minute)) {
		t.Fatal("ALTERNATE must have disbelieved the marched door")
	}
	h := a.heading
	for i := 0; i < 4; i++ {
		a.escalate(led, "intent.56", "stall")
		a.applyRung(&Ctx{Led: led})
	}
	if a.heading%len(bearings) == h%len(bearings) {
		t.Fatal("the next FAIL-LEG must search a different bearing")
	}
}

func TestNoteWatchdogFeedsLadderOnlyWhenArmed(t *testing.T) {
	a := NewAdvance(Act2Itinerary())
	led := verbs.NewLedger(64)

	// Flag off: a watchdog verdict changes nothing.
	SetDeliberate(false)
	a.NoteWatchdog("stuck", led)
	if a.lad.Key != "" {
		t.Fatalf("flag OFF: watchdog must not touch the ladder, key=%q", a.lad.Key)
	}

	// Flag on but no committed intent: still inert (nothing to escalate).
	SetDeliberate(true)
	defer SetDeliberate(false)
	a.NoteWatchdog("stuck", led)
	if a.lad.Key != "" {
		t.Fatalf("no committed intent: watchdog must not touch the ladder, key=%q", a.lad.Key)
	}

	// Flag on with a live intent: the verdict climbs the ladder.
	a.intent = RouteIntent{Area: area.DryHills}
	a.NoteWatchdog("orbit", led)
	if a.lad.Key != a.ladderKey() || a.lad.Rung != route.Replan || !a.ladPending {
		t.Fatalf("armed watchdog should seed the ladder at REPLAN for the intent key, got %+v pending=%v", a.lad, a.ladPending)
	}
	a.NoteWatchdog("orbit", led)
	if a.lad.Rung != route.Alternate {
		t.Fatalf("a second armed verdict should climb to ALTERNATE, got %s", a.lad.Rung)
	}
	// Grant churn is not a route verdict.
	a.NoteWatchdog("thrash", led)
	if a.lad.Rung != route.Alternate {
		t.Fatalf("thrash must not climb the ladder, got %s", a.lad.Rung)
	}
}
