package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/loot"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

// ---------------------------------------------------------------- Verified shop buys
//
// THE SHOP IS READ, NEVER PROBED. Measured live 2026-09-23 on the MSI (Drognan):
//   - the stock is in memory: every vendor item carries its grid cell AND its tab
//     (Location.Page — potions on page 3, "Misc");
//   - memory NEVER reports panel hovers (0 hits across four cursor conventions with
//     91 stock items open), and panels ignore posted clicks — only an OS-level click
//     with the game foregrounded lands (the relog law: MENUS DEMAND TRUE FOREGROUND);
//   - "Right Click to Buy": the old left click picked up / bought the wrong thing.
// So a buy computes the cell from measured geometry, clicks the tab, RIGHT-clicks
// the cell for real, and is judged by gold AND the owned count. A payment without
// the item stops all buying — a mistake can happen once, never twice.

// Trade-panel geometry in PHYSICAL client px, measured at a 1050-px-tall client
// (1920x1050 physical). The UI scales with client height.
const (
	shopRefH      = 1050.0
	shopCell0X    = 183.0 // column 0 center
	shopCell0Y    = 238.0 // row 0 center
	shopPitchX    = 47.67
	shopPitchY    = 47.5
	shopTab0X     = 215.0 // tab 0 center
	shopTabPitchX = 121.0
	shopTabY      = 197.0
)

func shopScale(ctx *Ctx) float64 {
	h := float64(ctx.GR.GameAreaSizeY) * ctx.M.PanelScale() // physical client height
	if h <= 0 {
		return 1
	}
	return h / shopRefH
}

// shopCellPx: physical client px of a vendor grid cell's center.
func shopCellPx(ctx *Ctx, p data.Position) (int, int) {
	k := shopScale(ctx)
	return int((shopCell0X + shopPitchX*float64(p.X)) * k), int((shopCell0Y + shopPitchY*float64(p.Y)) * k)
}

// shopTabPx: physical client px of a vendor tab button.
func shopTabPx(ctx *Ctx, page int) (int, int) {
	k := shopScale(ctx)
	return int((shopTab0X + shopTabPitchX*float64(page)) * k), int(shopTabY * k)
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

// ownedCount: copies of an item id she owns, belt and bag.
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
	buyNotStock             // nothing matching in the stock
	buyNoConfirm            // could not take the foreground — clicked nothing
	buyWrong                // gold dropped without the item arriving — STOP buying
	buyDeaf                 // the click did nothing (no gold change)
)

// buyTx is ONE verified purchase in flight, a Step at a time — no sleeps:
// the tab click, a 250ms settle, the real right-click on the cell, then the
// owned count and gold polled for up to 1s. The judgment is the proven one:
// the item arrived (ok), gold dropped without it (WRONG: stop buying), or
// nothing happened (deaf).
type buyTx struct {
	stage        uint8 // 0 idle, 1 tab clicked, 2 cell clicked (judging)
	what         string
	target       data.Item
	cx, cy       int
	gold0, have0 int
	at           time.Time
	// count overrides "owned" for the judgment (a scroll lands in its tome,
	// not the bag: the tome's quantity is the count). nil = ownedCount.
	count func(ctx *Ctx) int
}

func (t *buyTx) owned(ctx *Ctx) int {
	if t.count != nil {
		return t.count(ctx)
	}
	return ownedCount(ctx, int(t.target.ID))
}

// active: a purchase is in flight (resume it before deciding anything new).
func (t *buyTx) active() bool { return t.stage != 0 }

// step advances the purchase. done=false: come back after wait. want is read
// only when a new purchase starts.
func (t *buyTx) step(ctx *Ctx, holder, what string, want func(data.Item) bool) (v buyVerdict, done bool, wait time.Duration) {
	switch t.stage {
	case 0:
		t.what = what
		stock := vendorStock(ctx, want)
		if len(stock) == 0 {
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResRefused,
				Evidence: what + ": vendor stocks none"})
			return buyNotStock, true, 0
		}
		// The panel must be ON SCREEN — lingering stock is not an open shop, and a
		// right-click into the town casts a skill ("Impossible.").
		if !game.ShopVisible(ctx.GR.Screenshot()) {
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResRefused,
				Evidence: what + ": trade panel not on screen — no click"})
			return buyNoConfirm, true, 0
		}
		t.target = stock[0]
		tx, ty := shopTabPx(ctx, t.target.Location.Page)
		t.cx, t.cy = shopCellPx(ctx, t.target.Position)
		snapPNG(ctx, "logs/buy_pre.png")
		// The tab first (harmless if already shown), then the purchase itself.
		if !ctx.M.RealMenuClick(tx, ty) {
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResRefused,
				Evidence: what + ": could not foreground the game for the shop click"})
			return buyNoConfirm, true, 0
		}
		t.stage = 1
		return 0, false, 250 * time.Millisecond
	case 1:
		snapPNG(ctx, "logs/buy_tab.png")
		t.gold0 = ctx.GR.GetData().PlayerUnit.TotalPlayerGold()
		t.have0 = t.owned(ctx)
		if !ctx.M.RealMenuRightClick(t.cx, t.cy) {
			t.stage = 0
			return buyNoConfirm, true, 0
		}
		t.stage, t.at = 2, time.Now()
		return 0, false, 100 * time.Millisecond
	}
	gold1 := ctx.GR.GetData().PlayerUnit.TotalPlayerGold()
	have1 := t.owned(ctx)
	if have1 <= t.have0 && time.Since(t.at) < time.Second {
		return 0, false, 100 * time.Millisecond
	}
	t.stage = 0
	ev := fmt.Sprintf("%s id=%d page=%d grid(%d,%d) px(%d,%d) gold %d→%d owned %d→%d",
		t.what, int(t.target.ID), t.target.Location.Page, t.target.Position.X, t.target.Position.Y, t.cx, t.cy, t.gold0, gold1, t.have0, have1)
	v = judgeBuy(t.have0, have1, t.gold0, gold1)
	switch v {
	case buyOK:
		ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResDone, Evidence: ev})
	case buyWrong:
		ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResDeaf, Evidence: "PAID WITHOUT RECEIVING — buying halted: " + ev})
	default:
		ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: holder, Result: verbs.ResDeaf, Evidence: "click did nothing: " + ev})
	}
	return v, true, 0
}

// judgeBuy: the verified purchase's verdict from the owned count and gold.
func judgeBuy(have0, have1, gold0, gold1 int) buyVerdict {
	switch {
	case have1 > have0:
		return buyOK
	case gold1 < gold0:
		return buyWrong
	}
	return buyDeaf
}

// buyScrollStock — the tome refill, READ from the stock like the potions (owner,
// 2026-09-25: "its out of tp" — the old probe clicked six Akara-shop cells in
// Lut Gholein, none raised the tome, and the scroll belief retired with the
// tome empty). The scroll row comes from the mod's code (tsc/isc), the tab and
// cell from its Location, and the verdict from the TOME's quantity.
func (r *Restock) buyScrollStock(ctx *Ctx, tomeID int) (done bool, wait time.Duration) {
	if !r.sbuy.active() {
		code := "isc"
		if tomeID == 533 {
			code = "tsc"
		}
		r.sbuyTome = tomeID
		r.sbuy.count = func(c *Ctx) int { return tomeCount(c, tomeID) }
		want := func(it data.Item) bool { return loot.Classify(int(it.ID)).Code == code }
		v, done, w := r.sbuy.step(ctx, r.Name(), code+" scroll", want)
		if !done {
			return false, w
		}
		r.judgeScroll(ctx, v)
		return true, 0
	}
	v, done, w := r.sbuy.step(ctx, r.Name(), "", nil)
	if !done {
		return false, w
	}
	r.judgeScroll(ctx, v)
	return true, 0
}

// judgeScroll: two dead buys in a row (or a payment without the scroll) retire
// the scroll belief for this world, as before.
func (r *Restock) judgeScroll(ctx *Ctx, v buyVerdict) {
	_, frozen := r.scrollCursors(r.sbuyTome)
	switch v {
	case buyOK:
		*frozen = 0
		r.scrBought++
	case buyWrong:
		scrollWorks.Store(false)
	default:
		if *frozen++; *frozen >= 2 {
			scrollWorks.Store(false)
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: r.Name(), Result: verbs.ResDeaf,
				Evidence: fmt.Sprintf("tome %d: two dead scroll buys from the stock — scroll belief retired", r.sbuyTome)})
		}
	}
}
