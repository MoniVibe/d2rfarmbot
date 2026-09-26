package activity

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------- build profiles
//
// OWNER (2026-09-26): "do we have profiles? or would it know how to use builds
// intuitively? either way, we need to maintain a few profiles i think, so the
// bot knows how to play the character its piloting". Both: the bot reads what
// the hotkeys hold and plays each skill by its role (combat priors — traps,
// buffs, summons, strikes); a profile at profiles/<Character>.yaml
// states the build's intent where the defaults would guess. Absent file: the
// defaults. Every field is optional.
//
//	style: hit-and-run        # the owner's words for the build (logged; later: posture)
//	traps: 5                  # sentries wanted at a pack (0 = never lay traps)
//	summons:                  # summon skill (in-game name) -> count kept alive (0 = never)
//	  Shadow Warrior: 1
//	  Raise Skeleton: 8

// Profile is one character's build intent.
type Profile struct {
	Style   string         `yaml:"style"`
	Traps   *int           `yaml:"traps"`
	Summons map[string]int `yaml:"summons"`
}

var profile = struct {
	sync.Mutex
	p    Profile
	char string
}{}

// LoadProfile reads profiles/<char>.yaml (missing = the defaults).
// ok=true when a file was read.
func LoadProfile(dir, char string) (Profile, bool, error) {
	var p Profile
	b, err := os.ReadFile(filepath.Join(dir, char+".yaml"))
	if os.IsNotExist(err) {
		setProfile(char, Profile{})
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if err := yaml.Unmarshal(b, &p); err != nil {
		return p, false, err
	}
	setProfile(char, p)
	return p, true, nil
}

func setProfile(char string, p Profile) {
	profile.Lock()
	profile.p, profile.char = p, char
	profile.Unlock()
}

// profileTraps: the sentries wanted at a pack (the default when unset).
func profileTraps(def int) int {
	profile.Lock()
	defer profile.Unlock()
	if profile.p.Traps == nil {
		return def
	}
	return *profile.p.Traps
}

// profileSummon: the count wanted for a summon skill named name (the default
// when the profile does not list it). Names compare letters-and-digits only.
func profileSummon(name string, def int) int {
	profile.Lock()
	defer profile.Unlock()
	k := canonName(name)
	for n, c := range profile.p.Summons {
		if canonName(n) == k {
			return c
		}
	}
	return def
}

func canonName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
