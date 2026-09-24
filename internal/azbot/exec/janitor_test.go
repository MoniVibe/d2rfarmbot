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
	// Relay R10: only a KNOWN-junk item may be dropped, and only in the field.
	field := worldOnly
	field.CursorJunk = true
	g := Gate(r, field)
	if g.Open || !g.Drop || !g.Cursor || g.Action.Kind == screen.ActKey {
		t.Fatalf("foreign known-junk cursor on clear field ground: %+v", g)
	}
	for name, n := range map[string]HolderNeeds{
		"field, not known junk": worldOnly,
		"town, known junk":      {Mode: ModeOf(screen.World), Town: true, CursorJunk: true},
		"town":                  {Mode: ModeOf(screen.World), Town: true},
	} {
		g := Gate(r, n)
		if g.Open || g.Drop || g.Acts() || !g.CursorHold || !g.Cursor || !strings.Contains(g.Why, "HELD") {
			t.Fatalf("%s: a cursor item must be HELD, never dropped: %+v", name, g)
		}
	}
	if g := Gate(r, HolderNeeds{Mode: AnyMode, CursorOwn: true}); !g.Open {
		t.Fatalf("an owned cursor is not foreign: %+v", g)
	}
	// Over the SEEN bag: parked in a free cell (town or field) — no drop, no ESC.
	for _, town := range []bool{true, false} {
		r = seen(screen.Inventory, map[screen.Panel]screen.Point{screen.Inventory: {X: 1788, Y: 18}})
		r.CursorItem = true
		g := Gate(r, HolderNeeds{Mode: ModeOf(screen.World), Town: town})
		if g.Open || !g.Park || g.Drop || g.Action.Kind != screen.ActNone || !g.Acts() {
			t.Fatalf("cursor over the bag (town=%v): %+v", town, g)
		}
	}
	// Over a half panel that is not the bag: left alone — no park, no drop, no ESC.
	r = seen(screen.RightPanel, map[screen.Panel]screen.Point{screen.RightPanel: {X: 1788, Y: 18}})
	r.CursorItem = true
	if g := Gate(r, worldOnly); g.Open || g.Acts() || !strings.Contains(g.Why, "left") {
		t.Fatalf("cursor over a half panel: %+v", g)
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

var jt0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

// ms: the fake clock.
func ms(n int) time.Time { return jt0.Add(time.Duration(n) * time.Millisecond) }

// obsAt: a reading published at ms n whose panels are also believed.
func obsAt(n int, r screen.Reading) *Seen {
	return &Seen{At: ms(n), State: screen.State{Mode: screen.World, Panels: r.Panels}, Reading: r}
}

var (
	rtg      = map[screen.Panel]screen.Point{screen.PauseMenu: {X: 960, Y: 685}}
	subX     = map[screen.Panel]screen.Point{screen.SubPanel: {X: 1413, Y: 81}}
	pauseAt  = func() screen.Reading { return seen(screen.PauseMenu, rtg) }
	survival = HolderNeeds{Mode: ModeOf(screen.World), Survival: true}
)

func TestJanitorRateFreshnessAndWedge(t *testing.T) {
	r := seen(screen.LeftPanel, nil)
	j := NewJanitor()
	if d := j.Decide(ms(0), nil, worldOnly); !d.Open {
		t.Fatal("no reading yet: the gate is open")
	}
	// One reading is a rumor: no ESC yet (JanitorEscFrames).
	if d := j.Decide(ms(0), obsAt(0, r), worldOnly); d.Act || d.Open || !strings.Contains(d.Wait, "esc withheld") {
		t.Fatalf("ESC on one reading: %+v", d)
	}
	d := j.Decide(ms(100), obsAt(100, r), worldOnly)
	if !d.Act || d.Action.Kind != screen.ActKey {
		t.Fatalf("first action: %+v", d)
	}
	j.Acted(ms(150))
	if d := j.Decide(ms(300), obsAt(290, r), worldOnly); d.Act || d.Wait != "rate" || d.Open {
		t.Fatalf("400ms floor: %+v", d)
	}
	if d := j.Decide(ms(600), obsAt(300, r), worldOnly); d.Act || d.Wait != "awaiting a fresh reading" {
		t.Fatalf("a reading 150ms after the action does not judge it: %+v", d)
	}
	// Five more unanswered actions (six in all) inside 10s, then the wedge.
	n := 600
	for i := 0; i < 5; i++ {
		n += 500
		if d := j.Decide(ms(n), obsAt(n, r), worldOnly); !d.Act {
			t.Fatalf("action %d: %+v", i+2, d)
		}
	}
	n += 500
	d = j.Decide(ms(n), obsAt(n, r), worldOnly)
	if d.Act || !d.Wedged || !d.NewWedge || d.Gate() != "wedge" {
		t.Fatalf("seventh try must wedge: %+v", d)
	}
	// Wedged: no action, logged once — and the holder is NOT held on a side
	// panel (the wedge gates on the pause menu only).
	n += 500
	if d := j.Decide(ms(n), obsAt(n, r), worldOnly); d.Act || !d.Wedged || d.NewWedge || !d.Open {
		t.Fatalf("wedged on a side panel: open, no action: %+v", d)
	}
	n += 2000 // well past the last ESC's phantom window (this pause is the owner's)
	if d := j.Decide(ms(n), obsAt(n, pauseAt()), worldOnly); d.Act || !d.Wedged || d.Open || d.Gate() != "wedge" {
		t.Fatalf("wedged on the pause menu: held, no action: %+v", d)
	}
	// The wedge rests 60s, then the janitor tries again.
	n += 61_000
	j.Decide(ms(n), obsAt(n, r), worldOnly)
	n += 100
	if d := j.Decide(ms(n), obsAt(n, r), worldOnly); !d.Act {
		t.Fatalf("after the rest: %+v", d)
	}
}

// Real progress — every action peels a layer — is not a wedge.
func TestJanitorProgressIsNotAWedge(t *testing.T) {
	j := NewJanitor()
	pts := map[screen.Panel]screen.Point{screen.SubPanel: {X: 1413, Y: 81}, screen.PauseMenu: {X: 960, Y: 685}}
	layers := []screen.Panel{screen.SubPanel | screen.PauseMenu | screen.LeftPanel, screen.PauseMenu | screen.LeftPanel, screen.LeftPanel}
	n := 0
	for _, p := range layers {
		n += 500
		d := j.Decide(ms(n), obsAt(n, seen(p, pts)), worldOnly)
		if !d.Act || d.Wedged || d.Phantom != "" {
			t.Fatalf("%s: %+v", p, d)
		}
	}
	n += 500
	if d := j.Decide(ms(n), obsAt(n, seen(0, nil)), worldOnly); !d.Open || d.Phantom != "" {
		t.Fatalf("clear: %+v", d)
	}
	if q := j.Quarantined(ms(n)); q != 0 {
		t.Fatalf("real panels quarantined: %s", q)
	}
}

// THE R9 LOOP: a phantom sub-panel appears, is clicked away, the gate opens,
// it appears again — the foreign set never shrinks below its first and the
// gate keeps reopening, so the 6-in-10s wedge never trips. The loop breaker
// counts every action: the ninth inside 30s is a ui_wedge.
func TestJanitorLoopBreakerR9(t *testing.T) {
	j := NewJanitor()
	sub := seen(screen.SubPanel, subX)
	acts := 0
	n := 0
	for i := 0; i < 9; i++ {
		n += 1000
		d := j.Decide(ms(n), obsAt(n, sub), worldOnly)
		if i < 8 {
			if !d.Act || d.Action.Panel != screen.SubPanel {
				t.Fatalf("click %d: %+v", i+1, d)
			}
			acts++
			j.Acted(ms(n + 10))
			// The click "worked": the next reading is clear and the gate opens.
			if d := j.Decide(ms(n+500), obsAt(n+500, seen(0, nil)), worldOnly); !d.Open {
				t.Fatalf("clear after click %d: %+v", i+1, d)
			}
			continue
		}
		if d.Act || !d.NewWedge || !strings.Contains(d.Why, "loop breaker") || !d.Open {
			t.Fatalf("ninth action in 30s must wedge (open: no pause): %+v", d)
		}
	}
	if acts != JanitorLoopN {
		t.Fatalf("acted %d times", acts)
	}
	// Wedged: the phantom holds nobody, the pause menu does, nothing is pressed.
	n += 1000
	if d := j.Decide(ms(n), obsAt(n, sub), worldOnly); d.Act || !d.Open || !d.Wedged {
		t.Fatalf("wedged + sub-panel: %+v", d)
	}
	n += 1000
	if d := j.Decide(ms(n), obsAt(n, pauseAt()), worldOnly); d.Act || d.Open {
		t.Fatalf("wedged + pause: %+v", d)
	}
}

// ESC needs the target positively seen in two consecutive readings; one
// reading counted once however many ticks decide on it; a gap resets.
func TestJanitorEscNeedsTwoReadings(t *testing.T) {
	j := NewJanitor()
	bag := seen(screen.Inventory, nil) // no X seen: ESC is the only close
	s := obsAt(0, bag)
	for k := 0; k < 3; k++ {
		if d := j.Decide(ms(40*k), s, worldOnly); d.Act {
			t.Fatalf("the same reading decided %d times sent ESC: %+v", k+1, d)
		}
	}
	gap := seen(0, nil)
	gap.Unsure = screen.Inventory
	j.Decide(ms(200), obsAt(200, gap), worldOnly)
	if d := j.Decide(ms(400), obsAt(400, bag), worldOnly); d.Act {
		t.Fatalf("a gap resets the streak: %+v", d)
	}
	if d := j.Decide(ms(600), obsAt(600, bag), worldOnly); !d.Act || d.Action.Kind != screen.ActKey {
		t.Fatalf("two consecutive readings: %+v", d)
	}
}

// THE R9 ESC: a phantom shop (no X: the old grid fallback) earns an ESC, the
// ESC raises the pause menu. The janitor convicts the shop detector, clicks
// Return to Game once, and ignores the shop for five minutes.
func TestJanitorPhantomEscR9(t *testing.T) {
	for _, n := range []HolderNeeds{worldOnly, survival} {
		j := NewJanitor()
		shop := seen(screen.Shop, nil)
		if n.Survival {
			// A survival holder is never acted for off a pause menu, so the
			// ESC comes from an earlier holder; the verdict still lands.
			j.Decide(ms(0), obsAt(0, shop), worldOnly)
			if d := j.Decide(ms(100), obsAt(100, shop), worldOnly); !d.Act {
				t.Fatalf("setup ESC: %+v", d)
			}
		} else {
			j.Decide(ms(0), obsAt(0, shop), n)
			if d := j.Decide(ms(100), obsAt(100, shop), n); !d.Act || d.Action.Kind != screen.ActKey {
				t.Fatalf("ESC: %+v", d)
			}
		}
		j.Acted(ms(120))
		d := j.Decide(ms(600), obsAt(400, pauseAt()), n)
		want := "phantom: shop — ESC raised pause; quarantined 5m"
		if !d.Act || d.Action.Kind != screen.ActClick || d.Action.X != 960 || d.Action.Y != 685 || d.Why != want {
			t.Fatalf("survival=%v: Return to Game on the phantom's pause: %+v", n.Survival, d)
		}
		if q := j.Quarantined(ms(600)); q != screen.Shop {
			t.Fatalf("quarantine %s", q)
		}
		j.Acted(ms(620))
		// The phantom again: Unsure now — neither held nor acted on.
		for k := 0; k < 3; k++ {
			at := 2000 + 500*k
			if d := j.Decide(ms(at), obsAt(at, shop), n); !d.Open || d.Act {
				t.Fatalf("quarantined shop: %+v", d)
			}
		}
		// Five minutes on, the detector is trusted again.
		j.Decide(ms(301_000), obsAt(301_000, shop), worldOnly)
		if d := j.Decide(ms(301_500), obsAt(301_500, shop), worldOnly); d.Open || !d.Act {
			t.Fatalf("after the quarantine: %+v", d)
		}
	}
}

// An ESC that closes a real panel raises nothing: no conviction.
func TestJanitorRealEscNoPhantom(t *testing.T) {
	j := NewJanitor()
	bag := seen(screen.Inventory, nil)
	j.Decide(ms(0), obsAt(0, bag), worldOnly)
	j.Decide(ms(100), obsAt(100, bag), worldOnly)
	j.Acted(ms(120))
	for _, at := range []int{600, 1000, 2000} {
		if d := j.Decide(ms(at), obsAt(at, seen(0, nil)), worldOnly); !d.Open || d.Phantom != "" {
			t.Fatalf("clear after a real ESC: %+v", d)
		}
	}
	// A pause menu long after the ESC (the owner's) convicts nobody.
	if d := j.Decide(ms(4000), obsAt(4000, pauseAt()), worldOnly); d.Phantom != "" || j.Quarantined(ms(4000)) != 0 {
		t.Fatalf("late pause: %+v", d)
	}
}

// A close click after which the panel still stands and nothing else changed:
// the X was never there. A click that closes convicts nobody.
func TestJanitorPhantomClick(t *testing.T) {
	j := NewJanitor()
	sub := seen(screen.SubPanel, subX)
	if d := j.Decide(ms(0), obsAt(0, sub), worldOnly); !d.Act || d.Action.Kind != screen.ActClick {
		t.Fatalf("click: %+v", d)
	}
	j.Acted(ms(50))
	d := j.Decide(ms(500), obsAt(500, sub), worldOnly)
	if d.Phantom != "phantom: subpanel — close click changed nothing; quarantined 5m" || !d.Open || d.Act {
		t.Fatalf("phantom click: %+v", d)
	}
	if j.Quarantined(ms(500)) != screen.SubPanel {
		t.Fatal("not quarantined")
	}

	j = NewJanitor()
	j.Decide(ms(0), obsAt(0, sub), worldOnly)
	j.Acted(ms(50))
	if d := j.Decide(ms(500), obsAt(500, seen(0, nil)), worldOnly); d.Phantom != "" || !d.Open {
		t.Fatalf("a click that closed: %+v", d)
	}
	// Something else changed (a pause menu surfaced under it): not convicted.
	j = NewJanitor()
	both := seen(screen.SubPanel|screen.LeftPanel, subX)
	j.Decide(ms(0), obsAt(0, both), worldOnly)
	j.Acted(ms(50))
	if d := j.Decide(ms(500), obsAt(500, seen(screen.SubPanel, subX)), worldOnly); d.Phantom != "" {
		t.Fatalf("the screen changed: %+v", d)
	}
}

// A survival holder is never held or acted for, unless the game is paused.
func TestJanitorSurvivalStandsDown(t *testing.T) {
	j := NewJanitor()
	for k, r := range []screen.Reading{seen(screen.Shop, nil), seen(screen.SubPanel, subX), seen(screen.RightPanel, nil)} {
		at := 1000 * k
		j.Decide(ms(at), obsAt(at, r), survival)
		if d := j.Decide(ms(at+100), obsAt(at+100, r), survival); !d.Open || d.Act || d.Wait != "survival" {
			t.Fatalf("%s: %+v", r.Panels, d)
		}
	}
	cur := seen(0, nil)
	cur.CursorItem = true
	if d := j.Decide(ms(5000), obsAt(5000, cur), survival); !d.Open || d.Act {
		t.Fatalf("cursor: %+v", d)
	}
	// The pause menu: the game is frozen, clicking cannot hurt the fight.
	if d := j.Decide(ms(6000), obsAt(6000, pauseAt()), survival); d.Open || !d.Act || d.Action.Panel != screen.PauseMenu {
		t.Fatalf("pause under a survival holder: %+v", d)
	}
}

// A sub-panel ruled Unsure (unreachable, quarantined) withholds every ESC:
// were it real, the ESC would land on it.
func TestGateUnsureSubPanelWithholdsEsc(t *testing.T) {
	r := seen(screen.Inventory, nil)
	if g := Gate(r, worldOnly); g.Action.Kind != screen.ActKey {
		t.Fatalf("baseline ESC: %+v", g)
	}
	r.Unsure = screen.SubPanel
	r.Evidence[screen.SubPanel] = "sub-panel red X — no pause menu before it: unreachable"
	if g := Gate(r, worldOnly); g.Acts() || g.Open || !strings.Contains(g.Action.Reason, "sub-panel reads Unsure") {
		t.Fatalf("ESC under an Unsure sub-panel: %+v", g)
	}
	// A click on a seen X is still fine (it lands on its own button).
	r = seen(screen.Inventory, map[screen.Panel]screen.Point{screen.Inventory: {X: 1788, Y: 18}})
	r.Unsure = screen.SubPanel
	if g := Gate(r, worldOnly); g.Action.Kind != screen.ActClick {
		t.Fatalf("click under an Unsure sub-panel: %+v", g)
	}
}

// Relay R10, 09:20:43, tick 13841: the watchdog benched Equip mid-Dress with
// an item on the cursor, Unstick took the grant, and the gate answered
// "foreign cursor item: drop at feet" — the item hit the Lut Gholein floor.
// The same tick now HOLDS Unstick (no action at all), and a ui_wedge never
// lets the item loose either; a survival holder is still never delayed.
func TestJanitorNeverDropsInTown(t *testing.T) {
	cur := seen(0, nil)
	cur.CursorItem = true
	obs := func(n int) *Seen {
		return &Seen{At: ms(n), State: screen.State{Mode: screen.World, CursorItem: true}, Reading: cur}
	}
	unstickTown := HolderNeeds{Mode: ModeOf(screen.World), Town: true} // needsWorld: CursorEmpty
	j := NewJanitor()
	for n := 0; n < 20000; n += 500 {
		d := j.Decide(ms(n), obs(n), unstickTown)
		if d.Act || d.Drop || d.Open || !d.CursorHold {
			t.Fatalf("at %dms: %+v", n, d)
		}
	}
	// Wedged (the parker spent its actions): still held.
	j.wedgeUntil = ms(100000)
	if d := j.Decide(ms(21000), obs(21000), unstickTown); d.Open || d.Act || !d.Wedged {
		t.Fatalf("wedged with a cursor item: %+v", d)
	}
	sv := unstickTown
	sv.Survival = true
	if d := j.Decide(ms(21500), obs(21500), sv); !d.Open || d.Act {
		t.Fatalf("wedged survival holder delayed: %+v", d)
	}
	// Control: the field drop still happens for known junk.
	field := HolderNeeds{Mode: ModeOf(screen.World), CursorJunk: true}
	if d := NewJanitor().Decide(ms(0), obs(0), field); !d.Act || !d.Drop {
		t.Fatalf("known junk in the field: %+v", d)
	}
}
