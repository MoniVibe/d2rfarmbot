package gear

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data"
)

// SuggestionKind is the family of one-step improvement.
type SuggestionKind string

const (
	SuggestEquip  SuggestionKind = "equip"
	SuggestSocket SuggestionKind = "socket"
	SuggestCube   SuggestionKind = "cube"
	// TODO(vendor): SuggestBuy — blocked on the bot being able to read vendor
	// inventories. When that lands, add a vendor pass in FeasibleUpgrades that
	// prices candidate bases against the gold argument (already plumbed).
)

// Suggestion is one feasible, human-followable improvement step.
type Suggestion struct {
	Kind SuggestionKind
	// What is a human-readable instruction, e.g.
	// "cube: Chipped Amethyst x3 -> Flawed Amethyst".
	What string
	// Gain is the estimated build-value delta of performing the step; the
	// slice returned by FeasibleUpgrades is sorted by Gain descending.
	Gain float64
	// Score is the absolute score of the resulting/acquired item.
	Score float64
	// Gamble marks steps with random outcomes (corruptions, rerolls). Their
	// Score/Gain only count the deterministic floor.
	Gamble  bool
	Reasons []string
}

// FeasibleUpgrades returns ranked one-step improvements achievable with what
// the character owns right now:
//
//   - EQUIP:  an inventory item outscores what is equipped in its slot.
//   - SOCKET: an owned gem into an owned item with an open socket, valued via
//     gems.txt effects for the correct context (weapon/helm/shield).
//   - CUBE:   a cubemain recipe whose inputs are all owned. Deterministic
//     outputs are scored exactly; random-affix outputs are flagged Gamble and
//     scored at base value.
//
// gold is currently unused (vendor suggestions are out of scope until vendor
// inventory is readable) but kept in the signature as the seam.
func (t *Tables) FeasibleUpgrades(equipped, inventory []data.Item, gold int, w BuildWeights) []Suggestion {
	_ = gold // TODO(vendor): price shopping list against gold.

	var out []Suggestion
	out = append(out, t.equipSuggestions(equipped, inventory, w)...)
	out = append(out, t.socketSuggestions(equipped, inventory, w)...)
	out = append(out, t.cubeSuggestions(inventory, w)...)

	sort.SliceStable(out, func(i, j int) bool { return out[i].Gain > out[j].Gain })
	return out
}

// ---------------------------------------------------------------------------
// EQUIP

func (t *Tables) equipSuggestions(equipped, inventory []data.Item, w BuildWeights) []Suggestion {
	// Current best score per body slot. Slots are itemtypes BodyLoc codes
	// normalized so that paired slots (rings) compete against the WEAKEST
	// occupant — replacing the worse ring is always the right move.
	slotWorst := map[string]struct {
		score float64
		name  string
		found bool
	}{}
	for _, eq := range equipped {
		slot := t.BodySlot(eq.Desc().Code)
		if slot == "" {
			continue
		}
		s, _ := t.ScoreItem(eq, w)
		cur, ok := slotWorst[slot]
		if !ok || s < cur.score {
			slotWorst[slot] = struct {
				score float64
				name  string
				found bool
			}{s, displayName(eq), true}
		}
	}

	var out []Suggestion
	for _, it := range inventory {
		slot := t.BodySlot(it.Desc().Code)
		if slot == "" {
			continue // not equippable (gems, potions, ...)
		}
		score, reasons := t.ScoreItem(it, w)
		cur := slotWorst[slot]
		gain := score - cur.score
		if gain <= 0 {
			continue
		}
		what := fmt.Sprintf("equip: %s into %s slot", displayName(it), slot)
		if cur.found {
			what = fmt.Sprintf("equip: %s over %s (%s slot)", displayName(it), cur.name, slot)
		}
		out = append(out, Suggestion{
			Kind: SuggestEquip, What: what,
			Gain: gain, Score: score,
			Gamble:  !it.Identified, // may be better OR worse once identified
			Reasons: reasons,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// SOCKET

func (t *Tables) socketSuggestions(equipped, inventory []data.Item, w BuildWeights) []Suggestion {
	var gems []data.Item
	for _, it := range inventory {
		if _, ok := t.Gems[it.Desc().Code]; ok {
			gems = append(gems, it)
		}
	}
	if len(gems) == 0 {
		return nil
	}

	targets := make([]data.Item, 0, len(equipped)+len(inventory))
	targets = append(targets, equipped...)
	targets = append(targets, inventory...)

	var out []Suggestion
	seen := map[string]bool{} // dedupe identical gem-code x target pairs
	for _, target := range targets {
		if t.openSockets(target) == 0 {
			continue
		}
		def, ok := t.Items[target.Desc().Code]
		if !ok {
			continue
		}
		for _, g := range gems {
			gem := t.Gems[g.Desc().Code]
			key := gem.Code + "->" + fmt.Sprint(target.UnitID)
			if seen[key] {
				continue
			}
			seen[key] = true

			// gems.txt effects are context-dependent; the base item's
			// gemapplytype says which block applies (0 weapon, 1 helm/armor,
			// 2 shield).
			var mods []GemMod
			switch def.GemApplyType {
			case 0:
				mods = gem.Weapon
			case 1:
				mods = gem.Helm
			case 2:
				mods = gem.Shield
			}
			gain, reasons := scoreGemMods(mods, w)
			if gain <= 0 {
				continue // gem adds nothing this build values in this context
			}
			out = append(out, Suggestion{
				Kind: SuggestSocket,
				What: fmt.Sprintf("socket: %s into %s (%s)", gem.Name, displayName(target), strings.Join(reasons, ", ")),
				Gain: gain, Score: gain,
				Reasons: reasons,
			})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// CUBE

// cubeSuggestions finds recipes whose inputs can all be satisfied by DISTINCT
// owned inventory items (equipped gear is never sacrificed to the cube).
//
// Matching is greedy over recipe inputs in order; specs with qty=N consume N
// distinct matching items (the bot reader models stackables as separate item
// entries). Greedy can in theory miss an assignment a full bipartite matching
// would find (specs overlapping in what they accept), but cube recipes list
// most-specific inputs first, and a false negative only hides a suggestion —
// it never fabricates one.
func (t *Tables) cubeSuggestions(inventory []data.Item, w BuildWeights) []Suggestion {
	var out []Suggestion
	for i := range t.Recipes {
		rec := &t.Recipes[i]
		consumed, ok := t.satisfy(rec, inventory)
		if !ok {
			continue
		}

		gamble := !rec.Deterministic()
		score, reasons := t.scoreRecipeOutput(rec, consumed, w)

		// Gain = value produced minus value destroyed (the consumed items).
		spent := 0.0
		var names []string
		for _, it := range consumed {
			s, _ := t.ScoreItem(it, w)
			spent += s
			names = append(names, displayName(it))
		}

		outName := rec.outputName(t, consumed)
		what := fmt.Sprintf("cube: %s -> %s", strings.Join(names, " + "), outName)
		if gamble {
			what += " (gamble: random outcome)"
			reasons = append(reasons, "random-affix output — scored at base value only")
		}
		out = append(out, Suggestion{
			Kind: SuggestCube, What: what,
			Gain: score - spent, Score: score,
			Gamble: gamble, Reasons: reasons,
		})
	}
	return out
}

// satisfy tries to reserve distinct inventory items for every input spec.
func (t *Tables) satisfy(rec *Recipe, inventory []data.Item) ([]data.Item, bool) {
	used := make([]bool, len(inventory))
	var consumed []data.Item
	for _, spec := range rec.Inputs {
		needed := spec.Quantity
		for i, it := range inventory {
			if needed == 0 {
				break
			}
			if used[i] || !t.Matches(spec, it) {
				continue
			}
			used[i] = true
			consumed = append(consumed, it)
			needed--
		}
		if needed > 0 {
			return nil, false
		}
	}
	return consumed, true
}

// scoreRecipeOutput values the recipe result conservatively.
func (t *Tables) scoreRecipeOutput(rec *Recipe, consumed []data.Item, w BuildWeights) (float64, []string) {
	switch rec.Output.Kind {
	case OutputCode:
		def := t.Items[rec.Output.Code] // validated at parse time
		score, reasons := t.scoreItemDefBase(def, w)
		// Deterministic granted mods add exact value; random ones add none.
		for _, m := range rec.Mods {
			if m.Min == m.Max && (m.Chance == 0 || m.Chance == 100) {
				v, rs := scoreGemMods([]GemMod{{Code: m.Code, Param: m.Param, Min: m.Min, Max: m.Max}}, w)
				score += v
				reasons = append(reasons, rs...)
			}
		}
		return score, reasons
	case OutputUseItem, OutputUseType:
		// Result is (a transform of) input 1: floor its value at the input's
		// base, since affixes get rerolled/added unpredictably.
		if len(consumed) > 0 {
			def, ok := t.Items[consumed[0].Desc().Code]
			if ok {
				return t.scoreItemDefBase(def, w)
			}
		}
		return 0, nil
	}
	return 0, nil
}

// scoreItemDefBase values a table item definition with no rolled affixes —
// what an unidentified/base instance of it would be worth.
func (t *Tables) scoreItemDefBase(def *ItemDef, w BuildWeights) (float64, []string) {
	acc := &scoreAccum{}
	if def.MaxDef > 0 {
		mid := (def.MinDef + def.MaxDef) / 2
		acc.add(float64(mid)*w.Defense, "~%d base defense", mid)
	}
	if def.GemSockets > 0 {
		acc.add(float64(def.GemSockets), "up to %d sockets", def.GemSockets)
	}
	return acc.total, acc.reasons
}

// outputName renders what the recipe produces for the What string.
func (r *Recipe) outputName(t *Tables, consumed []data.Item) string {
	switch r.Output.Kind {
	case OutputCode:
		name := r.Output.Code
		if def, ok := t.Items[r.Output.Code]; ok && def.Name != "" {
			name = def.Name
		}
		if r.Output.Quantity > 1 {
			name = fmt.Sprintf("%s x%d", name, r.Output.Quantity)
		}
		return name
	case OutputUseItem:
		if len(consumed) > 0 {
			return displayName(consumed[0]) + " (modified)"
		}
		return "input item (modified)"
	case OutputUseType:
		if len(consumed) > 0 {
			return "new " + consumed[0].Desc().Name
		}
		return "new item of input's type"
	}
	return r.Output.Raw
}

// displayName prefers the identified name, falling back to base names.
func displayName(it data.Item) string {
	if it.IdentifiedName != "" {
		return it.IdentifiedName
	}
	if it.Desc().Name != "" {
		return it.Desc().Name
	}
	return string(it.Name)
}
