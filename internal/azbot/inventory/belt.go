package inventory

// ---------------------------------------------------------------- the belt plan
//
// OWNER (2026-09-24): "could it be more dynamic and use whatever it has, but
// reserve 2 for hp". The bot reads what the belt and bag hold; at least MinHPCols
// columns carry healing, the rest carry whatever it has. The game's own rule for
// a Shift+clicked bag potion is used as-is: it lands in the leftmost column of its
// family with room, else in the leftmost EMPTY column. So the plan steers only by
// which bottle it sends and by emptying a column (evict) when healing needs it.

// MinHPCols: columns reserved for healing.
const MinHPCols = 2

// MoveKind is one belt gesture.
type MoveKind uint8

const (
	// MoveFill: Shift+click the bag potion; the game places it (see above).
	MoveFill MoveKind = iota + 1
	// MoveEvict: lift the bottom bottle of a column (a click on its belt slot)
	// and park it in the bag — emptying a column so healing can take it.
	MoveEvict
)

// Move is one step the executor performs; the next memory read proves it.
type Move struct {
	Kind   MoveKind
	Unit   uint32 // the bag potion (Fill) or the belt bottle (Evict)
	GX, GY int    // bag cell (Fill)
	Column int    // belt column (Evict)
	Why    string
}

// landing is the column the game would put a potion of family p in, or -1.
func (m *Model) landing(p Potion) int {
	for c := 0; c < 4; c++ {
		if m.ColumnKind(c) == p && m.ColumnCount(c) < m.BeltRows {
			return c
		}
	}
	for c := 0; c < 4; c++ {
		if m.ColumnCount(c) == 0 {
			return c
		}
	}
	return -1
}

// hpColumns counts the columns holding healing.
func (m *Model) hpColumns() int {
	n := 0
	for c := 0; c < 4; c++ {
		if m.ColumnKind(c) == PotHP {
			n++
		}
	}
	return n
}

// BeltPlan returns the next belt move, or ok=false when the belt is as good as
// the bag allows. One move at a time: memory re-reads between moves.
func (m *Model) BeltPlan() (Move, bool) {
	if m.BeltRows <= 0 || m.Cursor {
		return Move{}, false
	}
	var hp, other []Item
	for _, it := range m.Bag {
		switch PotionOf(it.ID) {
		case PotHP:
			hp = append(hp, it)
		case PotMP, PotRV:
			other = append(other, it)
		}
	}
	emptyCols := 0
	for c := 0; c < 4; c++ {
		if m.ColumnCount(c) == 0 {
			emptyCols++
		}
	}
	// 1. Healing first: fill an HP column with room, or claim an empty column.
	if len(hp) > 0 {
		if c := m.landing(PotHP); c >= 0 {
			it := hp[0]
			return Move{Kind: MoveFill, Unit: it.Unit, GX: it.GX, GY: it.GY, Why: "healing to the belt"}, true
		}
		// 2. No room for healing and fewer than MinHPCols: empty the lightest
		//    non-healing column (its bottles go to the bag).
		if m.hpColumns() < MinHPCols {
			best, bestN := -1, 1<<30
			for c := 0; c < 4; c++ {
				if k := m.ColumnKind(c); k != PotHP && k != PotNone && m.ColumnCount(c) < bestN {
					best, bestN = c, m.ColumnCount(c)
				}
			}
			if best >= 0 {
				bottom := m.column(best)[0]
				return Move{Kind: MoveEvict, Unit: bottom.Unit, Column: best, Why: "a column for healing (fewer than 2 hold hp)"}, true
			}
		}
	}
	// 3. Others fill what is left — but never an empty column that healing still
	//    needs: keep enough empty columns to reach MinHPCols.
	needHP := MinHPCols - m.hpColumns()
	if needHP < 0 {
		needHP = 0
	}
	for _, it := range other {
		p := PotionOf(it.ID)
		c := m.landing(p)
		if c < 0 {
			continue
		}
		if m.ColumnCount(c) == 0 && emptyCols <= needHP {
			continue // that empty column is reserved for healing
		}
		return Move{Kind: MoveFill, Unit: it.Unit, GX: it.GX, GY: it.GY, Why: p.String() + " to the belt"}, true
	}
	return Move{}, false
}
