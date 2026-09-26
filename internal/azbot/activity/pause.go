package activity

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/motor"
	"github.com/hectorgimenez/koolo/internal/game"
)

// EnsureWorld dismisses every screen that swallows world input — a pause menu, or
// a pause sub-panel (Chronicle / Loot Filter / Options) — by CLICKING its button,
// peeling up to four layers. Never ESC: ESC toggles the pause menu, and every
// blind ESC on 2026-09-23 ended in a paused game or a sub-panel that ate the next
// run's keys. Memory cannot see these screens; game.UIBlocker reads the screen.
// Returns the number of layers dismissed.
func EnsureWorld(gr *game.MemoryReader, m *motor.Motor) int {
	n := 0
	for i := 0; i < 4; i++ {
		kind, x, y, ok := game.UIBlocker(gr.Screenshot())
		if !ok {
			return n
		}
		_ = kind
		m.RealMenuClick(x, y)
		time.Sleep(450 * time.Millisecond)
		n++
	}
	return n
}

// ClearPause is kept for the executive's sentry: true when anything was dismissed.
func ClearPause(gr *game.MemoryReader, m *motor.Motor) bool { return EnsureWorld(gr, m) > 0 }
