package gear

import (
	"strings"
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
)

func TestScoreItemSummoner(t *testing.T) {
	tb := loadFixtureTables(t)
	w := DefaultSummonerWeights()

	plain := newTestItem(t, "amu", "Amulet", item.QualityNormal)
	skills := newTestItem(t, "amu", "Amulet of the Whale", item.QualityMagic,
		stat.Data{ID: stat.AllSkills, Value: 2},
		stat.Data{ID: stat.MaxLife, Value: 40})

	sPlain, _ := tb.ScoreItem(plain, w)
	sSkills, reasons := tb.ScoreItem(skills, w)

	if sSkills <= sPlain {
		t.Errorf("+2 skills amulet (%.1f) should beat plain amulet (%.1f)", sSkills, sPlain)
	}
	// 2*40 skills + 40*0.5 life = 100
	if sSkills != 100 {
		t.Errorf("score = %.1f, want 100 (reasons: %v)", sSkills, reasons)
	}
	if !containsSubstring(reasons, "all skills") || !containsSubstring(reasons, "life") {
		t.Errorf("reasons missing contributions: %v", reasons)
	}
}

func TestScoreClassAndTabSkills(t *testing.T) {
	tb := loadFixtureTables(t)
	w := DefaultSummonerWeights()

	necroAmu := newTestItem(t, "amu", "Necro Amulet", item.QualityMagic,
		stat.Data{ID: stat.AddClassSkills, Value: 1, Layer: 2},   // +1 necro skills
		stat.Data{ID: stat.AddSkillTab, Value: 1, Layer: 18})     // +1 summoning
	sorcAmu := newTestItem(t, "amu", "Sorc Amulet", item.QualityMagic,
		stat.Data{ID: stat.AddClassSkills, Value: 1, Layer: 1},   // +1 sorc skills
		stat.Data{ID: stat.AddSkillTab, Value: 1, Layer: 8})      // +1 fire

	sNecro, _ := tb.ScoreItem(necroAmu, w)
	sSorc, _ := tb.ScoreItem(sorcAmu, w)
	if sNecro != 65 { // 35 class + 30 summoning tab
		t.Errorf("necro amulet = %.1f, want 65", sNecro)
	}
	if sSorc != 0 {
		t.Errorf("sorc amulet = %.1f, want 0 for a necro summoner", sSorc)
	}
}

func TestScoreUnidentifiedConservative(t *testing.T) {
	tb := loadFixtureTables(t)
	w := DefaultSummonerWeights()

	unid := newTestItem(t, "amu", "Amulet", item.QualityRare,
		stat.Data{ID: stat.AllSkills, Value: 2}) // stats present in memory...
	unid.Identified = false                      // ...but the item is unread

	score, reasons := tb.ScoreItem(unid, w)
	if score != 0 {
		t.Errorf("unidentified amulet scored %.1f, want 0 (base only)", score)
	}
	if !containsSubstring(reasons, "identify first") {
		t.Errorf("missing 'identify first' reason: %v", reasons)
	}
}

func TestScoreAttributeRequirementFit(t *testing.T) {
	tb := loadFixtureTables(t)
	w := DefaultSummonerWeights() // char str 60, target 80 -> gap 20

	small := newTestItem(t, "amu", "Amulet", item.QualityMagic,
		stat.Data{ID: stat.Strength, Value: 10})
	big := newTestItem(t, "amu", "Amulet", item.QualityMagic,
		stat.Data{ID: stat.Strength, Value: 40})

	sSmall, _ := tb.ScoreItem(small, w) // 10 within gap: 10*1.0 = 10
	sBig, _ := tb.ScoreItem(big, w)     // 20 within gap + 20 excess*0.1: 22
	if sSmall != 10 {
		t.Errorf("small str amulet = %.1f, want 10", sSmall)
	}
	if sBig != 22 {
		t.Errorf("big str amulet = %.1f, want 22 (20 + 20*0.1)", sBig)
	}
	// Str past target must NOT scale linearly.
	if sBig >= 4*sSmall {
		t.Errorf("excess strength scored linearly: %f vs %f", sBig, sSmall)
	}
}

func TestScoreUnusableRequirements(t *testing.T) {
	tb := loadFixtureTables(t)
	w := DefaultSummonerWeights() // StrTarget 80

	heavy := newTestItem(t, "ghm", "Great Helm", item.QualityMagic,
		stat.Data{ID: stat.MaxLife, Value: 100})
	heavy = withDesc(t, heavy, func(d *item.Description) { d.RequiredStrength = 120 })

	usable := newTestItem(t, "cap", "Cap", item.QualityMagic,
		stat.Data{ID: stat.MaxLife, Value: 100})

	sHeavy, reasons := tb.ScoreItem(heavy, w)
	sUsable, _ := tb.ScoreItem(usable, w)
	if sHeavy >= sUsable {
		t.Errorf("unwearable helm (%.1f) should score below wearable cap (%.1f)", sHeavy, sUsable)
	}
	if !containsSubstring(reasons, "requirements out of reach") {
		t.Errorf("missing requirement reason: %v", reasons)
	}
}

func containsSubstring(reasons []string, sub string) bool {
	for _, r := range reasons {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}
