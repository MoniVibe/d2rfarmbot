package gamedata

import (
	"fmt"
	"strings"
)

// Difficulty indexes the per-difficulty arrays.
type Difficulty int

const (
	Normal Difficulty = iota
	Nightmare
	Hell
)

// diffSuffix is the column suffix per difficulty ("Level", "Level(N)", "Level(H)").
var diffSuffix = [3]string{"", "(N)", "(H)"}

// Element indexes Monster.Res.
type Element int

const (
	Physical Element = iota
	Magic
	Fire
	Lightning
	Cold
	Poison
)

var elemCols = [6]string{"ResDm", "ResMa", "ResFi", "ResLi", "ResCo", "ResPo"}
var elemNames = [6]string{"physical", "magic", "fire", "lightning", "cold", "poison"}

func (e Element) String() string {
	if e >= 0 && int(e) < len(elemNames) {
		return elemNames[e]
	}
	return "?"
}

// MonSize is the monstats2 footprint and selection box.
type MonSize struct {
	SizeX, SizeY  int // footprint in subtiles
	Height        int
	OverlayHeight int
	PixHeight     int
	MeleeRng      int
	SpawnCol      int
	HitClass      int
	// htLeft/htTop/htWidth/htHeight: the pixel hit-test box relative to the
	// unit's feet, used when NoGfxHitTest (else the sprite's pixels select).
	HtLeft, HtTop, HtWidth, HtHeight int
	NoGfxHitTest                     bool
	Selectable                       bool // isSel
	NoSel                            bool
	Attackable                       bool // isAtt
	Small, Large, Critter            bool
}

// Monster is one monstats.txt row (+ its monstats2 row via MonStatsEx).
// ID is the monster class id memory reports (txtFileNo).
type Monster struct {
	ID         int
	Code       string // the Id column ("andariel")
	BaseID     string
	NameKey    string // NameStr
	Name       string
	MonType    string
	MonStatsEx string
	Enabled    bool

	Level [3]int // Level, Level(N), Level(H) — the normal-difficulty base level
	// Res is the resistance percent per difficulty and Element; ≥100 is immune.
	Res [3][6]int

	Boss, PrimeEvil, NPC, Killable, InTown, Interact bool
	Undead, Demon, Flying, IsMelee, NeverCount       bool

	Size MonSize
	// Skills: Skill1..Skill8 (the mod's own). Reviver: resurrects the dead or heals
	// the pack (Resurrect/Resurrect2, *Heal*) — Fight kills these first.
	Skills  []string
	Reviver bool
	HasSize bool // a monstats2 row was found
}

// Immune reports whether the monster is immune to e on difficulty d.
func (m *Monster) Immune(d Difficulty, e Element) bool {
	return m != nil && d >= 0 && d <= Hell && m.Res[d][e] >= 100
}

// Immunities lists the elements the monster is immune to on d.
func (m *Monster) Immunities(d Difficulty) []Element {
	var out []Element
	for e := Physical; e <= Poison; e++ {
		if m.Immune(d, e) {
			out = append(out, e)
		}
	}
	return out
}

// SuperUnique is one superuniques.txt row.
type SuperUnique struct {
	ID        int    // hcIdx (the id memory reports), else the counted row
	Key       string // Superunique column
	NameKey   string
	Name      string
	Class     string // monstats Id code of its base monster
	MonsterID int    // -1 when Class is unknown
}

func (db *DB) loadMonsters() {
	sizes := map[string]MonSize{}
	if t := db.table("monstats2.txt"); t != nil {
		t.each(func(_ int, r row) {
			id := r.str("Id")
			if id == "" {
				return
			}
			if _, dup := sizes[id]; dup {
				return
			}
			sizes[id] = MonSize{SizeX: r.int("SizeX"), SizeY: r.int("SizeY"), Height: r.int("Height"),
				OverlayHeight: r.int("OverlayHeight"), PixHeight: r.int("pixHeight"),
				MeleeRng: r.int("MeleeRng"), SpawnCol: r.int("spawnCol"), HitClass: r.int("HitClass"),
				HtLeft: r.int("htLeft"), HtTop: r.int("htTop"), HtWidth: r.int("htWidth"),
				HtHeight: r.int("htHeight"), NoGfxHitTest: r.bool("noGfxHitTest"),
				Selectable: r.bool("isSel"), NoSel: r.bool("noSel"), Attackable: r.bool("isAtt"),
				Small: r.bool("small"), Large: r.bool("large"), Critter: r.bool("critter")}
		})
	}
	t := db.table("monstats.txt")
	if t == nil {
		return
	}
	t.each(func(idx int, r row) {
		m := &Monster{ID: idx, Code: r.str("Id"), BaseID: r.str("BaseId"), NameKey: r.str("NameStr"),
			MonType: r.str("MonType"), MonStatsEx: r.str("MonStatsEx"), Enabled: r.bool("enabled"),
			Boss: r.bool("boss"), PrimeEvil: r.bool("primeevil"), NPC: r.bool("npc"),
			Killable: r.bool("killable"), InTown: r.bool("inTown"), Interact: r.bool("interact"),
			Undead: r.bool("lUndead") || r.bool("hUndead"), Demon: r.bool("demon"),
			Flying: r.bool("flying"), IsMelee: r.bool("isMelee"), NeverCount: r.bool("neverCount")}
		for k := 1; k <= 8; k++ {
			if sk := r.str(fmt.Sprintf("Skill%d", k)); sk != "" {
				m.Skills = append(m.Skills, sk)
				l := strings.ToLower(sk)
				if strings.HasPrefix(l, "resurrect") || strings.Contains(l, "heal") {
					m.Reviver = true // SkeletonRaise is a skeleton's own rising: not a reviver
				}
			}
		}
		m.Name = db.Strings.NameIn("monsters.json", m.NameKey, firstNonEmpty(m.NameKey, m.Code))
		for d := Normal; d <= Hell; d++ {
			m.Level[d] = r.int("Level" + diffSuffix[d])
			for e, col := range elemCols {
				m.Res[d][e] = r.int(col + diffSuffix[d])
			}
		}
		ex := m.MonStatsEx
		if ex == "" {
			ex = m.Code
		}
		if sz, ok := sizes[ex]; ok {
			m.Size, m.HasSize = sz, true
		}
		db.Monsters = append(db.Monsters, m)
		if _, dup := db.monByCode[m.Code]; !dup && m.Code != "" {
			db.monByCode[m.Code] = m
		}
	})
}

func (db *DB) loadSuperUniques() {
	t := db.table("superuniques.txt")
	if t == nil {
		return
	}
	t.each(func(idx int, r row) {
		su := &SuperUnique{ID: r.intOr("hcIdx", idx), Key: r.str("Superunique"), NameKey: r.str("Name"),
			Class: r.str("Class"), MonsterID: -1}
		su.Name = db.Strings.NameIn("monsters.json", su.NameKey, su.Key)
		if m := db.monByCode[su.Class]; m != nil {
			su.MonsterID = m.ID
		}
		db.SuperUniques = append(db.SuperUniques, su)
	})
}

// Monster is the row for a monster class id.
func (db *DB) Monster(id int) *Monster {
	if db == nil || id < 0 || id >= len(db.Monsters) {
		return nil
	}
	return db.Monsters[id]
}

// MonsterByCode is the row with that Id code ("andariel").
func (db *DB) MonsterByCode(code string) *Monster {
	if db == nil {
		return nil
	}
	return db.monByCode[code]
}

// SuperUnique is the superunique with that hcIdx.
func (db *DB) SuperUnique(id int) *SuperUnique {
	if db == nil {
		return nil
	}
	for _, su := range db.SuperUniques {
		if su.ID == id {
			return su
		}
	}
	return nil
}
