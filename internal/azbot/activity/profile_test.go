package activity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfileOverridesTheDefaults(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Necro.yaml"), []byte("style: summoner\ntraps: 0\nsummons:\n  Raise Skeleton: 8\n  Clay Golem: 0\n"), 0o644)
	p, ok, err := LoadProfile(dir, "Necro")
	defer setProfile("", Profile{})
	if err != nil || !ok || p.Style != "summoner" {
		t.Fatalf("load: %+v %v %v", p, ok, err)
	}
	if profileSummon("raise skeleton", 5) != 8 || profileSummon("Clay Golem", 1) != 0 || profileSummon("Raven", 5) != 5 {
		t.Fatal("listed summons override (names compared loosely); unlisted keep the default")
	}
	if profileTraps(5) != 0 {
		t.Fatal("traps: 0 means never")
	}
	if _, ok, _ := LoadProfile(dir, "Nobody"); ok || profileTraps(5) != 5 {
		t.Fatal("no file: the defaults")
	}
}
