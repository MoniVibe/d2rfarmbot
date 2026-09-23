package screen

import (
	"fmt"
	"image"
	"strings"
)

// Hints is what the caller read from memory this tick. Every field is a plain
// value so the oracle stays pure (the executive fills it from percept).
//
// ABSENCE IN MEMORY PROVES NOTHING: every UI channel on this mod has read false
// with its panel on screen at least once (QuitMenu, 0xF4 on the bag, the shop
// byte). A false hint never removes a panel; a true hint adds one only through
// a channel proven to read true while that panel is up.
type Hints struct {
	Valid   bool // percept Snapshot.Valid — a real in-game frame
	Loading bool // load screen (OpenMenus.LoadingScreen / area transition)
	Dead    bool // mode.Dead / mode.Death, or HP% 0
	HPPct   int  // informational; Dead is the verdict
	InTown  bool

	// MenuByte: UIBytes[0xF4]==1 (percept MenuOpen). Proven for NPC menus only
	// (2026-07-18) — blind to the bag and the pause menu.
	MenuByte bool
	// NPCShop: OpenMenus.NPCShop. The screen's shop detector is proven, so this
	// only speaks when no capture was taken; with a capture a disagreement is
	// reported Unsure (a ghost window reads like a shop).
	NPCShop bool
	// WaypointFlag: OpenMenus.Waypoint. It LINGERS true after the panel closes
	// (usewaypoint.go, 03:08 and 03:18 — "the flag lies at level"), so it never
	// asserts the panel, only marks it Unsure.
	WaypointFlag bool
	// CursorItem: an item rides the cursor (WARNING 9) — memory's cursor read.
	CursorItem bool
}

// memoryProven: panels a memory channel may assert on its own (0xF4 → NPC menu).
const memoryProven = NPCMenu

// Point is a click target in capture pixel space.
type Point struct{ X, Y int }

// Reading is one observation: what is up, how we know, and what we could not
// check.
type Reading struct {
	Mode       Mode
	Panels     Panel // positively observed (sight, or a proven memory channel)
	Sight      Panel // the subset of Panels seen on the capture
	Unsure     Panel // detector unavailable, ambiguous, or sources disagree
	CursorItem bool
	Evidence   map[Panel]string // per panel (Panels and Unsure): why
	Close      map[Panel]Point  // close buttons seen on the capture
}

// State is the believed screen: what the Tracker debounces and logs.
type State struct {
	Mode       Mode
	Panels     Panel
	CursorItem bool
}

// String: "world", "world+inventory+shop", "world+cursor".
func (s State) String() string {
	out := s.Mode.String()
	if s.Panels != 0 {
		out += "+" + s.Panels.String()
	}
	if s.CursorItem {
		out += "+cursor"
	}
	return out
}

// State drops the provenance.
func (r Reading) State() State {
	return State{Mode: r.Mode, Panels: r.Panels, CursorItem: r.CursorItem}
}

func (r Reading) String() string {
	s := r.State().String()
	if r.Unsure != 0 {
		s += " (unsure: " + r.Unsure.String() + ")"
	}
	return s
}

// Clear: in the world with nothing positively seen eating input. Unsure panels
// are NOT excluded — the caller decides how much blindness it tolerates.
func (r Reading) Clear() bool { return r.Mode == World && !r.Panels.Blocking() }

// Why joins the evidence for the panels in p.
func (r Reading) Why(p Panel) string {
	var parts []string
	for _, q := range All {
		if p&q != 0 && r.Evidence[q] != "" {
			parts = append(parts, r.Evidence[q])
		}
	}
	return strings.Join(parts, "; ")
}

func (r *Reading) see(p Panel, why string) {
	r.Panels |= p
	r.Sight |= p
	r.Unsure &^= p
	r.Evidence[p] = why
}

func (r *Reading) unsure(p Panel, why string) {
	if r.Panels&p != 0 {
		return
	}
	r.Unsure |= p
	if r.Evidence[p] == "" {
		r.Evidence[p] = why
	} else {
		r.Evidence[p] += "; " + why
	}
}

// stubbed: panels with no photographed detector, and the capture each awaits.
var stubbed = []struct {
	p       Panel
	fn      func(image.Image) (bool, bool)
	capture string
}{
	{NPCMenu, NPCMenuVisible, "npc_menu_*.png"},
	{NPCDialog, NPCDialogVisible, "npc_dialog_text.png"},
	{Inventory, InventoryVisible, "inventory.png"},
	{CharSheet, CharSheetVisible, "charsheet.png"},
	{SkillTree, SkillTreeVisible, "skilltree.png"},
	{QuestLog, QuestLogVisible, "questlog.png"},
	{Stash, StashVisible, "stash.png"},
	{Waypoint, WaypointVisible, "waypoint.png"},
	{Chat, ChatVisible, "chat.png"},
	{SkillPicker, SkillPickerVisible, "skill_picker.png"},
	{Mercenary, MercenaryVisible, "hireling.png"},
	{Automap, AutomapVisible, "automap_overlay.png"},
}

// Observe reads one capture plus this tick's memory hints.
func Observe(img image.Image, h Hints) Reading {
	r := Reading{CursorItem: h.CursorItem, Evidence: map[Panel]string{}, Close: map[Panel]Point{}}
	switch {
	case h.Loading:
		r.Mode = Loading
	case !h.Valid:
		r.Mode = Unknown
	case h.Dead:
		r.Mode = Dead
	default:
		r.Mode = World
	}
	// A load screen draws no panels; whatever was up is gone with the world.
	if r.Mode == Loading {
		return r
	}
	sighted := usable(img)
	if sighted {
		observeSight(&r, img)
	} else {
		for _, q := range []Panel{PauseMenu, SubPanel, Shop, LeftPanel, RightPanel} {
			r.unsure(q, "no capture")
		}
		for _, s := range stubbed {
			r.unsure(s.p, "no capture")
		}
	}

	if h.MenuByte {
		r.Panels |= NPCMenu
		r.Unsure &^= NPCMenu
		r.Evidence[NPCMenu] = "memory 0xF4 (NPC menu or dialog)"
	}
	if h.NPCShop && r.Panels&Shop == 0 {
		if sighted {
			r.unsure(Shop, "memory NPCShop, but no vendor X or grid on screen")
		} else {
			r.Panels |= Shop
			r.Unsure &^= Shop
			r.Evidence[Shop] = "memory NPCShop (no capture)"
		}
	}
	if h.WaypointFlag && r.Panels&Waypoint == 0 {
		r.unsure(Waypoint, "memory Waypoint flag (lingers after close — not proof)")
	}
	return r
}

func observeSight(r *Reading, img image.Image) {
	if x, y, ok := SubPanelX(img); ok {
		r.see(SubPanel, "sub-panel red X")
		r.Close[SubPanel] = Point{x, y}
	}
	if PauseMenuVisible(img) {
		r.see(PauseMenu, "grey pause buttons")
		x, y := ReturnToGameAt(img)
		r.Close[PauseMenu] = Point{x, y}
	}
	xx, xy, xok := ShopOpenX(img)
	grid := TradePanelVisible(img)
	switch {
	case xok && grid:
		r.see(Shop, "vendor red X + empty grid")
	case xok:
		r.see(Shop, "vendor red X")
	case grid:
		r.see(Shop, "vendor empty grid")
	}
	if xok {
		r.Close[Shop] = Point{xx, xy}
	}

	// Half panels: the vendor IS the left frame, and trade always shows the bag
	// on the right — so beside a vendor the right frame is the bag.
	if ls := LeftPanelScore(img); ls.Present() && r.Panels&Shop == 0 {
		r.see(LeftPanel, fmt.Sprintf("left-half panel (frame %.2f/%.2f)", ls.In[0], ls.In[1]))
	}
	if rs := RightPanelScore(img); rs.Present() {
		p, why := RightPanel, "right-half panel"
		if r.Panels&Shop != 0 {
			p, why = Inventory, "right-half panel beside the vendor"
		}
		r.see(p, fmt.Sprintf("%s (frame %.2f/%.2f)", why, rs.In[0], rs.In[1]))
		if x, y, ok := RightPanelX(img); ok {
			r.Close[p] = Point{x, y}
		}
	}

	for _, s := range stubbed {
		if r.Panels&s.p != 0 {
			continue
		}
		seen, unsure := s.fn(img)
		switch {
		case seen:
			r.see(s.p, "sight")
		case unsure:
			r.unsure(s.p, "no detector (needs relay/R2/"+s.capture+")")
		}
	}
}
