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

import (
	"strings"
	"unicode"

	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/state"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
)

// Role is the seed classification a prior gives a proven selection (P-7.5).
type Role int

const (
	RoleNone     Role = iota
	RoleContact       // strikes that land in reach ≤3 — the CONTACT TOOL seed
	RoleReach         // projected shots/casts — the REACH TOOL seed
	RoleThrow         // thrown-ammo attacks: reach with an ammo FUEL gauge
	RoleTownTP        // town-portal utility (not a TOOL)
	RoleIdentify      // identify utility — the panel side door (WARNING 5)
	RoleVault         // cursor-targeted self-displacement: the ring becomes scenery
	RoleTrap          // ground-placed sentries: laid at a pack, then the fight goes on in melee
	RoleBuff          // self-cast buff with a readable player state (BuffState)
	RoleSummon        // a summon kept alive: recast when its unit is gone (SummonCode)
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
	skill.FireBolt:       RoleReach,
	skill.ChargedBolt:    RoleReach,
	skill.IceBolt:        RoleReach,
	skill.Inferno:        RoleReach,
	skill.IceBlast:       RoleReach,
	skill.FireBall:       RoleReach,
	skill.Lightning:      RoleReach,
	skill.Blaze:          RoleReach,
	skill.FireWall:       RoleReach,
	skill.GlacialSpike:   RoleReach,
	skill.ChainLightning: RoleReach,
	skill.Meteor:         RoleReach,
	skill.Blizzard:       RoleReach,
	skill.Hydra:          RoleReach,
	skill.FrozenOrb:      RoleReach,
	skill.StaticField:    RoleContact, // radius around her — lands where she stands
	skill.FrostNova:      RoleContact,
	skill.Nova:           RoleContact,

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
	skill.Leap:        RoleVault, // the owner granted it 2026-07-20: "jump through problematic situations"
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

	// Barbarian warcries (self buffs)
	skill.Shout:         RoleBuff,
	skill.BattleOrders:  RoleBuff,
	skill.BattleCommand: RoleBuff,

	// Assassin
	skill.TigerStrike:    RoleContact,
	skill.DragonTalon:    RoleContact,
	skill.FistsOfFire:    RoleContact,
	skill.DragonClaw:     RoleContact,
	skill.CobraStrike:    RoleContact,
	skill.ClawsOfThunder: RoleContact,
	skill.DragonTail:     RoleContact,
	skill.BladesOfIce:    RoleContact,
	skill.DragonFlight:   RoleContact,
	skill.PhoenixStrike:  RoleContact,
	skill.PsychicHammer:  RoleReach,
	// Traps are NOT reach (owner, 2026-09-26, KillaryClinton: "launch some
	// sentries and melee the rest"): a trap binding keeps the assassin a
	// brawler — sentries are an opener laid at a pack, the claws do the rest.
	skill.ShockWeb:          RoleTrap,
	skill.ChargedBoltSentry: RoleTrap,
	skill.WakeOfFire:        RoleTrap,
	skill.BladeSentinel:     RoleTrap, // placed at a point like a trap; "reach" disarmed the brawler
	skill.BurstOfSpeed:      RoleBuff,
	skill.Fade:              RoleBuff,
	skill.LightningSentry:   RoleTrap,
	skill.WakeOfInferno:     RoleTrap,
	skill.DeathSentry:       RoleTrap,
	skill.BladeFury:         RoleReach,
	skill.FireBlast:         RoleThrow,
}

// The local d2go build carries the live skills table separately from the enum
// constants.  A few class skills therefore have different numeric positions in
// those two tables (for example, the live table calls ID 133 "Double Swing").
// Resolve the semantic name first so calibration never mistakes one class skill
// for another merely because the enum was generated from a different table.
func canonicalSkillName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

var rolePriorsByName = func() map[string]Role {
	m := make(map[string]Role, len(rolePriors))
	for id, role := range rolePriors {
		if name, ok := skill.SkillNames[id]; ok {
			m[canonicalSkillName(name)] = role
		}
	}
	// The live table calls the tome skills "Book ..." rather than "Tome ...".
	m["bookofidentify"] = RoleIdentify
	m["bookoftownportal"] = RoleTownTP
	return m
}()

// Prior returns the seed role for a selection, RoleNone when the table is
// silent. Callers treat the answer as a hint, never as proof (P-7.2).
func Prior(id skill.ID) Role {
	// THE MOD'S OWN TABLE FIRST (2026-09-26): Reimagined adds skills the
	// vanilla enum does not know (264 is "Snowlash Sentry" here, "Mind Blast"
	// in d2go). The mod's skills.txt key is authoritative for what it adds.
	if r := modRole(id); r != RoleNone {
		return r
	}
	if def, ok := skill.Skills[id]; ok {
		if role, found := rolePriorsByName[canonicalSkillName(def.Name)]; found {
			return role
		}
	}
	return rolePriors[id]
}

// buffStates: the player state each self-buff sets while active. A buff with
// no entry here is never recast (no way to see that it lapsed).
var buffStates = map[skill.ID]state.State{
	skill.BurstOfSpeed:  state.Quickness,
	skill.Fade:          state.Fade,
	skill.Shout:         state.Shout,
	skill.BattleOrders:  state.Battleorders,
	skill.BattleCommand: state.Battlecommand,
}

// BuffState: the state a self-buff shows while active (by the live skill
// table's name first, like Prior — the enum and the table disagree on some IDs).
func BuffState(id skill.ID) (state.State, bool) {
	if def, ok := skill.Skills[id]; ok {
		for b, st := range buffStates {
			if name, ok := skill.SkillNames[b]; ok && canonicalSkillName(name) == canonicalSkillName(def.Name) {
				return st, true
			}
		}
	}
	st, ok := buffStates[id]
	return st, ok
}

// modRole classifies by the mod skill table's key: every sentry is a trap
// (Snowlash, Glacial Burst, the vanilla ones), the shadows are summons.
func modRole(id skill.ID) Role {
	db := gamedata.Get()
	if db == nil {
		return RoleNone
	}
	sk := db.Skill(int(id))
	if sk == nil {
		return RoleNone
	}
	k := canonicalSkillName(sk.Key)
	switch {
	case strings.HasSuffix(k, "sentry"):
		return RoleTrap
	case k == "shadowwarrior" || k == "shadowmaster":
		return RoleSummon
	}
	return RoleNone
}

// SummonCode: the monstats code of a summon skill's unit ("" = not a summon).
func SummonCode(id skill.ID) string {
	db := gamedata.Get()
	if db == nil {
		return ""
	}
	if sk := db.Skill(int(id)); sk != nil {
		switch canonicalSkillName(sk.Key) {
		case "shadowwarrior":
			return "shadowwarrior"
		case "shadowmaster":
			return "shadowmaster"
		}
	}
	return ""
}
