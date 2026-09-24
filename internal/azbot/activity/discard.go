package activity

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Discard (ClassLoot)

// Discard is the loot SWAP's first half: a tier S (or a better tier A) drop
// lies at her feet, the bag is full, and the loot brain chose the worst
// carried item to make room. Discard opens the bag, lifts that item onto the
// cursor, drops it at her feet and closes the bag; Loot then takes the prize.
//
// PROVEN PIECES ONLY, NEW ORDER: the bag door is Identify/Equip's ID-tome
// cast (tome key, right-click); the cell is the Fence's measured 10x8 grid
// (invCellPx) with the RealMenuClick the janitor uses; the drop is the
// janitor's cursor rule (one plain click at her feet, center+140). What is
// new is doing it in the FIELD, and lifting an item on purpose (the owner
// once watched the empty-tome loop do it by accident). Every gesture has a
// memory postcondition — the cursor unit, then the unit on the ground — and
// a wrong unit on the cursor is put straight back and retires the belief.
var discardWorks atomic.Bool

func init() { discardWorks.Store(true) }

type dcPhase uint8

const (
	dcClean dcPhase = iota // precondition: nothing on screen, the victim where the plan saw it
	dcDoor                 // the ID-tome key (read back), then the right-click cast opens the bag
	dcGrab                 // click the victim's cell until it rides the cursor (the ID hand eats one click)
	dcDrop                 // one plain click at her feet; the unit must reach the ground
	dcClose                // janitor OFF: the bag closed by sight
)

func (p dcPhase) String() string {
	switch p {
	case dcClean:
		return "Clean"
	case dcDoor:
		return "Door"
	case dcGrab:
		return "Grab"
	case dcDrop:
		return "Drop"
	case dcClose:
		return "Close"
	}
	return "?"
}

type Discard struct {
	life    svcLife[dcPhase]
	plan    roomPlan
	door    int // tome-key presses this episode
	clicks  int // grab clicks this episode
	drops   int // drop clicks this episode
	clicked bool
	coolAt  time.Time
}

var (
	_ Life   = (*Discard)(nil)
	_ Phased = (*Discard)(nil)
)

func NewDiscard() *Discard {
	d := &Discard{}
	d.life.init(d.Name(), dcClose, bagClaims[dcPhase])
	d.life.ph.Budget(dcClean, 6*time.Second)
	d.life.ph.Budget(dcDoor, 3*time.Second)
	d.life.ph.Budget(dcGrab, 4*time.Second)
	d.life.ph.Budget(dcDrop, 3*time.Second)
	d.life.ph.Budget(dcClose, 8*time.Second)
	return d
}

func (d *Discard) Name() string      { return "discard" }
func (d *Discard) PhaseName() string { return d.life.ph.Phase().String() }

func (d *Discard) Demand(s *percept.Snapshot) *arbiter.Demand {
	// Mid-ritual with the bag up: keep the grant until the ritual ends (a
	// released bag ritual is the janitor's to close, and the drop is half done).
	if d.life.live && !d.life.clean() && s.Valid && !s.Me.InTown && d.life.bid != nil {
		c := *d.life.bid
		return &c
	}
	if !discardWorks.Load() || !s.Valid || s.Me.InTown || s.Me.HPPct < 50 || s.Me.CursorItem ||
		time.Now().Before(d.coolAt) || lootBlockedByHostile(s) || s.Me.IDScrolls <= 0 {
		return nil
	}
	if _, ok := theLoot.swapReady(s); !ok {
		return nil
	}
	dm := &arbiter.Demand{Who: d.Name(), Class: arbiter.ClassLoot,
		Urgency: 0.97, // the prize is at her feet: making room beats every other pickup
		Commit:  arbiter.Commitment{MinHold: 3 * time.Second}}
	c := *dm
	d.life.bid = &c
	return dm
}

func (d *Discard) Needs(s *percept.Snapshot) Needs {
	if d.life.clean() {
		return needsService(0)
	}
	return d.life.needs()
}

func (d *Discard) Begin(ctx *Ctx, resumed bool) {
	d.life.begin(resumed)
	d.door, d.clicks, d.drops, d.clicked = 0, 0, 0, false
	if resumed {
		if cur := d.life.ph.Phase(); bagResume(cur, dcClose) != cur {
			d.life.to(bagResume(cur, dcClose), "resumed: "+cur.String()+" re-verifies from a clean screen")
		}
	}
}

func (d *Discard) Suspend(ctx *Ctx, _ phase.Reason) { d.life.suspend(ctx) }

func (d *Discard) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	d.life.end(ctx, v, why)
	d.door, d.clicks, d.drops, d.clicked = 0, 0, 0, false
	if v != phase.Done {
		d.coolAt = time.Now().Add(20 * time.Second)
	}
}

// cursorUnit is the unit riding the cursor (0 = none).
func cursorUnit(ctx *Ctx) data.UnitID {
	if cur := ctx.GR.GetData().Inventory.ByLocation(item.LocationCursor); len(cur) > 0 {
		return cur[0].UnitID
	}
	return 0
}

func groundHas(ctx *Ctx, u data.UnitID) bool {
	for _, it := range ctx.GR.GetData().Inventory.ByLocation(item.LocationGround) {
		if it.UnitID == u {
			return true
		}
	}
	return false
}

// victimAt re-identifies the victim against LIVE memory: the same unit at the
// same cell, or no click.
func (d *Discard) victimAt(ctx *Ctx) bool {
	for _, it := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		if uint32(it.UnitID) == d.plan.Victim.Unit {
			return it.Position.X == d.plan.Victim.GX && it.Position.Y == d.plan.Victim.GY
		}
	}
	return false
}

func (d *Discard) Step(ctx *Ctx) Status {
	l := &d.life
	if st, over := l.overrun(ctx); over {
		return st
	}
	if l.ph.Phase() == dcClose {
		return l.closing(ctx)
	}
	s := ctx.Snap
	if !s.Valid {
		return l.wait(100 * time.Millisecond)
	}
	switch l.ph.Phase() {
	case dcClean:
		if s.Me.CursorItem {
			return l.finish(ctx, phase.Abandoned, phase.Precondition, "an item already rides the cursor")
		}
		if ok, st := l.cleanScreen(ctx, 0); !ok {
			return st
		}
		p, ok := theLoot.swapReady(s)
		if !ok {
			return l.finish(ctx, phase.Done, phase.Completed, "no swap pending (room appeared, or the prize is gone)")
		}
		d.plan = p
		if ctx.Cap == nil || ctx.Cap.Identify == nil {
			return d.retire(ctx, "no ID-tome binding: no bag door to open")
		}
		if !d.victimAt(ctx) {
			theLoot.abandonRoom("victim moved")
			return l.finish(ctx, phase.Abandoned, phase.Precondition, "the victim is not where the plan saw it")
		}
		l.to(dcDoor, fmt.Sprintf("drop item %d at (%d,%d) for item %d (tier %s)",
			p.Victim.Item.ID, p.Victim.GX, p.Victim.GY, p.Row, p.Tier))
		return l.running()
	case dcDoor:
		if f, bad := l.interlocked(ctx); bad {
			l.to(dcClean, "interlock: "+f.String()+" up — no cast into it")
			return l.running()
		}
		rs := ctx.GR.GetData().PlayerUnit.RightSkill
		if rs != skill.TomeOfIdentify && rs != skill.ScrollOfIdentify {
			if d.door >= 3 {
				return l.finish(ctx, phase.Abandoned, phase.Deaf, "the ID-tome key selected nothing in 3 presses")
			}
			ctx.M.MoveStop()
			ctx.M.PressKey(ctx.Cap.Identify.Key)
			d.door++
			return l.wait(150 * time.Millisecond)
		}
		// The tome is armed (read back): the cast opens the bag with the ID hand.
		ctx.M.ClickRight(ctx.GR.GameAreaSizeX/2, ctx.GR.GameAreaSizeY/2+180)
		l.to(dcGrab, "cast: the bag opens")
		return l.wait(900 * time.Millisecond)
	case dcGrab:
		return d.grab(ctx)
	case dcDrop:
		return d.drop(ctx)
	}
	return l.running()
}

// grab lifts the victim onto the cursor. The cast leaves the IDENTIFY HAND up:
// the first click on an item identifies it (or is eaten by an identified one);
// the next lifts it. At most three clicks.
func (d *Discard) grab(ctx *Ctx) Status {
	l := &d.life
	if u := cursorUnit(ctx); u != 0 {
		if uint32(u) != d.plan.Victim.Unit {
			// The wrong item rides the cursor: put it back where it came from
			// and never try this gesture again this session.
			d.putBack(ctx, u)
			return d.retire(ctx, fmt.Sprintf("WRONG ITEM on the cursor (unit %d, wanted %d) — put back", u, d.plan.Victim.Unit))
		}
		l.to(dcDrop, "victim on the cursor")
		return l.running()
	}
	if d.clicked && !d.victimAt(ctx) {
		// Not on the cursor and not in its cell: where did it go?
		if groundHas(ctx, data.UnitID(d.plan.Victim.Unit)) {
			return d.dropped(ctx)
		}
		return d.retire(ctx, "the victim left its cell but is on neither the cursor nor the ground")
	}
	if seen, ok := seenPanels(ctx); !ok || seen&screen.Inventory == 0 {
		return l.wait(150 * time.Millisecond) // never click a cell of a bag nobody sees
	}
	if f, bad := l.interlocked(ctx); bad {
		l.to(dcClean, "interlock: "+f.String()+" up")
		return l.running()
	}
	if d.clicks >= 3 {
		return l.finish(ctx, phase.Abandoned, phase.Deaf, "3 cell clicks, nothing lifted")
	}
	if !d.victimAt(ctx) {
		theLoot.abandonRoom("victim moved")
		return l.finish(ctx, phase.Abandoned, phase.Precondition, "the victim is not in its cell")
	}
	x, y := invCellPx(ctx, d.plan.Victim.GX, d.plan.Victim.GY)
	ctx.M.RealMenuClick(x, y)
	d.clicks++
	d.clicked = true
	return l.wait(350 * time.Millisecond)
}

// drop lets the victim fall at her feet (the janitor's cursor-drop spot).
func (d *Discard) drop(ctx *Ctx) Status {
	l := &d.life
	victim := data.UnitID(d.plan.Victim.Unit)
	if cursorUnit(ctx) == 0 {
		if groundHas(ctx, victim) {
			return d.dropped(ctx)
		}
		if d.victimAt(ctx) {
			return l.finish(ctx, phase.Abandoned, phase.Deaf, "the victim went back to its cell")
		}
		return l.wait(150 * time.Millisecond) // between frames: the next read decides
	}
	if d.drops >= 3 {
		return d.retire(ctx, "3 drop clicks and the victim still rides the cursor")
	}
	ctx.M.BareClick(ctx.GR.GameAreaSizeX/2, ctx.GR.GameAreaSizeY/2+140)
	d.drops++
	return l.wait(400 * time.Millisecond)
}

func (d *Discard) dropped(ctx *Ctx) Status {
	ctx.Led.Append(verbs.Outcome{Verb: "discard", Holder: d.Name(), Result: verbs.ResDone,
		Target:   fmt.Sprintf("item=%d unit=%d", d.plan.Victim.Item.ID, d.plan.Victim.Unit),
		Evidence: fmt.Sprintf("dropped to make room for item %d (tier %s)", d.plan.Row, d.plan.Tier)})
	theLoot.swapped(data.UnitID(d.plan.Victim.Unit))
	return d.life.finish(ctx, phase.Done, phase.Completed, "victim on the ground: room made")
}

// putBack returns a wrongly lifted unit to its own cell (its pre-grab cell in
// the snapshot's bag list).
func (d *Discard) putBack(ctx *Ctx, u data.UnitID) {
	for _, b := range ctx.Snap.Bag {
		if b.Unit == u {
			x, y := invCellPx(ctx, b.GX, b.GY)
			ctx.M.RealMenuClick(x, y)
			return
		}
	}
}

func (d *Discard) retire(ctx *Ctx, ev string) Status {
	discardWorks.Store(false)
	theLoot.abandonRoom(ev)
	ctx.Led.Append(verbs.Outcome{Verb: "discard", Holder: d.Name(), Result: verbs.ResDeaf,
		Evidence: ev + " — discard belief retired for the session"})
	return d.life.finish(ctx, phase.Abandoned, phase.Deaf, ev)
}
