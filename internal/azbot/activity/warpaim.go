package activity

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/gamedata"
)

// ---------------------------------------------------------------- the warp's own hitbox
//
// OWNER (2026-09-26): "review how it clicks on things to travel to different
// maps, because it misses those a lot". The clicks went to the entrance's
// ground tile plus fixed guesses (0,-28)/(0,-56)/(-18,-28). The mod's
// lvlwarp.txt says where each entrance is clickable: a select box in classic
// (800x600) pixels relative to the warp's tile — Cave Cliff L spans x -90..0,
// y -100..+10, so x=0 sat on its EDGE; Cliff R is centered ~(+30,-35), the
// opposite side. The entrance unit's txtFileNo IS the lvlwarp row (the UP1
// stairs preset read 4 = "Act 1 Cave Up").

// classicToProj: classic pixels to the 19.8/9.9-per-subtile projection the
// click path uses (classic D2 draws a subtile 16 px wide: 19.8/16).
const classicToProj = 19.8 / 16.0

// warpAimOffsets: click offsets in projection pixels inside the warp's select
// box — the center first, then the box's inner quarters. nil when the warp or
// its box is unknown (the caller keeps its old ladder).
func warpAimOffsets(warpID int) []data.Position {
	return boxOffsets(gamedata.Get().Warp(warpID))
}

// boxOffsets: the select box's center and inner quarters in projection px.
func boxOffsets(w *gamedata.LvlWarp) []data.Position {
	if w == nil || w.SelectDX <= 0 || w.SelectDY <= 0 {
		return nil
	}
	cx := float64(w.SelectX) + float64(w.SelectDX)/2
	cy := float64(w.SelectY) + float64(w.SelectDY)/2
	qx, qy := float64(w.SelectDX)/4, float64(w.SelectDY)/4
	pts := [][2]float64{{cx, cy}, {cx - qx, cy}, {cx + qx, cy}, {cx, cy - qy}, {cx, cy + qy}}
	out := make([]data.Position, 0, len(pts))
	for _, p := range pts {
		out = append(out, data.Position{X: int(p[0] * classicToProj), Y: int(p[1] * classicToProj)})
	}
	return out
}

// warpInteractive: the entrance is clicked, not walked into (a known box and
// not NoInteract) — the contact push is skipped for it.
func warpInteractive(warpID int) bool {
	w := gamedata.Get().Warp(warpID)
	return w != nil && !w.NoInteract && w.SelectDX > 0 && w.SelectDY > 0
}

// openBorder: the mod's levels.txt joins from and to without a warp (a
// walk-through border, nothing to click). Unknown tables: false.
func openBorder(from, to area.ID) bool {
	l := gamedata.Get().Level(int(from))
	if l == nil {
		return false
	}
	for _, e := range l.Exits {
		if e.Level == int(to) {
			return e.Warp < 0
		}
	}
	return false
}
