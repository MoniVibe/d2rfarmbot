package main

// The executive's gate and janitor (docs/AZBOT_V2.md step 6), behind -janitor /
// AZBOT_JANITOR=1. Each tick after arbitration the holder's Needs are judged
// against the stable screen Reading (exec.Janitor): a foreign panel or a
// foreign cursor item earns ONE janitor action, the holder's held clock pauses,
// and its Step is skipped. The policy is pure (internal/azbot/exec); this file
// only turns its answers into motor input and trace lines.

import (
	"fmt"
	"image/png"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/azbot/activity"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/inventory"
	"github.com/hectorgimenez/koolo/internal/azbot/loot"
	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/trace"
	"github.com/hectorgimenez/koolo/internal/game"
)

type gatekeeper struct {
	logger *slog.Logger
	m      *motor.Motor
	gr     *game.MemoryReader
	sh     *shadow
	j      *exec.Janitor
	ses    *exec.Session // layer 0: the panels it claims (the pause menu while Relogging)

	said      string    // last gate line's why (a line per change, not per tick)
	blockedAt time.Time // when the current block began; zero while open

	// THE CURSOR RULE (relay R10): never a drop in town, in the field only
	// known junk; a held item goes back to whoever parks it.
	junk    junkWatch // the known-junk set (bag items the percept judged merchandise)
	picker  string    // the last holder that owned cursor work while the item rode it
	handAt  time.Time // the last hand-back (bench) — at most one per handBackRest
	heldSay time.Time // the last loud "CURSOR ITEM HELD" line
	// invKey/bagAt: a held item with the bag shut gets the bag OPENED (R24: a
	// potion the fence left on the cursor waited forever for a parker that never
	// bid). The open bag then makes the Park rule place it in a free cell.
	invKey     uint16
	invPhase   inventory.Phase // the last logged inventory state
	invStuck   inventory.Stuck
	invCloseAt time.Time
	bagAt      time.Time
}

// handBackRest: how long a holder is benched to hand a held cursor item back.
const handBackRest = 3 * time.Second

func newGatekeeper(logger *slog.Logger, m *motor.Motor, gr *game.MemoryReader, sh *shadow, ses *exec.Session) *gatekeeper {
	return &gatekeeper{logger: logger, m: m, gr: gr, sh: sh, j: exec.NewJanitor(), ses: ses}
}

// Stable is the reading the gate judges (nil before the first capture).
func (g *gatekeeper) Stable() *screen.Reading {
	seen := g.sh.Eye.Latest()
	if seen == nil {
		return nil
	}
	r := exec.Stable(*seen)
	return &r
}

// needs is the holder's gate view: the claims of its CURRENT phase (a town
// service claims nothing in its Clean phase, so every leftover is foreign),
// plus what the session claims: the pause menu while it is Relogging (the
// relog walks the pause menu on purpose; nothing else may keep it up, and the
// moment the relog ends it is foreign and clicked away by Return to Game).
func (g *gatekeeper) needs(who string, s *percept.Snapshot, roster *activity.Roster) exec.HolderNeeds {
	n := exec.NoHolder
	if a := roster.Get(who); a != nil {
		n = a.Needs(s).Holder()
	}
	if g.ses != nil {
		n.Claims |= g.ses.Claims()
	}
	n.Town = s.Valid && s.Me.InTown
	// OWNER (2026-09-24, R23: "it was stuck because it picked a mana potion"):
	// in the field a foreign cursor item is dropped unless the loot policy keeps
	// it (runes, gems, charms, uniques...). Town still never drops (exec rule).
	n.CursorJunk = s.Valid && s.Me.CursorItem && (g.junk.cursorJunk(g.gr) || !cursorKeeper(g.gr))
	return n
}

// perform executes one janitor action; the returned text is the trace's act=.
func (g *gatekeeper) perform(d exec.Decision) string {
	switch {
	case d.Park:
		// The bag is SEEN open: a free cell of the measured grid (the same
		// place-click Equip parks with); a later Reading judges CursorItem.
		ctx := &activity.Ctx{M: g.m, GR: g.gr, Seen: g.sh.Eye.Latest(), Screen: g.Stable()}
		act, ok := activity.ParkCursor(ctx, nil)
		if !ok {
			g.logger.Warn("CURSOR ITEM: park not possible — held, never dropped", "why", act)
		}
		return act
	case d.Drop:
		// The legacy cursor drop's own spot: her feet, below the HUD's reach.
		x, y := g.gr.GameAreaSizeX/2, g.gr.GameAreaSizeY/2+140
		g.m.BareClick(x, y)
		return fmt.Sprintf("drop %d,%d", x, y)
	case d.Action.Kind == screen.ActClick:
		act := fmt.Sprintf("click %d,%d", d.Action.X, d.Action.Y)
		if !g.m.RealMenuClick(d.Action.X, d.Action.Y) {
			act += " (refused: no foreground)"
		}
		return act
	case d.Action.Kind == screen.ActKey:
		act := fmt.Sprintf("key 0x%02X", d.Action.VK)
		if !g.m.RealKey(uint16(d.Action.VK)) {
			act += " (refused: no foreground)"
		}
		return act
	}
	return ""
}

// step gates this tick. ok: the holder may Step. unblocked: on the tick the
// gate reopens, how long it was shut (0 otherwise). demands: this tick's bids
// (the cursor hand-back looks for a bidder that parks).
func (g *gatekeeper) step(tick uint64, s *percept.Snapshot, arb *arbiter.Arbiter, roster *activity.Roster, demands []arbiter.Demand) (ok bool, unblocked time.Duration) {
	who := holderWho(arb)
	now := time.Now()
	seen := g.sh.Eye.Latest()
	if s.Valid {
		g.junk.observe(g.gr, s)
	}
	if s.Valid {
		g.invObserve(now, s, who)
	}
	n := g.needs(who, s, roster)
	switch {
	case !s.Valid || !s.Me.CursorItem:
		g.picker = ""
	case who != "" && n.CursorOwn:
		g.picker = who // holding with the item riding the cursor, owning cursor work
	}
	// Survival holders (Stand/Flee/Breakout/Dodge) are never held or acted
	// for unless the pause menu is up (exec.Janitor rule 5).
	n.Survival = arb.Current() != nil && arb.Current().Demand.Class == arbiter.ClassSurvive
	d := g.j.Decide(now, seen, n)
	g.sh.SetGate(d.Gate())
	if d.Phantom != "" {
		// A detector convicted without an action this tick (a close click
		// that changed nothing): log it where the gate lines are.
		g.logger.Warn("screen: phantom detector quarantined", "hold", who, "why", d.Phantom)
		emit(trace.Gate(who, d.Phantom, "", tick))
	}
	if d.Open {
		arb.SetBlocked(false)
		if !g.blockedAt.IsZero() {
			unblocked = now.Sub(g.blockedAt)
			emit(trace.Gate(who, fmt.Sprintf("open after %.1fs: %s", unblocked.Seconds(), d.Why), "", tick))
			g.blockedAt, g.said = time.Time{}, ""
		}
		return true, unblocked
	}
	if g.blockedAt.IsZero() {
		g.blockedAt = now
	}
	arb.SetBlocked(true)
	if d.NewWedge {
		shot := fmt.Sprintf("logs/ui_wedge_%d.png", now.Unix())
		if f, err := os.Create(shot); err == nil {
			_ = png.Encode(f, g.gr.Screenshot())
			f.Close()
		}
		g.logger.Warn("UI WEDGE — janitor actions are not clearing the screen; not acting for 60s, gating on the pause menu only",
			"hold", who, "why", d.Why, "screen", shot)
		emit(trace.Gate(who, "ui_wedge: "+d.Why, "", tick))
		g.said = "ui_wedge"
	}
	if d.CursorHold || (d.Cursor && d.Wedged) {
		g.cursorHeld(tick, who, d, s, arb, roster, demands)
		if d.CursorHold && g.invKey != 0 && time.Since(g.bagAt) > 2*time.Second {
			g.bagAt = time.Now()
			act := fmt.Sprintf("open the bag (key 0x%02X) for the held item's park", g.invKey)
			if !g.m.RealKey(g.invKey) {
				act += " (refused: no foreground)"
			}
			g.sh.Kick()
			emit(trace.Gate(who, d.Why, act, tick))
		}
	}
	if d.Act {
		act := g.perform(d)
		g.j.Acted(time.Now())
		if strings.Contains(act, "refused") {
			g.j.Refused()
		}
		g.sh.Kick() // judge the action on the next tick's photograph
		if strings.HasPrefix(d.Why, "phantom") {
			g.logger.Warn("screen: phantom detector quarantined", "hold", who, "why", d.Why, "act", act)
		}
		emit(trace.Gate(who, d.Why, act, tick))
		g.said = d.Why
		return false, 0
	}
	why := d.Why
	if d.Wait != "" && d.Wait != "rate" && d.Wait != "awaiting a fresh reading" {
		why += " [" + d.Wait + "]"
	}
	if why != g.said && d.Wait != "rate" && d.Wait != "awaiting a fresh reading" {
		emit(trace.Gate(who, why, "", tick))
		g.said = why
	}
	return false, 0
}

// cursorHeld: the gate HOLDS the holder for a foreign cursor item it may not
// drop (town, or not known junk) with the bag shut. Said loudly (every 10s),
// and handed back: the current holder is benched briefly so a bidder that
// owns cursor work takes the grant — the picker (whoever last held it with
// the item riding the cursor) if it bids, else any such bidder (Equip parks
// in town). Survival and recovery holders are never benched here.
func (g *gatekeeper) cursorHeld(tick uint64, who string, d exec.Decision, s *percept.Snapshot, arb *arbiter.Arbiter, roster *activity.Roster, demands []arbiter.Demand) {
	now := time.Now()
	to := ""
	if cur := arb.Current(); cur != nil && cur.Demand.Class > arbiter.ClassRecover && now.Sub(g.handAt) >= handBackRest {
		to = handBackTo(who, g.picker, demands, func(name string) bool {
			a := roster.Get(name)
			return a != nil && a.Needs(s).Cursor != exec.CursorEmpty
		})
		if to != "" {
			g.handAt = now
			arb.Bench(who, now.Add(handBackRest), "cursor item: handed back to "+to)
			emit(trace.Gate(who, "cursor item held: handed back to "+to, "bench "+who, tick))
		}
	}
	if now.Sub(g.heldSay) >= 10*time.Second || to != "" {
		g.heldSay = now
		g.logger.Warn("CURSOR ITEM HELD — never dropped; world actions blocked until it is parked",
			"hold", who, "town", s.Valid && s.Me.InTown, "why", d.Why, "handback", orNone(to), "picker", orNone(g.picker))
	}
}

// cursorParker: the activity that parks a stray cursor item in town (Equip's
// WARNING 9 errand). Other services own cursor work too, but only wait on a
// held item.
const cursorParker = "equip"

// handBackTo picks who a held cursor item goes back to: the picker when it
// bids, else the parker when it bids. Never the holder itself.
func handBackTo(holder, picker string, demands []arbiter.Demand, ownsCursor func(string) bool) string {
	for _, want := range []string{picker, cursorParker} {
		for _, d := range demands {
			if want != "" && d.Who == want && d.Who != holder && ownsCursor(d.Who) {
				return d.Who
			}
		}
	}
	return ""
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// junkWatch is the KNOWN-JUNK set: bag items the percept judged merchandise
// (Snapshot.Junk), remembered by unit id so an item still counts as junk once
// it rides the cursor. Only such an item may be dropped, and only in the field
// (relay R10). Refreshed from memory at most once a second, while the cursor
// is empty (a held item keeps the verdict it had in the bag).
type junkWatch struct {
	ids map[data.UnitID]bool
	at  time.Time
}

func (w *junkWatch) observe(gr *game.MemoryReader, s *percept.Snapshot) {
	if s.Me.CursorItem || time.Since(w.at) < time.Second {
		return
	}
	w.at = time.Now()
	if len(s.Junk) == 0 {
		w.ids = nil
		return
	}
	w.ids = junkIDs(s.Junk, gr.GetData().Inventory.ByLocation(item.LocationInventory))
}

// cursorJunk: the item on the cursor was judged junk while it lay in the bag.
func (w *junkWatch) cursorJunk(gr *game.MemoryReader) bool {
	if len(w.ids) == 0 {
		return false
	}
	return knownJunk(w.ids, gr.GetData().Inventory.ByLocation(item.LocationCursor))
}

// junkIDs: the unit ids of the bag items standing in the percept's junk cells.
func junkIDs(junk []percept.InvItem, bag []data.Item) map[data.UnitID]bool {
	cells := make(map[data.Position]bool, len(junk))
	for _, j := range junk {
		cells[data.Position{X: j.GX, Y: j.GY}] = true
	}
	ids := map[data.UnitID]bool{}
	for _, it := range bag {
		if cells[it.Position] {
			ids[it.UnitID] = true
		}
	}
	return ids
}

// knownJunk: the cursor holds exactly one item and it is in the junk set.
func knownJunk(ids map[data.UnitID]bool, cursor []data.Item) bool {
	return len(cursor) == 1 && ids[cursor[0].UnitID]
}

// settle is the startup hygiene by the janitor: before calibration presses its
// probe keys, observe until the screen belief is World with nothing foreign,
// clearing what is seen one action at a time. Bounded; returns actions taken.
func (g *gatekeeper) settle(p *percept.Perceptor) int {
	n := 0
	deadline := time.Now().Add(6 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		s := p.Capture()
		g.sh.Kick()
		g.sh.observe(0, s, "startup")
		seen := g.sh.Eye.Latest()
		d := g.j.Decide(time.Now(), seen, exec.NoHolder)
		if d.Open && seen != nil && exec.Stable(*seen).Mode == screen.World && i >= 3 {
			break
		}
		if d.Act {
			act := g.perform(d)
			g.j.Acted(time.Now())
			if strings.Contains(act, "refused") {
				g.j.Refused()
			}
			emit(trace.Gate("startup", d.Why, act, 0))
			n++
		}
		time.Sleep(150 * time.Millisecond)
	}
	return n
}

// cursorKeeper: the cursor item is one the loot policy keeps (tier S/A or a
// lifeline). Unknown or unreadable counts as a keeper (never drop blind).
func cursorKeeper(gr *game.MemoryReader) bool {
	cur := gr.GetData().Inventory.ByLocation(item.LocationCursor)
	if len(cur) == 0 {
		return true
	}
	c := loot.Active()
	if c == nil {
		return true
	}
	return c.Keep(int(cur[0].ID), int(cur[0].Quality))
}

// invObserve feeds the ONE inventory tracker (owner: "let it know states like
// 'inventory open' or 'item held' or 'inventory swap loop'"), logs each state
// change, names a stuck state with its prescription, and applies the
// quarantine (the gate's own cursor rules still park or shed a held item).
func (g *gatekeeper) invObserve(now time.Time, s *percept.Snapshot, who string) {
	r := g.Stable()
	obs := inventory.Observation{At: now, Held: uint32(s.Me.CursorUnit)}
	if r != nil {
		obs.BagOpen = r.Sight&screen.Inventory != 0
		obs.StashOpen = r.Sight&screen.Stash != 0
		obs.VendorOpen = r.Sight&screen.Shop != 0
	}
	switch who {
	case "stash", "fence", "belt", "equip", "identify", "restock", "repair", "discard", "spend", "socket", "shed":
		obs.Planned = true // an inventory errand holds: its panel is its work
	}
	ph, st, rx, why := activity.InvTracker.Observe(obs)
	if ph != g.invPhase {
		g.logger.Info("inventory state", "state", ph.String(), "holder", who)
		g.invPhase = ph
	}
	// RxClosePanel, carried out (R34: the janitor's close click on a lone bag
	// "changed nothing" and its detector was quarantined as a phantom, so the
	// bag stood open): the inventory key toggles the bag shut, at most every 3s.
	if st == inventory.StuckPanelIdle && ph == inventory.BagOpen && g.invKey != 0 && now.Sub(g.invCloseAt) > 3*time.Second {
		g.invCloseAt = now
		if g.m.RealKey(g.invKey) {
			g.logger.Info("inventory: idle bag closed with the inventory key")
		}
	}
	if st != g.invStuck {
		if st != inventory.StuckNone {
			g.logger.Warn("INVENTORY STUCK", "state", ph.String(), "stuck", st.String(), "rx", rx.String(), "why", why, "holder", who)
			if rx == inventory.RxQuarantine {
				if u := activity.InvTracker.StuckUnit(); u != 0 {
					activity.InvTracker.Quarantine(u, now)
					g.logger.Warn("inventory: unit quarantined", "unit", u, "for", inventory.QuarantineFor.String())
				}
			}
		}
		g.invStuck = st
	}
}
