package activity

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Identify at Cain (ClassService)

// OWNER RULING (2026-09-24, R13): identify at DECKARD CAIN, not with the ID tome.
// The tome ran dry mid-ritual, the follow-up click grabbed the item, and equip
// then looped on a cursor item that would not park. Cain identifies the whole
// bag in one menu pick and needs no charges.

// cainIDWorks: the live belief that Cain's "Identify Items" row identifies on
// this mod. Two full menu-slot sweeps with no count change retire it (NewWorld
// re-arms it after a relog), so a menu that never identifies cannot gate town.
var cainIDWorks atomic.Bool

func init() { cainIDWorks.Store(true) }

// cainIDSlots: the Down-count candidates for "Identify Items". Cain's menu is
// Talk / Identify Items / Cancel, so slot 1 comes first; quest lines can grow it.
var cainIDSlots = []int{1, 2, 0, 3}

// cainIDSettle: how long after ENTER the unidentified count may take to drop.
const cainIDSettle = 2 * time.Second

// CainIdentify walks to Cain (Act 1 camp: cain5; Lut Gholein: cain2), talks, and
// picks Identify Items. The postcondition is the unidentified count reaching 0.
type CainIdentify struct {
	e      errand
	sweeps int // full slot sweeps with no count drop: the disproof counter
	before int // unidentified count when ENTER was pressed
	coolAt time.Time
}

var (
	_ Life   = (*CainIdentify)(nil)
	_ Phased = (*CainIdentify)(nil)
)

func NewCainIdentify() *CainIdentify {
	c := &CainIdentify{e: errand{npcID: npc.DeckardCain5, act2NPC: npc.DeckardCain2}}
	c.e.initLife(c.Name())
	return c
}

// Name stays "identify": the shadow table, the docket and the traces know it by that.
func (c *CainIdentify) Name() string      { return "identify" }
func (c *CainIdentify) PhaseName() string { return c.e.ph.Phase().String() }

func (c *CainIdentify) Demand(s *percept.Snapshot) *arbiter.Demand {
	return c.e.keepBid(c.demand(s, time.Now()), s)
}

func (c *CainIdentify) demand(s *percept.Snapshot, now time.Time) *arbiter.Demand {
	if servicesCooled() {
		return nil
	}
	if !s.Valid || !s.Me.InTown || s.Me.UnidentCount == 0 || !cainIDWorks.Load() || now.Before(c.coolAt) {
		return nil
	}
	return &arbiter.Demand{Who: c.Name(), Class: arbiter.ClassService,
		Urgency: 0.55, // same seat the tome ritual had: above a routine restock, below Heal
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (c *CainIdentify) Needs(*percept.Snapshot) Needs { return c.e.needs() }

func (c *CainIdentify) Begin(ctx *Ctx, resumed bool) {
	c.e.begin(resumed)
	if resumed {
		c.e.resume(ctx)
		return
	}
	c.e.resetTrip()
	c.sweeps, c.before = 0, -1
}

func (c *CainIdentify) Suspend(ctx *Ctx, _ phase.Reason) { c.e.suspend(ctx) }

func (c *CainIdentify) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	c.e.end(ctx, v, why)
	c.e.resetTrip()
	c.sweeps, c.before = 0, -1
	coolOnEnd(v, why, &c.coolAt)
}

func (c *CainIdentify) Step(ctx *Ctx) Status {
	e := &c.e
	if st, stop := e.pre(ctx); stop {
		return st
	}
	s := ctx.Snap
	if s.Me.UnidentCount == 0 {
		return e.finish(ctx, phase.Done, phase.Completed, "backlog identified at Cain")
	}
	switch e.ph.Phase() {
	case erMenu:
		// Our talk opened Cain's menu (the errand's Talk phase checked the click
		// was ours). HOME normalizes, then DOWN to the candidate slot, ENTER.
		if !s.MenuOpen {
			if time.Since(e.clickAt) > 4*time.Second {
				e.to(erTalk, "menu died")
			}
			return e.wait(100 * time.Millisecond)
		}
		ctx.M.MoveStop()
		ctx.M.RealKey(0x24) // HOME
		downs := cainIDSlots[e.menuTry%len(cainIDSlots)]
		for i := 0; i < downs; i++ {
			ctx.M.RealKey(0x28) // DOWN
		}
		ctx.M.RealKey(0x0D) // ENTER
		c.before = s.Me.UnidentCount
		e.to(erOpening, fmt.Sprintf("identify selected (menu slot %d, %d unidentified)", downs, c.before))
		return e.wait(150 * time.Millisecond)
	case erOpening:
		if s.Me.UnidentCount < c.before {
			ctx.Led.Append(verbs.Outcome{Verb: "identify", Holder: c.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("cain: unidentified %d -> %d (menu slot %d)", c.before, s.Me.UnidentCount, cainIDSlots[e.menuTry%len(cainIDSlots)])})
			if s.Me.UnidentCount == 0 {
				return e.finish(ctx, phase.Done, phase.Completed, "backlog identified at Cain")
			}
			e.to(erClean, "partly identified: talk again")
			return e.running()
		}
		if e.ph.InPhase() < cainIDSettle {
			return e.wait(100 * time.Millisecond)
		}
		// Wrong row (or Talk opened a speech): close by sight in Clean and try the next slot.
		e.menuTry++
		if e.menuTry%len(cainIDSlots) == 0 {
			c.sweeps++
			if c.sweeps >= 2 {
				cainIDWorks.Store(false)
				ctx.Led.Append(verbs.Outcome{Verb: "identify", Holder: c.Name(), Result: verbs.ResDeaf,
					Evidence: fmt.Sprintf("cain: %d menu slots tried twice, count stuck at %d — belief retired", len(cainIDSlots), s.Me.UnidentCount)})
				return e.finish(ctx, phase.Abandoned, phase.Deaf, "no Cain menu slot identified")
			}
		}
		e.to(erClean, fmt.Sprintf("no identify after slot try %d", e.menuTry))
		return e.running()
	}
	// Seek, Approach, Talk: the shared errand. It stops at erMenu for us (trade=false,
	// so its own Menu phase never runs: we intercept above before driving).
	st, _, dead := e.drive(ctx, c.Name())
	if dead {
		c.coolAt = time.Now().Add(120 * time.Second)
	}
	return st
}
