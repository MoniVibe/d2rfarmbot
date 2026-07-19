// Town services: the ClassService activities. Everything here is a PORT of machinery
// proven live in the -charsitest / -akaratest training drills of 2026-07-19: banded
// approach, bare-click talk, Home/Down/Enter trade, instant-buy vendor cells, the
// repair-all button, and the mod item-ID ledger (names are scrambled; IDs cannot lie).
package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
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
}

func (e *errand) reset() { e.phase, e.ringIdx, e.tries = 0, 0, 0 }

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
			return false, false
		}
		verbs.Stride{To: wp, MinGain: 1}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, who)
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
		hold := 1600 * time.Millisecond
		if dist < 14 {
			hold = 500 * time.Millisecond
		}
		verbs.Stride{To: target.Position, Hold: hold, MinGain: 1}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, who)
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
	case 3: // menu open: HOME normalizes the highlight, DOWN, ENTER = TRADE
		if !s.MenuOpen {
			if time.Since(e.clickAt) > 4*time.Second {
				e.phase = 2 // menu died — talk again
			}
			return false, false
		}
		ctx.M.MoveStop()
		ctx.M.MenuKey(0x24) // HOME
		ctx.M.MenuKey(0x28) // DOWN
		ctx.M.KeyLane().Press(0x0D) // ENTER
		time.Sleep(1200 * time.Millisecond)
		e.phase = 4
	case 4:
		if len(ctx.GR.GetData().Inventory.ByLocation(item.LocationVendor)) > 0 {
			return true, false
		}
		e.phase = 2 // trade never opened — start over from the talk
		return false, false
	}
	return false, false
}

// closeShop escapes out of the panel stack.
func closeShop(ctx *Ctx) {
	ctx.M.KeyLane().Press(0x1B) // ESC — safe: a panel IS open
	time.Sleep(300 * time.Millisecond)
}

// ServicesPending reports whether a town errand is waiting: a real belt deficit she can
// afford, or gear worn to the quarter. The class ladder puts Travel ABOVE Service, so
// the travel activities consult this and stand down — otherwise Travel starves the
// errands forever and she marches out with an empty belt (the latent starvation bug).
func ServicesPending(s *percept.Snapshot) bool {
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
	if !s.Valid || !s.Me.InTown || s.Me.JunkCount == 0 {
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
	// disagreeing on a tome cell is a refusal, not a sale.
	for _, inv := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		if inv.Position.X == it.GX && inv.Position.Y == it.GY && (int(inv.ID) == 533 || int(inv.ID) == 534) {
			ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResRefused,
				Evidence: fmt.Sprintf("cell (%d,%d) holds a TOME (id %d) — sell refused", it.GX, it.GY, int(inv.ID))})
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

// ---------------------------------------------------------------- Repair (ClassService)

// Repair visits Charsi when any equipped item's durability sinks below a quarter.
// The repair-all click costs single-digit gold at these levels and the durability
// delta is the honest postcondition (reads are live on this repack).
type Repair struct {
	e errand
}

func NewRepair() *Repair {
	return &Repair{e: errand{npcID: npc.Charsi,
		ring: []data.Position{{X: 6020, Y: 4952}, {X: 5992, Y: 4941}, {X: 5963, Y: 5001}, {X: 5962, Y: 4956}, {X: 5952, Y: 4944}}}}
}

func (rp *Repair) Name() string { return "repair" }

func (rp *Repair) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || !s.Me.InTown || s.Me.Gold < 10 || s.Me.MinDurPct > 25 {
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
		rp.e.reset()
		return Done
	}
	open, dead := rp.e.step(ctx, rp.Name())
	if dead {
		rp.e.reset()
		return Abandoned
	}
	if !open {
		return Running
	}
	ctx.M.UIClick(repairBtnX, repairBtnY)
	time.Sleep(500 * time.Millisecond)
	return Running
}
