package activity

import (
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/state"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
)

func TestBuffRecastsOnlyWhenLapsedAndCalm(t *testing.T) {
	SetBuffs([]combat.Binding{{Key: 0x70, Skill: skill.BurstOfSpeed}})
	defer SetBuffs(nil)
	b := NewBuff()
	s := &percept.Snapshot{Valid: true}
	s.Me.HPPct, s.Me.MPPct = 100, 100
	if b.Demand(s) == nil {
		t.Fatal("Burst of Speed lapsed, calm, mana: must bid")
	}
	s.Me.States = state.States{state.Quickness}
	if b.Demand(s) != nil {
		t.Fatal("state present: no bid")
	}
	s.Me.States = nil
	s.Enemies = []percept.EnemyRef{{Pos: data.Position{X: 3, Y: 0}}}
	if b.Demand(s) != nil {
		t.Fatal("an enemy within buffCalm: no bid")
	}
	s.Enemies = nil
	b.triedAt[int(skill.BurstOfSpeed)] = time.Now()
	if b.Demand(s) != nil {
		t.Fatal("just tried: wait buffRetry")
	}
}
