package gamedata

import "strconv"

// Skill is one skills.txt row joined to its skilldesc row. ID is the skill id
// memory reports.
type Skill struct {
	ID      int
	Key     string // the skill column ("Concentrate") — the table's internal name
	DescKey string // skilldesc row key
	NameKey string // skilldesc "str name" string key
	Name    string // the in-game name (the mod renames: 144 Concentrate → "Carnage")
	Class   string // charclass: ama sor nec pal bar dru ass (+ the table's newer classes); "" = monster/item

	ReqLevel  int
	MaxLevel  int
	ReqSkills []string // reqskill1-3 (skill keys)
	ReqIDs    []int    // the same, resolved to ids (-1 unresolved)

	// Tree position (skilldesc): Page 1-3 (0 = not on a tree), Row 1-6, Column 1-3.
	Page, Row, Column int

	Passive, Aura    bool
	LeftSkill, Right bool
	InTown           bool
	Range            string // none / h2h / rng / both / loc
	Mana             int    // base mana cost (mana, before manashift)
	MinMana          int
	LvlMana          int // extra mana per level
	ManaShift        int
}

// ManaCost is the in-game mana cost at skill level lvl (≥1): the table stores
// mana and lvlmana in 1/256ths scaled by 2^manashift.
func (s *Skill) ManaCost(lvl int) float64 {
	if s == nil || lvl < 1 {
		return 0
	}
	c := float64((s.Mana+s.LvlMana*(lvl-1))<<s.ManaShift) / 256
	if m := float64(s.MinMana); c < m {
		c = m
	}
	return c
}

type skillDesc struct {
	page, row, col int
	nameKey        string
}

func (db *DB) loadSkills() {
	descs := map[string]skillDesc{}
	if t := db.table("skilldesc.txt"); t != nil {
		t.each(func(_ int, r row) {
			k := r.str("skilldesc")
			if _, dup := descs[k]; dup || k == "" {
				return
			}
			descs[k] = skillDesc{page: r.int("SkillPage"), row: r.int("SkillRow"),
				col: r.int("SkillColumn"), nameKey: r.str("str name")}
		})
	}
	t := db.table("skills.txt")
	if t == nil {
		return
	}
	t.each(func(idx int, r row) {
		s := &Skill{ID: idx, Key: r.str("skill"), DescKey: r.str("skilldesc"), Class: r.str("charclass"),
			ReqLevel: r.int("reqlevel"), MaxLevel: r.int("maxlvl"), Passive: r.bool("passive"),
			Aura: r.bool("aura"), LeftSkill: r.bool("leftskill"), Right: r.bool("rightskill"),
			InTown: r.bool("InTown"), Range: r.str("range"), Mana: r.int("mana"),
			MinMana: r.int("minmana"), LvlMana: r.int("lvlmana"), ManaShift: r.int("manashift")}
		for i := 1; i <= 3; i++ {
			if q := r.str("reqskill" + strconv.Itoa(i)); q != "" {
				s.ReqSkills = append(s.ReqSkills, q)
			}
		}
		if d, ok := descs[s.DescKey]; ok {
			s.Page, s.Row, s.Column, s.NameKey = d.page, d.row, d.col, d.nameKey
		}
		s.Name = db.Strings.NameIn("skills.json", s.NameKey, s.Key)
		db.Skills = append(db.Skills, s)
		if _, dup := db.skillByKey[s.Key]; !dup && s.Key != "" {
			db.skillByKey[s.Key] = s
		}
	})
	for _, s := range db.Skills {
		for _, q := range s.ReqSkills {
			id := -1
			if p := db.skillByKey[q]; p != nil {
				id = p.ID
			}
			s.ReqIDs = append(s.ReqIDs, id)
		}
	}
}

// Skill is the row for a skill id.
func (db *DB) Skill(id int) *Skill {
	if db == nil || id < 0 || id >= len(db.Skills) {
		return nil
	}
	return db.Skills[id]
}

// SkillByKey is the row with that skills.txt key ("Leap Attack").
func (db *DB) SkillByKey(key string) *Skill {
	if db == nil {
		return nil
	}
	return db.skillByKey[key]
}

// ClassSkills are a class's tree skills (Page > 0), in id order.
func (db *DB) ClassSkills(class string) []*Skill {
	var out []*Skill
	if db == nil {
		return out
	}
	for _, s := range db.Skills {
		if s.Class == class && s.Page > 0 {
			out = append(out, s)
		}
	}
	return out
}
