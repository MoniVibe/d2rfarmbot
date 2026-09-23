package activity

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

func TestEscalateClimbsForSameKeyAndResetsForNew(t *testing.T) {
	a := NewAdvance(Act2Itinerary())
	led := verbs.NewLedger(64)
	k := "intent.42"
	if r := a.escalate(led, k, "first"); r != rungReplan {
		t.Fatalf("first escalation should be rung REPLAN, got %s", r)
	}
	if r := a.escalate(led, k, "again"); r != rungAlternate {
		t.Fatalf("second escalation should climb to ALTERNATE, got %s", r)
	}
	if r := a.escalate(led, k, "again"); r != rungPortal {
		t.Fatalf("third escalation should climb to PORTAL, got %s", r)
	}
	if r := a.escalate(led, k, "again"); r != rungFailLeg {
		t.Fatalf("fourth escalation should climb to FAIL-LEG, got %s", r)
	}
	if r := a.escalate(led, k, "again"); r != rungFailLeg {
		t.Fatalf("the ladder tops out at FAIL-LEG, got %s", r)
	}
	// A new key (a different committed leg) resets the ladder.
	if r := a.escalate(led, "intent.99", "new leg"); r != rungReplan {
		t.Fatalf("a new key must reset to REPLAN, got %s", r)
	}
}

func TestNoteWatchdogFeedsLadderOnlyWhenArmed(t *testing.T) {
	a := NewAdvance(Act2Itinerary())
	led := verbs.NewLedger(64)

	// Flag off: a watchdog verdict changes nothing.
	SetDeliberate(false)
	a.NoteWatchdog("stuck", led)
	if a.ladKey != "" {
		t.Fatalf("flag OFF: watchdog must not touch the ladder, key=%q", a.ladKey)
	}

	// Flag on but no committed intent: still inert (nothing to escalate).
	SetDeliberate(true)
	defer SetDeliberate(false)
	a.NoteWatchdog("stuck", led)
	if a.ladKey != "" {
		t.Fatalf("no committed intent: watchdog must not touch the ladder, key=%q", a.ladKey)
	}

	// Flag on with a live intent: the verdict climbs the ladder.
	a.intent = RouteIntent{Area: area.DryHills}
	a.NoteWatchdog("orbit", led)
	if a.ladKey != a.ladderKey() || a.ladRung != rungReplan {
		t.Fatalf("armed watchdog should seed the ladder at REPLAN for the intent key, got key=%q rung=%s", a.ladKey, a.ladRung)
	}
	a.NoteWatchdog("orbit", led)
	if a.ladRung != rungAlternate {
		t.Fatalf("a second armed verdict should climb to ALTERNATE, got %s", a.ladRung)
	}
}
