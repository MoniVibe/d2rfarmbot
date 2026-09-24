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
		"npc_menu":         NPCMenu, // the floating list, found by its gold frame
		"pause_menu":       PauseMenu,
		"chronicle":        SubPanel,
		"loot_filter":      SubPanel,
		"shop_open":        Shop | Inventory | Automap, // the automap was up (area name beside the bag)
		"shop_armor":       Shop | Inventory | Automap,
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
		if !want.Blocking() != r.Clear() {
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

// Every panel has a photographed detector now: a capture leaves nothing
// Unsure, and only a missing capture does (never absent-by-assumption).
func TestObserveUnsureOnlyWithoutCapture(t *testing.T) {
	r := Observe(load(t, "town_clear"), world)
	if r.Unsure != 0 {
		t.Errorf("unsure with a capture: %s", r.Unsure)
	}
	r = Observe(nil, world)
	for _, q := range All {
		if r.Unsure&q == 0 || r.Panels&q != 0 {
			t.Errorf("%s: no capture must read unsure, got %s", q, r)
		}
		if !strings.Contains(r.Evidence[q], "no capture") {
			t.Errorf("%s evidence %q", q, r.Evidence[q])
		}
	}
	if a := CloseStep(r); a.Kind != ActNone {
		t.Errorf("blind: %s", a)
	}
	// The cursor item is the one thing sight still cannot see.
	if seen, unsure := CursorItemVisible(load(t, "town_clear")); seen || !unsure {
		t.Error("cursor item detector should be a stub")
	}
}

func TestObserveMemoryHints(t *testing.T) {
	// 0xF4 LATCHES after an errand (relay R4: ui=npcmenu through a whole field
	// fight). With a frame, sight owns the menu: a latched byte adds nothing...
	h := world
	h.MenuByte = true
	r := Observe(load(t, "town_clear"), h)
	if r.Panels.Has(NPCMenu) || r.Panels.Blocking() {
		t.Errorf("town_clear+latched 0xF4 must read clear: %s", r)
	}
	if !strings.Contains(r.Evidence[NPCMenu], "latched") {
		t.Errorf("evidence %q", r.Evidence[NPCMenu])
	}
	// ...without a frame it is still the only NPC-menu witness.
	if r := Observe(nil, h); !r.Panels.Has(NPCMenu) {
		t.Errorf("no frame+0xF4: %s", r)
	}
	// ...and only agrees where sight already holds the menu or the speech.
	r = Observe(load(t, "npc_menu"), h)
	if !r.Sight.Has(NPCMenu) || !strings.Contains(r.Evidence[NPCMenu], "gold-framed box") ||
		!strings.Contains(r.Evidence[NPCMenu], "0xF4 agrees") {
		t.Errorf("npc_menu+0xF4: %s %q", r, r.Evidence[NPCMenu])
	}
	r = Observe(loadAny(t, "r2/npc_dialog_text", false), h)
	if r.Panels != NPCDialog || !strings.Contains(r.Evidence[NPCDialog], "0xF4 agrees") {
		t.Errorf("dialog+0xF4: %s %q", r, r.Evidence[NPCDialog])
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
