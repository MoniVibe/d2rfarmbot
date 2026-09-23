package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ledCtx builds a Ctx with a live ledger — intentLeg logs intent changes and a
// nil ledger would hide those writes, so give it a real one.
func ledCtx() *Ctx { return &Ctx{Led: verbs.NewLedger(64)} }

func snapAt(ar area.ID) *percept.Snapshot {
	s := &percept.Snapshot{Valid: true}
	s.Me.Area = ar
	s.Me.Level = 99
	return s
}

// legOf finds the itinerary index of an area for the Act 2 spine used in tests.
func legOf(a *Advance, ar area.ID) int { return a.legIndexOf(ar) }

func TestIntentLegLegacyPathUnchangedWhenFlagOff(t *testing.T) {
	SetDeliberate(false)
	a := NewAdvance(Act2Itinerary())
	s := snapAt(area.LutGholein)
	if got := a.intentLeg(ledCtx(), s, 5); got != 5 {
		t.Fatalf("flag OFF must pass rawIdx through: got %d want 5", got)
	}
	if a.intent.Active() {
		t.Fatal("flag OFF must never adopt an intent")
	}
}

func TestIntentLegHoldsCommittedGoalAgainstShallowerRecompute(t *testing.T) {
	SetDeliberate(true)
	defer SetDeliberate(false)
	a := NewAdvance(Act2Itinerary())
	deep := legOf(a, area.DryHills)   // leg 5
	shallow := legOf(a, area.RockyWaste) // leg 4
	if deep <= shallow {
		t.Fatalf("test spine assumption broken: DryHills(%d) must be deeper than RockyWaste(%d)", deep, shallow)
	}
	// Standing at the town, commit to the deep leg.
	s := snapAt(area.LutGholein)
	if got := a.intentLeg(ledCtx(), s, deep); got != deep {
		t.Fatalf("first commit = %d, want %d", got, deep)
	}
	if !a.intent.Active() || a.intent.Area != area.DryHills {
		t.Fatalf("intent not committed to DryHills: %+v", a.intent)
	}
	// A shallower recomputation inside the window must NOT switch the target —
	// this is the town<->gate anti-flip law.
	if got := a.intentLeg(ledCtx(), s, shallow); got != deep {
		t.Fatalf("shallower recompute switched target to %d, want held at %d", got, deep)
	}
	if a.intent.Area != area.DryHills {
		t.Fatalf("committed area drifted to %d", int(a.intent.Area))
	}
}

func TestIntentLegAdoptsDeeperProgress(t *testing.T) {
	SetDeliberate(true)
	defer SetDeliberate(false)
	a := NewAdvance(Act2Itinerary())
	s := snapAt(area.LutGholein)
	shallow := legOf(a, area.RockyWaste)
	deeper := legOf(a, area.DryHills)
	a.intentLeg(ledCtx(), s, shallow)
	if a.intent.Area != area.RockyWaste {
		t.Fatalf("expected commit to RockyWaste, got %d", int(a.intent.Area))
	}
	// A DEEPER candidate is progress — commit forward.
	if got := a.intentLeg(ledCtx(), s, deeper); got != deeper {
		t.Fatalf("deeper progress = %d, want %d", got, deeper)
	}
	if a.intent.Area != area.DryHills {
		t.Fatalf("did not re-commit forward: %d", int(a.intent.Area))
	}
}

func TestIntentLegReachedRetiresIntent(t *testing.T) {
	SetDeliberate(true)
	defer SetDeliberate(false)
	a := NewAdvance(Act2Itinerary())
	deep := legOf(a, area.DryHills)
	a.intentLeg(ledCtx(), snapAt(area.LutGholein), deep)
	if !a.intent.Active() {
		t.Fatal("intent should be active after commit")
	}
	// Arriving in the committed area retires that target; the caller now offers
	// the NEXT leg (campIdx advanced), so a fresh intent forms forward — the
	// point is that the march never keeps aiming at ground already reached.
	next := deep + 1
	a.intentLeg(ledCtx(), snapAt(area.DryHills), next)
	if a.intent.Area == area.DryHills {
		t.Fatalf("reaching DryHills must not keep it as the committed target: %+v", a.intent)
	}
	if a.intent.Area != a.Itinerary[next].Area {
		t.Fatalf("arrival should commit forward to leg %d (%d), got %d", next, int(a.Itinerary[next].Area), int(a.intent.Area))
	}
}

func TestIntentLegRecomputesAfterWindowLapses(t *testing.T) {
	SetDeliberate(true)
	defer SetDeliberate(false)
	a := NewAdvance(Act2Itinerary())
	deep := legOf(a, area.DryHills)
	shallow := legOf(a, area.RockyWaste)
	s := snapAt(area.LutGholein)
	a.intentLeg(ledCtx(), s, deep)
	// Force the commit window to have lapsed.
	a.intent.CommitUntil = time.Now().Add(-time.Second)
	if got := a.intentLeg(ledCtx(), s, shallow); got != shallow {
		t.Fatalf("after the window lapsed a shallower target may be adopted: got %d want %d", got, shallow)
	}
	if a.intent.Area != area.RockyWaste {
		t.Fatalf("lapsed intent did not recompute: %d", int(a.intent.Area))
	}
}

func TestIntentLegStampsBeyondTownCommitment(t *testing.T) {
	SetDeliberate(true)
	defer SetDeliberate(false)
	advanceCommitBeyondTownUntil = time.Time{}
	a := NewAdvance(Act2Itinerary())
	a.intentLeg(ledCtx(), snapAt(area.LutGholein), legOf(a, area.DryHills))
	if !time.Now().Before(advanceCommitBeyondTownUntil) {
		t.Fatal("a committed beyond-town intent must stamp the beyond-town window")
	}
}
