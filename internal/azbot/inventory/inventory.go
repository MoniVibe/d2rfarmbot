// Package inventory is the bot's memory-driven inventory model and planner.
//
// OWNER (2026-09-24): "the bot should know how to manage inventory properly ...
// do it memory driven rather than scripted". The model is read from game memory
// every time (bag grid, belt slots, cursor); item identity and size come from the
// MOD's own tables (loot.Classify undoes the +15 misc shift), never d2go's vanilla
// descriptions. The planner compares the model with the policy and returns the
// next moves; the executor (package activity) performs one gesture at a time and
// proves it by the next memory read.
package inventory

import (
	"strings"

	"github.com/hectorgimenez/koolo/internal/azbot/loot"
)

// Potion is a bottle's family, from its mod item code.
type Potion uint8

const (
	PotNone Potion = iota
	PotHP
	PotMP
	PotRV
)

func (p Potion) String() string { return [...]string{"-", "hp", "mp", "rv"}[p] }

// PotionOf reads the family from the mod row's code: hp1..hp5, mp1..mp5, rvs/rvl.
func PotionOf(id int) Potion {
	code := loot.Classify(id).Code
	switch {
	case strings.HasPrefix(code, "hp"):
		return PotHP
	case strings.HasPrefix(code, "mp"):
		return PotMP
	case code == "rvs" || code == "rvl":
		return PotRV
	}
	return PotNone
}

// Item is one bag item as memory reports it.
type Item struct {
	Unit       uint32
	ID         int // mod row
	GX, GY     int
	Quality    int
	Identified bool
}

// BeltSlot is one belt cell: memory flattens the belt, index = row*4 + column.
type BeltSlot struct {
	Unit  uint32
	ID    int
	Index int
}

// Model is one memory reading of the carried inventory.
type Model struct {
	Bag      []Item
	Belt     []BeltSlot
	BeltRows int // 1..4 (the belt item's size)
	Cursor   bool
}

// Column c's potions, bottom (row 0) first. Belt columns stack from the bottom.
func (m *Model) column(c int) []BeltSlot {
	var out []BeltSlot
	for r := 0; r < m.BeltRows; r++ {
		for _, s := range m.Belt {
			if s.Index == r*4+c {
				out = append(out, s)
			}
		}
	}
	return out
}

// ColumnKind is the family a column holds (the first bottle's), PotNone when empty.
func (m *Model) ColumnKind(c int) Potion {
	col := m.column(c)
	if len(col) == 0 {
		return PotNone
	}
	return PotionOf(col[0].ID)
}

// ColumnCount is how many bottles column c holds.
func (m *Model) ColumnCount(c int) int { return len(m.column(c)) }
