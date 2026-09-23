package game

import "image"

// THE PAUSE MENU IS INVISIBLE TO MEMORY (2026-09-23: OpenMenus.QuitMenu and the
// 0xF4 UI byte both read false with the ESC menu on screen). Every blind ESC was
// a coin flip — on an already-closed panel it OPENS the pause menu, and a paused
// game silently ate whole runs (strides gain=0, talk clicks into the Chronicle).
// The screen is the oracle: the six stacked menu buttons have neutral grey stone
// faces where the town is warm sand. Measured on 1920x1050 physical captures:
// 12/12 grey on every pause-menu shot, 0/12 on every clear shot.

// pause-button face sample points on a 1920x1050 capture (two per button).
var pauseSamples = [][2]int{
	{850, 400}, {1070, 400}, {850, 468}, {1070, 468}, {850, 536}, {1070, 536},
	{850, 604}, {1070, 604}, {850, 672}, {1070, 672}, {850, 808}, {1070, 808},
}

// ReturnToGameShot is the "Return to Game" button center on a 1920x1050 capture.
var ReturnToGameShot = [2]int{960, 685}

// scaleShot maps 1920x1050 reference coords into img's space (the menu is
// centered and scales with height).
func scaleShot(img image.Image, x, y int) (int, int) {
	b := img.Bounds()
	k := float64(b.Dy()) / 1050
	cx := float64(b.Dx()) / 2
	return int(cx + (float64(x)-960)*k), int(float64(y) * k)
}

// PauseMenuVisible reports whether the ESC/pause menu is on screen.
func PauseMenuVisible(img image.Image) bool {
	if img == nil || img.Bounds().Dy() < 200 {
		return false
	}
	gray := 0
	for _, p := range pauseSamples {
		x, y := scaleShot(img, p[0], p[1])
		r, g, b, _ := img.At(x, y).RGBA()
		R, G, B := int(r>>8), int(g>>8), int(b>>8)
		mx, mn := R, R
		for _, v := range []int{G, B} {
			if v > mx {
				mx = v
			}
			if v < mn {
				mn = v
			}
		}
		if mx-mn <= 22 && mx >= 60 && mx <= 170 {
			gray++
		}
	}
	return gray >= 11
}

// ReturnToGameAt: the Return-to-Game button in img's pixel space.
func ReturnToGameAt(img image.Image) (int, int) {
	return scaleShot(img, ReturnToGameShot[0], ReturnToGameShot[1])
}

// THE TRADE PANEL IS ALSO A SCREEN FACT (2026-09-23: vendor stock LINGERS in
// memory after the window closes, so "stock readable" passed while the right-click
// cast a skill in town — "Fableboi says: Impossible."). An open vendor panel shows
// its grid of near-black empty cells on the left; sampled cells (cols 5-8, rows
// 6-9 — empty on both the Misc and Armor tabs) measured 15-16/16 dark with the
// shop open, at most 2/16 otherwise. The panel is LEFT-anchored and scales with
// client height.
func TradePanelVisible(img image.Image) bool {
	if img == nil || img.Bounds().Dy() < 200 {
		return false
	}
	k := float64(img.Bounds().Dy()) / 1050
	dark := 0
	for _, col := range []int{5, 6, 7, 8} {
		for _, row := range []int{6, 7, 8, 9} {
			x := int((183 + 47.67*float64(col)) * k)
			y := int((238 + 47.5*float64(row)) * k)
			r, g, b, _ := img.At(x, y).RGBA()
			mx := r
			if g > mx {
				mx = g
			}
			if b > mx {
				mx = b
			}
			if mx>>8 <= 40 {
				dark++
			}
		}
	}
	return dark >= 12
}

// redX: a panel close button's red glyph at a 1920x1050-reference point.
func redX(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	R, G, B := int(r>>8), int(g>>8), int(b>>8)
	return R >= 140 && G <= 90 && B <= 60 && R-G >= 70
}

// UIBlocker identifies a screen that swallows world input and where to click to
// dismiss it — ALWAYS a click, never ESC (ESC toggles the pause menu). Measured
// 2026-09-23 on 1920x1050 captures:
//   - "subpanel": Chronicle / Loot Filter / Options share one centered frame whose
//     red close X sits at (1413,81) — identical color on both, never red elsewhere;
//   - "pause": the ESC menu (grey stone buttons) — dismissed by Return to Game.
// The vendor trade panel is NOT a blocker here (the shop is wanted mid-errand);
// ShopCloseAt closes it deliberately.
func UIBlocker(img image.Image) (kind string, x, y int, ok bool) {
	if img == nil || img.Bounds().Dy() < 200 {
		return "", 0, 0, false
	}
	if cx, cy := scaleShot(img, 1413, 81); redX(img, cx, cy) {
		return "subpanel", cx, cy, true
	}
	if PauseMenuVisible(img) {
		x, y := ReturnToGameAt(img)
		return "pause", x, y, true
	}
	return "", 0, 0, false
}

// ShopOpenX reports the vendor panel's red close X (left-anchored at (657,120)).
func ShopOpenX(img image.Image) (int, int, bool) {
	if img == nil || img.Bounds().Dy() < 200 {
		return 0, 0, false
	}
	k := float64(img.Bounds().Dy()) / 1050
	x, y := int(657*k), int(120*k)
	return x, y, redX(img, x, y)
}

// ShopVisible: the vendor panel is on screen — its red close X (stock-independent)
// OR the dark empty-grid signature. A fully stocked tab (a blacksmith's armor
// wall) fills the sampled cells, so the grid test alone read "closed" on open
// shops (2026-09-23: fence and repair burned 45s each on a shop that WAS open).
func ShopVisible(img image.Image) bool {
	if _, _, ok := ShopOpenX(img); ok {
		return true
	}
	return TradePanelVisible(img)
}
