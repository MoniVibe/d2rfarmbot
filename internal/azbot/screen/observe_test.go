package screen

import (
	"strings"
	"testing"
)

var world = Hints{Valid: true, InTown: true, HPPct: 100}

// Every capture yields exactly its panel set by sight alone (no memory help).
func TestObserveCaptures(t *testing.T) {
	for file, want := range map[string]Panel{
		"town_clear":       0,
		"town_after_trade": 0,
		"npc_menu":         0, // the floating list has no sight detector yet
		"pause_menu":       PauseMenu,
		"chronicle":        SubPanel,
		"loot_filter":      SubPanel,
		"shop_open":        Shop | Inventory,
		"shop_armor":       Shop | Inventory,
	} {
		r := Observe(load(t, file), world)
		if r.Mode != World {
			t.Errorf("%s: mode %s", file, r.Mode)
		}
		if r.Panels != want || r.Sight != want {
			t.Errorf("%s: panels=%s sight=%s, want %s", file, r.Panels, r.Sight, want)
		}
		if r.Panels&r.Unsure != 0 {
			t.Errorf("%s: %s both seen and unsure", file, r.Panels&r.Unsure)
		}
		if (want == 0) != r.Clear() {
			t.Errorf("%s: Clear=%v", file, r.Clear())
		}
		for _, q := range All {
			if r.Panels&q != 0 && r.Evidence[q] == "" {
				t.Errorf("%s: %s has no evidence", file, q)
			}
		}
		t.Logf("%-17s %s", file, r)
	}
}

func TestObserveCloseButtons(t *testing.T) {
	r := Observe(load(t, "pause_menu"), world)
	if pt := r.Close[PauseMenu]; pt != (Point{960, 685}) {
		t.Errorf("pause close %v", pt)
	}
	r = Observe(load(t, "chronicle"), world)
	if pt := r.Close[SubPanel]; pt != (Point{1413, 81}) {
		t.Errorf("subpanel close %v", pt)
	}
	r = Observe(load(t, "shop_open"), world)
	if pt := r.Close[Shop]; pt != (Point{657, 120}) {
		t.Errorf("shop close %v", pt)
	}
	if pt := r.Close[Inventory]; pt != (Point{1788, 18}) {
		t.Errorf("bag close %v", pt)
	}
}

// Unphotographed panels are reported Unsure, never absent-by-assumption.
func TestObserveStubsAreUnsure(t *testing.T) {
	r := Observe(load(t, "town_clear"), world)
	for _, q := range []Panel{Inventory, CharSheet, SkillTree, QuestLog, Stash, Waypoint,
		Chat, SkillPicker, Mercenary, NPCDialog, NPCMenu, Automap} {
		if r.Unsure&q == 0 {
			t.Errorf("%s not unsure", q)
		}
		if !strings.Contains(r.Evidence[q], "relay/R2/") {
			t.Errorf("%s evidence %q names no capture", q, r.Evidence[q])
		}
	}
	for _, q := range []Panel{PauseMenu, SubPanel, Shop, LeftPanel, RightPanel} {
		if r.Unsure&q != 0 {
			t.Errorf("%s has a real detector but reads unsure", q)
		}
	}
}

func TestObserveMemoryHints(t *testing.T) {
	// 0xF4 is proven for NPC menus: it adds the menu the capture can't place.
	h := world
	h.MenuByte = true
	r := Observe(load(t, "npc_menu"), h)
	if !r.Panels.Has(NPCMenu) || r.Sight.Has(NPCMenu) || r.Unsure&NPCMenu != 0 {
		t.Errorf("npc_menu+0xF4: %s sight=%s", r, r.Sight)
	}
	if !strings.Contains(r.Evidence[NPCMenu], "0xF4") {
		t.Errorf("evidence %q", r.Evidence[NPCMenu])
	}

	// NPCShop against a capture that shows no vendor: a ghost, not a shop.
	h = world
	h.NPCShop = true
	r = Observe(load(t, "town_after_trade"), h)
	if r.Panels.Has(Shop) || r.Unsure&Shop == 0 {
		t.Errorf("ghost shop: %s", r)
	}
	// ...but with no capture at all, memory is all there is.
	r = Observe(nil, h)
	if !r.Panels.Has(Shop) {
		t.Errorf("no capture + NPCShop: %s", r)
	}

	// The waypoint flag lingers: it never asserts.
	h = world
	h.WaypointFlag = true
	r = Observe(load(t, "town_clear"), h)
	if r.Panels.Has(Waypoint) || r.Unsure&Waypoint == 0 || !strings.Contains(r.Evidence[Waypoint], "lingers") {
		t.Errorf("waypoint flag: %s %q", r, r.Evidence[Waypoint])
	}

	// A false hint never removes what the screen shows (0xF4 is blind to pause).
	r = Observe(load(t, "pause_menu"), world)
	if !r.Panels.Has(PauseMenu) {
		t.Error("pause lost without a memory hint")
	}

	h = world
	h.CursorItem = true
	if r = Observe(load(t, "town_clear"), h); !r.CursorItem || r.State().String() != "world+cursor" {
		t.Errorf("cursor: %s", r.State())
	}
}

func TestObserveModes(t *testing.T) {
	img := load(t, "pause_menu")
	for _, c := range []struct {
		h    Hints
		want Mode
	}{
		{Hints{}, Unknown},
		{Hints{Valid: true}, World},
		{Hints{Valid: true, Dead: true}, Dead},
		{Hints{Valid: true, Loading: true}, Loading},
		{Hints{Loading: true}, Loading},
	} {
		r := Observe(img, c.h)
		if r.Mode != c.want {
			t.Errorf("%+v: mode %s, want %s", c.h, r.Mode, c.want)
		}
		// A load screen carries no panels; otherwise the capture still speaks.
		if (c.want == Loading) == r.Panels.Has(PauseMenu) {
			t.Errorf("%+v: panels %s", c.h, r.Panels)
		}
	}
}

func TestPanelSet(t *testing.T) {
	p := Shop | Inventory
	if p.String() != "shop+inventory" || Panel(0).String() != "none" {
		t.Errorf("String %q", p.String())
	}
	if !p.Has(Shop) || !p.Has(Shop|Inventory) || p.Has(Shop|Stash) || p.Has(0) {
		t.Error("Has")
	}
	if Automap.Blocking() || !(Automap | Chat).Blocking() || Panel(0).Blocking() {
		t.Error("Blocking")
	}
	for _, q := range All {
		if q != Automap && !q.Blocking() {
			t.Errorf("%s should block", q)
		}
	}
}
