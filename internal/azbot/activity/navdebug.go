// navdebug: THE OWNER'S WINDOW INTO HER HEAD (01:42: "if i wanted to check
// why she doesn't advance, i could see the tiles she goes for... rather than
// 'she's stuck on a wall'"). Every few seconds while the march holds, render
// logs/nav.png: the map grid's walkability around her, her position, the
// march target, the drive, and the last click waystation — plus one honest
// nav log line. A picture per failure beats five theories per night.
package activity

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
	"github.com/hectorgimenez/koolo/internal/game"
)

var navAt time.Time

const navHalf = 48 // tiles of world on each side of her
const navPx = 6    // pixels per tile

func navMark(img *image.RGBA, me, p data.Position, c color.RGBA) {
	dx, dy := p.X-me.X+navHalf, p.Y-me.Y+navHalf
	if dx < 0 || dy < 0 || dx >= navHalf*2 || dy >= navHalf*2 {
		return
	}
	for yy := dy*navPx - 2; yy <= dy*navPx+navPx+1; yy++ {
		for xx := dx*navPx - 2; xx <= dx*navPx+navPx+1; xx++ {
			if xx >= 0 && yy >= 0 && xx < navHalf*2*navPx && yy < navHalf*2*navPx {
				img.SetRGBA(xx, yy, c)
			}
		}
	}
}

// NavDebug renders the nav picture and logs the nav line. Rate-limited here
// so callers can invoke it every Step without thought.
func NavDebug(ctx *Ctx, tgt data.Position, note string) {
	if time.Since(navAt) < 3*time.Second {
		return
	}
	navAt = time.Now()
	s := ctx.Snap
	me := s.Me.Pos
	d := ctx.GR.GetData()
	ctx.Led.Append(verbs.Outcome{Verb: "nav", Holder: note, Result: verbs.ResRefused,
		Evidence: fmt.Sprintf("me=(%d,%d) area=%d tgt=(%d,%d) way=(%d,%d)",
			me.X, me.Y, int(s.Me.Area), tgt.X, tgt.Y, verbs.LastWaystation.X, verbs.LastWaystation.Y)})

	img := image.NewRGBA(image.Rect(0, 0, navHalf*2*navPx, navHalf*2*navPx))
	ad, hasMap := d.Areas[s.Me.Area]
	for ty := 0; ty < navHalf*2; ty++ {
		for tx := 0; tx < navHalf*2; tx++ {
			wp := data.Position{X: me.X + tx - navHalf, Y: me.Y + ty - navHalf}
			c := color.RGBA{40, 40, 40, 255} // unknown: dark gray
			if hasMap && ad.Grid != nil {
				rp := ad.Grid.RelativePosition(wp)
				if rp.X >= 0 && rp.Y >= 0 && rp.X < ad.Grid.Width && rp.Y < ad.Grid.Height {
					if ad.Grid.CollisionGrid[rp.Y][rp.X] == game.CollisionTypeWalkable {
						c = color.RGBA{30, 90, 30, 255} // walkable: green
					} else {
						c = color.RGBA{110, 30, 30, 255} // wall: red
					}
				}
			}
			for yy := 0; yy < navPx; yy++ {
				for xx := 0; xx < navPx; xx++ {
					img.SetRGBA(tx*navPx+xx, ty*navPx+yy, c)
				}
			}
		}
	}
	navMark(img, me, verbs.LastWaystation, color.RGBA{255, 0, 255, 255}) // click waystation: magenta
	navMark(img, me, tgt, color.RGBA{255, 220, 0, 255})                  // march target: yellow
	navMark(img, me, me, color.RGBA{255, 255, 255, 255})                 // her: white
	if f, err := os.Create("logs/nav.png"); err == nil {
		_ = png.Encode(f, img)
		f.Close()
	}
}
