package nav

import "math"

// Iso projection (koolo's px per tile about the player) and the carrot box: the
// half-extents of the screen region a force-move cursor may sit in.
const (
	IsoX    = 19.8
	IsoY    = 9.9
	CarrotX = 300.0
	CarrotY = 140.0
)

// ScreenCarrot projects a world delta to a screen offset (from the player) that
// preserves the TRUE screen angle and always lands inside the rx×ry box. The old
// carrot placed the cursor at (rx·cos, ry·sin) — an ellipse, which bent the
// heading up to ~20° along the world axes, exactly where corridors run. A uniform
// radius r = min(rx/|cos|, ry/|sin|) keeps the angle. Zero delta → (0,0).
func ScreenCarrot(dx, dy float64, isoX, isoY, rx, ry float64) (int, int) {
	sx := (dx - dy) * isoX
	sy := (dx + dy) * isoY
	l := math.Hypot(sx, sy)
	if l < 1e-9 {
		return 0, 0
	}
	c, s := sx/l, sy/l
	r := math.Inf(1)
	if math.Abs(c) > 1e-9 {
		r = rx / math.Abs(c)
	}
	if math.Abs(s) > 1e-9 {
		r = math.Min(r, ry/math.Abs(s))
	}
	return int(math.Round(r * c)), int(math.Round(r * s))
}

// ScreenCarrotReach is ScreenCarrot with the cursor radius capped at reach px
// (0 = the box edge). D2R force-move walks toward the cursor POINT, not just its
// direction: R6 measured ~7 tiles of travel from an 80ms tap with the carrot on the
// box edge (~15 tiles out), so a short tap cannot mean a short step unless the
// cursor comes in. Angle is preserved either way.
func ScreenCarrotReach(dx, dy float64, isoX, isoY, rx, ry, reach float64) (int, int) {
	ox, oy := ScreenCarrot(dx, dy, isoX, isoY, rx, ry)
	if reach <= 0 {
		return ox, oy
	}
	l := math.Hypot(float64(ox), float64(oy))
	if l <= reach || l < 1e-9 {
		return ox, oy
	}
	k := reach / l
	return int(math.Round(float64(ox) * k)), int(math.Round(float64(oy) * k))
}
