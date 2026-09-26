package activity

import (
	"fmt"
	"image"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/loot"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Stash (ClassService)

// OWNER (2026-09-24): "it should try to drop off items in the stash". The bag sat
// at bag_free=0 in R16-R18, so tier-S drops were skipped ("S:full") and the quest
// artifacts had nowhere to land. Stash carries the keepers (uniques, runes, gems,
// the mod's own rows) out of the bag. Charms (they work in the bag), the cube,
// tomes, quest artifacts and the equip docket stay.
//
// Placement is by SIGHT, verified by MEMORY: the item is lifted with a plain click
// (the Ctrl+click lifts instead of moving on this mod — the fence's "SELL BECAME A
// PICKUP"), a free W×H block is found on the visible stash page by its near-black
// empty cells (measured R2: empty (14,13,15), occupied navy or item colors), the
// item is set down there, and the move counts only when the cursor is empty and the
// unit has left the bag. A page with no room turns to the next with the page arrow.

// Stash-panel geometry in screenshot px at the 1050-px reference (measured on the
// R2 capture, 1920x1050): a 16x13 grid, column 0 center x=183, row 0 center y=129.
const (
	stashCols       = 16
	stashRows       = 13
	stashCell0X     = 183.0
	stashCell0Y     = 129.0
	stashPitchX     = 47.5
	stashPitchY     = 47.7
	stashNextPgX    = 615.0 // the page arrow right of "Page n / 5"
	stashNextPgY    = 752.0
	stashPrevPgX    = 462.0 // the page arrow left of "Page n / 5"
	stashSharedTabX = 297.0 // the "Shared" tab header (measured in a click drill, R29)
	stashSharedTabY = 89.0
)

// The stash tabs (headers at y=89, measured on the R29 captures).
const (
	stashTabPersonal = iota
	stashTabShared
	stashTabGems
	stashTabMaterials
	stashTabRunes
	stashMaxPages = 5
	stashEmptyMax = 22 // every sampled channel at or below this = an empty cell
)

// stashWorks: the live belief that the stash ritual moves items on this mod. Two
// episodes that move nothing retire it; NewWorld re-arms it.
var stashWorks atomic.Bool

// stashShot: the open stash is photographed once per process.
var stashShot atomic.Bool

func init() { stashWorks.Store(true) }

type stPhase uint8

const (
	stClean stPhase = iota // precondition: nothing on screen
	stWalk                 // reach the stash chest
	stOpen                 // hover-confirmed click; wait for the panel by sight
	stLift                 // click the keeper's bag cell; the cursor must fill
	stPlace                // find room by sight, set it down; cursor empty + gone from the bag
	stClose                // janitor OFF: our panels closed by sight
)

func (p stPhase) String() string {
	return [...]string{"Clean", "Walk", "Open", "Lift", "Place", "Close"}[p]
}

func stashClaims(p stPhase) screen.Panel {
	if p >= stOpen {
		return screen.Stash | claimsBag
	}
	return 0
}

// Stash is the town errand that empties the bag of keepers into the stash.
type Stash struct {
	life          svcLife[stPhase]
	clickT        time.Time
	target        data.UnitID // the keeper being moved
	tgtW          int
	tgtH          int
	tgtGX         int
	tgtGY         int
	pages         int // page turns this lift
	moved         int
	fails         int
	coolAt        time.Time
	tabbed        bool      // the Shared tab was clicked this episode
	tab, want     int       // the open tab (read by sight), and the tab the current keeper belongs in
	tabTries      int       // clicks toward want that did not show it
	shifting      bool      // a shift-transfer is being judged
	rewinds       int       // left-arrow presses toward Shared page 1 this episode
	triedPersonal bool      // Shared full: Personal was tried
	noShift       bool      // this keeper takes the lift-and-place path
	fullUntil     time.Time // a stash with no room for the keeper: stand down (no retry loop)
	// the chest hover sweep (one aim per tick)
	aimIdx     int
	aimX, aimY int
	aimed      bool
}

var (
	_ Life   = (*Stash)(nil)
	_ Phased = (*Stash)(nil)
)

func NewStash() *Stash {
	st := &Stash{}
	st.life.init(st.Name(), stClose, stashClaims)
	st.life.ph.Budget(stClean, 10*time.Second)
	st.life.ph.Budget(stWalk, 45*time.Second)
	st.life.ph.Budget(stOpen, 10*time.Second)
	st.life.ph.Budget(stLift, 60*time.Second) // R40: Lift moves every keeper in turn (tab reads, the page-1 rewind, Ctrl+clicks); 4s timeboxed each visit
	st.life.ph.Budget(stPlace, 12*time.Second)
	st.life.ph.Budget(stClose, 8*time.Second)
	return st
}

func (st *Stash) Name() string      { return "stash" }
func (st *Stash) PhaseName() string { return st.life.ph.Phase().String() }

// stashable: the bag's items the ONE bag plan (loot/plan.go) sends to the
// stash, in bag order — the same plan percept's sell list comes from.
func stashable(s *percept.Snapshot, c *loot.Config) []percept.BagItem {
	now := time.Now()
	if c == nil {
		return nil
	}
	p := c.NewPlanner(s.Me.InvFree)
	p.Level = s.Me.Level
	p.UsesBow = s.Me.HasBow
	var out []percept.BagItem
	for _, b := range s.Bag {
		d, _ := p.Dispose(loot.Carried{Unit: uint32(b.Unit), Unique: int(b.Unique), Item: loot.Item{ID: b.ID, Name: b.Name, Quality: b.Qual}, GX: b.GX, GY: b.GY, Identified: b.Ident, Upgrade: b.Upgrade})
		if d == loot.DispStash && !InvTracker.Quarantined(uint32(b.Unit), now) {
			out = append(out, b)
		}
	}
	return out
}

func (st *Stash) Demand(s *percept.Snapshot) *arbiter.Demand {
	return st.life.keepBid(st.demand(s, time.Now()), s)
}

func (st *Stash) demand(s *percept.Snapshot, now time.Time) *arbiter.Demand {
	if servicesCooled() || !s.Valid || !s.Me.InTown || !stashWorks.Load() || now.Before(st.coolAt) || now.Before(st.fullUntil) {
		return nil
	}
	// R28: a 2x3 keeper (a unique bow) rode the cursor in town with no 2x3 hole
	// in the bag — the gate never drops a keeper, Equip could not park it: a
	// wedge. A keeper with no bag room belongs in the stash: Stash takes it.
	if s.Me.CursorItem {
		return &arbiter.Demand{Who: st.Name(), Class: arbiter.ClassService, Urgency: 0.86,
			Commit: arbiter.Commitment{MinHold: 5 * time.Second}}
	}
	if len(stashable(s, loot.Active())) == 0 {
		return nil
	}
	return &arbiter.Demand{Who: st.Name(), Class: arbiter.ClassService,
		Urgency: 0.5, // after Cain (0.55): identified keepers are the ones worth keeping
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (st *Stash) Needs(*percept.Snapshot) Needs { return st.life.needs() }

func (st *Stash) Begin(ctx *Ctx, resumed bool) {
	st.life.begin(resumed)
	if !resumed {
		sn := ctx.Snap
		ctx.Led.Append(verbs.Outcome{Verb: "stash", Holder: st.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("bag census: free=%d bag=%d keepers=%d unid=%d junk=%d", sn.Me.InvFree, len(sn.Bag), len(stashable(sn, loot.Active())), sn.Me.UnidentCount, sn.Me.JunkCount)})
	}
	if resumed && st.life.ph.Phase() != stClose {
		st.life.to(stClean, "resumed: re-verify from a clean screen")
	}
	if !resumed {
		st.moved, st.fails = 0, 0
	}
	st.target, st.pages, st.tabbed, st.shifting, st.noShift = 0, 0, false, false, false
	st.rewinds, st.triedPersonal = 0, false
}

func (st *Stash) Suspend(ctx *Ctx, _ phase.Reason) { st.life.suspend(ctx) }

func (st *Stash) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	st.life.end(ctx, v, why)
	if st.moved == 0 && v != phase.Done {
		st.fails++
		if st.fails >= 2 {
			stashWorks.Store(false)
			ctx.Led.Append(verbs.Outcome{Verb: "stash", Holder: st.Name(), Result: verbs.ResDeaf,
				Evidence: "two episodes moved nothing — stash belief retired"})
		}
	}
	coolOnEnd(v, why, &st.coolAt)
	st.target = 0
}

func (st *Stash) Step(ctx *Ctx) Status {
	l := &st.life
	if s, over := l.overrun(ctx); over {
		return s
	}
	if l.ph.Phase() == stClose {
		return l.closing(ctx)
	}
	s := ctx.Snap
	if !s.Valid || !s.Me.InTown {
		return l.wait(100 * time.Millisecond)
	}
	open := ctx.Screen != nil && screen.Stash&positiveOf(ctx) != 0
	switch l.ph.Phase() {
	case stClean:
		if s.Me.CursorItem {
			l.to(stWalk, "a cursor item to stash: straight to the chest")
			return l.running()
		}
		if len(stashable(s, loot.Active())) == 0 {
			return l.finish(ctx, phase.Done, phase.Completed, fmt.Sprintf("nothing to stash (moved %d)", st.moved))
		}
		if ok, stt := l.cleanScreen(ctx, 0); !ok {
			return stt
		}
		l.to(stWalk, "screen clear")
		return l.running()
	case stWalk:
		chest, ok := stashChest(ctx)
		if !ok {
			return l.finish(ctx, phase.Abandoned, phase.NoTarget, "no stash chest known in this town")
		}
		if chebyshev(s.Me.Pos, chest.Position) <= 4 {
			ctx.M.MoveStop()
			l.to(stOpen, "at the chest")
			return l.running()
		}
		moveTo(ctx, chest.Position, moveto.Opts{Holder: st.Name(), Purpose: moveto.Errand, Arrive: 3, MaxHold: 1200 * time.Millisecond, Fallback: true})
		return l.running()
	case stOpen:
		if open {
			// THE SHARED TAB FIRST (R29, the owner's photo + a click drill): the stash
			// opens on PERSONAL — full, and with NO page arrow — so five "page turns"
			// clicked nothing and it read "full on 5 pages" while Shared pages 4-5
			// stood empty. Shared has the pages.
			if !st.tabbed {
				k := shopScale(ctx)
				ctx.M.RealMenuClick(int(stashSharedTabX*k), int(stashSharedTabY*k))
				st.tabbed, st.clickT = true, time.Now()
				st.tab, st.want = stashTabShared, stashTabShared
				return l.wait(400 * time.Millisecond)
			}
			if !stashShot.Swap(true) {
				snapPNG(ctx, "logs/stash_open.png") // the gold button's geometry (withdraw errand, to come)
			}
			l.to(stLift, "stash open on the Shared tab (by sight)")
			return l.running()
		}
		if time.Since(st.clickT) < 1500*time.Millisecond {
			return l.wait(100 * time.Millisecond)
		}
		chest, ok := stashChest(ctx)
		if !ok {
			return l.finish(ctx, phase.Abandoned, phase.NoTarget, "stash chest unloaded")
		}
		if !st.hoverStep(ctx, chest) {
			return l.wait(50 * time.Millisecond) // the hover read lands next tick
		}
		st.clickT = time.Now()
		return l.wait(300 * time.Millisecond)
	case stLift:
		if !open {
			l.to(stClean, "stash closed under us")
			return l.running()
		}
		if s.Me.CursorItem {
			if cur := ctx.GR.GetData().Inventory.ByLocation(item.LocationCursor); len(cur) > 0 && cur[0].UnitID != st.target {
				st.target = cur[0].UnitID // adopted: an item already riding the cursor
				st.tgtW, st.tgtH = footprint(cur[0])
				st.tgtGX, st.tgtGY, st.pages = -1, -1, 0
			}
			l.to(stPlace, "keeper on the cursor")
			return l.running()
		}
		// CTRL+CLICK TRANSFER (owner, 2026-09-24: "it can shift click so things
		// transfer"): one click moves the bag item into the OPEN tab — no cursor,
		// no free-cell guessing. Runes, gems and the mod's rows get their own tabs.
		if st.target != 0 && st.shifting {
			if time.Since(st.clickT) < 600*time.Millisecond {
				return l.wait(100 * time.Millisecond)
			}
			st.shifting = false
			if s.Me.CursorItem {
				l.to(stPlace, "the transfer lifted it: place by sight")
				return l.running()
			}
			if !inBag(ctx, st.target) { // memory cannot see the mod's Shared pages 4-5: leaving the bag is the proof
				st.moved++
				ctx.Led.Append(verbs.Outcome{Verb: "stash", Holder: st.Name(), Result: verbs.ResDone,
					Evidence: fmt.Sprintf("ctrl-stashed unit %d (%dx%d) from bag (%d,%d) to the %s tab", int(st.target), st.tgtW, st.tgtH, st.tgtGX, st.tgtGY, stashTabName[st.tab])})
				st.target = 0
				return l.running()
			}
			// It stayed: this tab/page has no room. A special tab falls back to
			// Shared; Shared turns its page; after the pages, the cursor path.
			switch {
			case st.tab != stashTabShared:
				st.want = stashTabShared
			// OWNER (R38): "he tries to stash things but only on the 5th page, there
			// are other pages and a personal tab too". Shared is rewound to page 1 on
			// arrival (below), filled forward to page 5, then Personal.
			case st.tab == stashTabShared && st.pages < stashMaxPages-1:
				k := shopScale(ctx)
				ctx.M.RealMenuClick(int(stashNextPgX*k), int(stashNextPgY*k))
				st.pages++
				st.clickT = time.Now()
				return l.wait(400 * time.Millisecond)
			case st.tab == stashTabShared && !st.triedPersonal:
				st.want, st.triedPersonal = stashTabPersonal, true
			default:
				st.fullUntil = time.Now().Add(30 * time.Minute)
				return l.finish(ctx, phase.Abandoned, phase.Refused, "no room on Shared pages 1-5 or Personal")
			}
		}
		if st.target != 0 && !st.shifting && st.want == st.tab && !st.noShift && time.Since(st.clickT) < 600*time.Millisecond {
			return l.wait(100 * time.Millisecond)
		}
		var k percept.BagItem
		if st.target != 0 && inBag(ctx, st.target) {
			k = percept.BagItem{Unit: st.target, GX: st.tgtGX, GY: st.tgtGY}
		} else {
			keep := stashable(s, loot.Active())
			if len(keep) == 0 {
				return l.finish(ctx, phase.Done, phase.Completed, fmt.Sprintf("bag emptied of keepers (moved %d)", st.moved))
			}
			k = keep[0]
			c := loot.Classify(k.ID)
			st.target, st.tgtW, st.tgtH, st.tgtGX, st.tgtGY, st.pages, st.noShift = k.Unit, c.W, c.H, k.GX, k.GY, 0, false
			st.want = stashTabFor(c.Kind)
		}
		// THE TAB IS READ, NOT ASSUMED (owner, R33: "it goes to the gems tab,
		// returns to the regular tab"): the active header reads ~2x brighter
		// (measured on three captures). Two clicks that do not show the wanted tab
		// send this keeper to Shared, logged.
		if seen := activeStashTab(ctx.GR.Screenshot(), shopScale(ctx)); seen >= 0 {
			st.tab = seen
		}
		if st.want != st.tab {
			if st.tabTries >= 2 {
				ctx.Led.Append(verbs.Outcome{Verb: "stash", Holder: st.Name(), Result: verbs.ResWhiff,
					Evidence: fmt.Sprintf("the %s tab never showed (seen %s) — this keeper goes to Shared", stashTabName[st.want], stashTabName[st.tab])})
				st.want, st.tabTries = stashTabShared, 0
				return l.running()
			}
			kk := shopScale(ctx)
			ctx.M.RealMenuClick(int(stashTabX[st.want]*kk), int(stashSharedTabY*kk))
			st.tabTries++
			st.pages, st.clickT = 0, time.Now()
			return l.wait(400 * time.Millisecond)
		}
		st.tabTries = 0
		if st.tab == stashTabShared && st.rewinds < stashMaxPages-1 {
			k := shopScale(ctx)
			ctx.M.RealMenuClick(int(stashPrevPgX*k), int(stashNextPgY*k)) // back to page 1 (a page-1 press is harmless)
			st.rewinds++
			st.pages = 0
			st.clickT = time.Now()
			return l.wait(200 * time.Millisecond)
		}
		cx, cy := invCellPx(ctx, k.GX, k.GY)
		if st.noShift {
			ctx.M.RealMenuClick(cx, cy) // lift; stPlace sets it down by sight
		} else {
			// CTRL+CLICK = TRANSFER with the stash open (click drill, R31: Shift did
			// nothing; Ctrl moved a grand charm to Shared page 5). With NO panel open
			// the same gesture DROPS the item ("Ctrl + Left Click to Drop"): only ever
			// here, with the stash seen open (the open check above).
			ctx.M.RealMenuCtrlClick(cx, cy)
			st.shifting = true
		}
		st.clickT = time.Now()
		return l.wait(250 * time.Millisecond)
	case stPlace:
		if !s.Me.CursorItem {
			if !inBag(ctx, st.target) {
				st.moved++
				ctx.Led.Append(verbs.Outcome{Verb: "stash", Holder: st.Name(), Result: verbs.ResDone,
					Evidence: fmt.Sprintf("stashed unit %d (%dx%d) from bag (%d,%d), page turns %d", int(st.target), st.tgtW, st.tgtH, st.tgtGX, st.tgtGY, st.pages)})
			}
			st.target = 0
			l.to(stLift, "cursor empty")
			return l.running()
		}
		if time.Since(st.clickT) < 450*time.Millisecond {
			return l.wait(100 * time.Millisecond) // the last click is being judged
		}
		img := ctx.GR.Screenshot()
		gx, gy, ok := stashFreeBlock(img, st.tgtW, st.tgtH, shopScale(ctx))
		if !ok {
			if st.pages >= stashMaxPages {
				// Every page full: set it back where it came from and stop.
				if st.tgtGX >= 0 {
					cx, cy := invCellPx(ctx, st.tgtGX, st.tgtGY)
					ctx.M.RealMenuClick(cx, cy)
				}
				st.clickT = time.Now()
				st.fullUntil = time.Now().Add(30 * time.Minute) // R29: "stash full" retried every 10s
				return l.finish(ctx, phase.Abandoned, phase.Refused, fmt.Sprintf("stash full on %d pages — keeper put back", st.pages))
			}
			k := shopScale(ctx)
			ctx.M.RealMenuClick(int(stashNextPgX*k), int(stashNextPgY*k))
			st.pages++
			st.clickT = time.Now()
			return l.wait(400 * time.Millisecond)
		}
		cx, cy := stashBlockPx(gx, gy, st.tgtW, st.tgtH, shopScale(ctx))
		ctx.M.RealMenuClick(cx, cy)
		st.clickT = time.Now()
		return l.wait(300 * time.Millisecond)
	}
	return l.running()
}

// positiveOf: the screen oracle's positive panel set this tick (0 when unknown).
func positiveOf(ctx *Ctx) screen.Panel {
	if ctx.Screen == nil {
		return 0
	}
	return exec.Positive(*ctx.Screen)
}

// stashChest: the town's stash object, live first, then the map prior.
func stashChest(ctx *Ctx) (data.Object, bool) {
	d := ctx.GR.GetData()
	for _, ob := range d.Objects {
		if ob.Name == object.Bank && ob.ID != 0 {
			return ob, true
		}
	}
	if ad, ok := d.Areas[ctx.Snap.Me.Area]; ok {
		for _, ob := range ad.Objects {
			if ob.Name == object.Bank {
				return ob, true
			}
		}
	}
	return data.Object{}, false
}

// stashAim: the object hover sweep's candidate offsets (the Imbibe recipe, one aim
// per tick instead of a sleep): base projection, then up the chest's body.
var stashAim = func() (out [][2]int) {
	for dy := -40; dy <= 12; dy += 8 {
		for _, dx := range []int{0, -10, 10, -20, 20} {
			out = append(out, [2]int{dx, dy})
		}
	}
	return
}()

// hoverStep advances the sweep one tick: if the last aim hovers the object, click
// there and report true; otherwise aim the next candidate.
func (st *Stash) hoverStep(ctx *Ctx, ob data.Object) bool {
	if ob.ID == 0 {
		return false
	}
	if st.aimed {
		if hd := ctx.GR.GetData().HoverData; hd.IsHovered && hd.UnitID == ob.ID {
			ctx.M.BareClick(st.aimX, st.aimY)
			st.aimed = false
			return true
		}
	}
	ctx.M.MoveStop()
	me := ctx.GR.GetData().PlayerUnit.Position
	bx := int(float32((ob.Position.X-me.X)-(ob.Position.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
	by := int(float32((ob.Position.X-me.X)+(ob.Position.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2
	for n := 0; n < len(stashAim); n++ {
		o := stashAim[st.aimIdx%len(stashAim)]
		st.aimIdx++
		cx, cy := bx+o[0], by+o[1]
		if verbs.ClickableLogical(ctx.GR, cx, cy) {
			ctx.M.AimPhysical(cx, cy)
			st.aimX, st.aimY, st.aimed = cx, cy, true
			return false
		}
	}
	st.aimed = false
	return false
}

// inBag: the unit still sits in the inventory.
func inBag(ctx *Ctx, u data.UnitID) bool {
	for _, it := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		if it.UnitID == u {
			return true
		}
	}
	return false
}

// stashCellEmpty: a cell reads empty when every sample is near-black.
func stashCellEmpty(img image.Image, gx, gy int, k float64) bool {
	cx := (stashCell0X + stashPitchX*float64(gx)) * k
	cy := (stashCell0Y + stashPitchY*float64(gy)) * k
	for _, dx := range []float64{-14, 0, 14} {
		for _, dy := range []float64{-14, 0, 14} {
			r, g, b, _ := img.At(int(cx+dx*k), int(cy+dy*k)).RGBA()
			if r>>8 > stashEmptyMax || g>>8 > stashEmptyMax || b>>8 > stashEmptyMax {
				return false
			}
		}
	}
	return true
}

// stashFreeBlock: the top-left cell of the first empty w×h block, column-major
// (fills the page left to right like the game's own auto-place).
func stashFreeBlock(img image.Image, w, h int, k float64) (int, int, bool) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	var empty [stashCols][stashRows]bool
	for x := 0; x < stashCols; x++ {
		for y := 0; y < stashRows; y++ {
			empty[x][y] = stashCellEmpty(img, x, y, k)
		}
	}
	for x := 0; x+w <= stashCols; x++ {
		for y := 0; y+h <= stashRows; y++ {
			ok := true
			for i := 0; i < w && ok; i++ {
				for j := 0; j < h && ok; j++ {
					ok = empty[x+i][y+j]
				}
			}
			if ok {
				return x, y, true
			}
		}
	}
	return 0, 0, false
}

// stashBlockPx: screenshot px of a w×h block's center with top-left (gx,gy).
func stashBlockPx(gx, gy, w, h int, k float64) (int, int) {
	x := stashCell0X + stashPitchX*(float64(gx)+float64(w-1)/2)
	y := stashCell0Y + stashPitchY*(float64(gy)+float64(h-1)/2)
	return int(x * k), int(y * k)
}

var stashTabX = [...]float64{203, 297, 393, 487, 583}
var stashTabName = [...]string{"Personal", "Shared", "Gems", "Materials", "Runes"}

// stashTabFor: runes, gems and the mod's own rows have their own tabs.
func stashTabFor(k loot.Kind) int {
	switch k {
	case loot.KindRune:
		return stashTabRunes
	case loot.KindGem:
		return stashTabGems
	case loot.KindModUnknown:
		return stashTabMaterials
	}
	return stashTabShared
}

// activeStashTab reads which tab header is lit (-1 when none stands out).
func activeStashTab(img image.Image, k float64) int {
	spans := [...][2]float64{{160, 245}, {252, 340}, {348, 436}, {444, 530}, {540, 628}}
	best, bestV, second := -1, 0.0, 0.0
	for i, sp := range spans {
		sum, n := 0.0, 0
		for x := sp[0]; x < sp[1]; x += 3 {
			for y := 78.0; y < 100; y += 2 {
				r, g, b, _ := img.At(int(x*k), int(y*k)).RGBA()
				sum += float64(r>>8+g>>8+b>>8) / 3
				n++
			}
		}
		v := sum / float64(n)
		if v > bestV {
			second, bestV, best = bestV, v, i
		} else if v > second {
			second = v
		}
	}
	if bestV < 1.5*second { // no header clearly lit
		return -1
	}
	return best
}
