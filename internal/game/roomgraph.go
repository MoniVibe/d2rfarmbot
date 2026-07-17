package game

import (
	"errors"
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/memory"
)

// Live DRLG room-graph reader (advisor design). The game's own room graph knows area topology
// before transition units stream in: enumerate the current level's Room2 nodes, inspect each
// Room2's neighbors, and any neighbor whose LevelNo differs is a cross-level border — that tells
// us which side the destination area is on and where to walk, with no "wander until it loads".
//
// Offsets are for this D2R build; the unit->path->room1->room2->level chain matches d2go's proven
// reads. Room positions/sizes are TILE units (×5 for world subtiles).
const (
	rgUnitPath    = 0x38
	rgPathRoom1   = 0x20
	rgRoom1Room2  = 0x18
	rgLevelRoom2  = 0x10  // DrlgLevel -> first Room2
	rgLevelNo     = 0x1F8 // DrlgLevel -> LevelNo
	rgRoom2Near   = 0x10  // Room2 -> near-room pointer array
	rgRoom2NearCA = 0x18  // neighbor count candidate A (uint32)
	rgRoom2NearCB = 0x50  // neighbor count candidate B (uint16)
	rgRoom2Next   = 0x48
	rgRoom2Room1  = 0x58 // loaded Room1, 0 when unloaded
	rgRoom2PosX   = 0x60
	rgRoom2PosY   = 0x64
	rgRoom2SizeX  = 0x68
	rgRoom2SizeY  = 0x6C
	rgRoom2Level  = 0x90

	rgMaxRooms     = 512
	rgMaxNeighbors = 32
)

type TileRect struct{ X, Y, W, H int }

type LiveRoom struct {
	Address   uintptr
	LevelID   area.ID
	Rect      TileRect
	Room1Ptr  uintptr // 0 = not streamed in yet
	Neighbors []uintptr
}

type LiveRoomGraph struct {
	LevelID  area.ID
	Current  uintptr
	Rooms    map[uintptr]LiveRoom
	External map[uintptr]LiveRoom // neighbor rooms belonging to OTHER levels
}

func (gd *MemoryReader) rgPtr(addr uintptr) uintptr {
	return uintptr(gd.Process.ReadUInt(addr, memory.Uint64))
}
func (gd *MemoryReader) rgU32(addr uintptr) uint {
	return gd.Process.ReadUInt(addr, memory.Uint32)
}

func (gd *MemoryReader) readRoomHeader(addr uintptr) (LiveRoom, error) {
	if addr == 0 {
		return LiveRoom{}, errors.New("null Room2")
	}
	levelPtr := gd.rgPtr(addr + rgRoom2Level)
	if levelPtr == 0 {
		return LiveRoom{}, fmt.Errorf("Room2 %x has null level", addr)
	}
	r := LiveRoom{
		Address:  addr,
		LevelID:  area.ID(gd.rgU32(levelPtr + rgLevelNo)),
		Room1Ptr: gd.rgPtr(addr + rgRoom2Room1),
		Rect: TileRect{
			X: int(gd.rgU32(addr + rgRoom2PosX)),
			Y: int(gd.rgU32(addr + rgRoom2PosY)),
			W: int(gd.rgU32(addr + rgRoom2SizeX)),
			H: int(gd.rgU32(addr + rgRoom2SizeY)),
		},
	}
	if r.LevelID <= 0 || r.LevelID > 1000 {
		return LiveRoom{}, fmt.Errorf("Room2 %x implausible level %d", addr, r.LevelID)
	}
	if r.Rect.W <= 0 || r.Rect.H <= 0 || r.Rect.W > 512 || r.Rect.H > 512 {
		return LiveRoom{}, fmt.Errorf("Room2 %x implausible rect %+v", addr, r.Rect)
	}
	return r, nil
}

// The neighbor-count field differs between D2R layouts; validate both candidates on this build.
func (gd *MemoryReader) validateNeighborArray(list uintptr, count int) bool {
	if count < 0 || count > rgMaxNeighbors {
		return false
	}
	if count == 0 {
		return true
	}
	if list == 0 {
		return false
	}
	check := count
	if check > 3 {
		check = 3
	}
	for i := 0; i < check; i++ {
		ptr := gd.rgPtr(list + uintptr(i*8))
		if ptr == 0 {
			return false
		}
		if _, err := gd.readRoomHeader(ptr); err != nil {
			return false
		}
	}
	return true
}

func (gd *MemoryReader) readValidatedNeighborCount(room, nearList uintptr) (int, error) {
	a := int(gd.rgU32(room + rgRoom2NearCA))
	if gd.validateNeighborArray(nearList, a) {
		return a, nil
	}
	b := int(gd.Process.ReadUInt(room+rgRoom2NearCB, memory.Uint16))
	if gd.validateNeighborArray(nearList, b) {
		return b, nil
	}
	return 0, fmt.Errorf("could not validate Room2 neighbor count at %x", room)
}

// ReadCurrentRoomGraph enumerates every Room2 in the player's current level and records neighbors,
// tagging neighbors that belong to other levels (cross-level borders) in External.
func (gd *MemoryReader) ReadCurrentRoomGraph() (*LiveRoomGraph, error) {
	playerUnit := gd.GameReader.GetData().PlayerUnit.Address
	if playerUnit == 0 {
		return nil, errors.New("no player unit address")
	}
	path := gd.rgPtr(playerUnit + rgUnitPath)
	if path == 0 {
		return nil, errors.New("null path")
	}
	room1 := gd.rgPtr(path + rgPathRoom1)
	currentRoom2 := gd.rgPtr(room1 + rgRoom1Room2)
	if currentRoom2 == 0 {
		return nil, errors.New("null current Room2")
	}
	levelPtr := gd.rgPtr(currentRoom2 + rgRoom2Level)
	if levelPtr == 0 {
		return nil, errors.New("null DrlgLevel")
	}
	levelID := area.ID(gd.rgU32(levelPtr + rgLevelNo))
	firstRoom := gd.rgPtr(levelPtr + rgLevelRoom2)
	if firstRoom == 0 {
		return nil, errors.New("null first Room2")
	}

	g := &LiveRoomGraph{
		LevelID:  levelID,
		Current:  currentRoom2,
		Rooms:    make(map[uintptr]LiveRoom),
		External: make(map[uintptr]LiveRoom),
	}
	seen := make(map[uintptr]struct{})
	roomPtr := firstRoom
	for len(g.Rooms) < rgMaxRooms && roomPtr != 0 {
		if _, dup := seen[roomPtr]; dup {
			break
		}
		seen[roomPtr] = struct{}{}

		room, err := gd.readRoomHeader(roomPtr)
		if err != nil {
			return nil, err
		}
		nearList := gd.rgPtr(roomPtr + rgRoom2Near)
		nearCount, err := gd.readValidatedNeighborCount(roomPtr, nearList)
		if err != nil {
			return nil, err
		}
		for i := 0; i < nearCount; i++ {
			nearPtr := gd.rgPtr(nearList + uintptr(i*8))
			if nearPtr == 0 {
				continue
			}
			room.Neighbors = append(room.Neighbors, nearPtr)
			if near, err := gd.readRoomHeader(nearPtr); err == nil && near.LevelID != levelID {
				g.External[nearPtr] = near
			}
		}
		g.Rooms[roomPtr] = room
		roomPtr = gd.rgPtr(roomPtr + rgRoom2Next)
	}
	if len(g.Rooms) == rgMaxRooms {
		return nil, errors.New("Room2 enumeration hit safety cap")
	}
	return g, nil
}

// LiveCollision is a room's actual collision grid read from D2R memory (mod-accurate, unlike
// koolo-map's classic-data grid). Cells are world subtiles; low bit of the uint16 mask = blocked.
type LiveCollision struct {
	SubX, SubY, W, H int
	MaskPtr          uintptr
}

const rgRoom1Coll = 0x38 // Room1 -> collision struct

// ReadRoom1Collision reads a Room1's collision header (nil-safe; ok=false if not loaded/implausible).
func (gd *MemoryReader) ReadRoom1Collision(room1 uintptr) (LiveCollision, bool) {
	if room1 == 0 {
		return LiveCollision{}, false
	}
	coll := gd.rgPtr(room1 + rgRoom1Coll)
	if coll == 0 {
		return LiveCollision{}, false
	}
	c := LiveCollision{
		SubX:    int(gd.rgU32(coll + 0x00)),
		SubY:    int(gd.rgU32(coll + 0x04)),
		W:       int(gd.rgU32(coll + 0x08)),
		H:       int(gd.rgU32(coll + 0x0C)),
		MaskPtr: gd.rgPtr(coll + 0x20),
	}
	if c.W <= 0 || c.H <= 0 || c.W > 2000 || c.H > 2000 || c.MaskPtr == 0 {
		return LiveCollision{}, false
	}
	return c, true
}

// LiveBlockedAt reports whether world subtile (x,y) is blocked in this room's live collision.
// ok=false if the point is outside the room's collision rectangle.
func (gd *MemoryReader) LiveBlockedAt(c LiveCollision, x, y int) (blocked, ok bool) {
	lx, ly := x-c.SubX, y-c.SubY
	if lx < 0 || ly < 0 || lx >= c.W || ly >= c.H {
		return false, false
	}
	v := gd.Process.ReadUInt(c.MaskPtr+uintptr((ly*c.W+lx)*2), memory.Uint16)
	return v&1 != 0, true
}

// BuildLiveGrid assembles a navigation Grid for the current level straight from D2R's live
// room collision — the authoritative, mod-accurate source. koolo-map's generated grid is
// wrong for mod-altered areas (e.g. Reimagined's Act2 Far Oasis reads the player's own
// standing tile as blocked); this reads the real thing. Cells covered by a streamed-in room
// are set walkable/blocked from that room's mask; cells in rooms not yet streamed stay
// NonWalkable (unknown = blocked, safe — they fill in as the player approaches and rooms load).
// Origin/size come from the live DrlgLevel frame, so the grid is aligned by construction.
func (gd *MemoryReader) BuildLiveGrid() (*Grid, int, error) {
	lf, err := gd.ReadLiveLevelFrame()
	if err != nil {
		return nil, 0, err
	}
	if lf.SizeX <= 0 || lf.SizeY <= 0 || lf.SizeX > 4000 || lf.SizeY > 4000 {
		return nil, 0, fmt.Errorf("implausible live level size %dx%d", lf.SizeX, lf.SizeY)
	}
	graph, err := gd.ReadCurrentRoomGraph()
	if err != nil {
		return nil, 0, err
	}
	W, H, ox, oy := lf.SizeX, lf.SizeY, lf.OriginX, lf.OriginY
	cg := make([][]CollisionType, H)
	for y := range cg {
		cg[y] = make([]CollisionType, W) // zero value = CollisionTypeNonWalkable (unknown = blocked)
	}
	loaded := 0
	for _, room := range graph.Rooms {
		if room.Room1Ptr == 0 || int(room.LevelID) != lf.Area {
			continue
		}
		c, ok := gd.ReadRoom1Collision(room.Room1Ptr)
		if !ok {
			continue
		}
		// Bulk-read the whole mask in one shot (W*H uint16) — per-cell reads would be
		// thousands of syscalls per room.
		buf := gd.Process.ReadBytesFromMemory(c.MaskPtr, uint(c.W*c.H*2))
		if len(buf) < c.W*c.H*2 {
			continue
		}
		for ly := 0; ly < c.H; ly++ {
			for lx := 0; lx < c.W; lx++ {
				gx := c.SubX + lx - ox
				gy := c.SubY + ly - oy
				if gx < 0 || gy < 0 || gx >= W || gy >= H {
					continue
				}
				idx := (ly*c.W + lx) * 2
				v := uint16(buf[idx]) | uint16(buf[idx+1])<<8
				if v&1 == 0 { // low bit clear = walkable
					cg[gy][gx] = CollisionTypeWalkable
				}
			}
		}
		loaded++
	}
	if loaded == 0 {
		return nil, 0, errors.New("no rooms with streamed-in collision")
	}
	return NewGrid(cg, ox, oy), loaded, nil
}

// CountLoadedRooms reports how many rooms in the graph have their Room1 collision streamed in.
func (g *LiveRoomGraph) CountLoadedRooms() (loaded, total int) {
	for _, r := range g.Rooms {
		total++
		if r.Room1Ptr != 0 {
			loaded++
		}
	}
	return
}

type BorderSide uint8

const (
	BorderLeft BorderSide = iota
	BorderRight
	BorderTop
	BorderBottom
)

func (s BorderSide) String() string {
	switch s {
	case BorderLeft:
		return "left(W)"
	case BorderRight:
		return "right(E)"
	case BorderTop:
		return "top(N)"
	case BorderBottom:
		return "bottom(S)"
	}
	return "?"
}

type LiveBorder struct {
	FromRoom, ToRoom   uintptr
	ToArea             area.ID
	Side               BorderSide
	SpanStart, SpanEnd int // tile units along the boundary
	FromRect, ToRect   TileRect
}

func rgMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func rgMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sharedBorder(from, to TileRect) (BorderSide, int, int, bool) {
	oy0, oy1 := rgMax(from.Y, to.Y), rgMin(from.Y+from.H, to.Y+to.H)
	if to.X+to.W == from.X && oy1 > oy0 {
		return BorderLeft, oy0, oy1, true
	}
	if from.X+from.W == to.X && oy1 > oy0 {
		return BorderRight, oy0, oy1, true
	}
	ox0, ox1 := rgMax(from.X, to.X), rgMin(from.X+from.W, to.X+to.W)
	if to.Y+to.H == from.Y && ox1 > ox0 {
		return BorderTop, ox0, ox1, true
	}
	if from.Y+from.H == to.Y && ox1 > ox0 {
		return BorderBottom, ox0, ox1, true
	}
	return 0, 0, 0, false
}

// BordersTo returns every live room border from the current level into the destination area.
func (g *LiveRoomGraph) BordersTo(dst area.ID) []LiveBorder {
	var out []LiveBorder
	for _, room := range g.Rooms {
		for _, nearPtr := range room.Neighbors {
			near, ok := g.External[nearPtr]
			if !ok || near.LevelID != dst {
				continue
			}
			if side, start, end, touching := sharedBorder(room.Rect, near.Rect); touching {
				out = append(out, LiveBorder{
					FromRoom: room.Address, ToRoom: near.Address, ToArea: near.LevelID,
					Side: side, SpanStart: start, SpanEnd: end,
					FromRect: room.Rect, ToRect: near.Rect,
				})
			}
		}
	}
	return out
}

// CrossingAt returns an approach point (just inside the boundary) and a cross point (just past it),
// in world SUBTILES, at the given orthogonal tile coordinate along the border.
func (b LiveBorder) CrossingAt(orthTile int) (approach, cross data.Position) {
	orth := orthTile * 5
	switch b.Side {
	case BorderLeft:
		x := b.FromRect.X * 5
		return data.Position{X: x + 4, Y: orth}, data.Position{X: x - 7, Y: orth}
	case BorderRight:
		x := (b.FromRect.X + b.FromRect.W) * 5
		return data.Position{X: x - 4, Y: orth}, data.Position{X: x + 7, Y: orth}
	case BorderTop:
		y := b.FromRect.Y * 5
		return data.Position{X: orth, Y: y + 4}, data.Position{X: orth, Y: y - 7}
	case BorderBottom:
		y := (b.FromRect.Y + b.FromRect.H) * 5
		return data.Position{X: orth, Y: y - 4}, data.Position{X: orth, Y: y + 7}
	}
	return data.Position{}, data.Position{}
}
