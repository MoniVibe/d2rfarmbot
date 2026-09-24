// escalate.go — THE ESCALATION LADDER (the owner: "orbit/stuck loops where the
// watchdog 'cools' a holder 6-8s and hands the same problem around"). Pure
// cooling repeats the same failed plan after the window; a LADDER climbs to a
// DIFFERENT remedy each time the SAME committed intent keeps failing:
//
//	rung 0 REPLAN    — throw away the stale plan and replan via Journey
//	rung 1 ALTERNATE — disbelieve the current door; borderTarget picks
//	                   another source / the live rooms / the search tour
//	rung 2 PORTAL    — let the existing reroute machinery judge a network
//	                   ride now (reused, not reinvented)
//	rung 3 FAILLEG   — doubt every door for a window and hand the march to
//	                   the coverage search, on a new bearing each cycle
//
// Past FAIL-LEG the ladder wraps (route.Ladder): run w topped out at rung 3 and
// logged it 20+ times in 20 min while nothing acted on it. The watchdog stays
// as a backstop; Advance CONSUMES its verdicts (NoteWatchdog) and applies the
// rung on its next Step. Every rung is ledgered. Inert unless deliberate.
package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/route"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/azbot/watchdog"
)

// ladderKey is the ladder's identity — the committed campaign leg. As long as
// the same target keeps failing the ladder climbs; a new target resets it.
func (a *Advance) ladderKey() string {
	return fmt.Sprintf("intent.%d", int(a.intent.Area))
}

// escalate climbs the ladder one rung for `key` (or restarts at rung 0 on a
// new key or a quiet ladder) and returns the rung to act on. The rung is left
// pending for applyRung. Each call is ledgered. Callers invoke it only under
// the deliberate flag.
func (a *Advance) escalate(led *verbs.Ledger, key, reason string) route.Rung {
	a.lad = a.lad.Climb(key, time.Now())
	a.ladPending = true
	if led != nil {
		led.Append(verbs.Outcome{Verb: "escalate", Holder: a.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("rung %d (%s) for %s cycle %d: %s", int(a.lad.Rung), a.lad.Rung, key, a.lad.Cycle, reason)})
	}
	return a.lad.Rung
}

// applyRung acts on the pending rung — the part the old ladder never did for
// watchdog verdicts: the rung was logged and the same march resumed.
func (a *Advance) applyRung(ctx *Ctx) {
	if !a.ladPending {
		return
	}
	a.ladPending = false
	now := time.Now()
	ev := ""
	switch a.lad.Rung {
	case route.Replan:
		a.j, a.grid = nil, nil
		ev = "fresh plan"
	case route.Alternate:
		a.j = nil
		if a.marchGoal != (data.Position{}) {
			a.disbelief.Add(a.marchGoal, now.Add(3*time.Minute))
			ev = fmt.Sprintf("door (%d,%d) disbelieved 3m", a.marchGoal.X, a.marchGoal.Y)
		} else {
			ev = "no door to disbelieve"
		}
	case route.Portal:
		a.j, a.grid = nil, nil
		a.rerouteCool = time.Time{} // the reroute judges a network ride this tick
		ev = "reroute cooldown lifted"
	default:
		w := route.SearchWindow(a.lad.Cycle)
		a.disbelief.Blind(now.Add(w))
		a.searchUntil = now.Add(w)
		a.heading = route.TurnHeading(a.heading, len(bearings))
		a.j = nil
		ev = fmt.Sprintf("every door doubted %s; coverage search on bearing %d", w, a.heading%len(bearings))
	}
	if ctx != nil && ctx.Led != nil {
		ctx.Led.Append(verbs.Outcome{Verb: "escalate", Holder: a.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("apply %s for %s cycle %d: %s", a.lad.Rung, a.lad.Key, a.lad.Cycle, ev)})
	}
}

// NoteWatchdog lets Advance CONSUME a watchdog verdict: instead of only being
// cooled, a stuck/orbit conviction against the march climbs the ladder, so
// when Advance next wins the grant it reaches for a DIFFERENT remedy rather
// than repeating the plan the watchdog just convicted. Inert without the flag
// or a live committed intent. The executive keeps applying its cooldown as the
// backstop.
func (a *Advance) NoteWatchdog(kind string, led *verbs.Ledger) {
	if !deliberate || !a.intent.Active() {
		return
	}
	// THRASH is grant churn among holders (run w: fight/stand/advance handing
	// over 7-14 times in 30s), not evidence the route is wrong — it alone drove
	// intent.56 to FAIL-LEG in 32s. The cooldown still applies.
	if kind == "thrash" {
		return
	}
	a.escalate(led, a.ladderKey(), "watchdog "+kind)
}

// Judged is Advance's side of the executive's verdict hook (Judgeable): the
// verdict climbs the ladder before the bench lands.
func (a *Advance) Judged(ctx *Ctx, v watchdog.Verdict) {
	var led *verbs.Ledger
	if ctx != nil {
		led = ctx.Led
	}
	a.NoteWatchdog(v.Pathology.String(), led)
}
