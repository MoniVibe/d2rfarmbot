package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Shop grid (hover-learned)
//
// THE SHOP IS READ, NEVER PROBED (2026-09-23, Lut Gholein: the Act 1 pixel cells
// bought three wrong items for ~9,000 gold, because "probe by purchase" learns the
// layout by paying for mistakes). The vendor's stock is in memory with GRID
// positions, and each stock item carries IsHovered. So:
//   1. learn the grid→pixel map once per client size by HOVERING the panel;
//   2. before every buy, hover the target's computed cell and require the GAME to
//      confirm that exact stock unit is under the cursor;
//   3. after the click, require gold to drop AND the item count to rise.

// ShopGrid maps a vendor grid cell to its panel pixel center (UIClick space).
type ShopGrid struct {
	AX, BX float64 // px = AX*gx + BX
	AY, BY float64 // py = AY*gy + BY
}

func (g ShopGrid) Cell(p data.Position) (int, int) {
	return int(g.AX*float64(p.X) + g.BX + 0.5), int(g.AY*float64(p.Y) + g.BY + 0.5)
}

func (g ShopGrid) valid() bool { return g.AX > 20 && g.AX < 120 && g.AY > 20 && g.AY < 120 }

func shopGridKey(ctx *Ctx) string {
	return fmt.Sprintf("shop.grid.%dx%d", ctx.GR.GameAreaSizeX, ctx.GR.GameAreaSizeY)
}

// fitLine: least squares y = a*x + b.
func fitLine(xs, ys []float64) (a, b float64, ok bool) {
	n := float64(len(xs))
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx, sy, sxx, sxy = sx+xs[i], sy+ys[i], sxx+xs[i]*xs[i], sxy+xs[i]*ys[i]
	}
	den := n*sxx - sx*sx
	if n < 2 || den == 0 {
		return 0, 0, false
	}
	a = (n*sxy - sx*sy) / den
	return a, (sy - a*sx) / n, true
}

// hoveredVendorItem returns the stock item the game reports under the cursor.
func hoveredVendorItem(ctx *Ctx) (data.Item, bool) {
	d := ctx.GR.GetData()
	hd := d.HoverData
	for _, it := range d.Inventory.ByLocation(item.LocationVendor) {
		if it.IsHovered {
			hoverSrcFlag++
			return it, true
		}
		// The global hover record (the oracle attacks use) — the per-item flag
		// read 0 hits in the first live sweep (2026-09-23).
		if hd.IsHovered && hd.UnitType == 4 && hd.UnitID == it.UnitID {
			hoverSrcGlobal++
			return it, true
		}
	}
	return data.Item{}, false
}

// which hover signal found stock items (sweep diagnostics).
var hoverSrcFlag, hoverSrcGlobal int

// learnShopGrid sweeps the panel (shop open) and fits the cell-center map from
// 1x1 stock items. Cached forever per client size; a stale cache is caught by the
// per-buy hover confirmation and relearned.
func learnShopGrid(ctx *Ctx, holder string) (ShopGrid, bool) {
	var g ShopGrid
	key := shopGridKey(ctx)
	if ctx.Mem != nil && ctx.Mem.GetJSON(key, &g) && g.valid() {
		return g, true
	}
	// Panels may need TRUE focus for hover (the -shopmap harness foregrounded the
	// game) — NOT taken: stealing focus breaks background play. The sweep logs
	// which hover signal fires so the evidence decides.
	ctx.M.HoverReady()
	hoverSrcFlag, hoverSrcGlobal = 0, 0
	anyHover := 0
	var gxs, pxs, gys, pys []float64
	distinctX, distinctY := map[int]bool{}, map[int]bool{}
	w := ctx.GR.GameAreaSizeX
	h := ctx.GR.GameAreaSizeY
	// The trade panel owns the left half of the client; stock sits in its lower
	// two-thirds. Coarse enough to be quick, fine enough to land in every cell.
	t0 := time.Now()
	for sy := h / 6; sy <= h*9/10; sy += 22 {
		for sx := w / 40; sx <= w*6/10; sx += 22 {
			ctx.M.HoverPanel(sx, sy)
			time.Sleep(35 * time.Millisecond)
			if ctx.GR.GetData().HoverData.IsHovered {
				anyHover++
			}
			it, ok := hoveredVendorItem(ctx)
			if !ok {
				continue
			}
			if d := it.Desc(); d.InventoryWidth != 1 || d.InventoryHeight != 1 {
				continue
			}
			gxs, pxs = append(gxs, float64(it.Position.X)), append(pxs, float64(sx))
			gys, pys = append(gys, float64(it.Position.Y)), append(pys, float64(sy))
			distinctX[it.Position.X], distinctY[it.Position.Y] = true, true
		}
	}
	ctx.M.ReleasePanelCursor()
	ax, bx, okx := fitLine(gxs, pxs)
	ay, by, oky := fitLine(gys, pys)
	g = ShopGrid{AX: ax, BX: bx, AY: ay, BY: by}
	ev := fmt.Sprintf("sweep %.0fs: %d hits (flag %d global %d, any-hover %d, stock %d, focused %v), %d cols %d rows → px=%.1f*gx+%.1f py=%.1f*gy+%.1f",
		time.Since(t0).Seconds(), len(gxs), hoverSrcFlag, hoverSrcGlobal, anyHover,
		len(vendorStock(ctx, func(data.Item) bool { return true })), ctx.M.GameFocused(),
		len(distinctX), len(distinctY), ax, bx, ay, by)
	if !okx || !oky || len(distinctX) < 2 || len(distinctY) < 2 || !g.valid() {
		ctx.Led.Append(verbs.Outcome{Verb: "shopgrid", Holder: holder, Result: verbs.ResDeaf, Evidence: "no usable fit — " + ev})
		return g, false
	}
	if ctx.Mem != nil {
		ctx.Mem.PutJSON(key, memory.ScopeForever, memory.Provenance{Source: "measured", Evidence: ev}, g)
	}
	ctx.Led.Append(verbs.Outcome{Verb: "shopgrid", Holder: holder, Result: verbs.ResDone, Evidence: ev})
	return g, true
}

// forgetShopGrid drops a cached grid that failed a hover confirmation.
func forgetShopGrid(ctx *Ctx) {
	if ctx.Mem != nil {
		ctx.Mem.PutJSON(shopGridKey(ctx), memory.ScopeForever,
			memory.Provenance{Source: "proven-negative", Evidence: "hover confirmation failed on a cached cell"}, ShopGrid{})
	}
}

// vendorStock lists the open vendor's stock items matching want.
func vendorStock(ctx *Ctx, want func(data.Item) bool) []data.Item {
	var out []data.Item
	for _, it := range ctx.GR.GetData().Inventory.ByLocation(item.LocationVendor) {
		if want(it) {
			out = append(out, it)
		}
	}
	return out
}

// ownedCount: stock-id copies she owns, belt and bag.
func ownedCount(ctx *Ctx, id int) int {
	n := 0
	d := ctx.GR.GetData()
	for _, it := range d.Inventory.ByLocation(item.LocationInventory) {
		if int(it.ID) == id {
			n++
		}
	}
	for _, bp := range d.Inventory.Belt.Items {
		if int(bp.ID) == id {
			n++
		}
	}
	return n
}

// buyVerdict is one verified purchase attempt's result.
type buyVerdict int

const (
	buyOK        buyVerdict = iota
	buyNotStock             // nothing matching in the stock (or not on the visible tab)
	buyNoConfirm            // could not hover-confirm the cell — clicked NOTHING
	buyWrong                // gold dropped without the item arriving — STOP buying
	buyDeaf                 // the click did nothing (no gold change)
)

// buyVerified buys ONE stock item matching want, or nothing. The click only
// happens on a game-confirmed hover of that exact stock unit.
func buyVerified(ctx *Ctx, holder string, what string, want func(data.Item) bool) buyVerdict {
	stock := vendorStock(ctx, want)
	if len(stock) == 0 {
		ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResRefused,
			Evidence: what + ": vendor stocks none"})
		return buyNotStock
	}
	g, ok := learnShopGrid(ctx, holder)
	if !ok {
		return buyNoConfirm
	}
	target := stock[0]
	cx, cy := g.Cell(target.Position)
	ctx.M.HoverReady()
	confirmed := false
	// Hold the computed center, then a small ring — the fit is a centroid.
	for _, o := range [][2]int{{0, 0}, {-6, -6}, {6, 6}, {6, -6}, {-6, 6}} {
		ctx.M.HoverPanel(cx+o[0], cy+o[1])
		time.Sleep(60 * time.Millisecond)
		if it, ok := hoveredVendorItem(ctx); ok && it.UnitID == target.UnitID {
			cx, cy, confirmed = cx+o[0], cy+o[1], true
			break
		}
	}
	if !confirmed {
		ctx.M.ReleasePanelCursor()
		// Either the grid moved (relearn next time) or the item is on another tab.
		forgetShopGrid(ctx)
		ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResWhiff,
			Evidence: fmt.Sprintf("%s id=%d at grid(%d,%d) px(%d,%d): hover never confirmed — no click",
				what, int(target.ID), target.Position.X, target.Position.Y, cx, cy)})
		return buyNoConfirm
	}
	gold0 := ctx.GR.GetData().PlayerUnit.TotalPlayerGold()
	have0 := ownedCount(ctx, int(target.ID))
	ctx.M.UIClickPanel(cx, cy)
	var gold1, have1 int
	for i := 0; i < 8; i++ {
		time.Sleep(100 * time.Millisecond)
		gold1 = ctx.GR.GetData().PlayerUnit.TotalPlayerGold()
		have1 = ownedCount(ctx, int(target.ID))
		if have1 > have0 {
			break
		}
	}
	ev := fmt.Sprintf("%s id=%d grid(%d,%d) px(%d,%d) gold %d→%d owned %d→%d",
		what, int(target.ID), target.Position.X, target.Position.Y, cx, cy, gold0, gold1, have0, have1)
	switch {
	case have1 > have0:
		ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResDone, Evidence: ev})
		return buyOK
	case gold1 < gold0:
		ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResDeaf, Evidence: "PAID WITHOUT RECEIVING — buying halted: " + ev})
		return buyWrong
	default:
		ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResDeaf, Evidence: "click did nothing: " + ev})
		return buyDeaf
	}
}
