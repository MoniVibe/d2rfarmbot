package inventory

// ---------------------------------------------------------------- durability
//
// OWNER (2026-09-24): "inventory should know that half its items are broken. add
// a durability awareness thing so it can tell, or let it repair obsessively". The
// equipped gear's durability is read from memory; the policy repairs at EVERY
// town visit where anything is worn below TownRepairPct (repairs are cheap at
// these levels), and in the field a broken piece (0 durability: the game stops
// counting its stats) or one near breaking sends him home.

// Gear is one equipped piece's durability.
type Gear struct {
	Slot   string
	ID     int
	Dur    int
	MaxDur int // 0 = no durability (jewelry, indestructible)
}

// Pct is the piece's durability percent (100 when it has none to lose).
func (g Gear) Pct() int {
	if g.MaxDur <= 0 {
		return 100
	}
	return g.Dur * 100 / g.MaxDur
}

// Broken: at 0 the game treats the item as broken (its bonuses stop counting).
func (g Gear) Broken() bool { return g.MaxDur > 0 && g.Dur <= 0 }

const (
	// TownRepairPct: in town, anything below this is repaired — "obsessively".
	TownRepairPct = 90
	// FieldRepairPct: in the field, a piece this low (or broken) is a trip home.
	FieldRepairPct = 15
)

// Wear is the gear's durability summary.
type Wear struct {
	Worst  Gear
	Broken []Gear
	Low    []Gear // below TownRepairPct
}

// Assess summarizes a set of equipped pieces.
func Assess(gear []Gear) Wear {
	w := Wear{Worst: Gear{Dur: 1, MaxDur: 1}}
	for _, g := range gear {
		if g.MaxDur <= 0 {
			continue
		}
		if g.Pct() < w.Worst.Pct() {
			w.Worst = g
		}
		if g.Broken() {
			w.Broken = append(w.Broken, g)
		}
		if g.Pct() < TownRepairPct {
			w.Low = append(w.Low, g)
		}
	}
	return w
}

// RepairInTown: any piece below TownRepairPct.
func (w Wear) RepairInTown() bool { return len(w.Low) > 0 }

// GoHome: a piece is broken or nearly so — the field trip ends.
func (w Wear) GoHome() bool { return len(w.Broken) > 0 || w.Worst.Pct() < FieldRepairPct }
