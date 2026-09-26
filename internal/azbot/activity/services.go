// Town services: the ClassService activities. Everything here is a PORT of machinery
// proven live in the -charsitest / -akaratest training drills of 2026-07-19: banded
// approach, bare-click talk, Home/Down/Enter trade, instant-buy vendor cells, the
// repair-all button, and the mod item-ID ledger (names are scrambled; IDs cannot lie).
package activity

import (
	"fmt"
	"image/png"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/inventory"
	"github.com/hectorgimenez/koolo/internal/azbot/loot"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/moveto"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

// Mod item-ID ledger + measured panel coordinates (1920x1050 client).
const (
	modHPPotionID          = 602      // "Herb", 30g — proven by purchase delta
	modManaPotionID        = 607      // the old INVALID607 mystery, 60g — proven by purchase delta
	hpCellX, hpCellY       = 612, 478 // Akara Misc tab, red potion cell
	manaCellX, manaCellY   = 612, 525 // Akara Misc tab, blue potion cell
	repairBtnX, repairBtnY = 570, 747 // Charsi trade panel, repair-all (2g fixed Buckler 3/12->12/12)
)

// errand is the shared NPC-service state machine. One bounded slice per Step;
// its lifecycle (phases, claims, budgets, closing) is svcLife's.
type errand struct {
	svcLife[errandPhase] // Clean, Seek, Approach, Talk, Menu, Opening, Act, Close (errandphase.go)

	lastTrace string
	// tradeSelected: vendor stock LINGERS in memory after the trade window closes
	// (2026-09-23: buys fired into Drognan's Talk/Trade menu because stock still
	// read). Stock is trusted as "shop open" ONLY after Trade was selected on THIS
	// trip; a buy click that does nothing or a reset clears it.
	tradeSelected bool
	npcID         npc.ID
	act1NPC       npc.ID        // captured from the constructor on first use
	act2NPC       npc.ID        // service counterpart in Lut Gholein; zero means no alternate
	act3NPC       npc.ID        // service counterpart in Kurast Docks; zero means no alternate
	circled       bool          // the town circles were appended to this ring
	lastSeen      data.Position // where the NPC last streamed in (Approach walks there when it unloads)
	lastSeenAt    time.Time
	ring          []data.Position // search waypoints until the NPC loads
	ringIdx       int
	clickAt       time.Time
	tries         int
	// menuTry: which menu-option slot to ENTER on this attempt. The blind law was
	// "Home, Down, Enter = TRADE is the second item" — but NPC menus GROW (quest
	// lines appear), and run 26 spent 30 minutes failing a restock against a menu
	// that had presumably changed shape. The errand now self-discovers the trade
	// slot: each failed attempt tries a different Down-count (1,2,0,3), and the
	// vendor-stock oracle judges. What works is what's true.
	trade      bool // this errand ends in a TRADE window; readable stock proves it open
	menuTry    int
	blocked    int // consecutive blocked approach strides — the fire-pit-wall detector
	hoverFails int // consecutive hover-sweep misses — the torch-owns-this-bearing detector
	// Far movement goes by PLANNER (MoveTo) — town walls live in the static
	// grid, and local slides can never round a real wall (run 38: fence paced
	// the north wall at y=4897 while every ring waypoint sat past y=4930).
	// adopt: the errand (name) whose trade window at THIS errand's NPC stood
	// open when this episode began — Clean claims it and the first Step
	// adopts it (vendorHandoff). "" = none.
	adopt string
	// parks: cursor items this trip parked at our own open bag (pre); parkAt
	// the last one's click.
	parks  int
	parkAt time.Time
}

// vendorHandoff: the trade window a vendor errand left OPEN on a clean finish
// (janitor ON: End drops the claims without closing). The next errand at the
// SAME NPC adopts it — its Clean claims the vendor panels so the gate does not
// tear the window down, and its first Step goes straight to Act — instead of
// the janitor closing it and the walk, talk and menu starting over. Any other
// next holder claims nothing in its Clean and the janitor closes the window
// (one click: the explicit, cheap Clean).
var vendorHandoff struct {
	npc npc.ID
	who string
	at  time.Time
}

// handoffFresh: an open window is adoptable this long after its errand ended.
const handoffFresh = 3 * time.Second

// needs: the lifecycle's claims — plus, in Clean, the vendor panels of a
// trade window this episode is adopting (the gate must not close it first).
func (e *errand) needs() Needs {
	if e.adopt != "" && e.clean() {
		return needsService(claimsVendor)
	}
	return e.svcLife.needs()
}

// handOff (End, before the lifecycle resets): a Done at an open trade window
// leaves it for the next errand at the same NPC.
func (e *errand) handOff(v phase.Verdict, who string) {
	if JanitorOn && e.trade && v == phase.Done && e.ph.Phase() == erAct && e.tradeSelected {
		vendorHandoff.npc, vendorHandoff.who, vendorHandoff.at = e.npcID, who, time.Now()
	}
}

// adoptHandoff (Begin of a fresh episode): the previous errand at this
// errand's NPC left its trade window open moments ago. One adopter per
// handoff; a mismatch forgets it.
func (e *errand) adoptHandoff(ctx *Ctx) {
	e.adopt = ""
	h := vendorHandoff
	vendorHandoff.who = ""
	if !JanitorOn || !e.trade || h.who == "" || time.Since(h.at) > handoffFresh ||
		ctx == nil || ctx.Snap == nil || !ctx.Snap.Valid {
		return
	}
	e.useNPCForArea(ctx.Snap.Me.Area)
	if e.npcID == h.npc {
		e.adopt = h.who
	}
}

// initLife wires the errand's lifecycle for its owner service.
func (e *errand) initLife(who string) {
	e.init(who, erClose, errandClaims)
	for p, d := range errandBudgets {
		e.ph.Budget(p, d)
	}
}

// reset starts the trip over from the clean screen (an area change swapped
// the NPC): counters go, the phase returns to Clean.
func (e *errand) reset() {
	e.to(erClean, "reset")
	e.resetTrip()
}

// resetTrip forgets the trip's counters (not the phase).
func (e *errand) resetTrip() {
	e.ringIdx, e.tries, e.menuTry, e.blocked, e.hoverFails, e.parks = 0, 0, 0, 0, 0, 0
	e.circled = false
	e.tradeSelected = false
}

// resume re-verifies the phase after a preemption (errandResume).
func (e *errand) resume(ctx *Ctx) {
	seen, _ := seenPanels(ctx)
	menu := ctx.Snap != nil && ctx.Snap.MenuOpen
	cur := e.ph.Phase()
	if p := errandResume(cur, JanitorOn, menu, seen&screen.Shop != 0); p != cur {
		e.tradeSelected = false
		e.to(p, "resumed: "+cur.String()+" no longer holds")
	}
}

// useNPCForArea keeps the service state machine from carrying Act 1 town
// assumptions into Lut Gholein. The first live/static NPC observation is also
// used to replace the old hand-measured ring with the current map's positions.
func (e *errand) useNPCForArea(ar area.ID) {
	if e.act1NPC == 0 {
		e.act1NPC = e.npcID
	}
	want := e.act1NPC
	if e.act2NPC != 0 && ar.Act() == 2 {
		want = e.act2NPC
	}
	if e.act3NPC != 0 && ar.Act() == 3 {
		want = e.act3NPC
	}
	if e.npcID != want {
		e.npcID = want
		e.reset()
	}
}

// walkTo drives one MoveTo step toward goal. Returns true when the planner
// says stop (arrived is the caller's distance check; stalled/nopath = give up
// on this goal — MoveTo already dropped the trip).
func (e *errand) walkTo(ctx *Ctx, goal data.Position, who string) (exhausted bool) {
	o := moveto.Opts{Holder: who, Purpose: moveto.Errand, Arrive: 4}
	if ctx.Grid == nil {
		// No grid: the click gait — his town's fences are as invisible as hers (04:36).
		o.Click, o.MaxHold = true, 1100*time.Millisecond
	}
	return stalled(moveTo(ctx, goal, o))
}

// shopOpen: the trade window is up — the vendor stock reads AND the panel is
// on screen (stock LINGERS; the screen decides).
func shopOpen(ctx *Ctx) bool {
	return len(ctx.GR.GetData().Inventory.ByLocation(item.LocationVendor)) > 0 && game.ShopVisible(ctx.GR.Screenshot())
}

// step advances the errand toward an open trade panel. open=true when the shop
// is OPEN (vendor stock readable — the honest oracle) and the caller may act.
func (e *errand) step(ctx *Ctx, who string) errandStep {
	s := ctx.Snap
	// PHASE TRACE (2026-09-23: restock passed the town self-test yet failed in
	// the live run; the state machine must be visible wherever it runs).
	if tr := fmt.Sprintf("phase=%s menuTry=%d tries=%d menuOpen=%v tradeSelected=%v", e.ph.Phase(), e.menuTry, e.tries, s.MenuOpen, e.tradeSelected); tr != e.lastTrace {
		e.lastTrace = tr
		ctx.Led.Append(verbs.Outcome{Verb: "errand-trace", Holder: who, Result: verbs.ResRefused, Evidence: tr})
	}
	// THE PRECONDITION: an errand starts only from a clean screen (2026-09-24:
	// "fence phase=0 menuOpen=true", "repair starts with menuOpen=true" — the
	// previous errand's menu rode into the next one's talk). Clean claims
	// nothing: the janitor closes leftovers (ON), or the errand does, by sight.
	if e.ph.Phase() == erClean {
		if who := e.adopt; who != "" {
			e.adopt = "" // one look: open now, or the claims drop and the janitor closes it
			if shopOpen(ctx) {
				e.tradeSelected = true
				e.to(erAct, "adopted "+who+"'s open trade window (same NPC): no close, no walk, no talk")
				return errandStep{open: true}
			}
		}
		ok, st := e.cleanScreen(ctx, 0)
		if !ok {
			if st.V == phase.Abandoned {
				return errandStep{dead: true, why: st.Why, ev: st.Evidence}
			}
			return errandStep{wait: time.Until(st.WakeAt)}
		}
		e.to(erSeek, "screen clear")
	}
	d := ctx.GR.GetData()
	if e.ph.Phase() < erAct && heldOf(ctx, who) > errandBudget {
		return errandStep{dead: true, why: phase.Timebox,
			ev: fmt.Sprintf("npc=%d service attempt exceeded %s held in area %d", int(e.npcID), errandBudget, int(s.Me.Area))}
	}
	e.useNPCForArea(s.Me.Area)
	if e.ph.Phase() == erClean {
		return errandStep{} // the NPC changed with the area: re-verify the screen first
	}
	var target data.Monster
	found := false
	for _, mo := range d.Monsters {
		if mo.Name == e.npcID {
			target, found = mo, true
			break
		}
	}
	// Map NPC positions are available before a live unit streams into the
	// current room. They are a safe approach ring; the live Monster read above
	// remains the only authority for the eventual hover/click.
	if !found {
		if np, ok := d.AreaData.NPCs.FindOne(e.npcID); ok && len(np.Positions) > 0 {
			e.ring = append(e.ring[:0], np.Positions...)
			if e.ringIdx >= len(e.ring) {
				e.ringIdx = 0
			}
		} else if near, ok := npcNeighbour[e.npcID]; ok && len(e.ring) == 0 {
			// The map lacks this NPC but names a neighbour (owner, 2026-09-25:
			// "hratli is near meshif"): seek beside the neighbour first.
			if np, ok := d.AreaData.NPCs.FindOne(near); ok && len(np.Positions) > 0 {
				e.ring = append(e.ring[:0], np.Positions...)
				e.ringIdx = 0
			}
			// He wanders (owner: "hratli went back to the blacksmith, south"): the
			// neighbour is the first stop, the town circles follow.
			e.ring = append(e.ring, townCircles(ctx.Grid, s.Me.Pos)...)
		}
	}

	// THE SHOP IS OPEN WHEN ITS STOCK IS READABLE (photographed 12:36: Akara's
	// trade window stood WIDE OPEN while the errand logged "menu never opened"
	// and re-clicked her shut — this mod opens the vendor WITHOUT setting the
	// 0xF4 menu byte the talk phase waited on). Vendor stock is the honest
	// oracle the Lexicon already trusts: if it reads, we are trading. Jump
	// straight to act, and NEVER re-click an open shop closed. Trade errands
	// only — Heal wants the heal-dialog, not the merchant's shelves.
	if e.trade && e.tradeSelected && e.ph.Phase() >= erTalk && shopOpen(ctx) {
		e.to(erAct, "shop open: stock readable and trade panel on screen")
		return errandStep{open: true}
	}

	// NO RING AT ALL (R78: the map named no Hratli in Kurast Docks, the ring was
	// empty and Repair gave up in 0.2s): circle the town from here — two rings at
	// 40 and 80 tiles — until the NPC streams in.
	if !found && len(e.ring) == 0 && s.Me.InTown {
		e.ring = townCircles(ctx.Grid, s.Me.Pos)
		e.ringIdx = 0
	}
	// R81: the map DID place Hratli — 870 tiles off, behind a wall — so the ring
	// was that one dead point. In town the walkable circles always follow what
	// the map says (once per ring).
	if !found && s.Me.InTown && !e.circled && len(e.ring) > 0 {
		e.ring = append(e.ring, townCircles(ctx.Grid, s.Me.Pos)...)
		e.circled = true
	}
	switch e.ph.Phase() {
	case erSeek: // walk the ring until the NPC loads
		if found {
			e.to(erApproach, "npc loaded")
			return errandStep{}
		}
		if e.ringIdx >= len(e.ring) {
			return errandStep{dead: true, why: phase.NoTarget,
				ev: fmt.Sprintf("npc=%d never loaded on the whole ring (P-6.2)", int(e.npcID))} // walked the whole ring, no NPC — give up this trip
		}
		wp := e.ring[e.ringIdx]
		if chebyshev(s.Me.Pos, wp) <= 5 {
			e.ringIdx++
			forgetMove(who) // the next ring waypoint is a new trip
			return errandStep{}
		}
		if e.walkTo(ctx, wp, who) {
			e.ringIdx++ // no route to this waypoint — try the next
		}
	case erApproach: // the band (4..7) — closer breaks hover, farther breaks the click
		if found {
			e.lastSeen, e.lastSeenAt = target.Position, time.Now()
		}
		if !found {
			// R82: Hratli sat at the streaming edge — loaded, unloaded, every 0.8s,
			// and every unload sent the errand back to Seek without a step taken.
			// A recent sighting is walked toward; only a stale one re-seeks.
			if !e.lastSeenAt.IsZero() && time.Since(e.lastSeenAt) < 8*time.Second {
				e.walkTo(ctx, e.lastSeen, who)
				return errandStep{}
			}
			e.to(erSeek, "npc unloaded")
			return errandStep{}
		}
		dist := chebyshev(s.Me.Pos, target.Position)
		if talkBand(dist) == 0 {
			ctx.M.MoveStop()
			e.to(erTalk, fmt.Sprintf("in band, dist %d", dist))
			return errandStep{}
		}
		if talkBand(dist) < 0 {
			// Proportional backstep TO THE BAND, not a triple-distance fling —
			// the old *3 threw her from the clinch past 7 and the approach
			// re-overshot: the torch dance (the owner, 10:2x: "walks back and
			// forth to Charsi's torch").
			adx, ady := s.Me.Pos.X-target.Position.X, s.Me.Pos.Y-target.Position.Y
			m := maxInt(absInt(adx), absInt(ady))
			if m == 0 {
				m = 1
			}
			// 8 out, not 5: from dist 3 a 5-out point sits 2 tiles away, inside Stride's
			// arrived-early radius — the backstep "arrived" instantly forever
			// (2026-09-23 selftest: 75s of gain=0 arrived-early). Stride stops ~2 short,
			// landing ~6 out: inside the 4..7 band.
			back := data.Position{X: target.Position.X + adx*8/m, Y: target.Position.Y + ady*8/m}
			moveTo(ctx, back, moveto.Opts{Holder: who, Purpose: moveto.Errand, MaxHold: 400 * time.Millisecond, Fallback: true})
			return errandStep{}
		}
		// FAR approach goes by PLANNER (real walls demand real routing); the last
		// stretch is local footwork around camp furniture the grids can't see.
		if dist > 12 {
			if e.walkTo(ctx, target.Position, who) {
				return errandStep{dead: true, why: phase.Unreachable,
					ev: fmt.Sprintf("npc=%d: planner found no route from (%d,%d) at dist %d (P-6.2)", int(e.npcID), s.Me.Pos.X, s.Me.Pos.Y, dist)}
			}
			return errandStep{}
		}
		// AIM AT THE BAND, never the body: sliding at the NPC's center lands in
		// the hover-breaking clinch and the backstep oscillates. A point 5 out
		// on the current bearing IS the destination.
		adx, ady := s.Me.Pos.X-target.Position.X, s.Me.Pos.Y-target.Position.Y
		bm := maxInt(absInt(adx), absInt(ady))
		if bm == 0 {
			bm = 1
		}
		bandPt := data.Position{X: target.Position.X + adx*5/bm, Y: target.Position.Y + ady*5/bm}
		hold := 500 * time.Millisecond
		// PLANNED, and on a wall streak ARC: the camps put fire pits and tables between
		// her and the NPC — none of it in any collision grid. A straight stride beat
		// its head on Akara's fire 28 times in a row (run 31, the owner: "she is
		// circling akara... goes back and forth"). Blocked steps (a stride that
		// moved nothing, or a planner refusal) = walk the arc 90° around the NPC
		// and come at them from a new bearing. Planned pulses are short, so the
		// streak is five, not the old three 1.5s slides.
		st := moveTo(ctx, bandPt, moveto.Opts{Holder: who, Purpose: moveto.Errand, Arrive: 1, MaxHold: hold, Fallback: true})
		if st.Blocked || stalled(st) {
			e.blocked++
		} else {
			e.blocked = 0
		}
		if e.blocked >= 5 {
			e.blocked = 0
			adx, ady := s.Me.Pos.X-target.Position.X, s.Me.Pos.Y-target.Position.Y
			arc := data.Position{X: target.Position.X + ady, Y: target.Position.Y - adx}
			moveTo(ctx, arc, moveto.Opts{Holder: who + "/arc", Purpose: moveto.Errand, MaxHold: 900 * time.Millisecond, Fallback: true})
		}
	case erTalk: // hover-confirm, BARE click, wait for the menu byte
		// Clicking Akara opens a DIALOG (MenuOpen high) that the Menu phase
		// steers to Trade — REVERTED here after run 72 froze at gold=741 with
		// this path removed (the 12:44 screenshot showed the END state, an open
		// shop, not HOW it opened: dialog → Home/Down/Enter → trade). The
		// vendor-stock short-circuit above handles the already-open case so an
		// open shop is never re-navigated closed.
		if s.MenuOpen {
			// OUR menu only (2026-09-23 selftest photo: a walk-click beside another
			// NPC opened HIS Talk/Introduction/Gossip menu; the errand steered it six
			// times and never traded). A menu that opened without our talk click in
			// the last few seconds is a stray: back to Clean, which closes it by
			// sight (never a blind ESC), and talk again.
			if !talkedRecently(e.clickAt, time.Now()) {
				ctx.Led.Append(verbs.Outcome{Verb: "errand", Holder: who, Result: verbs.ResRefused,
					Evidence: "stray NPC menu (not opened by our talk click) — closing by sight"})
				e.to(erClean, "stray NPC menu")
				return errandStep{}
			}
			e.to(erMenu, "menu byte after our talk click")
			return errandStep{}
		}
		if !found {
			e.to(erSeek, "npc unloaded")
			return errandStep{}
		}
		// THE DIAG'S VERDICT (12:25: "dist=20 ... hoverConfirmed=true"): a
		// missed bare click is a MOVE order — she sails past the NPC, and
		// every retry then fires a 20-tile walk-to-talk that the 3 s window
		// truncates before arrival, forever. The band is re-checked on EVERY
		// attempt; beyond 8 she re-approaches instead of clicking.
		if chebyshev(s.Me.Pos, target.Position) > 8 {
			e.to(erApproach, "out of the talk band")
			return errandStep{}
		}
		// One bounded click attempt per 3s: the click starts a WALK-to-talk; let
		// it play out, polling lightly for the menu byte.
		if left := 3*time.Second - time.Since(e.clickAt); left > 0 {
			return errandStep{wait: minDur(left, 150*time.Millisecond)}
		}
		if e.tries >= 6 {
			NoteGhost() // P-4.8a: a dead NPC counts toward the world's poison
			return errandStep{dead: true, why: phase.Deaf,
				ev: fmt.Sprintf("npc=%d: 6 talk attempts, menu never opened (hover or click deaf — P-6.2)", int(e.npcID))}
		}
		me := d.PlayerUnit.Position
		bx := int(float32((target.Position.X-me.X)-(target.Position.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
		by := int(float32((target.Position.X-me.X)+(target.Position.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2 + game.UnitAimDY() // NPC body, not feet
		// Tracked, short, most-likely-first (hoverUnitTracked): ~1s, not 3-8s.
		px, py, confirmed := hoverUnitTracked(ctx, target.UnitID)
		e.tries++
		if e.tries == 3 {
			snapPNG(ctx, "logs/talk_fail.png") // P-6.2: the third deaf talk photographs itself
			ctx.Led.Append(verbs.Outcome{Verb: "errand", Holder: who, Result: verbs.ResWhiff,
				Evidence: fmt.Sprintf("talk diag: npc=%d unit@(%d,%d) me@(%d,%d) dist=%d proj=(%d,%d) hoverConfirmed=%v",
					int(e.npcID), target.Position.X, target.Position.Y, me.X, me.Y,
					chebyshev(data.Position{X: me.X, Y: me.Y}, target.Position), bx, by, confirmed)})
		}
		if !confirmed {
			// A WALL OWNS THE HOVER, not the town (04:53, the screenshot: a
			// stone wall between the barbarian and Akara ate every aim). For a
			// STATIONARY town NPC whose position is solid, a blind talk-click
			// at the projection is safe — no shift means walk-or-talk, never an
			// attack — and the walk-to-talk clears the wall the hover couldn't.
			// After 3 hover misses, click blind at the label; keep arcing too.
			// NEVER BLIND AMONG NEIGHBOURS (2026-09-26: the fence's blind label
			// click landed on Warriv beside the vendor, the menu's second line
			// was "Go East", and she sailed to Act 2 mid-errand).
			crowded := false
			for _, mo := range d.Monsters {
				if mo.UnitID != target.UnitID && mo.IsGoodNPC() && chebyshev(mo.Position, target.Position) <= 6 {
					crowded = true
					break
				}
			}
			if e.hoverFails++; e.hoverFails >= 3 && !crowded {
				e.hoverFails = 0
				if cx, cy, ok := verbs.ClampClickLogical(ctx.GR, bx, by-30); ok {
					ctx.M.BareClick(cx, cy) // the label sits above the base
				}
				e.clickAt = time.Now()
				return errandStep{}
			}
			// A tracked miss retries next tick. The old hover-arc side-step spent 0.9s
			// for gain=0 and re-approached from scratch (2026-09-23 gap analysis).
			return errandStep{}
		}
		e.hoverFails = 0
		ctx.M.BareClick(px, py)
		e.clickAt = time.Now()
	case erMenu: // HOME normalizes, then ENTER the menuTry'th candidate slot
		if !s.MenuOpen {
			if time.Since(e.clickAt) > 4*time.Second {
				e.to(erTalk, "menu died") // talk again
			}
			return errandStep{wait: 100 * time.Millisecond}
		}
		ctx.M.MoveStop()
		// MENUS DEMAND TRUE FOREGROUND (2026-09-23 photo: Drognan's Talk/Trade/Cancel
		// menu stood open while posted Home/Down/Enter did nothing). Real scancodes
		// with the game foregrounded — the same law the shop clicks obey.
		ctx.M.RealKey(0x24) // HOME
		downs := []int{1, 2, 0, 3}[e.menuTry%4]
		for i := 0; i < downs; i++ {
			ctx.M.RealKey(0x28) // DOWN
		}
		ctx.M.RealKey(0x0D)    // ENTER
		e.tradeSelected = true // a Trade selection was actually made
		e.to(erOpening, fmt.Sprintf("trade selected (menu slot try %d)", e.menuTry))
		return errandStep{wait: 100 * time.Millisecond}
	case erOpening:
		// The trade window takes a moment to populate after ENTER. Poll, don't
		// glance (2026-09-23 trace: an instant check read 0 stock, ESC'd the
		// opening shop and burned all six menu tries in three seconds). The
		// short-circuit above takes the open shop; here it has not opened yet.
		if e.ph.InPhase() < 2*time.Second {
			return errandStep{wait: 100 * time.Millisecond}
		}
		// Trade did not open: the slot was wrong (menus GROW when quest lines appear —
		// the blind 'second item is Trade' law cost run 26 a 30-minute restock loop).
		// Back all the way out (Clean closes the menu by sight) and try the next
		// slot on a fresh talk.
		e.menuTry++
		if e.menuTry >= 6 {
			return errandStep{dead: true, why: phase.Deaf,
				ev: fmt.Sprintf("npc=%d: no menu slot opened a trade in %d attempts", int(e.npcID), e.menuTry)}
		}
		e.tradeSelected = false
		e.to(erClean, fmt.Sprintf("trade never opened (menu slot try %d)", e.menuTry))
	case erAct:
		// The window we traded in is gone (the short-circuit above failed):
		// start over from a clean screen rather than click into whatever is up.
		e.tradeSelected = false
		e.to(erClean, "trade window no longer on screen")
	}
	return errandStep{}
}

// talkBand: 0 inside the 4..7 talk band, <0 too close (hover breaks), >0 too
// far (the click becomes a walk).
func talkBand(dist int) int {
	switch {
	case dist < 4:
		return -1
	case dist > 7:
		return 1
	}
	return 0
}

// talkedRecently: a menu that opens within 5s of our talk click is ours.
func talkedRecently(clickAt, now time.Time) bool {
	return !clickAt.IsZero() && now.Sub(clickAt) <= 5*time.Second
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// closeShopStep is ONE sight-driven step toward a closed trade window for the
// manual -selftest harness (no executive, no Eye): the panel's own red X, then
// the pause peel. Never ESC. done=true when nothing is left to click.
func closeShopStep(ctx *Ctx) (done bool, wait time.Duration) {
	if x, y, ok := game.ShopOpenX(ctx.GR.Screenshot()); ok {
		ctx.M.RealMenuClick(x, y)
		return false, 400 * time.Millisecond
	}
	EnsureWorld(ctx.GR, ctx.M)
	return true, 0
}

// healerHeals: the live belief that Akara's talk-heal works on this mod (vanilla law:
// the Act 1 healer refills life+mana the moment you talk). Disproven by the Heal
// errand itself — two talks with no HP change flips it false so the wounded-pending
// gate can never deadlock her in town on a mod that removed the mercy.
var healerHeals atomic.Bool

func init() { healerHeals.Store(true) }

// globalServiceCoolUntil: the IDLE BREAKER's lever (the owner, 04:47: 'hangs
// on Akara'). When the executive sees sustained town idle — a service
// abandoned with nothing re-bidding — it cools EVERY service at once so the
// march reclaims the actuator; the errands retry from the field next trip.
var globalServiceCoolUntil time.Time

// CoolAllServices silences all service demands for d (called by the executive).
func CoolAllServices(d time.Duration) { globalServiceCoolUntil = time.Now().Add(d) }

// lineCool: docket lines silenced one by one (2026-09-26: "identify" waited on
// a Cain who is not in camp until Tristram — the valve cooled EVERY service,
// restock with it, and she left town with 219k gold and no healing).
var lineCool = struct {
	sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

// CoolServiceLine silences one docket line for d (the idle valve's scalpel).
func CoolServiceLine(line string, d time.Duration) {
	lineCool.Lock()
	lineCool.until[line] = time.Now().Add(d)
	lineCool.Unlock()
}

// lineOpen: the docket line is not silenced.
func lineOpen(line string) bool {
	lineCool.Lock()
	defer lineCool.Unlock()
	return time.Now().After(lineCool.until[line])
}

func servicesCooled() bool { return time.Now().Before(globalServiceCoolUntil) }

// ServicesPending reports whether a town errand is waiting: a real belt deficit she can
// afford, gear worn to the quarter, or WOUNDS a free healer can close. The class ladder
// puts Travel ABOVE Service, so the travel activities consult this and stand down —
// otherwise Travel starves the errands forever and she marches out with an empty belt
// (the latent starvation bug) or at 37% straight back into the pack that chased her home
// (measured 04:13:07: Breakout's portal landed her in town with zero gold and zero junk —
// nothing pended, Advance marched her out wounded two seconds later).
// ServicesPending: some town errand is due (Advance holds the march for it).
func ServicesPending(s *percept.Snapshot) bool { return ServicesPendingWhy(s) != "" }

// ServicesPendingWhy names the docket line that holds the march ("" = none) — logged
// by the idle breaker so a deadlocked docket names itself (R15: 30 min idle in town).
func ServicesPendingWhy(s *percept.Snapshot) string {
	if servicesCooled() {
		return "" // the idle breaker handed the wheel to the march
	}
	if s.Me.HPPct <= 55 && healerHeals.Load() {
		if lineOpen("heal") {
			return "heal" // Akara's refill is free; leaving town below the drink line is denial
		}
	}
	// Dressing and identifying are errands too — Travel outranks Service by class,
	// and without these lines Advance marched her out with a unique bow still bagged
	// the moment the potions were paid for (run 34: Equip got ONE step). P-4.8 with
	// the escape clause run 42 taught: a pending docket whose service CANNOT run
	// (no landing room, nothing left to sell) must not gate the march — she idled
	// in town 12 minutes on that deadlock.
	if equippableCands(s) > 0 && equipWorks.Load() && s.Me.InvFree >= 6 {
		if lineOpen("equip") {
			return "equip"
		}
	}
	if s.Me.UnidentCount > 0 && cainIDWorks.Load() { // Cain identifies (owner 2026-09-24); no tome charges needed
		if lineOpen("identify") {
			return "identify"
		}
	}
	// P-8.1: banked points are an errand — the march waited on every other
	// docket while 20 points sat in the bank and the unique stayed bagged
	// (run 52: Advance took the actuator at 0.20 over Spend's 0.35 by class).
	// Same escape clause: a retired spend belief does not gate the march.
	if s.Me.StatPoints > 0 && spendWorks.Load() {
		if lineOpen("spend-stats") {
			return "spend-stats"
		}
	}
	if s.Me.SkillPoints > 0 && skillSpendWorks.Load() {
		if lineOpen("spend-skills") {
			return "spend-skills" // P-8.7: banked dps is an errand
		}
	}
	// P-4.5: a near-empty tome is an errand too — marching out with no escape
	// hatch is how retreats lose their destination (P-2.3), and an empty ID
	// tome starves the whole judging pipeline.
	// TP only: identify is Cain's now (owner 2026-09-24), so an empty ID tome is not an
	// errand — R15 idled 30 min in town on "pending=scrolls" with the ID tome at 0.
	if s.Me.TPScrolls >= 0 && s.Me.TPScrolls <= 2 && scrollGap(s.Me.TPScrolls, s.Me.Gold) > 0 {
		if lineOpen("scrolls") {
			return "scrolls"
		}
	}
	if s.Me.Gold >= 10 && s.Me.MinDurPct < inventory.TownRepairPct {
		if lineOpen("repair") {
			return "repair"
		}
	}
	if s.Me.Gold >= 100 && time.Now().After(potionCoolUntil) {
		if hp, mana := plan(s); hp+mana >= 2 {
			if lineOpen("potions") {
				return "potions"
			}
		}
	}
	// The Fence's docket: broke with junk to sell, or a heavy bag either way.
	// ...or a loot room trip (Haul): she came home to make room for a tier S
	// drop, and every junk cell sold is room.
	// R27: haul's return leg left before the stash ran, so every tier-S drop
	// cost a town trip that emptied nothing. Keepers waiting = an errand.
	// R31: a keeper lifted mid-stash left the bag, the docket read clear, and
	// the march preempted Stash with the item on the cursor. A cursor item in
	// town is always an errand.
	if s.Me.InTown && s.Me.CursorItem {
		if lineOpen("cursor") {
			return "cursor"
		}
	}
	if stashWorks.Load() && len(stashable(s, loot.Active())) > 0 {
		if lineOpen("stash") {
			return "stash"
		}
	}
	// OWNER (R27): the bag must be CLEARED every visit — any item the bag plan sells.
	if s.Me.JunkCount > 0 {
		if lineOpen("fence") {
			return "fence"
		}
	}
	return ""
}

// ---------------------------------------------------------------- vendor errands on contract v2

// pre is the vendor services' common head: the phase budget, the close phase
// (janitor OFF), the snapshot guards. stop=true: return st.
func (e *errand) pre(ctx *Ctx) (st Status, stop bool) {
	if st, over := e.overrun(ctx); over {
		return st, true
	}
	if e.ph.Phase() == erClose {
		return e.closing(ctx), true
	}
	s := ctx.Snap
	if !s.Valid {
		return e.wait(100 * time.Millisecond), true
	}
	if s.Me.CursorItem {
		// At our own open trade window the bag is up beside it: the item is
		// parked into a free cell (ParkCursor — the bag SEEN, the measured
		// grid) rather than waited on until the panel lock's budget runs out.
		// A few tries per trip; then the wait, and the budget, stand.
		if e.ph.Phase() == erAct && e.parks < 3 && time.Since(e.parkAt) > 450*time.Millisecond && ctx.GR != nil {
			if act, ok := ParkCursor(ctx, nil); ok {
				e.parks++
				e.parkAt = time.Now()
				ctx.Led.Append(verbs.Outcome{Verb: "errand", Holder: e.ph.Act, Result: verbs.ResDone, Evidence: "cursor item at our shop: " + act})
				return e.wait(450 * time.Millisecond), true
			}
		}
		return e.wait(150 * time.Millisecond), true // WARNING 9: a shop or NPC click with a held item misfires
	}
	return Status{}, false
}

// drive runs one errand Step for its owner. open=true: the trade window is up
// and the owner acts in this Step; dead=true: st is the abandoned verdict.
func (e *errand) drive(ctx *Ctx, who string) (st Status, open, dead bool) {
	es := e.step(ctx, who)
	switch {
	case es.dead:
		ctx.Led.Append(verbs.Outcome{Verb: "errand", Holder: who, Result: verbs.ResDeaf, Evidence: es.why.String() + ": " + es.ev})
		return e.finish(ctx, phase.Abandoned, es.why, es.ev), false, true
	case es.open:
		return Status{}, true, false
	case es.wait > 0:
		return e.wait(es.wait), false, false
	}
	return e.running(), false, false
}

// coolOnEnd: an episode that ran out of budget or wedged the screen cools its
// service like a dead trip (P-4.2a: an abandoned trip stays abandoned), unless
// the Step already set a longer cool.
func coolOnEnd(v phase.Verdict, why phase.Reason, until *time.Time) {
	if v == phase.Abandoned && (why == phase.Timebox || why == phase.UIWedge) {
		if t := time.Now().Add(120 * time.Second); t.After(*until) {
			*until = t
		}
	}
}

// ---------------------------------------------------------------- Restock (ClassService)

// Restock keeps the belt at doctrine: one row of mana, the rest HP — scaled to the
// belt she actually wears — and the TP tome at its floor of 8 scrolls (P-4.5: an
// empty tome un-writes P-2.3's destination; the owner: "she's out already").
// Bids softly in town when the belt has real gaps and she can pay; never bids in
// the field (travel-to-town for potions is a Goal decision).
type Restock struct {
	e          errand
	potBought  int
	lastBeltHP int
	lastBeltMN int
	beltFrozen int // potion buys with no belt rise — a full belt eats nothing
	scrBought  int
	probeTP    int // scroll-cell discovery cursors (P-4.5: cells are LEARNED)
	probeID    int
	frozenTP   int // learned-cell buys with no tome delta (WARNING 2 family)
	frozenID   int
	nextAt     time.Time // ghost-abort cooldown: a dead window stays dead for minutes,
	// not seconds — eight identical aborts in eight minutes taught the churn (12:07)
	potFails int      // consecutive verified-buy failures this trip
	noMana   bool     // the open vendor stocks no mana potion
	buy      buyTx    // the verified potion purchase in flight
	kind     string   // its kind: "health" | "mana"
	scr      scrollTx // the scroll purchase in flight
	sbuy     buyTx    // the verified scroll purchase in flight (stock-read)
	sbuyTome int
}

var (
	_ Life   = (*Restock)(nil)
	_ Phased = (*Restock)(nil)
)

// potCount: total bottles of one kind she owns, belt AND bag — the only honest
// BUY check. A bottle that landed in the bag still landed; the belt-only read
// misread bag-landings as ghosts (12:15).
func potCount(ctx *Ctx, id int) int {
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

// scrollWorks: the live belief that scroll restocking works here — retired when
// probing exhausts every candidate or a learned cell freezes (WARNING 2).
var scrollWorks atomic.Bool

func init() { scrollWorks.Store(true) }

const tpScrollFloor = 8

// scrollGap is one tome's gap to doctrine — 0 when the tome is absent (a loose
// scroll is not a tome) or the purse cannot pay.
func scrollGap(gauge, gold int) int {
	if gauge < 0 || gauge >= tpScrollFloor || gold < 200 || !scrollWorks.Load() {
		return 0
	}
	return tpScrollFloor - gauge
}

// scrollDeficit sums both tomes' gaps (P-4.5: TP for the escape hatch, ID for
// the judging of the bag).
func scrollDeficit(s *percept.Snapshot) int {
	return scrollGap(s.Me.TPScrolls, s.Me.Gold) + scrollGap(s.Me.IDScrolls, s.Me.Gold)
}

// tomeCount reads a tome's LIVE charge count — the only truth a scroll BUY is
// judged by (P-4.5).
func tomeCount(ctx *Ctx, tomeID int) int {
	for _, it := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		if int(it.ID) == tomeID {
			if q, ok := it.FindStat(stat.Quantity, 0); ok {
				return q.Value
			}
			return 0
		}
	}
	return -1
}

func NewRestock() *Restock {
	r := &Restock{e: errand{npcID: npc.Akara, act2NPC: npc.Drognan, act3NPC: npc.Ormus, trade: true,
		ring: []data.Position{{X: 6023, Y: 4933}, {X: 6070, Y: 4960}, {X: 6100, Y: 4990}, {X: 6050, Y: 5010}, {X: 6110, Y: 4930}}}}
	r.e.initLife(r.Name())
	return r
}

func (r *Restock) Name() string { return "restock" }

func plan(s *percept.Snapshot) (buyHP, buyMana int) {
	wantMana := 4
	if s.Me.BeltSlots <= 4 {
		wantMana = 1
	}
	if s.Me.MaxMana < 20 {
		wantMana = 0 // a 4-point pool needs no drink (the owner, 04:57: the barb)
	}
	wantHP := s.Me.BeltSlots - wantMana
	buyHP, buyMana = wantHP-s.Me.BeltHP, wantMana-s.Me.BeltMana
	if buyHP < 0 {
		buyHP = 0
	}
	if buyMana < 0 {
		buyMana = 0
	}
	// A FULL BELT BUYS NOTHING (measured 09:59: 6 HP + 2 mana on an 8-slot
	// belt, doctrine wanting 4 mana — the deficit re-bid an unwinnable errand
	// three times in a minute). Buys are capped by free slots; the mix
	// corrects itself as she drinks. Free slots come from OCCUPANCY (BeltUsed),
	// not the ID-filtered counts: a belt full of bottles the filter doesn't
	// recognize read as empty, and the phantom deficit bought ~2,300 gold of
	// potions into the bag in two grants (the owner, 2026-07-20: "spammed
	// health potions from akara").
	free := s.Me.BeltSlots - s.Me.BeltUsed
	if free < 0 {
		free = 0
	}
	if buyMana > free {
		buyMana = free
	}
	free -= buyMana
	if buyHP > free {
		buyHP = free
	}
	return
}

func (r *Restock) Demand(s *percept.Snapshot) *arbiter.Demand { return r.e.keepBid(r.demand(s), s) }

func (r *Restock) demand(s *percept.Snapshot) *arbiter.Demand {
	if servicesCooled() {
		return nil
	}
	if !s.Valid || !s.Me.InTown || s.Me.Gold < 100 || time.Now().Before(r.nextAt) {
		return nil
	}
	buyHP, buyMana := plan(s)
	deficit := buyHP + buyMana
	if time.Now().Before(potionCoolUntil) {
		deficit = 0 // P-4.2a: the abandoned trip stays abandoned for its cool
	}
	// A near-empty tome is worth the trip on its own (P-4.5): with ≤2 scrolls
	// the next retreat may have no destination.
	if deficit < 2 && !(s.Me.TPScrolls >= 0 && s.Me.TPScrolls <= 2 && scrollDeficit(s) > 0) {
		return nil // one missing potion isn't worth a trip across town
	}
	return &arbiter.Demand{Who: r.Name(), Class: arbiter.ClassService,
		Urgency: 0.3 + float64(deficit)/float64(s.Me.BeltSlots+1),
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (r *Restock) Needs(*percept.Snapshot) Needs { return r.e.needs() }

func (r *Restock) Begin(ctx *Ctx, resumed bool) {
	r.e.begin(resumed)
	// A purchase in flight is never resumed blind: the next decision reads
	// the live counts again.
	r.buy, r.scr, r.sbuy = buyTx{}, scrollTx{}, buyTx{}
	if resumed {
		r.e.resume(ctx)
		return
	}
	r.e.resetTrip()
	r.resetCounters()
	r.e.adoptHandoff(ctx)
}

func (r *Restock) Suspend(ctx *Ctx, _ phase.Reason) { r.e.suspend(ctx) }

func (r *Restock) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	r.e.handOff(v, r.Name())
	r.e.end(ctx, v, why)
	r.e.resetTrip()
	r.resetCounters()
	r.buy, r.scr, r.sbuy = buyTx{}, scrollTx{}, buyTx{}
	coolOnEnd(v, why, &r.nextAt)
}

func (r *Restock) Step(ctx *Ctx) Status {
	e := &r.e
	if st, stop := e.pre(ctx); stop {
		return st
	}
	s := ctx.Snap
	// A purchase in flight finishes first: its judgment reads the live counts.
	if r.buy.active() {
		v, done, w := r.buy.step(ctx, r.Name(), "", nil)
		if !done {
			return e.wait(w)
		}
		return r.afterPotion(ctx, v)
	}
	if r.sbuy.active() {
		if done, w := r.buyScrollStock(ctx, 0); !done {
			return e.wait(w)
		}
		return r.afterScroll(ctx)
	}
	buyHP, buyMana := plan(s)
	if buyHP+buyMana+scrollDeficit(s) == 0 {
		return e.finish(ctx, phase.Done, phase.Completed, "belt and tomes at doctrine")
	}
	st, open, dead := e.drive(ctx, r.Name())
	if dead {
		r.nextAt = time.Now().Add(120 * time.Second)
		return st
	}
	if !open {
		return st
	}
	// Shop open: ONE purchase at a time (instant-buy cells). Potions first, then
	// scrolls (P-4.5): blood before the escape hatch.
	if buyMana > 0 || buyHP > 0 {
		// THE BELT THAT EATS NOTHING (P-4.9; the owner, 2026-07-20: "spammed
		// health potions from akara"): a buy is only a RESTOCK if the belt
		// rises — bottles that keep landing in the bag satisfy the owned-count
		// delta and never the deficit, so the trip would buy forever. Three
		// consecutive buys with a frozen belt end the trip and cool potions
		// hard; the beltFrozen field was declared for exactly this and never
		// wired until the spam.
		if r.potBought == 0 {
			r.lastBeltHP, r.lastBeltMN, r.beltFrozen = s.Me.BeltHP, s.Me.BeltMana, 0
		} else if s.Me.BeltHP != r.lastBeltHP || s.Me.BeltMana != r.lastBeltMN {
			r.beltFrozen = 0
			r.lastBeltHP, r.lastBeltMN = s.Me.BeltHP, s.Me.BeltMana
		} else {
			r.beltFrozen++
		}
		if r.beltFrozen >= 3 {
			potionCoolUntil = time.Now().Add(60 * time.Minute) // R27: frozen belt -> bottles land in the bag and the fence sells them back
			r.nextAt = time.Now().Add(60 * time.Minute)
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: r.Name(), Result: verbs.ResDeaf,
				Evidence: "belt frozen through 3 buys — bottles land in the bag; trip ended, potions cooled 5m"})
			return e.finish(ctx, phase.Abandoned, phase.Refused, "belt frozen through 3 buys")
		}
		// VERIFIED BUYS (2026-09-23) supersede the probing: the stock is READ
		// from memory, the cell computed from measured geometry, the result
		// judged by gold AND owned count. Probe-by-purchase paid for its
		// mistakes (9k gold of Act 2 junk on Act 1 cell guesses).
		r.kind = "mana"
		if buyMana == 0 || r.noMana {
			r.kind = "health"
		}
		kind := r.kind
		want := func(it data.Item) bool { return percept.PotionKind(it, true) == kind }
		if kind == "health" && len(vendorStock(ctx, func(it data.Item) bool { return int(it.ID) == 603 })) > 0 {
			want = func(it data.Item) bool { return int(it.ID) == 603 } // the proven belt bottle
		}
		v, done, w := r.buy.step(ctx, r.Name(), kind+" potion", want)
		if !done {
			return e.wait(w)
		}
		return r.afterPotion(ctx, v)
	}
	// Scrolls: the TP tome first (the escape hatch), then the ID tome.
	tome, key := 534, "shop.akara.cell.idscroll"
	if scrollGap(s.Me.TPScrolls, s.Me.Gold) > 0 {
		tome, key = 533, "shop.akara.cell.tpscroll"
	}
	if done, w := r.buyScrollStock(ctx, tome); !done {
		_ = key
		return e.wait(w)
	}
	return r.afterScroll(ctx)
}

// afterPotion applies one verified purchase's verdict (the proven law, one
// purchase per decision, 400ms between purchases).
func (r *Restock) afterPotion(ctx *Ctx, v buyVerdict) Status {
	e := &r.e
	switch v {
	case buyOK:
		r.potFails = 0
	case buyNotStock:
		if r.kind == "mana" {
			r.noMana = true // this vendor sells no mana: fill the belt with health
			return e.running()
		}
		r.potFails = 99
	case buyWrong:
		potionCoolUntil = time.Now().Add(30 * time.Minute)
		r.potFails = 99
	default:
		// The click did nothing: the shop is NOT open, whatever lingering
		// stock says. Close what is up by sight and talk again, selecting
		// Trade for real.
		r.potFails++
		e.tradeSelected = false
		e.to(erClean, "buy click did nothing")
		return e.running()
	}
	if r.potFails >= 3 {
		if time.Now().After(potionCoolUntil) {
			potionCoolUntil = time.Now().Add(5 * time.Minute)
		}
		r.nextAt = time.Now().Add(4 * time.Minute)
		why := phase.Deaf
		if v == buyNotStock {
			why = phase.Refused
		}
		return e.finish(ctx, phase.Abandoned, why, "potion buys failing (verdict "+fmt.Sprint(v)+")")
	}
	r.potBought++
	if r.potBought > ctx.Snap.Me.BeltSlots+2+8 { // runaway guard, probe headroom included
		// The runaway abandon must COOL or it re-grants in seconds and buys
		// another armful — the 10:56 spam re-granted 25s after abandoning.
		potionCoolUntil = time.Now().Add(4 * time.Minute)
		r.nextAt = time.Now().Add(4 * time.Minute)
		return e.finish(ctx, phase.Abandoned, phase.Refused, fmt.Sprintf("runaway guard: %d potion buys", r.potBought))
	}
	return e.wait(400 * time.Millisecond)
}

func (r *Restock) afterScroll(ctx *Ctx) Status {
	r.scrBought++
	if r.scrBought > 2*tpScrollFloor+12 { // probes + refills, generously bounded
		return r.e.finish(ctx, phase.Abandoned, phase.Refused, fmt.Sprintf("runaway guard: %d scroll buys", r.scrBought))
	}
	return r.e.wait(400 * time.Millisecond)
}

func (r *Restock) resetCounters() {
	r.potBought, r.beltFrozen, r.scrBought, r.potFails, r.noMana = 0, 0, 0, 0, false
	r.lastBeltHP, r.lastBeltMN = 0, 0
	r.frozenTP, r.frozenID = 0, 0
}

// scrollTx is one scroll purchase in flight: the misc-tab click, a 450ms
// settle, then the TOME's quantity delta judges it.
type scrollTx struct {
	stage   uint8 // 0 idle, 1 clicked (judging)
	tomeID  int
	memKey  string
	cell    [2]int
	learned bool
	before  int
}

func (t *scrollTx) active() bool { return t.stage != 0 }

// buyScroll — P-4.5: one scroll purchase, judged by the TOME's quantity delta
// (gold cannot tell a scroll from junk). The cell is LEARNED once per tome and
// kept forever; until learned, probe the misc-tab candidates — a wrong probe's
// junk goes to the fence like any other merchandise. done=false: come back
// after wait (the click landed; the judgment follows). tomeID/memKey are read
// only when a new purchase starts.
func (r *Restock) buyScroll(ctx *Ctx, tomeID int, memKey string) (done bool, wait time.Duration) {
	t := &r.scr
	if t.stage == 0 {
		probeIdx, _ := r.scrollCursors(tomeID)
		candidates := [][2]int{{612, 431}, {612, 384}, {612, 337}, {664, 478}, {664, 431}, {664, 384}}
		var cell [2]int
		learned := ctx.Mem != nil && ctx.Mem.GetJSON(memKey, &cell)
		if !learned {
			if *probeIdx >= len(candidates) {
				scrollWorks.Store(false)
				ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: r.Name(), Result: verbs.ResDeaf,
					Evidence: fmt.Sprintf("no misc-tab candidate raised tome %d — scroll belief retired", tomeID)})
				return true, 0
			}
			cell = candidates[*probeIdx]
		}
		*t = scrollTx{stage: 1, tomeID: tomeID, memKey: memKey, cell: cell, learned: learned, before: tomeCount(ctx, tomeID)}
		ctx.M.UIClick(cell[0], cell[1])
		return false, 450 * time.Millisecond
	}
	tx := *t
	*t = scrollTx{}
	probeIdx, frozen := r.scrollCursors(tx.tomeID)
	after := tomeCount(ctx, tx.tomeID)
	switch {
	case after > tx.before:
		*frozen = 0
		if !tx.learned && ctx.Mem != nil {
			ctx.Mem.PutJSON(tx.memKey, memory.ScopeForever,
				memory.Provenance{Source: "measured", Evidence: fmt.Sprintf("probe: tome %d %d→%d at (%d,%d)", tx.tomeID, tx.before, after, tx.cell[0], tx.cell[1])}, tx.cell)
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: r.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("%s LEARNED at (%d,%d)", tx.memKey, tx.cell[0], tx.cell[1])})
		}
	case !tx.learned:
		*probeIdx++ // wrong cell: whatever it bought is the fence's problem
	default:
		// A learned cell with a frozen delta twice is a GHOST WINDOW (WARNING 2).
		if *frozen++; *frozen >= 2 {
			scrollWorks.Store(false)
			ctx.Led.Append(verbs.Outcome{Verb: "buy", Holder: r.Name(), Result: verbs.ResDeaf,
				Evidence: fmt.Sprintf("tome %d frozen at %d twice on the learned cell — scroll belief retired", tx.tomeID, tx.before)})
		}
	}
	return true, 0
}

// scrollCursors: the probe and frozen counters of one tome.
func (r *Restock) scrollCursors(tomeID int) (probe, frozen *int) {
	if tomeID == 533 {
		return &r.probeTP, &r.frozenTP
	}
	return &r.probeID, &r.frozenID
}

// ---------------------------------------------------------------- Fence (ClassService)

// Fence sells inventory junk to Akara — the classic bots' economy law: loot → sell →
// gold → repair/potions. A bot with an empty purse cannot take care of itself (the
// owner, 03:08: "she doesn't repair either but she's out of gold i guess?"). One
// ctrl-click quick-sell at a time, exactly-this-item-gone as the postcondition.
//
// THE TOME OATH (the owner: "she simply dropped her tp book again. i'd like her to
// stop that"): a tome must never leave the bag at the Fence. Three layers —
// percept never marks tomes junk; every cell is re-identified against LIVE memory
// the instant before the ctrl-click; and the tome count is audited across the
// session — a missing tome aborts the errand with a loud ledger entry.
type Fence struct {
	e      errand
	sold   int
	tomes0 int // tome census when the shop opened; -1 = not yet taken
	// Ghost-window defense (the owner: "trying to sell outside akara's trade
	// window"): the vendor-stock read can LINGER after the panel closes, and a
	// ctrl-click into a ghost window with any panel up DROPS the item — the tome
	// and cube likely died of this. A sell that doesn't shrink the junk count is
	// evidence; two in a row is a verdict.
	lastJunk int
	ghost    int
	coolAt   time.Time
	sell     sellTx // the quick-sell in flight
	pickups  int    // sells this trip whose Ctrl+click LIFTED the item instead (putBack)
}

var (
	_ Life   = (*Fence)(nil)
	_ Phased = (*Fence)(nil)
)

func NewFence() *Fence {
	fc := &Fence{tomes0: -1, e: errand{npcID: npc.Akara, act2NPC: npc.Drognan, act3NPC: npc.Ormus, trade: true,
		ring: []data.Position{{X: 6023, Y: 4933}, {X: 6070, Y: 4960}, {X: 6100, Y: 4990}, {X: 6050, Y: 5010}, {X: 6110, Y: 4930}}}}
	fc.e.initLife(fc.Name())
	return fc
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

func (fc *Fence) Demand(s *percept.Snapshot) *arbiter.Demand { return fc.e.keepBid(fc.demand(s), s) }

func (fc *Fence) demand(s *percept.Snapshot) *arbiter.Demand {
	if servicesCooled() {
		return nil
	}
	if !s.Valid || !s.Me.InTown || s.Me.JunkCount == 0 || time.Now().Before(fc.coolAt) {
		return nil
	}
	// OWNER (R27): every town visit clears the bag — no "solvent and light" wait.
	return &arbiter.Demand{Who: fc.Name(), Class: arbiter.ClassService,
		Urgency: 0.45 + float64(minInt(s.Me.JunkCount, 8))/20, // poverty + full bags push it up
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (fc *Fence) Needs(*percept.Snapshot) Needs { return fc.e.needs() }

func (fc *Fence) Begin(ctx *Ctx, resumed bool) {
	fc.e.begin(resumed)
	fc.sell = sellTx{}
	if resumed {
		fc.e.resume(ctx)
		return
	}
	fc.e.resetTrip()
	fc.sold, fc.tomes0, fc.ghost, fc.lastJunk, fc.pickups = 0, -1, 0, 0, 0
	fc.e.adoptHandoff(ctx)
}

func (fc *Fence) Suspend(ctx *Ctx, _ phase.Reason) { fc.e.suspend(ctx) }

func (fc *Fence) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	fc.e.handOff(v, fc.Name())
	fc.e.end(ctx, v, why)
	fc.e.resetTrip()
	fc.sold, fc.tomes0, fc.ghost, fc.lastJunk, fc.pickups = 0, -1, 0, 0, 0
	fc.sell = sellTx{}
	coolOnEnd(v, why, &fc.coolAt)
}

// invCell converts an inventory GRID slot to the proven panel pixel formula.
func invCell(gx, gy int) (int, int) { return 1292 + gx*45 + 22, 395 + gy*45 + 22 }

// invCellPx: an inventory grid cell's center in PHYSICAL client px (the space
// RealMenuClick takes), measured 2026-09-23 on a 1920x1050 capture with a trade
// window open: 10x8 grid (this mod), column pitch 47.6, row pitch 47.75, cell
// (0,0) at (1311,420); verified against the two blue potions memory placed at
// (8,0),(9,0). The panel is RIGHT-anchored and scales with client height.
func invCellPx(ctx *Ctx, gx, gy int) (int, int) {
	k := shopScale(ctx)
	w := float64(ctx.GR.GameAreaSizeX) * ctx.M.PanelScale()
	x := w - (1920-1310.8-47.6*float64(gx))*k
	y := (420 + 47.75*float64(gy)) * k
	return int(x), int(y)
}

// sellVerdict is one verified quick-sell's result.
type sellVerdict uint8

const (
	sellOK    sellVerdict = iota // exactly the target left the bag
	sellWrong                    // something else left: STOP fencing
	sellDeaf                     // nothing left the bag
)

// judgeSell: the fence's postcondition — EXACTLY this item gone.
func judgeSell(target data.UnitID, gone []data.UnitID) sellVerdict {
	switch {
	case len(gone) == 1 && gone[0] == target:
		return sellOK
	case len(gone) > 0:
		return sellWrong
	}
	return sellDeaf
}

// sellTx is one quick-sell in flight: the real Ctrl+click, then the bag
// polled for up to 1s — no sleeps.
type sellTx struct {
	active bool
	target data.UnitID
	gx, gy int
	cx, cy int
	gold0  int
	before map[data.UnitID]bool
	at     time.Time
	back   int  // put-back clicks: the Ctrl+click lifted the item (putBack)
	drop   bool // the lifted item was set down on the vendor's window (the sell-by-drop)
}

// gone: units of before no longer in the bag.
func (t *sellTx) gone(ctx *Ctx) []data.UnitID {
	now := map[data.UnitID]bool{}
	for _, inv := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		now[inv.UnitID] = true
	}
	var out []data.UnitID
	for u := range t.before {
		if !now[u] {
			out = append(out, u)
		}
	}
	return out
}

func (fc *Fence) Step(ctx *Ctx) Status {
	e := &fc.e
	if fc.sell.active && ctx.Snap.Valid && (ctx.Snap.Me.CursorItem || fc.sell.back > 0) {
		// Before pre()'s cursor wait: the item on the cursor is OURS.
		if st, over := e.overrun(ctx); over {
			return st
		}
		return fc.putBack(ctx)
	}
	if st, stop := e.pre(ctx); stop {
		return st
	}
	s := ctx.Snap
	if fc.sell.active {
		return fc.judge(ctx)
	}
	if len(s.Junk) == 0 {
		return e.finish(ctx, phase.Done, phase.Completed, fmt.Sprintf("bag clean: %d sold", fc.sold)) // the bag is honest merchandise no more
	}
	st, open, dead := e.drive(ctx, fc.Name())
	if dead {
		// P-4.2a: an abandoned trip stays abandoned (05:17: Fence re-bid 0.65
		// the instant it gave up on a wall-blocked Akara — the idle breaker
		// never sees idle, so the SERVICE must cool itself). Retry next town.
		fc.coolAt = time.Now().Add(120 * time.Second)
		return st
	}
	if !open {
		return st
	}
	// WARNING 2 + Lexicon/SELL: the delta check is the only proof — a sell that
	// doesn't shrink the junk count is firing into a GHOST WINDOW, and ctrl-clicks
	// into one DROP items. Two frozen deltas = abort. Never a third.
	if fc.sold > 0 {
		if len(s.Junk) >= fc.lastJunk {
			fc.ghost++
		} else {
			fc.ghost = 0
		}
		if fc.ghost >= 2 {
			NoteGhost() // P-4.8a: the classic ghost window counts toward the poison
			ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResDeaf,
				Evidence: "GHOST TRADE WINDOW: sells not landing — aborting before a ctrl-click drops an item"})
			fc.coolAt = time.Now().Add(45 * time.Second)
			return e.finish(ctx, phase.Abandoned, phase.Deaf, "ghost trade window: sells not landing")
		}
	}
	fc.lastJunk = len(s.Junk)
	// TOME OATH, audit layer: census on shop-open, verified before every sell. A tome
	// that vanished mid-errand means a click went somewhere it must never go — stop
	// selling IMMEDIATELY and say so; silence is never success.
	census := tomeCensus(ctx)
	if fc.tomes0 < 0 {
		fc.tomes0 = census
	} else if census < fc.tomes0 {
		ev := fmt.Sprintf("TOME LOST mid-errand (%d -> %d) — fencing aborted", fc.tomes0, census)
		ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResDeaf, Evidence: ev})
		return e.finish(ctx, phase.Abandoned, phase.Precondition, ev)
	}
	// Shop open: quick-sell ONE junk item; the bag is the postcondition.
	it := s.Junk[0]
	// TOME OATH, identity layer: the snapshot called this cell junk — re-identify it
	// against LIVE memory the instant before the ctrl-click. Percept and reality
	// disagreeing on a lifeline cell is a refusal, not a sale. Lifelines: tomes,
	// the Horadric Cube (549 — fenced once, the owner's "what the hell"), and any
	// quest-typed item; type-by-ID beats the scrambled name table.
	for _, inv := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		if inv.Position.X != it.GX || inv.Position.Y != it.GY {
			continue
		}
		id := int(inv.ID)
		// loot.Lifeline adds the mod's numbering: the cube is row 564 here (the
		// recorded sell list carried it), and quest rows sit 15 past vanilla.
		if id == 533 || id == 534 || id == 549 || inv.Desc().Type == item.TypeQuest || loot.Lifeline(id) {
			ev := fmt.Sprintf("cell (%d,%d) holds a LIFELINE (id %d, type %s) — sell refused", it.GX, it.GY, id, inv.Desc().Type)
			ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResRefused, Evidence: ev})
			return e.finish(ctx, phase.Abandoned, phase.Precondition, ev)
		}
	}
	// REAL SELL, VERIFIED (2026-09-23: the posted SellClick at the old 45-px Act 1
	// grid never landed — "it has an issue clearing the inventory"). The cell comes
	// from the MEASURED inventory grid (10x8 on this mod), the click is a real
	// Ctrl+click, and the result must be EXACTLY this item gone. Any other item
	// leaving the bag stops fencing on the spot.
	t := sellTx{gx: it.GX, gy: it.GY, before: map[data.UnitID]bool{}}
	for _, inv := range ctx.GR.GetData().Inventory.ByLocation(item.LocationInventory) {
		t.before[inv.UnitID] = true
		if inv.Position.X == it.GX && inv.Position.Y == it.GY {
			t.target = inv.UnitID
		}
	}
	if t.target == 0 {
		ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResRefused,
			Evidence: fmt.Sprintf("junk cell (%d,%d) no longer holds an item — resync", it.GX, it.GY)})
		return e.wait(300 * time.Millisecond)
	}
	t.gold0 = ctx.GR.GetData().PlayerUnit.TotalPlayerGold()
	t.cx, t.cy = invCellPx(ctx, it.GX, it.GY)
	ctx.M.RealMenuCtrlClick(t.cx, t.cy)
	t.active, t.at = true, time.Now()
	fc.sell = t
	return e.wait(100 * time.Millisecond)
}

// judge polls the sell in flight (≤1s) and applies its verdict.
func (fc *Fence) judge(ctx *Ctx) Status {
	e, t := &fc.e, &fc.sell
	if cur := ctx.GR.GetData().Inventory.ByLocation(item.LocationCursor); len(cur) > 0 && cur[0].UnitID == t.target {
		return fc.putBack(ctx) // it left the bag onto the CURSOR: lifted, not sold
	}
	gone := t.gone(ctx)
	if len(gone) == 0 && time.Since(t.at) < time.Second {
		return e.wait(100 * time.Millisecond)
	}
	t.active = false
	gold1 := ctx.GR.GetData().PlayerUnit.TotalPlayerGold()
	switch judgeSell(t.target, gone) {
	case sellOK:
		ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("sold cell (%d,%d) px(%d,%d) gold %d→%d", t.gx, t.gy, t.cx, t.cy, t.gold0, gold1)})
	case sellWrong:
		ev := fmt.Sprintf("WRONG ITEM LEFT THE BAG (wanted unit %d at (%d,%d), gone=%v) — fencing halted", t.target, t.gx, t.gy, gone)
		ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResDeaf, Evidence: ev})
		fc.coolAt = time.Now().Add(30 * time.Minute)
		return e.finish(ctx, phase.Abandoned, phase.Deaf, ev)
	default:
		ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResWhiff,
			Evidence: fmt.Sprintf("sell click did nothing: cell (%d,%d) px(%d,%d) gold %d→%d", t.gx, t.gy, t.cx, t.cy, t.gold0, gold1)})
	}
	fc.sold++
	if fc.sold > 40 { // runaway guard
		return e.finish(ctx, phase.Abandoned, phase.Refused, "runaway guard: 40 sells")
	}
	return e.wait(400 * time.Millisecond)
}

// putBack: the quick-sell's Ctrl+click LIFTED the item instead of selling it
// (relay R10: every fence Act put the junk on the cursor — "cursor item" a
// tick after "shop open", not one "sold cell" line all run — and pre() waited
// on it until Equip outbid the errand and the gate shut the shop under it).
// The item is the fence's: it goes back where it came from (the one region
// certain to fit it, else any free one) through ParkCursor — the bag SEEN
// open, the measured grid, a real click — and the cursor-empty read judges it.
// Two lifts in one trip retire the gesture: fencing cools for 10 minutes.
func (fc *Fence) putBack(ctx *Ctx) Status {
	e, t := &fc.e, &fc.sell
	cur := ctx.GR.GetData().Inventory.ByLocation(item.LocationCursor)
	// SELL BY DROP (R20: the bag sat full of junk, "picked=0", fencing cooled on
	// every trip). A lifted item set down on the vendor's own window IS a sale in
	// D2R. Judged by the cursor emptying, the unit leaving the bag and the gold.
	if t.drop && len(cur) == 0 {
		t.active = false
		gold1 := ctx.GR.GetData().PlayerUnit.TotalPlayerGold()
		if !inBag(ctx, t.target) {
			fc.sold++
			ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResDone,
				Evidence: fmt.Sprintf("sold cell (%d,%d) by drop on the vendor window, gold %d→%d", t.gx, t.gy, t.gold0, gold1)})
			return e.wait(400 * time.Millisecond)
		}
		ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResWhiff,
			Evidence: fmt.Sprintf("drop on the vendor window did not sell cell (%d,%d): the item is back in the bag", t.gx, t.gy)})
		return e.wait(400 * time.Millisecond)
	}
	if !t.drop && t.back == 0 && len(cur) > 0 && cur[0].UnitID == t.target && shopOpen(ctx) {
		cx, cy := shopCellPx(ctx, data.Position{X: 4, Y: 4})
		ctx.M.RealMenuClick(cx, cy)
		t.drop, t.at = true, time.Now()
		return e.wait(450 * time.Millisecond)
	}
	if t.drop && len(cur) > 0 && time.Since(t.at) < 450*time.Millisecond {
		return e.wait(100 * time.Millisecond) // the drop is being judged
	}
	if len(cur) == 0 {
		if t.back == 0 {
			return e.wait(100 * time.Millisecond) // the snapshot ran ahead of the live read
		}
		t.active = false
		fc.pickups++
		ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResWhiff,
			Evidence: fmt.Sprintf("SELL BECAME A PICKUP: the Ctrl+click at px(%d,%d) lifted cell (%d,%d) — put back in %d click(s)", t.cx, t.cy, t.gx, t.gy, t.back)})
		if fc.pickups >= 2 {
			fc.coolAt = time.Now().Add(10 * time.Minute)
			return e.finish(ctx, phase.Abandoned, phase.Deaf, "the Ctrl+click lifts instead of selling (2 pickups, both put back) — fencing cooled 10m")
		}
		return e.wait(400 * time.Millisecond)
	}
	if t.back > 0 && time.Since(t.at) < 450*time.Millisecond {
		return e.wait(100 * time.Millisecond) // the place-click is being judged
	}
	switch {
	case cur[0].UnitID != t.target:
		t.active = false
		return e.finish(ctx, phase.Abandoned, phase.Deaf,
			fmt.Sprintf("a different item rides the cursor mid-sell (unit %d, sold %d) — the gate's parker takes it", cur[0].UnitID, t.target))
	case t.back >= 3:
		t.active = false
		return e.finish(ctx, phase.Abandoned, phase.Deaf, "the lifted item would not go back (3 place-clicks) — the gate's parker takes it")
	case !bagSeen(ctx) && time.Since(t.at) > 2*time.Second:
		t.active = false
		return e.finish(ctx, phase.Abandoned, phase.Deaf, "the lifted item: the bag not seen open for 2s — no blind place-click; the gate holds it")
	}
	act, ok := ParkCursor(ctx, &data.Position{X: t.gx, Y: t.gy})
	if !ok {
		return e.wait(150 * time.Millisecond)
	}
	ctx.Led.Append(verbs.Outcome{Verb: "fence", Holder: fc.Name(), Result: verbs.ResDone, Evidence: "put back: " + act})
	t.back++
	t.at = time.Now()
	return e.wait(450 * time.Millisecond)
}

// ---------------------------------------------------------------- Heal (ClassService)

// Heal is Akara's free refill: the Act 1 healer restores life and mana the moment
// the TALK happens — no menu item, no gold. The errand that was missing at 04:13:07:
// Breakout's portal delivered her to town at 37% with an empty purse, and because
// nothing pended she marched right back into the pack. Talking to Akara IS the
// restock when the purse is empty.
type Heal struct {
	e      errand
	talks  int // menu-opens that produced no HP change — the disproof counter
	coolAt time.Time
}

var (
	_ Life   = (*Heal)(nil)
	_ Phased = (*Heal)(nil)
)

func NewHeal() *Heal {
	h := &Heal{e: errand{npcID: npc.Akara, act2NPC: npc.Fara, act3NPC: npc.Ormus,
		ring: []data.Position{{X: 6023, Y: 4933}, {X: 6070, Y: 4960}, {X: 6100, Y: 4990}, {X: 6050, Y: 5010}, {X: 6110, Y: 4930}}}}
	h.e.initLife(h.Name())
	return h
}

func (h *Heal) Name() string { return "heal" }

func (h *Heal) Demand(s *percept.Snapshot) *arbiter.Demand { return h.e.keepBid(h.demand(s), s) }

func (h *Heal) demand(s *percept.Snapshot) *arbiter.Demand {
	if servicesCooled() {
		return nil
	}
	// P-4.1: in town she tops up below 75 — free is free, and idling at 57
	// kept Breakout's eject seat armed all morning (11:06). Only below 55
	// does the wound GATE the march (ServicesPending keeps that line).
	if !s.Valid || !s.Me.InTown || s.Me.HPPct > 75 || !healerHeals.Load() || time.Now().Before(h.coolAt) {
		return nil
	}
	return &arbiter.Demand{Who: h.Name(), Class: arbiter.ClassService,
		Urgency: 0.9 - float64(s.Me.HPPct)/200, // free and instant: outranks the shopping
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (h *Heal) Needs(*percept.Snapshot) Needs { return h.e.needs() }

func (h *Heal) Begin(ctx *Ctx, resumed bool) {
	h.e.begin(resumed)
	if resumed {
		h.e.resume(ctx)
		return
	}
	h.e.resetTrip()
	h.talks = 0
}

func (h *Heal) Suspend(ctx *Ctx, _ phase.Reason) { h.e.suspend(ctx) }

func (h *Heal) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	h.e.end(ctx, v, why)
	h.e.resetTrip()
	h.talks = 0
	coolOnEnd(v, why, &h.coolAt)
}

func (h *Heal) Step(ctx *Ctx) Status {
	e := &h.e
	if st, stop := e.pre(ctx); stop {
		return st
	}
	s := ctx.Snap
	if s.Me.HPPct > 90 {
		return e.finish(ctx, phase.Done, phase.Completed, fmt.Sprintf("refilled: hp %d", s.Me.HPPct)) // the pending clears and the march resumes
	}
	// The menu byte high after OUR talk click = the talk happened = the heal (if
	// this mod kept it) already landed. Read the verdict from a FRESH hp — the
	// snapshot predates the talk. A menu that was up before our click is a
	// leftover: Clean (and the errand's Talk) close it by sight first.
	if e.ph.Phase() >= erTalk && s.MenuOpen && talkedRecently(e.clickAt, time.Now()) {
		if hp := ctx.GR.GetData().PlayerUnit.HPPercent(); hp > 60 {
			return e.finish(ctx, phase.Done, phase.Completed, fmt.Sprintf("talk healed: hp %d", hp))
		}
		h.talks++
		if h.talks >= 2 {
			// Two talks, no refill: this mod's Akara does not heal. Stop believing —
			// package-wide — or the wounded-pending gate deadlocks her in town forever.
			healerHeals.Store(false)
			ctx.Led.Append(verbs.Outcome{Verb: "heal", Holder: h.Name(), Result: verbs.ResDeaf,
				Evidence: "two talks, no HP change — this Akara does not heal; belief retired"})
			return e.finish(ctx, phase.Abandoned, phase.Refused, "two talks, no HP change")
		}
		e.clickAt = time.Time{}
		e.to(erClean, "talk did not heal: close the menu and talk again")
		return e.running()
	}
	// Drive the errand only through the TALK: the intercept above fires before
	// the errand could ever advance into trade navigation.
	st, _, dead := e.drive(ctx, h.Name())
	if dead {
		h.coolAt = time.Now().Add(120 * time.Second)
	}
	return st
}

// snapPNG: one screenshot into logs/ — the debugging eye for rituals whose oracles
// are blind (the inventory panel byte lies; the pictures don't).
func snapPNG(ctx *Ctx, path string) {
	if f, err := os.Create(path); err == nil {
		_ = png.Encode(f, ctx.GR.Screenshot())
		f.Close()
	}
}

// ---------------------------------------------------------------- Repair (ClassService)

// Repair visits Charsi when any equipped item's durability sinks below a quarter.
// The repair-all click costs single-digit gold at these levels and the durability
// delta is the honest postcondition (reads are live on this repack). Transaction
// honesty (the owner: "kind of stuck on charsi after a repair"): a click that
// doesn't IMPROVE durability twice running means the job is as done as it gets —
// leave with what you got instead of pressing the button forever.
type Repair struct {
	e       errand
	lastDur int
	stale   int
	coolAt  time.Time
}

var (
	_ Life   = (*Repair)(nil)
	_ Phased = (*Repair)(nil)
)

func NewRepair() *Repair {
	rp := &Repair{lastDur: -1, e: errand{npcID: npc.Charsi, act2NPC: npc.Fara, act3NPC: npc.Hratli, trade: true,
		ring: []data.Position{{X: 6020, Y: 4952}, {X: 5992, Y: 4941}, {X: 5963, Y: 5001}, {X: 5962, Y: 4956}, {X: 5952, Y: 4944}}}}
	rp.e.initLife(rp.Name())
	return rp
}

func (rp *Repair) Name() string { return "repair" }

func (rp *Repair) Demand(s *percept.Snapshot) *arbiter.Demand { return rp.e.keepBid(rp.demand(s), s) }

func (rp *Repair) demand(s *percept.Snapshot) *arbiter.Demand {
	if servicesCooled() {
		return nil
	}
	// Owner: "repair obsessively" — every town visit with anything below 90%.
	if !s.Valid || !s.Me.InTown || s.Me.Gold < 10 || s.Me.MinDurPct >= inventory.TownRepairPct || time.Now().Before(rp.coolAt) {
		return nil
	}
	return &arbiter.Demand{Who: rp.Name(), Class: arbiter.ClassService,
		Urgency: 0.5 + (90-float64(s.Me.MinDurPct))/180, // broken gear outranks the shopping
		Commit:  arbiter.Commitment{MinHold: 5 * time.Second}}
}

func (rp *Repair) Needs(*percept.Snapshot) Needs { return rp.e.needs() }

func (rp *Repair) Begin(ctx *Ctx, resumed bool) {
	rp.e.begin(resumed)
	if resumed {
		rp.e.resume(ctx)
		return
	}
	rp.e.resetTrip()
	rp.lastDur, rp.stale = -1, 0
	rp.e.adoptHandoff(ctx)
}

func (rp *Repair) Suspend(ctx *Ctx, _ phase.Reason) { rp.e.suspend(ctx) }

func (rp *Repair) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	rp.e.handOff(v, rp.Name())
	rp.e.end(ctx, v, why)
	rp.e.resetTrip()
	rp.lastDur, rp.stale = -1, 0
	coolOnEnd(v, why, &rp.coolAt)
}

func (rp *Repair) Step(ctx *Ctx) Status {
	e := &rp.e
	if st, stop := e.pre(ctx); stop {
		return st
	}
	s := ctx.Snap
	if s.Me.MinDurPct > 90 { // repaired — the durability delta happened
		return e.finish(ctx, phase.Done, phase.Completed, fmt.Sprintf("durability %d%%", s.Me.MinDurPct))
	}
	st, open, dead := e.drive(ctx, rp.Name())
	if dead {
		// P-4.2a: the abandoned trip stays abandoned (04:36: 'never loaded on
		// the whole ring' re-bid three times a SECOND while he wall-hugged —
		// Charsi lives elsewhere on this seed; retry when the world changes).
		rp.coolAt = time.Now().Add(120 * time.Second)
		return st
	}
	if !open {
		return st
	}
	if rp.lastDur >= 0 {
		if s.Me.MinDurPct <= rp.lastDur {
			rp.stale++
		} else {
			rp.stale = 0
		}
		if rp.stale >= 2 {
			// The button no longer buys durability (unrepairable piece or a ghost
			// window): the errand is over — cool off so the demand can't re-loop.
			ev := fmt.Sprintf("durability frozen at %d%% after repairs — leaving with what we got", s.Me.MinDurPct)
			ctx.Led.Append(verbs.Outcome{Verb: "repair", Holder: rp.Name(), Result: verbs.ResTimeout, Evidence: ev})
			rp.coolAt = time.Now().Add(3 * time.Minute)
			return e.finish(ctx, phase.Abandoned, phase.Refused, ev)
		}
	}
	rp.lastDur = s.Me.MinDurPct
	ctx.M.RealMenuClick(repairBtnX, repairBtnY) // trade panels honor only real input (a posted UIClick never repaired)
	return e.wait(500 * time.Millisecond)
}

// npcNeighbour: where to look for a town NPC the map oracle does not place — a
// neighbour it does (owner, 2026-09-25: "hratli is near meshif").
var npcNeighbour = map[npc.ID]npc.ID{
	npc.Hratli: npc.Meshif2, // Kurast Docks: the smith stands by the boat
}

// townCircles: two rings (40 and 80 tiles, 8 points each) around a town spot —
// the seek of last resort for an NPC the map does not place.
func townCircles(g *game.Grid, c data.Position) []data.Position {
	var out []data.Position
	for _, r := range []int{25, 50, 75} {
		for _, d := range [][2]int{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}} {
			p := data.Position{X: c.X + d[0]*r, Y: c.Y + d[1]*r}
			if q, ok := nearestWalkable(g, p, 20); ok {
				out = append(out, q)
			} else if g == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

// nearestWalkable: the walkable cell nearest p within reach (R80: the Docks are
// a strip — circle points 80 tiles out sat in water, every one NoPath in 0.2s).
func nearestWalkable(g *game.Grid, p data.Position, reach int) (data.Position, bool) {
	if g == nil {
		return p, false
	}
	for r := 0; r <= reach; r++ {
		for dx := -r; dx <= r; dx++ {
			for dy := -r; dy <= r; dy++ {
				if max(absInt(dx), absInt(dy)) != r {
					continue
				}
				q := data.Position{X: p.X + dx, Y: p.Y + dy}
				if g.IsWalkable(q) {
					return q, true
				}
			}
		}
	}
	return p, false
}
