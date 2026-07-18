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
	gold := 0
	if v, ok := d.PlayerUnit.Stats.FindStat(stat.Gold, 0); ok {
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
	p.last.Store(s)
	return s
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
