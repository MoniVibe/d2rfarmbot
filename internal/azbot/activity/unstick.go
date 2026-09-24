// Unstick carries out the watchdog's prescriptions (docs/AZBOT_V2.md step 9).
// The watchdog convicts and prescribes; the executive benches the culprit and
// opens a prescription here; Unstick bids (ClassRecover) only while one is open
// and performs it as an ordinary holder: gated by the janitor, preemptible by
// survival, timeboxed per phase, ended with a typed verdict. It replaces the
// executive's own escape strides, the pocket breaker's blocking TP and the
// deadman/pacer TP that fired whoever held the grant.
//
// The decisions live in the pure unstick package; this file only performs the
// one verb it names — Stride, CastSelf (town portal) or EnterPortal.
package activity

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/nav"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/unstick"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

const (
	// rxShelfLife: a prescription nobody could start (survival held the
	// grant) is stale — the watchdog will judge the new place afresh.
	rxShelfLife = 20 * time.Second
	// unstickUrgency: over Reclaim (0.9) so a stuck corpse run is unstuck,
	// under Respawn (1.0). (Relog is session-owned since step 8: while it
	// runs nothing below the session bids.)
	unstickUrgency = 0.92
)

type Unstick struct {
	mc    *unstick.Machine
	rx    *unstick.Rx
	begun bool
	ng    *nav.Grid
	ngSrc *game.Grid
}

var (
	_ Life   = (*Unstick)(nil)
	_ Phased = (*Unstick)(nil)
)

func NewUnstick() *Unstick {
	return &Unstick{mc: unstick.New(nil, func(l string) {
		if PhaseSink != nil {
			PhaseSink(l)
		}
	})}
}

func (u *Unstick) Name() string { return "unstick" }

// Prescribe opens a prescription. A waiting one is replaced unless that would
// downgrade a portal to footwork; a running footwork episode is upgraded by a
// portal prescription. Reports whether anything changed.
func (u *Unstick) Prescribe(rx unstick.Rx) bool {
	if u.rx == nil || !u.begun {
		if u.rx != nil && u.rx.Portal && !rx.Portal {
			return false
		}
		r := rx
		u.rx = &r
		return true
	}
	if rx.Portal && u.mc.Upgrade(rx) {
		u.rx.Portal = true
		return true
	}
	return false
}

// Open reports the open prescription.
func (u *Unstick) Open() (unstick.Rx, bool) {
	if u.rx == nil {
		return unstick.Rx{}, false
	}
	return *u.rx, true
}

func (u *Unstick) Demand(s *percept.Snapshot) *arbiter.Demand {
	if u.rx == nil {
		return nil
	}
	if !u.begun && time.Since(u.rx.Opened) > rxShelfLife {
		u.rx = nil // the moment passed
		return nil
	}
	if !s.Valid || s.Me.HPPct <= 0 {
		return nil
	}
	return &arbiter.Demand{Who: u.Name(), Class: arbiter.ClassRecover, Urgency: unstickUrgency,
		Commit: arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (u *Unstick) Needs(*percept.Snapshot) Needs { return needsWorld }

func (u *Unstick) Begin(ctx *Ctx, resumed bool) {
	if u.rx == nil {
		return
	}
	u.begun = true
	if ph := u.mc.Phase(); !resumed || ph == unstick.Idle || ph == unstick.Done || ph == unstick.Abandoned {
		u.mc.Start(*u.rx)
	}
}

// grid is the nav view of the live grid, rebuilt only when the executive
// regrows it (clearance is a whole-grid pass).
func (u *Unstick) grid(ctx *Ctx) *nav.Grid {
	if ctx.Grid == nil {
		return nil
	}
	if ctx.Grid != u.ngSrc {
		u.ng, u.ngSrc = journey.NavGrid(ctx.Grid), ctx.Grid
	}
	return u.ng
}

func (u *Unstick) Step(ctx *Ctx) Status {
	if u.rx == nil {
		return Status{V: phase.Abandoned, Why: phase.NoTarget, Evidence: "no open prescription"}
	}
	s := ctx.Snap
	if s == nil || !s.Valid {
		return Status{V: phase.Blocked, Phase: u.PhaseName()}
	}
	o := unstick.Obs{Pos: s.Me.Pos, Area: int(s.Me.Area), InTown: s.Me.InTown,
		CanPortal: ctx.Cap != nil && ctx.Cap.TownTP != nil}
	if ctx.Held != nil {
		o.Held = ctx.Held(u.Name())
	}
	for _, pt := range s.Portals {
		if !verbs.IsDeadDoor(pt.ID) {
			o.Portals = append(o.Portals, unstick.Portal{ID: pt.ID, Pos: pt.Pos})
		}
	}
	if u.mc.Phase() == unstick.Stride {
		o.Grid = u.grid(ctx)
	}
	d := u.mc.Step(o)
	switch d.Act.Kind {
	case unstick.ActStride:
		verbs.Stride{To: d.Act.To, Hold: d.Act.Hold, MinGain: d.Act.MinGain, Planned: d.Act.Planned}.
			Do(ctx.M, ctx.GR, ctx.P, ctx.Led, u.Name())
	case unstick.ActCast:
		if d.Act.First {
			// Cursed ground (a map door near two firings is disbelieved), and
			// Return must not ride the fresh portal back into the pen (02:28).
			NoteBreakerSite(u.rx.Site)
			MarkPortalHot(4 * time.Minute)
		}
		verbs.CastSelf{Key: ctx.Cap.TownTP.Key}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, u.Name())
	case unstick.ActEnter:
		verbs.EnterPortal{Target: d.Act.Portal.ID, TargetPos: d.Act.Portal.Pos}.
			Do(ctx.M, ctx.GR, ctx.P, ctx.Led, u.Name())
	}
	return Status{V: d.V, Why: d.Why, Phase: u.PhaseName(), Evidence: d.Evidence, WakeAt: d.WakeAt}
}

func (u *Unstick) Suspend(ctx *Ctx, _ phase.Reason) {
	if ctx != nil && ctx.M != nil {
		ctx.M.MoveStop()
	}
}

// End closes the prescription whatever the verdict: the watchdog judges the
// place afresh and prescribes again if it must.
func (u *Unstick) End(*Ctx, phase.Verdict, phase.Reason) {
	u.rx, u.begun = nil, false
}

// PhaseName is the state line's phase= ("" with no prescription).
func (u *Unstick) PhaseName() string {
	if u.rx == nil {
		return ""
	}
	return u.mc.Phase().String()
}
