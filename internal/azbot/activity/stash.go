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
	stashCols     = 16
	stashRows     = 13
	stashCell0X   = 183.0
	stashCell0Y   = 129.0
	stashPitchX   = 47.5
	stashPitchY   = 47.7
	stashNextPgX  = 615.0 // the page arrow right of "Page n / 5"
	stashNextPgY  = 752.0
	stashMaxPages = 5
	stashEmptyMax = 22 // every sampled channel at or below this = an empty cell
)

// stashWorks: the live belief that the stash ritual moves items on this mod. Two
// episodes that move nothing retire it; NewWorld re-arms it.
var stashWorks atomic.Bool

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
	life   svcLife[stPhase]
	clickT time.Time
	target data.UnitID // the keeper being moved
	tgtW   int
	tgtH   int
	tgtGX  int
	tgtGY  int
	pages  int // page turns this lift
	moved  int
	fails  int
	coolAt time.Time
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
	st.life.ph.Budget(stLift, 4*time.Second)
	st.life.ph.Budget(stPlace, 12*time.Second)
	st.life.ph.Budget(stClose, 8*time.Second)
	return st
}

func (st *Stash) Name() string      { return "stash" }
func (st *Stash) PhaseName() string { return st.life.ph.Phase().String() }

// stashable: the bag's keepers, in bag order. Pure over the snapshot and policy.
func stashable(s *percept.Snapshot, c *loot.Config) []percept.BagItem {
	if c == nil {
		return nil
	}
	var out []percept.BagItem
	for _, b := range s.Bag {
		if b.Upgrade {
			continue
		}
		v, pinned := c.EvaluateCarried(loot.Carried{Item: loot.Item{ID: b.ID, Quality: b.Qual}, GX: b.GX, GY: b.GY, Identified: b.Ident})
		if pinned || v.Tier != loot.TierS {
			continue
		}
		switch v.Class.Kind {
		case loot.KindCharm, loot.KindCube, loot.KindTome, loot.KindQuest, loot.KindScroll, loot.KindPotion, loot.KindGold:
			continue
		}
		out = append(out, b)
	}
	return out
}

func (st *Stash) Demand(s *percept.Snapshot) *arbiter.Demand {
	return st.life.keepBid(st.demand(s, time.Now()), s)
}

func (st *Stash) demand(s *percept.Snapshot, now time.Time) *arbiter.Demand {
	if servicesCooled() || !s.Valid || !s.Me.InTown || !stashWorks.Load() || now.Before(st.coolAt) {
		return nil
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
	if resumed && st.life.ph.Phase() != stClose {
		st.life.to(stClean, "resumed: re-verify from a clean screen")
	}
	if !resumed {
		st.moved, st.fails = 0, 0
	}
	st.target, st.pages = 0, 0
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
			return l.wait(150 * time.Millisecond) // someone else's cursor item: wait for its park
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
			l.to(stLift, "stash open (by sight)")
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
			l.to(stPlace, "keeper on the cursor")
			return l.running()
		}
		if st.target != 0 && time.Since(st.clickT) < 600*time.Millisecond {
			return l.wait(100 * time.Millisecond)
		}
		keep := stashable(s, loot.Active())
		if len(keep) == 0 {
			return l.finish(ctx, phase.Done, phase.Completed, fmt.Sprintf("bag emptied of keepers (moved %d)", st.moved))
		}
		k := keep[0]
		c := loot.Classify(k.ID)
		st.target, st.tgtW, st.tgtH, st.tgtGX, st.tgtGY, st.pages = k.Unit, c.W, c.H, k.GX, k.GY, 0
		cx, cy := invCellPx(ctx, k.GX, k.GY)
		ctx.M.RealMenuClick(cx, cy)
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
				cx, cy := invCellPx(ctx, st.tgtGX, st.tgtGY)
				ctx.M.RealMenuClick(cx, cy)
				st.clickT = time.Now()
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
