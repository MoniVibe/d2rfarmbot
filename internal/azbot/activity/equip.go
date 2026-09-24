package activity

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

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

// eqPhase is Equip's phase (contract v2).
type eqPhase uint8

const (
	eqClean eqPhase = iota // precondition: nothing on screen (but the bag, with an item on the cursor)
	eqDoor                 // THE IDENTIFY SIDE DOOR: the tome key, then the right-click cast opens the bag
	eqDress                // one gesture at a time: park a cursor item, or prime + shift-click a candidate
	eqClose                // janitor OFF: the bag closed by sight
)

func (p eqPhase) String() string {
	switch p {
	case eqClean:
		return "Clean"
	case eqDoor:
		return "Door"
	case eqDress:
		return "Dress"
	case eqClose:
		return "Close"
	}
	return "?"
}

// dressStage: where one Dress gesture stands (each stage is one Step, the
// waits between them are Status Waits — no sleeps).
type dressStage uint8

const (
	dressPick   dressStage = iota // judge the docket, choose the next gesture
	dressParked                   // a place-click landed: the cursor-empty read judges it
	dressPrimed                   // the plain click on the candidate landed: put back a grab, then shift
	dressRegrab                   // the grab was put back: now the shift-click
)

// Equip dresses her in the upgrades the Identify service unveils — the unique bow
// rode in her bag while she fought with a starter bow (the owner: "she has armor and
// a unique bow she could equip but she rolls with her current gear"). Town-only, one
// shift-click at a time, the equipped-list delta as the postcondition.
type Equip struct {
	life    svcLife[eqPhase]
	lastN   int
	fails   int
	strikes map[int]int // per-item silent refusals — three strikes refuse the item (P-4.4)
	door    bool        // Door: the tome key is pressed, the cast is next
	stage   dressStage
	cx, cy  int  // the candidate cell of the gesture in flight
	snapDue bool // photograph the first shift-click's result (equip_click.png)
}

var (
	_ Life   = (*Equip)(nil)
	_ Phased = (*Equip)(nil)
)

func NewEquip() *Equip {
	eq := &Equip{lastN: -1}
	eq.life.init(eq.Name(), eqClose, bagClaims[eqPhase])
	eq.life.ph.Budget(eqClean, 10*time.Second)
	eq.life.ph.Budget(eqDoor, 3*time.Second)
	eq.life.ph.Budget(eqDress, 60*time.Second)
	eq.life.ph.Budget(eqClose, 8*time.Second)
	return eq
}

func (eq *Equip) Name() string      { return "equip" }
func (eq *Equip) PhaseName() string { return eq.life.ph.Phase().String() }

func (eq *Equip) Demand(s *percept.Snapshot) *arbiter.Demand {
	return eq.life.keepBid(eq.demand(s), s)
}

func (eq *Equip) demand(s *percept.Snapshot) *arbiter.Demand {
	if servicesCooled() {
		return nil
	}
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

// cleanKeep: with an item riding the cursor an open bag is exactly what
// parking needs — Clean leaves it (an ESC with a held item is no cure).
func cleanKeep(cursorItem bool) screen.Panel {
	if cursorItem {
		return claimsBag
	}
	return 0
}

// Needs: the bag from the door on; in Clean only when an item rides the cursor.
func (eq *Equip) Needs(s *percept.Snapshot) Needs {
	if eq.life.clean() {
		return needsService(cleanKeep(s != nil && s.Me.CursorItem))
	}
	return eq.life.needs()
}

func (eq *Equip) Begin(ctx *Ctx, resumed bool) {
	eq.life.begin(resumed)
	eq.door, eq.stage = false, dressPick
	if !resumed {
		eq.lastN, eq.fails, eq.snapDue = -1, 0, false
		return
	}
	if cur := eq.life.ph.Phase(); bagResume(cur, eqClose) != cur {
		eq.life.to(bagResume(cur, eqClose), "resumed: "+cur.String()+" re-verifies from a clean screen")
	}
}

func (eq *Equip) Suspend(ctx *Ctx, _ phase.Reason) { eq.life.suspend(ctx) }

func (eq *Equip) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	eq.life.end(ctx, v, why)
	eq.lastN, eq.fails, eq.door, eq.stage, eq.snapDue = -1, 0, false, dressPick, false
}

// parkRegion finds the first free region of the 10x4 grid a w×h item fits,
// scanning rows top-down (occ[x][y] = occupied). Pure: the proven scan.
func parkRegion(occ *[10][4]bool, w, h int) (gx, gy int, ok bool) {
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
			return gx, gy, true
		}
	}
	return 0, 0, false
}

// parkClick — WARNING 9's recovery: with the panel open, place the cursor
// item into a free grid region its own footprint fits. The cursor-empty read
// (dressParked, 350ms later) is the only proof. false: no region fits.
func (eq *Equip) parkClick(ctx *Ctx) bool {
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
	gx, gy, ok := parkRegion(&occ, w, h)
	if !ok {
		return false // no region fits: the fence must make room first
	}
	// A place-click aims at the region's CENTER cell (the game anchors the
	// held item by its center).
	cx, cy := invCell(gx+(w-1)/2, gy+(h-1)/2)
	ctx.M.UIClick(cx, cy)
	return true
}

func cursorHeld(ctx *Ctx) bool {
	return len(ctx.GR.GetData().Inventory.ByLocation(item.LocationCursor)) > 0
}

func (eq *Equip) Step(ctx *Ctx) Status {
	l := &eq.life
	if st, over := l.overrun(ctx); over {
		return st
	}
	if l.ph.Phase() == eqClose {
		return l.closing(ctx)
	}
	s := ctx.Snap
	if !s.Valid {
		return l.wait(100 * time.Millisecond)
	}
	switch l.ph.Phase() {
	case eqClean:
		if ok, st := l.cleanScreen(ctx, cleanKeep(s.Me.CursorItem)); !ok {
			return st
		}
		if !s.Me.CursorItem && equippableCands(s) == 0 {
			return l.finish(ctx, phase.Done, phase.Completed, "docket empty")
		}
		if ctx.Cap == nil || ctx.Cap.Identify == nil {
			if !s.Me.CursorItem {
				equipWorks.Store(false)
				ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResDeaf,
					Evidence: "no ID-tome binding to open the panel with — equip belief retired"})
			}
			return l.finish(ctx, phase.Abandoned, phase.Precondition, "no ID-tome binding: no door to open, nothing safe to click")
		}
		if seen, _ := seenPanels(ctx); s.Me.CursorItem && seen&claimsBag != 0 {
			l.to(eqDress, "bag already open under a cursor item")
			return l.running()
		}
		l.to(eqDoor, "screen clear")
		return l.running()
	case eqDoor:
		// THE IDENTIFY SIDE DOOR (owner-observed: "it also opens when we use the
		// identify skill"): FOUR input classes were photographed failing to press
		// the panel hotkey (message, key-state stub, plain-vk, scancode — runs
		// 33/37/38/39). The cast path uses only proven gameplay primitives.
		if f, bad := l.interlocked(ctx); bad {
			l.to(eqClean, "interlock: "+f.String()+" up — no cast into it")
			return l.running()
		}
		if !eq.door {
			ctx.M.MoveStop()
			ctx.M.PressKey(ctx.Cap.Identify.Key)
			eq.door = true
			return l.wait(150 * time.Millisecond)
		}
		ctx.M.ClickRight(ctx.GR.GameAreaSizeX/2, ctx.GR.GameAreaSizeY/2+180)
		eq.door, eq.stage = false, dressPick
		l.to(eqDress, "cast: the bag opens")
		if s.Me.CursorItem {
			return l.wait(900 * time.Millisecond)
		}
		return l.wait(1200 * time.Millisecond)
	}
	return eq.dress(ctx, s)
}

// dress is one Dress Step.
func (eq *Equip) dress(ctx *Ctx, s *percept.Snapshot) Status {
	l := &eq.life
	switch eq.stage {
	case dressParked:
		eq.stage = dressPick
		if !cursorHeld(ctx) {
			return l.running() // clean — the docket resumes
		}
		if eq.fails++; eq.fails > 5 {
			return eq.retire(ctx, "cursor item would not park (no room or deaf panel) — retiring")
		}
		return l.wait(550 * time.Millisecond)
	case dressPrimed:
		// Normalize the cursor: the door-opening cast leaves the IDENTIFY HAND up;
		// the plain click on the (already identified) candidate consumes it
		// harmlessly. If that click grabbed the item instead, put it straight
		// back — never roam with an item on the cursor.
		eq.stage = dressRegrab
		if cursorHeld(ctx) {
			ctx.M.UIClick(eq.cx, eq.cy)
			return l.wait(250 * time.Millisecond)
		}
		fallthrough
	case dressRegrab:
		eq.stage = dressPick
		ctx.M.ShiftClick(eq.cx, eq.cy)
		if eq.fails == 1 {
			eq.snapDue = true // did the item move / cursor grab it? the photo answers
		}
		return l.wait(1200 * time.Millisecond) // let the shift-click land before judging
	}
	// dressPick.
	// SAFETY INTERLOCK: shift-click with a VENDOR up means SELL. Anything the
	// bag phase does not claim (the trade window, an NPC menu) sends her back
	// to Clean, which closes it — never a click into it.
	if f, bad := l.interlocked(ctx); bad {
		l.to(eqClean, "interlock: "+f.String()+" up — a shift-click would SELL")
		return l.running()
	}
	if eq.snapDue {
		eq.snapDue = false
		snapPNG(ctx, "logs/equip_click.png")
	}
	// WARNING 9: parking the cursor item precedes every ritual — including our
	// own docket. The cursor-empty read is the only proof; a bag with no room
	// hands the problem to the fence.
	if s.Me.CursorItem {
		if !eq.parkClick(ctx) {
			if eq.fails++; eq.fails > 5 {
				return eq.retire(ctx, "cursor item would not park (no room or deaf panel) — retiring")
			}
			return l.wait(900 * time.Millisecond)
		}
		eq.stage = dressParked
		return l.wait(350 * time.Millisecond)
	}
	if equippableCands(s) == 0 {
		return l.finish(ctx, phase.Done, phase.Completed, "dressed, or all refused (P-4.4)")
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
	eq.fails++
	if eq.fails > 12 {
		// The absolute breaker: every candidate froze through its whole strike
		// budget — the gesture itself is broken here, not one item.
		return eq.retire(ctx, fmt.Sprintf("12 shift-clicks, docket stuck at %d — equip belief retired (is -invkey right?)", s.Me.EquipCandCount))
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
		return l.finish(ctx, phase.Done, phase.Completed, "docket empty after refusals")
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
		return l.running()
	}
	// P-4.4: a candidate equips onto the ACTIVE hands — a bow needs the bow set
	// out before the click, or the gesture benches the javelins instead of the
	// white bow. The count audit still judges; a deaf swap burns one rotation.
	if it.IsBow && s.Me.WeaponKind != "bow" {
		ctx.M.KeyLane().Press(ctx.SwapKey)
		return l.wait(1200 * time.Millisecond)
	}
	eq.cx, eq.cy = invCell(it.GX, it.GY)
	if eq.fails == 1 {
		snapPNG(ctx, "logs/equip_open.png") // is the panel even up? the photo answers
	}
	ctx.M.UIClick(eq.cx, eq.cy)
	eq.stage = dressPrimed
	return l.wait(250 * time.Millisecond)
}

// retire drops the equip belief for the session and ends the episode.
func (eq *Equip) retire(ctx *Ctx, ev string) Status {
	ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResDeaf, Evidence: ev})
	equipWorks.Store(false)
	return eq.life.finish(ctx, phase.Abandoned, phase.Deaf, ev)
}
