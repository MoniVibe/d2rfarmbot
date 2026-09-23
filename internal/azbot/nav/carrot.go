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
