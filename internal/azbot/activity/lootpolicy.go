package activity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/loot"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

// ---------------------------------------------------------------- the loot brain

// The loot brain is the live half of package loot: it loads config/loot.yaml,
// plans every ground item in reach once per tick (take / swap / haul / skip),
// logs each decision once per change, keeps the census and the catalog, and
// holds the one pending room-making plan that Discard (swap) and Haul (town
// trip) act on. Loot itself only ever walks to and clicks a TAKE.

// LootConfigPath is where the owner's policy lives (relative to the working
// directory, like logs/).
var LootConfigPath = "config/loot.yaml"

// LootCatalogPath receives one JSON line per newly seen item row+quality.
var LootCatalogPath = "logs/loot_catalog.jsonl"

// roomPlan is the pending room-making plan: the item she makes room for, and
// how (drop Victim, or a town trip).
type roomPlan struct {
	Act       loot.Action
	Target    data.UnitID
	TargetPos data.Position
	Row       int
	Tier      loot.Tier
	Value     float64
	Area      area.ID
	Victim    loot.Carried // Swap only
	At        time.Time
	Town      bool // Haul: she reached town on this plan's trip
}

type lootMind struct {
	mu        sync.Mutex
	loaded    bool
	cfg       *loot.Config
	census    *loot.Census
	catalog   *loot.Catalog
	mem       *memory.Store
	log       func(msg string, args ...any)
	seq       uint64                    // the snapshot the plan cache belongs to
	plans     map[data.UnitID]loot.Plan // this tick's plans (Loot's pick reads them)
	said      map[data.UnitID]string    // the last logged decision per unit
	dropped   map[data.UnitID]bool      // units she discarded: never looted again
	pending   *roomPlan
	lastTier  map[data.UnitID]loot.Tier // for the census when a pickup lands
	stashSaid time.Time                 // the last "stash routine missing" line
	area      area.ID                   // the field area the per-unit maps belong to
}

var theLoot = &lootMind{}

// WireLoot hands the loot brain its memory store and log sink (the executive
// calls it once at startup; without it decisions are simply not logged).
func WireLoot(mem *memory.Store, log func(msg string, args ...any)) {
	theLoot.mu.Lock()
	defer theLoot.mu.Unlock()
	theLoot.mem, theLoot.log = mem, log
	theLoot.loadLocked()
}

// loadLocked reads the policy once (callers hold mu).
func (m *lootMind) loadLocked() {
	if m.loaded {
		return
	}
	m.loaded = true
	c, ok, err := loot.Load(LootConfigPath)
	switch {
	case err != nil:
		m.say("loot config", "result", "refused", "path", LootConfigPath, "err", err.Error(), "using", "built-in defaults")
	case !ok:
		m.say("loot config", "result", "missing", "path", LootConfigPath, "using", "built-in defaults")
	default:
		m.say("loot config", "result", "loaded", "path", c.Source)
	}
	loot.SetActive(c)
	m.cfg = c
	m.census = loot.NewCensus(c.CensusEvery, time.Now())
	m.catalog = loot.NewCatalog()
	m.plans, m.said = map[data.UnitID]loot.Plan{}, map[data.UnitID]string{}
	m.dropped, m.lastTier = map[data.UnitID]bool{}, map[data.UnitID]loot.Tier{}
}

func (m *lootMind) say(msg string, args ...any) {
	if m.log != nil {
		m.log(msg, args...)
	}
}

// progressUrgent: the owner's "trying to progress" — the march is lawful and
// bidding (Advance stamps the hint every Demand while it marches).
func progressUrgent() bool { return time.Now().Before(marchLawfulUntil) }

// situation is her side of every plan this tick.
func (m *lootMind) situation(s *percept.Snapshot) loot.Situation {
	sit := loot.Situation{
		Free:      s.Me.InvFree,
		BeltFree:  s.Me.BeltSlots - s.Me.BeltUsed,
		Urgent:    progressUrgent(),
		PotionsOn: LootPotions,
	}
	for _, j := range s.Junk {
		sit.SellCells += loot.Classify(j.ID).Cells()
	}
	for _, b := range s.Bag {
		sit.Bag = append(sit.Bag, loot.Carried{Unit: uint32(b.Unit), Item: loot.Item{ID: b.ID, Name: b.Name, Quality: b.Qual},
			GX: b.GX, GY: b.GY, Identified: b.Ident, Upgrade: b.Upgrade})
	}
	// A town trip needs a proven road (the TP binding with charges, or a door
	// already standing) and a calm field — the Withdraw ritual's own rules.
	road := liveDoor(s) || (townPortalBound && s.Me.TPScrolls > 0)
	calm := true
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= 12 {
			calm = false
			break
		}
	}
	sit.HaulOK = road && calm && !s.Me.InTown && time.Now().After(haulCoolUntil)
	return sit
}

func refItem(it percept.ItemRef) loot.Item {
	return loot.Item{ID: it.Class, Name: it.Name, Quality: it.Quality, Potion: it.Potion}
}

// planFor plans one ground item (cached per snapshot).
func (m *lootMind) planFor(s *percept.Snapshot, it percept.ItemRef) loot.Plan {
	// Snapshots are immutable per Seq; Seq 0 (a hand-built snapshot) is never cached.
	if m.seq != s.Seq || m.plans == nil || s.Seq == 0 {
		m.seq, m.plans = s.Seq, map[data.UnitID]loot.Plan{}
	}
	if p, ok := m.plans[it.ID]; ok {
		return p
	}
	var p loot.Plan
	if m.dropped[it.ID] {
		p = loot.Plan{Verdict: m.cfg.Evaluate(refItem(it)), Why: "she dropped it to make room"}
	} else if weak, why := m.weakUnique(s, it); weak {
		// ONLY STRONG UNIQUES (owner, 2026-09-25): a duplicate of one he owns,
		// an outlevelled weapon/armor piece or a quiver with no bow stays on the
		// ground — R45's bag rode 14 unique quivers, sold and re-looted each trip.
		v := m.cfg.Evaluate(refItem(it))
		v.Tier, v.Why = loot.TierC, why
		p = loot.Plan{Act: loot.Skip, Verdict: v, Why: why}
	} else {
		p = m.cfg.Plan(refItem(it), m.situation(s))
	}
	m.plans[it.ID] = p
	return p
}

// observe is the once-per-tick pass (Loot's Demand calls it): plan every item
// in reach, log changed decisions, feed the census and the catalog, choose the
// pending room-making plan, and emit the census line when due.
func (m *lootMind) observe(s *percept.Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadLocked()
	if !s.Valid {
		return
	}
	now := time.Now()
	if s.Me.Area != m.area && !s.Me.InTown {
		// Unit IDs belong to their level: a new area starts the per-unit
		// memories (logged decisions, dropped units) afresh.
		m.area = s.Me.Area
		m.said, m.lastTier, m.dropped = map[data.UnitID]string{}, map[data.UnitID]loot.Tier{}, map[data.UnitID]bool{}
	}
	var best *roomPlan
	for _, it := range s.Items {
		m.noteRow(refItem(it), s.Me.Area, "ground", now)
		if chebyshev(s.Me.Pos, it.Pos) > 30 {
			continue
		}
		p := m.planFor(s, it)
		m.census.Seen(uint32(it.ID), p.Verdict.Tier)
		m.lastTier[it.ID] = p.Verdict.Tier
		if p.Act == loot.Skip {
			m.census.Skipped(uint32(it.ID), p.Verdict.Tier, loot.ReasonClass(p))
		}
		m.decided(it, p)
		if (p.Act == loot.Swap || p.Act == loot.Haul) && (best == nil || p.Verdict.Value > best.Value) {
			rp := &roomPlan{Act: p.Act, Target: it.ID, TargetPos: it.Pos, Row: it.Class, Tier: p.Verdict.Tier,
				Value: p.Verdict.Value, Area: s.Me.Area, At: now}
			if p.Drop != nil {
				rp.Victim = *p.Drop
			}
			best = rp
		}
	}
	for _, b := range s.Bag {
		m.noteRow(loot.Item{ID: b.ID, Name: b.Name, Quality: b.Qual}, s.Me.Area, "bag", now)
	}
	m.refreshPending(s, best, now)
	// THE STASH IS MISSING (screen detection only — no drilled open/deposit
	// ritual): past ~70% in town the keepers would go to the stash; until that
	// routine is proven, say so (once per 10 minutes) instead of inventing clicks.
	// TODO(stash): a contract-v2 Stash service (walk to the chest, open it by
	// sight, ctrl-click tier S keepers, verify each left the bag).
	if s.Me.InTown && s.Me.InvFree*100 < loot.BagCells*30 && now.Sub(m.stashSaid) > 10*time.Minute {
		m.stashSaid = now
		m.say("outcome", "verb", "lootstash", "result", "missing", "bag_free", s.Me.InvFree,
			"why", "bag past 70% and no proven stash routine — keepers stay in the bag, the Fence sells tier B/C (TODO)")
	}
	if line, ok := m.census.Line(now); ok {
		m.say("loot census", "census", line, "bag_free", s.Me.InvFree, "urgent", progressUrgent())
	}
}

// decided logs a unit's decision when it changes (verb=lootpick ...).
func (m *lootMind) decided(it percept.ItemRef, p loot.Plan) {
	key := p.Act.String() + "|" + p.Verdict.Tier.String() + "|" + p.Why
	if m.said[it.ID] == key {
		return
	}
	m.said[it.ID] = key
	m.say("outcome", "verb", "lootpick", "item", it.Class, "unit", int(it.ID), "name", it.Name,
		"q", loot.QualityName(it.Quality), "tier", p.Verdict.Tier.String(), "act", p.Act.String(),
		"why", p.Verdict.Why+"; "+p.Why)
}

// refreshPending keeps, replaces or drops the pending room-making plan.
func (m *lootMind) refreshPending(s *percept.Snapshot, best *roomPlan, now time.Time) {
	// The trip's legs are read off her feet, not off Haul's grant (the grant
	// can lapse on the very tick the portal lands her in town).
	if p := m.pending; p != nil && p.Act == loot.Haul {
		switch {
		case s.Me.InTown && !p.Town:
			p.Town = true
			m.say("outcome", "verb", "lootroom", "result", "in-town", "item", p.Row, "next", "fence, then the portal back")
			return
		case s.Me.InTown:
			return // mid-trip: town is where it is supposed to be
		case p.Town:
			// Back through the portal beside the item: the Fence made room and
			// Loot's own plan reads TAKE now.
			m.say("outcome", "verb", "lootroom", "result", "back", "item", p.Row,
				"at", fmt.Sprintf("(%d,%d)", p.TargetPos.X, p.TargetPos.Y))
			m.pending = nil
		}
	}
	if p := m.pending; p != nil {
		switch {
		case p.Act == loot.Haul && now.Sub(p.At) > 5*time.Minute,
			p.Act == loot.Swap && now.Sub(p.At) > 90*time.Second:
			m.say("outcome", "verb", "lootroom", "result", "expired", "act", p.Act.String(), "item", p.Row)
			m.pending = nil
		case s.Me.InTown:
			return // not on this plan's trip: hold it (the field decides again)
		case s.Me.Area != p.Area:
			m.say("outcome", "verb", "lootroom", "result", "left-area", "act", p.Act.String(), "item", p.Row)
			m.pending = nil
		case !onGround(s, p.Target):
			m.pending = nil // picked (or gone): nothing left to make room for
		default:
			if pl, ok := m.plans[p.Target]; ok && pl.Act == loot.Take {
				m.pending = nil // room exists now: Loot takes it
				return
			}
			if best != nil && best.Target != p.Target && best.Value > p.Value+0.05 {
				m.pending = best
				m.rememberPending()
			}
			return
		}
	}
	if best != nil && !s.Me.InTown {
		m.pending = best
		m.rememberPending()
	}
}

// rememberPending writes the plan to memory (area scope): what she is making
// room for and where it lies — the portal trip's way back to it.
func (m *lootMind) rememberPending() {
	p := m.pending
	m.say("outcome", "verb", "lootroom", "result", "planned", "act", p.Act.String(), "item", p.Row,
		"unit", int(p.Target), "tier", p.Tier.String(), "at", fmt.Sprintf("(%d,%d)", p.TargetPos.X, p.TargetPos.Y),
		"drop", p.Victim.Item.ID)
	if m.mem != nil {
		m.mem.PutJSON("loot.pending", memory.ScopeArea,
			memory.Provenance{Source: "derived", Evidence: "tier " + p.Tier.String() + " drop with a full bag"}, p)
	}
}

func onGround(s *percept.Snapshot, u data.UnitID) bool {
	for _, it := range s.Items {
		if it.ID == u {
			return true
		}
	}
	return false
}

// noteRow catalogs a first sighting (memory fact + logs/loot_catalog.jsonl).
func (m *lootMind) noteRow(it loot.Item, ar area.ID, where string, now time.Time) {
	key := loot.CatalogKey(it.ID, it.Quality)
	if m.catalog.Has(key) {
		return
	}
	if m.mem != nil {
		if _, ok := m.mem.Get("loot.catalog." + key); ok {
			m.catalog.Know(key) // cataloged by an earlier run
			return
		}
	}
	e, isNew := m.catalog.Note(it, m.cfg.Evaluate(it), int(ar), where, now)
	if !isNew {
		return
	}
	if m.mem != nil {
		m.mem.PutJSON("loot.catalog."+key, memory.ScopeForever,
			memory.Provenance{Source: "measured", Evidence: "first seen " + where}, e)
	}
	if b, err := json.Marshal(e); err == nil && m.log != nil { // the file only for a wired (live) brain
		if err := os.MkdirAll(filepath.Dir(LootCatalogPath), 0o755); err == nil {
			if f, err := os.OpenFile(LootCatalogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				f.Write(append(b, '\n'))
				f.Close()
			}
		}
	}
	m.say("outcome", "verb", "lootcatalog", "item", e.ID, "name", e.Name, "realname", e.RealName, "q", e.Quality, "kind", e.Kind,
		"code", e.Code, "tier", e.Tier, "where", where, "hint", e.TagHint)
}

// want is Loot's score for one ground item: the plan's value for a TAKE; for
// a SWAP the approach only (walk there while far — Discard owns the moment
// she stands at it); nothing else.
func (m *lootMind) want(s *percept.Snapshot, it percept.ItemRef) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadLocked()
	p := m.planFor(s, it)
	switch p.Act {
	case loot.Take:
		return p.Verdict.Value
	case loot.Swap:
		if d := chebyshev(s.Me.Pos, it.Pos); d > 3 && discardWorks.Load() {
			return p.Verdict.Value - 0.01 // approach: a take of equal worth goes first
		}
	}
	return 0
}

// takeable: the pickup probe's oracle — a click lands only on a TAKE.
func (m *lootMind) takeable(s *percept.Snapshot, it percept.ItemRef) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadLocked()
	return m.planFor(s, it).Act == loot.Take
}

// picked records a landed pickup (census + log).
func (m *lootMind) picked(it percept.ItemRef, evidence string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadLocked()
	lootLanded.Store(time.Now().UnixNano())
	t, ok := m.lastTier[it.ID]
	if !ok {
		t = m.cfg.Evaluate(refItem(it)).Tier
	}
	m.census.Picked(t)
	m.say("outcome", "verb", "lootpick", "item", it.Class, "unit", int(it.ID), "name", it.Name,
		"q", loot.QualityName(it.Quality), "tier", t.String(), "act", "picked", "why", evidence)
	if m.pending != nil && m.pending.Target == it.ID {
		m.pending = nil
	}
}

// ---- the room-making plan, as Discard and Haul see it

// swapReady: a pending SWAP whose target lies within reach and whose victim
// still sits in the bag at its cell.
func (m *lootMind) swapReady(s *percept.Snapshot) (roomPlan, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pending
	if p == nil || p.Act != loot.Swap || s.Me.InTown || s.Me.Area != p.Area {
		return roomPlan{}, false
	}
	near := false
	for _, it := range s.Items {
		if it.ID == p.Target && chebyshev(s.Me.Pos, it.Pos) <= 4 {
			near = true
		}
	}
	if !near {
		return roomPlan{}, false
	}
	for _, b := range s.Bag {
		if uint32(b.Unit) == p.Victim.Unit && b.GX == p.Victim.GX && b.GY == p.Victim.GY {
			return *p, true
		}
	}
	return roomPlan{}, false
}

// swapped: the victim is on the ground. It is never looted again; the target
// now fits, and Loot takes it.
func (m *lootMind) swapped(victim data.UnitID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped[victim] = true
	if m.pending != nil {
		m.say("outcome", "verb", "lootroom", "result", "swapped", "item", m.pending.Row, "dropped", m.pending.Victim.Item.ID)
	}
	m.pending = nil
}

// abandonRoom drops the pending plan (a failed swap or trip) with a reason.
func (m *lootMind) abandonRoom(why string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		// Only the room-making failed: the item itself stays wanted, and a
		// later plan (after a fence sale, say) may still take it.
		m.say("outcome", "verb", "lootroom", "result", "abandoned", "act", m.pending.Act.String(), "item", m.pending.Row, "why", why)
	}
	m.pending = nil
}

// haulReady: a pending HAUL (the field leg).
func (m *lootMind) haulReady(s *percept.Snapshot) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pending
	return p != nil && p.Act == loot.Haul && !p.Town && !s.Me.InTown && s.Me.Area == p.Area
}

// hauling: a town trip for loot room is under way (she is in town on it).
func (m *lootMind) hauling() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending != nil && m.pending.Act == loot.Haul && m.pending.Town
}

// lootLanded: when a pickup last LANDED (ground-gone), for the watchdog.
var lootLanded atomic.Int64

// lootProductiveFor: a landed pickup this recent keeps a pile walk from being
// judged an orbit (the watchdog's own window is 20s).
const lootProductiveFor = 12 * time.Second

// LootProductive: Loot landed a pickup within lootProductiveFor of now. Only a
// landing counts — a deaf click on the same unit is not progress.
func LootProductive(now time.Time) bool {
	at := lootLanded.Load()
	return at != 0 && now.Sub(time.Unix(0, at)) < lootProductiveFor
}

// weakUnique: the strong-unique rule for a ground unique — owned = the stash
// tabs memory shows, what he wears, and the bag.
func (m *lootMind) weakUnique(s *percept.Snapshot, it percept.ItemRef) (bool, string) {
	if it.Quality != loot.QUnique || it.Unique <= 0 {
		return false, ""
	}
	owned := func(row int) bool {
		if s.StoredUniques[row] {
			return true
		}
		for _, b := range s.Bag {
			if int(b.Unique) == row {
				return true
			}
		}
		return false
	}
	return m.cfg.WeakUnique(it.Unique, loot.Classify(it.Class), s.Me.Level, s.Me.HasBow, owned, nil)
}
