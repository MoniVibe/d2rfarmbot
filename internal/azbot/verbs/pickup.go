package verbs

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

// Pickup grabs one ground item: hover-confirm its unit, click, then demand the typed
// postcondition — ground-gone within the window. A click that changes nothing is
// ResDeaf and the CALLER decides (different bearing, blacklist), never a blind retry.
type Pickup struct {
	Target    data.UnitID
	TargetPos data.Position
	// TargetQuality is copied from the selecting snapshot. Background clicks are
	// positional, so the live item must still be the same high-quality target
	// before we send a click; a recycled UnitID or stale frame fails closed.
	TargetQuality int
	Window        time.Duration // default 1.2s
	// AllowBelow lifts the uniques-only guard for belt bottles (2026-09-23: potion
	// looting). Loot sets it ONLY for items the belt classifier calls a potion.
	AllowBelow bool
	// Accept widens the hover oracle to the caller's loot POLICY (step 11, run
	// u: in a pile the probe kept hovering the neighbours — "seen" named Keys —
	// and whiffed twice at d=3 on the one exact UnitID). A hovered ground item
	// Accept approves is as good a click as the target: the click goes to THAT
	// item and the postcondition follows it. Nil = the exact target only.
	Accept func(it data.Item) bool
}

// hoveredPick is the pure decision behind the probe: which ground item under
// the cursor may be clicked. The target wins (by HoverData or its own
// IsHovered flag); otherwise any hovered item the policy accepts. ok=false:
// nothing clickable is hovered.
func hoveredPick(hover data.HoverData, ground []data.Item, target data.UnitID, accept func(data.Item) bool) (data.UnitID, bool) {
	if hover.IsHovered && hover.UnitID == target {
		return target, true
	}
	for _, it := range ground {
		if it.UnitID == target && it.IsHovered {
			return target, true
		}
	}
	if accept == nil {
		return 0, false
	}
	for _, it := range ground {
		hovered := it.IsHovered || (hover.IsHovered && hover.UnitType == 4 && hover.UnitID == it.UnitID)
		if hovered && accept(it) {
			return it.UnitID, true
		}
	}
	return 0, false
}

func (pk Pickup) Do(m *motor.Motor, gr *game.MemoryReader, p *percept.Perceptor, led *Ledger, holder string) Outcome {
	win := pk.Window
	if win <= 0 {
		win = 1200 * time.Millisecond
	}
	o := Outcome{Verb: "pickup", Holder: holder, Target: fmt.Sprintf("item=%d", pk.Target)}
	if !m.Engage.Engaged() {
		o.Result = ResRefused
		o.Evidence = "motor disengaged"
		led.Append(o)
		return o
	}
	m.MoveStop()
	m.ModifierAmnesty() // a latched shift turns the pickup click into an attack
	// swing at the ground (04:30, the brawler: hover confirmed, click landed,
	// item stayed — his ring-queue spams shift-attacks hundreds of times a
	// minute and one lost release latches it; the zon's right-click volleys
	// never exposed this). The EnterPortal lesson, third organ.
	d := gr.GetData()
	// Re-read the target immediately before projecting it to the cursor. The
	// selector's Snapshot can be a few frames old (especially while unfocused),
	// and using its position lets a blind click land on a neighbor after the
	// ground table has moved or a UnitID has been recycled.
	findGroundTarget := func(dd game.Data) (data.Item, bool) {
		for _, it := range dd.Inventory.ByLocation(item.LocationGround) {
			if it.UnitID == pk.Target {
				return it, true
			}
		}
		return data.Item{}, false
	}
	liveTarget, ok := findGroundTarget(d)
	if !ok {
		o.Result = ResRefused
		o.Evidence = "target no longer live before click"
		led.Append(o)
		return o
	}
	if (liveTarget.Quality < item.QualityUnique && !pk.AllowBelow) || (pk.TargetQuality > 0 && int(liveTarget.Quality) != pk.TargetQuality) {
		o.Result = ResRefused
		o.Evidence = fmt.Sprintf("target quality changed before click: %d", int(liveTarget.Quality))
		led.Append(o)
		return o
	}
	// The live position is authoritative for the projection. Keep the original
	// TargetPos only as a diagnostic anchor; it is intentionally not used for the
	// click after this point.
	targetPos := liveTarget.Position
	if chebyshev(targetPos, pk.TargetPos) > 6 {
		o.Result = ResRefused
		o.Evidence = fmt.Sprintf("target moved/recycled: live=(%d,%d) snap=(%d,%d)",
			targetPos.X, targetPos.Y, pk.TargetPos.X, pk.TargetPos.Y)
		led.Append(o)
		return o
	}
	targetSig := struct {
		id      int
		quality item.Quality
		unique  int32
	}{liveTarget.ID, liveTarget.Quality, liveTarget.UniqueSetID}
	inventoryCount := func(dd game.Data, sig interface{}) int {
		want := sig.(struct {
			id      int
			quality item.Quality
			unique  int32
		})
		n := 0
		for _, it := range dd.Inventory.ByLocation(item.LocationInventory) {
			if it.ID == want.id && it.Quality == want.quality && it.UniqueSetID == want.unique {
				n++
			}
		}
		// Bottles auto-place into the BELT (2026-09-23: potion looting) — a belt
		// landing is a successful pickup, not a misclick.
		for _, it := range dd.Inventory.Belt.Items {
			if it.ID == want.id {
				n++
			}
		}
		return n
	}
	beforeTargetInInventory := inventoryCount(d, targetSig)
	// P-6.2: evidence names its item — "why did she pick THAT up" must be
	// answerable from the ledger (the owner asked and the log had no answer).
	for _, it := range d.Inventory.ByLocation(item.LocationGround) {
		if it.UnitID == pk.Target {
			o.Target = fmt.Sprintf("item=%d type=%d qual=%d", pk.Target, int(it.ID), int(it.Quality))
			break
		}
	}
	// PROBE ON STILL GROUND (step 11, run u: two whiffs at d=3 while she slid
	// d=6→10→9): a projection taken while she is still carried by the last
	// stride aims where the item WAS relative to her. Let her stop (≤450ms)
	// before the first probe, and re-project from her live position per row.
	me := d.PlayerUnit.Position
	for i := 0; i < 3; i++ {
		time.Sleep(150 * time.Millisecond)
		now := gr.GetData().PlayerUnit.Position
		if now == me {
			break
		}
		me = now
	}
	sweepStart := me
	project := func(at data.Position) (int, int) {
		return int(float32((targetPos.X-at.X)-(targetPos.Y-at.Y))*19.8) + gr.GameAreaSizeX/2,
			int(float32((targetPos.X-at.X)+(targetPos.Y-at.Y))*9.9) + gr.GameAreaSizeY/2
	}
	bx, by := project(me)
	clicked := pk.Target
	groundGone := func() bool {
		for _, it := range gr.GetData().Inventory.ByLocation(item.LocationGround) {
			if it.UnitID == clicked {
				return false
			}
		}
		return true
	}
	// BACKGROUND PICKUP: hover highlighting is focus-gated on this D2R build,
	// while the posted world click still reaches the game. Once Loot has already
	// walked within three tiles of a unique, make one positional pickup attempt
	// instead of spending the whole sweep proving an impossible hover. The typed
	// ground-gone postcondition decides whether the click worked; a miss returns
	// to Loot's bounded failure/ban path rather than re-bidding forever.
	if !m.HoverReady() {
		if !ClickableLogical(gr, bx, by) {
			o.Result = ResRefused
			o.Evidence = fmt.Sprintf("background pickup point (%d,%d) is off the world", bx, by)
			led.Append(o)
			return o
		}
		m.ClickLeft(bx, by)
		deadline := time.Now().Add(win)
		for time.Now().Before(deadline) {
			time.Sleep(150 * time.Millisecond)
			if groundGone() {
				after := inventoryCount(gr.GetData(), targetSig)
				if after <= beforeTargetInInventory {
					o.Result = ResWhiff
					o.Evidence = "background click removed target from ground but target was not added to inventory; probable misclick"
					led.Append(o)
					return o
				}
				o.Result = ResDone
				o.Evidence = "background positional pickup; live-target and inventory-confirmed"
				led.Append(o)
				return o
			}
		}
		o.Result = ResWhiff
		o.Evidence = "background positional pickup missed"
		led.Append(o)
		return o
	}

	// The item's OWN IsHovered is the honest oracle (d2go computes it with the
	// unit-type check; raw HoverData never confirmed a set sash the owner
	// watched her hover for minutes, 00:05) — and it is DOUBLE-CONFIRMED,
	// because a single read reflects the prior probe's cursor (the
	// enterportal frame-latency lesson, finally applied here).
	//
	// PROBE = HOVER ONLY: the sweep moves the cursor and reads; the ONE click
	// comes after a double-confirmed hover on an item worth taking — the
	// target, or (with Accept) any wanted item the cursor landed on.
	itemHovered := func() (data.UnitID, bool) {
		dd := gr.GetData()
		return hoveredPick(dd.HoverData, dd.Inventory.ByLocation(item.LocationGround), pk.Target, pk.Accept)
	}
	confirmed, px, py := false, bx, by
	var seen []string // what the cursor DID hover (whiff diagnosis, as in hoverstrike)
sweep:
	for _, dy := range []int{0, -8, 8, -16, -24} {
		bx, by = project(gr.GetData().PlayerUnit.Position) // fresh per row
		for _, dx := range []int{0, -10, 10, -20, 20} {
			cx, cy := bx+dx, by+dy
			if !ClickableLogical(gr, cx, cy) {
				continue
			}
			m.AimPhysical(cx, cy)
			time.Sleep(50 * time.Millisecond)
			id, ok := itemHovered()
			if !ok {
				if hd := gr.GetData().HoverData; hd.IsHovered {
					seen = append(seen, fmt.Sprintf("%d/t%d", hd.UnitID, hd.UnitType))
				} else {
					seen = append(seen, "-")
				}
				continue
			}
			m.AimPhysical(cx, cy)
			time.Sleep(70 * time.Millisecond)
			if id2, ok2 := itemHovered(); ok2 && id2 == id {
				confirmed, px, py, clicked = true, cx, cy, id
				break sweep
			}
		}
	}
	if !confirmed {
		o.Result = ResWhiff
		now := gr.GetData().PlayerUnit.Position
		o.Evidence = fmt.Sprintf("no hover confirmation on item (d=%d aim=%d,%d seen=%v)", chebyshev(now, targetPos), bx, by, seen)
		if now != sweepStart {
			// She moved while only the cursor did: not ours — name it.
			o.Evidence += fmt.Sprintf(" drift=(%d,%d) during the hover-only sweep", now.X-sweepStart.X, now.Y-sweepStart.Y)
		}
		led.Append(o)
		return o
	}
	if clicked != pk.Target {
		o.Target += fmt.Sprintf(" took=%d (wanted neighbour under the cursor)", clicked)
	}
	m.ClickLeft(px, py)

	deadline := time.Now().Add(win)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		if groundGone() {
			o.Result = ResDone
			o.Evidence = "ground-gone"
			led.Append(o)
			return o
		}
	}
	o.Result = ResDeaf
	o.Evidence = "item still on ground after click"
	led.Append(o)
	return o
}
