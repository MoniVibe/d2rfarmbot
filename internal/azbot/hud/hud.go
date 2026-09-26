// Package hud is the world-click no-go map: the screen regions where a click
// meant for the WORLD lands on the game's HUD instead (step 11, "HUD no-click
// zone"). Pure — no game, no motor — so it is measured and tested on Linux.
//
// Two coordinate spaces meet here and every function names the one it takes:
//
//   - PHYS: physical client pixels, the space of a screenshot (1920x1050 on the
//     rig) and of the game's world hit-test. The zones are measured in it.
//   - LOGICAL: the verbs' world-aim space — the koolo projection (19.8/9.9 px per
//     tile about the client centre) in the LOGICAL window a DPI-unaware process
//     sees. game.worldAimClient maps it to PHYS per axis (aim calibration, then
//     the world scale); Aim carries that affine map.
//
// A blind click clamped to "20px from the edge" (the old rule everywhere but
// ClickMove) put a southern target's shift/right-click on the bottom bar — skill
// icons open the skill picker, the belt drinks, and this mod's mini-menu buttons
// (character, inventory, skills, quests, ...) sit on the bar left of the stamina
// orb, ALWAYS visible.
package hud

import "math"

// RefW, RefH: the physical client the zones were measured on — the golden
// screenshots in internal/azbot/screen/testdata (world_town*.jpg,
// world_field_night.jpg, automap_overlay.jpg: all 1920x1050).
const RefW, RefH = 1920, 1050

// EdgePhys: no world click this close to the client edge. worldAimClient clamps
// the cursor 8px inside; a click there aims at a point the caller never chose.
const EdgePhys = 12

// Anchor says how a zone travels to a client that is not RefW x RefH: D2R lays
// the HUD out by height, the bottom panel centred, the portraits in the corner.
type Anchor int

const (
	BottomCenter Anchor = iota
	TopLeft
)

// Zone is one HUD rectangle in reference PHYS px, [X0,X1) x [Y0,Y1).
type Zone struct {
	Name           string
	X0, Y0, X1, Y1 int
	Anchor         Anchor
}

// Zones: measured on the golden screenshots with a 10px grid overlay (zoomed
// crops of the bottom corners and the top-left portrait), then padded 3-8px:
//
//	bottom bar  — frame x 568..1352, top rail y≈952; holds the XP/stamina bars,
//	              the always-visible mini-menu (x 600..840, y 1008..1042), the
//	              left/right skill buttons (860..1060) and the belt (1080..1325).
//	life orb    — angel wing tip x≈275, head top y≈872, orb top y≈880, orb
//	              right edge x≈565.
//	mana orb    — orb left edge x≈1360, orb top y≈880, demon horns top y≈850,
//	              wing right edge x≈1645.
//	merc        — hireling portrait + life bar x 18..82, y 17..105, name below
//	              (clicking it opens the hireling panel).
//	quest log   — the "Quest Log" reminder button that pops after a quest
//	              update (world_field_night.jpg: text y≈748, button 452..507 x
//	              770..825); transient, but a click there opens the quest panel.
//
// The automap overlay's top-right area name (automap_overlay.jpg) is text, not
// a control, and is not a zone.
var Zones = []Zone{
	{Name: "bottom-bar", X0: 555, Y0: 940, X1: 1365, Y1: RefH, Anchor: BottomCenter},
	{Name: "life-orb", X0: 262, Y0: 858, X1: 572, Y1: RefH, Anchor: BottomCenter},
	{Name: "mana-orb", X0: 1348, Y0: 843, X1: 1658, Y1: RefH, Anchor: BottomCenter},
	{Name: "merc-portrait", X0: 0, Y0: 0, X1: 92, Y1: 112, Anchor: TopLeft},
	{Name: "quest-log", X0: 418, Y0: 738, X1: 542, Y1: 832, Anchor: BottomCenter},
}

// scaled returns z in the PHYS px of a w x h client.
func (z Zone) scaled(w, h int) (x0, y0, x1, y1 float64) {
	s := float64(h) / RefH
	switch z.Anchor {
	case TopLeft:
		return float64(z.X0) * s, float64(z.Y0) * s, float64(z.X1) * s, float64(z.Y1) * s
	default: // BottomCenter
		cx := float64(w) / 2
		fh := float64(h)
		return cx + float64(z.X0-RefW/2)*s, fh - float64(RefH-z.Y0)*s,
			cx + float64(z.X1-RefW/2)*s, fh - float64(RefH-z.Y1)*s
	}
}

// ZoneAtPhys names what a click at PHYS (x,y) of a w x h client would hit instead
// of the world: a zone name, "edge" (within EdgePhys of the border or outside),
// or "" when the point is world-clickable.
func ZoneAtPhys(x, y float64, w, h int) string {
	if w <= 0 || h <= 0 {
		w, h = RefW, RefH
	}
	e := EdgePhys * float64(h) / RefH
	if x < e || y < e || x >= float64(w)-e || y >= float64(h)-e {
		return "edge"
	}
	for _, z := range Zones {
		x0, y0, x1, y1 := z.scaled(w, h)
		if x >= x0 && x < x1 && y >= y0 && y < y1 {
			return z.Name
		}
	}
	return ""
}

// SafePhys: a world click at PHYS (x,y) of a w x h client reaches the world.
func SafePhys(x, y float64, w, h int) bool { return ZoneAtPhys(x, y, w, h) == "" }

// ClampPhys pulls PHYS (x,y) back along the ray from (fromX,fromY) — the player —
// until it is world-clickable, so the direction a blind attack or walk click
// carries is preserved. It returns the safe point nearest the target on that
// ray; ok=false when nothing on the ray (short of the origin itself) is safe.
func ClampPhys(x, y, fromX, fromY float64, w, h int) (float64, float64, bool) {
	if SafePhys(x, y, w, h) {
		return x, y, true
	}
	dx, dy := x-fromX, y-fromY
	n := int(math.Ceil(math.Max(math.Abs(dx), math.Abs(dy)))) // 1px steps
	for i := n - 1; i > 0; i-- {
		t := float64(i) / float64(n)
		px, py := fromX+dx*t, fromY+dy*t
		if SafePhys(px, py, w, h) {
			return px, py, true
		}
	}
	return fromX, fromY, false
}

// Aim is the per-axis affine map from the verbs' LOGICAL world-aim px to PHYS px
// (phys = K*logical + B), plus the PHYS client size — game.WorldAimAffine
// supplies the live one.
type Aim struct {
	KX, BX, KY, BY float64
	W, H           int
}

// Scaled is the uncalibrated map: PHYS = LOGICAL * s (s = the display scale).
func Scaled(s float64, logicalW, logicalH int) Aim {
	return Aim{KX: s, KY: s, W: int(float64(logicalW)*s + 0.5), H: int(float64(logicalH)*s + 0.5)}
}

// PhysOfLogical maps a LOGICAL world-aim point to PHYS px.
func (a Aim) PhysOfLogical(x, y int) (float64, float64) {
	return a.KX*float64(x) + a.BX, a.KY*float64(y) + a.BY
}

// ShotOfLogical is PhysOfLogical rounded: the SCREENSHOT px a hardware click
// (motor.RealMenuClick, which takes screenshot px) needs to land where a posted
// world click at LOGICAL (x,y) lands.
func (a Aim) ShotOfLogical(x, y int) (int, int) {
	px, py := a.PhysOfLogical(x, y)
	return int(math.Round(px)), int(math.Round(py))
}

// ZoneAtLogical is ZoneAtPhys for a LOGICAL world-aim point.
func (a Aim) ZoneAtLogical(x, y int) string {
	px, py := a.PhysOfLogical(x, y)
	return ZoneAtPhys(px, py, a.W, a.H)
}

// SafeLogical: a world click at LOGICAL (x,y) reaches the world.
func (a Aim) SafeLogical(x, y int) bool { return a.ZoneAtLogical(x, y) == "" }

// ClampLogical is ClampPhys in LOGICAL px: march from the target back toward
// (fromX,fromY) one logical px at a time and return the first safe point. The
// map is affine, so the logical ray IS the physical ray — the angle the game
// reads (cursor minus player, in PHYS) is kept. ok=false: nothing on the ray
// short of the origin is safe.
func (a Aim) ClampLogical(x, y, fromX, fromY int) (int, int, bool) {
	if a.SafeLogical(x, y) {
		return x, y, true
	}
	dx, dy := x-fromX, y-fromY
	n := dx
	if n < 0 {
		n = -n
	}
	if m := dy; m > n || -m > n {
		if m < 0 {
			m = -m
		}
		n = m
	}
	for i := n - 1; i > 0; i-- {
		px := fromX + int(math.Round(float64(dx*i)/float64(n)))
		py := fromY + int(math.Round(float64(dy*i)/float64(n)))
		if a.SafeLogical(px, py) {
			return px, py, true
		}
	}
	return fromX, fromY, false
}
