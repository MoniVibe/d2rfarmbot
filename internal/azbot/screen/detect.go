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

func rgb(img image.Image, x, y int) (int, int, int) { return pix(img, x, y) }

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
// cast a skill in town — "Fableboi says: Impossible."). The vendor's proof is its
// own artwork (ShopOpenX: the red X plus chromeVendor, 16/16 vs <=1/16).
//
// THE EMPTY-GRID FALLBACK IS GONE (relay R9, 2026-09-24). It counted near-black
// cells (cols 5-8, rows 6-9) under a red X at (657,120) and named that a shop.
// Dark worlds read as an empty grid (world_cave 16/16, the night field 13/16,
// the paused Maggot Lair 16/16), and dark red scenery passes the red-tile test
// (max 19x19 red fraction 0.71 on the cave's torches, 0.84 on the night field)
// — so in the Maggot Lair, mid-fight, it read "vendor empty grid", the janitor
// pressed ESC at nothing, and ESC raised the pause menu. TradePanelVisible is
// now only a stock measure ON a proven vendor panel: the vendor chrome first.
func TradePanelVisible(img image.Image) bool {
	if _, _, ok := ShopOpenX(img); !ok {
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

// SubPanelX reports the centered sub-panel's red close X. Chronicle / Loot
// Filter / Options share one frame whose X sits at (1413,81).
//
// A RED TILE IS NOT A SUB-PANEL (relay R9, 2026-09-24): in the Maggot Lair the
// 19x19 red test at (1413,81) fired on dark red scenery, the janitor's "close"
// click landed in the world, 98 times in 90s. The X now counts only inside the
// sub-panel's own stone frame (chromeSub: top rim and title band, both side
// edges, the bottom rim — never the title, which names the sub-panel):
// measured 16/16 on chronicle and loot_filter (15/16 shifted one pixel, 10/16
// at two), 0/16 on all 34 other captures, including the paused lair
// (esc_loop_now), the Pit cave and the night field.
func SubPanelX(img image.Image) (int, int, bool) {
	x, y, ok := xSub.find(img)
	return x, y, ok && chromeSub.score(img) >= chromeSub.min
}

// SubPanelScore is the raw measurement behind SubPanelX (evidence and tests).
func SubPanelScore(img image.Image) (red float64, chrome int) {
	if !usable(img) {
		return 0, 0
	}
	return xSub.redFrac(img), chromeSub.score(img)
}

// chromeSub: the sub-panel frame's stone (x 488-1432, y 62-855, centered),
// 5x5 means where chronicle and loot_filter agree within 6 and no other
// capture comes within the tolerance. Seen at >= 12.
var chromeSub = chromeSig{name: "sub-panel frame", a: center, tol: 14, min: 12, pts: [][5]int{
	{620, 66, 111, 110, 100}, {860, 66, 114, 113, 104}, {1100, 66, 118, 117, 106}, {1340, 66, 107, 107, 98},
	{740, 84, 106, 106, 98}, {980, 84, 109, 109, 101}, {1220, 84, 103, 103, 97},
	{508, 220, 77, 76, 71}, {490, 320, 56, 55, 52}, {508, 470, 57, 55, 54},
	{1428, 220, 67, 67, 65}, {1410, 320, 76, 73, 67}, {1428, 520, 68, 67, 61},
	{680, 834, 66, 64, 58}, {890, 834, 60, 58, 53}, {1280, 834, 61, 58, 53},
}}

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
// The char sheet, quest log, waypoint and mercenary panels carry the SAME X at
// the same spot (relay R2), so the X alone no longer names the vendor: it
// counts only with the vendor's own chrome (chromeVendor, 16/16 vs 0/16 on
// those four).
func ShopOpenX(img image.Image) (int, int, bool) {
	x, y, ok := xLeft.find(img)
	return x, y, ok && chromeVendor.score(img) >= chromeVendor.min
}

// ShopVisible: the vendor panel is on screen — its red close X inside the
// vendor's own chrome (stock-independent: a fully stocked tab shows it too).
// The dark empty-grid fallback is gone (relay R9, see TradePanelVisible).
func ShopVisible(img image.Image) bool {
	_, _, ok := ShopOpenX(img)
	return ok
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
// Relay R2 (2026-09-24) confirmed the layout: the char sheet, quest log,
// waypoint and hireling read the left frame (in 0.82-0.87), the bag and the
// skill tree the right one (0.80-0.90), and no capture with nothing on a side
// reads a frame there. It also showed the limit: in the grey Act 1 camp the
// outside column is grey too (out 0.55) and the bag's frame reads ABSENT, and
// the stash's wide frame fails the left inner edge. So the named detectors
// (panels.go) carry every photographed panel; a half panel is the fallback
// that says "something is open here" for a panel never photographed.

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

// RightPanelX reports the bag's red close X — (1788,18), right-anchored, its
// 19x19 red tile (closeSpot). The skill tree's X sits lower, at (1785,120).
func RightPanelX(img image.Image) (int, int, bool) { return xBag.find(img) }
