package verbs

import (
	"math"

	"github.com/hectorgimenez/koolo/internal/azbot/hud"
	"github.com/hectorgimenez/koolo/internal/game"
)

// HUD returns the no-click map for the verbs' LOGICAL world-aim space (the koolo
// projection every verb computes bx/by in), built from the live aim calibration.
func HUD(gr *game.MemoryReader) hud.Aim {
	kx, bx, ky, by, w, h := game.WorldAimAffine(gr)
	return hud.Aim{KX: kx, BX: bx, KY: ky, BY: by, W: w, H: h}
}

// ClickableLogical: a LOGICAL world-aim point a verb may hover or click — inside
// the historical 20px frame AND off the HUD (bottom bar with its mini-menu, skill
// buttons and belt; both orbs; merc portrait; quest-log button).
func ClickableLogical(gr *game.MemoryReader, x, y int) bool {
	return clickable(gr, HUD(gr), x, y)
}

func clickable(gr *game.MemoryReader, a hud.Aim, x, y int) bool {
	if x < 20 || y < 20 || x > gr.GameAreaSizeX-20 || y > gr.GameAreaSizeY-20 {
		return false
	}
	return a.SafeLogical(x, y)
}

// ClampClickLogical pulls a LOGICAL world-aim point back along the ray from the
// player (the projection's centre) until ClickableLogical holds — for blind
// clicks whose DIRECTION is the payload (volleys, door pushes, corpse clicks).
// ok=false: no clickable point on the ray short of the player.
func ClampClickLogical(gr *game.MemoryReader, x, y int) (int, int, bool) {
	a := HUD(gr)
	if clickable(gr, a, x, y) {
		return x, y, true
	}
	fx, fy := gr.GameAreaSizeX/2, gr.GameAreaSizeY/2
	dx, dy := x-fx, y-fy
	n := max(dx, -dx, dy, -dy)
	for i := n - 1; i > 0; i-- {
		px := fx + int(math.Round(float64(dx*i)/float64(n)))
		py := fy + int(math.Round(float64(dy*i)/float64(n)))
		if clickable(gr, a, px, py) {
			return px, py, true
		}
	}
	return fx, fy, false
}

// ShotOfLogical converts a LOGICAL world-aim point to the SCREENSHOT px that
// motor.RealMenuClick takes — the hardware click lands where the posted click
// at (x,y) would. Passing logical px straight to RealMenuClick divides them by
// the display scale a second time (the arch bursts landed at ~80% of the aim).
func ShotOfLogical(gr *game.MemoryReader, x, y int) (int, int) {
	return HUD(gr).ShotOfLogical(x, y)
}
