package gamedata

import (
	"sort"
	"strconv"
	"strings"
)

// Level is one levels.txt row (ID = the Id column = the area id memory reports).
type Level struct {
	ID       int
	Key      string // Name column ("Act 2 - Desert 3")
	NameKey  string // LevelName (also the string key)
	Name     string // the in-game name ("Far Oasis")
	WarpName string // LevelWarp, resolved ("To The Far Oasis")
	Act      int    // 0-based, as the table stores it
	IsTown   bool

	// Waypoint is the waypoint index, -1 when the level has none (255 in the table).
	Waypoint int
	// MonLvl is the area level per difficulty: MonLvlEx when set (the
	// expansion game), else the classic MonLvl. In Normal the game spawns
	// regular monsters at their monstats Level; in Nightmare/Hell at this.
	MonLvl        [3]int
	MonLvlClassic [3]int
	// Vis/Warp: the neighbouring level ids reachable from here and the lvlwarp
	// id each connection uses (-1 / 0 unused slots are dropped from Exits).
	Vis   [8]int
	Warp  [8]int
	Exits []Exit

	Mon, NMon, UMon []string // monstats codes spawnable here (normal / NM+H / uniques)
	PreventTP       bool
	Teleport        int
	SizeX, SizeY    [3]int
}

// Exit is one Vis/Warp pair.
type Exit struct {
	Level int // destination level id
	Warp  int // lvlwarp id (-1 when the connection is an open border)
}

// LvlWarp is one lvlwarp.txt row: the clickable entrance box and walk-out.
type LvlWarp struct {
	ID   int
	Name string
	// SelectX/Y/DX/DY: the selection box in pixels relative to the warp's
	// tile origin (what a click must land inside).
	SelectX, SelectY, SelectDX, SelectDY int
	ExitWalkX, ExitWalkY                 int
	OffsetX, OffsetY                     int
	LitVersion                           bool
	Tiles                                int
	NoInteract                           bool
	Direction                            string // "b"/"l"/"r"
	UniqueID                             int
}

func (db *DB) loadLevels() {
	t := db.table("levels.txt")
	if t == nil {
		return
	}
	t.each(func(idx int, r row) {
		id, ok := r.intOK("Id")
		if !ok {
			return
		}
		l := &Level{ID: id, Key: r.str("Name"), NameKey: r.str("LevelName"), Act: r.int("Act"),
			Waypoint: r.intOr("Waypoint", 255), PreventTP: r.bool("PreventTownPortal"),
			Teleport: r.int("Teleport")}
		if l.Waypoint == 255 {
			l.Waypoint = -1
		}
		l.Name = db.Strings.NameIn("levels.json", l.NameKey, l.NameKey)
		if l.Name == "" {
			l.Name = l.Key
		}
		wk := r.str("LevelWarp")
		l.WarpName = db.Strings.NameIn("levels.json", wk, wk)
		l.IsTown = strings.HasSuffix(strings.ToLower(l.Key), "- town")
		for d := Normal; d <= Hell; d++ {
			l.MonLvlClassic[d] = r.int("MonLvl" + diffSuffix[d])
			l.MonLvl[d] = r.intOr("MonLvlEx"+diffSuffix[d], l.MonLvlClassic[d])
			l.SizeX[d] = r.int("SizeX" + diffSuffix[d])
			l.SizeY[d] = r.int("SizeY" + diffSuffix[d])
		}
		for i := 0; i < 8; i++ {
			n := strconv.Itoa(i)
			l.Vis[i] = r.int("Vis" + n)
			l.Warp[i] = r.intOr("Warp"+n, -1)
			if l.Vis[i] > 0 {
				l.Exits = append(l.Exits, Exit{Level: l.Vis[i], Warp: l.Warp[i]})
			}
		}
		for i := 1; i <= 25; i++ {
			n := strconv.Itoa(i)
			if s := r.str("mon" + n); s != "" {
				l.Mon = append(l.Mon, s)
			}
			if s := r.str("nmon" + n); s != "" {
				l.NMon = append(l.NMon, s)
			}
			if s := r.str("umon" + n); s != "" {
				l.UMon = append(l.UMon, s)
			}
		}
		db.Levels[id] = l
	})
}

func (db *DB) loadWarps() {
	t := db.table("lvlwarp.txt")
	if t == nil {
		return
	}
	t.each(func(idx int, r row) {
		id, ok := r.intOK("Id")
		if !ok {
			return
		}
		if _, dup := db.Warps[id]; dup {
			return
		}
		db.Warps[id] = &LvlWarp{ID: id, Name: r.str("Name"), SelectX: r.int("SelectX"),
			SelectY: r.int("SelectY"), SelectDX: r.int("SelectDX"), SelectDY: r.int("SelectDY"),
			ExitWalkX: r.int("ExitWalkX"), ExitWalkY: r.int("ExitWalkY"), OffsetX: r.int("OffsetX"),
			OffsetY: r.int("OffsetY"), LitVersion: r.bool("LitVersion"), Tiles: r.int("Tiles"),
			NoInteract: r.bool("NoInteract"), Direction: r.str("Direction"), UniqueID: r.int("UniqueId")}
	})
}

// Level is the level with that area id.
func (db *DB) Level(id int) *Level {
	if db == nil {
		return nil
	}
	return db.Levels[id]
}

// LevelByName finds a level by its in-game name or table key (case-insensitive).
func (db *DB) LevelByName(name string) *Level {
	if db == nil {
		return nil
	}
	for _, id := range db.LevelIDs() {
		l := db.Levels[id]
		if strings.EqualFold(l.Name, name) || strings.EqualFold(l.NameKey, name) || strings.EqualFold(l.Key, name) {
			return l
		}
	}
	return nil
}

// LevelIDs are the loaded level ids in ascending order.
func (db *DB) LevelIDs() []int {
	ids := make([]int, 0, len(db.Levels))
	for id := range db.Levels {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// Warp is the lvlwarp row with that id.
func (db *DB) Warp(id int) *LvlWarp {
	if db == nil {
		return nil
	}
	return db.Warps[id]
}

// WarpTo is the lvlwarp row the level `from` uses to reach `to` (nil when the
// connection is an open border or unknown).
func (db *DB) WarpTo(from, to int) *LvlWarp {
	l := db.Level(from)
	if l == nil {
		return nil
	}
	for _, e := range l.Exits {
		if e.Level == to && e.Warp >= 0 {
			return db.Warp(e.Warp)
		}
	}
	return nil
}
