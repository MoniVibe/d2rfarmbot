package combat

import (
	"strings"
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data/skill"
)

func TestCalibrationKeysSafeOnly(t *testing.T) {
	keys, rejected := CalibrationKeys("f9, F11 ,f10,f12,i,tab,1,esc,enter,q,f3", "f10")
	want := "f1,f2,f3,f4,f5,f6,f7,f8,f9,f11"
	if strings.Join(keys, ",") != want {
		t.Fatalf("keys = %v, want %s", keys, want)
	}
	for _, bad := range []string{"f10", "f12", "i", "tab", "1", "esc", "enter", "q"} {
		found := false
		for _, r := range rejected {
			if strings.HasPrefix(r, bad+" ") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s must be rejected: %v", bad, rejected)
		}
	}
	if keys, _ := CalibrationKeys("", "f10"); len(keys) != 8 {
		t.Fatalf("no extras: %v", keys)
	}
	if keys, _ := CalibrationKeys(DefaultExtraCalibrationKeys, "f9"); strings.Join(keys, ",") != "f1,f2,f3,f4,f5,f6,f7,f8,f11" {
		t.Fatalf("the killswitch is never probed: %v", keys)
	}
}

func TestNeedSecondLook(t *testing.T) {
	tp, leap := skill.ID(220), skill.ID(143)
	// R9's first pass: TP armed at start, F1 (TP) silent, the selection ended on Leap.
	if !NeedSecondLook(tp, leap, []Binding{{Key: 0x74, Skill: leap}}) {
		t.Fatal("the armed-at-start skill was never claimed: look again")
	}
	if NeedSecondLook(tp, tp, nil) {
		t.Fatal("nothing moved: a second press cannot flip anything")
	}
	if NeedSecondLook(tp, leap, []Binding{{Key: 0x70, Skill: tp}, {Key: 0x74, Skill: leap}}) {
		t.Fatal("the initial skill is already claimed")
	}
}

func TestOwnedTownPortalByName(t *testing.T) {
	var tpID skill.ID
	for id, def := range skill.Skills {
		if canonicalSkillName(def.Name) == "bookoftownportal" {
			tpID = id
			break
		}
	}
	if tpID == 0 {
		t.Skip("the local table names no Book of Townportal")
	}
	if id, ok := OwnedTownPortal(map[skill.ID]int{skill.AttackSkill: 0, tpID: 1}); !ok || id != tpID {
		t.Fatalf("OwnedTownPortal = %d,%v want %d", id, ok, tpID)
	}
	if !strings.Contains(TownPortalDetail(map[skill.ID]int{tpID: 1}), "-tpkey") {
		t.Fatal("an owned but unbound tome must point at -tpkey")
	}
	if _, ok := OwnedTownPortal(map[skill.ID]int{skill.AttackSkill: 0}); ok {
		t.Fatal("no tome, no Town Portal")
	}
}
