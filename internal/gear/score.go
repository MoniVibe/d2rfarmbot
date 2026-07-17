package gear

import (
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
)

// BuildWeights expresses how much one point of each affix is worth to a build,
// in a single abstract "value" unit.
//
// SCORING PHILOSOPHY
//
//   - Scores are linear sums of (stat value x weight). No diminishing returns,
//     no breakpoint modelling — the oracle ranks alternatives, it does not
//     simulate DPS. Linear weights are enough to order "+2 summoning skills"
//     above "+10 defense" and are trivially auditable in the reasons list.
//   - Strength/Dexterity on items are counted at full weight only while they
//     close the gap between the character's raw attributes and the attribute
//     targets (gear requirements the build plans around). Past the target
//     they are nearly worthless for a caster, so the excess is scored at
//     ExcessAttributeFactor (default 10%).
//   - Unidentified items are scored from base properties only (defense,
//     socket potential) plus a conservative unknown-affix bonus of zero, with
//     the reason "identify first". This intentionally underestimates: the
//     oracle must never recommend equipping an unknown over a known good.
//   - An item whose strength/dexterity requirement exceeds the character's
//     attribute target is scored at UnusableFactor of its value: it is not
//     worthless (attributes grow) but it cannot be an upgrade right now.
type BuildWeights struct {
	// class-relevant skill weights
	AllSkills   float64            // +N all skills
	ClassSkills float64            // +N <class> skills; class picked by ClassStatLayer
	SkillTabs   map[int]float64    // stat.AddSkillTab layer -> weight per point
	Skills      map[skill.ID]float64 // stat.SingleSkill layer (= skill id) -> weight

	// ClassStatLayer is the stat.AddClassSkills layer for the build's class
	// (0 ama, 1 sor, 2 nec, 3 pal, 4 bar, 5 dru, 6 asn — d2go StatStringMap).
	ClassStatLayer int

	Life      float64 // per point of +life
	Mana      float64
	PerResist float64 // per point of each single resist ("+all res 10" arrives as 4 stats)
	Defense   float64
	FCR       float64 // faster cast rate
	FHR       float64 // faster hit recovery
	MagicFind float64
	Strength  float64 // per point, while under StrTarget
	Dexterity float64 // per point, while under DexTarget

	// Character context for requirement-fit logic.
	CharStrength  int
	CharDexterity int
	StrTarget     int // attribute level the build wants (for gear reqs)
	DexTarget     int

	ExcessAttributeFactor float64 // weight multiplier for str/dex past target
	UnusableFactor        float64 // score multiplier when reqs unmeetable
}

// DefaultSummonerWeights is tuned for a Necromancer summoner: skeletons do the
// killing, so +skills (especially the Summoning tab) dwarf everything; the
// character itself mostly needs to stay alive (life, resists) and recast
// quickly enough (a little FCR). Damage/attack stats are worth nothing.
func DefaultSummonerWeights() BuildWeights {
	return BuildWeights{
		AllSkills:      40,
		ClassSkills:    35,
		ClassStatLayer: 2, // necromancer
		SkillTabs: map[int]float64{
			18: 30, // Summoning Skills (Necromancer) — d2go AddSkillTab layer
			16: 8,  // Curses
			17: 4,  // Poison and Bone
		},
		Skills: map[skill.ID]float64{
			skill.RaiseSkeleton:     12,
			skill.SkeletonMastery:   12,
			skill.RaiseSkeletalMage: 6,
			skill.ClayGolem:         4,
		},
		Life:      0.5,
		Mana:      0.2,
		PerResist: 0.6,
		Defense:   0.05,
		FCR:       0.8,
		FHR:       0.4,
		MagicFind: 0.3,
		Strength:  1.0,
		Dexterity: 0.8,

		CharStrength:  60,
		CharDexterity: 40,
		StrTarget:     80, // enough for mid-tier armors/shields
		DexTarget:     55,

		ExcessAttributeFactor: 0.1,
		UnusableFactor:        0.2,
	}
}

// scoreAccum collects weighted contributions with human-readable reasons.
type scoreAccum struct {
	total   float64
	reasons []string
}

func (a *scoreAccum) add(v float64, format string, args ...any) {
	if v == 0 {
		return
	}
	a.total += v
	a.reasons = append(a.reasons, fmt.Sprintf("%+.1f  %s", v, fmt.Sprintf(format, args...)))
}

// ScoreItem values a live d2go item for a build. Returns the score and the
// list of contributions that produced it (largest first), so a human can audit
// why the oracle preferred one item over another.
func (t *Tables) ScoreItem(it data.Item, w BuildWeights) (float64, []string) {
	acc := &scoreAccum{}

	// Base value everyone gets, identified or not.
	t.scoreBase(it, w, acc)

	if !it.Identified {
		acc.reasons = append(acc.reasons, "identify first (scored at base value only)")
		return t.applyRequirementFit(it, w, acc)
	}

	for _, s := range it.Stats {
		t.scoreStat(s, w, acc)
	}
	return t.applyRequirementFit(it, w, acc)
}

func (t *Tables) scoreBase(it data.Item, w BuildWeights, acc *scoreAccum) {
	// Defense: prefer the live stat (includes ED% etc.); fall back to the
	// midpoint of the base range.
	if st, ok := it.FindStat(stat.Defense, 0); ok {
		acc.add(float64(st.Value)*w.Defense, "%d defense", st.Value)
	} else if def, ok := t.Items[it.Desc().Code]; ok && def.MaxDef > 0 {
		mid := (def.MinDef + def.MaxDef) / 2
		acc.add(float64(mid)*w.Defense, "~%d base defense", mid)
	}
	// Empty sockets are option value: each can hold a gem/rune later.
	if open := t.openSockets(it); open > 0 {
		acc.add(float64(open)*2, "%d open socket(s)", open)
	}
}

func (t *Tables) scoreStat(s stat.Data, w BuildWeights, acc *scoreAccum) {
	v := float64(s.Value)
	switch s.ID {
	case stat.AllSkills:
		acc.add(v*w.AllSkills, "+%d all skills", s.Value)
	case stat.AddClassSkills:
		if s.Layer == w.ClassStatLayer {
			acc.add(v*w.ClassSkills, "+%d class skills", s.Value)
		}
	case stat.AddSkillTab:
		if wt, ok := w.SkillTabs[s.Layer]; ok {
			acc.add(v*wt, "+%d skill tab (layer %d)", s.Value, s.Layer)
		}
	case stat.SingleSkill:
		if wt, ok := w.Skills[skill.ID(s.Layer)]; ok {
			acc.add(v*wt, "+%d to skill %d", s.Value, s.Layer)
		}
	case stat.MaxLife, stat.Life:
		// Memory reader reports +life items via MaxLife.
		acc.add(v*w.Life, "+%d life", s.Value)
	case stat.MaxMana:
		acc.add(v*w.Mana, "+%d mana", s.Value)
	case stat.FireResist:
		acc.add(v*w.PerResist, "+%d fire res", s.Value)
	case stat.ColdResist:
		acc.add(v*w.PerResist, "+%d cold res", s.Value)
	case stat.LightningResist:
		acc.add(v*w.PerResist, "+%d light res", s.Value)
	case stat.PoisonResist:
		acc.add(v*w.PerResist, "+%d poison res", s.Value)
	case stat.FasterCastRate:
		acc.add(v*w.FCR, "+%d%% fcr", s.Value)
	case stat.FasterHitRecovery:
		acc.add(v*w.FHR, "+%d%% fhr", s.Value)
	case stat.MagicFind:
		acc.add(v*w.MagicFind, "+%d%% magic find", s.Value)
	case stat.Strength:
		t.scoreAttribute(v, w.Strength, w.CharStrength, w.StrTarget, w.ExcessAttributeFactor, "strength", acc)
	case stat.Dexterity:
		t.scoreAttribute(v, w.Dexterity, w.CharDexterity, w.DexTarget, w.ExcessAttributeFactor, "dexterity", acc)
	}
}

// scoreAttribute implements the requirement-fit rule: attribute points count
// fully only up to the remaining gap to the build's target, then at the
// excess factor.
func (t *Tables) scoreAttribute(v, weight float64, have, target int, excess float64, name string, acc *scoreAccum) {
	gap := float64(target - have)
	if gap < 0 {
		gap = 0
	}
	useful := v
	if useful > gap {
		useful = gap
	}
	rest := v - useful
	acc.add(useful*weight+rest*weight*excess, "+%.0f %s (%.0f toward req target)", v, name, useful)
}

// applyRequirementFit heavily discounts items the character cannot wear even
// at the build's planned attribute targets.
func (t *Tables) applyRequirementFit(it data.Item, w BuildWeights, acc *scoreAccum) (float64, []string) {
	d := it.Desc()
	if (w.StrTarget > 0 && d.RequiredStrength > w.StrTarget) ||
		(w.DexTarget > 0 && d.RequiredDexterity > w.DexTarget) {
		acc.total *= w.UnusableFactor
		acc.reasons = append(acc.reasons, fmt.Sprintf(
			"requirements out of reach (needs %d str / %d dex, targets %d/%d) — heavily discounted",
			d.RequiredStrength, d.RequiredDexterity, w.StrTarget, w.DexTarget))
	}
	return acc.total, acc.reasons
}

// openSockets = capacity minus filled. Capacity comes from the live NumSockets
// stat when present (accounts for Larzuk/recipes), else the base-item cap.
func (t *Tables) openSockets(it data.Item) int {
	total := t.socketCount(it)
	open := total - len(it.Sockets)
	if open < 0 {
		return 0
	}
	return open
}

// scoreGemMods values what a gem would add in a given context, reusing the
// build weights via the property-code table below. Unknown property codes
// contribute zero (conservative).
func scoreGemMods(mods []GemMod, w BuildWeights) (float64, []string) {
	acc := &scoreAccum{}
	for _, m := range mods {
		avg := float64(m.Min+m.Max) / 2
		switch m.Code {
		case "str":
			acc.add(avg*w.Strength, "+%.0f strength (gem)", avg)
		case "dex":
			acc.add(avg*w.Dexterity, "+%.0f dexterity (gem)", avg)
		case "hp":
			acc.add(avg*w.Life, "+%.0f life (gem)", avg)
		case "mana":
			acc.add(avg*w.Mana, "+%.0f mana (gem)", avg)
		case "ac":
			acc.add(avg*w.Defense, "+%.0f defense (gem)", avg)
		case "res-all":
			acc.add(avg*4*w.PerResist, "+%.0f all res (gem)", avg)
		case "res-fire", "res-cold", "res-ltng", "res-pois":
			acc.add(avg*w.PerResist, "+%.0f %s (gem)", avg, m.Code)
		case "mag%":
			acc.add(avg*w.MagicFind, "+%.0f%% magic find (gem)", avg)
		// att, dmg-*, thorns, ... are worth 0 to caster builds; extend as
		// more builds get weight sets.
		default:
		}
	}
	return acc.total, acc.reasons
}
