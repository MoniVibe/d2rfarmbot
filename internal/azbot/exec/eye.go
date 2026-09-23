package exec

import (
	"math"
	"sort"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// Cadence bounds how often the executive photographs the screen: every Nth
// tick, never closer than MinGap, and at once after a Kick (an activity just
// clicked a panel). A capture is a full-window PrintWindow + pixel swap — not
// free at 25 ticks/s.
type Cadence struct {
	EveryN int           // 0 or 1 = every tick
	MinGap time.Duration // floor between captures

	last   time.Time
	lastN  uint64
	kicked bool
}

// Kick asks for a capture on the next Due (the MinGap floor still holds).
func (c *Cadence) Kick() { c.kicked = true }

// Due reports whether tick should capture; a true answer is taken as done.
func (c *Cadence) Due(tick uint64, now time.Time) bool {
	if !c.last.IsZero() && now.Sub(c.last) < c.MinGap {
		return false
	}
	n := uint64(c.EveryN)
	if n < 1 {
		n = 1
	}
	if !c.kicked && !c.last.IsZero() && tick-c.lastN < n {
		return false
	}
	c.last, c.lastN, c.kicked = now, tick, false
	return true
}

// Latency keeps a window of samples for p50/p99 reporting.
type Latency struct {
	s []time.Duration
}

func (l *Latency) Add(d time.Duration) { l.s = append(l.s, d) }

// Len is the samples in the current window.
func (l *Latency) Len() int { return len(l.s) }

// Flush returns p50, p99 and max of the window and starts a new one.
func (l *Latency) Flush() (p50, p99, max time.Duration, n int) {
	n = len(l.s)
	if n == 0 {
		return 0, 0, 0, 0
	}
	s := append([]time.Duration(nil), l.s...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	l.s = l.s[:0]
	return Quantile(s, 0.50), Quantile(s, 0.99), s[n-1], n
}

// Quantile of a SORTED slice, nearest-rank.
func Quantile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// Seen is one published screen observation: the Tracker's stable belief plus
// the raw Reading it last folded in.
type Seen struct {
	At      time.Time
	Tick    uint64
	State   screen.State   // debounced belief — what consumers should act on
	Reading screen.Reading // the latest raw observation (evidence, unsure, close points)
}

// Eye publishes the latest Seen for other goroutines (the Sentinel, later).
// Read-only in shadow mode: nothing gates on it yet.
type Eye struct{ p atomic.Pointer[Seen] }

// Publish replaces the current observation.
func (e *Eye) Publish(s Seen) { e.p.Store(&s) }

// Latest is the current observation; nil before the first capture.
func (e *Eye) Latest() *Seen { return e.p.Load() }
