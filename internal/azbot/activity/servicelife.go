package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// Town services on contract v2 (docs/AZBOT_V2.md step 7). Every service —
// Identify, Equip, Spend and the vendor errands — shares this lifecycle:
//
//   - a Stringer phase enum whose ZERO value is the CLEAN phase: the service
//     starts only from a screen with nothing positively seen. Clean claims
//     nothing, so with the janitor ON the gate closes every leftover (a shop
//     another errand left, a stray NPC menu, the bag) before Clean's Step; with
//     the janitor OFF the service closes them itself, by sight (tidy).
//   - Claims per phase: a panel is claimed only in the phases that open or use
//     it; outside them it is foreign and the janitor closes it.
//   - per-phase budgets in HELD time — overrun ⇒ Abandoned(Timebox).
//   - a typed Reason on every terminal verdict. With the janitor OFF a verdict
//     is latched while the CLOSE phase shuts the service's own panels by sight;
//     with it ON, End drops the claims and the janitor does it.
//   - Suspend: MoveStop, no clicks. Begin(resumed) re-verifies the phase.
//   - End resets the episode.
//
// Nothing here sleeps: a wait is Status{V: Wait, WakeAt}.

// svcLife is the shared lifecycle state over one service's phase enum P.
type svcLife[P phase.Phase] struct {
	ph     phase.Phaser[P]
	close  P                    // the sight-closing phase (janitor OFF)
	claims func(P) screen.Panel // panels the service may own in each phase

	since time.Time       // entry into the clean or close phase: a clear verdict needs a newer reading
	pend  Status          // the terminal verdict latched while closing
	bid   *arbiter.Demand // last bid of this episode, kept while the service may own a panel
	live  bool            // between Begin and End
}

func (l *svcLife[P]) init(name string, closePh P, claims func(P) screen.Panel) {
	l.ph.Act, l.ph.Log = name, phaseLog
	l.close, l.claims = closePh, claims
}

// clean reports whether the current phase is the precondition phase (P's zero).
func (l *svcLife[P]) clean() bool {
	var zero P
	return l.ph.Phase() == zero
}

func (l *svcLife[P]) to(p P, why string) {
	var zero P
	if p != l.ph.Phase() && (p == zero || p == l.close) {
		l.since = time.Now()
	}
	l.ph.To(p, why)
}

// claim is what the current phase owns.
func (l *svcLife[P]) claim() screen.Panel { return l.claims(l.ph.Phase()) }

func (l *svcLife[P]) needs() Needs { return needsService(l.claim()) }

func (l *svcLife[P]) status(v phase.Verdict, why phase.Reason, ev string) Status {
	return Status{V: v, Why: why, Phase: l.ph.Phase().String(), Claim: l.claim(), Evidence: ev}
}

func (l *svcLife[P]) running() Status { return l.status(phase.Running, phase.NoReason, "") }

// wait parks the holder for d (the executive does not Step it before then).
func (l *svcLife[P]) wait(d time.Duration) Status {
	st := l.status(phase.Wait, phase.NoReason, "")
	st.WakeAt = time.Now().Add(d)
	return st
}

// finish ends the episode with v/why. Janitor ON (or nothing of ours can be
// up): at once — End drops the claims and the janitor closes what is left by
// sight. Janitor OFF: latch the verdict and close our own panels first.
func (l *svcLife[P]) finish(ctx *Ctx, v phase.Verdict, why phase.Reason, ev string) Status {
	if JanitorOn || l.clean() || l.claim() == 0 {
		return l.status(v, why, ev)
	}
	l.pend = Status{V: v, Why: why, Evidence: ev}
	l.to(l.close, fmt.Sprintf("closing before %s/%s", v, why))
	return l.closing(ctx)
}

// closing is the close phase's Step: the sight closer until a fresh reading is
// clear, then the latched verdict. A wedge gives up closing, never the verdict.
func (l *svcLife[P]) closing(ctx *Ctx) Status {
	clear, st := tidy(ctx, l.ph.Act, 0, l.since)
	switch {
	case clear:
		return l.status(l.pend.V, l.pend.Why, l.pend.Evidence)
	case st.V == phase.Abandoned:
		return l.status(l.pend.V, l.pend.Why, l.pend.Evidence+"; close: "+st.Evidence)
	}
	out := l.status(phase.Wait, phase.NoReason, st.Evidence)
	out.WakeAt = st.WakeAt
	return out
}

// cleanScreen is the clean phase's precondition: nothing positively seen but
// keep. Janitor ON: the gate closed every foreign panel before this Step
// (clean claims only keep), so a sighting here is its lag — wait. Janitor
// OFF: the service's own sight closer.
func (l *svcLife[P]) cleanScreen(ctx *Ctx, keep screen.Panel) (bool, Status) {
	if JanitorOn {
		if r := ctx.Screen; r != nil && exec.Positive(*r)&^keep != 0 {
			return false, l.wait(150 * time.Millisecond)
		}
		return true, Status{}
	}
	clear, st := tidy(ctx, l.ph.Act, keep, l.since)
	if clear {
		return true, Status{}
	}
	if st.V == phase.Abandoned {
		return false, l.status(phase.Abandoned, st.Why, st.Evidence)
	}
	out := l.status(phase.Wait, phase.NoReason, st.Evidence)
	out.WakeAt = st.WakeAt
	return false, out
}

// interlocked: something the current phase does not claim is positively seen
// (a vendor window, an NPC menu, the other half-panel) — never click into it.
// The caller goes back to Clean, which closes it (janitor, or by sight). With
// the janitor ON the gate already refused such a Step; this is the OFF path's
// guard (2026-09-24: Spend clicked New Stats and pressed 'C' into Drognan's
// open trade window — the memory NPCShop flag read false).
func (l *svcLife[P]) interlocked(ctx *Ctx) (screen.Panel, bool) {
	seen, ok := seenPanels(ctx)
	if !ok {
		return 0, false
	}
	f := seen &^ l.claim()
	return f, f != 0
}

// overrun: the current phase spent more than its held-time budget —
// Abandoned(Timebox), closing first (janitor OFF). An overrun CLOSE phase
// reports the latched verdict as it stands.
func (l *svcLife[P]) overrun(ctx *Ctx) (Status, bool) {
	over, p := l.ph.Overrun(heldOf(ctx, l.ph.Act))
	if !over {
		return Status{}, false
	}
	if p == l.close {
		return l.status(l.pend.V, l.pend.Why, l.pend.Evidence+"; close over its budget"), true
	}
	return l.finish(ctx, phase.Abandoned, phase.Timebox, fmt.Sprintf("phase %s over its held-time budget", p)), true
}

// keepBid: while the episode may own a panel (a claiming phase), the service
// keeps its last bid, so the arbiter never releases it with its shop or bag up
// (2026-09-24: Fence went solvent mid-sale, its Demand went nil, the release
// left Drognan's trade window open and Spend clicked into it). The Step then
// sees its goal met and ends honestly — closing first when the janitor is off.
func (l *svcLife[P]) keepBid(d *arbiter.Demand, s *percept.Snapshot) *arbiter.Demand {
	if d != nil {
		c := *d
		l.bid = &c
		return d
	}
	if l.live && l.bid != nil && l.claim() != 0 && !l.clean() && s.Valid && s.Me.InTown {
		c := *l.bid
		return &c
	}
	return nil
}

func (l *svcLife[P]) begin(resumed bool) {
	l.live = true
	if !resumed {
		l.ph.Reset()
		l.since, l.pend = time.Now(), Status{}
	}
}

// suspend: MoveStop only; no new clicks (contract v2).
func (l *svcLife[P]) suspend(ctx *Ctx) {
	if ctx != nil && ctx.M != nil {
		ctx.M.MoveStop()
	}
}

// end closes the episode. Janitor OFF: a release that came from outside the
// Step (a monitor, a withdrawn bid while suspended) still gets one sight-close
// action for whatever of ours is seen — never a blind key.
func (l *svcLife[P]) end(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	if !JanitorOn && ctx != nil && ctx.M != nil && l.claim() != 0 {
		tidy(ctx, l.ph.Act, 0, time.Time{})
	}
	if !l.clean() {
		var zero P
		l.ph.To(zero, fmt.Sprintf("end: %s/%s", v, why))
	}
	l.ph.Reset()
	l.live, l.bid, l.pend = false, nil, Status{}
}

// heldOf is who's held time (0 without a ledger: no budget ever overruns).
func heldOf(ctx *Ctx, who string) time.Duration {
	if ctx == nil || ctx.Held == nil {
		return 0
	}
	return ctx.Held(who)
}

// ---------------------------------------------------------------- the sight closer

// svcJanitor paces the services' own sight closer (janitor OFF): the
// executive janitor's rate limit, fresh-reading rule and wedge detector.
var (
	svcJanitor = exec.NewJanitor()
	tidyAt     time.Time // the sight closer's last action
)

// tidySettle: a reading must be this much newer than an action (or the phase
// entry) to judge it — the Tracker wants two agreeing frames (~100ms apart).
const tidySettle = 350 * time.Millisecond

// tidy is ONE step of a service's own sight closer toward a screen with
// nothing positively seen but keep: screen.CloseStep over the stable Reading
// (exec.Eye), exactly the janitor's policy — a seen X is clicked, an ESC only
// for a positively observed ESC-closable panel, never with the pause menu up.
// clear=true when a reading at least tidySettle newer than since (and than
// the last action) shows nothing to close. Otherwise st is a Wait (acted,
// rate, stale reading, nothing safe to press) or Abandoned(UIWedge).
func tidy(ctx *Ctx, who string, keep screen.Panel, since time.Time) (bool, Status) {
	seen := ctx.Seen
	if seen == nil {
		return true, Status{} // no capture yet: the gate precedent — never block on blindness
	}
	now := time.Now()
	d := svcJanitor.Decide(now, seen, exec.HolderNeeds{Mode: exec.ModeOf(screen.World), Claims: keep, CursorOwn: true})
	if d.Open {
		if tidyAt.After(since) {
			since = tidyAt
		}
		if seen.At.Before(since.Add(tidySettle)) {
			return false, Status{V: phase.Wait, WakeAt: now.Add(100 * time.Millisecond), Evidence: "awaiting a fresh reading"}
		}
		return true, Status{}
	}
	if d.Wedged {
		return false, Status{V: phase.Abandoned, Why: phase.UIWedge, Evidence: "ui_wedge: " + d.Why}
	}
	if d.Act {
		act := performClose(ctx, d)
		svcJanitor.Acted(time.Now())
		tidyAt = time.Now()
		if ctx.Led != nil {
			ctx.Led.Append(verbs.Outcome{Verb: "tidy", Holder: who, Result: verbs.ResDone, Evidence: d.Why + " → " + act})
		}
		return false, Status{V: phase.Wait, WakeAt: now.Add(tidySettle), Evidence: act}
	}
	why := d.Why
	if d.Wait != "" {
		why += " [" + d.Wait + "]"
	}
	return false, Status{V: phase.Wait, WakeAt: now.Add(150 * time.Millisecond), Evidence: why}
}

// performClose turns one janitor decision into motor input (the executive's
// gatekeeper does the same with the janitor ON). Menus demand true
// foreground: Real* input only.
func performClose(ctx *Ctx, d exec.Decision) string {
	switch d.Action.Kind {
	case screen.ActClick:
		act := fmt.Sprintf("click %d,%d", d.Action.X, d.Action.Y)
		if !ctx.M.RealMenuClick(d.Action.X, d.Action.Y) {
			act += " (refused: no foreground)"
		}
		return act
	case screen.ActKey:
		act := fmt.Sprintf("key 0x%02X", d.Action.VK)
		if !ctx.M.RealKey(uint16(d.Action.VK)) {
			act += " (refused: no foreground)"
		}
		return act
	}
	return ""
}

// seenPanels: what the stable Reading positively shows (sight plus proven
// memory channels) — the services' interlocks read it. ok=false without a
// reading: callers never act on blindness as if it were proof of absence.
func seenPanels(ctx *Ctx) (screen.Panel, bool) {
	if ctx.Screen != nil {
		return exec.Positive(*ctx.Screen), true
	}
	if ctx.Seen != nil {
		return exec.Positive(exec.Stable(*ctx.Seen)), true
	}
	return 0, false
}
