package screen

import "image"

// ---------------------------------------------------------------- Sight primitives
//
// The detectors below read a few hundred pixels per frame, so pixel access is
// the whole cost: the capture (*image.RGBA from Screenshot) and decoded PNGs
// (*image.RGBA / *image.NRGBA) are read straight from Pix; anything else goes
// through the image.Image interface.

// pix reads one pixel as 8-bit RGB. Out-of-bounds reads are black.
func pix(img image.Image, x, y int) (int, int, int) {
	switch m := img.(type) {
	case *image.RGBA:
		if !(image.Point{x, y}.In(m.Rect)) {
			return 0, 0, 0
		}
		i := m.PixOffset(x, y)
		return int(m.Pix[i]), int(m.Pix[i+1]), int(m.Pix[i+2])
	case *image.NRGBA:
		if !(image.Point{x, y}.In(m.Rect)) {
			return 0, 0, 0
		}
		i := m.PixOffset(x, y)
		return int(m.Pix[i]), int(m.Pix[i+1]), int(m.Pix[i+2])
	}
	r, g, b, _ := img.At(x, y).RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}

// anchor maps a 1920x1050 reference point into img's pixel space.
type anchor uint8

const (
	center    anchor = iota // horizontally centered, scales with height (ScaleShot)
	leftEdge                // hugs the left edge (fromLeft)
	rightEdge               // hugs the right edge (fromRight)
)

func (a anchor) at(img image.Image, x, y int) (int, int) {
	switch a {
	case leftEdge:
		return fromLeft(img, x, y)
	case rightEdge:
		return fromRight(img, x, y)
	}
	return ScaleShot(img, x, y)
}

// scaleK is the height scale from the reference capture to img.
func scaleK(img image.Image) float64 { return float64(img.Bounds().Dy()) / RefH }

// ---------------------------------------------------------------- Close buttons

// THE CLOSE X IS A RED SQUARE, NOT A PIXEL. Every framed panel's X is a ~20x19
// dark-red tile (R>=45, R>=2G, R>=2B) — 301/380 pixels unhovered, 358/380 with
// the cursor on it ("Close" tooltip). The old single-pixel test at the tile's
// center only worked because (657,120) happens to land on a bright stroke.
// Measured 2026-09-24 over 34 captures (8 old + 26 relay R2), fraction of the
// 19x19 box around each spot:
//   - left X (657,120): 0.78 (vendor, waypoint, mercenary) / 0.94 hovered
//     (char sheet, quest log); 0.00 on every other capture;
//   - stash X (946,18), bag X (1788,18), skill-tree X (1785,120), sub-panel X
//     (1413,81): 0.78 where the panel is up, 0.00 everywhere else.
const redBoxMin = 0.50

// closeSpot is one photographed X position.
type closeSpot struct {
	x, y int
	a    anchor
}

var (
	xLeft  = closeSpot{657, 120, leftEdge}   // vendor, char sheet, quest log, waypoint, mercenary
	xStash = closeSpot{946, 18, leftEdge}    // stash (the wide left panel)
	xBag   = closeSpot{1788, 18, rightEdge}  // inventory
	xTree  = closeSpot{1785, 120, rightEdge} // skill tree
	xSub   = closeSpot{1413, 81, center}     // Options / Chronicle / Loot Filter
)

// redFrac: fraction of red-tile pixels in the 19x19 (scaled) box at the spot.
func (c closeSpot) redFrac(img image.Image) float64 {
	cx, cy := c.a.at(img, c.x, c.y)
	h := int(9*scaleK(img) + 0.5)
	if h < 2 {
		h = 2
	}
	n, hit := 0, 0
	for y := cy - h; y <= cy+h; y++ {
		for x := cx - h; x <= cx+h; x++ {
			R, G, B := pix(img, x, y)
			n++
			if R >= 45 && R >= 2*G && R >= 2*B {
				hit++
			}
		}
	}
	return float64(hit) / float64(n)
}

// find reports the X's center in img space when its red tile is there.
func (c closeSpot) find(img image.Image) (int, int, bool) {
	if !usable(img) {
		return 0, 0, false
	}
	x, y := c.a.at(img, c.x, c.y)
	return x, y, c.redFrac(img) >= redBoxMin
}

// ---------------------------------------------------------------- Static text

// glyphSig recognises a STATIC word (a panel title, a fixed hint line) by
// sample points on its strokes and in the gaps around them. The points were
// picked from the reference capture by tool (scratch glyph.py, 2026-09-24):
// "on" points are stroke pixels that NO other capture shows in the text color
// at that spot; "off" points are 3x3-clear background. Text is rendered
// pixel-exact at a fixed place, so at 1920x1050 a panel scores 20/20 + 12/12
// and every other capture 0/20 (at most 3/20 when the whole word is also tried
// shifted by one pixel, which a 1-px UI offset must not blind). Other client
// sizes will not match the strokes — those fall back to the half-panel frame
// and the red X.
type glyphSig struct {
	name   string
	a      anchor
	text   func(r, g, b int) bool
	on     [][2]int
	off    [][2]int
	minOn  int
	minOff int
}

// titleGold: the panel-title color (199,179,119 at the stroke core).
func titleGold(r, g, b int) bool { return r >= 150 && r-b >= 50 && r >= g && g >= b }

// hintWhite: white UI text (255,255,255 at the stroke core).
func hintWhite(r, g, b int) bool { return min(r, g, b) >= 190 }

// glyphScore is the raw measurement, kept for evidence and tests.
type glyphScore struct{ On, Off, NOn, NOff, DX, DY int }

func (s glyphSig) score(img image.Image) glyphScore {
	best := glyphScore{NOn: len(s.on), NOff: len(s.off)}
	if !usable(img) {
		return best
	}
	// The exact place first; a whole-word shift of one pixel only when the
	// exact place misses (a shift per POINT would let other words' strokes in).
	for _, d := range glyphShifts {
		sc := glyphScore{NOn: len(s.on), NOff: len(s.off), DX: d[0], DY: d[1]}
		for _, p := range s.on {
			x, y := s.a.at(img, p[0], p[1])
			if s.text(pix(img, x+d[0], y+d[1])) {
				sc.On++
			}
		}
		for _, p := range s.off {
			x, y := s.a.at(img, p[0], p[1])
			R, G, B := pix(img, x+d[0], y+d[1])
			if max(R, G, B) <= 110 {
				sc.Off++
			}
		}
		if sc.On+sc.Off > best.On+best.Off {
			best = sc
		}
		if s.match(best) {
			break
		}
	}
	return best
}

var glyphShifts = [][2]int{{0, 0}, {-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, -1}, {1, -1}, {-1, 1}, {1, 1}}

func (s glyphSig) match(sc glyphScore) bool { return sc.On >= s.minOn && sc.Off >= s.minOff }

// ---------------------------------------------------------------- Static chrome

// chromeSig recognises a panel by its static artwork: 5x5-mean colors at
// reference points, each within tol per channel. Points were picked by tool
// (scratch pick.py) from areas that hold no item, number or name, kept only
// where every positive capture agrees within 10 and spread over the panel.
type chromeSig struct {
	name string
	a    anchor
	tol  int
	pts  [][5]int // x, y, r, g, b
	min  int
}

func (s chromeSig) score(img image.Image) int {
	if !usable(img) {
		return 0
	}
	k := scaleK(img)
	h := int(2*k + 0.5)
	hit := 0
	for _, p := range s.pts {
		cx, cy := s.a.at(img, p[0], p[1])
		var sr, sg, sb, n int
		for y := cy - h; y <= cy+h; y++ {
			for x := cx - h; x <= cx+h; x++ {
				R, G, B := pix(img, x, y)
				sr, sg, sb, n = sr+R, sg+G, sb+B, n+1
			}
		}
		if abs(sr/n-p[2]) <= s.tol && abs(sg/n-p[3]) <= s.tol && abs(sb/n-p[4]) <= s.tol {
			hit++
		}
	}
	return hit
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
