package screen

import (
	"testing"
	"time"
)

func reading(m Mode, p Panel, ev map[Panel]string) Reading {
	if ev == nil {
		ev = map[Panel]string{}
	}
	return Reading{Mode: m, Panels: p, Sight: p, Evidence: ev}
}

// A fake clock: tick i happens at t0 + i*200ms.
func tick(i int) time.Time {
	return time.Unix(1_000_000, 0).Add(time.Duration(i) * 200 * time.Millisecond)
}

func TestTrackerDebounce(t *testing.T) {
	tr := NewTracker(0) // default N=2
	bag := map[Panel]string{Inventory: "right-half panel"}
	seq := []struct {
		r    Reading
		want string // believed state after the update
		log  string // transition emitted on this update, "" for none
	}{
		{reading(World, 0, nil), "unknown", ""},
		{reading(World, 0, nil), "world", "screen: unknown -> world"},
		{reading(World, Inventory, bag), "world", ""}, // one frame is a rumor
		{reading(World, 0, nil), "world", ""},         // ...and it went away
		{reading(World, Inventory, bag), "world", ""},
		{reading(World, Inventory, bag), "world+inventory", "screen: world -> world+inventory (right-half panel)"},
		{reading(World, Inventory, bag), "world+inventory", ""},
		{reading(World, 0, nil), "world+inventory", ""},
		{reading(World, 0, nil), "world", "screen: world+inventory -> world (inventory gone)"},
	}
	for i, s := range seq {
		tn, changed := tr.Update(tick(i), s.r)
		if got := tr.State().String(); got != s.want {
			t.Fatalf("step %d: state %q, want %q", i, got, s.want)
		}
		if changed != (s.log != "") || (changed && tn.String() != s.log) {
			t.Fatalf("step %d: transition %v %q, want %q", i, changed, tn, s.log)
		}
		if changed && (!tn.At.Equal(tick(i)) || !tr.Since().Equal(tick(i))) {
			t.Fatalf("step %d: at %v since %v", i, tn.At, tr.Since())
		}
	}
}

// Unsure is no vote: blindness neither confirms nor refutes a believed panel.
func TestTrackerUnsureNoVote(t *testing.T) {
	tr := NewTracker(2)
	pause := reading(World, PauseMenu, map[Panel]string{PauseMenu: "grey pause buttons"})
	tr.Update(tick(0), pause)
	tr.Update(tick(1), pause)
	if !tr.State().Panels.Has(PauseMenu) {
		t.Fatal("pause not believed")
	}
	blind := reading(World, 0, nil)
	blind.Unsure = PauseMenu
	for i := 2; i < 6; i++ {
		if _, ch := tr.Update(tick(i), blind); ch {
			t.Fatalf("unsure flipped the belief at %d", i)
		}
	}
	// A clear, sighted frame then an unsure one then clear: the streak survives
	// the blind frame, so two clears still close it.
	tr.Update(tick(6), reading(World, 0, nil))
	tr.Update(tick(7), blind)
	tn, ch := tr.Update(tick(8), reading(World, 0, nil))
	if !ch || tr.State().Panels != 0 || tn.Evidence != "pause gone" {
		t.Fatalf("pause did not clear: %v %v", ch, tn)
	}
}

func TestTrackerModeAndCursor(t *testing.T) {
	tr := NewTracker(3)
	for i := 0; i < 3; i++ {
		tr.Update(tick(i), reading(World, 0, nil))
	}
	// Mode candidates must agree with EACH OTHER: dead, loading, dead is noise.
	tr.Update(tick(3), reading(Dead, 0, nil))
	tr.Update(tick(4), reading(Loading, 0, nil))
	tr.Update(tick(5), reading(Dead, 0, nil))
	if tr.State().Mode != World {
		t.Fatalf("mode flipped on disagreeing frames: %s", tr.State())
	}
	tr.Update(tick(6), reading(Dead, 0, nil))
	tn, ch := tr.Update(tick(7), reading(Dead, 0, nil))
	if !ch || tr.State().Mode != Dead || tn.String() != "screen: world -> dead" {
		t.Fatalf("dead not believed: %v %v", ch, tn)
	}
	cur := reading(Dead, 0, nil)
	cur.CursorItem = true
	tr.Update(tick(8), cur)
	tr.Update(tick(9), cur)
	tn, ch = tr.Update(tick(10), cur)
	if !ch || tn.String() != "screen: dead -> dead+cursor (cursor item)" {
		t.Fatalf("cursor: %v %v", ch, tn)
	}
}

// N=1 is an undebounced mirror — useful for the log-only first wiring.
func TestTrackerN1(t *testing.T) {
	tr := NewTracker(1)
	if _, ch := tr.Update(tick(0), reading(World, Shop, nil)); !ch || tr.State().String() != "world+shop" {
		t.Fatalf("N=1: %s", tr.State())
	}
}
