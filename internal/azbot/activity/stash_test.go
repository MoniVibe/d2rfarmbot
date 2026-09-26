package activity

import (
	"image"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/loot"
	"image/color"
	"testing"
)

// paint a synthetic stash page: every cell occupied (navy) except the listed ones.
func stashPage(empty map[[2]int]bool) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1050))
	navy := color.RGBA{18, 19, 37, 255}
	dark := color.RGBA{14, 13, 15, 255}
	for x := 0; x < stashCols; x++ {
		for y := 0; y < stashRows; y++ {
			c := navy
			if empty[[2]int{x, y}] {
				c = dark
			}
			cx := int(stashCell0X + stashPitchX*float64(x))
			cy := int(stashCell0Y + stashPitchY*float64(y))
			for dx := -20; dx <= 20; dx++ {
				for dy := -20; dy <= 20; dy++ {
					img.Set(cx+dx, cy+dy, c)
				}
			}
		}
	}
	return img
}

func TestStashFreeBlockFindsRoomBySight(t *testing.T) {
	full := stashPage(nil)
	if _, _, ok := stashFreeBlock(full, 1, 1, 1); ok {
		t.Fatal("a full page has no room")
	}
	// a 2x3 hole at columns 5-6, rows 4-6
	hole := map[[2]int]bool{}
	for x := 5; x <= 6; x++ {
		for y := 4; y <= 6; y++ {
			hole[[2]int{x, y}] = true
		}
	}
	img := stashPage(hole)
	if gx, gy, ok := stashFreeBlock(img, 2, 3, 1); !ok || gx != 5 || gy != 4 {
		t.Fatalf("2x3 hole at (5,4): got (%d,%d) ok=%v", gx, gy, ok)
	}
	if _, _, ok := stashFreeBlock(img, 2, 4, 1); ok {
		t.Fatal("a 2x4 item does not fit a 2x3 hole")
	}
	cx, cy := stashBlockPx(5, 4, 2, 3, 1)
	if cx != 444 || cy != 367 {
		t.Fatalf("block center wrong: (%d,%d)", cx, cy)
	}
}

func TestFootprintIsModAware(t *testing.T) {
	// R26: the TP tome (mod row 533) is 1x2; d2go's vanilla row 533 is not.
	if w, h := footprint(data.Item{ID: 533}); w != 1 || h != 2 {
		t.Fatalf("TP tome footprint: want 1x2, got %dx%d", w, h)
	}
	if w, h := footprint(data.Item{ID: 620}); w != 1 || h != 3 {
		t.Fatalf("grand charm footprint: want 1x3, got %dx%d", w, h)
	}
}

func TestStashTabForKind(t *testing.T) {
	if stashTabFor(loot.KindRune) != stashTabRunes || stashTabFor(loot.KindGem) != stashTabGems ||
		stashTabFor(loot.KindModUnknown) != stashTabMaterials || stashTabFor(loot.KindCharm) != stashTabShared {
		t.Fatal("runes/gems/mod rows go to their tabs; the rest to Shared")
	}
}
