package activity

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/loot"
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
	parks   int         // park clicks this episode (R25 cap)
	strikes map[int]int // per-item silent refusals — three strikes refuse the item (P-4.4)
	door    bool        // Door: the tome key is pressed, the cast is next
	stage   dressStage
	cx, cy  int  // the candidate cell of the gesture in flight
	snapDue bool // photograph the first shift-click's result (equip_click.png)
	// doorTry: doors opened this episode for an item riding the cursor (1:
	// the tome cast, 2: the inventory key) — the bag must be SEEN before a
	// place-click (relay R10: the cast did not raise the bag under a held
	// item and the blind park click dropped it on the floor).
	doorTry int
}

// equipCursorCool: Equip's cursor-parking bid rests until then after the bag
// could not be seen open under the item (the gate holds the item meanwhile).
var equipCursorCool time.Time

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
	// highest errand, above its own dressing and everyone's shopping. (It never
	// outbids a service at its own panel: that service owns the item — the
	// panel lock, svcLife.keepBid.)
	if s.Me.CursorItem && time.Now().After(equipCursorCool) {
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
// parking needs — Clean leaves it (an ESC with a held item is no cure). So is
// a trade window beside it: closing the trade window takes the bag with it
// (relay R10 ticks 94-99: the gate clicked the shop shut under Equip — "shop
// gone", then "inventory gone" — and the bag never rose again under the held
// item). The park is a plain place-click on a bag cell, never a shift-click:
// the vendor is safe from it.
func cleanKeep(cursorItem bool) screen.Panel {
	if cursorItem {
		return claimsBag | claimsTrade
	}
	return 0
}

// claimsTrade: the vendor's trade frame (the left half).
const claimsTrade = screen.Shop | screen.LeftPanel

// Needs: the bag from the door on; in Clean only when an item rides the
// cursor. While an item rides it (Clean, Dress) an open trade window is kept
// too — it holds the bag open for the park.
func (eq *Equip) Needs(s *percept.Snapshot) Needs {
	cursor := s != nil && s.Me.CursorItem
	if eq.life.clean() {
		return needsService(cleanKeep(cursor))
	}
	n := eq.life.needs()
	if cursor && eq.life.ph.Phase() == eqDress {
		n.Claims |= cleanKeep(true)
	}
	return n
}

func (eq *Equip) Begin(ctx *Ctx, resumed bool) {
	eq.life.begin(resumed)
	eq.door, eq.stage, eq.doorTry, eq.parks = false, dressPick, 0, 0
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
	eq.lastN, eq.fails, eq.door, eq.stage, eq.snapDue, eq.doorTry = -1, 0, false, dressPick, false, 0
}

// The bag grid of this mod: 10x8 (measured 2026-09-23, invCellPx; relay R10's
// fence Ctrl+clicks at invCellPx lifted items at rows 4-5, identify touched
// (9,5)). The vanilla 10x4 scan left rows 4-7 unseen: "would not park (no
// room)" with half the bag free.
const bagCols, bagRows = 10, 8

// parkRegion finds the first free region of the bag grid a w×h item fits,
// scanning rows top-down (occ[x][y] = occupied). Pure: the proven scan.
func parkRegion(occ *[bagCols][bagRows]bool, w, h int) (gx, gy int, ok bool) {
	for gy := 0; gy <= bagRows-h; gy++ {
	scan:
		for gx := 0; gx <= bagCols-w; gx++ {
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

// footprint is an item's bag footprint (unknown sizes count 1x1).
// footprint: an item's cell size, MOD-AWARE. R26 (owner: "it just moves it
// around"): it.Desc() is d2go's vanilla table, and the mod shifts misc rows by
// 15 — the TP tome (533) read as a 1x1 "Jawbone", so the park saw the tome's
// lower cell as free and clicked the potion onto it: a swap, forever.
func footprint(it data.Item) (w, h int) {
	if c := loot.Classify(int(it.ID)); c.W > 0 && c.H > 0 && c.VanillaID >= 0 {
		return c.W, c.H
	}
	w, h = it.Desc().InventoryWidth, it.Desc().InventoryHeight
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	return w, h
}

// bagOccupancy: the bag grid's occupied cells, from memory.
func bagOccupancy(items []data.Item) *[bagCols][bagRows]bool {
	var occ [bagCols][bagRows]bool
	for _, it := range items {
		iw, ih := footprint(it)
		for x := it.Position.X; x < it.Position.X+iw && x < bagCols; x++ {
			for y := it.Position.Y; y < it.Position.Y+ih && y < bagRows; y++ {
				if x >= 0 && y >= 0 {
					occ[x][y] = true
				}
			}
		}
	}
	return &occ
}

// regionFree: the w×h region at (gx,gy) lies in the grid and is empty.
func regionFree(occ *[bagCols][bagRows]bool, gx, gy, w, h int) bool {
	if gx < 0 || gy < 0 || gx+w > bagCols || gy+h > bagRows {
		return false
	}
	for x := gx; x < gx+w; x++ {
		for y := gy; y < gy+h; y++ {
			if occ[x][y] {
				return false
			}
		}
	}
	return true
}

// invFootPx: the physical client px of a w×h footprint's CENTRE whose top-left
// cell is (gx,gy) — the game anchors a held item by its centre. The measured
// grid (invCellPx) plus half the footprint in pitches.
func invFootPx(ctx *Ctx, gx, gy, w, h int) (int, int) {
	x, y := invCellPx(ctx, gx, gy)
	k := shopScale(ctx)
	return x + int(float64(w-1)*47.6/2*k), y + int(float64(h-1)*47.75/2*k)
}

// bagSeen: the bag is POSITIVELY seen open (sight, or a proven memory
// channel). Without a reading it is not seen: a place-click is never blind.
func bagSeen(ctx *Ctx) bool {
	seen, ok := seenPanels(ctx)
	return ok && seen&screen.Inventory != 0
}

// ParkCursor is THE place-click for an item riding the cursor, shared by
// Equip and the executive's gate (relay R10): only with the bag SEEN open —
// a place-click at a shut bag lands in the world and DROPS the item — into a
// free region of the measured 10x8 grid its footprint fits (prefer: the
// region it came from, when still free), as a real click at the measured
// physical cell geometry. The cursor-empty read on a later tick is the only
// proof. ok=false: nothing was clicked (why says what stood in the way).
func ParkCursor(ctx *Ctx, prefer *data.Position) (act string, ok bool) {
	if !bagSeen(ctx) {
		return "no park: the bag is not seen open (a place-click there would drop the item)", false
	}
	d := ctx.GR.GetData()
	cur := d.Inventory.ByLocation(item.LocationCursor)
	if len(cur) == 0 {
		return "no park: the cursor is empty in memory", false
	}
	w, h := footprint(cur[0])
	occ := bagOccupancy(d.Inventory.ByLocation(item.LocationInventory))
	gx, gy, fits := 0, 0, false
	if prefer != nil && regionFree(occ, prefer.X, prefer.Y, w, h) {
		gx, gy, fits = prefer.X, prefer.Y, true
	} else {
		gx, gy, fits = parkRegion(occ, w, h)
	}
	if !fits {
		return fmt.Sprintf("no park: no free %dx%d region in the %dx%d bag", w, h, bagCols, bagRows), false
	}
	x, y := invFootPx(ctx, gx, gy, w, h)
	if !ctx.M.RealMenuClick(x, y) {
		return fmt.Sprintf("park at cell (%d,%d) refused: no foreground", gx, gy), false
	}
	return fmt.Sprintf("park %dx%d item at cell (%d,%d) px %d,%d", w, h, gx, gy, x, y), true
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
		if s.Me.CursorItem && bagSeen(ctx) {
			l.to(eqDress, "bag already open under a cursor item")
			return l.running()
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
		if s.Me.CursorItem && eq.doorTry >= 1 && ctx.InvKey != 0 {
			// The second door under a held item: the cast did not raise the
			// bag (R10). The inventory key, as a scancode — judged by sight in
			// Dress like the cast; nothing is placed until the bag is seen.
			eq.doorTry++
			ctx.M.MoveStop()
			ctx.M.RealKey(uint16(ctx.InvKey))
			eq.door, eq.stage = false, dressPick
			l.to(eqDress, "inventory key: the bag must be seen before the place-click")
			return l.wait(900 * time.Millisecond)
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
			eq.doorTry++
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
	// WARNING 9: parking the cursor item precedes every ritual — including our
	// own docket, and the vendor interlock below (the park is a plain
	// place-click on a bag cell, not a shift-click: a trade window beside the
	// bag is safe, and it is what holds the bag open). The cursor-empty read
	// is the only proof; a bag with no room hands the problem to the fence.
	if s.Me.CursorItem {
		if !bagSeen(ctx) {
			// NEVER A BLIND PLACE-CLICK (relay R10: the cast did not raise the
			// bag under the held item; the park click landed in the world and
			// the item lay on the Lut Gholein floor). Another door, or give
			// the item to the gate, which HOLDS it — nothing is dropped.
			if eq.doorTry >= 2 || (eq.doorTry >= 1 && ctx.InvKey == 0) {
				ev := fmt.Sprintf("the bag was never seen open under the cursor item (%d door(s)) — no blind place-click: it would drop the item; the gate holds it", eq.doorTry)
				ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResDeaf, Evidence: ev})
				equipCursorCool = time.Now().Add(20 * time.Second)
				return l.finish(ctx, phase.Abandoned, phase.Deaf, ev)
			}
			l.to(eqDoor, fmt.Sprintf("bag not seen under the cursor item: door %d", eq.doorTry+1))
			return l.running()
		}
		// R25 (owner: "it tries dropping it where the tp tome is, and it just loops"):
		// the park click can land on an occupied cell and the item never leaves the
		// cursor. Three park clicks per episode, then Equip steps back for 60s and
		// the gate (junk: drop; keeper: open the bag and park) takes the item.
		if eq.parks++; eq.parks > 3 {
			equipCursorCool = time.Now().Add(60 * time.Second)
			ev := "3 park clicks and the item still rides the cursor — Equip steps back 60s; the gate takes it"
			ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResDeaf, Evidence: ev})
			return l.finish(ctx, phase.Abandoned, phase.Deaf, ev)
		}
		act, ok := ParkCursor(ctx, nil)
		ctx.Led.Append(verbs.Outcome{Verb: "equip", Holder: eq.Name(), Result: verbs.ResDone, Evidence: act})
		if !ok {
			if eq.fails++; eq.fails > 5 {
				return eq.retire(ctx, "cursor item would not park: "+act+" — retiring")
			}
			return l.wait(900 * time.Millisecond)
		}
		eq.stage = dressParked
		return l.wait(350 * time.Millisecond)
	}
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
