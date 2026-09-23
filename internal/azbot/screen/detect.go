package screen

import "image"

// THE PAUSE MENU IS INVISIBLE TO MEMORY (2026-09-23: OpenMenus.QuitMenu and the
// 0xF4 UI byte both read false with the ESC menu on screen). Every blind ESC was
// a coin flip — on an already-closed panel it OPENS the pause menu, and a paused
// game silently ate whole runs (strides gain=0, talk clicks into the Chronicle).
// The screen is the oracle: the six stacked menu buttons have neutral grey stone
// faces where the town is warm sand. Measured on 1920x1050 physical captures:
// 12/12 grey on every pause-menu shot, 0/12 on every clear shot.

// RefW, RefH: the reference capture size every coordinate here was measured at.
const (
	RefW = 1920
	RefH = 1050
)

// pause-button face sample points on a 1920x1050 capture (two per button).
var pauseSamples = [][2]int{
	{850, 400}, {1070, 400}, {850, 468}, {1070, 468}, {850, 536}, {1070, 536},
	{850, 604}, {1070, 604}, {850, 672}, {1070, 672}, {850, 808}, {1070, 808},
}

// ReturnToGameShot is the "Return to Game" button center on a 1920x1050 capture.
var ReturnToGameShot = [2]int{960, 685}

// usable: a capture big enough to hold the measured UI.
func usable(img image.Image) bool { return img != nil && img.Bounds().Dy() >= 200 }

// ScaleShot maps 1920x1050 reference coords into img's space (the menu is
// centered and scales with height).
func ScaleShot(img image.Image, x, y int) (int, int) {
	b := img.Bounds()
	k := float64(b.Dy()) / RefH
	cx := float64(b.Dx()) / 2
	return int(cx + (float64(x)-960)*k), int(float64(y) * k)
}

// fromLeft / fromRight map reference coords for edge-anchored panels. The side
// panels sit symmetric on the reference shot (vendor frame starts x=118, bag
// frame ends 1920-1805=115), so each is taken to hug its own edge.
func fromLeft(img image.Image, x, y int) (int, int) {
	k := float64(img.Bounds().Dy()) / RefH
	return int(float64(x) * k), int(float64(y) * k)
}

func fromRight(img image.Image, x, y int) (int, int) {
	b := img.Bounds()
	k := float64(b.Dy()) / RefH
	return b.Dx() - int(float64(RefW-x)*k), int(float64(y) * k)
}

func rgb(img image.Image, x, y int) (int, int, int) {
	r, g, b, _ := img.At(x, y).RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}

// PauseMenuVisible reports whether the ESC/pause menu is on screen.
func PauseMenuVisible(img image.Image) bool {
	if !usable(img) {
		return false
	}
	gray := 0
	for _, p := range pauseSamples {
		x, y := ScaleShot(img, p[0], p[1])
		R, G, B := rgb(img, x, y)
		mx, mn := max(R, G, B), min(R, G, B)
		if mx-mn <= 22 && mx >= 60 && mx <= 170 {
			gray++
		}
	}
	return gray >= 11
}

// ReturnToGameAt: the Return-to-Game button in img's pixel space.
func ReturnToGameAt(img image.Image) (int, int) {
	return ScaleShot(img, ReturnToGameShot[0], ReturnToGameShot[1])
}

// THE TRADE PANEL IS ALSO A SCREEN FACT (2026-09-23: vendor stock LINGERS in
// memory after the window closes, so "stock readable" passed while the right-click
// cast a skill in town — "Fableboi says: Impossible."). An open vendor panel shows
// its grid of near-black empty cells on the left; sampled cells (cols 5-8, rows
// 6-9 — empty on both the Misc and Armor tabs) measured 15-16/16 dark with the
// shop open, at most 2/16 otherwise. The panel is LEFT-anchored and scales with
// client height.
// A sub-panel covers the same cells with its own dark interior over a dimmed
// town (Chronicle and Loot Filter both read 12+/16 dark), so the grid yields
// to the sub-panel's X — a covered vendor takes no clicks anyway.
func TradePanelVisible(img image.Image) bool {
	if !usable(img) {
		return false
	}
	if _, _, ok := SubPanelX(img); ok {
		return false
	}
	k := float64(img.Bounds().Dy()) / RefH
	dark := 0
	for _, col := range []int{5, 6, 7, 8} {
		for _, row := range []int{6, 7, 8, 9} {
			x := int((183 + 47.67*float64(col)) * k)
			y := int((238 + 47.5*float64(row)) * k)
			R, G, B := rgb(img, x, y)
			if max(R, G, B) <= 40 {
				dark++
			}
		}
	}
	return dark >= 12
}

// redX: a panel close button's red glyph at an img-space point.
func redX(img image.Image, x, y int) bool {
	R, G, B := rgb(img, x, y)
	return R >= 140 && G <= 90 && B <= 60 && R-G >= 70
}

// SubPanelX reports the centered sub-panel's red close X. Chronicle / Loot
// Filter / Options share one frame whose X sits at (1413,81) — identical color
// on both, never red elsewhere (measured 2026-09-23).
func SubPanelX(img image.Image) (int, int, bool) {
	if !usable(img) {
		return 0, 0, false
	}
	x, y := ScaleShot(img, 1413, 81)
	return x, y, redX(img, x, y)
}

// UIBlocker identifies a screen that swallows world input and where to click to
// dismiss it — ALWAYS a click, never ESC (ESC toggles the pause menu). Measured
// 2026-09-23 on 1920x1050 captures:
//   - "subpanel": Chronicle / Loot Filter / Options (SubPanelX);
//   - "pause": the ESC menu (grey stone buttons) — dismissed by Return to Game.
//
// The vendor trade panel is NOT a blocker here (the shop is wanted mid-errand);
// ShopOpenX closes it deliberately. The sub-panel is checked first: it sits on
// top of the pause menu and must be peeled before Return to Game is reachable.
func UIBlocker(img image.Image) (kind string, x, y int, ok bool) {
	if !usable(img) {
		return "", 0, 0, false
	}
	if cx, cy, ok := SubPanelX(img); ok {
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
	if !usable(img) {
		return 0, 0, false
	}
	x, y := fromLeft(img, 657, 120)
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

// ---------------------------------------------------------------- Half panels
//
// THE SIDE PANELS ARE FRAMED IN NEUTRAL STONE. Every D2R side panel is a tall
// grey frame; the town under it is warm sand and brown shadow. A panel is its
// two vertical frame edges: a column band that reads neutral grey down most of
// its height, with the column just OUTSIDE the frame NOT grey (the centered
// Chronicle frame is grey on both sides of x=1248 and must not pass).
// Measured 2026-09-23 on the 1920x1050 captures (band-mean neutral fraction):
//   - vendor frame (left):  x 118-134 and 660-672 read 0.87/0.87 with the shop
//     open; outside, x 106-112 and 682-688, 0.08/0.16;
//   - bag frame (right):    x 1248-1260 and 1790-1802 read 0.88/0.90 beside the
//     vendor; outside, x 1236-1242 and 1808-1814, <=0.07;
//   - town_clear, town_after_trade, npc_menu, pause_menu, loot_filter read
//     <=0.37 on every band; the Chronicle's grey surface reads 0.88 at x 1248
//     but 0.87 just outside it and 0.16 on the far edge — rejected twice.
// Act 2 (Lut Gholein) only: a snowy or grey-stone town is exactly why the
// outside column must be open — grey ground is grey on both sides of a band.
// Only the vendor + bag pair has been photographed. That other panels share
// these frames (the classic layout: char sheet, quests, waypoint, stash and
// hireling on the left; bag and skill tree on the right) is an ASSUMPTION until
// their captures land — so a half panel says "something is open here", never
// which one.

// neutral: grey stone — low chroma, neither black nor bright.
func neutral(img image.Image, x, y int) bool {
	R, G, B := rgb(img, x, y)
	mx, mn := max(R, G, B), min(R, G, B)
	return mx-mn <= 16 && mx >= 25 && mx <= 150
}

// frameEdge is one vertical frame edge in reference coords.
type frameEdge struct {
	in0, in1   int // the frame's own columns
	out0, out1 int // columns just outside the panel
}

type halfFrame struct {
	name   string
	right  bool // anchored to the right edge
	y0, y1 int
	edges  [2]frameEdge
}

var (
	leftFrame = halfFrame{name: "left", y0: 130, y1: 800,
		edges: [2]frameEdge{{118, 134, 106, 112}, {660, 672, 682, 688}}}
	rightFrame = halfFrame{name: "right", right: true, y0: 40, y1: 840,
		edges: [2]frameEdge{{1248, 1260, 1236, 1242}, {1790, 1802, 1808, 1814}}}
)

// frame thresholds sit mid-gap between the measured populations.
const (
	frameInMin  = 0.60
	frameOutMax = 0.40
)

// bandFrac: fraction of neutral samples over reference columns [x0,x1].
func (h halfFrame) bandFrac(img image.Image, x0, x1 int) float64 {
	n, hit := 0, 0
	for rx := x0; rx <= x1; rx++ {
		for ry := h.y0; ry <= h.y1; ry += 4 {
			var x, y int
			if h.right {
				x, y = fromRight(img, rx, ry)
			} else {
				x, y = fromLeft(img, rx, ry)
			}
			n++
			if neutral(img, x, y) {
				hit++
			}
		}
	}
	return float64(hit) / float64(n)
}

// FrameScore is the raw half-panel measurement, kept for evidence and tests.
type FrameScore struct {
	In  [2]float64 // per edge: frame columns
	Out [2]float64 // per edge: outside columns
}

// Present: both edges framed and both outsides open.
func (s FrameScore) Present() bool {
	for i := 0; i < 2; i++ {
		if s.In[i] < frameInMin || s.Out[i] > frameOutMax {
			return false
		}
	}
	return true
}

func (h halfFrame) score(img image.Image) FrameScore {
	var s FrameScore
	if !usable(img) {
		return s
	}
	for i, e := range h.edges {
		s.In[i] = h.bandFrac(img, e.in0, e.in1)
		s.Out[i] = h.bandFrac(img, e.out0, e.out1)
	}
	return s
}

// LeftPanelScore / RightPanelScore measure the half-panel frames.
func LeftPanelScore(img image.Image) FrameScore  { return leftFrame.score(img) }
func RightPanelScore(img image.Image) FrameScore { return rightFrame.score(img) }

// LeftPanelVisible: some framed panel occupies the left half.
func LeftPanelVisible(img image.Image) bool { return LeftPanelScore(img).Present() }

// RightPanelVisible: some framed panel occupies the right half.
func RightPanelVisible(img image.Image) bool { return RightPanelScore(img).Present() }

// RightPanelX reports the right panel's red close X — (1788,18), right-anchored,
// the same glyph color as the vendor X (163,59,30 on both shop captures). Only
// the bag's X has been photographed there.
func RightPanelX(img image.Image) (int, int, bool) {
	if !usable(img) {
		return 0, 0, false
	}
	x, y := fromRight(img, 1788, 18)
	return x, y, redX(img, x, y)
}
