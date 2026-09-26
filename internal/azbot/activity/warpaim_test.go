package activity

import (
	"testing"

	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
)

// Cave Cliff L (lvlwarp row 0 on this mod): select -90,-100 90x110 — the old
// fixed click (0,-28) sat on the box's right EDGE; the center is (-45,-45)
// classic px = (-55,-55) in the 19.8/9.9 projection.
func TestBoxOffsetsAimAtTheSelectBoxCenter(t *testing.T) {
	cliffL := &gamedata.LvlWarp{SelectX: -90, SelectY: -100, SelectDX: 90, SelectDY: 110}
	offs := boxOffsets(cliffL)
	if len(offs) != 5 || offs[0].X != -55 || offs[0].Y != -55 {
		t.Fatalf("center %v, want (-55,-55)", offs)
	}
	for _, o := range offs {
		cx, cy := float64(o.X)/classicToProj, float64(o.Y)/classicToProj
		if cx < -90 || cx > 0 || cy < -100 || cy > 10 {
			t.Fatalf("offset %v lands outside the box", o)
		}
	}
	if boxOffsets(nil) != nil || boxOffsets(&gamedata.LvlWarp{}) != nil {
		t.Fatal("no box, no offsets")
	}
}
