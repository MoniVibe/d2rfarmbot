package character

import (
	"log/slog"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
)

const (
	PoisonNovaMinDistance      = 4
	PoisonNovaMaxDistance      = 12
	LowerResistMinDistance     = 18
	LowerResistMaxDistance     = 25
	CorpseExplosionMaxDistance = 15
	AmplifyDamageMinDistance   = 18
	AmplifyDamageMaxDistance   = 25
	LowerResistThreshold       = 80
	MonsterDensityThreshold    = 3
)

type PoisonNovaNecro struct {
	BaseCharacter
}

func (p PoisonNovaNecro) CheckKeyBindings() []skill.ID {
	requiredKeybindings := []skill.ID{
		skill.PoisonNova,
		skill.LowerResist,
		skill.AmplifyDamage,
		skill.CorpseExplosion,
		skill.BoneArmor,
		skill.Teleport,
		skill.TomeOfTownPortal,
	}
	missingKeybindings := []skill.ID{}
	for _, cskill := range requiredKeybindings {
		if _, found := p.Data.KeyBindings.KeyBindingForSkill(cskill); !found {
			missingKeybindings = append(missingKeybindings, cskill)
		}
	}
	if len(missingKeybindings) > 0 {
		p.Logger.Debug("There are missing required key bindings.", slog.Any("Bindings", missingKeybindings))
	}
	return missingKeybindings
}

func (p PoisonNovaNecro) PreCTABuffSkills() []skill.ID {
	return []skill.ID{skill.BattleCommand, skill.BattleOrders}
}

func (p PoisonNovaNecro) BuffSkills() []skill.ID {
	return []skill.ID{skill.BoneArmor}
}

func (p PoisonNovaNecro) KillMonsterSequence(
	monsterSelector func(d game.Data) (data.UnitID, bool),
	skipOnImmunities []stat.Resist,
) error {
	ctx := context.Get()
	for {
		ctx.PauseIfNotPriority()

		id, found := monsterSelector(*p.Data)
		if !found {
			return nil
		}

		if !p.preBattleChecks(id, skipOnImmunities) {
			return nil
		}

		monster, found := p.Data.Monsters.FindByID(id)
		if !found || monster.Stats[stat.Life] <= 0 {
			return nil
		}

		// Repositioning against high monster density
		if len(p.Data.Monsters.Enemies()) > MonsterDensityThreshold {
			if _, hasTeleport := p.Data.KeyBindings.KeyBindingForSkill(skill.Teleport); hasTeleport {
				step.SecondaryAttack(skill.Teleport, monster.UnitID, 0, step.Distance(5, 7))
			}
		}

		// 1. Lower Resist
		if p.shouldCastLowerResist(monster) {
			step.SecondaryAttack(skill.LowerResist, monster.UnitID, 1, step.RangedDistance(LowerResistMinDistance, LowerResistMaxDistance))
		}

		// 2. Poison Nova
		step.SecondaryAttack(skill.PoisonNova, monster.UnitID, 2, step.RangedDistance(PoisonNovaMinDistance, PoisonNovaMaxDistance))

		// 3. Amplify Damage
		step.SecondaryAttack(skill.AmplifyDamage, monster.UnitID, 1, step.RangedDistance(AmplifyDamageMinDistance, AmplifyDamageMaxDistance))

		// Give poison a little bit of time to create a corpse
		time.Sleep(time.Millisecond * 300)

		// 4. Corpse Explosion Workaround: Target living enemies with CE selected.
		// The game engine will find the nearest corpse to explode.
		for i := 0; i < 5; i++ {
			// Target the initial monster
			step.SecondaryAttack(skill.CorpseExplosion, monster.UnitID, 1)

			// Target other nearby enemies as well
			for _, m := range p.Data.Monsters.Enemies() {
				if m.UnitID != monster.UnitID {
					step.SecondaryAttack(skill.CorpseExplosion, m.UnitID, 1)
					break // Move to next CE loop iteration after finding one more enemy
				}
			}
		}
	}
}

func (p PoisonNovaNecro) shouldCastLowerResist(monster data.Monster) bool {
	maxLife := float64(monster.Stats[stat.MaxLife])
	if maxLife == 0 {
		return false
	}

	hpPercentage := (float64(monster.Stats[stat.Life]) / maxLife) * 100
	return hpPercentage > LowerResistThreshold
}

func (p PoisonNovaNecro) killBoss(bossID npc.ID, monsterType data.MonsterType) error {
	ctx := context.Get()
	for {
		ctx.PauseIfNotPriority()

		boss, found := p.Data.Monsters.FindOne(bossID, monsterType)
		if !found || boss.Stats[stat.Life] <= 0 {
			return nil
		}

		step.SecondaryAttack(skill.LowerResist, boss.UnitID, 1, step.Distance(LowerResistMinDistance, LowerResistMaxDistance))
		step.SecondaryAttack(skill.PoisonNova, boss.UnitID, 1, step.Distance(PoisonNovaMinDistance, PoisonNovaMaxDistance))
	}
}

func (p PoisonNovaNecro) killMonsterByName(id npc.ID, monsterType data.MonsterType, skipOnImmunities []stat.Resist) error {
	return p.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		if m, found := d.Monsters.FindOne(id, monsterType); found {
			return m.UnitID, true
		}
		return 0, false
	}, skipOnImmunities)
}

func (p PoisonNovaNecro) KillAndariel() error {
	return p.killBoss(npc.Andariel, data.MonsterTypeUnique)
}

func (p PoisonNovaNecro) KillDuriel() error {
	return p.killBoss(npc.Duriel, data.MonsterTypeUnique)
}

func (p PoisonNovaNecro) KillMephisto() error {
	return p.killBoss(npc.Mephisto, data.MonsterTypeUnique)
}

func (p PoisonNovaNecro) KillDiablo() error {
	timeout := time.Second * 20
	startTime := time.Now()
	diabloFound := false

	for {
		if time.Since(startTime) > timeout && !diabloFound {
			p.Logger.Error("Diablo was not found, timeout reached")
			return nil
		}

		diablo, found := p.Data.Monsters.FindOne(npc.Diablo, data.MonsterTypeUnique)
		if !found || diablo.Stats[stat.Life] <= 0 {
			if diabloFound {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}

		diabloFound = true
		p.Logger.Info("Diablo detected, attacking")
		return p.killBoss(npc.Diablo, data.MonsterTypeUnique)
	}
}

func (p PoisonNovaNecro) KillBaal() error {
	return p.killBoss(npc.BaalCrab, data.MonsterTypeUnique)
}

func (p PoisonNovaNecro) KillCountess() error {
	return p.killMonsterByName(npc.DarkStalker, data.MonsterTypeSuperUnique, nil)
}

func (p PoisonNovaNecro) KillSummoner() error {
	return p.killMonsterByName(npc.Summoner, data.MonsterTypeUnique, nil)
}

func (p PoisonNovaNecro) KillIzual() error {
	return p.killBoss(npc.Izual, data.MonsterTypeUnique)
}

func (p PoisonNovaNecro) KillCouncil() error {
	return p.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		for _, m := range d.Monsters.Enemies() {
			if m.Name == npc.CouncilMember || m.Name == npc.CouncilMember2 || m.Name == npc.CouncilMember3 {
				return m.UnitID, true
			}
		}
		return 0, false
	}, nil)
}

func (p PoisonNovaNecro) KillPindle() error {
	return p.killMonsterByName(npc.DefiledWarrior, data.MonsterTypeSuperUnique, p.CharacterCfg.Game.Pindleskin.SkipOnImmunities)
}

func (p PoisonNovaNecro) KillNihlathak() error {
	return p.killMonsterByName(npc.Nihlathak, data.MonsterTypeSuperUnique, nil)
}