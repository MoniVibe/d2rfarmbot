// Package arbiter is LAW 1: one decision, one activity holding the actuator per cycle.
// Strict class ordering; cross-class promotion is FORBIDDEN (the herb can never outbid
// the corpse again); within a class, commitment is hysteresis — never a sleep.
package arbiter

import (
	"fmt"
	"time"
)

type Class uint8

const (
	ClassSurvive Class = iota // flee, hazard escape — preempts everything immediately
	ClassRecover              // corpse recovery, respawn, re-arm
	ClassFight
	ClassLoot
	ClassTravel
	ClassService
	ClassExplore
	ClassIdle // the null demand; never granted over anything real
)

func (c Class) String() string {
	switch c {
	case ClassSurvive:
		return "survive"
	case ClassRecover:
		return "recover"
	case ClassFight:
		return "fight"
	case ClassLoot:
		return "loot"
	case ClassTravel:
		return "travel"
	case ClassService:
		return "service"
	case ClassExplore:
		return "explore"
	default:
		return "idle"
	}
}

type Commitment struct {
	MinHold      time.Duration // stint before a same-class rival may evict
	SwitchMargin float64       // rival must exceed holder's urgency by this much
}

type Demand struct {
	Who     string
	Class   Class
	Urgency float64 // ranks WITHIN class only
	Commit  Commitment
	// Order is the bidder's registration index: the tie-break after urgency, so an
	// exact tie never rides Go's map order. Equal Orders (e.g. all zero) fall back
	// to Who — deterministic either way.
	Order int
}

// better is the total order Decide ranks by: class, urgency, registration, name.
func better(a, b Demand) bool {
	if a.Class != b.Class {
		return a.Class < b.Class
	}
	if a.Urgency != b.Urgency {
		return a.Urgency > b.Urgency
	}
	if a.Order != b.Order {
		return a.Order < b.Order
	}
	return a.Who < b.Who
}

// Clock is injectable so decision tests (and replay) run on recorded time.
type Clock interface{ Now() time.Time }

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// Grant is the arbiter's decision: who holds the actuator, and since when.
type Grant struct {
	Demand Demand
	Since  time.Time // start of THIS stint (a resumed activity gets a new Since, not a new ledger)
	a      *Arbiter
}

// Held is the holder's episode total from the arbiter's ledger (survives preemption).
func (g *Grant) Held() time.Duration {
	if g == nil {
		return 0
	}
	if g.a == nil {
		return time.Since(g.Since)
	}
	return g.a.Held(g.Demand.Who)
}

// Kind classifies a Decide outcome.
type Kind uint8

const (
	Keep     Kind = iota // incumbent kept (or still nobody)
	Fresh                // seated with no incumbent
	Preempt              // strictly higher class evicted the holder
	Outbid               // same class, past MinHold and by SwitchMargin
	Released             // holder stopped bidding (or was benched); best bidder, if any, seated
)

func (k Kind) String() string {
	switch k {
	case Keep:
		return "keep"
	case Fresh:
		return "fresh"
	case Preempt:
		return "preempt"
	case Outbid:
		return "outbid"
	case Released:
		return "released"
	default:
		return "?"
	}
}

// Change says what a Decide did and why — the executive logs it verbatim.
type Change struct {
	From, To string // "" = nobody
	Kind     Kind
	Reason   string
}

// Changed reports a holder change (the old Decide bool).
func (c Change) Changed() bool { return c.Kind != Keep }

// Why is "kind: reason", the log's why= field.
func (c Change) Why() string {
	if c.Reason == "" {
		return c.Kind.String()
	}
	return c.Kind.String() + ": " + c.Reason
}

func (c Change) String() string {
	return fmt.Sprintf("%s -> %s (%s)", orDash(c.From), orDash(c.To), c.Why())
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type benchSlot struct {
	until time.Time
	why   string
}

// Arbiter owns the single grant, the per-activity held ledger, the bench and the
// progress clocks. The zero value is ready (wall clock).
type Arbiter struct {
	Clock Clock // nil = wall clock

	cur     *Grant
	runFrom time.Time // when the holder's held clock last started
	blocked bool      // holder can't act (gate): its held clock is stopped

	// held is closed-out time per activity. It survives preemption so deadlines
	// don't reset when survival interrupts; it dies with the episode (End/Release,
	// a released grant, or the bidder withdrawing its demand).
	held     map[string]time.Duration
	evidence map[string]time.Duration // Held(who) at its last MarkProgress
	bench    map[string]benchSlot

	endedWho, endedWhy string // the last ended holder, named by the next Fresh
}

func (a *Arbiter) now() time.Time {
	if a.Clock == nil {
		return time.Now()
	}
	return a.Clock.Now()
}

func (a *Arbiter) init() {
	if a.held == nil {
		a.held = map[string]time.Duration{}
		a.evidence = map[string]time.Duration{}
		a.bench = map[string]benchSlot{}
	}
}

// Current returns the standing grant (nil if none).
func (a *Arbiter) Current() *Grant { return a.cur }

// Stint is how long the current grant has stood (MinHold's clock); 0 with no holder.
func (a *Arbiter) Stint() time.Duration {
	if a.cur == nil {
		return 0
	}
	return a.now().Sub(a.cur.Since)
}

// Held is who's episode total: time it held the grant unblocked, across preemptions.
func (a *Arbiter) Held(who string) time.Duration {
	h := a.held[who]
	if a.cur != nil && a.cur.Demand.Who == who && !a.blocked {
		h += a.now().Sub(a.runFrom)
	}
	return h
}

// SetBlocked stops (true) or restarts (false) the holder's held clock — the gate
// is refusing it this tick, so its deadlines must not burn.
func (a *Arbiter) SetBlocked(on bool) {
	if a.cur == nil || on == a.blocked {
		return
	}
	a.init()
	if on {
		a.pause()
	} else {
		a.runFrom = a.now()
	}
	a.blocked = on
}

// Blocked reports whether the holder's clock is stopped.
func (a *Arbiter) Blocked() bool { return a.cur != nil && a.blocked }

// pause folds the running holder's clock into its ledger.
func (a *Arbiter) pause() {
	if a.cur != nil && !a.blocked {
		a.held[a.cur.Demand.Who] += a.now().Sub(a.runFrom)
	}
}

func (a *Arbiter) seat(d Demand) *Grant {
	a.pause()
	now := a.now()
	a.cur = &Grant{Demand: d, Since: now, a: a}
	a.runFrom, a.blocked = now, false
	return a.cur
}

// forget ends who's episode: its ledger and progress clock go.
func (a *Arbiter) forget(who string) {
	delete(a.held, who)
	delete(a.evidence, who)
}

// End closes who's episode (Done/Abandoned). If who holds, the grant is released.
func (a *Arbiter) End(who, why string) {
	a.init()
	if a.cur != nil && a.cur.Demand.Who == who {
		a.cur = nil
		a.blocked = false
		a.endedWho, a.endedWhy = who, why
	}
	a.forget(who)
}

// Release ends the current grant (activity finished or abandoned).
func (a *Arbiter) Release() {
	if a.cur != nil {
		a.End(a.cur.Demand.Who, "released")
	}
}

// Bench bars who from being granted until the given time. A benched holder is
// released on the next Decide.
func (a *Arbiter) Bench(who string, until time.Time, why string) {
	a.init()
	a.bench[who] = benchSlot{until: until, why: why}
}

// Benched reports whether who is barred right now, and why. Expired slots drop.
func (a *Arbiter) Benched(who string) (string, bool) {
	b, ok := a.bench[who]
	if !ok {
		return "", false
	}
	if !a.now().Before(b.until) {
		delete(a.bench, who)
		return "", false
	}
	return b.why, true
}

// MarkProgress resets who's "last evidence" clock (an outcome, a phase change).
func (a *Arbiter) MarkProgress(who string) {
	a.init()
	a.evidence[who] = a.Held(who)
}

// Silence is how much of who's held time has passed since its last evidence —
// measured on the held clock, so preempted and blocked time never count as mute.
func (a *Arbiter) Silence(who string) time.Duration {
	return a.Held(who) - a.evidence[who]
}

// Mute reports whether the HOLDER has produced no progress for bar.
func (a *Arbiter) Mute(who string, bar time.Duration) bool {
	return a.cur != nil && a.cur.Demand.Who == who && a.Silence(who) > bar
}

// Decide picks the winner and says why. Benched bids are filtered here.
func (a *Arbiter) Decide(demands []Demand) (*Grant, Change) {
	a.init()
	endedWho, endedWhy := a.endedWho, a.endedWhy
	a.endedWho, a.endedWhy = "", ""

	bidding := make(map[string]bool, len(demands))
	var best Demand
	have := false
	holderIn, holderBench := false, ""
	for _, d := range demands {
		bidding[d.Who] = true
		if why, ok := a.Benched(d.Who); ok {
			if a.cur != nil && d.Who == a.cur.Demand.Who {
				holderBench = why
			}
			continue
		}
		if a.cur != nil && d.Who == a.cur.Demand.Who {
			holderIn = true
		}
		if !have || better(d, best) {
			best, have = d, true
		}
	}
	// A withdrawn demand ends that episode: stale held time must not greet the
	// next, unrelated engagement as an instant deadline.
	defer func() {
		for who := range a.held {
			if !bidding[who] {
				a.forget(who)
			}
		}
		for who := range a.evidence {
			if !bidding[who] {
				a.forget(who)
			}
		}
	}()

	if a.cur == nil {
		if !have {
			return nil, Change{Kind: Keep}
		}
		why := "no holder"
		if endedWho != "" {
			why = endedWho + " " + endedWhy
		}
		return a.seat(best), Change{From: endedWho, To: best.Who, Kind: Fresh, Reason: why}
	}
	cur := a.cur
	from := cur.Demand.Who
	if have && best.Who == from {
		cur.Demand.Urgency = best.Urgency // refresh the holder's own urgency
		return cur, Change{From: from, To: from, Kind: Keep}
	}
	// 0. A grant lives only as long as its demand: an incumbent that STOPPED bidding
	// has nothing to hold with — release it and seat the best live bidder. (Measured
	// 2026-07-19: Breakout stopped bidding at HP 0 but kept the grant, and Respawn —
	// a lower CLASS — could never evict it: 25s+ frozen on the death screen.)
	if !holderIn {
		why := from + " stopped bidding"
		if holderBench != "" {
			why = from + " benched: " + holderBench
		}
		a.End(from, why)
		a.endedWho, a.endedWhy = "", ""
		if !have {
			return nil, Change{From: from, Kind: Released, Reason: why}
		}
		return a.seat(best), Change{From: from, To: best.Who, Kind: Released, Reason: why}
	}
	// 1. Fear overrides dwell: a strictly higher CLASS preempts immediately.
	// The loser's ledger stays — it resumes where it left off.
	if best.Class < cur.Demand.Class {
		why := best.Class.String() + " > " + cur.Demand.Class.String()
		return a.seat(best), Change{From: from, To: best.Who, Kind: Preempt, Reason: why}
	}
	// 2. Same class: hysteresis — MinHold AND SwitchMargin both required.
	if best.Class == cur.Demand.Class {
		stint, c := a.Stint(), cur.Demand.Commit
		if stint < c.MinHold {
			return cur, Change{From: from, To: from, Kind: Keep,
				Reason: fmt.Sprintf("%s minhold %s < %s", best.Who, stint.Round(100*time.Millisecond), c.MinHold)}
		}
		if best.Urgency < cur.Demand.Urgency+c.SwitchMargin {
			return cur, Change{From: from, To: from, Kind: Keep,
				Reason: fmt.Sprintf("%s margin %.2f < %.2f+%.2f", best.Who, best.Urgency, cur.Demand.Urgency, c.SwitchMargin)}
		}
		why := fmt.Sprintf("%s %.2f >= %.2f+%.2f after %s", best.Class, best.Urgency,
			cur.Demand.Urgency, c.SwitchMargin, stint.Round(100*time.Millisecond))
		return a.seat(best), Change{From: from, To: best.Who, Kind: Outbid, Reason: why}
	}
	// 3. The incumbent keeps the actuator.
	return cur, Change{From: from, To: from, Kind: Keep,
		Reason: fmt.Sprintf("%s outranks %s", cur.Demand.Class, best.Class)}
}
