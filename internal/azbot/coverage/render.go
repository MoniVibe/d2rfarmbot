package coverage

import (
	"image"
	"image/color"
)

// Overlay is what the debug picture draws over the map.
type Overlay struct {
	Me    Pos
	Goal  *Pos  // current exploration goal (nil: none)
	Route []Pos // the route to it
	Black []Pos // blacklisted goals
}

var (
	colVoid    = color.RGBA{8, 8, 10, 255}
	colWall    = color.RGBA{48, 44, 52, 255}
	colUnseen  = color.RGBA{118, 118, 118, 255} // walkable, never seen
	colUnknown = color.RGBA{84, 84, 96, 255}    // unstreamed room ground
	colSeen    = color.RGBA{222, 218, 200, 255}
	colMe      = color.RGBA{255, 40, 40, 255}
	colGoal    = color.RGBA{255, 210, 0, 255}
	colRoute   = color.RGBA{0, 200, 255, 255}
	colBlack   = color.RGBA{0, 0, 0, 255}
	palette    = []color.RGBA{
		{230, 60, 160, 255}, {40, 170, 70, 255}, {250, 130, 20, 255}, {120, 80, 230, 255},
		{20, 150, 170, 255}, {200, 40, 40, 255}, {140, 170, 20, 255}, {60, 110, 250, 255},
	}
)

// RenderScale is pixels per tile: small areas are drawn larger so a phone-sized
// look still reads.
func RenderScale(w, h int) int {
	switch d := maxInt(w, h); {
	case d > 900:
		return 1
	case d > 400:
		return 2
	}
	return 3
}

// Render draws the coverage picture: walls dark, walkable-unseen grey, seen
// light, frontier clusters colored, blacklisted goals as black crosses, the
// route line, the goal and her dot. Image is W*s × H*s with s = RenderScale.
func Render(m *Map, t *Terrain, clusters []Cluster, ov Overlay) *image.RGBA {
	s := RenderScale(m.W, m.H)
	img := image.NewRGBA(image.Rect(0, 0, m.W*s, m.H*s))
	fill := func(x, y int, c color.RGBA) { // map-relative tile
		if x < 0 || y < 0 || x >= m.W || y >= m.H {
			return
		}
		for yy := 0; yy < s; yy++ {
			for xx := 0; xx < s; xx++ {
				img.SetRGBA(x*s+xx, y*s+yy, c)
			}
		}
	}
	fits := m.Fits(t)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			i := y*m.W + x
			c := colVoid
			if fits {
				switch {
				case t.C[i] == Wall || (t.C[i] == Unknown && m.wall.get(i)):
					c = colWall
				case m.seen.get(i) && m.passable(t, i):
					c = colSeen
				case t.C[i] == Floor:
					c = colUnseen
				case t.C[i] == Unknown:
					c = colUnknown
				}
			} else if m.seen.get(i) {
				c = colSeen
			}
			fill(x, y, c)
		}
	}
	for k, cl := range clusters {
		pc := palette[k%len(palette)]
		for _, q := range cl.Cells {
			fill(q.X-m.OffX, q.Y-m.OffY, pc)
		}
	}
	for _, q := range ov.Route {
		fill(q.X-m.OffX, q.Y-m.OffY, colRoute)
	}
	blob := func(p Pos, r int, c color.RGBA) {
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				fill(p.X-m.OffX+dx, p.Y-m.OffY+dy, c)
			}
		}
	}
	for _, b := range ov.Black {
		for d := -2; d <= 2; d++ {
			fill(b.X-m.OffX+d, b.Y-m.OffY+d, colBlack)
			fill(b.X-m.OffX+d, b.Y-m.OffY-d, colBlack)
		}
	}
	if ov.Goal != nil {
		blob(*ov.Goal, 2, colGoal)
	}
	blob(ov.Me, 2, colMe)
	return img
}
