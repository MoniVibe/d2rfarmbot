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
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
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
	menuTry    int
	blocked    int // consecutive blocked approach strides — the fire-pit-wall detector
	hoverFails int // consecutive hover-sweep misses — the torch-owns-this-bearing detector
	j       *journey.Journey // the PLANNER for far movement — town walls live in the
	// static grid, and local slides can never round a real wall (run 38: fence paced
	// the north wall at y=4897 while every ring waypoint sat past y=4930)
}

func (e *errand) reset() {
	e.phase, e.ringIdx, e.tries, e.menuTry, e.blocked, e.hoverFails = 0, 0, 0, 0, 0, 0
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
			ctx.Led.Append(verbs.Outcome{Verb: "errand", Holder: who, Result: verbs.ResWhiff,
				Evidence: fmt.Sprintf("npc=%d never loaded on the whole ring (P-6.2)", int(e.npcID))})
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
			// Proportional backstep TO THE BAND, not a triple-distance fling —
			// the old *3 threw her from the clinch past 7 and the approach
			// re-overshot: the torch dance (the owner, 10:2x: "walks back and
			// forth to Charsi's torch").
			adx, ady := s.Me.Pos.X-target.Position.X, s.Me.Pos.Y-target.Position.Y
			m := maxInt(absInt(adx), absInt(ady))
			if m == 0 {
				m = 1
			}
			back := data.Position{X: target.Position.X + adx*5/m, Y: target.Position.Y + ady*5/m}
			verbs.Stride{To: back, Hold: 400 * time.Millisecond}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, who)
			return false, false
		}
		// FAR approach goes by PLANNER (real walls demand real routing); the last
		// stretch is local footwork around camp furniture the grids can't see.
		if dist > 12 {
			if e.walkTo(ctx, target.Position, who) {
				ctx.Led.Append(verbs.Outcome{Verb: "errand", Holder: who, Result: verbs.ResBlocked,
					Evidence: fmt.Sprintf("npc=%d: planner found no route from (%d,%d) at dist %d (P-6.2)", int(e.npcID), s.Me.Pos.X, s.Me.Pos.Y, dist)})
				return false, true // no route to the NPC at all — give up this trip
			}
			return false, false
		}
		// AIM AT THE BAND, never the body: sliding at the NPC's center lands in
		// the hover-breaking clinch and the backstep oscillates. A point 5 out
		// on the current bearing IS the destination.
		adx, ady := s.Me.Pos.X-target.Position.X, s.Me.Pos.Y-target.Position.Y
		bm := maxInt(absInt(adx), absInt(ady))
		if bm == 0 {
			bm = 1
		}
		bandPt := data.Position{X: target.Position.X + adx*5/bm, Y: target.Position.Y + ady*5/bm}
		hold := 500 * time.Millisecond
		// SLIDE, and on a wall streak ARC: the camps put fire pits and tables between
		// her and the NPC — none of it in any collision grid. A straight stride beat
		// its head on Akara's fire 28 times in a row (run 31, the owner: "she is
		// circling akara... goes back and forth"). Three blocked slides = walk the
		// arc 90° around the NPC and come at them from a new bearing.
		o := slideStride(ctx, bandPt, hold, 1, who)
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
			ctx.Led.Append(verbs.Outcome{Verb: "errand", Holder: who, Result: verbs.ResDeaf,
				Evidence: fmt.Sprintf("npc=%d: 6 talk attempts, menu never opened (hover or click deaf — P-6.2)", int(e.npcID))})
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
			// The same bearing that failed hover will fail it again — the torch
			// owns this line of sight, not the town. Two hover misses walk the
			// 90° arc before re-approaching.
			if e.hoverFails++; e.hoverFails >= 2 {
				e.hoverFails = 0
				adx, ady := s.Me.Pos.X-target.Position.X, s.Me.Pos.Y-target.Position.Y
				arc := data.Position{X: target.Position.X + ady, Y: target.Position.Y - adx}
				slideStride(ctx, arc, 900*time.Millisecond, 1, who+"/hoverarc")
			}
			e.phase = 1 // re-approach fresh — never click blind
			return false, false
		}
		e.hoverFails = 0
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
	// the moment the potions were paid for (run 34: Equip got ONE step). P-4.8 with
	// the escape clause run 42 taught: a pending docket whose service CANNOT run
	// (no landing room, nothing left to sell) must not gate the march — she idled
	// in town 12 minutes on that deadlock.
	if equippableCands(s) > 0 && equipWorks.Load() && s.Me.InvFree >= 6 {
		return true
	}
	if s.Me.UnidentCount > 0 && identifyWorks.Load() && s.Me.IDScrolls > 0 {
		return true
	}
	// P-8.1: banked points are an errand — the march waited on every other
	// docket while 20 points sat in the bank and the unique stayed bagged
	// (run 52: Advance took the actuator at 0.20 over Spend's 0.35 by class).
	// Same escape clause: a retired spend belief does not gate the march.
	if s.Me.StatPoints > 0 && spendWorks.Load() {
		return true
	}
	// P-4.5: a near-empty tome is an errand too — marching out with no escape
	// hatch is how retreats lose their destination (P-2.3), and an empty ID
	// tome starves the whole judging pipeline.
	if (s.Me.TPScrolls >= 0 && s.Me.TPScrolls <= 2 || s.Me.IDScrolls == 0) && scrollDeficit(s) > 0 {
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
// belt she actually wears — and the TP tome at its floor of 8 scrolls (P-4.5: an
// empty tome un-writes P-2.3's destination; the owner: "she's out already").
// Bids softly in town when the belt has real gaps and she can pay; never bids in
// the field (travel-to-town for potions is a Goal decision).
type Restock struct {
	e          errand
	potBought  int
	lastBeltHP int
	lastBeltMN int
	beltFrozen int // potion buys with no belt rise — a full belt eats nothing
	scrBought  int
	probeTP    int // scroll-cell discovery cursors (P-4.5: cells are LEARNED)
	probeID    int
	frozenTP   int // learned-cell buys with no tome delta (WARNING 2 family)
	frozenID   int
}

// scrollWorks: the live belief that scroll restocking works here — retired when
// probing exhausts every candidate or a learned cell freezes (WARNING 2).
var scrollWorks atomic.Bool

func init() { scrollWorks.Store(true) }

const tpScrollFloor = 8

// scrollGap is one tome's gap to doctrine — 0 when the tome is absent (a loose
// scroll is not a tome) or the purse cannot pay.
func scrollGap(gauge, gold int) int {
	if gauge < 0 || gauge >= tpScrollFloor || gold < 200 || !scrollWorks.Load() {
		return 0
	}
	return tpScrollFloor - gauge
}

// scrollDeficit sums both tomes' gaps (P-4.5: TP for the escape hatch, ID for
// the judging of the bag).
func scrollDeficit(s *percept.Snapshot) int {
	return scrollGap(s.Me.TPScrolls, s.Me.Gold) + scrollGap(s.Me.IDScrolls, s.Me.Gold)
}

// tomeCount reads a tome's LIVE charge count — the only truth a scroll BUY is
// judged by (P-4.5).
func tomeCount(ctx *Ctx, tomeID int) int {
	for _, it := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		if int(it.ID) == tomeID {
			if q, ok := it.FindStat(stat.Quantity, 0); ok {
				return q.Value
			}
			return 0
		}
	}
	return -1
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
	// A FULL BELT BUYS NOTHING (measured 09:59: 6 HP + 2 mana on an 8-slot
	// belt, doctrine wanting 4 mana — the deficit re-bid an unwinnable errand
	// three times in a minute). Buys are capped by free slots; the mix
	// corrects itself as she drinks.
	free := s.Me.BeltSlots - s.Me.BeltHP - s.Me.BeltMana
	if free < 0 {
		free = 0
	}
	if buyMana > free {
		buyMana = free
	}
	free -= buyMana
	if buyHP > free {
		buyHP = free
	}
	return
}

func (r *Restock) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.Gold < 100 {
		return nil
	}
	buyHP, buyMana := plan(s)
	deficit := buyHP + buyMana
	// A near-empty tome is worth the trip on its own (P-4.5): with ≤2 scrolls
	// the next retreat may have no destination.
	if deficit < 2 && !(s.Me.TPScrolls >= 0 && s.Me.TPScrolls <= 2 && scrollDeficit(s) > 0) {
		return nil // one missing potion isn't worth a trip across town
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
	if s.Me.CursorItem {
		return Running // WARNING 9: an instant-buy cell click would misfire
	}
	buyHP, buyMana := plan(s)
	if buyHP+buyMana+scrollDeficit(s) == 0 {
		if r.e.phase >= 3 {
			closeShop(ctx)
		}
		r.e.reset()
		r.resetCounters()
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
	// Shop open: ONE purchase per step (instant-buy cells). Potions first, then
	// scrolls (P-4.5): blood before the escape hatch.
	if buyMana > 0 || buyHP > 0 {
		// THE BELT IS THE ONLY PROOF a potion buy landed: a full belt sends the
		// bottle to the bag and plan() never converges — she sprayed Akara with
		// mana orders until the guard tripped (the owner, 09:50). Two buys with
		// no belt rise: stop buying bottles this visit.
		if r.potBought > 0 && s.Me.BeltHP == r.lastBeltHP && s.Me.BeltMana == r.lastBeltMN {
			if r.beltFrozen++; r.beltFrozen >= 2 {
				ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: r.Name(), Result: verbs.ResDeaf,
					Evidence: fmt.Sprintf("belt frozen at hp=%d mana=%d after %d buys — full belt or ghost window", s.Me.BeltHP, s.Me.BeltMana, r.potBought)})
				closeShop(ctx)
				r.e.reset()
				r.resetCounters()
				return Abandoned
			}
		} else {
			r.beltFrozen = 0
		}
		r.lastBeltHP, r.lastBeltMN = s.Me.BeltHP, s.Me.BeltMana
		if buyMana > 0 {
			ctx.M.UIClick(manaCellX, manaCellY)
		} else {
			ctx.M.UIClick(hpCellX, hpCellY)
		}
		r.potBought++
		if r.potBought > s.Me.BeltSlots+2 { // runaway guard: buys that never land
			closeShop(ctx)
			r.e.reset()
			r.resetCounters()
			return Abandoned
		}
	} else {
		// Scrolls: the TP tome first (the escape hatch), then the ID tome.
		if scrollGap(s.Me.TPScrolls, s.Me.Gold) > 0 {
			r.buyScroll(ctx, 533, "shop.akara.cell.tpscroll", &r.probeTP, &r.frozenTP)
		} else {
			r.buyScroll(ctx, 534, "shop.akara.cell.idscroll", &r.probeID, &r.frozenID)
		}
		r.scrBought++
		if r.scrBought > 2*tpScrollFloor+12 { // probes + refills, generously bounded
			closeShop(ctx)
			r.e.reset()
			r.resetCounters()
			return Abandoned
		}
	}
	time.Sleep(400 * time.Millisecond)
	return Running
}

func (r *Restock) resetCounters() {
	r.potBought, r.beltFrozen, r.scrBought = 0, 0, 0
	r.lastBeltHP, r.lastBeltMN = 0, 0
	r.frozenTP, r.frozenID = 0, 0
}

// buyScroll — P-4.5: one scroll purchase, judged by the TOME's quantity delta
// (gold cannot tell a scroll from junk). The cell is LEARNED once per tome and
// kept forever; until learned, probe the misc-tab candidates — a wrong probe's
// junk goes to the fence like any other merchandise.
func (r *Restock) buyScroll(ctx *Ctx, tomeID int, memKey string, probeIdx, frozen *int) {
	candidates := [][2]int{{612, 431}, {612, 384}, {612, 337}, {664, 478}, {664, 431}, {664, 384}}
	var cell [2]int
	learned := ctx.Mem != nil && ctx.Mem.GetJSON(memKey, &cell)
	if !learned {
		if *probeIdx >= len(candidates) {
			scrollWorks.Store(false)
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: r.Name(), Result: verbs.ResDeaf,
				Evidence: fmt.Sprintf("no misc-tab candidate raised tome %d — scroll belief retired", tomeID)})
			return
		}
		cell = candidates[*probeIdx]
	}
	before := tomeCount(ctx, tomeID)
	ctx.M.UIClick(cell[0], cell[1])
	time.Sleep(450 * time.Millisecond)
	after := tomeCount(ctx, tomeID)
	switch {
	case after > before:
		*frozen = 0
		if !learned && ctx.Mem != nil {
			ctx.Mem.PutJSON(memKey, memory.ScopeForever,
				memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("probe: tome %d %d→%d at (%d,%d)", tomeID, before, after, cell[0], cell[1])}, cell)
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: r.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("%s LEARNED at (%d,%d)", memKey, cell[0], cell[1])})
		}
	case !learned:
		*probeIdx++ // wrong cell: whatever it bought is the fence's problem
	default:
		// A learned cell with a frozen delta twice is a GHOST WINDOW (WARNING 2).
		if *frozen++; *frozen >= 2 {
			scrollWorks.Store(false)
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: r.Name(), Result: verbs.ResDeaf,
				Evidence: fmt.Sprintf("tome %d frozen at %d twice on the learned cell — scroll belief retired", tomeID, before)})
		}
	}
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
	if s.Me.CursorItem {
		return Running // WARNING 9: a ctrl-click with a held item is a drop
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
	// WARNING 2 + Lexicon/SELL: the delta check is the only proof — a sell that
	// doesn't shrink the junk count is firing into a GHOST WINDOW, and ctrl-clicks
	// into one DROP items. Two frozen deltas = abort. Never a third.
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
	// P-4.1: in town she tops up below 75 — free is free, and idling at 57
	// kept Breakout's eject seat armed all morning (11:06). Only below 55
	// does the wound GATE the march (ServicesPending keeps that line).
	if !s.Valid || !s.Me.InTown || s.Me.HPPct > 75 || !healerHeals.Load() {
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
	if s.Me.CursorItem {
		return Running // WARNING 9: an NPC click with a held item can drop it
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
	// AN EMPTY TOME IDENTIFIES NOTHING (the owner watched the loop, 09:50:
	// the cast fizzles, the follow-up click GRABS the item, the next click
	// throws it — WARNING 9's mess on repeat). Zero charges: stand down until
	// Restock refills the tome (P-4.5).
	if s.Me.IDScrolls <= 0 {
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
	if s.Me.CursorItem {
		return Running // WARNING 9: the tome ritual clicks cells — parking first
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

// refusedEquips: item type-ids whose shift-click froze the docket three times —
// the game's silent refusal as an oracle (P-4.4, photographed 10:11: a
// game-red armor docketed past every readable gate). Session-lifetime.
var refusedEquips = map[int]bool{}

// equippableCands counts docket items the game has not refused — the number
// the march treaty and the Equip demand actually care about.
func equippableCands(s *percept.Snapshot) int {
	n := 0
	for _, u := range s.Upgrades {
		if !refusedEquips[u.ID] {
			n++
		}
	}
	return n
}

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
	strikes map[int]int // per-item silent refusals — three strikes refuse the item (P-4.4)
}

func NewEquip() *Equip { return &Equip{lastN: -1} }

func (eq *Equip) Name() string { return "equip" }

func (eq *Equip) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown {
		return nil
	}
	// WARNING 9: a cursor item paralyzes every service — parking it is Equip's
	// highest errand, above its own dressing and everyone's shopping.
	if s.Me.CursorItem {
		return &arbiter.Demand{Who: eq.Name(), Class: arbiter.ClassService,
			Urgency: 0.85,
			Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
	}
	if equippableCands(s) == 0 || !equipWorks.Load() {
		return nil // an all-refused docket is an empty docket (P-4.4)
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

// parkCursor — WARNING 9's recovery: with the panel open, place the cursor
// item into a free grid region its own footprint fits; the cursor-empty read
// is the only proof. Returns true when the cursor is clean.
func (eq *Equip) parkCursor(ctx *Ctx) bool {
	d := ctx.GR.GetData()
	cur := d.Inventory.ByLocation(item.LocationCursor)
	if len(cur) == 0 {
		return true
	}
	w, h := cur[0].Desc().InventoryWidth, cur[0].Desc().InventoryHeight
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	var occ [10][4]bool
	for _, it := range d.Inventory.ByLocation(item.LocationInventory) {
		iw, ih := it.Desc().InventoryWidth, it.Desc().InventoryHeight
		if iw <= 0 {
			iw = 1
		}
		if ih <= 0 {
			ih = 1
		}
		for x := it.Position.X; x < it.Position.X+iw && x < 10; x++ {
			for y := it.Position.Y; y < it.Position.Y+ih && y < 4; y++ {
				if x >= 0 && y >= 0 {
					occ[x][y] = true
				}
			}
		}
	}
	for gy := 0; gy <= 4-h; gy++ {
	scan:
		for gx := 0; gx <= 10-w; gx++ {
			for x := gx; x < gx+w; x++ {
				for y := gy; y < gy+h; y++ {
					if occ[x][y] {
						continue scan
					}
				}
			}
			// A place-click aims at the region's CENTER cell (the game anchors
			// the held item by its center).
			cx, cy := invCell(gx+(w-1)/2, gy+(h-1)/2)
			ctx.M.UIClick(cx, cy)
			time.Sleep(350 * time.Millisecond)
			return len(ctx.GR.GetData().Inventory.ByLocation(item.LocationCursor)) == 0
		}
	}
	return false // no region fits: the fence must make room first
}

func (eq *Equip) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	// WARNING 9: parking the cursor item precedes every ritual — including our
	// own docket. The door opens first if needed; the cursor-empty read is the
	// only proof; a bag with no room hands the problem to the fence.
	if s.Me.CursorItem {
		if len(ctx.GR.GetData().Inventory.ByLocation(item.LocationVendor)) > 0 {
			closeShop(ctx)
			return Running
		}
		if !eq.opened {
			if ctx.Cap == nil || ctx.Cap.Identify == nil {
				return Abandoned // no door to open; nothing safe to click
			}
			ctx.M.MoveStop()
			ctx.M.PressKey(ctx.Cap.Identify.Key)
			time.Sleep(150 * time.Millisecond)
			ctx.M.ClickRight(ctx.GR.GameAreaSizeX/2, ctx.GR.GameAreaSizeY/2+180)
			eq.opened = true
			eq.clickAt = time.Now()
			return Running
		}
		if time.Since(eq.clickAt) < 900*time.Millisecond {
			return Running
		}
		eq.clickAt = time.Now()
		if eq.parkCursor(ctx) {
			return Running // clean — the docket resumes next Step
		}
		if eq.fails++; eq.fails > 5 {
			ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResDeaf,
				Evidence: "cursor item would not park (no room or deaf panel) — retiring"})
			equipWorks.Store(false)
			eq.closePanel(ctx)
			eq.lastN, eq.fails = -1, 0
			return Abandoned
		}
		return Running
	}
	if equippableCands(s) == 0 {
		eq.closePanel(ctx)
		eq.lastN, eq.fails = -1, 0
		return Done // dressed or all refused — either way the docket is empty (P-4.4)
	}
	// Progress audit: the candidate count DROPPING is the only oracle this ritual
	// gets — the 0xF4 panel byte is blind to the plain inventory (run 31 retired
	// the belief over that lie). In town a shift-click that misses the panel is
	// one air swing; the count judges everything.
	if eq.lastN >= 0 && s.Me.EquipCandCount < eq.lastN {
		eq.fails = 0
		eq.strikes = nil // something landed — forgive the whole docket
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
	if eq.fails > 12 {
		// The absolute breaker: every candidate froze through its whole strike
		// budget — the gesture itself is broken here, not one item.
		equipWorks.Store(false)
		ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResDeaf,
			Evidence: fmt.Sprintf("12 shift-clicks, docket stuck at %d — equip belief retired (is -invkey right?)", s.Me.EquipCandCount)})
		eq.closePanel(ctx)
		eq.lastN, eq.fails = -1, 0
		return Abandoned
	}
	// ROTATE the LIVE docket (refused items dropped) — hammering Upgrades[0] let
	// one stubborn piece starve the wearable ones behind it.
	cands := make([]percept.InvItem, 0, len(s.Upgrades))
	for _, u := range s.Upgrades {
		if !refusedEquips[u.ID] {
			cands = append(cands, u)
		}
	}
	if len(cands) == 0 {
		eq.closePanel(ctx)
		eq.lastN, eq.fails = -1, 0
		return Done
	}
	it := cands[(eq.fails-1)%len(cands)]
	// THE REFUSAL ORACLE (P-4.4): three silent refusals on one item and the
	// game has spoken — a hidden requirement the memory read cannot see
	// (photographed 10:11, the game-red armor). Drop it, dress the rest.
	if eq.strikes == nil {
		eq.strikes = map[int]int{}
	}
	if eq.strikes[it.ID]++; eq.strikes[it.ID] >= 3 {
		refusedEquips[it.ID] = true
		ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResBlocked,
			Evidence: fmt.Sprintf("item %d refused by the game 3 times — dropped from the docket (P-4.4)", it.ID)})
		return Running
	}
	// P-4.4: a candidate equips onto the ACTIVE hands — a bow needs the bow set
	// out before the click, or the gesture benches the javelins instead of the
	// white bow. The count audit still judges; a deaf swap burns one rotation.
	if it.IsBow && s.Me.WeaponKind != "bow" {
		ctx.M.KeyLane().Press(ctx.SwapKey)
		eq.clickAt = time.Now()
		return Running
	}
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

// ---------------------------------------------------------------- Spend (ClassService)

// spendWorks: the live belief that the New-Stats screen button and the panel's
// plus buttons spend points here. Retired on frozen counts, like equipWorks.
var spendWorks atomic.Bool

func init() { spendWorks.Store(true) }

// Spend — P-8 POINTS ARE ORDNANCE. Banked stat points buy the wardrobe's
// requirement gates first (P-8.3: exactly to the gate, strength before
// dexterity; the owner's unique bow is the standing customer), the rest go to
// vitality. The panel opens by its SCREEN BUTTON (P-8.2 — WARNING 5, panel
// hotkeys are deaf); every click is judged by the StatPoints delta (SPEND,
// Lexicon); ESC fires only after a VERIFIED spend proves a panel was open
// (WARNING 4 — the pause trap).
type Spend struct {
	opened   bool
	verified int // clicks proven by a points delta since the panel opened
	frozen   int // consecutive clicks with no delta
	clickAt  time.Time
	doorAt   time.Time // when the New Stats door was clicked — the settle clock
}

// Screen geometry, client pixels (1920x1050): the New Stats button photographed
// live 2026-07-19 08:21 (logs/shot.png, physical 475,793 / 1.2); the plus
// buttons are farmbot's proven -statalloc coords on this same window.
const (
	newStatsBtnX, newStatsBtnY = 397, 662
	strBtnX, strBtnY           = 347, 305
	dexBtnX, dexBtnY           = 347, 428
	vitBtnX, vitBtnY           = 347, 552
)

func NewSpend() *Spend { return &Spend{} }

func (sp *Spend) Name() string { return "spend" }

func (sp *Spend) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.StatPoints <= 0 || !spendWorks.Load() {
		return nil
	}
	urg := 0.35 // vitality dump: below Identify — the docket may still reveal a gate
	if s.Me.NeedStr > s.Me.Str || s.Me.NeedDex > s.Me.Dex {
		urg = 0.8 // a gate is buyable: clear it BEFORE Equip (0.75) runs this visit (P-8.1)
	}
	return &arbiter.Demand{Who: sp.Name(), Class: arbiter.ClassService,
		Urgency: urg,
		Commit:  arbiter.Commitment{MinHold: 4 * time.Second}}
}

func (sp *Spend) Step(ctx *Ctx) Verdict {
	s := ctx.Snap
	if !s.Valid {
		return Running
	}
	if s.Me.CursorItem {
		return Running // WARNING 9: no clicks while an item rides the cursor
	}
	if s.Me.StatPoints <= 0 {
		sp.closePanel(ctx)
		return Done // ordnance spent — the docket re-judges on the next snapshot
	}
	if time.Since(sp.clickAt) < 450*time.Millisecond {
		return Running // let the last click land before judging
	}
	// SAFETY INTERLOCK (Equip's law): panel clicks with a VENDOR up are trades.
	if len(ctx.GR.GetData().Inventory.ByLocation(item.LocationVendor)) > 0 {
		closeShop(ctx)
		return Running
	}
	if !sp.opened {
		ctx.M.MoveStop()
		snapPNG(ctx, "logs/spend_pre.png") // is the New Stats button even there?
		ctx.M.UIClick(newStatsBtnX, newStatsBtnY)
		sp.opened = true
		sp.clickAt, sp.doorAt = time.Now(), time.Now()
		return Running
	}
	// SPEND (Lexicon): door settle 1 s — the panel's slide-in eats early clicks
	// (measured 08:41: two clicks into the animation retired the whole belief).
	if time.Since(sp.doorAt) < 1100*time.Millisecond {
		return Running
	}
	if sp.frozen == 0 && sp.verified == 0 {
		snapPNG(ctx, "logs/spend_door.png") // did the New Stats click open anything?
	}
	// The recipient (P-8.3): strength to the gate, then dexterity, then vitality.
	bx, by := vitBtnX, vitBtnY
	if s.Me.NeedStr > s.Me.Str {
		bx, by = strBtnX, strBtnY
	} else if s.Me.NeedDex > s.Me.Dex {
		bx, by = dexBtnX, dexBtnY
	}
	before := s.Me.StatPoints
	ctx.M.UIClick(bx, by)
	time.Sleep(300 * time.Millisecond)
	if sp.frozen == 1 && sp.verified == 0 {
		snapPNG(ctx, "logs/spend_click.png") // where did the plus click actually land?
	}
	after := before
	if v, ok := ctx.GR.GetData().PlayerUnit.BaseStats.FindStat(stat.StatPoints, 0); ok {
		after = v.Value
	}
	if after < before {
		sp.verified++
		sp.frozen = 0
	} else {
		sp.frozen++
		// PLAN B DOOR after the first frozen click: the New Stats button comes
		// and goes (photographed present 08:21, absent 09:35) — but farmbot's
		// -statalloc PROVED the 'c' hotkey opens the char panel on this build,
		// BaseStats-delta-verified. WARNING 5's dead hotkeys were the
		// inventory key; 'c' has its own proof. One press, then judge again.
		if sp.frozen == 1 {
			ctx.M.PressKey(0x43) // 'C'
			sp.doorAt = time.Now()
			sp.clickAt = time.Now()
			return Running
		}
		if sp.frozen >= 3 {
			// Frozen through both doors: retire for the session (SPEND, Lexicon).
			spendWorks.Store(false)
			ctx.Led.Append(verbs.Outcome{Verb: "spend", Holder: sp.Name(), Result: verbs.ResDeaf,
				Evidence: fmt.Sprintf("count frozen at %d through both doors (verified %d) — belief retired", before, sp.verified)})
			sp.closePanel(ctx) // ESC only if a spend ever verified — WARNING 4
			return Abandoned
		}
	}
	sp.clickAt = time.Now()
	return Running
}

// closePanel ESCs shut ONLY when a verified spend proved a panel was open —
// an unproven ESC is the pause trap (WARNING 4).
func (sp *Spend) closePanel(ctx *Ctx) {
	if sp.opened && sp.verified > 0 {
		ctx.M.KeyLane().Press(0x1B)
	}
	sp.opened, sp.verified, sp.frozen = false, 0, 0
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
