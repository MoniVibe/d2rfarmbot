package activity

import (
	"fmt"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- the campaign's act gates
//
// OWNER (2026-09-26): "an idempotent bot capable of running the entire diablo
// campaign". Every act change so far was the owner's hand (Warriv, Meshif).
// An act ends when its boss dies; the next act is a TALK away (Warriv "Go
// East", Jerhyn then Meshif "Sail East", Tyrael to Harrogath) or a red portal
// (Mephisto's, handled by Advance's portal hop). The quest log reads 0 on this
// build, so the boss's death is witnessed live and persisted per character.

func bossKey(char string, act int) string { return fmt.Sprintf("bossdown.%s.act%d", char, act) }

var bosses = struct {
	sync.Mutex
	char string
	down map[int]bool
}{}

// ObserveBosses is called by the executive every tick: an act boss seen dying
// or dead is persisted (idempotent: a restart, a relog or a crash keeps it).
func ObserveBosses(s *percept.Snapshot, mem *memory.Store, char string) {
	if s == nil || !s.Valid || mem == nil || char == "" {
		return
	}
	bosses.Lock()
	defer bosses.Unlock()
	if bosses.char != char || bosses.down == nil {
		bosses.char, bosses.down = char, map[int]bool{}
		for act := 1; act <= 5; act++ {
			var v bool
			if mem.GetJSON(bossKey(char, act), &v) && v {
				bosses.down[act] = true
			}
		}
	}
	// Standing in act N proves every earlier act's boss died (the owner's
	// hand, or saves older than the witness).
	for act := 1; act < s.Me.Area.Act(); act++ {
		if !bosses.down[act] {
			bosses.down[act] = true
			mem.PutJSON(bossKey(char, act), memory.ScopeForever,
				memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("stood in act %d", s.Me.Area.Act())}, true)
		}
	}
	for _, id := range s.Me.BossesDead {
		act := percept.ActBoss[id]
		if act == 0 || bosses.down[act] {
			continue
		}
		bosses.down[act] = true
		mem.PutJSON(bossKey(char, act), memory.ScopeForever,
			memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("boss npc %d seen dead in area %d", int(id), int(s.Me.Area))}, true)
	}
}

// BossDown: the act's boss is known dead for the current character.
func BossDown(act int) bool {
	bosses.Lock()
	defer bosses.Unlock()
	return bosses.down[act]
}

// markBossDown: an act left by any road proves the boss behind it died
// (the owner played it by hand, or a save predates the witness).
func markBossDown(mem *memory.Store, act int) {
	bosses.Lock()
	defer bosses.Unlock()
	if bosses.down == nil || bosses.down[act] {
		return
	}
	bosses.down[act] = true
	if mem != nil && bosses.char != "" {
		mem.PutJSON(bossKey(bosses.char, act), memory.ScopeForever,
			memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("stood in act %d", act+1)}, true)
	}
}

// voyageLeg: one act's way out by talk.
type voyageLeg struct {
	town    area.ID
	pre     npc.ID // a talk the gate needs first (Jerhyn after Duriel); 0 = none
	npc     npc.ID
	nextAct int
	label   string
}

var voyageLegs = map[int]voyageLeg{
	1: {town: area.RogueEncampment, npc: npc.Warriv, nextAct: 2, label: "Warriv: Go East"},
	2: {town: area.LutGholein, pre: npc.Jerhyn, npc: npc.Meshif, nextAct: 3, label: "Meshif: Sail East"},
	4: {town: area.ThePandemoniumFortress, npc: npc.Tyrael2, nextAct: 5, label: "Tyrael: to Harrogath"},
}

// ---------------------------------------------------------------- Voyage (ClassTravel)

type Voyage struct {
	e       errand
	leg     voyageLeg
	preDone bool      // the prerequisite talk happened on this trip
	slot    int       // menu slot tried on this talk
	enterAt time.Time // when the slot was entered: the act change is awaited
	coolAt  time.Time
	mem     *memory.Store
}

var (
	_ Life   = (*Voyage)(nil)
	_ Phased = (*Voyage)(nil)
)

func NewVoyage() *Voyage {
	v := &Voyage{e: errand{npcID: npc.Warriv}}
	v.e.initLife(v.Name())
	return v
}

func (v *Voyage) Name() string { return "voyage" }

func (v *Voyage) PhaseName() string { return v.e.ph.Phase().String() }

func (v *Voyage) Demand(s *percept.Snapshot) *arbiter.Demand { return v.e.keepBid(v.demand(s), s) }

func (v *Voyage) demand(s *percept.Snapshot) *arbiter.Demand {
	if !campaignMode || !s.Valid || !s.Me.InTown || time.Now().Before(v.coolAt) {
		return nil
	}
	act := s.Me.Area.Act()
	leg, ok := voyageLegs[act]
	if !ok || s.Me.Area != leg.town || !BossDown(act) {
		return nil
	}
	if ServicesPending(s) {
		return nil // the bag and the belt first: the next act starts in a strange town
	}
	v.leg = leg
	return &arbiter.Demand{Who: v.Name(), Class: arbiter.ClassTravel, Urgency: 0.6,
		Commit: arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (v *Voyage) Needs(*percept.Snapshot) Needs { return v.e.needs() }

func (v *Voyage) Begin(ctx *Ctx, resumed bool) {
	v.mem = ctx.Mem
	v.e.begin(resumed)
	if resumed {
		v.e.resume(ctx)
		return
	}
	v.e.resetTrip()
	v.target(v.leg.pre)
	if v.leg.pre == 0 || v.preDone {
		v.target(v.leg.npc)
	}
}

// target points the errand at an NPC (its town seat comes from the live presets).
func (v *Voyage) target(id npc.ID) {
	if id == 0 {
		return
	}
	v.e.npcID, v.e.act1NPC, v.e.act2NPC, v.e.act3NPC = id, id, id, id
}

func (v *Voyage) Suspend(ctx *Ctx, _ phase.Reason) { v.e.suspend(ctx) }

func (v *Voyage) End(ctx *Ctx, verdict phase.Verdict, why phase.Reason) {
	v.e.end(ctx, verdict, why)
	v.e.resetTrip()
	v.enterAt = time.Time{}
	if verdict == phase.Abandoned {
		v.coolAt = time.Now().Add(60 * time.Second)
	}
}

// voyageSlots: the Down-counts tried in turn (Talk is always first; the
// travel line is usually second, below a quest line when one is up).
var voyageSlots = []int{1, 2, 3}

func (v *Voyage) Step(ctx *Ctx) Status {
	e := &v.e
	if st, stop := e.pre(ctx); stop {
		return st
	}
	s := ctx.Snap
	if s.Me.Area.Act() != v.leg.town.Act() {
		markBossDown(v.mem, v.leg.town.Act())
		ctx.Led.Append(verbs.Outcome{Verb: "voyage", Holder: v.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("%s: now in act %d (area %d)", v.leg.label, s.Me.Area.Act(), int(s.Me.Area))})
		return e.finish(ctx, phase.Done, phase.Completed, "act changed")
	}
	// A slot was entered: the load screen (snapshot invalid) or the new act
	// is the proof. Six seconds of the same town = wrong slot.
	if !v.enterAt.IsZero() {
		if time.Since(v.enterAt) < 6*time.Second {
			return e.wait(250 * time.Millisecond)
		}
		v.enterAt = time.Time{}
		v.slot++
		if v.slot >= len(voyageSlots) {
			return e.finish(ctx, phase.Abandoned, phase.Refused, fmt.Sprintf("%s: no menu slot sailed", v.leg.label))
		}
		e.clickAt = time.Time{}
		e.to(erClean, fmt.Sprintf("slot %d did not sail: talk again", v.slot))
		return e.running()
	}
	// Our talk opened the menu: the prerequisite talk is done by opening it;
	// the voyage talk picks the travel line.
	if e.ph.Phase() >= erTalk && s.MenuOpen && talkedRecently(e.clickAt, time.Now()) {
		if v.leg.pre != 0 && !v.preDone && e.npcID == v.leg.pre {
			v.preDone = true
			ctx.Led.Append(verbs.Outcome{Verb: "voyage", Holder: v.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("prerequisite talk with npc %d done", int(v.leg.pre))})
			v.target(v.leg.npc)
			e.clickAt = time.Time{}
			e.to(erClean, "prerequisite talked: close and find the captain")
			return e.running()
		}
		ctx.M.MoveStop()
		ctx.M.RealKey(0x24) // HOME
		for i := 0; i < voyageSlots[v.slot]; i++ {
			ctx.M.RealKey(0x28) // DOWN
		}
		ctx.M.RealKey(0x0D) // ENTER
		v.enterAt = time.Now()
		ctx.Led.Append(verbs.Outcome{Verb: "voyage", Holder: v.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("%s: entered menu slot %d", v.leg.label, voyageSlots[v.slot])})
		return e.wait(300 * time.Millisecond)
	}
	st, _, dead := e.drive(ctx, v.Name())
	if dead {
		v.coolAt = time.Now().Add(60 * time.Second)
	}
	return st
}

// ---------------------------------------------------------------- ActEnd (ClassTravel)
//
// The act's boss is dead: the march has nothing left here ("grind the
// summit" used to hold him on the boss's floor forever). Acts 1, 2 and 4 end
// in town (Voyage takes the talk); Act 3 ends through Mephisto's red portal.

type ActEnd struct {
	adv *Advance
	u   Unload // the roads home: Withdraw's portal ride, or the lit pad
}

func NewActEnd(adv *Advance) *ActEnd { return &ActEnd{adv: adv} }

func (ae *ActEnd) Name() string { return "actend" }

func (ae *ActEnd) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !campaignMode || !s.Valid || s.Me.InTown || s.Me.HPPct < 40 {
		return nil
	}
	act := s.Me.Area.Act()
	if !BossDown(act) || act == 5 {
		return nil
	}
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 12 {
			return nil // Fight clears the pocket; loot still has its minute
		}
	}
	if act == 3 {
		if s.Me.Area != area.DuranceOfHateLevel3 {
			return nil // the march brings him to the bridge
		}
	} else {
		ae.u.byPad = false
		if !liveDoor(s) && (!townPortalBound || s.Me.TPScrolls == 0 || time.Now().Before(ae.u.w.coolAt)) {
			if !wpHomeReady(s) {
				return nil
			}
			ae.u.byPad = true
		}
	}
	return &arbiter.Demand{Who: ae.Name(), Class: arbiter.ClassTravel, Urgency: 0.7,
		Commit: arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (ae *ActEnd) Step(ctx *Ctx) Verdict {
	if ctx.Snap.Me.Area == area.DuranceOfHateLevel3 {
		return ae.adv.enterObjectPortal(ctx, object.HellGate, "Mephisto's red portal to the Pandemonium Fortress")
	}
	if ae.u.byPad {
		return ae.u.rideHome(ctx)
	}
	return ae.u.w.ride(ctx, ae.Name())
}
