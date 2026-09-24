package nav

import (
	"math"
	"testing"
)

func TestScreenCarrotReachCapsRadiusKeepsAngle(t *testing.T) {
	for deg := 0; deg < 360; deg += 15 {
		a := float64(deg) * math.Pi / 180
		dx, dy := 20*math.Cos(a), 20*math.Sin(a)
		fx, fy := ScreenCarrot(dx, dy, IsoX, IsoY, CarrotX, CarrotY)
		rx, ry := ScreenCarrotReach(dx, dy, IsoX, IsoY, CarrotX, CarrotY, 80)
		if l := math.Hypot(float64(rx), float64(ry)); l > 81 {
			t.Fatalf("deg %d: reach 80 gave radius %.1f", deg, l)
		}
		if d := math.Abs(math.Atan2(float64(ry), float64(rx)) - math.Atan2(float64(fy), float64(fx))); d > 0.03 && d < 2*math.Pi-0.03 {
			t.Fatalf("deg %d: reach bent the angle by %.3f rad", deg, d)
		}
		if zx, zy := ScreenCarrotReach(dx, dy, IsoX, IsoY, CarrotX, CarrotY, 0); zx != fx || zy != fy {
			t.Fatalf("deg %d: reach 0 must equal ScreenCarrot", deg)
		}
	}
}
