package exec

import (
	"sort"
	"strings"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/trace"
)

// Core turns the arbiter's Changes into lifecycle calls, so cleanup is the
// executive's guarantee rather than each activity's memory:
//
//	Preempt/Outbid  → old.Suspend, new.Begin(resumed)
//	Released        → old.End, new.Begin(resumed)
//	Fresh           → new.Begin(resumed)
//	terminal Step / monitor verdict → End (the caller, via Core.End)
//
// resumed means the arbiter still had held time for the activity (its episode
// survived a preemption). Every Begin is closed by exactly one End.
type Core[C any] struct {
	Arb   *arbiter.Arbiter
	Find  func(who string) Life[C] // nil result = no lifecycle (replay, unknown names)
	Trace func(line string)        // nil = silent
	Tick  uint64                   // stamped on trace lines; the caller advances it

	open     map[string]bool      // begun and not yet ended (holding or suspended)
	wake     map[string]time.Time // parked by a Wait: not Stepped before this time
	last     arbiter.Change       // last non-keep change, for flight frames
	lastTick uint64
}

func (c *Core[C]) find(who string) Life[C] {
	if c.Find == nil || who == "" {
		return nil
	}
	return c.Find(who)
}

func (c *Core[C]) emit(line string) {
	if c.Trace != nil {
		c.Trace(line)
	}
}

// Open reports whether who's episode is live (begun, not ended).
func (c *Core[C]) Open(who string) bool { return c.open[who] }

// Last is the most recent grant change and the tick it happened on.
func (c *Core[C]) Last() (arbiter.Change, uint64) { return c.last, c.lastTick }

func (c *Core[C]) begin(ctx C, who string, resumed bool) {
	if who == "" {
		return
	}
	if c.open == nil {
		c.open = map[string]bool{}
	}
	resumed = resumed || c.open[who]
	c.open[who] = true
	delete(c.wake, who) // a new seat re-verifies at once (Begin), never sleeps on
	if l := c.find(who); l != nil {
		l.Begin(ctx, resumed)
	}
}

func (c *Core[C]) suspend(ctx C, who string, why phase.Reason) {
	if !c.open[who] {
		return
	}
	delete(c.wake, who)
	if l := c.find(who); l != nil {
		l.Suspend(ctx, why)
	}
}

// close tells the activity its episode is over; false if it was not open.
func (c *Core[C]) close(ctx C, who string, v phase.Verdict, why phase.Reason) bool {
	if !c.open[who] {
		return false
	}
	delete(c.open, who)
	delete(c.wake, who)
	if l := c.find(who); l != nil {
		l.End(ctx, v, why)
	}
	return true
}

// Park records a Step's Wait: until st.WakeAt the holder keeps its grant but
// is not Stepped (Asleep). Any other verdict, or a Wait with no WakeAt, clears
// the park — the holder Steps next tick. Arbitration still runs every tick, so
// a survival bid preempts a sleeper exactly as it would a runner (and the
// preemption drops the park: the resumed Begin re-verifies at once).
func (c *Core[C]) Park(who string, st Status) {
	if who == "" {
		return
	}
	if st.V != phase.Wait || st.WakeAt.IsZero() {
		delete(c.wake, who)
		return
	}
	if c.wake == nil {
		c.wake = map[string]time.Time{}
	}
	c.wake[who] = st.WakeAt
}

// Asleep reports whether who is parked by a Wait at now.
func (c *Core[C]) Asleep(who string, now time.Time) bool {
	t, ok := c.wake[who]
	if !ok {
		return false
	}
	if !now.Before(t) {
		delete(c.wake, who)
		return false
	}
	return true
}

// releasedReason types the arbiter's rule-zero release.
func releasedReason(reason string) phase.Reason {
	if strings.Contains(reason, " benched: ") {
		return phase.Judged
	}
	return phase.NoTarget
}

// Decide runs the arbiter over this tick's demands and applies the lifecycle.
func (c *Core[C]) Decide(ctx C, ds []arbiter.Demand) (*arbiter.Grant, arbiter.Change) {
	cur := ""
	if g := c.Arb.Current(); g != nil {
		cur = g.Demand.Who
	}
	pre := make(map[string]time.Duration, len(ds))
	bidding := make(map[string]bool, len(ds))
	for _, d := range ds {
		bidding[d.Who] = true
		if d.Who != cur {
			pre[d.Who] = c.Arb.Held(d.Who)
		}
	}
	g, ch := c.Arb.Decide(ds)
	switch ch.Kind {
	case arbiter.Preempt:
		c.suspend(ctx, ch.From, phase.Preempted)
		c.begin(ctx, ch.To, pre[ch.To] > 0)
	case arbiter.Outbid:
		c.suspend(ctx, ch.From, phase.Outbid)
		c.begin(ctx, ch.To, pre[ch.To] > 0)
	case arbiter.Released:
		c.close(ctx, ch.From, phase.Abandoned, releasedReason(ch.Reason))
		c.begin(ctx, ch.To, pre[ch.To] > 0)
	case arbiter.Fresh:
		// From is the holder the executive already Ended; a caller that
		// released the arbiter directly still gets its End here.
		c.close(ctx, ch.From, phase.Abandoned, phase.NoReason)
		c.begin(ctx, ch.To, pre[ch.To] > 0)
	}
	if ch.Changed() {
		c.last, c.lastTick = ch, c.Tick
		c.emit(trace.Grant(ch, c.Tick))
	}
	// A suspended activity that stopped bidding has lost its ledger (the arbiter
	// forgets withdrawn bidders): its episode is over.
	holder := ""
	if g != nil {
		holder = g.Demand.Who
	}
	var gone []string
	for who := range c.open {
		if who != holder && !bidding[who] {
			gone = append(gone, who)
		}
	}
	sort.Strings(gone)
	for _, who := range gone {
		c.close(ctx, who, phase.Abandoned, phase.NoTarget)
		c.emit(trace.Life(who, "end", trace.EndWhy(phase.Abandoned, phase.NoTarget, "withdrawn while suspended"), c.Tick))
	}
	return g, ch
}

// End closes who's episode — a terminal Step verdict or a monitor's judgment
// (watchdog, stall). The activity hears End, the arbiter forgets the episode
// (releasing the grant if who holds), and the trace names the cause.
func (c *Core[C]) End(ctx C, who string, v phase.Verdict, why phase.Reason, detail string) {
	if who == "" {
		return
	}
	c.close(ctx, who, v, why)
	c.Arb.End(who, trace.EndWhy(v, why, detail))
	c.last = arbiter.Change{From: who, Kind: arbiter.Released, Reason: trace.EndWhy(v, why, detail)}
	c.lastTick = c.Tick
	c.emit(trace.Ended(who, v, why, detail, c.Tick))
}
