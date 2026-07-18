// Package arbiter is LAW 1: one decision, one activity holding the actuator per cycle.
// Strict class ordering; cross-class promotion is FORBIDDEN (the herb can never outbid
// the corpse again); within a class, commitment is hysteresis — never a sleep.
package arbiter

import (
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
	MinHold      time.Duration // held-time before a same-class rival may evict
	SwitchMargin float64       // rival must exceed holder's urgency by this much
}

type Demand struct {
	Who     string
	Class   Class
	Urgency float64 // ranks WITHIN class only
	Commit  Commitment
}

// Grant is the arbiter's decision: who holds the actuator and for how long they've held it.
type Grant struct {
	Demand  Demand
	Since   time.Time     // wall time of grant
	held    time.Duration // accumulated held time (survives preemption pauses)
	resumed time.Time
}

func (g *Grant) Held() time.Duration {
	if g == nil {
		return 0
	}
	return g.held + time.Since(g.resumed)
}

// Arbiter owns the single grant.
type Arbiter struct {
	cur *Grant
}

// Current returns the standing grant (nil if none).
func (a *Arbiter) Current() *Grant { return a.cur }

// Release ends the current grant (activity finished or abandoned).
func (a *Arbiter) Release() { a.cur = nil }

// Decide picks the winner. Returns the grant and whether it changed holders this cycle.
func (a *Arbiter) Decide(demands []Demand) (*Grant, bool) {
	if len(demands) == 0 {
		a.cur = nil
		return nil, false
	}
	best := demands[0]
	for _, d := range demands[1:] {
		if d.Class < best.Class || (d.Class == best.Class && d.Urgency > best.Urgency) {
			best = d
		}
	}
	if a.cur == nil {
		a.cur = &Grant{Demand: best, Since: time.Now(), resumed: time.Now()}
		return a.cur, true
	}
	cur := a.cur
	if best.Who == cur.Demand.Who {
		cur.Demand.Urgency = best.Urgency // refresh the holder's own urgency
		return cur, false
	}
	// 0. A grant lives only as long as its demand: an incumbent that STOPPED bidding
	// has nothing to hold with — release it and seat the best live bidder. (Measured
	// 2026-07-19: Breakout stopped bidding at HP 0 but kept the grant, and Respawn —
	// a lower CLASS — could never evict it: 25s+ frozen on the death screen.)
	stillBids := false
	for _, d := range demands {
		if d.Who == cur.Demand.Who {
			stillBids = true
			break
		}
	}
	if !stillBids {
		a.cur = &Grant{Demand: best, Since: time.Now(), resumed: time.Now()}
		return a.cur, true
	}
	// 1. Fear overrides dwell: a strictly higher CLASS preempts immediately.
	if best.Class < cur.Demand.Class {
		cur.held += time.Since(cur.resumed)
		a.cur = &Grant{Demand: best, Since: time.Now(), resumed: time.Now()}
		return a.cur, true
	}
	// 2. Same class: hysteresis — MinHold AND SwitchMargin both required.
	if best.Class == cur.Demand.Class &&
		cur.Held() >= cur.Demand.Commit.MinHold &&
		best.Urgency >= cur.Demand.Urgency+cur.Demand.Commit.SwitchMargin {
		cur.held += time.Since(cur.resumed)
		a.cur = &Grant{Demand: best, Since: time.Now(), resumed: time.Now()}
		return a.cur, true
	}
	// 3. The incumbent keeps the actuator.
	return cur, false
}
