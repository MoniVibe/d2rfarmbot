package character

import (
	"log/slog"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/data/state"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
)

// RabiesDruid: shapeshift (Werewolf) melee poison build. Buffs + summons are cast in human
// form, then it shifts to Werewolf, applies Rabies (poison that spreads through a pack) and
// finishes with Fury. Templated off berserk_barb.go (melee loop) + wind_druid.go (druid buffs).
// NOTE: not yet live-tested — this build's front-end menu filters synthetic input, so game
// creation is blocked pending a driver-level input path; in-game logic below is untested.
type RabiesDruid struct {
	BaseCharacter
}

func (s RabiesDruid) CheckKeyBindings() []skill.ID {
	requireKeybindings := []skill.ID{skill.Werewolf, skill.Rabies, skill.Fury, skill.TomeOfTownPortal}
	missing := []skill.ID{}
	for _, sk := range requireKeybindings {
		if _, found := s.Data.KeyBindings.KeyBindingForSkill(sk); !found {
			missing = append(missing, sk)
		}
	}
	if len(missing) > 0 {
		s.Logger.Debug("Missing required key bindings for RabiesDruid", slog.Any("bindings", missing))
	}
	return missing
}

func (s RabiesDruid) BuffSkills() []skill.ID {
	// Cast in human form first (oak sage, heart of wolverine, summons), THEN Werewolf to shift.
	buffs := make([]skill.ID, 0)
	if _, found := s.Data.KeyBindings.KeyBindingForSkill(skill.OakSage); found {
		buffs = append(buffs, skill.OakSage)
	}
	if _, found := s.Data.KeyBindings.KeyBindingForSkill(skill.HeartOfWolverine); found {
		buffs = append(buffs, skill.HeartOfWolverine)
	}
	if _, found := s.Data.KeyBindings.KeyBindingForSkill(skill.SummonDireWolf); found {
		buffs = append(buffs, skill.SummonDireWolf, skill.SummonDireWolf, skill.SummonDireWolf)
	}
	if _, found := s.Data.KeyBindings.KeyBindingForSkill(skill.SummonGrizzly); found {
		buffs = append(buffs, skill.SummonGrizzly)
	}
	// Werewolf last so the caster-form buffs land before shifting.
	if _, found := s.Data.KeyBindings.KeyBindingForSkill(skill.Werewolf); found {
		buffs = append(buffs, skill.Werewolf)
	}
	return buffs
}

func (s RabiesDruid) PreCTABuffSkills() []skill.ID {
	return []skill.ID{}
}

// ensureWerewolf shifts into Werewolf form if not already (Rabies/Fury require it).
func (s RabiesDruid) ensureWerewolf() {
	ctx := context.Get()
	if s.Data.PlayerUnit.States.HasState(state.Wolf) {
		return
	}
	kb, found := s.Data.KeyBindings.KeyBindingForSkill(skill.Werewolf)
	if !found {
		return
	}
	ctx.HID.PressKeyBinding(kb)
	time.Sleep(60 * time.Millisecond)
	// Shapeshift targets self; click near the player to activate.
	px, py := ctx.PathFinder.GameCoordsToScreenCords(s.Data.PlayerUnit.Position.X, s.Data.PlayerUnit.Position.Y)
	ctx.HID.Click(game.RightButton, px, py)
	time.Sleep(300 * time.Millisecond)
}

func (s RabiesDruid) anyMonsterHasRabies() bool {
	for _, m := range s.Data.Monsters.Enemies() {
		if m.States.HasState(state.Rabies) && m.Stats[stat.Life] > 0 {
			return true
		}
	}
	return false
}

func (s RabiesDruid) KillMonsterSequence(
	monsterSelector func(d game.Data) (data.UnitID, bool),
	skipOnImmunities []stat.Resist,
) error {
	for attempts := 0; attempts < maxAttackAttempts; attempts++ {
		id, found := monsterSelector(*s.Data)
		if !found {
			return nil
		}
		if !s.preBattleChecks(id, skipOnImmunities) {
			return nil
		}
		monster, monsterFound := s.Data.Monsters.FindByID(id)
		if !monsterFound || monster.Stats[stat.Life] <= 0 {
			continue
		}

		s.ensureWerewolf()

		if s.PathFinder.DistanceFromMe(monster.Position) > meleeRange {
			if err := step.MoveTo(monster.Position); err != nil {
				s.Logger.Warn("Failed to move to monster", slog.String("error", err.Error()))
				continue
			}
		}

		s.performAttack(monster.UnitID)
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

func (s RabiesDruid) performAttack(monsterID data.UnitID) {
	ctx := context.Get()
	ctx.PauseIfNotPriority()
	monster, found := s.Data.Monsters.FindByID(monsterID)
	if !found {
		return
	}
	screenX, screenY := ctx.PathFinder.GameCoordsToScreenCords(monster.Position.X, monster.Position.Y)

	// Apply Rabies to seed poison spread if nothing in the pack is infected yet; otherwise
	// finish with Fury (fast multi-hit werewolf attack).
	if !s.anyMonsterHasRabies() && !monster.States.HasState(state.Rabies) {
		if kb, ok := s.Data.KeyBindings.KeyBindingForSkill(skill.Rabies); ok {
			ctx.HID.PressKeyBinding(kb)
			time.Sleep(60 * time.Millisecond)
			ctx.HID.Click(game.RightButton, screenX, screenY)
			return
		}
	}
	if kb, ok := s.Data.KeyBindings.KeyBindingForSkill(skill.Fury); ok {
		ctx.HID.PressKeyBinding(kb)
		time.Sleep(60 * time.Millisecond)
		ctx.HID.Click(game.RightButton, screenX, screenY)
		return
	}
	// Fallback: basic werewolf melee.
	ctx.HID.Click(game.LeftButton, screenX, screenY)
}

func (s RabiesDruid) killMonster(id npc.ID, t data.MonsterType) error {
	return s.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		m, found := d.Monsters.FindOne(id, t)
		if !found {
			return 0, false
		}
		return m.UnitID, true
	}, nil)
}

func (s RabiesDruid) KillCountess() error  { return s.killMonster(npc.DarkStalker, data.MonsterTypeSuperUnique) }
func (s RabiesDruid) KillAndariel() error  { return s.killMonster(npc.Andariel, data.MonsterTypeUnique) }
func (s RabiesDruid) KillSummoner() error  { return s.killMonster(npc.Summoner, data.MonsterTypeUnique) }
func (s RabiesDruid) KillDuriel() error    { return s.killMonster(npc.Duriel, data.MonsterTypeUnique) }
func (s RabiesDruid) KillMephisto() error  { return s.killMonster(npc.Mephisto, data.MonsterTypeUnique) }
func (s RabiesDruid) KillIzual() error     { return s.killMonster(npc.Izual, data.MonsterTypeUnique) }
func (s RabiesDruid) KillPindle() error    { return s.killMonster(npc.DefiledWarrior, data.MonsterTypeSuperUnique) }
func (s RabiesDruid) KillNihlathak() error { return s.killMonster(npc.Nihlathak, data.MonsterTypeSuperUnique) }
func (s RabiesDruid) KillBaal() error      { return s.killMonster(npc.BaalCrab, data.MonsterTypeUnique) }

func (s RabiesDruid) KillCouncil() error {
	return s.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		for _, m := range d.Monsters.Enemies() {
			if (m.Name == npc.CouncilMember || m.Name == npc.CouncilMember2 || m.Name == npc.CouncilMember3) && m.Stats[stat.Life] > 0 {
				return m.UnitID, true
			}
		}
		return 0, false
	}, nil)
}

func (s RabiesDruid) KillDiablo() error {
	timeout := time.Second * 20
	startTime := time.Now()
	diabloFound := false
	for {
		if time.Since(startTime) > timeout && !diabloFound {
			s.Logger.Error("Diablo was not found, timeout reached")
			return nil
		}
		diablo, found := s.Data.Monsters.FindOne(npc.Diablo, data.MonsterTypeUnique)
		if !found || diablo.Stats[stat.Life] <= 0 {
			if diabloFound {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		diabloFound = true
		return s.killMonster(npc.Diablo, data.MonsterTypeUnique)
	}
}
