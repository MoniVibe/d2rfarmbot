package percept

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
)

func withStats(st ...stat.Data) data.Item { return data.Item{Stats: st} }

func TestGearScoreValuesSurvivalAndOwnClass(t *testing.T) {
	plain := withStats(stat.Data{ID: stat.Defense, Value: 20})
	sturdy := withStats(stat.Data{ID: stat.Defense, Value: 20}, stat.Data{ID: stat.MaxLife, Value: 30}, stat.Data{ID: stat.LifeAfterEachKill, Value: 5})
	if !upgradeBeats(GearScore(sturdy, data.Assassin), GearScore(plain, data.Assassin)) {
		t.Fatal("+30 life and life per kill beat bare defense")
	}
	own := withStats(stat.Data{ID: stat.AddClassSkills, Layer: int(data.Assassin), Value: 1})
	other := withStats(stat.Data{ID: stat.AddClassSkills, Layer: int(data.Sorceress), Value: 1})
	if GearScore(own, data.Assassin) <= 0 || GearScore(other, data.Assassin) != 0 {
		t.Fatal("+class skills count only for the wearer's class")
	}
	if GearScore(withStats(stat.Data{ID: stat.MaxLife, Value: 30 << 8}), data.Assassin) != 30 {
		t.Fatal("fixed-point life reads as its display value")
	}
}

func TestGearSlotsJudged(t *testing.T) {
	if GearSlot("ring") != item.LocLeftRing || GearSlot("circ") != item.LocHead || GearSlot("axe") != item.LocNone {
		t.Fatal("rings and circlets are judged; weapons are not")
	}
	if upgradeBeats(52, 50) || !upgradeBeats(80, 50) {
		t.Fatal("a real margin is required")
	}
}
