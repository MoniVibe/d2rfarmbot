package hud

import (
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The zones were measured on the golden world screenshots; they must still be
// the reference size.
func TestReferenceMatchesGoldenScreenshots(t *testing.T) {
	for _, f := range []string{"world_town.jpg", "world_field_night.jpg", "automap_overlay.jpg"} {
		fh, err := os.Open(filepath.Join("..", "screen", "testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := jpeg.DecodeConfig(fh)
		fh.Close()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Width != RefW || cfg.Height != RefH {
			t.Fatalf("%s is %dx%d, zones measured on %dx%d", f, cfg.Width, cfg.Height, RefW, RefH)
		}
	}
}

func TestHUDPointsRejected(t *testing.T) {
	cases := []struct {
		name string
		x, y float64
		zone string
	}{
		{"bar centre (skill buttons)", 960, 1010, "bottom-bar"},
		{"bar top rail", 700, 955, "bottom-bar"},
		{"mini-menu: character button", 617, 1024, "bottom-bar"},
		{"mini-menu: last button", 824, 1024, "bottom-bar"},
		{"stamina bar", 730, 996, "bottom-bar"},
		{"right skill", 1030, 1010, "bottom-bar"},
		{"belt slot 1", 1110, 1015, "bottom-bar"},
		{"belt slot 4", 1290, 1015, "bottom-bar"},
		{"life orb", 480, 960, "life-orb"},
		{"life orb top", 490, 885, "life-orb"},
		{"angel head", 365, 880, "life-orb"},
		{"angel wing", 285, 990, "life-orb"},
		{"mana orb", 1460, 950, "mana-orb"},
		{"demon horns", 1550, 860, "mana-orb"},
		{"demon wing", 1640, 1000, "mana-orb"},
		{"merc portrait", 50, 60, "merc-portrait"},
		{"quest log button", 480, 800, "quest-log"},
		{"bottom edge", 150, 1045, "edge"},
		{"left edge", 3, 500, "edge"},
		{"off screen", 2000, 500, "edge"},
	}
	for _, c := range cases {
		if got := ZoneAtPhys(c.x, c.y, RefW, RefH); got != c.zone {
			t.Errorf("%s (%v,%v): zone %q, want %q", c.name, c.x, c.y, got, c.zone)
		}
		if SafePhys(c.x, c.y, RefW, RefH) {
			t.Errorf("%s (%v,%v) called safe", c.name, c.x, c.y)
		}
	}
}

func TestWorldPointsSafe(t *testing.T) {
	for _, p := range [][2]float64{
		{960, 490},   // the player
		{960, 930},   // ground just above the bar
		{120, 1000},  // ground left of the angel
		{1800, 1010}, // ground right of the demon
		{560, 850},   // above the orb's shoulder
		{1900, 20},   // top-right: automap area name is text, not a control
		{400, 600},
	} {
		if z := ZoneAtPhys(p[0], p[1], RefW, RefH); z != "" {
			t.Errorf("(%v,%v): zone %q, want world", p[0], p[1], z)
		}
	}
}

// A 2560x1400 client: bottom zones travel with the centred panel, scaled by height.
func TestZonesScaleWithClient(t *testing.T) {
	s := 1400.0 / RefH
	x := 2560/2 + (960-960)*s
	y := 1400 - (1050-1010)*s
	if z := ZoneAtPhys(x, y, 2560, 1400); z != "bottom-bar" {
		t.Fatalf("scaled bar centre: %q", z)
	}
	// The merc portrait stays in the corner, scaled.
	if z := ZoneAtPhys(50*s, 60*s, 2560, 1400); z != "merc-portrait" {
		t.Fatalf("scaled portrait: %q", z)
	}
	// Ground right of the (centred, un-widened) HUD cluster is world.
	if z := ZoneAtPhys(2400, 1350, 2560, 1400); z != "" {
		t.Fatalf("scaled bottom-right ground: %q", z)
	}
}

func cross(ax, ay, bx, by float64) float64 { return ax*by - ay*bx }

func TestClampPhysKeepsAngleAndLandsSafe(t *testing.T) {
	fromX, fromY := 960.0, 490.0
	for _, tg := range [][2]float64{
		{960, 1040},  // straight south: the old 20px clamp put this on the skill buttons
		{700, 1030},  // south-west onto the mini-menu
		{1200, 1040}, // south-east onto the belt
		{470, 1000},  // the life orb
		{1500, 950},  // the mana orb
		{30, 40},     // the merc portrait
		{480, 800},   // the quest log button
		{2100, 1200}, // far off screen
	} {
		x, y, ok := ClampPhys(tg[0], tg[1], fromX, fromY, RefW, RefH)
		if !ok {
			t.Errorf("target %v: no safe point", tg)
			continue
		}
		if !SafePhys(x, y, RefW, RefH) {
			t.Errorf("target %v: clamped to unsafe (%v,%v) zone %q", tg, x, y, ZoneAtPhys(x, y, RefW, RefH))
		}
		dx, dy := tg[0]-fromX, tg[1]-fromY
		cx, cy := x-fromX, y-fromY
		if c := cross(dx, dy, cx, cy) / math.Hypot(dx, dy); math.Abs(c) > 1 {
			t.Errorf("target %v: clamp (%v,%v) left the ray by %.1fpx", tg, x, y, c)
		}
		if cx*dx+cy*dy <= 0 {
			t.Errorf("target %v: clamp (%v,%v) reversed direction", tg, x, y)
		}
		if math.Hypot(cx, cy) >= math.Hypot(dx, dy) {
			t.Errorf("target %v: clamp did not pull back", tg)
		}
		// It pulls back only as far as needed: one more px outward is unsafe.
		n := math.Hypot(cx, cy)
		ox, oy := fromX+cx*(n+2)/n, fromY+cy*(n+2)/n
		if SafePhys(ox, oy, RefW, RefH) && ZoneAtPhys(tg[0], tg[1], RefW, RefH) != "quest-log" {
			t.Errorf("target %v: clamp (%v,%v) pulled back further than needed", tg, x, y)
		}
	}
	// A safe target is untouched.
	if x, y, ok := ClampPhys(1300, 700, fromX, fromY, RefW, RefH); !ok || x != 1300 || y != 700 {
		t.Fatalf("safe target moved to (%v,%v)", x, y)
	}
}

// The rig: 1536x840 logical at 125%, aimcal KX=KY=1.11, OY=-27.5 (the player
// sits above centre) — the map game.WorldAimAffine builds.
func rigAim() Aim {
	const s, k, oy = 1.25, 1.11, -27.5
	cx, cy := 768.0, 420.0
	return Aim{KX: k * s, BX: cx * (1 - k) * s, KY: k * s, BY: (cy*(1-k) + oy) * s, W: 1920, H: 1050}
}

func TestLogicalOldEdgeClampLandsOnBar(t *testing.T) {
	a := rigAim()
	// volleyAt's old clamp: a target due south became (768, 840-20).
	if a.SafeLogical(768, 820) {
		t.Fatal("logical (768,820) must be HUD/edge on the rig")
	}
	if z := a.ZoneAtLogical(768, 790); z != "bottom-bar" {
		t.Fatalf("logical (768,790): %q, want bottom-bar", z)
	}
	if !a.SafeLogical(768, 420) {
		t.Fatal("the player's own point must be safe")
	}
}

func TestClampLogicalKeepsAngle(t *testing.T) {
	a := rigAim()
	fx, fy := 768, 420
	for _, tg := range [][2]int{{768, 820}, {560, 830}, {1000, 835}, {380, 800}, {1160, 800}, {10, 10}} {
		x, y, ok := a.ClampLogical(tg[0], tg[1], fx, fy)
		if !ok || !a.SafeLogical(x, y) {
			t.Errorf("target %v: (%d,%d) ok=%v zone=%q", tg, x, y, ok, a.ZoneAtLogical(x, y))
			continue
		}
		dx, dy := float64(tg[0]-fx), float64(tg[1]-fy)
		cx, cy := float64(x-fx), float64(y-fy)
		if c := cross(dx, dy, cx, cy) / math.Hypot(dx, dy); math.Abs(c) > 1 {
			t.Errorf("target %v: clamp (%d,%d) off the ray by %.1fpx", tg, x, y, c)
		}
		if cx*dx+cy*dy <= 0 || math.Hypot(cx, cy) > math.Hypot(dx, dy) {
			t.Errorf("target %v: clamp (%d,%d) not on the segment", tg, x, y)
		}
	}
	// Degenerate: the origin itself on the HUD — nothing to offer.
	if _, _, ok := a.ClampLogical(768, 830, 768, 829); ok {
		t.Fatal("a ray inside the bar has no safe point")
	}
}

func TestShotOfLogical(t *testing.T) {
	// Uncalibrated 125%: shot = logical * 1.25 — NOT logical (RealMenuClick divides
	// by the display scale, so passing logical px landed at 80% of the aim).
	a := Scaled(1.25, 1536, 840)
	if x, y := a.ShotOfLogical(768, 420); x != 960 || y != 525 {
		t.Fatalf("shot of centre = (%d,%d), want (960,525)", x, y)
	}
	if a.W != 1920 || a.H != 1050 {
		t.Fatalf("size %dx%d", a.W, a.H)
	}
	r := rigAim()
	if x, y := r.ShotOfLogical(768, 420); x != 960 || y != 491 {
		t.Fatalf("rig shot of centre = (%d,%d), want (960,491)", x, y)
	}
}
