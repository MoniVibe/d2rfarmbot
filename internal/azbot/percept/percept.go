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
	// HasBow: a bow rides SOME set (active or secondary) — the march's swap-back
	// (P-5.9) and the equip oracle's bow judgment key off this, never off which
	// set happens to be in her hands this tick.
	HasBow bool
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
	// InvFree: free inventory grid cells (vanilla 10x4 frame; item footprints from
	// the static Desc table). Loot consults it — a full bag turns every pickup into
	// a 3-fail ban cycle (the owner: "it tries to pick up things but its full").
	InvFree int
	// UnidentCount: magic+ items awaiting the ID tome — the Identify service's docket.
	UnidentCount int
	// EquipCandCount: identified inventory pieces that beat what she wears — the
	// Equip service's docket (shift-click auto-equip, owner-declared gesture).
	EquipCandCount int
	// P-8 SPEND self-model: banked stat points and the stat gap to the best
	// requirement-gated candidate in the bag. NeedStr/NeedDex are the HIGHEST
	// str/dex among identified pieces that would docket but for stats — points
	// spent to these gates unlock an equip in the same town visit (P-8.1).
	Str, Dex   int
	StatPoints int
	NeedStr    int
	NeedDex    int
	// TPScrolls: the TP tome's charge count (mod id 533, stat Quantity) — the
	// escape hatch's fuel gauge (P-4.5). -1 = no tome in the bag (nothing to
	// fill; a loose scroll is not a tome).
	TPScrolls int
	// IDScrolls: the ID tome's charge count (mod id 534) — the identify
	// ritual's fuel. An empty tome's cast FIZZLES and the follow-up click
	// GRABS the item (the owner watched the loop: "identifying already
	// identified items, then picking them up and throwing them").
	IDScrolls int
	// CursorItem: something rides the cursor (WARNING 9) — every service's
	// click interlock, and Equip's parking docket.
	CursorItem bool
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
	// IsBow: the candidate equips onto the bow set — Equip must have the bow set
	// ACTIVE before the shift-click (P-4.4; the click lands on the active hands).
	IsBow bool
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
	Unid     []InvItem    // unidentified magic+ items, grid slots (Identify's list)
	Upgrades []InvItem    // identified upgrades for worn slots (Equip's list)
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
	str, dex := 0, 0
	if v, ok := d.PlayerUnit.Stats.FindStat(stat.Strength, 0); ok {
		str = v.Value
	} else if v, ok := d.PlayerUnit.BaseStats.FindStat(stat.Strength, 0); ok {
		str = v.Value
	}
	if v, ok := d.PlayerUnit.Stats.FindStat(stat.Dexterity, 0); ok {
		dex = v.Value
	} else if v, ok := d.PlayerUnit.BaseStats.FindStat(stat.Dexterity, 0); ok {
		dex = v.Value
	}
	// Gold = inventory + STASH (the owner: "she has cash in the stash, it uses it when
	// she tries to buy something" — vendors draw from the bank on this build, so the
	// bank IS purchasing power; reading only pocket gold had her acting broke at a
	// funded stash). Both live in BaseStats on this repack (Stats reads 0).
	gold := 0
	if v, ok := d.PlayerUnit.BaseStats.FindStat(stat.Gold, 0); ok {
		gold = v.Value
	}
	if v, ok := d.PlayerUnit.BaseStats.FindStat(stat.StashGold, 0); ok {
		gold += v.Value
	}
	// Banked stat points (P-8: points are ordnance). BaseStats, like Level/Gold —
	// the Stats block reads 0 for these on this repack.
	statPts := 0
	if v, ok := d.PlayerUnit.BaseStats.FindStat(stat.StatPoints, 0); ok {
		statPts = v.Value
	}
	s.Valid = true
	s.Me = PlayerState{
		Pos:        pos,
		Area:       d.PlayerUnit.Area,
		Mode:       d.PlayerUnit.Mode,
		HPPct:      d.PlayerUnit.HPPercent(),
		MPPct:      d.PlayerUnit.MPPercent(),
		Level:      lvl,
		Gold:       gold,
		InTown:     d.PlayerUnit.Area.IsTown(),
		Str:        str,
		Dex:        dex,
		StatPoints: statPts,
		TPScrolls:  -1, // until the tome is seen in the bag
		IDScrolls:  -1,
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
	// slotQual/bowQual feed the EQUIP ORACLE: what quality currently occupies each
	// wearable slot (absent = empty slot = any identified candidate is an upgrade).
	s.Me.WeaponKind = "none"
	bowActive, bowSecondary := false, false
	quivActive, quivSecondary := -2, -2 // -2 = no quiver seen on that set
	slotQual := map[item.LocationType]int{}
	bowQual := -1
	for _, eq := range d.Inventory.ByLocation(item.LocationEquipped) {
		bl := eq.Location.BodyLocation
		switch bl {
		case item.LocHead, item.LocTorso, item.LocFeet, item.LocGloves:
			slotQual[bl] = int(eq.Quality)
		}
		active := bl == item.LocLeftArm || bl == item.LocRightArm
		secondary := bl == item.LocLeftArmSecondary || bl == item.LocRightArmSecondary
		if !active && !secondary {
			continue
		}
		// The bow set is the bow set wherever it rides: bowQual reads the equipped
		// bow on EITHER set (P-9.3 — the owner's unique sat undocketed for a night
		// because the judgment only ran while the bow was in her active hands).
		if n := string(eq.Name); contains(n, "Bow") || contains(n, "Crossbow") {
			bowQual = int(eq.Quality)
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
	// Arrows: ONLY a positively measured quantity counts. Quiver detection by NAME is
	// blind on this mod (the name table is scrambled — run 23 read the bow IN HAND as
	// arrows=0 while she shot fine), and quivers self-replenish (owner-confirmed), so
	// dryness is near-impossible anyway. ABSENCE PROVES NOTHING: an invisible quiver
	// must never bench the bow — that phantom 0 locked her to the javelin twice
	// (runs 22 and 23, the owner three times: "she still isn't bow first").
	switch {
	case bowActive && quivActive >= 0:
		s.Me.Arrows = quivActive
	case bowSecondary && quivSecondary >= 0:
		s.Me.Arrows = quivSecondary
	default:
		s.Me.Arrows = -1 // no measurable quiver: unknown, assume stocked
	}
	s.Me.HasBow = bowActive || bowSecondary
	s.Me.CursorItem = len(d.Inventory.ByLocation(item.LocationCursor)) > 0 // WARNING 9
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
	// Sellable junk audit: inventory items that are neither tomes nor potions. TOMES
	// ARE LIFELINES, NEVER STOCK — all of them, spares included. The old "keep exactly
	// one" rule marked duplicate tomes junk, and WHICH copy survived hung on inventory
	// iteration order: the audit could keep an empty probe-era spare and fence the
	// real, scroll-loaded book (the owner: "she simply dropped her tp book again").
	// A spare tome's 150g is never worth the escape hatch.
	// isUpgrade: the EQUIP ORACLE's v1 rule — an identified piece for a slot she
	// wears (or an empty slot) whose quality strictly beats the incumbent, THAT SHE
	// CAN ACTUALLY WEAR: level and stat requirements gate first (the owner watched
	// her hammer shift-click on a piece the game refused — "she tries equipping an
	// item she can't equip"). Bows only judged on the bow set.
	// fitsSlot: the piece would improve a slot she wears (or fill an empty one) —
	// judged on quality alone, requirements NOT yet consulted.
	fitsSlot := func(it data.Item) bool {
		switch it.Desc().Type {
		case "bow":
			// P-9.3: judged against the bow set WHEREVER it rides. The Equip
			// service swaps to the bow set before the click (P-4.4) — the
			// shift-click lands on the active hands, and benching the javelin
			// set into the bag was the old reason this gate existed.
			return s.Me.HasBow && int(it.Quality) > bowQual
		case "tors":
			q, worn := slotQual[item.LocTorso]
			return !worn || int(it.Quality) > q
		case "helm":
			q, worn := slotQual[item.LocHead]
			return !worn || int(it.Quality) > q
		case "boot":
			q, worn := slotQual[item.LocFeet]
			return !worn || int(it.Quality) > q
		case "glov":
			q, worn := slotQual[item.LocGloves]
			return !worn || int(it.Quality) > q
		}
		return false
	}
	isUpgrade := func(it data.Item) bool {
		if !it.Identified || int(it.Quality) < 4 {
			return false
		}
		desc := it.Desc()
		reqLvl := desc.RequiredLevel
		if v, ok := it.FindStat(stat.LevelRequire, 0); ok && v.Value > reqLvl {
			reqLvl = v.Value
		}
		if reqLvl > lvl || desc.RequiredStrength > str || desc.RequiredDexterity > dex {
			// The game would refuse the click — so we refuse the attempt. But a
			// piece gated ONLY by str/dex is a SPEND target (P-8.3): record the
			// gate so banked points can buy the unlock in the same town visit.
			if reqLvl <= lvl && fitsSlot(it) {
				if desc.RequiredStrength > str && desc.RequiredStrength > s.Me.NeedStr {
					s.Me.NeedStr = desc.RequiredStrength
				}
				if desc.RequiredDexterity > dex && desc.RequiredDexterity > s.Me.NeedDex {
					s.Me.NeedDex = desc.RequiredDexterity
				}
			}
			return false
		}
		return fitsSlot(it)
	}
	occupied := 0
	for _, it := range d.Inventory.ByLocation(item.LocationInventory) {
		id := int(it.ID)
		if w, h := it.Desc().InventoryWidth, it.Desc().InventoryHeight; w > 0 && h > 0 {
			occupied += w * h
		} else {
			occupied++ // unknown footprint: count one cell rather than none
		}
		if isUpgrade(it) {
			s.Upgrades = append(s.Upgrades, InvItem{ID: id, GX: it.Position.X, GY: it.Position.Y, Qual: int(it.Quality), IsBow: it.Desc().Type == "bow"})
			continue // an upgrade is never merchandise
		}
		switch {
		case id == 533 || id == 534 || id == 549: // TP/ID tomes + the HORADRIC CUBE
			if id == 533 || id == 534 { // tome charge counts: the rituals' fuel gauges
				n := 0
				if q, ok := it.FindStat(stat.Quantity, 0); ok {
					n = q.Value
				}
				if id == 533 {
					s.Me.TPScrolls = n
				} else {
					s.Me.IDScrolls = n
				}
			}
			continue
		case it.Desc().Type == item.TypeQuest:
			// Quest items are IRREPLACEABLE and the audit's old posture — "everything
			// I don't recognize is stock" — fenced the cube (the owner: "she also lost
			// her cube somehow, what the hell"). Type by numeric ID cannot be lied to
			// by the scrambled name table.
			continue
		case id == 602 || id == 607: // potions are fuel, not stock
			continue
		case int(it.Quality) >= 4 && !it.Identified:
			// UNIDENTIFIED goes to the docket FIRST — rares and uniques included.
			// (The old ordering filed rare+ under 'keeper' before this check ever
			// ran: the owner's unique bow could never be identified.)
			s.Unid = append(s.Unid, InvItem{ID: id, GX: it.Position.X, GY: it.Position.Y, Qual: int(it.Quality)})
			continue
		case int(it.Quality) >= 6: // identified rare+: keepers (equip/stash decide)
			continue
		case int(it.Quality) >= 4:
			// Identified magic she could actually draw (bow/javelin/quiver types) is
			// held for the equip flow; identified magic she cannot use is MERCHANDISE.
			if t := it.Desc().Type; t == "bow" || t == "jave" || t == "bowq" || t == "tpot" {
				continue
			}
		}
		s.Junk = append(s.Junk, InvItem{ID: id, GX: it.Position.X, GY: it.Position.Y, Qual: int(it.Quality)})
	}
	s.Me.InvFree = 40 - occupied // vanilla 10x4 frame; a modded larger bag reads conservative
	if s.Me.InvFree < 0 {
		s.Me.InvFree = 0
	}
	s.Me.UnidentCount = len(s.Unid)
	s.Me.EquipCandCount = len(s.Upgrades)
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
