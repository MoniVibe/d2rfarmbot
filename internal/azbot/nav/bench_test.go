package nav

import (
	"math/rand"
	"testing"
)

// A big outdoor level with scattered rocks: planning must stay cheap enough to run
// on every replan.
func BenchmarkPlanLargeOutdoor(b *testing.B) {
	rng := rand.New(rand.NewSource(3))
	const n = 800
	walk := make([]bool, n*n)
	for i := range walk {
		walk[i] = true
	}
	for k := 0; k < 4000; k++ {
		x, y := rng.Intn(n-6), rng.Intn(n-6)
		for dx := 0; dx < 5; dx++ {
			for dy := 0; dy < 5; dy++ {
				walk[(y+dy)*n+x+dx] = false
			}
		}
	}
	g := NewGrid(0, 0, n, n, func(x, y int) bool { return walk[y*n+x] })
	s, t := Pos{X: 10, Y: 10}, Pos{X: n - 10, Y: n - 10}
	for !g.Walkable(s) {
		s.X++
	}
	for !g.Walkable(t) {
		t.X--
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pl := g.Plan(s, t, Options{})
		if !pl.Found {
			b.Fatal(pl.Reason)
		}
		if i == 0 {
			b.ReportMetric(float64(pl.Expanded), "expanded")
		}
	}
}
