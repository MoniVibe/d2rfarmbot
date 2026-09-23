package activity

import (
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/game"
)

// hoverProbeOrder: offsets around a unit's measured body point, most likely first.
// The calibration (game.UnitAimDY) already puts probe 0 on the body; the rings
// cover sprite size and walking lag. 21 probes ≈ 1s worst case.
var hoverProbeOrder = [][2]int{
	{0, 0}, {0, -12}, {0, 12}, {-10, 0}, {10, 0},
	{-10, -12}, {10, -12}, {-10, 12}, {10, 12},
	{0, -24}, {0, 24}, {-20, 0}, {20, 0},
	{-20, -24}, {20, -24}, {0, -36}, {0, 36},
	{-30, 0}, {30, 0}, {-20, 24}, {20, 24},
}

// hoverUnitTracked finds a cursor point where the GAME confirms unit u is hovered,
// re-projecting from FRESH positions before every probe — town NPCs wander, and the
// old 84-probe sweep aimed 3-8 seconds at where the NPC had BEEN (2026-09-23: two
// dead 8-10s talk attempts before every successful third — the owner's "needs to
// click 3 times" and "goes idle 10 seconds"). Returns the confirmed point.
func hoverUnitTracked(ctx *Ctx, u data.UnitID) (int, int, bool) {
	for _, o := range hoverProbeOrder {
		d := ctx.GR.GetData()
		var pos data.Position
		found := false
		for _, m := range d.Monsters {
			if m.UnitID == u {
				pos, found = m.Position, true
				break
			}
		}
		if !found {
			return 0, 0, false
		}
		me := d.PlayerUnit.Position
		bx := int(float32((pos.X-me.X)-(pos.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
		by := int(float32((pos.X-me.X)+(pos.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2 + game.UnitAimDY()
		cx, cy := bx+o[0], by+o[1]
		if cx < 20 || cy < 20 || cx > ctx.GR.GameAreaSizeX-20 || cy > ctx.GR.GameAreaSizeY-20 {
			continue
		}
		ctx.M.AimPhysical(cx, cy)
		time.Sleep(35 * time.Millisecond)
		if hd := ctx.GR.GetData().HoverData; hd.IsHovered && hd.UnitID == u {
			return cx, cy, true
		}
	}
	return 0, 0, false
}
