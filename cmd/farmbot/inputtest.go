package main

// -inputtest: identify WHICH user-mode cursor-read export this D2R build consults for the
// in-game cursor, the koolo way — patch a candidate export (in a Windows system DLL, outside
// the Arxan-protected d2r.exe, no debugger) to return a fixed synthetic point, hold
// Force-Move via the existing key-patch, and see whether she walks toward it with the REAL
// mouse untouched. The export that steers her IS both the answer and the aquarium movement
// primitive: background, no cursor hijack, no anti-debug.

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hectorgimenez/koolo/internal/game"
)

func runInputTest(logger *slog.Logger, gr *game.MemoryReader, gi *game.MemoryInjector, moveHold func(time.Duration)) {
	cx, cy := gr.GameAreaSizeX/2, gr.GameAreaSizeY/2

	type cand struct {
		name     string
		has      bool
		physical bool // returns physical (DPI-actual) pixels; else logical screen pixels
		patch    func(x, y int) error
		restore  func() error
	}
	cands := []cand{
		{"GetPhysicalCursorPos", gi.HasPhysicalCursorPos(), true, gi.OverridePhysicalCursorPos, gi.RestorePhysicalCursorPos},
		{"GetCursorInfo", gi.HasCursorInfo(), false, gi.OverrideGetCursorInfo, gi.RestoreGetCursorInfo},
		{"GetCursorPos(control)", true, false, gi.CursorPos, gi.RestoreGetCursorPosAddr},
	}
	// screen offsets that map to pure world axes (same math as -movetest): pick open directions.
	dirs := []struct {
		name   string
		dx, dy int
		wantX  int // expected sign of world delta
		wantY  int
	}{
		{"south(+Y)", -200, 100, 0, 1},
		{"east(+X)", 200, 100, 1, 0},
		{"north(-Y)", 200, -100, 0, -1},
		{"west(-X)", -200, -100, -1, 0},
	}

	logger.Info("inputtest: probing cursor-read exports", "physicalAvail", gi.HasPhysicalCursorPos(), "cursorInfoAvail", gi.HasCursorInfo())

	for _, c := range cands {
		if !c.has {
			logger.Info("inputtest: SKIP (export not present)", "fn", c.name)
			continue
		}
		anyMoved := false
		for _, d := range dirs {
			sx, sy := cx+d.dx, cy+d.dy
			// value to inject in this export's coordinate space
			ix, iy := gr.WindowLeftX+sx, gr.WindowTopY+sy
			if c.physical {
				ix, iy = ix*3/2, iy*3/2 // 150% display scaling (matches the physical cursor mirror)
			}
			before := gr.GetData().PlayerUnit.Position
			_ = c.patch(ix, iy)
			moveHold(700 * time.Millisecond)
			_ = c.restore()
			time.Sleep(150 * time.Millisecond)
			after := gr.GetData().PlayerUnit.Position
			ddx, ddy := after.X-before.X, after.Y-before.Y
			moved := abs(ddx) > 2 || abs(ddy) > 2
			toward := (d.wantX == 0 || sign(ddx) == d.wantX) && (d.wantY == 0 || sign(ddy) == d.wantY)
			if moved {
				anyMoved = true
			}
			logger.Info("inputtest", "fn", c.name, "dir", d.name,
				"injected", fmt.Sprintf("(%d,%d)", ix, iy),
				"posDelta", fmt.Sprintf("(%+d,%+d)", ddx, ddy), "moved", moved, "towardExpected", moved && toward)
		}
		if anyMoved {
			logger.Info("inputtest: ★ CANDIDATE STEERS MOVEMENT", "fn", c.name,
				"note", "this export feeds the in-game cursor — patch it per-tick for hands-off (aquarium) movement")
		}
	}
	logger.Info("inputtest: done — the fn with moved=true is the input path; if only GetCursorPos moved her the classic koolo patch still works; if none, the read is via win32u/GameInput (deeper)")
}

func sign(x int) int {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}
