package activity

import (
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data"
	"sync"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Buff (ClassRecover)
//
// SPEED IS THE MISSION (owner, 2026-09-26: "the entire campaign in the least
// amount of time"): KillaryClinton carries Burst of Speed on F1 and the bot
// never cast it. A proven self-buff (combat.RoleBuff) whose player state is
// missing is recast in a calm moment: a one-key, one-click ritual, so it
// rides above the march and yields to anything biting.

const (
	buffMinMP   = 30              // the leap/trap pool comes first below this
	buffCalm    = 6               // tiles: nothing this close when casting
	buffRetry   = 4 * time.Second // per skill: a cast that did not take
	buffTownToo = false           // skills do not cast in town
)

var buffs = struct {
	sync.Mutex
	list    []combat.Binding
	summons []combat.Binding
}{}

// SetSummons: the proven summons kept alive (Shadow Warrior) — recast when
// their unit is not within 30 (percept Summons).
func SetSummons(b []combat.Binding) {
	buffs.Lock()
	buffs.summons = append([]combat.Binding(nil), b...)
	buffs.Unlock()
}

// summonRetry: a summon that did not appear is tried again after this;
// summonGap: between casts of a counted summon (one unit per cast).
const (
	summonRetry = 15 * time.Second
	summonGap   = 1200 * time.Millisecond
)

// nearestCorpse: the monster corpse nearest her (within percept's 15).
func nearestCorpse(s *percept.Snapshot) (data.Position, bool) {
	best, bd := data.Position{}, 1<<30
	for _, c := range s.Me.Corpses {
		if d := chebyshev(s.Me.Pos, c); d < bd {
			best, bd = c, d
		}
	}
	return best, bd < 1<<30
}

// SetBuffs is called by the executive after every capability calibration.
func SetBuffs(b []combat.Binding) {
	buffs.Lock()
	buffs.list = append([]combat.Binding(nil), b...)
	buffs.Unlock()
}

type Buff struct {
	triedAt map[int]time.Time // skill id -> last cast attempt
	next    combat.Binding
	corpse  data.Position // a corpse summon's ground (zero: cast at her feet)
}

func NewBuff() *Buff { return &Buff{triedAt: map[int]time.Time{}} }

func (b *Buff) Name() string { return "buff" }

// lapsed picks the first proven buff whose state is missing and whose retry
// window has passed.
func (b *Buff) lapsed(s *percept.Snapshot, now time.Time) (combat.Binding, bool) {
	buffs.Lock()
	defer buffs.Unlock()
	for _, bd := range buffs.list {
		st, ok := combat.BuffState(bd.Skill)
		if !ok || s.Me.States.HasState(st) {
			continue
		}
		if now.Sub(b.triedAt[int(bd.Skill)]) < buffRetry {
			continue
		}
		return bd, true
	}
	groups := map[string]bool{}
	for _, bd := range buffs.summons {
		sm, ok := combat.SummonOf(bd.Skill)
		if !ok {
			continue
		}
		// One of a group at a time (druid spirits, vines, golems): the first
		// bound skill of the group is the one kept.
		if sm.Group != "" {
			if groups[sm.Group] {
				continue
			}
			groups[sm.Group] = true
		}
		want := profileSummon(combat.SkillName(bd.Skill), sm.Want)
		if want <= 0 {
			continue
		}
		have := 0
		for _, c := range s.Me.Summons {
			if c == sm.Code {
				have++
			}
		}
		if have >= want {
			continue
		}
		// A counted summon (skeletons, ravens) adds one per cast: short gap. A
		// single that did not appear waits summonRetry before the next try.
		gap := summonRetry
		if want > 1 {
			gap = summonGap
		}
		if now.Sub(b.triedAt[int(bd.Skill)]) < gap {
			continue
		}
		if sm.Corpse {
			c, ok := nearestCorpse(s)
			if !ok {
				continue // no ground to raise from here
			}
			b.corpse = c
		} else {
			b.corpse = data.Position{}
		}
		return bd, true
	}
	return combat.Binding{}, false
}

func (b *Buff) Demand(s *percept.Snapshot) *arbiter.Demand {
	if !s.Valid || s.Me.HPPct <= 0 || s.Me.MPPct < buffMinMP || s.Me.CursorItem || (s.Me.InTown && !buffTownToo) {
		return nil
	}
	for _, e := range s.Enemies {
		if !e.Walled && chebyshev(s.Me.Pos, e.Pos) <= buffCalm {
			return nil
		}
	}
	bd, ok := b.lapsed(s, time.Now())
	if !ok {
		return nil
	}
	b.next = bd
	return &arbiter.Demand{Who: b.Name(), Class: arbiter.ClassRecover, Urgency: 0.3,
		Commit: arbiter.Commitment{MinHold: 300 * time.Millisecond}}
}

func (b *Buff) Step(ctx *Ctx) Verdict {
	bd := b.next
	if bd.Key == 0 {
		return Abandoned
	}
	b.triedAt[int(bd.Skill)] = time.Now()
	if b.corpse != (data.Position{}) {
		// Raise from the corpse: the skill aims at the ground under the cursor.
		groundAt(ctx, b.corpse, bd.Key, false)
		ctx.Led.Append(verbs.Outcome{Verb: "buff", Holder: b.Name(), Result: verbs.ResDone,
			Evidence: fmt.Sprintf("%s (skill %d) raised from the corpse at (%d,%d)", combat.SkillName(bd.Skill), int(bd.Skill), b.corpse.X, b.corpse.Y)})
		return Done
	}
	o := verbs.CastSelf{Key: bd.Key, WantID: int(bd.Skill)}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
	ctx.Led.Append(verbs.Outcome{Verb: "buff", Holder: b.Name(), Result: o.Result,
		Evidence: fmt.Sprintf("%s (skill %d) recast: its state (or its unit) was gone", combat.SkillName(bd.Skill), int(bd.Skill))})
	return Done
}
