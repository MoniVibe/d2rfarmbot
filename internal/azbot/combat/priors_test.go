package combat

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data/skill"
)

func skillIDByName(name string) (skill.ID, bool) {
	want := canonicalSkillName(name)
	for id, def := range skill.Skills {
		if canonicalSkillName(def.Name) == want {
			return id, true
		}
	}
	return 0, false
}

func TestPriorUsesLiveSkillNames(t *testing.T) {
	tests := []struct {
		name string
		want Role
	}{
		{name: "Leap", want: RoleVault},
		{name: "Double Swing", want: RoleContact},
		{name: "Leap Attack", want: RoleContact},
		{name: "Book of Identify", want: RoleIdentify},
		{name: "Book of Townportal", want: RoleTownTP},
	}
	for _, tc := range tests {
		id, ok := skillIDByName(tc.name)
		if !ok {
			t.Fatalf("live skills table has no %q", tc.name)
		}
		if got := Prior(id); got != tc.want {
			t.Errorf("Prior(%q, id=%d) = %v, want %v", tc.name, id, got, tc.want)
		}
	}
}
