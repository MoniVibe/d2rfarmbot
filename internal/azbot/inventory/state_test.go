package inventory

import (
	"testing"
	"time"
)

func TestNamedStatesAndStuckPrescriptions(t *testing.T) {
	tr := NewTracker()
	t0 := time.Now()
	at := func(d time.Duration) time.Time { return t0.Add(d) }

	if ph, st, _, _ := tr.Observe(Observation{At: at(0), BagOpen: true, Planned: true}); ph != BagOpen || st != StuckNone {
		t.Fatalf("bag open with work: %v %v", ph, st)
	}
	// item held past the limit -> park or shed
	tr.Observe(Observation{At: at(1 * time.Second), Held: 42, BagOpen: true})
	if ph, st, rx, _ := tr.Observe(Observation{At: at(6 * time.Second), Held: 42, BagOpen: true}); ph != ItemHeld || st != StuckHeld || rx != RxParkOrShed {
		t.Fatalf("held 5s: want item held / held too long / park-or-shed, got %v %v %v", ph, st, rx)
	}
	// the same unit lifted three times in the window -> swap loop -> quarantine
	tr2 := NewTracker()
	for i := 0; i < 3; i++ {
		tr2.Observe(Observation{At: at(time.Duration(i*4) * time.Second), Held: 7})
		tr2.Observe(Observation{At: at(time.Duration(i*4+2) * time.Second)})
	}
	if _, st, rx, _ := tr2.Observe(Observation{At: at(13 * time.Second), Held: 7}); st != StuckSwapLoop || rx != RxQuarantine {
		t.Fatalf("four lifts in 13s: want swap loop / quarantine, got %v %v", st, rx)
	}
	// a move issued three times with nothing changing -> move not taking
	tr3 := NewTracker()
	for i := 0; i < 3; i++ {
		tr3.Issued("fill 9", 9, false)
	}
	if _, st, rx, _ := tr3.Observe(Observation{At: at(0), BagOpen: true, Planned: true}); st != StuckNoEffect || rx != RxQuarantine {
		t.Fatalf("three dead moves: want no-effect / quarantine, got %v %v", st, rx)
	}
	tr3.Quarantine(9, at(0))
	if !tr3.Quarantined(9, at(time.Minute)) || tr3.Quarantined(9, at(11*time.Minute)) {
		t.Fatal("quarantine lasts QuarantineFor")
	}
	// a panel with nothing to do -> close it
	tr4 := NewTracker()
	tr4.Observe(Observation{At: at(0), StashOpen: true})
	if _, st, rx, _ := tr4.Observe(Observation{At: at(7 * time.Second), StashOpen: true}); st != StuckPanelIdle || rx != RxClosePanel {
		t.Fatalf("idle stash 7s: want panel idle / close, got %v %v", st, rx)
	}
}
