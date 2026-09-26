package game

import (
	"errors"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
)

// ---------------------------------------------------------------- live DRLG presets
//
// THE SEED-FREE MAP (2026-09-26). koolo-map regenerates a level from the map
// seed with vanilla data; on this build the seed read is stuck (466817790 on
// every relog) and the mod's randomized areas never matched it. The game
// itself already holds the truth: on level init it builds every Room2 of the
// level and each Room2 carries its PRESET UNITS — the waypoint, quest chests,
// the journal, the Orifice, stairs and cave mouths — at exact room-relative
// positions. Read them and the map oracle's guesses are unnecessary.
//
// Layout on this D2R build (probed with cmd/drlgprobe; validated in the Rogue
// Encampment: waypoint preset 119 at (5499,4689) == the live waypoint unit):
//
//	DrlgLevel +0x010 first Room2   +0x1B8 next initialized level   +0x1F8 LevelNo
//	Room2     +0x048 next Room2    +0x060..0x6C pos/size (tiles)    +0x098 first preset
//	Preset    +0x004 txtFileNo     +0x008 x (subtiles in room)      +0x010 next
//	          +0x020 unit type     +0x024 y (subtiles in room)
const (
	dlLevelNext  = 0x1B8
	rgRoom2First = 0x98 // Room2 -> first PresetUnit
	rgRoom2Tiles = 0x78 // Room2 -> first RoomTile {dest Room2 +0, next +8}
	psTxt        = 0x04
	psX          = 0x08
	psNext       = 0x10
	psType       = 0x20
	psY          = 0x24

	dlMaxLevels  = 64
	dlMaxPresets = 4096
)

// Preset unit types (D2 unit type numbering).
const (
	PresetMonster = 1
	PresetObject  = 2
	PresetTile    = 5
)

// LivePreset is one preset unit of a level, in world subtiles.
type LivePreset struct {
	Level area.ID
	Type  int
	Txt   int
	Pos   data.Position
}

// LiveLevel is one initialized level: its rooms and presets.
type LiveLevel struct {
	ID      area.ID
	Origin  data.Position // world subtiles
	Size    data.Position // world subtiles
	Rooms   []TileRect
	Presets []LivePreset
	Exits   []data.Level // room-tile warps (stairs, cave mouths): exact, lazily filled near the player
}

// currentLevelPtr follows unit->path->room1->room2->level.
func (gd *MemoryReader) currentLevelPtr() (uintptr, error) {
	pu := gd.GameReader.GetData().PlayerUnit.Address
	if pu == 0 {
		return 0, errors.New("no player unit address")
	}
	path := gd.rgPtr(pu + rgUnitPath)
	if path == 0 {
		return 0, errors.New("null path")
	}
	room1 := gd.rgPtr(path + rgPathRoom1)
	if room1 == 0 {
		return 0, errors.New("null room1")
	}
	room2 := gd.rgPtr(room1 + rgRoom1Room2)
	if room2 == 0 {
		return 0, errors.New("null room2")
	}
	lvl := gd.rgPtr(room2 + rgRoom2Level)
	if lvl == 0 {
		return 0, errors.New("null level")
	}
	return lvl, nil
}

// readLevel reads one DrlgLevel's rooms and presets.
func (gd *MemoryReader) readLevel(lvl uintptr) (LiveLevel, bool) {
	id := area.ID(gd.rgU32(lvl + rgLevelNo))
	if id <= 0 || id > 1000 {
		return LiveLevel{}, false
	}
	ll := LiveLevel{
		ID:     id,
		Origin: data.Position{X: int(gd.rgU32(lvl+0x24)) * 5, Y: int(gd.rgU32(lvl+0x28)) * 5},
		Size:   data.Position{X: int(gd.rgU32(lvl+0x2C)) * 5, Y: int(gd.rgU32(lvl+0x30)) * 5},
	}
	seen := map[uintptr]bool{}
	for r := gd.rgPtr(lvl + rgLevelRoom2); r != 0 && len(ll.Rooms) < rgMaxRooms && !seen[r]; r = gd.rgPtr(r + rgRoom2Next) {
		seen[r] = true
		rect := TileRect{
			X: int(gd.rgU32(r + rgRoom2PosX)), Y: int(gd.rgU32(r + rgRoom2PosY)),
			W: int(gd.rgU32(r + rgRoom2SizeX)), H: int(gd.rgU32(r + rgRoom2SizeY)),
		}
		if rect.W <= 0 || rect.H <= 0 || rect.W > 512 || rect.H > 512 {
			break // implausible: a stale or foreign pointer
		}
		ll.Rooms = append(ll.Rooms, rect)
		roomPresets := len(ll.Presets)
		for p, k := gd.rgPtr(r+rgRoom2First), 0; p != 0 && k < 256 && len(ll.Presets) < dlMaxPresets; p, k = gd.rgPtr(p+psNext), k+1 {
			t := int(gd.rgU32(p + psType))
			x, y := int(gd.rgU32(p+psX)), int(gd.rgU32(p+psY))
			if t != PresetMonster && t != PresetObject && t != PresetTile {
				continue
			}
			if x > rect.W*5+5 || y > rect.H*5+5 {
				continue // outside its room: not a preset we understand
			}
			ll.Presets = append(ll.Presets, LivePreset{
				Level: id, Type: t, Txt: int(gd.rgU32(p + psTxt)),
				Pos: data.Position{X: rect.X*5 + x, Y: rect.Y*5 + y},
			})
		}
		// Room tiles: each links this room to a room of another level. The
		// warp's own position is this room's tile preset (else its center).
		for t, k := gd.rgPtr(r+rgRoom2Tiles), 0; t != 0 && k < 8; t, k = gd.rgPtr(t+0x08), k+1 {
			dest := gd.rgPtr(t)
			if dest == 0 {
				continue
			}
			dl := gd.rgPtr(dest + rgRoom2Level)
			if dl == 0 {
				continue
			}
			to := area.ID(gd.rgU32(dl + rgLevelNo))
			if to <= 0 || to > 1000 || to == id {
				continue
			}
			pos := data.Position{X: rect.X*5 + rect.W*5/2, Y: rect.Y*5 + rect.H*5/2}
			for _, p := range ll.Presets[roomPresets:] {
				if p.Type == PresetTile {
					pos = p.Pos
					break
				}
			}
			ll.Exits = append(ll.Exits, data.Level{Area: to, Position: pos, IsEntrance: true})
		}
	}
	return ll, true
}

// ReadLiveLevels reads the current level and every initialized level chained
// after it, plus the levels of the current level's cross-level neighbour
// rooms (and their chains). Keyed by area.
func (gd *MemoryReader) ReadLiveLevels() (map[area.ID]LiveLevel, error) {
	cur, err := gd.currentLevelPtr()
	if err != nil {
		return nil, err
	}
	starts := []uintptr{cur}
	if g, err := gd.ReadCurrentRoomGraph(); err == nil {
		for addr := range g.External {
			if l := gd.rgPtr(addr + rgRoom2Level); l != 0 {
				starts = append(starts, l)
			}
		}
	}
	out := map[area.ID]LiveLevel{}
	visited := map[uintptr]bool{}
	for _, s := range starts {
		for l, k := s, 0; l != 0 && k < dlMaxLevels && !visited[l]; l, k = gd.rgPtr(l+dlLevelNext), k+1 {
			visited[l] = true
			if ll, ok := gd.readLevel(l); ok {
				if prev, dup := out[ll.ID]; !dup || len(ll.Presets) > len(prev.Presets) {
					out[ll.ID] = ll
				}
			}
		}
	}
	return out, nil
}

// liveCache keeps the last ReadLiveLevels for a short while: GetData runs
// several times per tick and a full read is a few thousand small reads.
type liveCache struct {
	mu     sync.Mutex
	at     time.Time
	area   area.ID
	levels map[area.ID]LiveLevel
}

const liveTTL = time.Second

// LiveLevelsCached serves ReadLiveLevels at most once per liveTTL (and again
// at once when the player's area changes). nil when the read failed.
func (gd *MemoryReader) LiveLevelsCached(cur area.ID) map[area.ID]LiveLevel {
	gd.live.mu.Lock()
	defer gd.live.mu.Unlock()
	if gd.live.levels != nil && gd.live.area == cur && time.Since(gd.live.at) < liveTTL {
		return gd.live.levels
	}
	lv, err := gd.ReadLiveLevels()
	if err != nil {
		lv = nil
	}
	gd.live.levels, gd.live.area, gd.live.at = lv, cur, time.Now()
	return lv
}

// liveObjects turns a level's object presets into map-style objects (ID 0:
// a preset, never a live unit) that no live unit already stands for.
func liveObjects(ll LiveLevel, live []data.Object) []data.Object {
	var out []data.Object
	for _, p := range ll.Presets {
		if p.Type != PresetObject {
			continue
		}
		name := object.Name(p.Txt)
		dup := false
		for _, o := range live {
			if o.Name == name && absInt(o.Position.X-p.Pos.X) <= 3 && absInt(o.Position.Y-p.Pos.Y) <= 3 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, data.Object{Name: name, Position: p.Pos})
		}
	}
	return out
}

// liveNPCs: the level's monster presets (town NPCs, quest bosses) REPLACE the
// map's entry for the same NPC — the map is seed-generated and the seed read
// is stuck (R-k2: Akara "at" (4532,4606), 900 tiles off, because even the
// Rogue Encampment layout is randomized). Map-only NPCs are kept.
func liveNPCs(ll LiveLevel, have data.NPCs) data.NPCs {
	live := data.NPCs{}
	for _, p := range ll.Presets {
		if p.Type != PresetMonster {
			continue
		}
		id := npc.ID(p.Txt)
		found := false
		for i := range live {
			if live[i].ID == id {
				live[i].Positions = append(live[i].Positions, p.Pos)
				found = true
				break
			}
		}
		if !found {
			live = append(live, data.NPC{ID: id, Positions: []data.Position{p.Pos}})
		}
	}
	out := live
	for _, n := range have {
		if _, ok := live.FindOne(n.ID); !ok {
			out = append(out, n)
		}
	}
	return out
}

// mergeExits: a live room-tile exit replaces the map's exit to the same area
// (the map's is seed-generated and wrong in randomized levels); the map's
// other exits stay as hints.
func mergeExits(live, mapped []data.Level) []data.Level {
	out := append([]data.Level(nil), live...)
	for _, m := range mapped {
		dup := false
		for _, l := range live {
			if l.Area == m.Area {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, m)
		}
	}
	return out
}
