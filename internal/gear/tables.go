// Package gear implements the knowledge + valuation layers of a "gear oracle"
// for a modded Diablo II Resurrected bot.
//
// It parses the mod's tab-separated excel tables (cubemain.txt, gems.txt,
// weapons.txt, armor.txt, misc.txt, itemtypes.txt), indexes them, and answers
// two questions:
//
//	(a) how good is a given d2go item for a given build (ScoreItem), and
//	(b) which one-step improvements are feasible right now
//	    (FeasibleUpgrades: equip-from-inventory, socket-a-gem, cube-recipe).
//
// NOTE ON THE MODDED TABLES: these are NOT classic-D2 tables. Observations
// from the shipped data (documented here so nobody "fixes" the parser back to
// classic assumptions):
//
//   - cubemain.txt has 106 columns (classic D2R has ~80). The extra columns are
//     "output b"/"output c" blocks with their own mod sets. ~15.8k rows, the
//     vast majority being corruption-orb recipes ("<item> & Orb of Corruption")
//     whose output is "usetype" (same base, new mods) or "useitem".
//   - cubemain rows use `op`/`param`/`value` gating (e.g. op=18 param=361:
//     "input must not already carry stat 361", the mod's corruption marker).
//     We store these raw but do not evaluate them; see Recipe.Op.
//   - A handful of inputs are full item NAMES ("Hellfire Torch", "t4 Splash
//     Charm") rather than codes. Those rows are skipped and counted in
//     ParseStats (about 45 of ~15.8k).
//   - gems.txt contains ~76 gem rows including modded gem grades (gm* codes);
//     runes are NOT in gems.txt in this mod.
package gear

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultExcelDir is where the mod ships its data tables. LoadTables takes the
// directory explicitly so tests can point at fixtures.
const DefaultExcelDir = `C:\Program Files (x86)\Diablo II Resurrected\mods\D2RMM\D2RMM.mpq\data\global\excel`

// ItemDef is one row of weapons.txt / armor.txt / misc.txt, keyed by code.
type ItemDef struct {
	Code     string
	Name     string
	Type     string // primary type code (itemtypes.txt Code)
	Type2    string // secondary type code, may be empty
	ReqStr   int
	ReqDex   int
	LevelReq int
	MinDef   int // armor only (minac); 0 otherwise
	MaxDef   int // armor only (maxac); 0 otherwise
	// GemSockets is the base-item socket cap (before itemtypes / ilvl caps).
	GemSockets int
	// GemApplyType selects which gems.txt effect column applies when a gem is
	// socketed into this base: 0=weapon mods, 1=helm/armor mods, 2=shield mods.
	GemApplyType int
	Source       string // "weapons", "armor" or "misc"
}

// TypeDef is one row of itemtypes.txt. Equiv1/Equiv2 form the type DAG used by
// recipe input matchers ("swor" must match every sword code).
type TypeDef struct {
	Name       string
	Code       string
	Equiv1     string
	Equiv2     string
	Body       bool   // true if items of this type occupy a body slot
	BodyLoc1   string // e.g. "tors", "head", "rarm"
	BodyLoc2   string
	MaxSockets int // highest MaxSockets band (MaxSockets3)
}

// GemMod is one effect line of a gems.txt context block.
type GemMod struct {
	Code  string // property code, e.g. "str", "ac", "res-all"
	Param string
	Min   int
	Max   int
}

// GemDef is one row of gems.txt: the per-context effects of socketing it.
type GemDef struct {
	Name   string
	Code   string
	Weapon []GemMod // applied when socketed into GemApplyType 0
	Helm   []GemMod // applied when socketed into GemApplyType 1
	Shield []GemMod // applied when socketed into GemApplyType 2
}

// ParseStats reports how tolerant parsing went. The mod tables contain rows we
// deliberately cannot handle (name-based inputs, disabled rows); we count
// instead of failing.
type ParseStats struct {
	RecipesParsed  int
	RecipesSkipped int
	SkipReasons    map[string]int
	ItemsLoaded    int
	TypesLoaded    int
	GemsLoaded     int
}

// Tables is the fully parsed + indexed knowledge base.
type Tables struct {
	Items   map[string]*ItemDef // by item code
	Types   map[string]*TypeDef // by type code
	Gems    map[string]*GemDef  // by item code (only true socketables)
	Recipes []Recipe
	Stats   ParseStats
}

// LoadTables parses every table under excelDir. It is tolerant of unknown or
// malformed rows (counted in Stats) but returns an error if a whole file is
// missing or has no usable header.
func LoadTables(excelDir string) (*Tables, error) {
	t := &Tables{
		Items: map[string]*ItemDef{},
		Types: map[string]*TypeDef{},
		Gems:  map[string]*GemDef{},
		Stats: ParseStats{SkipReasons: map[string]int{}},
	}

	if err := t.loadItemTypes(filepath.Join(excelDir, "itemtypes.txt")); err != nil {
		return nil, err
	}
	for _, f := range []string{"weapons.txt", "armor.txt", "misc.txt"} {
		if err := t.loadItems(filepath.Join(excelDir, f)); err != nil {
			return nil, err
		}
	}
	if err := t.loadGems(filepath.Join(excelDir, "gems.txt")); err != nil {
		return nil, err
	}
	// cubemain last: input specifiers are validated against Items/Types.
	if err := t.loadRecipes(filepath.Join(excelDir, "cubemain.txt")); err != nil {
		return nil, err
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// generic TSV machinery

// tsvTable gives header-name access to tab-separated rows. Column layouts in
// modded tables shift, so we never index columns positionally.
type tsvTable struct {
	cols map[string]int
	rows [][]string
}

type tsvRow struct {
	t      *tsvTable
	fields []string
}

func readTSV(path string) (*tsvTable, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("gear: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	t := &tsvTable{cols: map[string]int{}}
	first := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if first {
			for i, h := range fields {
				h = strings.ToLower(strings.TrimSpace(h))
				if _, dup := t.cols[h]; !dup { // first occurrence wins
					t.cols[h] = i
				}
			}
			first = false
			continue
		}
		t.rows = append(t.rows, fields)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("gear: reading %s: %w", path, err)
	}
	if len(t.cols) == 0 {
		return nil, fmt.Errorf("gear: %s: empty or missing header", path)
	}
	return t, nil
}

func (t *tsvTable) each(fn func(r tsvRow)) {
	for _, fields := range t.rows {
		fn(tsvRow{t: t, fields: fields})
	}
}

// get returns the trimmed cell under a header name, or "" when the column does
// not exist or the row is short (both common in these tables).
func (r tsvRow) get(col string) string {
	i, ok := r.t.cols[strings.ToLower(col)]
	if !ok || i >= len(r.fields) {
		return ""
	}
	return strings.TrimSpace(r.fields[i])
}

func (r tsvRow) getInt(col string) int {
	n, _ := strconv.Atoi(r.get(col))
	return n
}

// isCommentRow: rows whose description/name starts with a comment marker are
// documentation lines inside the data files, not data.
func isCommentRow(firstCell string) bool {
	return strings.HasPrefix(firstCell, "*") ||
		strings.HasPrefix(firstCell, "//") ||
		strings.HasPrefix(firstCell, "#")
}

// ---------------------------------------------------------------------------
// itemtypes.txt

func (t *Tables) loadItemTypes(path string) error {
	tab, err := readTSV(path)
	if err != nil {
		return err
	}
	tab.each(func(r tsvRow) {
		code := r.get("Code")
		if code == "" || isCommentRow(r.get("ItemType")) {
			return // "Any" root row and comments have no code
		}
		t.Types[code] = &TypeDef{
			Name:       r.get("ItemType"),
			Code:       code,
			Equiv1:     r.get("Equiv1"),
			Equiv2:     r.get("Equiv2"),
			Body:       r.getInt("Body") == 1,
			BodyLoc1:   r.get("BodyLoc1"),
			BodyLoc2:   r.get("BodyLoc2"),
			MaxSockets: r.getInt("MaxSockets3"),
		}
		t.Stats.TypesLoaded++
	})
	return nil
}

// ---------------------------------------------------------------------------
// weapons.txt / armor.txt / misc.txt

func (t *Tables) loadItems(path string) error {
	tab, err := readTSV(path)
	if err != nil {
		return err
	}
	source := strings.TrimSuffix(filepath.Base(path), ".txt")
	tab.each(func(r tsvRow) {
		code := r.get("code")
		name := r.get("name")
		if code == "" || isCommentRow(name) {
			return
		}
		t.Items[code] = &ItemDef{
			Code:         code,
			Name:         name,
			Type:         r.get("type"),
			Type2:        r.get("type2"),
			ReqStr:       r.getInt("reqstr"),
			ReqDex:       r.getInt("reqdex"),
			LevelReq:     r.getInt("levelreq"),
			MinDef:       r.getInt("minac"),
			MaxDef:       r.getInt("maxac"),
			GemSockets:   r.getInt("gemsockets"),
			GemApplyType: r.getInt("gemapplytype"),
			Source:       source,
		}
		t.Stats.ItemsLoaded++
	})
	return nil
}

// ---------------------------------------------------------------------------
// gems.txt

func (t *Tables) loadGems(path string) error {
	tab, err := readTSV(path)
	if err != nil {
		return err
	}
	readMods := func(r tsvRow, prefix string) []GemMod {
		var mods []GemMod
		for i := 1; i <= 3; i++ {
			code := r.get(fmt.Sprintf("%sMod%dCode", prefix, i))
			if code == "" {
				continue
			}
			mods = append(mods, GemMod{
				Code:  code,
				Param: r.get(fmt.Sprintf("%sMod%dParam", prefix, i)),
				Min:   r.getInt(fmt.Sprintf("%sMod%dMin", prefix, i)),
				Max:   r.getInt(fmt.Sprintf("%sMod%dMax", prefix, i)),
			})
		}
		return mods
	}
	tab.each(func(r tsvRow) {
		code := r.get("code")
		if code == "" || isCommentRow(r.get("name")) {
			return
		}
		t.Gems[code] = &GemDef{
			Name:   r.get("name"),
			Code:   code,
			Weapon: readMods(r, "weapon"),
			Helm:   readMods(r, "helm"),
			Shield: readMods(r, "shield"),
		}
		t.Stats.GemsLoaded++
	})
	return nil
}

// ---------------------------------------------------------------------------
// type-tree queries

// IsA reports whether an item code belongs to typeCode, walking the item's
// Type/Type2 up through the itemtypes Equiv1/Equiv2 DAG. It also accepts
// typeCode being the item code itself (cubemain inputs mix both freely).
func (t *Tables) IsA(itemCode, typeCode string) bool {
	if itemCode == typeCode {
		return true
	}
	def, ok := t.Items[itemCode]
	if !ok {
		return false
	}
	seen := map[string]bool{}
	var walk func(tc string) bool
	walk = func(tc string) bool {
		if tc == "" || seen[tc] {
			return false
		}
		seen[tc] = true
		if tc == typeCode {
			return true
		}
		td, ok := t.Types[tc]
		if !ok {
			return false
		}
		return walk(td.Equiv1) || walk(td.Equiv2)
	}
	return walk(def.Type) || walk(def.Type2)
}

// BodySlot returns the primary body location ("tors", "head", "rarm", ...) an
// item code equips into, or "" if it is not equippable. It walks up the type
// tree until it finds a type flagged Body.
func (t *Tables) BodySlot(itemCode string) string {
	def, ok := t.Items[itemCode]
	if !ok {
		return ""
	}
	seen := map[string]bool{}
	var walk func(tc string) string
	walk = func(tc string) string {
		if tc == "" || seen[tc] {
			return ""
		}
		seen[tc] = true
		td, ok := t.Types[tc]
		if !ok {
			return ""
		}
		if td.Body && td.BodyLoc1 != "" {
			return td.BodyLoc1
		}
		if s := walk(td.Equiv1); s != "" {
			return s
		}
		return walk(td.Equiv2)
	}
	if s := walk(def.Type); s != "" {
		return s
	}
	return walk(def.Type2)
}

// MaxSocketsFor returns the socket capacity of a base item code: the base
// item's gemsockets capped by its type's MaxSockets band. (We ignore the
// ilvl-based lower bands; the cap is what matters for "can I socket more".)
func (t *Tables) MaxSocketsFor(itemCode string) int {
	def, ok := t.Items[itemCode]
	if !ok {
		return 0
	}
	max := def.GemSockets
	if td, ok := t.Types[def.Type]; ok && td.MaxSockets < max {
		max = td.MaxSockets
	}
	return max
}
