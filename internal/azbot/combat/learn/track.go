package learn

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/mode"
)

// STRIKE TELEMETRY. Monster Life is a dead channel on this mod: the evidence
// is modes (GettingHit, KnockedBack, Death/Dead), disappearance and
// position. Every issued strike opens a follow-up window; every monster
// within MaxR of the strike's IMPACT point (leap: the aim point; carnage:
// the target) at strike time is watched; evidence during the window is
// credited to exactly one strike (Tracker.credit).

// MaxR is the watch radius around an impact point and the largest n<r>.
const MaxR = 6

// Timing of the follow-up window.
const (
	// Window: how long a strike watches for evidence.
	Window = 1500 * time.Millisecond
	// Latency: evidence younger than this after a click cannot be that
	// click's (no swing lands in 100 ms) — it belongs to an older strike.
	Latency = 100 * time.Millisecond
	// VanishKill: a watched monster gone from the live list WITHOUT a corpse
	// is a kill only within this distance of the crediting strike's impact
	// (the evidence rule: vanished-within-3 = kill; farther could be the
	// snapshot's edge, not a death).
	VanishKill = 3
	// MaxActionSec caps time-to-next-action (a pause is not the strike's cost).
	MaxActionSec = 2.0
	// StaleGrace: a window first observed this long past its end was not
	// watched — it closes without judging what changed meanwhile.
	StaleGrace = 500 * time.Millisecond
)

// Mon is one monster as a tick shows it.
type Mon struct {
	ID     uint32
	P      Pt
	Mode   uint32
	Corpse bool // in Death/Dead mode in the raw monster list
}

func hitMode(m uint32) bool {
	return m == uint32(mode.NpcGettingHit) || m == uint32(mode.NpcKnockedBack)
}

func deadMode(m uint32) bool { return m == uint32(mode.NpcDeath) || m == uint32(mode.NpcDead) }

// Strike is one issued strike and its follow-up.
type Strike struct {
	Seq    int
	Skill  string
	At     time.Time
	Me     Pt
	Aim    Pt // the impact point (leap: ground aim; carnage: the target)
	Target uint32
	D      int           // Chebyshev me→Aim at strike time
	N      [MaxR + 1]int // N[r]: live enemies within r of Aim at strike time (N[0]: on it)
	HP0    int
	MP0    int
	// Verdict: the caller's per-target judgement (policy.Judge) — carried
	// so the one strike line holds both.
	Verdict string

	pre         map[uint32]float64 // watched: id → distance from Aim at strike time
	evid        map[uint32]bool    // ids whose evidence this strike was credited
	HitD, KillD []float64          // credited evidence distances (HitD ⊇ KillD's monsters)
	NextAt      time.Time          // the next action's time (zero: none yet)
	hpMin       int
	mpMin       int
	Closed      bool
	ClosedAt    time.Time
}

// Cluster is the strike's cluster count (live enemies within BucketRadius of
// the impact point, at least 1).
func (s *Strike) Cluster() int {
	if n := s.N[BucketRadius]; n > 0 {
		return n
	}
	return 1
}

// ActionSec is the time the strike occupied: until the next action, capped;
// without a next action the skill's prior action time (the last swing of a
// fight is not charged the silence after it).
func (s *Strike) ActionSec() float64 {
	if !s.NextAt.IsZero() {
		t := s.NextAt.Sub(s.At).Seconds()
		return math.Max(0.15, math.Min(t, MaxActionSec))
	}
	return PriorFor(s.Skill, ClusterBucket(s.Cluster()), DistBucket(s.D)).ActionSec
}

// HPLoss / Mana: what the pool lost between this strike and the next action
// (the partition keeps the next strike's cost off this one).
func (s *Strike) HPLoss() int { return max(0, s.HP0-s.hpMin) }
func (s *Strike) Mana() int   { return max(0, s.MP0-s.mpMin) }

// Hits / Kills are the credited evidence counts.
func (s *Strike) Hits() int  { return len(s.HitD) }
func (s *Strike) Kills() int { return len(s.KillD) }

// Sample is the strike's measurement for the model.
func (s *Strike) Sample() Sample {
	return Sample{Skill: s.Skill, Cluster: s.Cluster(), Dist: s.D, ActionSec: s.ActionSec(),
		Kills: float64(s.Kills()), Hits: float64(s.Hits()), HPLoss: float64(s.HPLoss()), Mana: float64(s.Mana())}
}

// Exposed is every watched monster's distance from the impact point.
func (s *Strike) Exposed() []float64 {
	out := make([]float64, 0, len(s.pre))
	for _, d := range s.pre {
		out = append(out, d)
	}
	sort.Float64s(out)
	return out
}

// Fields is the telemetry tail of the strike's log line.
func (s *Strike) Fields() string {
	next := "-"
	if !s.NextAt.IsZero() {
		next = fmt.Sprintf("%.2fs", s.NextAt.Sub(s.At).Seconds())
	}
	return fmt.Sprintf("aim=(%d,%d) n1=%d n2=%d n3=%d n4=%d n5=%d n6=%d hits=%d kills=%d hitd=%s killd=%s hpLoss=%d mp=%d next=%s",
		s.Aim.X, s.Aim.Y, s.N[1], s.N[2], s.N[3], s.N[4], s.N[5], s.N[6], s.Hits(), s.Kills(),
		dists(s.HitD), dists(s.KillD), s.HPLoss(), s.Mana(), next)
}

func dists(ds []float64) string {
	if len(ds) == 0 {
		return "-"
	}
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = fmt.Sprintf("%.1f", d)
	}
	return strings.Join(parts, ",")
}

// Tracker watches the open strikes' windows and credits evidence.
type Tracker struct {
	open []*Strike
	last *Strike
	mode map[uint32]uint32 // last seen mode (rising-edge detection)
	pos  map[uint32]Pt
	gone map[uint32]bool // deaths (or losses) already accounted
	seq  int
}

// NewTracker is an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{mode: map[uint32]uint32{}, pos: map[uint32]Pt{}, gone: map[uint32]bool{}}
}

// Open is the number of strikes still watching.
func (t *Tracker) Open() int { return len(t.open) }

// Begin opens a strike: counts the cluster around its impact point, arms
// the watch list, and stamps the previous strike's time-to-next-action.
func (t *Tracker) Begin(s *Strike, mons []Mon) *Strike {
	t.seq++
	s.Seq = t.seq
	if t.last != nil && t.last.NextAt.IsZero() && !s.At.Before(t.last.At) {
		t.last.NextAt = s.At
	}
	s.D = Cheb(s.Me, s.Aim)
	s.pre, s.evid = map[uint32]float64{}, map[uint32]bool{}
	s.hpMin, s.mpMin = s.HP0, s.MP0
	for _, m := range mons {
		if m.Corpse || deadMode(m.Mode) || t.gone[m.ID] {
			continue
		}
		for r := 0; r <= MaxR; r++ {
			if Within(s.Aim, m.P, r) {
				s.N[r]++
			}
		}
		if Within(s.Aim, m.P, MaxR) {
			s.pre[m.ID] = Dist(s.Aim, m.P)
		}
		if _, ok := t.mode[m.ID]; !ok {
			t.mode[m.ID] = m.Mode
		}
		t.pos[m.ID] = m.P
	}
	t.open = append(t.open, s)
	t.last = s
	return s
}

// credit hands one piece of evidence on monster id to the strike most
// plausibly responsible: among the open strikes at least Latency old that
// watch id, the one whose impact point was NEAREST to it (a single-target
// swing owns its target's flinch; a leap owns the splash three tiles out),
// the newest on a tie. nil: nobody could have caused it.
func (t *Tracker) credit(id uint32, now time.Time) *Strike {
	var best *Strike
	bd := math.Inf(1)
	for i := len(t.open) - 1; i >= 0; i-- {
		s := t.open[i]
		if now.Sub(s.At) < Latency {
			continue
		}
		if d, ok := s.pre[id]; ok && d < bd {
			best, bd = s, d
		}
	}
	return best
}

// Observe feeds one tick: HP/MP (percent points), the live monsters and the
// corpses. Returns the strikes whose window closed this tick.
func (t *Tracker) Observe(now time.Time, hp, mp int, mons []Mon) []*Strike {
	// A window nobody watched to its end (the Fight lost the wheel) closes
	// UNJUDGED first: a monster gone after seconds unobserved is not a kill.
	var closed []*Strike
	fresh := t.open[:0]
	for _, s := range t.open {
		if now.Sub(s.At) >= Window+StaleGrace {
			s.Closed, s.ClosedAt = true, now
			closed = append(closed, s)
			continue
		}
		fresh = append(fresh, s)
	}
	t.open = fresh
	live := map[uint32]Mon{}
	corpse := map[uint32]bool{}
	for _, m := range mons {
		if m.Corpse || deadMode(m.Mode) {
			corpse[m.ID] = true
			continue
		}
		live[m.ID] = m
	}
	for _, s := range t.open {
		// The pool is charged to a strike only until the next action.
		if s.NextAt.IsZero() || !now.After(s.NextAt) {
			s.hpMin = min(s.hpMin, hp)
			s.mpMin = min(s.mpMin, mp)
		}
	}
	watched := map[uint32]bool{}
	for _, s := range t.open {
		for id := range s.pre {
			watched[id] = true
		}
	}
	ids := make([]uint32, 0, len(watched))
	for id := range watched {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if t.gone[id] {
			continue
		}
		m, present := live[id]
		switch {
		case !present:
			s := t.credit(id, now)
			t.gone[id] = true
			if s == nil {
				continue
			}
			d := s.pre[id]
			if !corpse[id] && d > VanishKill {
				continue // vanished far from the impact: not provably a death
			}
			if !s.evid[id] {
				s.evid[id] = true
				s.HitD = append(s.HitD, d)
			}
			s.KillD = append(s.KillD, d)
		case hitMode(m.Mode) && t.mode[id] != m.Mode:
			if s := t.credit(id, now); s != nil && !s.evid[id] {
				s.evid[id] = true
				s.HitD = append(s.HitD, s.pre[id])
			}
		}
	}
	for id, m := range live {
		t.mode[id] = m.Mode
		t.pos[id] = m.P
	}
	keep := t.open[:0]
	for _, s := range t.open {
		if now.Sub(s.At) >= Window {
			s.Closed, s.ClosedAt = true, now
			closed = append(closed, s)
			continue
		}
		keep = append(keep, s)
	}
	t.open = keep
	if len(t.gone) > 4096 { // bounded memory over a long run
		t.gone = map[uint32]bool{}
	}
	if len(t.mode) > 4096 {
		t.mode, t.pos = map[uint32]uint32{}, map[uint32]Pt{}
	}
	return closed
}

// Flush closes every open strike now (the engagement ended).
func (t *Tracker) Flush(now time.Time) []*Strike {
	out := t.open
	for _, s := range out {
		s.Closed, s.ClosedAt = true, now
	}
	t.open = nil
	return out
}

// ---------------------------------------------------------------- engagement

// EngageRadius: an engagement lasts while any enemy is within this many
// tiles; EngageQuiet of none (or EngageIdle without a strike) ends it.
const (
	EngageRadius = 12
	EngageQuiet  = 750 * time.Millisecond
	EngageIdle   = 6 * time.Second
)

// Summary is one engagement's account.
type Summary struct {
	Size, Cleared  int
	Dur            time.Duration
	HPLoss, Mana   int
	Leaps, Carnage int
	Swings, Basics int
}

func (s Summary) String() string {
	return fmt.Sprintf("engage summary: size=%d cleared=%d dur=%.1fs hpLoss=%d mp=%d leaps=%d carnage=%d ds=%d basic=%d",
		s.Size, s.Cleared, s.Dur.Seconds(), s.HPLoss, s.Mana, s.Leaps, s.Carnage, s.Swings, s.Basics)
}

// Engagement tracks one pack fought until none is within EngageRadius.
type Engagement struct {
	Active     bool
	start      time.Time
	lastStrike time.Time
	quietSince time.Time
	sum        Summary
}

// OnStrike counts a strike (opening the engagement if none is active).
func (e *Engagement) OnStrike(now time.Time, skill string, near int) {
	if !e.Active {
		*e = Engagement{Active: true, start: now}
	}
	e.lastStrike = now
	e.quietSince = time.Time{}
	e.sum.Size = max(e.sum.Size, near)
	switch skill {
	case Leap:
		e.sum.Leaps++
	case Carnage:
		e.sum.Carnage++
	case Swing:
		e.sum.Swings++
	default:
		e.sum.Basics++
	}
}

// OnClosed adds a closed strike's kills and costs.
func (e *Engagement) OnClosed(s *Strike) {
	if !e.Active {
		return
	}
	e.sum.Cleared += s.Kills()
	e.sum.HPLoss += s.HPLoss()
	e.sum.Mana += s.Mana()
}

// Tick reports whether the engagement just ended (nobody within
// EngageRadius for EngageQuiet, or no strike for EngageIdle).
func (e *Engagement) Tick(now time.Time, near int) bool {
	if !e.Active {
		return false
	}
	e.sum.Size = max(e.sum.Size, near)
	if near > 0 {
		e.quietSince = time.Time{}
	} else if e.quietSince.IsZero() {
		e.quietSince = now
	}
	return (!e.quietSince.IsZero() && now.Sub(e.quietSince) >= EngageQuiet) || now.Sub(e.lastStrike) >= EngageIdle
}

// End closes the engagement and returns its summary.
func (e *Engagement) End(now time.Time) Summary {
	s := e.sum
	s.Dur = now.Sub(e.start)
	*e = Engagement{}
	return s
}
