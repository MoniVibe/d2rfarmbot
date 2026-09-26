package combat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data/skill"
)

// BaseCalibrationKeys are the skill hotkeys D2R binds by default (F1-F8):
// calibration always probes them.
var BaseCalibrationKeys = []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8"}

// DefaultExtraCalibrationKeys (-calkeys) are probed after the base keys: D2R's
// hotkeys 9-16 are unbound by default and the owner may put a tome there.
const DefaultExtraCalibrationKeys = "f9,f11"

// CalibrationKeys builds the probe list: the base F1-F8 plus the owner's
// extras, SAFE KEYS ONLY. Calibration presses every key it returns while the
// character stands in the world, so anything that could open a panel, drink
// a potion, swap weapons, chat or quit is refused: only F1-F11 are ever
// accepted, never the killswitch (it would disengage the bot mid-probe) and
// never F12 (Windows reserves it for debuggers). Letters (I/C/T/Q/S/W…), digits
// (the belt), Tab, Enter, Esc and the rest are rejected with their reason.
func CalibrationKeys(extra, killswitch string) (keys []string, rejected []string) {
	seen := map[string]bool{}
	for _, k := range BaseCalibrationKeys {
		if !strings.EqualFold(k, killswitch) {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	kill := strings.ToLower(strings.TrimSpace(killswitch))
	for _, raw := range strings.Split(extra, ",") {
		k := strings.ToLower(strings.TrimSpace(raw))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		switch {
		case k == kill:
			rejected = append(rejected, k+" (the killswitch)")
		case k == "f12":
			rejected = append(rejected, k+" (reserved by Windows for debuggers)")
		case !safeFKey(k):
			rejected = append(rejected, k+" (not an F1-F11 skill hotkey: could open a panel, drink, swap or chat)")
		default:
			keys = append(keys, k)
		}
	}
	return keys, rejected
}

func safeFKey(k string) bool {
	switch k {
	case "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11":
		return true
	}
	return false
}

// IsTownPortalSkill reports whether id names the Town Portal tome or scroll
// (by the live table's NAME — the mod scrambles numeric ids).
func IsTownPortalSkill(id skill.ID) bool { return Prior(id) == RoleTownTP }

// OwnedTownPortal finds the Town Portal skill in the live Skills map by name:
// the tome in the bag shows up there whether or not any key selects it.
func OwnedTownPortal(known map[skill.ID]int) (skill.ID, bool) {
	ids := make([]int, 0, len(known))
	for id := range known {
		ids = append(ids, int(id))
	}
	sort.Ints(ids) // deterministic: the book (220) before a stray scroll id
	for _, id := range ids {
		if IsTownPortalSkill(skill.ID(id)) {
			return skill.ID(id), true
		}
	}
	return 0, false
}

// NeedSecondLook: the silent keys deserve a second press when the right
// selection has moved off the skill that was armed at the start AND no probed
// key claimed that skill — one of the silent keys may be bound to it (a key
// that selects the already-selected skill cannot flip anything).
func NeedSecondLook(initial, current skill.ID, proven []Binding) bool {
	if current == initial {
		return false
	}
	for _, b := range proven {
		if b.Skill == initial {
			return false
		}
	}
	return true
}

// NoTownPortalLine is the capability report when no key selects Town Portal:
// the loud line, and the detail that says whether a tome was seen at all.
const NoTownPortalLine = "capability: NO TOWN PORTAL BINDING — recall/unstick portal disabled"

// TownPortalDetail explains a missing binding for the log.
func TownPortalDetail(known map[skill.ID]int) string {
	if id, ok := OwnedTownPortal(known); ok {
		return fmt.Sprintf("%s (id %d) is in the Skills map but no probed key selects it — bind it to a skill hotkey or pass -tpkey",
			SkillName(id), int(id))
	}
	return "no Town Portal tome or scroll in the Skills map (none carried?)"
}
