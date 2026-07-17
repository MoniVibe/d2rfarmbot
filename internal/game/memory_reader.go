package game

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/memory"
	"github.com/hectorgimenez/d2go/pkg/utils"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/game/map_client"
	"github.com/lxn/win"
	"golang.org/x/sync/errgroup"
)

type MemoryReader struct {
	cfg *config.CharacterCfg
	*memory.GameReader
	mapSeed        uint
	HWND           win.HWND
	WindowLeftX    int
	WindowTopY     int
	GameAreaSizeX  int
	GameAreaSizeY  int
	supervisorName string
	cachedMapData  map[area.ID]AreaData
	logger         *slog.Logger
}

func NewGameReader(cfg *config.CharacterCfg, supervisorName string, pid uint32, window win.HWND, logger *slog.Logger) (*MemoryReader, error) {
	process, err := memory.NewProcessForPID(pid)
	if err != nil {
		return nil, err
	}

	gr := &MemoryReader{
		GameReader:     memory.NewGameReader(process),
		HWND:           window,
		supervisorName: supervisorName,
		cfg:            cfg,
		logger:         logger,
	}

	gr.updateWindowPositionData()

	return gr, nil
}

func (gd *MemoryReader) MapSeed() uint {
	return gd.mapSeed
}

func (gd *MemoryReader) FetchMapData() error {
	d := gd.GameReader.GetData()
	gd.mapSeed, _ = gd.getMapSeed(d.PlayerUnit.Address)
	t := time.Now()
	gd.logger.Debug("Fetching map data...", slog.Uint64("seed", uint64(gd.mapSeed)), slog.String("difficulty", string(config.Characters[gd.supervisorName].Game.Difficulty)))

	mapData, err := map_client.GetMapData(strconv.Itoa(int(gd.mapSeed)), config.Characters[gd.supervisorName].Game.Difficulty)
	if err != nil {
		return fmt.Errorf("error fetching map data: %w", err)
	}

	areas := make(map[area.ID]AreaData)
	var mu sync.Mutex
	g := errgroup.Group{}
	for _, lvl := range mapData {
		g.Go(func() error {
			cg := lvl.CollisionGrid()
			resultGrid := make([][]CollisionType, lvl.Size.Height)
			for i := range resultGrid {
				resultGrid[i] = make([]CollisionType, lvl.Size.Width)
			}

			for y := 0; y < lvl.Size.Height; y++ {
				for x := 0; x < lvl.Size.Width; x++ {
					if cg[y][x] {
						resultGrid[y][x] = CollisionTypeWalkable
					} else {
						resultGrid[y][x] = CollisionTypeNonWalkable
					}
				}
			}

			npcs, exits, objects, rooms := lvl.NPCsExitsAndObjects()
			grid := NewGrid(resultGrid, lvl.Offset.X, lvl.Offset.Y)
			mu.Lock()
			areas[area.ID(lvl.ID)] = AreaData{
				Area:           area.ID(lvl.ID),
				Name:           lvl.Name,
				NPCs:           npcs,
				AdjacentLevels: exits,
				Objects:        objects,
				Rooms:          rooms,
				Grid:           grid,
			}
			mu.Unlock()

			return nil
		})
	}

	_ = g.Wait()

	gd.cachedMapData = areas
	gd.logger.Debug("Fetch completed", slog.Int64("ms", time.Since(t).Milliseconds()))

	return nil
}

func (gd *MemoryReader) updateWindowPositionData() {
	// Client origin in screen coordinates (for translating client->screen clicks).
	point := win.POINT{}
	win.ClientToScreen(gd.HWND, &point)
	gd.WindowLeftX = int(point.X)
	gd.WindowTopY = int(point.Y)

	// True client size via GetClientRect — fullscreen/maximized safe. WINDOWPLACEMENT's
	// RcNormalPosition only holds the RESTORED window size (default 854x480), which is wrong
	// whenever D2R runs maximized/fullscreen and shifts every world->screen click off-center.
	rect := win.RECT{}
	if win.GetClientRect(gd.HWND, &rect) {
		gd.GameAreaSizeX = int(rect.Right - rect.Left)
		gd.GameAreaSizeY = int(rect.Bottom - rect.Top)
	}
}

func (gd *MemoryReader) GetData() Data {
	d := gd.GameReader.GetData()
	currentArea, ok := gd.cachedMapData[d.PlayerUnit.Area]
	if ok {
		// This hacky thing is because sometimes if the objects are far away we can not fetch them, basically WP.
		memObjects := gd.Objects(d.PlayerUnit.Position, d.HoverData)
		for _, clientObject := range currentArea.Objects {
			found := false
			for _, obj := range memObjects {
				// Only consider it a duplicate if same name AND same position
				if obj.Name == clientObject.Name && obj.Position.X == clientObject.Position.X && obj.Position.Y == clientObject.Position.Y {
					found = true
					break
				}
			}
			if !found {
				memObjects = append(memObjects, clientObject)
			}
		}

		d.AreaOrigin = data.Position{X: currentArea.OffsetX, Y: currentArea.OffsetY}
		d.NPCs = currentArea.NPCs
		d.AdjacentLevels = currentArea.AdjacentLevels
		d.Rooms = currentArea.Rooms
		d.Objects = memObjects
	}

	var cfgCopy config.CharacterCfg
	if gd.cfg != nil {
		cfgCopy = *gd.cfg
	}

	return Data{
		Data:         d,
		CharacterCfg: cfgCopy,
		AreaData:     currentArea,
		Areas:        gd.cachedMapData,
	}
}

// LiveLevelFrame is the current level's placement read straight from D2R memory.
// koolo-map's per-level Offset is the classic generator's DrlgLevel.PosX*5; the live D2R
// DrlgLevel.PosX*5 is the exact same semantic value, so it aligns the generated grid with no
// search or movement. Positions/sizes are converted to world SUBTILES (raw tile value * 5).
type LiveLevelFrame struct {
	Area                                 int
	OriginX, OriginY                     int  // world subtiles (PosX*5, PosY*5)
	SizeX, SizeY                         int  // world subtiles (SizeX*5, SizeY*5)
	RawPosX, RawPosY, RawSizeX, RawSizeY uint // raw tile values (pre-*5), for probe/validation
}

// ReadLiveLevelFrame walks the proven unit->path->room1->room2->level chain (same offsets d2go
// already uses) and reads DrlgLevel PosX/PosY/SizeX/SizeY at +0x24/+0x28/+0x2C/+0x30.
func (gd *MemoryReader) ReadLiveLevelFrame() (LiveLevelFrame, error) {
	d := gd.GameReader.GetData()
	playerUnit := d.PlayerUnit.Address
	if playerUnit == 0 {
		return LiveLevelFrame{}, errors.New("no player unit address")
	}
	rd := func(a uintptr) uintptr { return uintptr(gd.Process.ReadUInt(a, memory.Uint64)) }
	path := rd(playerUnit + 0x38)
	if path == 0 {
		return LiveLevelFrame{}, errors.New("null path pointer")
	}
	room1 := rd(path + 0x20)
	room2 := rd(room1 + 0x18)
	level := rd(room2 + 0x90)
	if level == 0 {
		return LiveLevelFrame{}, errors.New("null level pointer")
	}
	u32 := func(off uintptr) uint { return gd.Process.ReadUInt(level+off, memory.Uint32) }
	f := LiveLevelFrame{
		Area:     int(u32(0x1F8)),
		RawPosX:  u32(0x24),
		RawPosY:  u32(0x28),
		RawSizeX: u32(0x2C),
		RawSizeY: u32(0x30),
	}
	f.OriginX, f.OriginY = int(f.RawPosX)*5, int(f.RawPosY)*5
	f.SizeX, f.SizeY = int(f.RawSizeX)*5, int(f.RawSizeY)*5
	return f, nil
}

func (gd *MemoryReader) getMapSeed(playerUnit uintptr) (uint, error) {
	actPtr := uintptr(gd.Process.ReadUInt(playerUnit+0x20, memory.Uint64))
	actMiscPtr := uintptr(gd.Process.ReadUInt(actPtr+0x78, memory.Uint64))

	dwInitSeedHash1 := gd.Process.ReadUInt(actMiscPtr+0x840, memory.Uint32)
	//dwInitSeedHash2 := uintptr(gd.Process.ReadUInt(actMiscPtr+0x844, memory.Uint32))
	dwEndSeedHash1 := gd.Process.ReadUInt(actMiscPtr+0x868, memory.Uint32)

	mapSeed, found := utils.GetMapSeed(dwInitSeedHash1, dwEndSeedHash1)
	if !found {
		return 0, errors.New("error calculating map seed")
	}

	return mapSeed, nil
}
