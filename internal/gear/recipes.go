package gear

import (
	"strconv"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
)

// InputSpec is a structured matcher for one cubemain input cell, e.g.
// `"amu,mag"` or `"armo,sock=3,noe"`. The first comma token is always an item
// code OR a type code; every later token is a qualifier. Qualifiers seen in
// this mod's table (exhaustive per a scan of all 15.8k rows):
//
//	low nor hiq mag rar set uni crf  - quality
//	bas exc eli                      - tier (normal/exceptional/elite base)
//	eth noe                          - ethereal / not ethereal
//	nos                              - zero sockets
//	sock / sock=N                    - socketed / exactly N sockets
//	nru                              - not a runeword
//	qty=N                            - N copies required (stackables)
type InputSpec struct {
	Raw      string
	Code     string // item code or itemtypes code
	Quantity int    // >= 1

	Quality  item.Quality // 0 = any quality
	Tier     string       // "bas", "exc", "eli" or ""
	Ethereal *bool        // nil = don't care

	NoSockets       bool // nos
	RequireSocketed bool // bare "sock"
	Sockets         int  // sock=N; -1 when unspecified

	NotRuneword bool // nru

	// Unknown holds qualifiers we did not recognize. Matches() fails closed on
	// them so we never claim feasibility on semantics we don't understand.
	Unknown []string
}

// OutputKind classifies a cubemain output cell.
type OutputKind int

const (
	OutputCode    OutputKind = iota // a concrete item code
	OutputUseItem                   // "useitem": returns input item 1, modified
	OutputUseType                   // "usetype": new item of input 1's type
)

// OutputSpec is the parsed primary output ("output" column; the modded
// "output b"/"output c" alternates are intentionally ignored — they fire
// randomly, so they only ever downgrade a recipe to "gamble", and the mod uses
// them rarely).
type OutputSpec struct {
	Raw        string
	Kind       OutputKind
	Code       string // item code when Kind == OutputCode
	Quantity   int
	Quality    item.Quality // requested output quality (mag/rar/uni/...), 0 = base
	Regenerate bool         // "usetype,mod": rerolls/regenerates mods -> random
}

// RecipeMod is one granted affix column set ("mod 1".."mod 5").
type RecipeMod struct {
	Code   string // property code, e.g. "res-all", "sock", "hp"
	Chance int    // 0 or 100 = always; anything else = roll
	Param  string
	Min    int
	Max    int
}

// Recipe is one enabled, parseable cubemain row.
type Recipe struct {
	Description string
	NumInputs   int
	Inputs      []InputSpec
	Output      OutputSpec
	Mods        []RecipeMod

	// Op/OpParam/OpValue gate the recipe on an input stat (e.g. op=18
	// param=361: input must not already carry the corruption marker stat).
	// We store but do NOT evaluate ops: the bot can attempt the transmute and
	// nothing happens if the gate fails, so treating gated recipes as feasible
	// is safe, just occasionally optimistic.
	Op, OpParam, OpValue int

	// Lvl/Plvl/Ilvl are affix-level controls; any of them nonzero means output
	// affixes are level-dependent (=> not deterministic).
	Lvl, Plvl, Ilvl int
}

// Deterministic reports whether cubing this recipe yields a fully predictable
// item: a concrete output code, no quality reroll, no chance-based or ranged
// mods, no affix-level randomness. Non-deterministic recipes are still
// suggested but flagged "gamble" and scored at base value.
func (r Recipe) Deterministic() bool {
	if r.Output.Kind != OutputCode {
		return false
	}
	if r.Output.Quality == item.QualityMagic || r.Output.Quality == item.QualityRare ||
		r.Output.Quality == item.QualityUnique || r.Output.Quality == item.QualityCrafted {
		return false // random affix pool
	}
	if r.Output.Regenerate || r.Lvl != 0 || r.Plvl != 0 || r.Ilvl != 0 {
		return false
	}
	for _, m := range r.Mods {
		if m.Chance != 0 && m.Chance != 100 {
			return false
		}
		if m.Min != m.Max {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// parsing

var qualityTokens = map[string]item.Quality{
	"low": item.QualityLowQuality,
	"nor": item.QualityNormal,
	"hiq": item.QualitySuperior,
	"mag": item.QualityMagic,
	"rar": item.QualityRare,
	"set": item.QualitySet,
	"uni": item.QualityUnique,
	"crf": item.QualityCrafted,
}

// parseSpecTokens parses the comma-separated cell (quotes already stripped).
func parseInputSpec(raw string) InputSpec {
	spec := InputSpec{Raw: raw, Quantity: 1, Sockets: -1}
	tokens := strings.Split(raw, ",")
	spec.Code = strings.TrimSpace(tokens[0])
	tru, fls := true, false
	for _, tok := range tokens[1:] {
		tok = strings.TrimSpace(tok)
		switch {
		case tok == "":
		case qualityTokens[tok] != 0:
			spec.Quality = qualityTokens[tok]
		case tok == "bas" || tok == "exc" || tok == "eli":
			spec.Tier = tok
		case tok == "eth":
			spec.Ethereal = &tru
		case tok == "noe":
			spec.Ethereal = &fls
		case tok == "nos":
			spec.NoSockets = true
		case tok == "sock":
			spec.RequireSocketed = true
		case tok == "nru":
			spec.NotRuneword = true
		case strings.HasPrefix(tok, "sock="):
			if n, err := strconv.Atoi(tok[len("sock="):]); err == nil {
				spec.Sockets = n
			} else {
				spec.Unknown = append(spec.Unknown, tok)
			}
		case strings.HasPrefix(tok, "qty="):
			if n, err := strconv.Atoi(tok[len("qty="):]); err == nil && n >= 1 {
				spec.Quantity = n
			} else {
				spec.Unknown = append(spec.Unknown, tok)
			}
		default:
			spec.Unknown = append(spec.Unknown, tok)
		}
	}
	return spec
}

func parseOutputSpec(raw string) OutputSpec {
	out := OutputSpec{Raw: raw, Quantity: 1}
	tokens := strings.Split(raw, ",")
	head := strings.TrimSpace(tokens[0])
	switch head {
	case "useitem":
		out.Kind = OutputUseItem
	case "usetype":
		out.Kind = OutputUseType
	default:
		out.Kind = OutputCode
		out.Code = head
	}
	for _, tok := range tokens[1:] {
		tok = strings.TrimSpace(tok)
		switch {
		case tok == "mod":
			out.Regenerate = true
		case qualityTokens[tok] != 0:
			out.Quality = qualityTokens[tok]
		case strings.HasPrefix(tok, "qty="):
			if n, err := strconv.Atoi(tok[len("qty="):]); err == nil && n >= 1 {
				out.Quantity = n
			}
		// remaining qualifiers (sock=, eth, rep, ...) do not affect
		// feasibility or determinism classification enough to track yet.
		default:
		}
	}
	return out
}

func (t *Tables) loadRecipes(path string) error {
	tab, err := readTSV(path)
	if err != nil {
		return err
	}
	skip := func(reason string) {
		t.Stats.RecipesSkipped++
		t.Stats.SkipReasons[reason]++
	}

	tab.each(func(r tsvRow) {
		desc := r.get("description")
		if isCommentRow(desc) {
			skip("comment row")
			return
		}
		if r.get("enabled") != "1" {
			skip("disabled")
			return
		}
		numInputs := r.getInt("numinputs")
		if numInputs < 1 {
			skip("bad numinputs")
			return
		}

		rec := Recipe{
			Description: desc,
			NumInputs:   numInputs,
			Op:          r.getInt("op"),
			OpParam:     r.getInt("param"),
			OpValue:     r.getInt("value"),
			Lvl:         r.getInt("lvl"),
			Plvl:        r.getInt("plvl"),
			Ilvl:        r.getInt("ilvl"),
		}

		for i := 1; i <= 7; i++ {
			cell := strings.Trim(r.get("input "+strconv.Itoa(i)), `"`)
			if cell == "" {
				continue
			}
			spec := parseInputSpec(cell)
			// Fail closed: an input whose code is neither a known item code
			// nor a known type code (the mod uses raw item NAMES in ~45 rows)
			// cannot be matched against bot items, so the recipe is unusable.
			if _, isItem := t.Items[spec.Code]; !isItem {
				if _, isType := t.Types[spec.Code]; !isType {
					skip("unknown input code: " + spec.Code)
					return
				}
			}
			rec.Inputs = append(rec.Inputs, spec)
		}
		if len(rec.Inputs) == 0 {
			skip("no inputs")
			return
		}

		outCell := strings.Trim(r.get("output"), `"`)
		if outCell == "" {
			skip("no output")
			return
		}
		rec.Output = parseOutputSpec(outCell)
		if rec.Output.Kind == OutputCode {
			if _, ok := t.Items[rec.Output.Code]; !ok {
				skip("unknown output code: " + rec.Output.Code)
				return
			}
		}

		for i := 1; i <= 5; i++ {
			p := "mod " + strconv.Itoa(i)
			code := r.get(p)
			if code == "" {
				continue
			}
			rec.Mods = append(rec.Mods, RecipeMod{
				Code:   code,
				Chance: r.getInt(p + " chance"),
				Param:  r.get(p + " param"),
				Min:    r.getInt(p + " min"),
				Max:    r.getInt(p + " max"),
			})
		}

		t.Recipes = append(t.Recipes, rec)
		t.Stats.RecipesParsed++
	})
	return nil
}

// ---------------------------------------------------------------------------
// matching bot-side items

// Matches reports whether a live d2go item satisfies an input spec.
// Unknown qualifiers fail closed (never claim feasibility we can't verify).
// Quantity is NOT checked here — it is a multiset concern handled by the
// recipe feasibility search, which needs Quantity distinct matching items.
func (t *Tables) Matches(spec InputSpec, it data.Item) bool {
	if len(spec.Unknown) > 0 {
		return false
	}
	code := it.Desc().Code
	if !t.IsA(code, spec.Code) {
		return false
	}
	if spec.Quality != 0 && it.Quality != spec.Quality {
		return false
	}
	if spec.Tier != "" {
		d := it.Desc()
		switch spec.Tier {
		case "bas":
			if d.Code != d.NormalCode {
				return false
			}
		case "exc":
			if d.Code != d.UberCode {
				return false
			}
		case "eli":
			if d.Code != d.UltraCode {
				return false
			}
		}
	}
	if spec.Ethereal != nil && it.Ethereal != *spec.Ethereal {
		return false
	}
	socketCount := t.socketCount(it)
	if spec.NoSockets && socketCount != 0 {
		return false
	}
	if spec.RequireSocketed && socketCount == 0 {
		return false
	}
	if spec.Sockets >= 0 && socketCount != spec.Sockets {
		return false
	}
	if spec.NotRuneword && it.IsRuneword {
		return false
	}
	return true
}

// socketCount: total sockets on the item (filled + empty). The memory reader
// exposes filled sockets as child items and the total as stat NumSockets;
// fall back to filled-count when the stat is absent.
func (t *Tables) socketCount(it data.Item) int {
	if st, ok := it.FindStat(stat.NumSockets, 0); ok {
		return st.Value
	}
	if len(it.Sockets) > 0 || it.HasSockets {
		return len(it.Sockets)
	}
	return 0
}
