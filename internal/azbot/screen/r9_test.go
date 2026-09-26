package screen

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"
)

// RELAY R9 (2026-09-24, janitor on, Maggot Lair level 1, mid-fight):
//
//	ui world+automap -> world+shop+automap  why="vendor empty grid"
//	gate foreign: shop  act="key 0x1B"      (ESC at nothing: the pause menu)
//	ui world -> world+subpanel  why="sub-panel red X"
//	gate foreign: subpanel  act="click 1413,81"   (a world click) ... x98
//
// These tests hold the screen half of the fix: no shop and no sub-panel on a
// dark dungeon, a red tile alone is never a panel, and the reachability rules.

// field: in the world, outside town.
var field = Hints{Valid: true, HPPct: 100}

// The dark negatives read nothing but what they show: the lair frame its
// pause menu, the cave its area name, the night field nothing — in town hints
// (pure sight) and in field hints alike.
func TestR9DungeonNegatives(t *testing.T) {
	for _, name := range []string{"r9/esc_loop_now", "r2/world_cave", "r2/world_field_night"} {
		img := loadAny(t, name, false)
		for _, h := range []Hints{world, field} {
			r := Observe(img, h)
			if r.Panels != truth[name] || r.Sight != truth[name] {
				t.Errorf("%s (town=%v): %s — %s, want %s", name, h.InTown, r, r.Why(r.Panels), truth[name])
			}
			if r.Unsure != 0 {
				t.Errorf("%s (town=%v): unsure %s — %s", name, h.InTown, r.Unsure, r.Why(r.Unsure))
			}
		}
		if ShopVisible(img) || TradePanelVisible(img) {
			t.Errorf("%s: a shop on a dark world", name)
		}
		if _, _, ok := SubPanelX(img); ok {
			t.Errorf("%s: a sub-panel on a dark world", name)
		}
		want := ActNone
		if truth[name].Has(PauseMenu) {
			want = ActClick // Return to Game, never ESC
		}
		if a := CloseStep(Observe(img, field)); a.Kind != want || a.Kind == ActKey {
			t.Errorf("%s: CloseStep %s", name, a)
		}
	}
	// The R9 frame: grid cells all dark (the old fallback's 16/16) under no X.
	img := loadAny(t, "r9/esc_loop_now", false)
	if x, y := ReturnToGameAt(img); x != 960 || y != 685 {
		t.Errorf("Return to Game at (%d,%d)", x, y)
	}
}

// paintRed stamps a dark red 21x21 tile (lair scenery: R>=2G, R>=2B) at a
// reference spot — what the red-tile test saw at the X spots in the lair.
func paintRed(img *image.RGBA, c closeSpot) {
	x, y := c.a.at(img, c.x, c.y)
	draw.Draw(img, image.Rect(x-10, y-10, x+11, y+11), &image.Uniform{color.RGBA{92, 30, 22, 255}}, image.Point{}, draw.Src)
}

// A RED TILE IS NOT A PANEL: the cave with red scenery at every photographed
// X spot and its dark cells where the vendor grid was sampled — the exact
// conditions of R9 — reads no shop, no sub-panel and no side panel; the side
// X spots only make their half Unsure.
func TestR9RedTileIsNotAPanel(t *testing.T) {
	src := loadAny(t, "r2/world_cave", false).(*image.RGBA)
	// The vendor's X spot alone first (the old "vendor empty grid": red X,
	// 16/16 dark cells, no sub-panel X to veto it).
	only := image.NewRGBA(src.Rect)
	copy(only.Pix, src.Pix)
	paintRed(only, xLeft)
	if ShopVisible(only) || TradePanelVisible(only) || Observe(only, world).Panels.Blocking() {
		t.Errorf("red tile at the vendor X over a dark grid: %s", Observe(only, world))
	}
	img := image.NewRGBA(src.Rect)
	copy(img.Pix, src.Pix)
	for _, c := range []closeSpot{xLeft, xSub, xBag, xTree, xStash} {
		paintRed(img, c)
		if f := c.redFrac(img); f < redBoxMin {
			t.Fatalf("paint at %v reads %.2f red", c, f)
		}
	}
	if ShopVisible(img) || TradePanelVisible(img) {
		t.Error("red X + dark grid read as a shop (the R9 fallback)")
	}
	if _, _, ok := SubPanelX(img); ok {
		t.Error("red X alone read as a sub-panel")
	}
	for _, h := range []Hints{world, field} {
		r := Observe(img, h)
		if r.Panels.Blocking() || r.Sight.Blocking() {
			t.Errorf("town=%v: %s — %s", h.InTown, r, r.Why(r.Panels))
		}
		if !r.Unsure.Has(LeftPanel | RightPanel) {
			t.Errorf("town=%v: red tiles at the side X spots should leave the halves Unsure: %s", h.InTown, r)
		}
		if a := CloseStep(r); a.Kind != ActNone {
			t.Errorf("town=%v: CloseStep %s on scenery", h.InTown, a)
		}
	}
}

// REACHABILITY rule 1: the town-only panels outside town are Unsure, with the
// detector's evidence kept and the reason added; the rest of the frame stands.
func TestReachabilityTownOnly(t *testing.T) {
	r := Observe(load(t, "shop_open"), field)
	if r.Panels.Has(Shop) || !r.Unsure.Has(Shop) || !r.Sight.Has(Inventory) {
		t.Fatalf("shop outside town: %s", r)
	}
	if ev := r.Evidence[Shop]; !strings.Contains(ev, "vendor red X") || !strings.Contains(ev, "outside town") {
		t.Errorf("shop evidence %q", ev)
	}
	if _, ok := r.Close[Shop]; ok {
		t.Error("an unreachable shop keeps no close button")
	}
	if a := CloseStep(r); a.Panel != Inventory {
		t.Errorf("only the bag is closable: %s", a)
	}
	for name, q := range map[string]Panel{"npc_menu": NPCMenu, "r2/npc_dialog_text": NPCDialog, "r2/stash": Stash} {
		r := Observe(loadAny(t, name, false), field)
		if r.Panels.Has(q) || !r.Unsure.Has(q) {
			t.Errorf("%s outside town: %s", name, r)
		}
		if r := Observe(loadAny(t, name, false), world); !r.Sight.Has(q) {
			t.Errorf("%s in town: %s", name, r)
		}
	}
	// Memory cannot assert a town-only panel outside town either.
	h := field
	h.MenuByte, h.NPCShop = true, true
	if r := Observe(nil, h); r.Panels&TownOnly != 0 {
		t.Errorf("memory-asserted town panel outside town: %s", r)
	}
	// Everything else is reachable anywhere.
	for _, name := range []string{"r2/inventory", "r2/charsheet", "r2/waypoint", "pause_menu", "r2/chat"} {
		if r := Observe(loadAny(t, name, false), field); r.Panels != truth[name] {
			t.Errorf("%s outside town: %s, want %s", name, r, truth[name])
		}
	}
}

// REACHABILITY rule 2: a sub-panel opens from the pause menu only.
func TestTrackerSubPanelReachability(t *testing.T) {
	chron := Observe(load(t, "chronicle"), world)
	pause := Observe(load(t, "pause_menu"), world)
	clear := Observe(load(t, "town_clear"), world)
	tr := NewTracker(0)
	i := 0
	feed := func(r Reading) {
		tr.Update(tick(i), r)
		i++
	}
	feed(clear)
	feed(clear)
	// From the bare world: the sub-panel is unreachable — Unsure, never believed.
	for k := 0; k < 5; k++ {
		p := tr.Plausible(chron)
		if p.Panels.Has(SubPanel) || p.Sight.Has(SubPanel) || !p.Unsure.Has(SubPanel) {
			t.Fatalf("sub-panel from the world: %s", p)
		}
		if !strings.Contains(p.Evidence[SubPanel], "unreachable") {
			t.Errorf("evidence %q", p.Evidence[SubPanel])
		}
		if _, ok := p.Close[SubPanel]; ok {
			t.Error("an unreachable sub-panel keeps its close button")
		}
		feed(chron)
		if tr.State().Panels.Has(SubPanel) {
			t.Fatalf("believed an unreachable sub-panel: %s", tr.State())
		}
	}
	// Plausible never writes into the Reading it was given.
	if !chron.Sight.Has(SubPanel) || strings.Contains(chron.Evidence[SubPanel], "unreachable") {
		t.Fatalf("Plausible mutated its input: %s %q", chron, chron.Evidence[SubPanel])
	}
	// Through the pause menu: Options / Chronicle / Loot Filter are real.
	feed(pause)
	feed(pause)
	if !tr.State().Panels.Has(PauseMenu) {
		t.Fatalf("pause not believed: %s", tr.State())
	}
	if p := tr.Plausible(chron); !p.Sight.Has(SubPanel) {
		t.Fatalf("sub-panel from the pause menu: %s", p)
	}
	feed(chron)
	feed(chron)
	if st := tr.State(); !st.Panels.Has(SubPanel) || st.Panels.Has(PauseMenu) {
		t.Fatalf("pause -> sub-panel: %s", st)
	}
	feed(chron) // a believed sub-panel stays plausible
	if !tr.State().Panels.Has(SubPanel) {
		t.Fatal("believed sub-panel dropped")
	}
	// Closed back to the world: a sub-panel is unreachable again.
	feed(clear)
	feed(clear)
	if p := tr.Plausible(chron); p.Sight.Has(SubPanel) {
		t.Fatalf("sub-panel after closing to the world: %s", p)
	}
}
