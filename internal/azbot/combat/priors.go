// priors.go — the ONE place class skill knowledge may live (compliance rule 5).
// P-7.3: tables vote as priors, with provenance; behavior overrules them. A prior
// SEEDS a role — the flinch audit in Fight confirms or demotes what it seeds
// (P-7.2, P-7.4). No conditional anywhere else may name a class or a skill ID.
//
// Provenance: vanilla D2R skills.txt semantics (melee-range strikes vs projected
// missiles vs thrown-ammo attacks), hand-checked against d2go's skill enum. The
// mod scrambles NAMES, never IDs — IDs are the stable coordinate (the same law
// as the item economy: 602/607/533, tome skills proven at 218/220).
package combat

import "github.com/hectorgimenez/d2go/pkg/data/skill"

// Role is the seed classification a prior gives a proven selection (P-7.5).
type Role int

const (
	RoleNone     Role = iota
	RoleContact       // strikes that land in reach ≤3 — the CONTACT TOOL seed
	RoleReach         // projected shots/casts — the REACH TOOL seed
	RoleThrow         // thrown-ammo attacks: reach with an ammo FUEL gauge
	RoleTownTP        // town-portal utility (not a TOOL)
	RoleIdentify      // identify utility — the panel side door (WARNING 5)
)

// rolePriors seeds every class's early-to-mid kit plus the universal item
// skills. An ID absent here is RoleNone: the selection stays a bare claim
// until live evidence classifies it (P-7.2). Auras, curses, summons, and
// shapeshifts are deliberately absent — the doctrine has no role for them
// yet, and the Lexicon grows by necessity (compliance rule 4).
var rolePriors = map[skill.ID]Role{
	// Universal (no class)
	skill.AttackSkill: RoleContact,
	skill.Kick:        RoleContact,
	skill.Throw:       RoleThrow,

	// Item skills (universal utilities)
	skill.TomeOfTownPortal:   RoleTownTP,
	skill.ScrollOfTownPortal: RoleTownTP,
	skill.TomeOfIdentify:     RoleIdentify,
	skill.ScrollOfIdentify:   RoleIdentify,

	// Amazon
	skill.Jab:             RoleContact,
	skill.PowerStrike:     RoleContact,
	skill.Impale:          RoleContact,
	skill.Fend:            RoleContact,
	skill.ChargedStrike:   RoleContact,
	skill.LightningStrike: RoleContact,
	skill.MagicArrow:      RoleReach,
	skill.FireArrow:       RoleReach,
	skill.ColdArrow:       RoleReach,
	skill.ExplodingArrow:  RoleReach,
	skill.IceArrow:        RoleReach,
	skill.GuidedArrow:     RoleReach,
	skill.MultipleShot:    RoleReach,
	skill.Strafe:          RoleReach,
	skill.ImmolationArrow: RoleReach,
	skill.FreezingArrow:   RoleReach,
	skill.PoisonJavelin:   RoleThrow,
	skill.LightningBolt:   RoleThrow,
	skill.PlagueJavelin:   RoleThrow,
	skill.LightningFury:   RoleThrow,

	// Sorceress
	skill.FireBolt:      RoleReach,
	skill.ChargedBolt:   RoleReach,
	skill.IceBolt:       RoleReach,
	skill.Inferno:       RoleReach,
	skill.IceBlast:      RoleReach,
	skill.FireBall:      RoleReach,
	skill.Lightning:     RoleReach,
	skill.Blaze:         RoleReach,
	skill.FireWall:      RoleReach,
	skill.GlacialSpike:  RoleReach,
	skill.ChainLightning: RoleReach,
	skill.Meteor:        RoleReach,
	skill.Blizzard:      RoleReach,
	skill.Hydra:         RoleReach,
	skill.FrozenOrb:     RoleReach,
	skill.StaticField:   RoleContact, // radius around her — lands where she stands
	skill.FrostNova:     RoleContact,
	skill.Nova:          RoleContact,

	// Necromancer
	skill.Teeth:        RoleReach,
	skill.BoneSpear:    RoleReach,
	skill.BoneSpirit:   RoleReach,
	skill.PoisonDagger: RoleContact,
	skill.PoisonNova:   RoleContact, // radius around her

	// Paladin
	skill.Sacrifice:        RoleContact,
	skill.Smite:            RoleContact,
	skill.Zeal:             RoleContact,
	skill.Charge:           RoleContact,
	skill.Vengeance:        RoleContact,
	skill.HolyBolt:         RoleReach,
	skill.BlessedHammer:    RoleReach,
	skill.FistOfTheHeavens: RoleReach,

	// Barbarian
	skill.Bash:        RoleContact,
	skill.DoubleSwing: RoleContact,
	skill.Stun:        RoleContact,
	skill.Concentrate: RoleContact,
	skill.Frenzy:      RoleContact,
	skill.LeapAttack:  RoleContact,
	skill.Whirlwind:   RoleContact,
	skill.Berserk:     RoleContact,
	skill.DoubleThrow: RoleThrow,

	// Druid (caster and were-forms; shapeshifts themselves have no role yet)
	skill.Firestorm:     RoleReach,
	skill.MoltenBoulder: RoleReach,
	skill.ArcticBlast:   RoleReach,
	skill.Fissure:       RoleReach,
	skill.Twister:       RoleReach,
	skill.Tornado:       RoleReach,
	skill.Volcano:       RoleReach,
	skill.ShockWave:     RoleReach,
	skill.FeralRage:     RoleContact,
	skill.Maul:          RoleContact,
	skill.Rabies:        RoleContact,
	skill.FireClaws:     RoleContact,
	skill.Hunger:        RoleContact,
	skill.Fury:          RoleContact,

	// Assassin
	skill.TigerStrike:      RoleContact,
	skill.DragonTalon:      RoleContact,
	skill.FistsOfFire:      RoleContact,
	skill.DragonClaw:       RoleContact,
	skill.CobraStrike:      RoleContact,
	skill.ClawsOfThunder:   RoleContact,
	skill.DragonTail:       RoleContact,
	skill.BladesOfIce:      RoleContact,
	skill.DragonFlight:     RoleContact,
	skill.PhoenixStrike:    RoleContact,
	skill.PsychicHammer:    RoleReach,
	skill.ShockWeb:         RoleReach,
	skill.ChargedBoltSentry: RoleReach,
	skill.WakeOfFire:       RoleReach,
	skill.BladeSentinel:    RoleReach,
	skill.LightningSentry:  RoleReach,
	skill.WakeOfInferno:    RoleReach,
	skill.BladeFury:        RoleReach,
	skill.FireBlast:        RoleThrow,
}

// Prior returns the seed role for a selection, RoleNone when the table is
// silent. Callers treat the answer as a hint, never as proof (P-7.2).
func Prior(id skill.ID) Role {
	return rolePriors[id]
}
