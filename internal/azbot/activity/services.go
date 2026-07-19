// Town services: the ClassService activities. Everything here is a PORT of machinery
// proven live in the -charsitest / -akaratest training drills of 2026-07-19: banded
// approach, bare-click talk, Home/Down/Enter trade, instant-buy vendor cells, the
// repair-all button, and the mod item-ID ledger (names are scrambled; IDs cannot lie).
package activity

import (
	"fmt"
	"image/png"
	"os"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// Mod item-ID ledger + measured panel coordinates (1920x1050 client).
const (
	modHPPotionID   = 602 // "Herb", 30g — proven by purchase delta
	modManaPotionID = 607 // the old INVALID607 mystery, 60g — proven by purchase delta
	hpCellX, hpCellY     = 612, 478 // Akara Misc tab, red potion cell
	manaCellX, manaCellY = 612, 525 // Akara Misc tab, blue potion cell
	repairBtnX, repairBtnY = 570, 747 // Charsi trade panel, repair-all (2g fixed Buckler 3/12->12/12)
)

// errand is the shared NPC-service state machine. One bounded slice per Step.
type errand struct {
	npcID   npc.ID
	ring    []data.Position // search waypoints until the NPC loads
	ringIdx int
	phase   int // 0 seek, 1 approach, 2 talk, 3 menu-nav, 4 act (owner's), 5 done
	clickAt time.Time
	tries   int
	// menuTry: which menu-option slot to ENTER on this attempt. The blind law was
	// "Home, Down, Enter = TRADE is the second item" — but NPC menus GROW (quest
	// lines appear), and run 26 spent 30 minutes failing a restock against a menu
	// that had presumably changed shape. The errand now self-discovers the trade
	// slot: each failed attempt tries a different Down-count (1,2,0,3), and the
	// vendor-stock oracle judges. What works is what's true.
	menuTry int
	blocked int // consecutive blocked approach strides — the fire-pit-wall detector
	j       *journey.Journey // the PLANNER for far movement — town walls live in the
	// static grid, and local slides can never round a real wall (run 38: fence paced
	// the north wall at y=4897 while every ring waypoint sat past y=4930)
}

func (e *errand) reset() {
	e.phase, e.ringIdx, e.tries, e.menuTry, e.blocked = 0, 0, 0, 0, 0
	e.j = nil
}

// walkTo drives one planner step toward goal; falls back to a slide when the
// planner has no grid or no route. Returns true when the journey says stop
// (arrived handled by caller's distance checks; stalled/nopath = give up on goal).
func (e *errand) walkTo(ctx *Ctx, goal data.Position, who string) (exhausted bool) {
	if ctx.Grid == nil {
		slideStride(ctx, goal, 0, 1, who)
		return false
	}
	if e.j == nil || chebyshev(e.j.Goal, goal) > 4 {
		e.j = journey.New(ctx.GR, ctx.Grid, goal, who)
		e.j.Arrive = 4
	}
	st := e.j.Step(ctx.M, ctx.P, ctx.Led)
	if st.State == journey.Stalled || st.State == journey.NoPath {
		e.j = nil
		return true
	}
	return false
}

// step advances the errand toward an open trade panel. Returns true when the shop is
// OPEN (vendor stock readable — the honest oracle) and the caller may act.
func (e *errand) step(ctx *Ctx, who string) (shopOpen bool, dead bool) {
	s := ctx.Snap
	d := ctx.GR.GetData()
	var target data.Monster
	found := false
	for _, mo := range d.Monsters {
		if mo.Name == e.npcID {
			target, found = mo, true
			break
		}
	}

	switch e.phase {
	case 0: // seek: walk the ring until the NPC loads
		if found {
			e.phase = 1
			return false, false
		}
		if e.ringIdx >= len(e.ring) {
			return false, true // walked the whole ring, no NPC — give up this trip
		}
		wp := e.ring[e.ringIdx]
		if chebyshev(s.Me.Pos, wp) <= 5 {
			e.ringIdx++
			e.j = nil
			return false, false
		}
		if e.walkTo(ctx, wp, who) {
			e.ringIdx++ // no route to this waypoint — try the next
		}
	case 1: // approach: the band (4..7) — closer breaks hover, farther breaks the click
		if !found {
			e.phase = 0
			return false, false
		}
		dist := chebyshev(s.Me.Pos, target.Position)
		if dist >= 4 && dist <= 7 {
			ctx.M.MoveStop()
			e.phase = 2
			return false, false
		}
		if dist < 4 {
			back := data.Position{X: s.Me.Pos.X + (s.Me.Pos.X-target.Position.X)*3,
				Y: s.Me.Pos.Y + (s.Me.Pos.Y-target.Position.Y)*3}
			verbs.Stride{To: back, Hold: 400 * time.Millisecond}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, who)
			return false, false
		}
		// FAR approach goes by PLANNER (real walls demand real routing); the last
		// stretch is local footwork around camp furniture the grids can't see.
		if dist > 12 {
			if e.walkTo(ctx, target.Position, who) {
				return false, true // no route to the NPC at all — give up this trip
			}
			return false, false
		}
		hold := 500 * time.Millisecond
		// SLIDE, and on a wall streak ARC: the camps put fire pits and tables between
		// her and the NPC — none of it in any collision grid. A straight stride beat
		// its head on Akara's fire 28 times in a row (run 31, the owner: "she is
		// circling akara... goes back and forth"). Three blocked slides = walk the
		// arc 90° around the NPC and come at them from a new bearing.
		o := slideStride(ctx, target.Position, hold, 1, who)
		if o.Result == verbs.ResBlocked {
			e.blocked++
		} else {
			e.blocked = 0
		}
		if e.blocked >= 3 {
			e.blocked = 0
			adx, ady := s.Me.Pos.X-target.Position.X, s.Me.Pos.Y-target.Position.Y
			arc := data.Position{X: target.Position.X + ady, Y: target.Position.Y - adx}
			slideStride(ctx, arc, 900*time.Millisecond, 1, who+"/arc")
		}
	case 2: // talk: hover-confirm, BARE click, wait for the menu byte
		if s.MenuOpen {
			e.phase = 3
			return false, false
		}
		if !found {
			e.phase = 0
			return false, false
		}
		// One bounded click attempt per grant of this phase.
		if time.Since(e.clickAt) < 3*time.Second {
			return false, false // the click starts a WALK-to-talk; let it play out
		}
		if e.tries >= 6 {
			return false, true
		}
		me := d.PlayerUnit.Position
		bx := int(float32((target.Position.X-me.X)-(target.Position.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
		by := int(float32((target.Position.X-me.X)+(target.Position.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2
		confirmed, px, py := false, bx, by
	sweep:
		for dy := -8; dy >= -64; dy -= 8 {
			for _, dx := range []int{0, -8, 8, -16, 16, -24, 24} {
				cx, cy := bx+dx, by+dy
				if cx < 20 || cy < 20 || cx > ctx.GR.GameAreaSizeX-20 || cy > ctx.GR.GameAreaSizeY-20 {
					continue
				}
				ctx.M.AimPhysical(cx, cy)
				time.Sleep(40 * time.Millisecond)
				hd := ctx.GR.GetData().HoverData
				if !hd.IsHovered || hd.UnitID != target.UnitID {
					continue
				}
				ctx.M.AimPhysical(cx, cy)
				time.Sleep(60 * time.Millisecond)
				hd = ctx.GR.GetData().HoverData
				if hd.IsHovered && hd.UnitID == target.UnitID {
					confirmed, px, py = true, cx, cy
					break sweep
				}
			}
		}
		e.tries++
		if !confirmed {
			e.phase = 1 // re-approach fresh — never click blind
			return false, false
		}
		ctx.M.BareClick(px, py)
		e.clickAt = time.Now()
	case 3: // menu open: HOME normalizes, then ENTER the menuTry'th candidate slot
		if !s.MenuOpen {
			if time.Since(e.clickAt) > 4*time.Second {
				e.phase = 2 // menu died — talk again
			}
			return false, false
		}
		ctx.M.MoveStop()
		ctx.M.MenuKey(0x24) // HOME
		downs := []int{1, 2, 0, 3}[e.menuTry%4]
		for i := 0; i < downs; i++ {
			ctx.M.MenuKey(0x28) // DOWN
		}
		ctx.M.KeyLane().Press(0x0D) // ENTER
		time.Sleep(1200 * time.Millisecond)
		e.phase = 4
	case 4:
		if len(ctx.GR.GetData().Inventory.ByLocation(item.LocationVendor)) > 0 {
			return true, false
		}
		// Trade did not open: the slot was wrong (menus GROW when quest lines appear —
		// the blind 'second item is Trade' law cost run 26 a 30-minute restock loop).
		// Back all the way out and try the next slot on a fresh talk.
		e.menuTry++
		if e.menuTry >= 6 {
			ctx.Led.Append(verbs.Outcome{Verb: "errand", Holder: who, Result: verbs.ResDeaf,
				Evidence: fmt.Sprintf("npc=%d: no menu slot opened a trade in %d attempts", int(e.npcID), e.menuTry)})
			return false, true
		}
		if s.MenuOpen {
			ctx.M.KeyLane().Press(0x1B) // close menu/dialog — safe: a panel IS open
			time.Sleep(300 * time.Millisecond)
		}
		e.phase = 2
		return false, false
	}
	return false, false
}

// closeShop escapes out of the panel stack.
func closeShop(ctx *Ctx) {
	ctx.M.KeyLane().Press(0x1B) // ESC — safe: a panel IS open
	time.Sleep(300 * time.Millisecond)
}

// healerHeals: the live belief that Akara's talk-heal works on this mod (vanilla law:
// the Act 1 healer refills life+mana the moment you talk). Disproven by the Heal
// errand itself — two talks with no HP change flips it false so the wounded-pending
// gate can never deadlock her in town on a mod that removed the mercy.
var healerHeals atomic.Bool

func init() { healerHeals.Store(true) }

// ServicesPending reports whether a town errand is waiting: a real belt deficit she can
// afford, gear worn to the quarter, or WOUNDS a free healer can close. The class ladder
// puts Travel ABOVE Service, so the travel activities consult this and stand down —
// otherwise Travel starves the errands forever and she marches out with an empty belt
// (the latent starvation bug) or at 37% straight back into the pack that chased her home
// (measured 04:13:07: Breakout's portal landed her in town with zero gold and zero junk —
// nothing pended, Advance marched her out wounded two seconds later).
func ServicesPending(s *percept.Snapshot) bool {
	if s.Me.HPPct <= 55 && healerHeals.Load() {
		return true // Akara's refill is free; leaving town below the drink line is denial
	}
	// Dressing and identifying are errands too — Travel outranks Service by class,
	// and without these lines Advance marched her out with a unique bow still bagged
	// the moment the potions were paid for (run 34: Equip got ONE step).
	if s.Me.EquipCandCount > 0 && equipWorks.Load() {
		return true
	}
	if s.Me.UnidentCount > 0 && identifyWorks.Load() {
		return true
	}
	if s.Me.Gold >= 10 && s.Me.MinDurPct <= 25 {
		return true
	}
	if s.Me.Gold >= 100 {
		if hp, mana := plan(s); hp+mana >= 2 {
			return true
		}
	}
	// The Fence's docket: broke with junk to sell, or a heavy bag either way.
	if s.Me.JunkCount > 0 && (s.Me.Gold < 500 || s.Me.JunkCount >= 4) {
		return true
	}
	return false
}

// ---------------------------------------------------------------- Restock (ClassService)

// Restock keeps the belt at doctrine: one row of mana, the rest HP — scaled to the
// belt she actually wears. Bids softly in town when the belt has real gaps and she
// can pay; never bids in the field (travel-to-town for potions is a Goal decision).
type Restock struct {
	e      errand
	bought int
}

func NewRestock() *Restock {
	return &Restock{e: errand{npcID: npc.Akara,
		ring: []data.Position{{X: 6023, Y: 4933}, {X: 6070, Y: 4960}, {X: 6100, Y: 4990}, {X: 6050, Y: 5010}, {X: 6110, Y: 4930}}}}
}

func (r *Restock) Name() string { return "restock" }

func plan(s *percept.Snapshot) (buyHP, buyMana int) {
	wantMana := 4
	if s.Me.BeltSlots <= 4 {
		wantMana = 1
	}
	wantHP := s.Me.BeltSlots - wantMana
	buyHP, buyMana = wantHP-s.Me.BeltHP, wantMana-s.Me.BeltMana
	if buyHP < 0 {
		buyHP = 0
	}
	if buyMana < 0 {
		buyMana = 0
	}
	return
}

func (r *Restock) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.Gold < 100 {
		return nil
	}
	buyHP, buyMana := plan(s)
	deficit := buyHP + buyMana
	if deficit < 2 { // one missing potion isn't worth a trip across town
		return nil
	}
	return &arbiter.Demand{Who: r.Name(), Class: arbiter.ClassService,
		Urgency: 0.3 + float64(deficit)/float64(s.Me.BeltSlots+1),
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (r *Restock) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	buyHP, buyMana := plan(s)
	if buyHP+buyMana == 0 {
		if r.e.phase >= 3 {
			closeShop(ctx)
		}
		r.e.reset()
		r.bought = 0
		return Done
	}
	open, dead := r.e.step(ctx, r.Name())
	if dead {
		r.e.reset()
		return Abandoned
	}
	if !open {
		return Running
	}
	// Shop open: ONE purchase per step (instant-buy cells), verified by the next
	// snapshot's belt counts — the plan() recomputation IS the postcondition.
	if buyMana > 0 {
		ctx.M.UIClick(manaCellX, manaCellY)
	} else {
		ctx.M.UIClick(hpCellX, hpCellY)
	}
	r.bought++
	if r.bought > s.Me.BeltSlots+2 { // runaway guard: buys that never land
		closeShop(ctx)
		r.e.reset()
		r.bought = 0
		return Abandoned
	}
	time.Sleep(400 * time.Millisecond)
	return Running
}

// ---------------------------------------------------------------- Fence (ClassService)

// Fence sells inventory junk to Akara — the classic bots' economy law: loot → sell →
// gold → repair/potions. A bot with an empty purse cannot take care of itself (the
// owner, 03:08: "she doesn't repair either but she's out of gold i guess?"). One
// ctrl-click quick-sell per Step, gold delta as the postcondition.
//
// THE TOME OATH (the owner: "she simply dropped her tp book again. i'd like her to
// stop that"): a tome must never leave the bag at the Fence. Three layers —
// percept never marks tomes junk; every cell is re-identified against LIVE memory
// the instant before the ctrl-click; and the tome count is audited across the
// session — a missing tome aborts the errand with a loud ledger entry.
type Fence struct {
	e      errand
	sold   int
	gold0  int
	tomes0 int // tome census when the shop opened; -1 = not yet taken
	// Ghost-window defense (the owner: "trying to sell outside akara's trade
	// window"): the vendor-stock read can LINGER after the panel closes, and a
	// ctrl-click into a ghost window with any panel up DROPS the item — the tome
	// and cube likely died of this. A sell that doesn't shrink the junk count is
	// evidence; two in a row is a verdict.
	lastJunk int
	ghost    int
	coolAt   time.Time
}

func NewFence() *Fence {
	return &Fence{tomes0: -1, e: errand{npcID: npc.Akara,
		ring: []data.Position{{X: 6023, Y: 4933}, {X: 6070, Y: 4960}, {X: 6100, Y: 4990}, {X: 6050, Y: 5010}, {X: 6110, Y: 4930}}}}
}

// tomeCensus counts TP+ID tomes in the live inventory read — the Fence's oath audit.
func tomeCensus(ctx *Ctx) int {
	n := 0
	for _, it := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		if int(it.ID) == 533 || int(it.ID) == 534 {
			n++
		}
	}
	return n
}

func (fc *Fence) Name() string { return "fence" }

func (fc *Fence) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.JunkCount == 0 || time.Now().Before(fc.coolAt) {
		return nil
	}
	if s.Me.Gold >= 500 && s.Me.JunkCount < 4 {
		return nil // solvent and light: selling can wait for a fuller bag
	}
	return &arbiter.Demand{Who: fc.Name(), Class: arbiter.ClassService,
		Urgency: 0.45 + float64(minInt(s.Me.JunkCount, 8))/20, // poverty + full bags push it up
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

// invCell converts an inventory GRID slot to the proven panel pixel formula.
func invCell(gx, gy int) (int, int) { return 1292 + gx*45 + 22, 395 + gy*45 + 22 }

func (fc *Fence) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if len(s.Junk) == 0 {
		if fc.e.phase >= 3 {
			closeShop(ctx)
		}
		fc.e.reset()
		fc.sold, fc.tomes0 = 0, -1
		return Done // the bag is honest merchandise no more
	}
	open, dead := fc.e.step(ctx, fc.Name())
	if dead {
		fc.e.reset()
		return Abandoned
	}
	if !open {
		return Running
	}
	// GHOST-WINDOW DEFENSE: a "sell" that doesn't shrink the junk count means the
	// trade window is not really there (the vendor-stock read lingers after close) —
	// and ctrl-clicks into a ghost window DROP ITEMS. Two frozen sells = abort hard.
	if fc.sold > 0 {
		if len(s.Junk) >= fc.lastJunk {
			fc.ghost++
		} else {
			fc.ghost = 0
		}
		if fc.ghost >= 2 {
			ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResDeaf,
				Evidence: "GHOST TRADE WINDOW: sells not landing — aborting before a ctrl-click drops an item"})
			closeShop(ctx)
			fc.e.reset()
			fc.sold, fc.tomes0, fc.ghost = 0, -1, 0
			fc.coolAt = time.Now().Add(45 * time.Second)
			return Abandoned
		}
	}
	fc.lastJunk = len(s.Junk)
	// TOME OATH, audit layer: census on shop-open, verified before every sell. A tome
	// that vanished mid-errand means a click went somewhere it must never go — stop
	// selling IMMEDIATELY and say so; silence is never success.
	census := tomeCensus(ctx)
	if fc.tomes0 < 0 {
		fc.tomes0 = census
	} else if census < fc.tomes0 {
		ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResDeaf,
			Evidence: fmt.Sprintf("TOME LOST mid-errand (%d -> %d) — fencing aborted", fc.tomes0, census)})
		closeShop(ctx)
		fc.e.reset()
		fc.sold, fc.tomes0 = 0, -1
		return Abandoned
	}
	// Shop open: quick-sell ONE junk item per step; the next snapshot's Junk list and
	// gold are the postcondition.
	it := s.Junk[0]
	// TOME OATH, identity layer: the snapshot called this cell junk — re-identify it
	// against LIVE memory the instant before the ctrl-click. Percept and reality
	// disagreeing on a lifeline cell is a refusal, not a sale. Lifelines: tomes,
	// the Horadric Cube (549 — fenced once, the owner's "what the hell"), and any
	// quest-typed item; type-by-ID beats the scrambled name table.
	for _, inv := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		if inv.Position.X != it.GX || inv.Position.Y != it.GY {
			continue
		}
		id := int(inv.ID)
		if id == 533 || id == 534 || id == 549 || inv.Desc().Type == item.TypeQuest {
			ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResRefused,
				Evidence: fmt.Sprintf("cell (%d,%d) holds a LIFELINE (id %d, type %s) — sell refused", it.GX, it.GY, id, inv.Desc().Type)})
			closeShop(ctx)
			fc.e.reset()
			fc.sold, fc.tomes0 = 0, -1
			return Abandoned
		}
	}
	cx, cy := invCell(it.GX, it.GY)
	ctx.M.SellClick(cx, cy)
	fc.sold++
	if fc.sold > 40 { // runaway guard
		closeShop(ctx)
		fc.e.reset()
		fc.sold = 0
		return Abandoned
	}
	time.Sleep(400 * time.Millisecond)
	return Running
}

// ---------------------------------------------------------------- Heal (ClassService)

// Heal is Akara's free refill: the Act 1 healer restores life and mana the moment
// the TALK happens — no menu item, no gold. The errand that was missing at 04:13:07:
// Breakout's portal delivered her to town at 37% with an empty purse, and because
// nothing pended she marched right back into the pack. Talking to Akara IS the
// restock when the purse is empty.
type Heal struct {
	e     errand
	talks int // menu-opens that produced no HP change — the disproof counter
}

func NewHeal() *Heal {
	return &Heal{e: errand{npcID: npc.Akara,
		ring: []data.Position{{X: 6023, Y: 4933}, {X: 6070, Y: 4960}, {X: 6100, Y: 4990}, {X: 6050, Y: 5010}, {X: 6110, Y: 4930}}}}
}

func (h *Heal) Name() string { return "heal" }

func (h *Heal) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.HPPct > 55 || !healerHeals.Load() {
		return nil
	}
	return &arbiter.Demand{Who: h.Name(), Class: arbiter.ClassService,
		Urgency: 0.9 - float64(s.Me.HPPct)/200, // free and instant: outranks the shopping
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (h *Heal) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if s.Me.HPPct > 90 {
		if s.MenuOpen {
			closeShop(ctx)
		}
		h.e.reset()
		h.talks = 0
		return Done // refilled — the pending clears and the march resumes
	}
	// The menu byte high = the talk happened = the heal (if this mod kept it) already
	// landed. Close and read the verdict from a FRESH hp — the snapshot predates the talk.
	if s.MenuOpen {
		closeShop(ctx)
		if hp := ctx.GR.GetData().PlayerUnit.HPPercent(); hp > 60 {
			h.e.reset()
			h.talks = 0
			return Done
		}
		h.talks++
		if h.talks >= 2 {
			// Two talks, no refill: this mod's Akara does not heal. Stop believing —
			// package-wide — or the wounded-pending gate deadlocks her in town forever.
			healerHeals.Store(false)
			ctx.Led.Append(verbs.Outcome{Verb: "heal", Holder: h.Name(), Result: verbs.ResDeaf,
				Evidence: "two talks, no HP change — this Akara does not heal; belief retired"})
			h.e.reset()
			h.talks = 0
			return Abandoned
		}
		return Running
	}
	// Drive the errand only through the TALK (phases 0-2); the MenuOpen intercept
	// above fires before e.step could ever advance into trade navigation.
	if _, dead := h.e.step(ctx, h.Name()); dead {
		h.e.reset()
		return Abandoned
	}
	return Running
}

// ---------------------------------------------------------------- Identify (ClassService)

// identifyWorks: the live belief that the ID-tome SKILL path identifies items on this
// mod (select tome skill -> right-click casts -> inventory opens with the identify
// hand -> click the item). Every step has a readable postcondition; three attempts
// with no Identified flip retire the belief so she can never grind a dead ritual.
var identifyWorks atomic.Bool

func init() { identifyWorks.Store(true) }

// Identify burns the ID tome's charges on the unidentified magic+ backlog — the gate
// between "inventory full of mystery" and the Fence's valuation (the owner: "it won't
// identify and sell anything... inventory is getting full of things she could use").
type Identify struct {
	tries   int // attempts with no count progress — the disproof counter
	lastN   int
	clickAt time.Time
}

func NewIdentify() *Identify { return &Identify{lastN: -1} }

func (idn *Identify) Name() string { return "identify" }

func (idn *Identify) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.UnidentCount == 0 || !identifyWorks.Load() {
		return nil
	}
	return &arbiter.Demand{Who: idn.Name(), Class: arbiter.ClassService,
		Urgency: 0.55, // above a routine restock; below Heal and a deep-poverty Fence
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (idn *Identify) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if s.Me.UnidentCount == 0 {
		if s.MenuOpen {
			closeShop(ctx) // close the inventory the cast opened
		}
		idn.tries, idn.lastN = 0, -1
		return Done
	}
	// Progress audit: the count dropping IS the proof the ritual works.
	if idn.lastN >= 0 && s.Me.UnidentCount < idn.lastN {
		idn.tries = 0
	}
	idn.lastN = s.Me.UnidentCount
	if time.Since(idn.clickAt) < 1200*time.Millisecond {
		return Running // let the previous cast+click land before judging
	}
	if ctx.Cap == nil || ctx.Cap.Identify == nil {
		idn.coolOff(ctx, "no proven ID-tome binding")
		return Abandoned
	}
	idn.tries++
	if idn.tries > 3 {
		idn.coolOff(ctx, fmt.Sprintf("3 attempts, count stuck at %d — ritual retired", s.Me.UnidentCount))
		return Abandoned
	}
	it := s.Unid[0]
	ctx.M.MoveStop()
	// 1. Select the tome skill — readback-verified (the selection flip is the proof).
	ctx.M.PressKey(ctx.Cap.Identify.Key)
	time.Sleep(150 * time.Millisecond)
	// 2. Cast: a world right-click with the tome selected raises the identify hand
	//    and opens the inventory. Aim at open ground below her feet — never an NPC.
	ctx.M.ClickRight(ctx.GR.GameAreaSizeX/2, ctx.GR.GameAreaSizeY/2+180)
	time.Sleep(500 * time.Millisecond)
	// 3. Touch the item: the proven inventory-cell click. The postcondition is the
	//    next snapshot's Identified flag — judged at the top of the next Step.
	cx, cy := invCell(it.GX, it.GY)
	ctx.M.UIClick(cx, cy)
	idn.clickAt = time.Now()
	return Running
}

func (idn *Identify) coolOff(ctx *Ctx, why string) {
	identifyWorks.Store(false)
	ctx.Led.Append(verbs.Outcome{Verb: "identify", Holder: idn.Name(), Result: verbs.ResDeaf,
		Evidence: why + " — identify belief retired (drill the ritual manually)"})
	if ctx.Snap.MenuOpen {
		closeShop(ctx)
	}
	idn.tries, idn.lastN = 0, -1
}

// ---------------------------------------------------------------- Equip (ClassService)

// equipWorks: the live belief that the shift-click auto-equip gesture works here
// (owner-declared: "shift click and replace items from inventory to equip").
var equipWorks atomic.Bool

func init() { equipWorks.Store(true) }

// Equip dresses her in the upgrades the Identify service unveils — the unique bow
// rode in her bag while she fought with a starter bow (the owner: "she has armor and
// a unique bow she could equip but she rolls with her current gear"). Town-only, one
// shift-click per Step, the equipped-list delta as the postcondition.
type Equip struct {
	opened  bool // we pressed the inventory toggle (the panel byte is BLIND to it —
	// 0xF4 proved out for NPC menus only; run 31 retired the belief over that lie)
	lastN   int
	fails   int
	clickAt time.Time
}

func NewEquip() *Equip { return &Equip{lastN: -1} }

func (eq *Equip) Name() string { return "equip" }

func (eq *Equip) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.EquipCandCount == 0 || !equipWorks.Load() {
		return nil
	}
	// A swap needs LANDING ROOM: the displaced gear returns to the grid, and a full
	// bag makes the equip fail silently (photographed, run 40: packed grid, the blue
	// bow stuck). Sell first, dress after — Fence's urgency wins until there's space.
	if s.Me.InvFree < 6 {
		return nil
	}
	return &arbiter.Demand{Who: eq.Name(), Class: arbiter.ClassService,
		Urgency: 0.75, // dressing for the fight outranks shopping; Heal still leads
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (eq *Equip) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if s.Me.EquipCandCount == 0 {
		eq.closePanel(ctx)
		eq.lastN, eq.fails = -1, 0
		return Done // dressed — the docket is empty
	}
	// Progress audit: the candidate count DROPPING is the only oracle this ritual
	// gets — the 0xF4 panel byte is blind to the plain inventory (run 31 retired
	// the belief over that lie). In town a shift-click that misses the panel is
	// one air swing; the count judges everything.
	if eq.lastN >= 0 && s.Me.EquipCandCount < eq.lastN {
		eq.fails = 0
	}
	eq.lastN = s.Me.EquipCandCount
	if time.Since(eq.clickAt) < 1200*time.Millisecond {
		return Running // let the last shift-click land before judging
	}
	// SAFETY INTERLOCK: shift-click with a VENDOR up means SELL. The vendor-stock
	// read must be empty before the gesture ever fires.
	if len(ctx.GR.GetData().Inventory.ByLocation(item.LocationVendor)) > 0 {
		closeShop(ctx)
		return Running
	}
	if !eq.opened {
		// THE IDENTIFY SIDE DOOR (owner-observed: "it also opens when we use the
		// identify skill"): FOUR input classes were photographed failing to press
		// the panel hotkey (message, key-state stub, plain-vk, scancode — runs
		// 33/37/38/39). The cast path uses only proven gameplay primitives.
		if ctx.Cap == nil || ctx.Cap.Identify == nil {
			equipWorks.Store(false)
			ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResDeaf,
				Evidence: "no ID-tome binding to open the panel with — equip belief retired"})
			return Abandoned
		}
		ctx.M.MoveStop()
		ctx.M.PressKey(ctx.Cap.Identify.Key)
		time.Sleep(150 * time.Millisecond)
		ctx.M.ClickRight(ctx.GR.GameAreaSizeX/2, ctx.GR.GameAreaSizeY/2+180)
		eq.opened = true
		eq.clickAt = time.Now()
		return Running
	}
	eq.fails++
	if eq.fails > 3 {
		equipWorks.Store(false)
		ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResDeaf,
			Evidence: fmt.Sprintf("3 shift-clicks, docket stuck at %d — equip belief retired (is -invkey right?)", s.Me.EquipCandCount)})
		eq.closePanel(ctx)
		eq.lastN, eq.fails = -1, 0
		return Abandoned
	}
	// ROTATE the docket — hammering Upgrades[0] let one stubborn piece starve the
	// wearable ones behind it.
	it := s.Upgrades[(eq.fails-1)%len(s.Upgrades)]
	cx, cy := invCell(it.GX, it.GY)
	if eq.fails == 1 {
		snapPNG(ctx, "logs/equip_open.png") // is the panel even up? the photo answers
	}
	// Normalize the cursor: the door-opening cast leaves the IDENTIFY HAND up; one
	// plain click on the (already identified) candidate consumes it harmlessly. If
	// that click grabbed the item instead, put it straight back — never roam with
	// an item on the cursor.
	ctx.M.UIClick(cx, cy)
	time.Sleep(250 * time.Millisecond)
	if len(ctx.GR.GetData().Inventory.ByLocation(item.LocationCursor)) > 0 {
		ctx.M.UIClick(cx, cy)
		time.Sleep(250 * time.Millisecond)
	}
	ctx.M.ShiftClick(cx, cy)
	if eq.fails == 1 {
		time.Sleep(300 * time.Millisecond)
		snapPNG(ctx, "logs/equip_click.png") // did the item move / cursor grab it?
	}
	eq.clickAt = time.Now()
	return Running
}

// closePanel ESCs the cast-opened inventory shut. Safe: we only get here after the
// identify cast actually opened something (the pause-menu trap needs NO panel open).
func (eq *Equip) closePanel(ctx *Ctx) {
	if eq.opened {
		ctx.M.KeyLane().Press(0x1B)
		eq.opened = false
		time.Sleep(300 * time.Millisecond)
	}
}

// snapPNG: one screenshot into logs/ — the debugging eye for rituals whose oracles
// are blind (the inventory panel byte lies; the pictures don't).
func snapPNG(ctx *Ctx, path string) {
	if f, err := os.Create(path); err == nil {
		_ = png.Encode(f, ctx.GR.Screenshot())
		f.Close()
	}
}

// ---------------------------------------------------------------- Repair (ClassService)

// Repair visits Charsi when any equipped item's durability sinks below a quarter.
// The repair-all click costs single-digit gold at these levels and the durability
// delta is the honest postcondition (reads are live on this repack). Transaction
// honesty (the owner: "kind of stuck on charsi after a repair"): a click that
// doesn't IMPROVE durability twice running means the job is as done as it gets —
// leave with what you got instead of pressing the button forever.
type Repair struct {
	e       errand
	lastDur int
	stale   int
	coolAt  time.Time
}

func NewRepair() *Repair {
	return &Repair{lastDur: -1, e: errand{npcID: npc.Charsi,
		ring: []data.Position{{X: 6020, Y: 4952}, {X: 5992, Y: 4941}, {X: 5963, Y: 5001}, {X: 5962, Y: 4956}, {X: 5952, Y: 4944}}}}
}

func (rp *Repair) Name() string { return "repair" }

func (rp *Repair) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.Gold < 10 || s.Me.MinDurPct > 25 || time.Now().Before(rp.coolAt) {
		return nil
	}
	return &arbiter.Demand{Who: rp.Name(), Class: arbiter.ClassService,
		Urgency: 0.5 + (25-float64(s.Me.MinDurPct))/50, // outranks a routine restock
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (rp *Repair) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if s.Me.MinDurPct > 90 { // repaired — the durability delta happened
		if rp.e.phase >= 3 {
			closeShop(ctx)
		}
		rp.reset()
		return Done
	}
	open, dead := rp.e.step(ctx, rp.Name())
	if dead {
		rp.reset()
		return Abandoned
	}
	if !open {
		return Running
	}
	if rp.lastDur >= 0 {
		if s.Me.MinDurPct <= rp.lastDur {
			rp.stale++
		} else {
			rp.stale = 0
		}
		if rp.stale >= 2 {
			// The button no longer buys durability (unrepairable piece or a ghost
			// window): the errand is over — cool off so the demand can't re-loop.
			ctx.Led.Append(verbs.Outcome{Verb: "repair", Holder: rp.Name(), Result: verbs.ResTimeout,
				Evidence: fmt.Sprintf("durability frozen at %d%% after repairs — leaving with what we got", s.Me.MinDurPct)})
			closeShop(ctx)
			rp.reset()
			rp.coolAt = time.Now().Add(3 * time.Minute)
			return Done
		}
	}
	rp.lastDur = s.Me.MinDurPct
	ctx.M.UIClick(repairBtnX, repairBtnY)
	time.Sleep(500 * time.Millisecond)
	return Running
}

func (rp *Repair) reset() {
	rp.e.reset()
	rp.lastDur, rp.stale = -1, 0
}
