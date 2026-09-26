package percept

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
)

// ---------------------------------------------------------------- the gear score
//
// OWNER (2026-09-26): "consider inventory and equipment optimizations so the
// bot survives better, as it finds more loot" and "dont forget there's life
// per kill and lifesteal". The old equip oracle compared item QUALITY on four
// armor slots. The score reads the stats: survival first (life, vitality,
// resistances, sustain, hit recovery, damage reduction), then the build
// (+skills to the wearer's class, cast rate for a trapper), then speed (run
// walk — the campaign is a race). Class-agnostic: +class skills count only
// for the wearer's class. Weapons and shields stay the owner's choice.

// gearWeights: points per stat unit. Tuned so +1 to all skills ≈ 25 life ≈
// ~30 resistance points — a trapper's skills are her damage.
var gearWeights = map[stat.ID]float64{
	stat.MaxLife:               1.0,
	stat.Vitality:              2.5,
	stat.Strength:              0.8,
	stat.Dexterity:             0.8,
	stat.Energy:                0.3,
	stat.MaxMana:               0.3,
	stat.FireResist:            0.8,
	stat.ColdResist:            0.8,
	stat.LightningResist:       0.8,
	stat.PoisonResist:          0.5,
	stat.FasterHitRecovery:     0.5,
	stat.FasterRunWalk:         0.5,
	stat.FasterCastRate:        0.8,
	stat.IncreasedAttackSpeed:  0.3,
	stat.LifeSteal:             3.0,
	stat.ManaSteal:             1.5,
	stat.LifeAfterEachKill:     2.0,
	stat.ManaAfterKill:         1.5,
	stat.AllSkills:             25,
	stat.Defense:               0.05,
	stat.NormalDamageReduction: 2.0,
	stat.MagicDamageReduction:  2.0,
	stat.DamageReduced:         3.0, // percent
	stat.MagicFind:             0.1,
	stat.GoldFind:              0.05,
}

// statValue: a stat's display value (life and mana may ride fixed-point on
// some rows: 256× shows up as absurd magnitudes).
func statValue(d stat.Data) float64 {
	v := d.Value
	if (d.ID == stat.MaxLife || d.ID == stat.MaxMana) && v >= 1024 {
		v >>= 8
	}
	return float64(v)
}

// GearScore is the item's survival-weighted worth for a wearer of class.
func GearScore(it data.Item, class data.Class) float64 {
	score := 0.0
	for _, st := range it.Stats {
		switch st.ID {
		case stat.AddClassSkills:
			if st.Layer == int(class) {
				score += 22 * statValue(st)
			}
			continue
		case stat.AddSkillTab:
			if st.Layer/8 == int(class) { // layer = class*8 + tab
				score += 15 * statValue(st)
			}
			continue
		case stat.SingleSkill:
			score += 5 * statValue(st)
			continue
		}
		if w, ok := gearWeights[st.ID]; ok {
			score += w * statValue(st)
		}
	}
	return score
}

// GearSlot is the worn slot an item type fills (LocNone: not judged here —
// weapons, shields and belts stay the owner's call; a belt is also the potion
// rows).
func GearSlot(typ string) item.LocationType {
	switch typ {
	case "helm", "circ", "phlm", "pelt":
		return item.LocHead
	case "tors":
		return item.LocTorso
	case "glov":
		return item.LocGloves
	case "boot":
		return item.LocFeet
	case "amul":
		return item.LocNeck
	case "ring":
		return item.LocLeftRing // either ring slot: judged against the weaker
	}
	return item.LocNone
}

// upgradeMargin: a candidate beats the worn piece only by a real margin — a
// swap costs a town visit and a coin-flip score is not worth the click.
func upgradeBeats(candidate, worn float64) bool {
	m := worn * 0.1
	if m < 5 {
		m = 5
	}
	return candidate > worn+m
}
