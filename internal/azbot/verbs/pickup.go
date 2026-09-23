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
	if liveTarget.Quality < item.QualityUnique || (pk.TargetQuality > 0 && int(liveTarget.Quality) != pk.TargetQuality) {
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
	me := d.PlayerUnit.Position
	bx := int(float32((targetPos.X-me.X)-(targetPos.Y-me.Y))*19.8) + gr.GameAreaSizeX/2
	by := int(float32((targetPos.X-me.X)+(targetPos.Y-me.Y))*9.9) + gr.GameAreaSizeY/2
	groundGone := func() bool {
		for _, it := range gr.GetData().Inventory.ByLocation(item.LocationGround) {
			if it.UnitID == pk.Target {
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
	if !m.GameFocused() {
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
	itemHovered := func() bool {
		dd := gr.GetData()
		if dd.HoverData.IsHovered && dd.HoverData.UnitID == pk.Target {
			return true
		}
		for _, it := range dd.Inventory.ByLocation(item.LocationGround) {
			if it.UnitID == pk.Target && it.IsHovered {
				return true
			}
		}
		return false
	}
	confirmed, px, py := false, bx, by
sweep:
	for _, dy := range []int{0, -8, 8, -16, -24} {
		for _, dx := range []int{0, -10, 10, -20, 20} {
			cx, cy := bx+dx, by+dy
			if cx < 20 || cy < 20 || cx > gr.GameAreaSizeX-20 || cy > gr.GameAreaSizeY-20 {
				continue
			}
			m.AimPhysical(cx, cy)
			time.Sleep(50 * time.Millisecond)
			if !itemHovered() {
				continue
			}
			m.AimPhysical(cx, cy)
			time.Sleep(70 * time.Millisecond)
			if itemHovered() {
				confirmed, px, py = true, cx, cy
				break sweep
			}
		}
	}
	if !confirmed {
		o.Result = ResWhiff
		o.Evidence = "no hover confirmation on item"
		led.Append(o)
		return o
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
