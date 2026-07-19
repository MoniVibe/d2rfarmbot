// Package percept is azbot's perception layer: LAW 4 — dead reads are unrepresentable.
//
// One Perceptor produces immutable Snapshots on a fixed cadence. Decision code consumes
// Snapshots; verification reads go through the Verifier (M2+). There is no Life field, no
// KeyBindings, no OpenMenus — the channels proven dead on this repack do not exist here.
package percept

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/game"
)

// PlayerState is the live self-state subset: only channels PROVEN live on 3.2.92777.
type PlayerState struct {
	Pos    data.Position
	Area   area.ID
	Mode   mode.PlayerMode
	HPPct  int
	MPPct  int
	Level  int
	Gold   int
	InTown bool
	// Armed: something occupies a weapon-hand slot. Part of the self-model's
	// "who am I right now" — a lost weapon flips this the same cycle.
	Armed bool
	// HealPots: healing/rejuv potions actually IN the belt. The Sentinel drinks
	// nothing when this is 0 — the bot knows when it cannot heal (the empty-belt
	// death: 25s bleed from 30% while the drink reflex pressed keys into the void).
	HealPots int
	// ManaPots: mana potions in the belt (mod id 607) — the bow skill's fuel gauge.
	ManaPots int
	// HPCols/ManaCols: WHICH bottom-row belt columns hold each potion type. The belt
	// keys drink by column — a drink reflex that presses columns blindly gulps mana
	// while bleeding and never finds the blue when the pool is dry.
	HPCols   []int
	ManaCols []int
	// Belt self-model totals (whole belt, not just the drinkable bottom row) — what
	// the Restock service bids on. BeltSlots = rows*4 from the equipped belt's name.
	BeltSlots int
	BeltHP    int
	BeltMana  int
	// MinDurPct: the worst equipped item's durability percent (100 when nothing
	// tracks durability) — what the Repair service bids on.
	MinDurPct int
	// WeaponKind: what the ACTIVE hands hold — "bow", "melee", or "none". The W swap
	// flips this next capture; combat reads it as the closed loop on weapon swapping.
	WeaponKind string
	// Arrows: the equipped quiver's ammo count. -1 = quiver present but quantity
	// unreadable (assume fine); 0 with a bow = the quiver ran dry and VANISHED (D2R
	// removes it) — every basic attack is a whiff and nobody used to notice.
	Arrows int
	// Corpse: her own body, holding the gear and gold a death took. The Reclaim
	// activity's whole world.
	CorpseFound bool
	CorpsePos   data.Position
	// JunkCount: sellable inventory items (dup tomes, plain gear) — the Fence
	// service's reason to exist. The classic bots' law: loot → sell → gold →
	// repair/potions; a bot with an empty purse cannot take care of itself.
	JunkCount int
}

// EnemyRef is a live hostile: identity, position, and Mode (the honest liveness read —
// the Life stat is frozen on this repack and does not exist here).
type EnemyRef struct {
	ID   data.UnitID
	Pos  data.Position
	Mode uint32
}

// ItemRef is a ground item: identity, position, name (mod-remapped names resolved by
// consumers against the self-model's identity facts, not here).
type ItemRef struct {
	ID      data.UnitID
	Pos     data.Position
	Name    string
	Quality int
}

// PortalRef is a live portal object: identity AND position. Entering a portal means
// CLICKING it (hover-confirmed by UnitID) — walking onto one does nothing, so a
// bare position is useless to consumers.
type PortalRef struct {
	ID  data.UnitID
	Pos data.Position
}

// MissileRef is a projectile in flight (unit table type 3). No ownership, no velocity —
// consumers infer both from the snapshot stream (two frames give the vector; her own
// arrows always fly AWAY, so only closing missiles are threats).
type MissileRef struct {
	ID  data.UnitID
	Txt int
	Pos data.Position
}

// InvItem is an inventory item with its GRID slot — the Fence service's sell targets.
type InvItem struct {
	ID   int // numeric item ID (the mod scrambles names; IDs cannot lie)
	GX   int
	GY   int
	Qual int
}

// Snapshot is one immutable perception frame. Valid=false frames (load screens,
// zero-position garbage) are published so consumers see the gap, but carry no state.
type Snapshot struct {
	Seq   uint64
	At    time.Time
	Valid bool
	Me    PlayerState
	// MenuOpen: the ONE panel oracle — UIBytes wide-window offset 0xF4 (measured 2026-07-18).
	MenuOpen bool
	Enemies  []EnemyRef
	Items    []ItemRef
	Portals  []PortalRef  // town portals (and red portals) in the world
	Missiles []MissileRef // projectiles in flight — the dodge reflex's raw feed
	Junk     []InvItem    // sellable inventory items, grid slots (the Fence's list)
}

// AttachReport is the M0 epistemics gate verdict: behavioral probes over the channels
// azbot requires. (Pattern-level misses still print from d2go; promoting them into this
// report requires the d2go failure-semantics change noted in the design — tracked debt.)
type AttachReport struct {
	PositionSane bool
	StatsSane    bool
	UIBytesSane  bool
	ModeSane     bool
}

func (r AttachReport) OK() bool {
	return r.PositionSane && r.StatsSane && r.UIBytesSane && r.ModeSane
}

func (r AttachReport) String() string {
	return fmt.Sprintf("position=%v stats=%v uibytes=%v mode=%v",
		r.PositionSane, r.StatsSane, r.UIBytesSane, r.ModeSane)
}

// Perceptor owns the reader and publishes snapshots. It is the only GetData caller in azbot.
type Perceptor struct {
	gr   *game.MemoryReader
	seq  atomic.Uint64
	last atomic.Pointer[Snapshot]
	// Corpse latch: the live corpse unit exists only while its rooms are streamed in,
	// so a town respawn "loses" the body (measured 2026-07-19: Reclaim never bid, naked
	// Fight punched the swarm). The Perceptor itself witnesses the death and remembers
	// where the body fell until the gear comes back.
	corpseLatch    data.Position
	corpseLatched  bool
	lastAliveArmed bool
}

func New(gr *game.MemoryReader) *Perceptor { return &Perceptor{gr: gr} }

// Gate runs the M0 behavioral attach audit: refuse to run on garbage.
// Requires the character to be IN GAME (position sane) — call after game load settles.
func (p *Perceptor) Gate() AttachReport {
	var r AttachReport
	d := p.gr.GetData()
	pos := d.PlayerUnit.Position
	r.PositionSane = pos.X > 1000 && pos.X < 60000 && pos.Y > 1000 && pos.Y < 60000
	if lvl, ok := d.PlayerUnit.BaseStats.FindStat(stat.Level, 0); ok {
		r.StatsSane = lvl.Value >= 1 && lvl.Value <= 99
	}
	r.ModeSane = d.PlayerUnit.Mode <= 25 // PlayerMode enum range; garbage reads run wild
	ub := p.gr.UIBytes()
	r.UIBytesSane = len(ub) > 0xF4
	return r
}

// Capture reads one frame. Cheap enough for the Executive cadence (8-15Hz).
func (p *Perceptor) Capture() *Snapshot {
	d := p.gr.GetData()
	s := &Snapshot{Seq: p.seq.Add(1), At: time.Now()}
	pos := d.PlayerUnit.Position
	if pos.X == 0 && pos.Y == 0 {
		p.last.Store(s) // Valid=false: the gap is visible, not papered over
		return s
	}
	lvl := 0
	if v, ok := d.PlayerUnit.BaseStats.FindStat(stat.Level, 0); ok {
		lvl = v.Value
	}
	gold := 0 // gold lives in BaseStats on this repack (measured 2026-07-19; Stats reads 0)
	if v, ok := d.PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
		gold = v.Value
	}
	s.Valid = true
	s.Me = PlayerState{
		Pos:    pos,
		Area:   d.PlayerUnit.Area,
		Mode:   d.PlayerUnit.Mode,
		HPPct:  d.PlayerUnit.HPPercent(),
		MPPct:  d.PlayerUnit.MPPercent(),
		Level:  lvl,
		Gold:   gold,
		InTown: d.PlayerUnit.Area.IsTown(),
	}
	if ub := p.gr.UIBytes(); len(ub) > 0xF4 {
		s.MenuOpen = ub[0xF4] == 1
	}
	for _, m := range d.Monsters.Enemies() {
		if m.Mode == mode.NpcDeath || m.Mode == mode.NpcDead {
			continue
		}
		s.Enemies = append(s.Enemies, EnemyRef{ID: m.UnitID, Pos: m.Position, Mode: uint32(m.Mode)})
	}
	for _, it := range d.Inventory.ByLocation(item.LocationGround) {
		s.Items = append(s.Items, ItemRef{ID: it.UnitID, Pos: it.Position, Name: string(it.Name), Quality: int(it.Quality)})
	}
	// Weapon self-model across BOTH sets: the active hands (LocLeftArm/RightArm) name
	// WeaponKind; the secondary slots are readable too, so the bow set's ammo is known
	// even while she holds javelins — the swap-back decision needs that truth.
	s.Me.WeaponKind = "none"
	bowActive, bowSecondary := false, false
	quivActive, quivSecondary := -2, -2 // -2 = no quiver seen on that set
	for _, eq := range d.Inventory.ByLocation(item.LocationEquipped) {
		bl := eq.Location.BodyLocation
		active := bl == item.LocLeftArm || bl == item.LocRightArm
		secondary := bl == item.LocLeftArmSecondary || bl == item.LocRightArmSecondary
		if !active && !secondary {
			continue
		}
		n := string(eq.Name)
		isQuiver := contains(n, "Quiver") || contains(n, "Arrow") || contains(n, "Bolt")
		if isQuiver {
			qty := -1 // present but quantity unreadable: assume stocked
			if q, ok := eq.FindStat(stat.Quantity, 0); ok {
				qty = q.Value
			}
			if active {
				quivActive = qty
			} else {
				quivSecondary = qty
			}
		}
		if !active {
			if contains(n, "Bow") || contains(n, "Crossbow") {
				bowSecondary = true
			}
			continue
		}
		s.Me.Armed = true
		if contains(n, "Bow") || contains(n, "Crossbow") {
			s.Me.WeaponKind = "bow"
			bowActive = true
		} else if s.Me.WeaponKind != "bow" && !isQuiver && !contains(n, "Shield") && !contains(n, "Buckler") {
			s.Me.WeaponKind = "melee"
		}
	}
	// Arrows = the ammo wherever the bow lives (a dry quiver VANISHES from its slot,
	// so bow-without-quiver reads as 0 — every basic attack would whiff at air).
	switch {
	case bowActive:
		s.Me.Arrows = maxInt(quivActive, 0)
		if quivActive == -1 {
			s.Me.Arrows = -1
		}
	case bowSecondary:
		s.Me.Arrows = maxInt(quivSecondary, 0)
		if quivSecondary == -1 {
			s.Me.Arrows = -1
		}
	default:
		s.Me.Arrows = -1 // no bow anywhere: ammo is nobody's problem
	}
	// Belt potions count by NUMERIC ID first — the mod scrambles the name table
	// (its HP potion reads "Herb" id 602, its mana potion id 607 = the old INVALID607
	// mystery; both proven by vendor purchase deltas 2026-07-19). Name matching stays
	// as the vanilla fallback.
	for _, bp := range d.Inventory.Belt.Items {
		if bp.Position.Y != 0 {
			continue // only the bottom row is drinkable by the belt keys
		}
		n := string(bp.Name)
		if bp.ID == 602 || contains(n, "Healing") || contains(n, "Rejuvenation") {
			s.Me.HealPots++
			s.Me.HPCols = append(s.Me.HPCols, bp.Position.X)
		} else if bp.ID == 607 || contains(n, "Mana") {
			s.Me.ManaPots++
			s.Me.ManaCols = append(s.Me.ManaCols, bp.Position.X)
		}
	}
	s.Me.BeltSlots = d.Inventory.Belt.Rows() * 4
	for _, bp := range d.Inventory.Belt.Items {
		n := string(bp.Name)
		if bp.ID == 602 || contains(n, "Healing") || contains(n, "Rejuvenation") {
			s.Me.BeltHP++
		} else if bp.ID == 607 || contains(n, "Mana") {
			s.Me.BeltMana++
		}
	}
	// Sellable junk audit: inventory items that are neither her one TP tome, one ID
	// tome, nor potions. Plain gear and DUPLICATE tomes (two 300g spares ride along
	// from the probe-click era) are the Fence's stock.
	seenTP, seenID := false, false
	for _, it := range d.Inventory.ByLocation(item.LocationInventory) {
		id := int(it.ID)
		switch {
		case id == 533: // TP tome: keep exactly one
			if !seenTP {
				seenTP = true
				continue
			}
		case id == 534: // ID tome: keep exactly one
			if !seenID {
				seenID = true
				continue
			}
		case id == 602 || id == 607: // potions are fuel, not stock
			continue
		case int(it.Quality) >= 4: // magic+ might be gear-oracle food later — hold
			continue
		}
		s.Junk = append(s.Junk, InvItem{ID: id, GX: it.Position.X, GY: it.Position.Y, Qual: int(it.Quality)})
	}
	s.Me.JunkCount = len(s.Junk)
	if d.Corpse.Found {
		s.Me.CorpseFound = true
		s.Me.CorpsePos = d.Corpse.Position
		p.corpseLatch, p.corpseLatched = d.Corpse.Position, true // refresh with live truth
	} else if s.Me.HPPct <= 0 && !s.Me.InTown {
		// Witness the death: the body falls where she stands.
		p.corpseLatch, p.corpseLatched = pos, true
	} else if p.corpseLatched {
		if s.Me.Armed {
			p.corpseLatched = false // gear is back — the latch served its purpose
		} else {
			s.Me.CorpseFound = true // the remembered body, beyond the streamed rooms
			s.Me.CorpsePos = p.corpseLatch
		}
	}
	s.Me.MinDurPct = 100
	for _, eq := range d.Inventory.ByLocation(item.LocationEquipped) {
		dur, okD := eq.FindStat(stat.Durability, 0)
		mx, okM := eq.FindStat(stat.MaxDurability, 0)
		if okD && okM && mx.Value > 0 {
			pct := dur.Value * 100 / mx.Value
			if pct < s.Me.MinDurPct {
				s.Me.MinDurPct = pct
			}
		}
	}
	for i := range d.Objects {
		if d.Objects[i].IsPortal() || d.Objects[i].IsRedPortal() {
			s.Portals = append(s.Portals, PortalRef{ID: d.Objects[i].ID, Pos: d.Objects[i].Position})
		}
	}
	if !s.Me.InTown { // town has no hostile fire; skip the read there
		for _, ms := range p.gr.Missiles() {
			s.Missiles = append(s.Missiles, MissileRef{ID: ms.UnitID, Txt: ms.TxtFileNo, Pos: ms.Position})
		}
	}
	p.last.Store(s)
	return s
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SurvivalRead is the Sentinel's minimal bounded read: mode + pools + area, nothing else.
// Kept separate from Capture so the Sentinel's cadence never pays full-snapshot cost.
func (p *Perceptor) SurvivalRead() (m mode.PlayerMode, hp, mp int, a area.ID, valid bool) {
	d := p.gr.GetData()
	pos := d.PlayerUnit.Position
	if pos.X == 0 && pos.Y == 0 {
		return 0, 0, 0, 0, false
	}
	return d.PlayerUnit.Mode, d.PlayerUnit.HPPercent(), d.PlayerUnit.MPPercent(), d.PlayerUnit.Area, true
}

// Last returns the most recent snapshot (may be nil before the first Capture).
func (p *Perceptor) Last() *Snapshot { return p.last.Load() }
