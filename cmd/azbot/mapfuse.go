package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/azbot/journey"
	"github.com/hectorgimenez/koolo/internal/azbot/mapfuse"
	"github.com/hectorgimenez/koolo/internal/game"
)

// fusion is the executive's ONE grid source (owner: "don't we have maphack-level
// memory already?"). Every regrid reads the live rooms, records them in the atlas
// (persisted per seed), and fuses live > atlas > koolo-map prior (once it has earned
// trust for this seed+area) > optimistic unknown into the planning grid.
type fusion struct {
	gr     *game.MemoryReader
	logger *slog.Logger
	atlas  *game.Atlas
	trust  *mapfuse.Tracker

	priorSeed uint
	priorArea int
	prior     *mapfuse.Layer

	last     *mapfuse.Fused
	lastArea int
	lastSeed uint
	plan     []data.Position
	shotAt   time.Time
	saveAt   time.Time
}

const (
	fusionShotEvery = 60 * time.Second
	fusionSaveEvery = 30 * time.Second
)

func newFusion(gr *game.MemoryReader, logger *slog.Logger) *fusion {
	fz := &fusion{gr: gr, logger: logger, atlas: game.NewAtlas(filepath.Join("logs", "atlas")),
		trust: mapfuse.NewTracker(), lastArea: -1, saveAt: time.Now()}
	journey.OnPlan = func(p []data.Position) { fz.plan = p }
	return fz
}

// build is the drop-in for BuildLiveGridRooms: the fused grid for the current area.
func (fz *fusion) build() (*game.Grid, error) {
	l, _, err := fz.gr.BuildLiveLayer()
	if err != nil {
		return nil, err
	}
	seed := fz.gr.MapSeed()
	if l.Area != fz.lastArea || seed != fz.lastSeed {
		fz.flush() // area exit: the leaving picture + a save
		fz.plan = nil
	}
	live := toLayer(l.OffX, l.OffY, l.W, l.H, l.Cells)
	var atlas *mapfuse.Layer
	if seed != 0 {
		fz.atlas.MergeLiveLayer(seed, l.Area, l)
		if ox, oy, w, h, cells := fz.atlas.Overlay(seed, l.Area).Cells(); cells != nil {
			atlas = toLayer(ox, oy, w, h, cells)
		}
	}
	prior := fz.priorFor(seed, l)
	var tr mapfuse.Trust
	if prior != nil {
		var fresh bool
		tr, fresh = fz.trust.Update(mapfuse.Key{Seed: seed, Area: l.Area}, live, atlas, prior)
		if fresh {
			fz.logger.Info(fmt.Sprintf("mapfuse area=%d agree=%.2f n=%d shift=(%d,%d) trust=%t",
				l.Area, tr.Agree, tr.N, tr.Shift.X, tr.Shift.Y, tr.Trusted),
				"walls", fmt.Sprintf("%.2f/%d", tr.WallAgree, tr.WallN), "known", tr.Known)
		}
	}
	f := mapfuse.Fuse(live, atlas, prior, tr)
	fz.last, fz.lastArea, fz.lastSeed = f, l.Area, seed
	if time.Since(fz.shotAt) >= fusionShotEvery {
		fz.shot()
	}
	if time.Since(fz.saveAt) >= fusionSaveEvery {
		fz.save()
	}
	return fusedGrid(f), nil
}

// priorFor is koolo-map's grid for the area as a layer, cached per (seed, area).
// koolo-map's offset is the same DrlgLevel.PosX*5 the live frame uses; if a
// same-sized frame disagrees on the origin anyway, the live frame wins (the
// ±2-cell trust search covers only small shifts).
func (fz *fusion) priorFor(seed uint, l *game.LiveLayer) *mapfuse.Layer {
	if fz.prior != nil && fz.priorSeed == seed && fz.priorArea == l.Area {
		return fz.prior
	}
	fz.priorSeed, fz.priorArea, fz.prior = seed, l.Area, nil
	ad, ok := fz.gr.MapArea(area.ID(l.Area))
	if !ok || ad.Grid == nil || ad.Grid.Width == 0 || ad.Grid.Height == 0 {
		return nil
	}
	g := ad.Grid
	ox, oy := g.OffsetX, g.OffsetY
	if g.Width == l.W && g.Height == l.H && (ox != l.OffX || oy != l.OffY) {
		fz.logger.Info("mapfuse: prior origin re-anchored to the live frame", "area", l.Area,
			"prior", fmt.Sprintf("(%d,%d)", ox, oy), "live", fmt.Sprintf("(%d,%d)", l.OffX, l.OffY))
		ox, oy = l.OffX, l.OffY
	}
	p := mapfuse.NewLayer(ox, oy, g.Width, g.Height)
	for y := 0; y < g.Height; y++ {
		row := g.CollisionGrid[y]
		for x := 0; x < g.Width && x < len(row); x++ {
			if row[x] == game.CollisionTypeNonWalkable {
				p.Cells[y*g.Width+x] = mapfuse.Block
			} else {
				p.Cells[y*g.Width+x] = mapfuse.Walk
			}
		}
	}
	fz.prior = p
	return p
}

// shot writes logs/mapfuse_<area>_<seed>.png: the fused grid, disagreements, the plan.
func (fz *fusion) shot() {
	fz.shotAt = time.Now()
	if fz.last == nil {
		return
	}
	path := filepath.Join("logs", fmt.Sprintf("mapfuse_%d_%d.png", fz.lastArea, fz.lastSeed))
	ff, err := os.Create(path)
	if err != nil {
		return
	}
	defer ff.Close()
	pu := fz.gr.GetData().PlayerUnit
	me := pu.Position
	if int(pu.Area) != fz.lastArea {
		me = data.Position{} // area exit: the leaving picture has no "me"
	}
	if err := mapfuse.WritePNG(ff, fz.last, fz.plan, me); err != nil {
		fz.logger.Warn("mapfuse: debug picture failed", "err", err)
	}
}

func (fz *fusion) save() {
	fz.saveAt = time.Now()
	if err := fz.atlas.Save(); err != nil {
		fz.logger.Warn("mapfuse: atlas save failed", "err", err)
	}
}

// flush: the closing picture of the area being left, and the atlas to disk.
func (fz *fusion) flush() {
	if fz.last != nil {
		fz.shot()
	}
	fz.save()
}

func toLayer(offX, offY, w, h int, cells []uint8) *mapfuse.Layer {
	l := mapfuse.NewLayer(offX, offY, w, h)
	for i, c := range cells {
		l.Cells[i] = mapfuse.Cell(c) // LiveUnknown/LiveWalk/LiveBlock == Unknown/Walk/Block
	}
	return l
}

// fusedGrid renders the fused classes as a game grid. Observed cells are plain
// Walkable/NonWalkable (no wall-margin softening: nav keeps its own clearance
// field); unobserved cells carry their fused type so journey can price them.
func fusedGrid(f *mapfuse.Fused) *game.Grid {
	cg := make([][]game.CollisionType, f.H)
	for y := range cg {
		row := make([]game.CollisionType, f.W)
		for x := range row {
			switch f.Class[y*f.W+x] {
			case mapfuse.ClassLiveWalk, mapfuse.ClassAtlasWalk:
				row[x] = game.CollisionTypeWalkable
			case mapfuse.ClassLiveBlock, mapfuse.ClassAtlasBlock:
				row[x] = game.CollisionTypeNonWalkable
			case mapfuse.ClassPriorWalk:
				row[x] = game.CollisionTypePriorWalk
			case mapfuse.ClassPriorBlock:
				row[x] = game.CollisionTypePriorBlocked
			case mapfuse.ClassUnknownPriorWall:
				row[x] = game.CollisionTypeUnknownWall
			default:
				row[x] = game.CollisionTypeUnknown
			}
		}
		cg[y] = row
	}
	return &game.Grid{OffsetX: f.OffX, OffsetY: f.OffY, Width: f.W, Height: f.H, CollisionGrid: cg}
}
