package mapfuse

import (
	"image"
	"image/color"
	"image/png"
	"io"
)

// Debug palette. Observed cells are bright, the prior is muted, unknown is dark;
// disagreement (observed vs prior) is flagged hot pink so a mistrusted prior's
// evidence is visible at a glance.
var (
	colLiveWalk   = color.RGBA{60, 200, 80, 255}
	colLiveBlock  = color.RGBA{200, 50, 50, 255}
	colAtlasWalk  = color.RGBA{50, 130, 200, 255}
	colAtlasBlock = color.RGBA{110, 40, 120, 255}
	colPriorWalk  = color.RGBA{40, 80, 45, 255}
	colPriorBlock = color.RGBA{90, 60, 40, 255}
	colUnknown    = color.RGBA{25, 25, 25, 255}
	colUnkWall    = color.RGBA{55, 40, 30, 255}
	colDisagree   = color.RGBA{255, 60, 200, 255}
	colPlan       = color.RGBA{255, 230, 0, 255}
	colMe         = color.RGBA{255, 255, 255, 255}
)

const (
	renderMargin = 30
	renderMaxDim = 2048
)

func classColor(c Class) color.RGBA {
	switch c {
	case ClassLiveWalk:
		return colLiveWalk
	case ClassLiveBlock:
		return colLiveBlock
	case ClassAtlasWalk:
		return colAtlasWalk
	case ClassAtlasBlock:
		return colAtlasBlock
	case ClassPriorWalk:
		return colPriorWalk
	case ClassPriorBlock:
		return colPriorBlock
	case ClassUnknownPriorWall:
		return colUnkWall
	}
	return colUnknown
}

// Render draws the fused grid cropped to what matters (observed cells, the plan and
// the player, plus a margin), one pixel per cell — downsampled by an integer factor
// only when the crop exceeds renderMaxDim.
func Render(f *Fused, plan []Pos, me Pos) *image.RGBA {
	if f == nil || f.W == 0 || f.H == 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	x0, y0, x1, y1 := f.W, f.H, -1, -1
	grow := func(lx, ly int) {
		if lx < 0 || ly < 0 || lx >= f.W || ly >= f.H {
			return // another frame's point (a plan from before an area change)
		}
		x0, y0 = min(x0, lx), min(y0, ly)
		x1, y1 = max(x1, lx), max(y1, ly)
	}
	for i, c := range f.Class {
		if c.Known() {
			grow(i%f.W, i/f.W)
		}
	}
	for _, p := range plan {
		grow(p.X-f.OffX, p.Y-f.OffY)
	}
	if me != (Pos{}) {
		grow(me.X-f.OffX, me.Y-f.OffY)
	}
	if x1 < 0 {
		x0, y0, x1, y1 = 0, 0, f.W-1, f.H-1
	}
	x0, y0 = max(0, x0-renderMargin), max(0, y0-renderMargin)
	x1, y1 = min(f.W-1, x1+renderMargin), min(f.H-1, y1+renderMargin)
	cw, ch := x1-x0+1, y1-y0+1
	scale := 1
	for cw/scale > renderMaxDim || ch/scale > renderMaxDim {
		scale++
	}
	img := image.NewRGBA(image.Rect(0, 0, (cw+scale-1)/scale, (ch+scale-1)/scale))
	for ly := y0; ly <= y1; ly++ {
		for lx := x0; lx <= x1; lx++ {
			i := ly*f.W + lx
			col := classColor(f.Class[i])
			if f.Disagree[i] {
				col = colDisagree
			}
			px, py := (lx-x0)/scale, (ly-y0)/scale
			// When downsampled, a disagreement or a wall wins the pixel.
			sub := scale > 1 && ((lx-x0)%scale != 0 || (ly-y0)%scale != 0)
			if sub && !f.Disagree[i] && f.Class[i].Walkable() {
				continue
			}
			img.SetRGBA(px, py, col)
		}
	}
	plot := func(p Pos, c color.RGBA) {
		lx, ly := p.X-f.OffX-x0, p.Y-f.OffY-y0
		if lx < 0 || ly < 0 || lx >= cw || ly >= ch {
			return
		}
		img.SetRGBA(lx/scale, ly/scale, c)
	}
	for k := 1; k < len(plan); k++ {
		a, b := plan[k-1], plan[k]
		n := max(absInt(b.X-a.X), absInt(b.Y-a.Y))
		for s := 0; s <= n; s++ {
			t := 0.0
			if n > 0 {
				t = float64(s) / float64(n)
			}
			plot(Pos{X: a.X + int(float64(b.X-a.X)*t+0.5*sign(b.X-a.X)), Y: a.Y + int(float64(b.Y-a.Y)*t+0.5*sign(b.Y-a.Y))}, colPlan)
		}
	}
	if me != (Pos{}) {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				plot(Pos{X: me.X + dx*scale, Y: me.Y + dy*scale}, colMe)
			}
		}
	}
	return img
}

func sign(v int) float64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// WritePNG renders and encodes in one call.
func WritePNG(w io.Writer, f *Fused, plan []Pos, me Pos) error {
	return png.Encode(w, Render(f, plan, me))
}
