package exec

import (
	"testing"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

func TestCadence(t *testing.T) {
	c := &Cadence{EveryN: 2, MinGap: 100 * time.Millisecond}
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	if !c.Due(1, at(0)) {
		t.Fatal("first capture is always due")
	}
	if c.Due(2, at(200)) {
		t.Fatal("every 2nd tick: tick 2 is not due")
	}
	if !c.Due(3, at(240)) {
		t.Fatal("tick 3 is due")
	}
	if c.Due(5, at(300)) {
		t.Fatal("MinGap: 60ms after the last capture is too soon")
	}
	c.Kick()
	if c.Due(6, at(330)) {
		t.Fatal("a kick still honours MinGap")
	}
	if !c.Due(6, at(345)) {
		t.Fatal("a kick captures on the next tick past MinGap")
	}
}

func TestLatencyQuantiles(t *testing.T) {
	var l Latency
	for i := 1; i <= 100; i++ {
		l.Add(time.Duration(i) * time.Millisecond)
	}
	p50, p99, max, n := l.Flush()
	if p50 != 50*time.Millisecond || p99 != 99*time.Millisecond || max != 100*time.Millisecond || n != 100 {
		t.Fatalf("p50=%v p99=%v max=%v n=%d", p50, p99, max, n)
	}
	if l.Len() != 0 {
		t.Fatal("flush starts a new window")
	}
	if _, _, _, n := l.Flush(); n != 0 {
		t.Fatal("empty window")
	}
}

func TestEye(t *testing.T) {
	var e Eye
	if e.Latest() != nil {
		t.Fatal("nil before the first publish")
	}
	e.Publish(Seen{Tick: 3, State: screen.State{Mode: screen.World, Panels: screen.Inventory}})
	if s := e.Latest(); s == nil || s.Tick != 3 || !s.State.Panels.Has(screen.Inventory) {
		t.Fatalf("latest: %+v", s)
	}
}
