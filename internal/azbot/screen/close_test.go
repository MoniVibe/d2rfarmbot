package screen

import (
	"strings"
	"testing"
)

// On the real captures: one step per screen, always a seen button or nothing.
func TestCloseStepCaptures(t *testing.T) {
	for file, want := range map[string]Action{
		"town_clear":       {Kind: ActNone},
		"town_after_trade": {Kind: ActNone},
		"npc_menu":         {Kind: ActKey, Panel: NPCMenu}, // seen by its gold frame: ESC
		"pause_menu":       {Kind: ActClick, X: 960, Y: 685, Panel: PauseMenu},
		"chronicle":        {Kind: ActClick, X: 1413, Y: 81, Panel: SubPanel},
		"loot_filter":      {Kind: ActClick, X: 1413, Y: 81, Panel: SubPanel},
		"shop_open":        {Kind: ActClick, X: 657, Y: 120, Panel: Shop},
		"shop_armor":       {Kind: ActClick, X: 657, Y: 120, Panel: Shop},
	} {
		a := CloseStep(Observe(load(t, file), world))
		if a.Kind != want.Kind || a.X != want.X || a.Y != want.Y || a.Panel != want.Panel {
			t.Errorf("%s: %s, want %s at (%d,%d) for %s", file, a, want.Kind, want.X, want.Y, want.Panel)
		}
	}
	// A latched 0xF4 over a clear frame must never earn an ESC: that ESC is
	// what raises the pause menu out of nothing.
	h := world
	h.MenuByte = true
	if a := CloseStep(Observe(load(t, "town_clear"), h)); a.Kind != ActNone {
		t.Errorf("town_clear+latched 0xF4: %s, want none", a)
	}
}

// Relay R2: every photographed panel is closable by sight — its X when the
// capture shows one, else one ESC (never for the pause menu); the automap is
// left alone.
func TestCloseStepR2(t *testing.T) {
	for file, want := range map[string]Action{
		"r2/world_town": {}, "r2/world_town_2": {}, "r2/world_town_act1": {}, "r2/world_field_night": {},
		"r2/automap_overlay":             {}, // eats nothing
		"r2/inventory":                   {Kind: ActClick, X: 1788, Y: 18, Panel: Inventory},
		"r2/inventory_act1":              {Kind: ActClick, X: 1788, Y: 18, Panel: Inventory},
		"r2/charsheet":                   {Kind: ActClick, X: 657, Y: 120, Panel: CharSheet},
		"r2/inv_and_sheet":               {Kind: ActClick, X: 657, Y: 120, Panel: CharSheet},
		"r2/questlog":                    {Kind: ActClick, X: 657, Y: 120, Panel: QuestLog},
		"r2/waypoint":                    {Kind: ActClick, X: 657, Y: 120, Panel: Waypoint},
		"r2/waypoint_act1_and_inventory": {Kind: ActClick, X: 657, Y: 120, Panel: Waypoint},
		"r2/hireling":                    {Kind: ActClick, X: 657, Y: 120, Panel: Mercenary},
		"r2/stash":                       {Kind: ActClick, X: 946, Y: 18, Panel: Stash},
		"r2/skilltree":                   {Kind: ActClick, X: 1785, Y: 120, Panel: SkillTree},
		"r2/pause_menu":                  {Kind: ActClick, X: 960, Y: 685, Panel: PauseMenu},
		"r2/npc_talk":                    {Kind: ActKey, Panel: NPCMenu},
		"r2/npc_menu_2":                  {Kind: ActKey, Panel: NPCMenu},
		"r2/npc_menu_3":                  {Kind: ActKey, Panel: NPCMenu},
		"r2/npc_menu_4":                  {Kind: ActKey, Panel: NPCMenu},
		"r2/npc_dialog_text":             {Kind: ActKey, Panel: NPCDialog},
		"r2/chat":                        {Kind: ActKey, Panel: Chat},
		"r2/skill_picker":                {Kind: ActKey, Panel: SkillPicker},
		"r2/skill_picker_2":              {Kind: ActKey, Panel: SkillPicker},
	} {
		a := CloseStep(Observe(loadAny(t, file, false), world))
		if a.Kind != want.Kind || a.X != want.X || a.Y != want.Y || a.Panel != want.Panel {
			t.Errorf("%s: %s, want %s at (%d,%d) for %s", file, a, want.Kind, want.X, want.Y, want.Panel)
		}
		if a.Kind == ActKey && a.VK != VKEscape {
			t.Errorf("%s: key 0x%02X", file, a.VK)
		}
	}
}

func TestCloseStepTable(t *testing.T) {
	seen := func(p Panel, close map[Panel]Point) Reading {
		r := Reading{Mode: World, Panels: p, Sight: p, Evidence: map[Panel]string{}, Close: close}
		if r.Close == nil {
			r.Close = map[Panel]Point{}
		}
		return r
	}
	mem := func(p Panel) Reading {
		return Reading{Mode: World, Panels: p, Evidence: map[Panel]string{}, Close: map[Panel]Point{}}
	}
	withMode := func(r Reading, m Mode) Reading { r.Mode = m; return r }
	withUnsure := func(r Reading, u Panel) Reading { r.Unsure = u; return r }

	for _, c := range []struct {
		name  string
		r     Reading
		kind  ActionKind
		panel Panel
		x, y  int
		why   string
	}{
		{"clear", seen(0, nil), ActNone, 0, 0, 0, "clear"},
		{"clear but blind", withUnsure(seen(0, nil), Inventory|Stash), ActNone, 0, 0, 0, "unchecked: inventory+stash"},
		{"automap alone is not closed", seen(Automap, nil), ActNone, 0, 0, 0, "clear"},
		{"subpanel peels before pause", seen(SubPanel|PauseMenu, map[Panel]Point{SubPanel: {1, 2}, PauseMenu: {3, 4}}),
			ActClick, SubPanel, 1, 2, "red X"},
		{"pause", seen(PauseMenu, map[Panel]Point{PauseMenu: {3, 4}}), ActClick, PauseMenu, 3, 4, "Return to Game"},
		{"pause without button: never ESC", seen(PauseMenu, nil), ActUnknown, PauseMenu, 0, 0, "without a known button"},
		{"pause over a bag: button, not ESC", seen(PauseMenu|Inventory, map[Panel]Point{PauseMenu: {3, 4}}),
			ActClick, PauseMenu, 3, 4, ""},
		{"shop X before bag X", seen(Shop|Inventory, map[Panel]Point{Shop: {5, 6}, Inventory: {7, 8}}),
			ActClick, Shop, 5, 6, ""},
		{"shop by grid only: ESC", seen(Shop, nil), ActKey, Shop, 0, 0, "ESC"},
		{"bag with X", seen(RightPanel, map[Panel]Point{RightPanel: {7, 8}}), ActClick, RightPanel, 7, 8, ""},
		{"left panel seen: ESC", seen(LeftPanel, nil), ActKey, LeftPanel, 0, 0, "ESC"},
		{"char sheet seen: ESC", seen(CharSheet, nil), ActKey, CharSheet, 0, 0, "ESC"},
		{"npc menu via 0xF4: ESC", mem(NPCMenu), ActKey, NPCMenu, 0, 0, "ESC"},
		{"shop by memory only: no key", mem(Shop), ActUnknown, Shop, 0, 0, "not seen"},
		{"inventory by memory only: no key", mem(Inventory), ActUnknown, Inventory, 0, 0, "no key without sight"},
		{"dead with a bag: ESC withheld", withMode(seen(Inventory, nil), Dead), ActUnknown, Inventory, 0, 0, "withheld"},
		{"dead with pause: still click the button", withMode(seen(PauseMenu, map[Panel]Point{PauseMenu: {3, 4}}), Dead),
			ActClick, PauseMenu, 3, 4, ""},
		{"loading", withMode(seen(0, nil), Loading), ActUnknown, 0, 0, 0, "loading"},
	} {
		a := CloseStep(c.r)
		if a.Kind != c.kind || a.Panel != c.panel || a.X != c.x || a.Y != c.y || !strings.Contains(a.Reason, c.why) {
			t.Errorf("%s: got %s (panel %s)", c.name, a, a.Panel)
		}
		if a.Kind == ActKey && a.VK != VKEscape {
			t.Errorf("%s: key 0x%02X — only ESC is ever sent", c.name, a.VK)
		}
	}
}

// THE INVARIANT, exhaustively over single panels: a key is emitted only when
// the panel was positively observed (sight, or 0xF4 for the NPC menu).
func TestCloseStepNeverBlindKey(t *testing.T) {
	for _, q := range All {
		for _, m := range []Mode{World, Dead, Unknown} {
			r := Reading{Mode: m, Panels: q, Evidence: map[Panel]string{}, Close: map[Panel]Point{}}
			if a := CloseStep(r); a.Kind == ActKey && q != NPCMenu {
				t.Errorf("%s by memory (%s): sent a key: %s", q, m, a)
			}
			r = Reading{Mode: m, Unsure: q, Evidence: map[Panel]string{}, Close: map[Panel]Point{}}
			if a := CloseStep(r); a.Kind != ActNone {
				t.Errorf("%s unsure (%s): %s", q, m, a)
			}
		}
	}
}
