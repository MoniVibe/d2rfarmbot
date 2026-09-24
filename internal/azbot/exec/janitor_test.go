package exec

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

var worldOnly = HolderNeeds{Mode: ModeOf(screen.World)}

// seen builds a World reading with p positively seen and close buttons at cl.
func seen(p screen.Panel, cl map[screen.Panel]screen.Point) screen.Reading {
	r := screen.Reading{Mode: screen.World, Panels: p, Sight: p, Evidence: map[screen.Panel]string{}, Close: cl}
	if r.Close == nil {
		r.Close = map[screen.Panel]screen.Point{}
	}
	for _, q := range screen.All {
		if p&q != 0 {
			r.Evidence[q] = "test"
		}
	}
	return r
}

// allStubsUnsure: what every live frame reports today — the stubbed detectors.
const allStubsUnsure = screen.NPCMenu | screen.NPCDialog | screen.Inventory | screen.CharSheet |
	screen.SkillTree | screen.QuestLog | screen.Stash | screen.Waypoint | screen.Chat |
	screen.SkillPicker | screen.Mercenary | screen.Automap

func TestGateUnsureNeverBlocks(t *testing.T) {
	r := seen(0, nil)
	r.Unsure = allStubsUnsure
	if g := Gate(r, worldOnly); !g.Open || g.Acts() {
		t.Fatalf("Unsure must neither block nor act: %+v", g)
	}
	// A memory-asserted shop (no capture) is not a sighting either.
	r.Panels = screen.Shop
	if g := Gate(r, worldOnly); !g.Open {
		t.Fatalf("memory NPCShop is not a positive sighting: %+v", g)
	}
}

func TestGateForeignPanels(t *testing.T) {
	// The owner's bag (generic right half-panel) with its red X.
	r := seen(screen.RightPanel, map[screen.Panel]screen.Point{screen.RightPanel: {X: 1788, Y: 18}})
	g := Gate(r, worldOnly)
	if g.Open || g.Action.Kind != screen.ActClick || g.Action.X != 1788 || g.Foreign != screen.RightPanel {
		t.Fatalf("foreign right panel with X: %+v", g)
	}
	// Without an X: one ESC (seen, ESC-closable, world).
	r = seen(screen.LeftPanel, nil)
	if g := Gate(r, worldOnly); g.Action.Kind != screen.ActKey || g.Action.VK != screen.VKEscape {
		t.Fatalf("foreign left panel: %+v", g)
	}
	// The pause menu: Return to Game, never ESC.
	r = seen(screen.PauseMenu, map[screen.Panel]screen.Point{screen.PauseMenu: {X: 960, Y: 685}})
	if g := Gate(r, worldOnly); g.Action.Kind != screen.ActClick || g.Action.Panel != screen.PauseMenu {
		t.Fatalf("pause: %+v", g)
	}
	// The 0xF4 byte is a proven channel: the NPC menu is foreign without sight.
	r = seen(0, nil)
	r.Panels = screen.NPCMenu
	if g := Gate(r, worldOnly); g.Open || g.Action.Kind != screen.ActKey {
		t.Fatalf("memory-proven NPC menu: %+v", g)
	}
	// The automap eats nothing.
	if g := Gate(seen(screen.Automap, nil), worldOnly); !g.Open {
		t.Fatalf("automap blocked: %+v", g)
	}
}

func TestGateClaims(t *testing.T) {
	vendor := HolderNeeds{Mode: ModeOf(screen.World), Claims: screen.NPCMenu | screen.NPCDialog | screen.Shop |
		screen.Inventory | screen.RightPanel | screen.LeftPanel, CursorOwn: true}
	r := seen(screen.Shop|screen.Inventory, map[screen.Panel]screen.Point{screen.Shop: {X: 657, Y: 120}})
	if g := Gate(r, vendor); !g.Open {
		t.Fatalf("the vendor errand's own shop is not foreign: %+v", g)
	}
	// The same screen after the errand lost the grant: foreign, closed by the X.
	if g := Gate(r, worldOnly); g.Open || g.Action.Kind != screen.ActClick || g.Action.Panel != screen.Shop {
		t.Fatalf("an orphaned shop: %+v", g)
	}
	// Only the unclaimed part is foreign.
	spend := HolderNeeds{Mode: ModeOf(screen.World), Claims: screen.CharSheet | screen.SkillTree | screen.LeftPanel | screen.RightPanel}
	r = seen(screen.LeftPanel|screen.PauseMenu, map[screen.Panel]screen.Point{screen.PauseMenu: {X: 960, Y: 685}})
	if g := Gate(r, spend); g.Foreign != screen.PauseMenu || g.Action.Panel != screen.PauseMenu {
		t.Fatalf("spend + stray pause: %+v", g)
	}
}

// A sanctioned (claimed) pause menu still withholds the ESC for a foreign side
// panel — ESC there would toggle the pause menu.
func TestGateClaimedPauseWithholdsEsc(t *testing.T) {
	relog := HolderNeeds{Mode: AnyMode, Claims: screen.PauseMenu, CursorOwn: true}
	r := seen(screen.PauseMenu|screen.LeftPanel, map[screen.Panel]screen.Point{screen.PauseMenu: {X: 960, Y: 685}})
	g := Gate(r, relog)
	if g.Open || g.Acts() || g.Foreign != screen.LeftPanel {
		t.Fatalf("sanctioned pause + foreign panel: %+v", g)
	}
}

func TestGateMode(t *testing.T) {
	for _, m := range []screen.Mode{screen.Unknown, screen.Loading, screen.Dead} {
		r := seen(0, nil)
		r.Mode = m
		if g := Gate(r, worldOnly); g.Open || g.Acts() {
			t.Errorf("mode %s: world holder must not step: %+v", m, g)
		}
		if g := Gate(r, HolderNeeds{Mode: AnyMode}); !g.Open {
			t.Errorf("mode %s: any-mode holder is gated: %+v", m, g)
		}
	}
	// No ESC off World, even for a seen panel.
	r := seen(screen.LeftPanel, nil)
	r.Mode = screen.Dead
	if g := Gate(r, HolderNeeds{Mode: AnyMode}); g.Acts() || !g.Open {
		t.Fatalf("death screen + unclosable panel: no ESC, and Respawn is not frozen: %+v", g)
	}
	// A seen close button off-world is still clicked.
	r = seen(screen.PauseMenu, map[screen.Panel]screen.Point{screen.PauseMenu: {X: 960, Y: 685}})
	r.Mode = screen.Dead
	if g := Gate(r, HolderNeeds{Mode: AnyMode}); g.Open || g.Action.Kind != screen.ActClick {
		t.Fatalf("death screen + pause menu: %+v", g)
	}
}

func TestGateCursor(t *testing.T) {
	r := seen(0, nil)
	r.CursorItem = true
	g := Gate(r, worldOnly)
	if g.Open || !g.Drop || !g.Cursor || g.Action.Kind == screen.ActKey {
		t.Fatalf("foreign cursor on clear ground: %+v", g)
	}
	if g := Gate(r, HolderNeeds{Mode: AnyMode, CursorOwn: true}); !g.Open {
		t.Fatalf("an owned cursor is not foreign: %+v", g)
	}
	// Over an open bag: left alone — no drop, no ESC, not even for a foreign bag.
	r = seen(screen.RightPanel, map[screen.Panel]screen.Point{screen.RightPanel: {X: 1788, Y: 18}})
	r.CursorItem = true
	if g := Gate(r, worldOnly); g.Open || g.Acts() || !strings.Contains(g.Why, "left") {
		t.Fatalf("cursor over bag: %+v", g)
	}
	// A foreign pause menu goes first; the item waits.
	r = seen(screen.PauseMenu, map[screen.Panel]screen.Point{screen.PauseMenu: {X: 960, Y: 685}})
	r.CursorItem = true
	if g := Gate(r, worldOnly); g.Drop || g.Action.Panel != screen.PauseMenu {
		t.Fatalf("pause + cursor: %+v", g)
	}
}

func TestStable(t *testing.T) {
	raw := seen(screen.RightPanel|screen.LeftPanel, nil)
	raw.CursorItem = true
	s := Seen{State: screen.State{Mode: screen.World, Panels: screen.RightPanel | screen.PauseMenu}, Reading: raw}
	r := Stable(s)
	if r.Mode != screen.World || r.Sight != screen.RightPanel || r.Panels != screen.RightPanel || r.CursorItem {
		t.Fatalf("stable = belief ∩ latest: %+v", r)
	}
	s.State.Mode = screen.Dead
	if Stable(s).Mode != screen.Unknown {
		t.Fatal("belief and frame disagree on mode: Unknown")
	}
}

func TestJanitorRateFreshnessAndWedge(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	r := seen(screen.LeftPanel, nil)
	obs := func(ms int) *Seen {
		return &Seen{At: at(ms), State: screen.State{Mode: screen.World, Panels: screen.LeftPanel}, Reading: r}
	}
	j := NewJanitor()
	if d := j.Decide(at(0), nil, worldOnly); !d.Open {
		t.Fatal("no reading yet: the gate is open")
	}
	d := j.Decide(at(0), obs(0), worldOnly)
	if !d.Act || d.Action.Kind != screen.ActKey {
		t.Fatalf("first action: %+v", d)
	}
	j.Acted(at(150))
	if d := j.Decide(at(300), obs(290), worldOnly); d.Act || d.Wait != "rate" || d.Open {
		t.Fatalf("400ms floor: %+v", d)
	}
	if d := j.Decide(at(600), obs(300), worldOnly); d.Act || d.Wait != "awaiting a fresh reading" {
		t.Fatalf("a reading 150ms after the action does not judge it: %+v", d)
	}
	// Five more unanswered actions (six in all) inside 10s, then the wedge.
	ms := 600
	for i := 0; i < 5; i++ {
		ms += 500
		if d := j.Decide(at(ms), obs(ms), worldOnly); !d.Act {
			t.Fatalf("action %d: %+v", i+2, d)
		}
	}
	ms += 500
	d = j.Decide(at(ms), obs(ms), worldOnly)
	if d.Act || !d.Wedged || !d.NewWedge || d.Gate() != "wedge" {
		t.Fatalf("seventh try must wedge: %+v", d)
	}
	ms += 500
	if d := j.Decide(at(ms), obs(ms), worldOnly); d.Act || !d.Wedged || d.NewWedge || d.Open {
		t.Fatalf("wedged: gate closed, no action, logged once: %+v", d)
	}
	// The wedge rests 60s, then the janitor tries again.
	ms += 61_000
	if d := j.Decide(at(ms), obs(ms), worldOnly); !d.Act {
		t.Fatalf("after the rest: %+v", d)
	}
}

func TestJanitorShrinkResetsWedge(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	j := NewJanitor()
	pts := map[screen.Panel]screen.Point{screen.SubPanel: {X: 1413, Y: 81}, screen.PauseMenu: {X: 960, Y: 685}}
	// Slow but real progress: five tries per layer, each layer peeled shrinks
	// the foreign set and opens a fresh window.
	layers := []screen.Panel{screen.SubPanel | screen.PauseMenu | screen.LeftPanel, screen.PauseMenu | screen.LeftPanel, screen.LeftPanel}
	ms := 0
	for _, p := range layers {
		r := seen(p, pts)
		for i := 0; i < 5; i++ {
			ms += 500
			at := t0.Add(time.Duration(ms) * time.Millisecond)
			d := j.Decide(at, &Seen{At: at, State: screen.State{Mode: screen.World, Panels: p}, Reading: r}, worldOnly)
			if !d.Act || d.Wedged {
				t.Fatalf("%s try %d: %+v", p, i, d)
			}
		}
	}
	// A clear screen opens the gate.
	at := t0.Add(time.Duration(ms+500) * time.Millisecond)
	if d := j.Decide(at, &Seen{At: at, State: screen.State{Mode: screen.World}, Reading: seen(0, nil)}, worldOnly); !d.Open {
		t.Fatalf("clear: %+v", d)
	}
}

// A toggle loop (ESC raises the pause menu, the click drops it, the phantom
// panel stays) never shrinks below its first set: it must wedge.
func TestJanitorToggleLoopWedges(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	j := NewJanitor()
	right := seen(screen.RightPanel, nil)
	pause := seen(screen.PauseMenu, map[screen.Panel]screen.Point{screen.PauseMenu: {X: 960, Y: 685}})
	for i := 0; i < 7; i++ {
		ms := 500 * (i + 1)
		r := right
		if i%2 == 1 {
			r = pause
		}
		at := t0.Add(time.Duration(ms) * time.Millisecond)
		d := j.Decide(at, &Seen{At: at, State: screen.State{Mode: screen.World, Panels: r.Panels}, Reading: r}, worldOnly)
		if i < 6 && !d.Act {
			t.Fatalf("toggle %d: %+v", i, d)
		}
		if i == 6 && !d.NewWedge {
			t.Fatalf("toggle loop not wedged: %+v", d)
		}
	}
}
