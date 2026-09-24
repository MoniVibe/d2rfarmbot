package gamedata

import "strconv"

// ObjKind classifies an object by what operating it does (OperateFn).
type ObjKind string

const (
	ObjOther          ObjKind = "other"
	ObjContainer      ObjKind = "container"       // chests, caskets, urns, barrels, crates, racks, stashes in the world
	ObjQuestContainer ObjKind = "quest-container" // Horadric Cube / Scroll / Staff of Kings / Khalim chests
	ObjShrine         ObjKind = "shrine"
	ObjWell           ObjKind = "well"
	ObjDoor           ObjKind = "door"
	ObjWaypoint       ObjKind = "waypoint"
	ObjPortal         ObjKind = "portal"
	ObjStash          ObjKind = "stash" // the player's bank
	ObjQuest          ObjKind = "quest" // altars, orifice, seals, stones, levers...
)

// operateKinds maps OperateFn → kind (verified against the mod's objects.txt:
// 4 = every "chest", 23 = every Waypoint, 32 = Bank, 39/40/41 = the Horadric
// Cube / Scroll / Staff of Kings chests, 24 = TaintedSunShrine, 25 = SevenTombsReceptacle).
var operateKinds = map[int]ObjKind{
	1: ObjContainer, 3: ObjContainer, 4: ObjContainer, 5: ObjContainer, 7: ObjContainer,
	14: ObjContainer, 19: ObjContainer, 20: ObjContainer, 51: ObjContainer, 68: ObjContainer,
	2:  ObjShrine,
	22: ObjWell,
	8:  ObjDoor, 18: ObjDoor, 29: ObjDoor, 61: ObjDoor,
	23: ObjWaypoint,
	15: ObjPortal, 27: ObjPortal, 34: ObjPortal, 43: ObjPortal, 46: ObjPortal, 66: ObjPortal,
	70: ObjPortal, 71: ObjPortal, 72: ObjPortal, 73: ObjPortal,
	32: ObjStash,
	39: ObjQuestContainer, 40: ObjQuestContainer, 41: ObjQuestContainer,
	57: ObjQuestContainer, 58: ObjQuestContainer, 59: ObjQuestContainer,
	6: ObjQuest, 9: ObjQuest, 10: ObjQuest, 12: ObjQuest, 21: ObjQuest, 24: ObjQuest, 25: ObjQuest,
	28: ObjQuest, 31: ObjQuest, 42: ObjQuest, 44: ObjQuest, 45: ObjQuest, 49: ObjQuest,
	52: ObjQuest, 53: ObjQuest, 54: ObjQuest, 55: ObjQuest, 56: ObjQuest, 65: ObjQuest, 74: ObjQuest,
}

// Object is one objects.txt row. ID is the object class id memory reports.
type Object struct {
	ID      int
	Class   string // the Class column ("HoradricCubeChest")
	NameKey string
	Name    string

	// SizeX/SizeY: footprint in subtiles. Left/Top/Width/Height: the pixel
	// selection box relative to the object's origin (0 width = the sprite
	// selects). Xoffset/Yoffset: sprite draw offset. Xspace/Yspace: spacing.
	SizeX, SizeY             int
	Left, Top, Width, Height int
	Xoffset, Yoffset         int
	Xspace, Yspace           int
	NameOffset               int

	Selectable [8]bool // per mode
	IsDoor     bool
	Attackable bool
	OperateFn  int
	PopulateFn int
	InitFn     int
	ClientFn   int
	SubClass   int
	ShrineFn   int
	OpenWarp   bool
	AutoMap    int
	Kind       ObjKind
}

func (db *DB) loadObjects() {
	t := db.table("objects.txt")
	if t == nil {
		return
	}
	t.each(func(idx int, r row) {
		o := &Object{ID: idx, Class: r.str("Class"), NameKey: r.str("Name"), SizeX: r.int("SizeX"),
			SizeY: r.int("SizeY"), Left: r.int("Left"), Top: r.int("Top"), Width: r.int("Width"),
			Height: r.int("Height"), Xoffset: r.int("Xoffset"), Yoffset: r.int("Yoffset"),
			Xspace: r.int("Xspace"), Yspace: r.int("Yspace"), NameOffset: r.int("NameOffset"),
			IsDoor: r.bool("IsDoor"), Attackable: r.bool("IsAttackable0"), OperateFn: r.intOr("OperateFn", 0),
			PopulateFn: r.int("PopulateFn"), InitFn: r.int("InitFn"), ClientFn: r.int("ClientFn"),
			SubClass: r.int("SubClass"), ShrineFn: r.int("ShrineFunction"), OpenWarp: r.bool("OpenWarp"),
			AutoMap: r.int("AutoMap")}
		o.Name = db.Strings.Name(o.NameKey, o.NameKey)
		for i := 0; i < 8; i++ {
			o.Selectable[i] = r.bool("Selectable" + strconv.Itoa(i))
		}
		o.Kind = operateKinds[o.OperateFn]
		if o.Kind == "" {
			o.Kind = ObjOther
		}
		if o.IsDoor && o.Kind == ObjOther {
			o.Kind = ObjDoor
		}
		db.Objects = append(db.Objects, o)
		if _, dup := db.objByClass[o.Class]; !dup && o.Class != "" {
			db.objByClass[o.Class] = o
		}
	})
}

// Object is the row for an object class id.
func (db *DB) Object(id int) *Object {
	if db == nil || id < 0 || id >= len(db.Objects) {
		return nil
	}
	return db.Objects[id]
}

// ObjectByClass is the row with that Class ("HoradricCubeChest").
func (db *DB) ObjectByClass(class string) *Object {
	if db == nil {
		return nil
	}
	return db.objByClass[class]
}
