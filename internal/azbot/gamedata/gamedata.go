// Package gamedata is azbot's read-only knowledge of the game's own tables:
// the mod's excel .txt files (items, monsters, levels, warps, objects, skills)
// and its English string tables, loaded once at startup. Pure: stdlib only, no
// memory reads, no input.
//
// ROW NUMBERING is the game's: a record's ID is what memory reports as its
// class id (txtFileNo). Tables with an explicit id column (levels Id, lvlwarp
// Id, superuniques hcIdx) use it; the others count data rows, skipping blank
// lines and the "Expansion" divider rows exactly as the game does. Item class
// ids run weapons, then armor, then misc in one space.
//
// The data files are the mod author's and Blizzard's: they are read from the
// player's install at runtime and never committed (tests use small synthetic
// fixtures; the real-data test runs only when AZBOT_MODDATA points at them).
package gamedata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// DefaultRoot is the owner's D2RMM output (D2R launched -mod D2RMM -txt "").
const DefaultRoot = `C:\Program Files (x86)\Diablo II Resurrected\mods\D2RMM\D2RMM.mpq`

// Paths selects the data. Root may be a D2R data root (…/D2RMM.mpq, holding
// data/global/excel and data/local/lng/strings), its data/ directory, or a
// flat dump (excel/, strings/, modinfo_*.json). Excel/Strings override.
type Paths struct {
	Root    string
	Excel   string
	Strings string
}

// ModInfo is the mod's name and version (modinfo*.json), when found.
type ModInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (m ModInfo) String() string {
	s := strings.TrimSpace(m.Name + " " + m.Version)
	if s == "" {
		return "unknown"
	}
	return s
}

// DB is the loaded knowledge. Read-only after Load.
type DB struct {
	ExcelDir   string
	StringsDir string
	Mod        ModInfo

	Strings *Strings

	Items    []*Item // index = item class id
	Types    map[string]*ItemType
	Uniques  []*UniqueItem
	SetItems []*SetItem

	Monsters     []*Monster // index = monster class id
	SuperUniques []*SuperUnique

	Levels map[int]*Level
	Warps  map[int]*LvlWarp

	Objects []*Object // index = object class id

	Skills []*Skill // index = skill id

	// Missing lists the tables (and the strings directory) that were not
	// found; their records are simply absent.
	Missing []string

	tables     map[string]*table
	itemByCode map[string]*Item
	monByCode  map[string]*Monster
	objByClass map[string]*Object
	skillByKey map[string]*Skill
}

// tableFiles are the excel tables Load reads.
var tableFiles = []string{"itemtypes.txt", "weapons.txt", "armor.txt", "misc.txt", "uniqueitems.txt",
	"setitems.txt", "monstats.txt", "monstats2.txt", "superuniques.txt", "levels.txt", "lvlwarp.txt",
	"objects.txt", "skills.txt", "skilldesc.txt"}

// Load reads the tables under dir (see Paths.Root for the accepted layouts).
func Load(dir string) (*DB, error) { return LoadPaths(Paths{Root: dir}) }

// LoadPaths reads the tables. It fails only when no excel table can be found
// at all; a missing table or strings directory is recorded in DB.Missing.
func LoadPaths(p Paths) (*DB, error) {
	db := &DB{Types: map[string]*ItemType{}, Levels: map[int]*Level{}, Warps: map[int]*LvlWarp{},
		tables: map[string]*table{}, itemByCode: map[string]*Item{}, monByCode: map[string]*Monster{},
		objByClass: map[string]*Object{}, skillByKey: map[string]*Skill{}}
	db.ExcelDir = p.Excel
	if db.ExcelDir == "" {
		db.ExcelDir = firstDirWith(excelCandidates(p.Root), "weapons.txt", "levels.txt", "monstats.txt")
	}
	if db.ExcelDir == "" {
		return nil, fmt.Errorf("gamedata: no excel tables under %q: %w", p.Root, os.ErrNotExist)
	}
	db.StringsDir = p.Strings
	if db.StringsDir == "" {
		db.StringsDir = firstDirWith(stringsCandidates(p.Root, db.ExcelDir), "item-names.json", "skills.json", "monsters.json")
	}
	if db.StringsDir != "" {
		s, err := loadStrings(db.StringsDir)
		if err != nil {
			return nil, err
		}
		db.Strings = s
	} else {
		db.Strings = newStrings()
		db.Missing = append(db.Missing, "strings/")
	}
	found := 0
	for _, name := range tableFiles {
		t, err := openTable(db.ExcelDir, name)
		switch {
		case err == nil:
			db.tables[name] = t
			found++
		case errors.Is(err, os.ErrNotExist):
			db.Missing = append(db.Missing, name)
		default:
			return nil, err
		}
	}
	if found == 0 {
		return nil, fmt.Errorf("gamedata: no known table in %s: %w", db.ExcelDir, os.ErrNotExist)
	}
	db.Mod = findModInfo(p.Root, db.ExcelDir)

	db.loadItemTypes()
	db.loadItems()
	db.loadUniques()
	db.loadMonsters()
	db.loadSuperUniques()
	db.loadLevels()
	db.loadWarps()
	db.loadObjects()
	db.loadSkills()
	db.tables = nil // parsed rows are not kept
	return db, nil
}

func (db *DB) table(name string) *table { return db.tables[name] }

// Summary is the one-line startup report.
func (db *DB) Summary() string {
	s := fmt.Sprintf("items=%d monsters=%d levels=%d skills=%d mod=%s objects=%d strings=%d",
		len(db.Items), len(db.Monsters), len(db.Levels), len(db.Skills), db.Mod, len(db.Objects), db.Strings.Len())
	if len(db.Missing) > 0 {
		s += " missing=" + strings.Join(db.Missing, ",")
	}
	return s
}

func excelCandidates(root string) []string {
	if root == "" {
		return nil
	}
	return []string{filepath.Join(root, "excel"), filepath.Join(root, "data", "global", "excel"),
		filepath.Join(root, "global", "excel"), root}
}

func stringsCandidates(root, excel string) []string {
	var c []string
	if root != "" {
		c = append(c, filepath.Join(root, "strings"), filepath.Join(root, "data", "local", "lng", "strings"),
			filepath.Join(root, "local", "lng", "strings"))
	}
	// …/data/global/excel → …/data/local/lng/strings; …/excel → …/strings
	c = append(c, filepath.Join(excel, "..", "..", "local", "lng", "strings"), filepath.Join(excel, "..", "strings"))
	return c
}

func firstDirWith(dirs []string, anyOf ...string) string {
	for _, d := range dirs {
		for _, f := range anyOf {
			if _, err := findFile(d, f); err == nil {
				return d
			}
		}
	}
	return ""
}

// findModInfo reads the first modinfo*.json in the root, its parents (the
// D2RMM output sits two levels under mods/), or beside the excel tree.
func findModInfo(root, excel string) ModInfo {
	var dirs []string
	if root != "" {
		dirs = append(dirs, root, filepath.Dir(root))
	}
	dirs = append(dirs, filepath.Join(excel, ".."), filepath.Join(excel, "..", "..", ".."))
	for _, d := range dirs {
		ms, _ := filepath.Glob(filepath.Join(d, "modinfo*.json"))
		for _, m := range ms {
			b, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			var mi ModInfo
			if json.Unmarshal([]byte(strings.TrimPrefix(string(b), "\ufeff")), &mi) == nil && mi.Name != "" {
				return mi
			}
		}
	}
	return ModInfo{}
}

// ---- the process-wide instance (loaded once at startup by cmd/azbot) ----

var current atomic.Pointer[DB]

// Set installs db as the process-wide instance (nil clears it).
func Set(db *DB) { current.Store(db) }

// Get is the process-wide instance, nil when none was loaded — every consumer
// must keep working without it.
func Get() *DB { return current.Load() }
