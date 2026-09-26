package activity

import (
	"fmt"
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
	buffTownToo = true            // Burst of Speed pays in town errands as well
)

var buffs = struct {
	sync.Mutex
	list []combat.Binding
}{}

// SetBuffs is called by the executive after every capability calibration.
func SetBuffs(b []combat.Binding) {
	buffs.Lock()
	buffs.list = append([]combat.Binding(nil), b...)
	buffs.Unlock()
}

type Buff struct {
	triedAt map[int]time.Time // skill id -> last cast attempt
	next    combat.Binding
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
	o := verbs.CastSelf{Key: bd.Key, WantID: int(bd.Skill)}.Do(ctx.M, ctx.GR, ctx.P, ctx.Led, b.Name())
	ctx.Led.Append(verbs.Outcome{Verb: "buff", Holder: b.Name(), Result: o.Result,
		Evidence: fmt.Sprintf("%s (skill %d) recast: its state had lapsed", combat.SkillName(bd.Skill), int(bd.Skill))})
	return Done
}
