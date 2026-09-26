package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// Relay R10 (2026-09-24, Lut Gholein): 28 "T L=gate" lines, the janitor
// closing the town errands' own panels. The sequence, seven times over:
//
//	T L=phase act=fence from=Opening to=Act why="shop open: ..."        tick=13176
//	T L=ui ... to=world+shop+inventory+automap+cursor why="cursor item" tick=13181  (the Ctrl+click LIFTED the junk)
//	S ... hold=fence ... gate=ok phase=Act                                  (pre() waits on the cursor item)
//	T L=grant from=fence to=equip why="outbid: service 0.85 >= 0.75+0.00 after 5s" tick=13260
//	T L=gate hold=equip why="foreign: shop" act="click 657,120"           tick=13260
//
// The gate judged the RIGHT holder — the new one: Equip bid 0.85 for "a
// cursor item" (Fence's own) and outbid the errand at its panel; Equip's
// Clean claims only the bag, so Drognan's shop was foreign and clicked shut
// with the item riding the cursor.

type r10Clock struct{ t time.Time }

func (c *r10Clock) Now() time.Time { return c.t }

func withHandoffReset(t *testing.T) {
	t.Helper()
	was := vendorHandoff
	t.Cleanup(func() { vendorHandoff = was })
}

// lutGholein: the R10 snapshot — town, 4 junk, rich, an item on the cursor.
func lutGholein(cursor bool) *percept.Snapshot {
	s := &percept.Snapshot{Valid: true}
	s.Me.InTown, s.Me.Area = true, area.LutGholein
	s.Me.JunkCount, s.Me.Gold, s.Me.CursorItem = 4, 108880, cursor
	s.Me.InvFree = 2
	return s
}

func TestR10FenceKeepsItsShopWhenEquipBidsForItsCursorItem(t *testing.T) {
	withJanitor(t, true)
	withHandoffReset(t)
	clk := &r10Clock{t: time.Date(2026, 9, 24, 9, 20, 6, 0, time.UTC)}
	arb := &arbiter.Arbiter{Clock: clk}
	fc, eq := NewFence(), NewEquip()
	s := lutGholein(false)
	ctx := fakeCtx(s)

	// Tick 13098: the grant, Fence in Clean (its own urgency, no lock).
	d := fc.Demand(s)
	if d == nil || d.Urgency != 0.65 {
		t.Fatalf("fence bid %+v", d)
	}
	if g, ch := arb.Decide([]arbiter.Demand{*d}); g == nil || ch.Kind != arbiter.Fresh {
		t.Fatalf("seat: %v", ch)
	}
	fc.Begin(ctx, false)
	// Tick 13176: talk, menu, trade — the shop opens: Act.
	fc.e.to(erAct, "shop open: stock readable and trade panel on screen")
	fc.e.tradeSelected = true
	// Tick 13181: the Ctrl+click lifted the item.
	s.Me.CursorItem = true
	// Tick 13260: 5s+ into the stint, both bid.
	clk.t = clk.t.Add(9 * time.Second)
	fb, ebid := fc.Demand(s), eq.Demand(s)
	if ebid == nil || ebid.Urgency != 0.85 {
		t.Fatalf("equip's cursor bid %+v", ebid)
	}
	if fb == nil || fb.Urgency != PanelLockUrgency {
		t.Fatalf("fence at its panel must ride the lock: %+v", fb)
	}
	g, ch := arb.Decide([]arbiter.Demand{*fb, *ebid})
	if g.Demand.Who != "fence" || ch.Kind != arbiter.Keep {
		t.Fatalf("R10's outbid again: %v", ch)
	}
	// The gate on the same tick, with the R10 screen (shop + bag + cursor item).
	r := reading(screen.World, screen.Shop|screen.Inventory)
	r.CursorItem = true
	n := fc.Needs(s).Holder()
	n.Town = true
	if gr := exec.Gate(*r, n); !gr.Open || gr.Acts() {
		t.Fatalf("the gate acts under the errand's own shop: %+v", gr)
	}
	// Control — R10's line: Equip's Clean then claimed only the bag.
	old := exec.HolderNeeds{Mode: exec.ModeOf(screen.World), Claims: claimsBag, CursorOwn: true, Town: true}
	if gr := exec.Gate(*r, old); gr.Open || gr.Foreign != screen.Shop || !strings.Contains(gr.Why, "foreign: shop") {
		t.Fatalf("control (R10's gate line): %+v", gr)
	}
	// Second defense: had Equip held anyway, its Clean under a cursor item
	// now keeps the trade frame that holds the bag open for the park.
	en := eq.Needs(s).Holder()
	en.Town = true
	if gr := exec.Gate(*r, en); !gr.Open || gr.Acts() {
		t.Fatalf("equip under a cursor item closes the shop (and the bag with it): %+v", gr)
	}
	// A higher class still preempts the errand at its panel.
	adv := arbiter.Demand{Who: "advance", Class: arbiter.ClassTravel, Urgency: 0.2}
	if _, ch := arb.Decide([]arbiter.Demand{*fc.Demand(s), *ebid, adv}); ch.Kind != arbiter.Preempt {
		t.Fatalf("travel over a locked service: %v", ch)
	}
}

// When the errand leaves the panel (Clean), the lock goes with it: the
// cursor parker outbids as before.
func TestPanelLockOnlyAtThePanel(t *testing.T) {
	withJanitor(t, true)
	fc := NewFence()
	s := lutGholein(true)
	ctx := fakeCtx(s)
	fc.Begin(ctx, false)
	for p := erClean; p <= erAct; p++ {
		fc.e.to(p, "test")
		d := fc.Demand(s)
		locked := p >= erTalk
		if (d.Urgency == PanelLockUrgency) != locked {
			t.Errorf("%s: urgency %.2f, locked want %v", p, d.Urgency, locked)
		}
	}
	fc.End(ctx, phase.Abandoned, phase.Deaf)
	if d := fc.Demand(s); d == nil || d.Urgency == PanelLockUrgency {
		t.Fatalf("after End: %+v", d)
	}
}

// Same-NPC handoff: Restock finishes at Drognan with the window open, Fence
// (also Drognan in Lut Gholein) is granted next — its Clean claims the vendor
// panels, so the gate leaves the window up for its first Step to adopt.
// Repair (Fara) claims nothing in Clean: the one explicit, cheap close.
func TestVendorHandoffAtTheSameNPC(t *testing.T) {
	withJanitor(t, true)
	withHandoffReset(t)
	shop := reading(screen.World, screen.Shop|screen.Inventory)
	gate := func(n exec.Needs) exec.GateResult {
		h := n.Holder()
		h.Town = true
		return exec.Gate(*shop, h)
	}
	finishAtDrognan := func(v phase.Verdict) *Ctx {
		r := NewRestock()
		ctx := fakeCtx(lutGholein(false))
		r.Begin(ctx, false)
		r.e.useNPCForArea(area.LutGholein)
		r.e.to(erAct, "shop open")
		r.e.tradeSelected = true
		r.End(ctx, v, phase.Completed)
		return ctx
	}

	ctx := finishAtDrognan(phase.Done)
	if vendorHandoff.who != "restock" || vendorHandoff.npc != npc.Drognan {
		t.Fatalf("handoff %+v", vendorHandoff)
	}
	fc := NewFence()
	fc.Begin(ctx, false)
	if fc.e.adopt != "restock" || fc.Needs(ctx.Snap).Claims != claimsVendor {
		t.Fatalf("fence did not adopt: adopt=%q claims=%s", fc.e.adopt, fc.Needs(ctx.Snap).Claims)
	}
	if g := gate(fc.Needs(ctx.Snap)); !g.Open || g.Acts() {
		t.Fatalf("the janitor tears the adopted window down: %+v", g)
	}
	if vendorHandoff.who != "" {
		t.Fatal("one adopter per handoff")
	}

	// Another NPC: no adoption — Clean claims nothing, the janitor closes.
	finishAtDrognan(phase.Done)
	rp := NewRepair()
	rp.Begin(ctx, false)
	if rp.e.adopt != "" || rp.Needs(ctx.Snap).Claims != 0 {
		t.Fatalf("repair (Fara) adopted Drognan's window")
	}
	if g := gate(rp.Needs(ctx.Snap)); g.Open || g.Foreign&screen.Shop == 0 {
		t.Fatalf("a different NPC's errand must close the window first: %+v", g)
	}

	// Stale, or an errand that did not finish: nothing to adopt.
	finishAtDrognan(phase.Done)
	vendorHandoff.at = time.Now().Add(-10 * time.Second)
	fc2 := NewFence()
	fc2.Begin(ctx, false)
	if fc2.e.adopt != "" {
		t.Fatal("adopted a stale window")
	}
	finishAtDrognan(phase.Abandoned)
	if vendorHandoff.who != "" {
		t.Fatal("an abandoned errand handed its window on")
	}
}

// Equip never place-clicks at a bag it does not SEE (relay R10: the cast
// did not raise the bag under the held item; the "park" click landed in the
// world and the item lay on the floor). The fake context has no motor: any
// click would panic.
func TestEquipNeverParksBlind(t *testing.T) {
	withJanitor(t, true)
	was := equipCursorCool
	t.Cleanup(func() { equipCursorCool = was })
	eq := NewEquip()
	s := lutGholein(true)
	ctx := fakeCtx(s)
	ctx.Screen = reading(screen.World, 0) // the bag is not seen
	ctx.InvKey = 'I'
	eq.Begin(ctx, false)
	eq.life.to(eqDress, "cast: the bag opens")
	eq.doorTry = 1
	st := eq.dress(ctx, s)
	if eq.life.ph.Phase() != eqDoor || st.V.Terminal() {
		t.Fatalf("bag unseen after door 1: want the next door, got %s %+v", eq.life.ph.Phase(), st)
	}
	eq.life.to(eqDress, "inventory key")
	eq.doorTry = 2
	st = eq.dress(ctx, s)
	if st.V != phase.Abandoned || st.Why != phase.Deaf || !strings.Contains(st.Evidence, "no blind place-click") {
		t.Fatalf("after two doors: %+v", st)
	}
	if !equipWorks.Load() {
		t.Fatal("an unseen bag is not a broken gesture: the equip belief stands")
	}
	if eq.Demand(s) != nil {
		t.Fatal("the cursor bid rests after the bag stayed shut (the gate holds the item)")
	}
	if act, ok := ParkCursor(ctx, nil); ok || !strings.Contains(act, "not seen") {
		t.Fatalf("ParkCursor at an unseen bag: %q %v", act, ok)
	}
}
