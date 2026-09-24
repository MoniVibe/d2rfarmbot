package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/inventory"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Belt (memory-driven)

// OWNER (2026-09-24): "we got a lot of potions in the inventory (it can shift
// click them to fill the belt ...) and all belt slots are filled with mana
// potions". Belt reads the belt and bag from memory (inventory.Model), asks the
// belt plan for ONE move, performs it, and proves it by the next read:
//   Fill  — Shift+click the bag potion; the game places it (its own rule).
//   Evict — click the column's bottom slot on the HUD belt (the bottle lifts),
//           then park it in the bag.
// Moves that do not take are counted by the inventory tracker (move not taking →
// quarantine), so the belt can never grind a dead gesture.

// HUD belt slots, screenshot px at the 1050-px reference (measured R31).
const (
	beltSlot0X    = 1112.0
	beltSlotPitch = 60.3
	beltSlotY     = 1008.0
)

type btPhase uint8

const (
	btClean btPhase = iota
	btOpen          // the bag must be seen (Shift+click and the park need it)
	btAct           // one move issued, being judged
	btPark          // an evicted bottle rides the cursor: park it
	btClose
)

func (p btPhase) String() string { return [...]string{"Clean", "Open", "Act", "Park", "Close"}[p] }

func btClaims(p btPhase) screen.Panel {
	if p >= btOpen {
		return claimsBag
	}
	return 0
}

type Belt struct {
	life   svcLife[btPhase]
	mv     inventory.Move
	clickT time.Time
	keyT   time.Time
	before int // bottles in the belt when the move was issued
	moves  int
	coolAt time.Time
}

var (
	_ Life   = (*Belt)(nil)
	_ Phased = (*Belt)(nil)
)

func NewBelt() *Belt {
	b := &Belt{}
	b.life.init(b.Name(), btClose, btClaims)
	b.life.ph.Budget(btClean, 8*time.Second)
	b.life.ph.Budget(btOpen, 6*time.Second)
	b.life.ph.Budget(btAct, 4*time.Second)
	b.life.ph.Budget(btPark, 6*time.Second)
	b.life.ph.Budget(btClose, 6*time.Second)
	return b
}

func (b *Belt) Name() string      { return "belt" }
func (b *Belt) PhaseName() string { return b.life.ph.Phase().String() }

// beltModel reads the inventory model from one snapshot.
func beltModel(s *percept.Snapshot) *inventory.Model {
	m := &inventory.Model{BeltRows: s.Me.BeltSlots / 4, Cursor: s.Me.CursorItem}
	for _, bi := range s.Me.BeltItems {
		m.Belt = append(m.Belt, inventory.BeltSlot{Unit: uint32(bi.Unit), ID: bi.ID, Index: bi.Pos.X})
	}
	for _, it := range s.Bag {
		m.Bag = append(m.Bag, inventory.Item{Unit: uint32(it.Unit), ID: it.ID, GX: it.GX, GY: it.GY, Quality: it.Qual, Identified: it.Ident})
	}
	return m
}

func (b *Belt) Demand(s *percept.Snapshot) *arbiter.Demand {
	return b.life.keepBid(b.demand(s, time.Now()), s)
}

func (b *Belt) demand(s *percept.Snapshot, now time.Time) *arbiter.Demand {
	if !s.Valid || s.Me.CursorItem || s.Me.HPPct < 40 || now.Before(b.coolAt) || servicesCooled() {
		return nil
	}
	if !s.Me.InTown {
		for _, e := range s.Enemies {
			if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 12 {
				return nil // the bag opens only in a calm field
			}
		}
	}
	mv, ok := beltModel(s).BeltPlan()
	if !ok || InvTracker.Quarantined(mv.Unit, now) {
		return nil
	}
	cls, urg := arbiter.ClassService, 0.6
	if !s.Me.InTown {
		cls, urg = arbiter.ClassLoot, 0.3 // a quiet moment in the field
	}
	return &arbiter.Demand{Who: b.Name(), Class: cls, Urgency: urg, Commit: arbiter.Commitment{MinHold: 3 * time.Second}}
}

func (b *Belt) Needs(*percept.Snapshot) Needs { return b.life.needs() }

func (b *Belt) Begin(ctx *Ctx, resumed bool) {
	b.life.begin(resumed)
	if resumed && b.life.ph.Phase() != btClose && b.life.ph.Phase() != btPark {
		b.life.to(btClean, "resumed: re-read the belt")
	}
	if !resumed {
		b.moves = 0
	}
}

func (b *Belt) Suspend(ctx *Ctx, _ phase.Reason) { b.life.suspend(ctx) }

func (b *Belt) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	b.life.end(ctx, v, why)
	if b.moves == 0 && v != phase.Done {
		b.coolAt = time.Now().Add(2 * time.Minute)
	}
}

func beltSlotPx(ctx *Ctx, col int) (int, int) {
	k := shopScale(ctx)
	return int((beltSlot0X + beltSlotPitch*float64(col)) * k), int(beltSlotY * k)
}

func (b *Belt) Step(ctx *Ctx) Status {
	l := &b.life
	if st, over := l.overrun(ctx); over {
		return st
	}
	if l.ph.Phase() == btClose {
		return l.closing(ctx)
	}
	s := ctx.Snap
	if !s.Valid {
		return l.wait(100 * time.Millisecond)
	}
	switch l.ph.Phase() {
	case btClean:
		mv, ok := beltModel(s).BeltPlan()
		if !ok || InvTracker.Quarantined(mv.Unit, time.Now()) {
			return l.finish(ctx, phase.Done, phase.Completed, fmt.Sprintf("belt as good as the bag allows (%d moves)", b.moves))
		}
		if ok2, st := l.cleanScreen(ctx, claimsBag); !ok2 {
			return st
		}
		b.mv = mv
		l.to(btOpen, mv.Why)
		return l.running()
	case btOpen:
		if bagSeen(ctx) {
			b.before = len(s.Me.BeltItems)
			switch b.mv.Kind {
			case inventory.MoveFill:
				cx, cy := invCellPx(ctx, b.mv.GX, b.mv.GY)
				ctx.M.RealMenuShiftClick(cx, cy)
			case inventory.MoveEvict:
				cx, cy := beltSlotPx(ctx, b.mv.Column)
				ctx.M.RealMenuClick(cx, cy)
			}
			b.clickT = time.Now()
			l.to(btAct, fmt.Sprintf("issued %v unit %d", b.mv.Kind, b.mv.Unit))
			return l.wait(300 * time.Millisecond)
		}
		if ctx.InvKey != 0 && time.Since(b.keyT) > 1500*time.Millisecond {
			ctx.M.RealKey(uint16(ctx.InvKey))
			b.keyT = time.Now()
		}
		return l.wait(150 * time.Millisecond)
	case btAct:
		if time.Since(b.clickT) < 500*time.Millisecond {
			return l.wait(100 * time.Millisecond)
		}
		key := fmt.Sprintf("belt %v %d", b.mv.Kind, b.mv.Unit)
		switch b.mv.Kind {
		case inventory.MoveFill:
			took := len(s.Me.BeltItems) > b.before && !inBag(ctx, dataUnit(b.mv.Unit))
			InvTracker.Issued(key, b.mv.Unit, took)
			if took {
				b.moves++
				ctx.Led.Append(verbs.Outcome{Verb: "belt", Holder: b.Name(), Result: verbs.ResDone, Evidence: fmt.Sprintf("filled: %s (unit %d)", b.mv.Why, b.mv.Unit)})
			} else if s.Me.CursorItem {
				l.to(btPark, "the Shift+click lifted it: park it back")
				return l.running()
			}
		case inventory.MoveEvict:
			if s.Me.CursorItem {
				InvTracker.Issued(key, b.mv.Unit, true)
				l.to(btPark, "bottle lifted off the belt")
				return l.running()
			}
			InvTracker.Issued(key, b.mv.Unit, false)
		}
		l.to(btClean, "judged")
		return l.running()
	case btPark:
		if !s.Me.CursorItem {
			b.moves++
			ctx.Led.Append(verbs.Outcome{Verb: "belt", Holder: b.Name(), Result: verbs.ResDone, Evidence: "parked a bottle in the bag: " + b.mv.Why})
			l.to(btClean, "cursor empty")
			return l.running()
		}
		if time.Since(b.clickT) < 450*time.Millisecond {
			return l.wait(100 * time.Millisecond)
		}
		act, ok := ParkCursor(ctx, nil)
		b.clickT = time.Now()
		if !ok {
			return l.finish(ctx, phase.Abandoned, phase.Deaf, "bottle would not park: "+act)
		}
		return l.wait(350 * time.Millisecond)
	}
	return l.running()
}
