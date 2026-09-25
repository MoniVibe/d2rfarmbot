package activity

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/watchdog"
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

// Judgeable is an activity that consumes a watchdog verdict against it (Advance
// climbs its escalation ladder, so the plan it returns to is a different one).
// The executive calls it before benching the culprit; it must not actuate.
type Judgeable interface {
	Judged(ctx *Ctx, v watchdog.Verdict)
}

// Judged forwards a verdict to the wrapped activity when it consumes them.
func (l *Legacy) Judged(ctx *Ctx, v watchdog.Verdict) {
	if j, ok := l.A.(Judgeable); ok {
		j.Judged(ctx, v)
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

// Claims per service (docs/AZBOT_V2.md: "Claims live only while it holds the
// grant"). While the holder owns them the janitor leaves these panels alone;
// the moment it loses the grant they are foreign and the janitor closes them by
// sight. The bag, char sheet and skill tree are named by sight (relay R2); the
// generic half panels stay claimed beside them because a frame no detector
// names (an unphotographed panel, another client size) reads as Left/RightPanel.
const (
	// Identify / Equip: the bag the ID-tome cast opens.
	claimsBag = screen.Inventory | screen.RightPanel
	// Spend: the char sheet (left) and the skill tree (right).
	claimsSpend = screen.CharSheet | screen.SkillTree | screen.LeftPanel | screen.RightPanel
	// Vendor errands (Fence, Restock, Repair, Heal): NPC menu and speech, the
	// trade window (the vendor IS the left frame) and the bag beside it.
	claimsVendor = screen.NPCMenu | screen.NPCDialog | screen.Shop | screen.Inventory |
		screen.RightPanel | screen.LeftPanel
)

// needsService: a town service acts in the world, owns the cursor (bag work,
// parking) and claims exactly what its current phase opens.
func needsService(claims screen.Panel) Needs {
	return Needs{Mode: exec.ModeOf(screen.World), Claims: claims, Cursor: exec.CursorOwn}
}

var (
	needsWorld = Needs{Mode: exec.ModeOf(screen.World)}
	// Respawn answers the death screen, where the world-bound activities must
	// not act, so it is not mode-gated. (Relog walked the menus here until
	// step 8; the session owns it now — exec.Session.)
	needsAnyMode = Needs{Mode: exec.AnyMode, Cursor: exec.CursorAny}
)

// Roster is the ONE ordered activity list, shared by the live executive and
// -replay. Order is priority of registration: exact bid ties go to the earlier
// entry (arbiter Demand.Order).
type Roster struct {
	Acts    []Life
	Advance *Advance
	Fight   *Fight
	Unstick *Unstick // carries out the watchdog's prescriptions
	Recall  *Recall  // the session's wind-down town road (exec.WindTown)
	byName  map[string]Life
}

// Registry builds the roster. road is the hand-piloted town road (nil on a
// foreign seed, and in replay).
func Registry(legs []Leg, road []data.Position) *Roster {
	adv := NewAdvance(legs)
	fight := NewFight()
	fight.March = adv.MarchGoal // P-5.8: the door mouth is shot open
	// The town services are native v2 (step 7): phase enums, per-phase claims,
	// no sleeps. Everything else rides the Legacy adapter.
	world := func(a Activity) Life { return &Legacy{A: a, N: needsWorld} }
	free := func(a Activity) Life { return &Legacy{A: a, N: needsAnyMode} }
	unst := NewUnstick()
	recall := NewRecall()
	r := &Roster{Advance: adv, Fight: fight, Unstick: unst, Recall: recall, Acts: []Life{
		world(&Breakout{}), world(&Stand{}), world(&Flee{March: adv.MarchGoal}), world(NewDodge()),
		free(&Respawn{}), unst, world(recall), world(NewReclaim()), world(fight),
		NewDiscard(), world(NewLoot()), world(NewImbibe()), world(NewHaul()), NewFence(), NewRestock(),
		NewRepair(), NewHeal(), NewCainIdentify(), NewStash(), NewBelt(), NewShed(), // owner 2026-09-24: Cain identifies; keepers go to the stash
		NewEquip(), NewSpend(), world(adv),
		world(NewWithdraw()), world(NewUnload()), world(NewDoor()), world(&Return{}), world(&Travel{Road: road}), world(&Explore{Frontier: adv.FrontierFor, Bias: adv.ExploreBias}),
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
