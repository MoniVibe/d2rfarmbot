package screen

import (
	"fmt"
	"image"
)

// ---------------------------------------------------------------- NPC menu / speech

// THE NPC MENU FLOATS; ITS FRAME DOES NOT CHANGE. The Talk/Trade list is a
// near-black box (interior ~3,3,5) framed by a 1-px antique-gold rule
// (141,118,69) with a second rule 4 px inside and corner ornaments. It sits
// above the NPC's head, so its place is wherever the NPC stood: measured at
// x 798-943 y 119-236 (npc_talk), 1001-1146 / 215-332 (npc_menu_2), 910-1055 /
// 267-384 (npc_menu_3), 814-959 / 115-232 (npc_menu_4) and 890-1045 / 252-369
// (old npc_menu, 4 entries of another NPC). The NPC's speech box is the SAME
// frame, 388x166 at the top center (766-1153, 24-190).
//
// So the scan is position-free: every 16th column of the central band is read
// top to bottom for the rule color; a hit is walked into a horizontal run
// (1-px texture gaps allowed); a run >= 80 px wide looks down for a matching
// bottom rule, then both side rules and a dark interior are required.
// Measured 2026-09-24 over 34 captures: the five menus and the speech box
// read sides 0.99-1.00 and interior 0.90-0.94 dark; the sand towns throw up
// thousands of rule-colored pixels (the Lut Gholein ground IS that color) and
// a few rectangles of them, but none with an interior over 0.08 dark.
const (
	boxScanX0, boxScanX1 = 400, 1520 // reference columns scanned (centered)
	boxScanY0, boxScanY1 = 8, 640
	boxScanStep          = 16
	boxMinW, boxMaxW     = 80, 460
	boxMinH, boxMaxH     = 30, 320
	boxSideMin           = 0.90 // measured 0.99-1.00 on real boxes
	boxDarkMin           = 0.60 // measured 0.90-0.94 real, <= 0.08 on sand
	dialogMinW           = 330  // menus measured 145-155 wide, the speech box 387
)

func npcRule(r, g, b int) bool {
	return abs(r-141) <= 20 && abs(g-118) <= 20 && abs(b-69) <= 20
}

// NPCBox is one gold-framed black box, in img pixel space.
type NPCBox struct {
	X0, Y0, X1, Y1 int
	Side, Dark     float64
}

// Dialog: the speech box (wide); otherwise the Talk/Trade menu.
func (b NPCBox) Dialog(img image.Image) bool {
	return float64(b.X1-b.X0) >= dialogMinW*scaleK(img)
}

func (b NPCBox) String() string {
	return fmt.Sprintf("gold-framed box %dx%d at (%d,%d), sides %.2f, interior %.2f dark",
		b.X1-b.X0, b.Y1-b.Y0, b.X0, b.Y0, b.Side, b.Dark)
}

// ruleRun walks the rule through (x,y) both ways, bridging gaps of <= 2 px.
func ruleRun(img image.Image, x, y, lo, hi int) (int, int) {
	walk := func(dir int) int {
		end, gap := x, 0
		for p := x + dir; p >= lo && p <= hi && gap <= 2; p += dir {
			if npcRule(pix(img, p, y)) {
				end, gap = p, 0
			} else {
				gap++
			}
		}
		return end
	}
	return walk(-1), walk(1)
}

// NPCBoxes scans the central band for gold-framed black boxes.
func NPCBoxes(img image.Image) []NPCBox {
	if !usable(img) {
		return nil
	}
	b := img.Bounds()
	k := scaleK(img)
	sc := func(v int) int { return int(float64(v)*k + 0.5) }
	x0, y0 := ScaleShot(img, boxScanX0, boxScanY0)
	x1, y1 := ScaleShot(img, boxScanX1, boxScanY1)
	step := max(sc(boxScanStep), 4)
	var out []NPCBox
	tried := map[[2]int]bool{}
	inBox := func(x, y int) bool {
		for _, q := range out {
			if x >= q.X0-2 && x <= q.X1+2 && y >= q.Y0-2 && y <= q.Y1+2 {
				return true
			}
		}
		return false
	}
	for x := x0; x < x1; x += step {
		for y := y0; y < y1; y++ {
			if inBox(x, y) || !npcRule(pix(img, x, y)) {
				continue
			}
			// a top rule has black under it; sand-colored ground does not
			if !darkAt(img, x, y+sc(12)) && !darkAt(img, x, y+sc(20)) {
				continue
			}
			xa, xb := ruleRun(img, x, y, b.Min.X, b.Max.X-1)
			w := xb - xa
			if w < sc(boxMinW) || w > sc(boxMaxW) || tried[[2]int{xa, y}] {
				continue
			}
			tried[[2]int{xa, y}] = true
			if !darkUnder(img, xa, xb, y, sc) {
				continue
			}
			if box, ok := boxBelow(img, xa, xb, y, sc); ok {
				out = append(out, box)
			}
		}
	}
	return out
}

func darkAt(img image.Image, x, y int) bool {
	R, G, B := pix(img, x, y)
	return max(R, G, B) <= 40
}

// darkUnder is the cheap gate before the bottom-rule search: a real box is
// black right under its top rule (24 samples, rows +8/+16/+24), while the sand
// ground that shares the rule's color is not — without it a Lut Gholein frame
// cost 20+ ms of bottom-rule searches.
func darkUnder(img image.Image, xa, xb, y int, sc func(int) int) bool {
	n, dark := 0, 0
	for _, dy := range []int{8, 16, 24} {
		for i := 1; i <= 8; i++ {
			x := xa + (xb-xa)*i/9
			R, G, B := pix(img, x, y+sc(dy))
			n++
			if max(R, G, B) <= 40 {
				dark++
			}
		}
	}
	return dark*2 >= n
}

// boxBelow looks for the bottom rule under a top rule [xa,xb] at y and
// verifies the sides and the dark interior.
func boxBelow(img image.Image, xa, xb, y int, sc func(int) int) (NPCBox, bool) {
	mid := (xa + xb) / 2
	for y2 := y + sc(boxMinH); y2 <= y+sc(boxMaxH) && y2 < img.Bounds().Max.Y; y2++ {
		if !npcRule(pix(img, mid, y2)) {
			continue
		}
		a2, b2 := ruleRun(img, mid, y2, xa-4, xb+4)
		// the speech box's scroll arrows break its bottom rule's right end
		if abs(a2-xa) > 3 || b2 < xb-sc(16) {
			continue
		}
		box := NPCBox{X0: xa, Y0: y, X1: xb, Y1: y2}
		n, l, r := 0, 0, 0
		for yy := y; yy <= y2; yy++ {
			n++
			if npcRule(pix(img, xa, yy)) {
				l++
			}
			if npcRule(pix(img, xb, yy)) || npcRule(pix(img, xb-1, yy)) {
				r++
			}
		}
		box.Side = float64(min(l, r)) / float64(n)
		if box.Side < boxSideMin {
			continue
		}
		in, dark := 0, 0
		d := sc(8)
		for yy := y + d; yy <= y2-d; yy += 4 {
			for xx := xa + d; xx <= xb-d; xx += 4 {
				in++
				R, G, B := pix(img, xx, yy)
				if max(R, G, B) <= 40 {
					dark++
				}
			}
		}
		if in == 0 {
			continue
		}
		box.Dark = float64(dark) / float64(in)
		if box.Dark < boxDarkMin {
			continue
		}
		return box, true
	}
	return NPCBox{}, false
}

// NPCMenuSight: the floating Talk/Trade list (close: ESC).
func NPCMenuSight(img image.Image) Sighting { return npcSight(img, false) }

// NPCDialogSight: the NPC's speech box (close: ESC).
func NPCDialogSight(img image.Image) Sighting { return npcSight(img, true) }

func npcSight(img image.Image, dialog bool) Sighting {
	for _, b := range NPCBoxes(img) {
		if b.Dialog(img) == dialog {
			return Sighting{Seen: true, Why: b.String()}
		}
	}
	return Sighting{Why: "no gold-framed box"}
}
