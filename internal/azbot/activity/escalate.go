// escalate.go — THE ESCALATION LADDER (the owner: "orbit/stuck loops where the
// watchdog 'cools' a holder 6-8s and hands the same problem around"). Pure
// cooling repeats the same failed plan after the window; a LADDER climbs to a
// DIFFERENT remedy each time the SAME committed intent keeps failing:
//
//	rung 0 REPLAN    — throw away the stale plan and replan via Journey
//	rung 1 ALTERNATE — strike the map exit as a bad door; borderTarget picks
//	                   another door / the live-room / the search tour
//	rung 2 PORTAL    — hand back so the existing Return / reroute portal
//	                   machinery can ride the network (reused, not reinvented)
//	rung 3 FAILLEG   — the leg is proven unwalkable: persist the strike and let
//	                   the march fall to the next itinerary option
//
// The watchdog stays as a backstop, but Advance now CONSUMES its verdicts into
// the ladder (NoteWatchdog) instead of only being cooled. Every rung is
// ledgered. The whole ladder is inert unless the deliberate flag is armed.
package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

type ladderRung int

const (
	rungReplan ladderRung = iota
	rungAlternate
	rungPortal
	rungFailLeg
)

func (r ladderRung) String() string {
	switch r {
	case rungReplan:
		return "replan"
	case rungAlternate:
		return "alternate"
	case rungPortal:
		return "portal"
	default:
		return "fail-leg"
	}
}

// ladderKey is the ladder's identity — the committed campaign leg. As long as
// the same target keeps failing the ladder climbs; a new target resets it.
func (a *Advance) ladderKey() string {
	return fmt.Sprintf("intent.%d", int(a.intent.Area))
}

// escalate climbs the ladder one rung for `key` (or resets to rung 0 on a new
// key) and returns the rung to act on. Each call is ledgered. Callers invoke it
// only under the deliberate flag.
func (a *Advance) escalate(led *verbs.Ledger, key, reason string) ladderRung {
	if a.ladKey != key {
		a.ladKey, a.ladRung, a.ladAt = key, rungReplan, time.Now()
	} else if a.ladRung < rungFailLeg {
		a.ladRung++
		a.ladAt = time.Now()
	}
	if led != nil {
		led.Append(verbs.Outcome{Verb: "escalate", Holder: a.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("rung %d (%s) for %s: %s", int(a.ladRung), a.ladRung.String(), key, reason)})
	}
	return a.ladRung
}

// NoteWatchdog lets Advance CONSUME a watchdog verdict: instead of only being
// cooled, a stuck/orbit/thrash conviction against the march climbs the ladder,
// so when Advance next wins the grant it reaches for a DIFFERENT remedy rather
// than repeating the plan the watchdog just convicted. Inert without the flag
// or a live committed intent. The executive keeps applying its cooldown as the
// backstop.
func (a *Advance) NoteWatchdog(kind string, led *verbs.Ledger) {
	if !deliberate || !a.intent.Active() {
		return
	}
	a.escalate(led, a.ladderKey(), "watchdog "+kind)
}
