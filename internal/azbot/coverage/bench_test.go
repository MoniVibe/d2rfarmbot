package coverage

import (
	"testing"
	"time"
)

func benchTerrain() *Terrain {
	return NewTerrain(0, 0, 400, 400, func(x, y int) Cell {
		if x%40 == 0 && y%7 != 0 {
			return Wall
		}
		if x > 300 {
			return Unknown
		}
		return Floor
	})
}

// The per-tick cost: one moved-tile Update plus the cached frontier.
func BenchmarkTick(b *testing.B) {
	ter := benchTerrain()
	m := NewFor(ter)
	for i := 0; i < b.N; i++ {
		m.Update(ter, P(50+i%200, 200), DefaultSight)
		m.Frontier(ter, DefaultChunk)
	}
}

// A re-pick: frontier + one Dijkstra over a 400×400 area.
func BenchmarkRepick(b *testing.B) {
	ter := benchTerrain()
	m := NewFor(ter)
	for x := 20; x < 280; x += 20 {
		m.Update(ter, P(x, 200), DefaultSight)
	}
	for i := 0; i < b.N; i++ {
		var p Policy
		p.Next(m, ter, P(100, 200), Bias{}, time.Unix(0, 0))
	}
}
