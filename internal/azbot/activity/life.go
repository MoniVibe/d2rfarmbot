package activity

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// Activity contract v2 (docs/AZBOT_V2.md): the executive owns the lifecycle.
// Begin/Suspend/End are driven from the arbiter's Changes (exec.Core), so a
// preempted activity is told so instead of discovering it next grant.
type (
	Needs  = exec.Needs
	Status = exec.Status
)

type Life interface {
	Name() string
	Demand(s *percept.Snapshot) *arbiter.Demand
	Needs(s *percept.Snapshot) Needs
	Begin(ctx *Ctx, resumed bool)
	Step(ctx *Ctx) Status
	Suspend(ctx *Ctx, why phase.Reason) // MoveStop only; no new clicks
	End(ctx *Ctx, v phase.Verdict, why phase.Reason)
}

// Phased is an activity that can name its current phase (the state line's phase=).
type Phased interface{ PhaseName() string }

// Legacy adapts a v1 Activity to the v2 contract without changing what it does:
// Running→Running, Done→Done(Completed), Abandoned→Abandoned(NoReason). Begin
// and End are no-ops; Suspend releases the move key and nothing else.
type Legacy struct {
	A Activity
	N Needs
}

var (
	_ Life            = (*Legacy)(nil)
	_ exec.Life[*Ctx] = (*Legacy)(nil)
	_ Phased          = (*Legacy)(nil)
)

func (l *Legacy) Name() string                               { return l.A.Name() }
func (l *Legacy) Demand(s *percept.Snapshot) *arbiter.Demand { return l.A.Demand(s) }
func (l *Legacy) Needs(*percept.Snapshot) Needs              { return l.N }
func (l *Legacy) Begin(*Ctx, bool)                           {}
func (l *Legacy) End(*Ctx, phase.Verdict, phase.Reason)      {}

func (l *Legacy) Suspend(ctx *Ctx, _ phase.Reason) {
	if ctx != nil && ctx.M != nil {
		ctx.M.MoveStop()
	}
}

func (l *Legacy) Step(ctx *Ctx) Status {
	v := l.A.Step(ctx)
	st := Status{Phase: l.PhaseName()}
	switch v {
	case Done:
		st.V, st.Why = phase.Done, phase.Completed
	case Abandoned:
		st.V, st.Why = phase.Abandoned, phase.NoReason
	default:
		st.V = phase.Running
	}
	return st
}

// PhaseName is the wrapped activity's phase, "" when it has none.
func (l *Legacy) PhaseName() string {
	if p, ok := l.A.(Phased); ok {
		return p.PhaseName()
	}
	return ""
}

// allPanels: every panel bit — the services open trade, bag, dialogs and menus
// today and nothing narrows which.
var allPanels = func() screen.Panel {
	var p screen.Panel
	for _, q := range screen.All {
		p |= q
	}
	return p
}()

var (
	needsWorld   = Needs{Mode: exec.ModeOf(screen.World)}
	needsService = Needs{Mode: exec.ModeOf(screen.World), Claims: allPanels, Cursor: exec.CursorOwn}
	// Respawn answers the death screen; Relog walks the pause and main menus. Both
	// act where the world-bound activities must not, so neither is mode-gated.
	needsAnyMode = Needs{Mode: exec.AnyMode, Cursor: exec.CursorAny}
)

// Roster is the ONE ordered activity list, shared by the live executive and
// -replay. Order is priority of registration: exact bid ties go to the earlier
// entry (arbiter Demand.Order).
type Roster struct {
	Acts    []Life
	Advance *Advance
	Fight   *Fight
	byName  map[string]Life
}

// Registry builds the roster. road is the hand-piloted town road (nil on a
// foreign seed, and in replay).
func Registry(legs []Leg, road []data.Position) *Roster {
	adv := NewAdvance(legs)
	fight := NewFight()
	fight.March = adv.MarchGoal // P-5.8: the door mouth is shot open
	svc := func(a Activity) Life { return &Legacy{A: a, N: needsService} }
	world := func(a Activity) Life { return &Legacy{A: a, N: needsWorld} }
	free := func(a Activity) Life { return &Legacy{A: a, N: needsAnyMode} }
	r := &Roster{Advance: adv, Fight: fight, Acts: []Life{
		world(&Breakout{}), world(&Stand{}), world(&Flee{March: adv.MarchGoal}), world(NewDodge()),
		free(&Respawn{}), free(NewRelog()), world(NewReclaim()), world(fight),
		world(NewLoot()), world(NewImbibe()), svc(NewFence()), svc(NewRestock()), svc(NewRepair()),
		svc(NewHeal()), svc(NewIdentify()), svc(NewEquip()), svc(NewSpend()), world(adv),
		world(NewWithdraw()), world(&Return{}), world(&Travel{Road: road}), world(&Explore{Frontier: adv.FrontierFor}),
	}}
	r.byName = make(map[string]Life, len(r.Acts))
	for _, a := range r.Acts {
		r.byName[a.Name()] = a
	}
	return r
}

// Get finds an activity by name (nil if unknown).
func (r *Roster) Get(name string) Life { return r.byName[name] }

// Demands collects this tick's bids in registry order (Order = index).
func (r *Roster) Demands(s *percept.Snapshot) []arbiter.Demand {
	return exec.Collect(r.Acts, func(a Life) *arbiter.Demand { return a.Demand(s) })
}

// Lifecycle adapts the roster to exec.Core.
func (r *Roster) Lifecycle(name string) exec.Life[*Ctx] {
	if a := r.byName[name]; a != nil {
		return a
	}
	return nil
}
