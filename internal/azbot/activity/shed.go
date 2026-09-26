package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Shed (field)
//
// OWNER (2026-09-25): "i think we want it to drop them instead of hanging to them
// and selling them, anyway. unless they are keepers". The bag plan's sell list
// (percept Junk: tier B/C gear, weak uniques, the bottles past the reserve) is
// dropped in the field — in a calm moment, one item at a time: open the bag,
// Ctrl+click the item (with no other panel open the game DROPS it — the click
// drill law), and prove it left the bag. Keepers never appear on the sell list.
// Gold is no concern at this stage; the bag's room is.

type shPhase uint8

const (
	shClean shPhase = iota
	shOpen          // the bag must be seen
	shAct           // one Ctrl+click issued, being judged
	shClose
)

func (p shPhase) String() string { return [...]string{"Clean", "Open", "Act", "Close"}[p] }

func shClaims(p shPhase) screen.Panel {
	if p >= shOpen && p < shClose {
		return claimsBag
	}
	return 0
}

type Shed struct {
	life    svcLife[shPhase]
	target  percept.InvItem
	unit    data.UnitID
	clickT  time.Time
	keyT    time.Time
	dropped int
	coolAt  time.Time
	fails   map[data.UnitID]int
}

var (
	_ Life   = (*Shed)(nil)
	_ Phased = (*Shed)(nil)
)

func NewShed() *Shed {
	s := &Shed{fails: map[data.UnitID]int{}}
	s.life.init(s.Name(), shClose, shClaims)
	s.life.ph.Budget(shClean, 8*time.Second)
	s.life.ph.Budget(shOpen, 6*time.Second)
	s.life.ph.Budget(shAct, 4*time.Second)
	s.life.ph.Budget(shClose, 6*time.Second)
	return s
}

func (sh *Shed) Name() string      { return "shed" }
func (sh *Shed) PhaseName() string { return sh.life.ph.Phase().String() }

// shedNext: the first sell-list item still in the bag that has not failed twice.
func (sh *Shed) shedNext(s *percept.Snapshot) (percept.InvItem, data.UnitID, bool) {
	for _, j := range s.Junk {
		for _, b := range s.Bag {
			if b.GX == j.GX && b.GY == j.GY && b.ID == j.ID && sh.fails[b.Unit] < 2 {
				return j, b.Unit, true
			}
		}
	}
	return percept.InvItem{}, 0, false
}

func (sh *Shed) Demand(s *percept.Snapshot) *arbiter.Demand {
	return sh.life.keepBid(sh.demand(s, time.Now()), s)
}

func (sh *Shed) demand(s *percept.Snapshot, now time.Time) *arbiter.Demand {
	if !s.Valid || s.Me.InTown || s.Me.CursorItem || s.Me.HPPct < 50 || now.Before(sh.coolAt) {
		return nil
	}
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 15 {
			return nil // the bag opens only in a calm field
		}
	}
	if _, _, ok := sh.shedNext(s); !ok {
		return nil
	}
	return &arbiter.Demand{Who: sh.Name(), Class: arbiter.ClassLoot, Urgency: 0.35,
		Commit: arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (sh *Shed) Needs(*percept.Snapshot) Needs { return sh.life.needs() }

func (sh *Shed) Begin(ctx *Ctx, resumed bool) {
	sh.life.begin(resumed)
	if resumed && sh.life.ph.Phase() != shClose {
		sh.life.to(shClean, "resumed: re-read the bag")
	}
	if !resumed {
		sh.dropped = 0
	}
}

func (sh *Shed) Suspend(ctx *Ctx, _ phase.Reason) { sh.life.suspend(ctx) }

func (sh *Shed) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	sh.life.end(ctx, v, why)
	if sh.dropped == 0 && v != phase.Done {
		sh.coolAt = time.Now().Add(2 * time.Minute)
	}
}

func (sh *Shed) Step(ctx *Ctx) Status {
	l := &sh.life
	if st, over := l.overrun(ctx); over {
		return st
	}
	if l.ph.Phase() == shClose {
		return l.closing(ctx)
	}
	s := ctx.Snap
	if !s.Valid {
		return l.wait(100 * time.Millisecond)
	}
	switch l.ph.Phase() {
	case shClean:
		j, u, ok := sh.shedNext(s)
		if !ok {
			return l.finish(ctx, phase.Done, phase.Completed, fmt.Sprintf("bag shed (%d dropped)", sh.dropped))
		}
		if ok2, st := l.cleanScreen(ctx, claimsBag); !ok2 {
			return st
		}
		sh.target, sh.unit = j, u
		l.to(shOpen, fmt.Sprintf("drop unit %d (row %d) at (%d,%d)", u, j.ID, j.GX, j.GY))
		return l.running()
	case shOpen:
		if bagSeen(ctx) {
			cx, cy := invCellPx(ctx, sh.target.GX, sh.target.GY)
			ctx.M.RealMenuCtrlClick(cx, cy)
			sh.clickT = time.Now()
			l.to(shAct, fmt.Sprintf("ctrl-clicked unit %d", sh.unit))
			return l.wait(300 * time.Millisecond)
		}
		if ctx.InvKey != 0 && time.Since(sh.keyT) > 1500*time.Millisecond {
			ctx.M.RealKey(uint16(ctx.InvKey))
			sh.keyT = time.Now()
		}
		return l.wait(150 * time.Millisecond)
	case shAct:
		if time.Since(sh.clickT) < 500*time.Millisecond {
			return l.wait(100 * time.Millisecond)
		}
		gone := !inBag(ctx, sh.unit)
		InvTracker.Issued(fmt.Sprintf("shed %d", sh.unit), uint32(sh.unit), gone)
		switch {
		case gone && !s.Me.CursorItem:
			sh.dropped++
			theLoot.shed(sh.unit)
			ctx.Led.Append(verbs.Outcome{Verb: "loot", Holder: sh.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("shed: dropped unit %d (row %d) — not a keeper", sh.unit, sh.target.ID)})
		case s.Me.CursorItem:
			// Lifted instead of dropped: the janitor's field drop sheds a
			// non-keeper on the cursor; count it as done when it lands.
			sh.dropped++
			theLoot.shed(sh.unit)
		default:
			sh.fails[sh.unit]++
		}
		l.to(shClean, "judged")
		return l.running()
	}
	return l.running()
}
