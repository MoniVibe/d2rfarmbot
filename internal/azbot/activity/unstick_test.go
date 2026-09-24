package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/unstick"
	"github.com/hectorgimenez/koolo/internal/azbot/watchdog"
)

func fieldSnap() *percept.Snapshot {
	s := &percept.Snapshot{Valid: true}
	s.Me.HPPct = 80
	s.Me.Area = 3
	s.Me.Pos = data.Position{X: 50, Y: 50}
	return s
}

func TestUnstickBidsOnlyOnAPrescription(t *testing.T) {
	u := NewUnstick()
	s := fieldSnap()
	if u.Demand(s) != nil {
		t.Fatal("bid without a prescription")
	}
	if !u.Prescribe(unstick.Rx{Kind: "stuck", Site: s.Me.Pos, Area: 3, Opened: time.Now()}) {
		t.Fatal("prescription refused")
	}
	d := u.Demand(s)
	if d == nil || d.Class != arbiter.ClassRecover || d.Who != "unstick" {
		t.Fatalf("demand %+v", d)
	}
	s.Me.HPPct = 0
	if u.Demand(s) != nil {
		t.Fatal("bid while dead")
	}
	// A stale prescription nobody started expires.
	u2 := NewUnstick()
	u2.Prescribe(unstick.Rx{Kind: "stuck", Opened: time.Now().Add(-time.Minute)})
	if u2.Demand(fieldSnap()) != nil {
		t.Fatal("stale prescription still bid")
	}
	if _, ok := u2.Open(); ok {
		t.Fatal("stale prescription still open")
	}
}

func TestUnstickPrescriptionUpgradeAndEnd(t *testing.T) {
	u := NewUnstick()
	now := time.Now()
	u.Prescribe(unstick.Rx{Kind: "pinned", Portal: true, Opened: now})
	if u.Prescribe(unstick.Rx{Kind: "stuck", Opened: now}) {
		t.Fatal("a waiting portal prescription was downgraded to footwork")
	}
	u = NewUnstick()
	u.Prescribe(unstick.Rx{Kind: "stuck", Site: data.Position{X: 50, Y: 50}, Area: 3, Opened: now})
	u.Begin(&Ctx{}, false)
	if u.PhaseName() != "Stride" {
		t.Fatalf("phase %q", u.PhaseName())
	}
	if !u.Prescribe(unstick.Rx{Kind: "stuck", Portal: true, Opened: now}) || u.PhaseName() != "CastTP" {
		t.Fatalf("running footwork not upgraded: %q", u.PhaseName())
	}
	// No town-portal binding: the portal remedy is honestly refused, nothing actuates.
	st := u.Step(&Ctx{Snap: fieldSnap()})
	if st.V != phase.Abandoned || st.Why != phase.Precondition {
		t.Fatalf("status %+v", st)
	}
	u.End(nil, st.V, st.Why)
	if _, ok := u.Open(); ok || u.PhaseName() != "" || u.Demand(fieldSnap()) != nil {
		t.Fatal("End left the prescription open")
	}
}

func TestLegacyForwardsJudged(t *testing.T) {
	adv := NewAdvance(nil)
	var l Life = &Legacy{A: adv}
	j, ok := l.(Judgeable)
	if !ok {
		t.Fatal("Legacy does not forward Judged")
	}
	j.Judged(nil, watchdog.Verdict{Pathology: watchdog.Stuck}) // inert without a live intent; must not panic
}
