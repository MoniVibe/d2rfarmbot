package combat

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
)

func TestReadLeft(t *testing.T) {
	const carnage = skill.ID(999) // the mod's skill: absent from the local table
	owned := map[skill.ID]skill.Points{skill.AttackSkill: {}, carnage: {Level: 18}}

	l := ReadLeft(data.PlayerUnit{LeftSkill: carnage, Skills: owned})
	if !l.Proven || !l.Primary() || l.Mouse != "left" || l.Name != "skill#999" {
		t.Fatalf("owned non-basic left skill: got %+v, want proven, mouse=left, name by id", *l)
	}
	if l := ReadLeft(data.PlayerUnit{LeftSkill: skill.AttackSkill, Skills: owned}); l.Proven || l.Primary() {
		t.Fatalf("basic Attack on the left is not a primary: %+v", *l)
	}
	if l := ReadLeft(data.PlayerUnit{LeftSkill: carnage, Skills: map[skill.ID]skill.Points{}}); l.Proven {
		t.Fatalf("a left skill absent from the Skills map is not proven: %+v", *l)
	}
	forced := &LeftBinding{Skill: skill.AttackSkill, Forced: true}
	if !forced.Primary() {
		t.Fatal("-leftskill=on forces the primary")
	}
	off := &LeftBinding{Skill: carnage, Proven: true, Disabled: true}
	if off.Primary() {
		t.Fatal("-leftskill=off disables even a proven left skill")
	}
	var none *LeftBinding
	if none.Primary() {
		t.Fatal("nil left binding is never primary")
	}
}

func TestSkillNameFallsBackToID(t *testing.T) {
	if got := SkillName(skill.ID(30000)); got != "skill#30000" {
		t.Fatalf("SkillName(unknown) = %q", got)
	}
	if got := SkillName(skill.AttackSkill); got != "Attack" {
		t.Fatalf("SkillName(0) = %q", got)
	}
}
