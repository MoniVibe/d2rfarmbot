package loot

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Census counts what the loot policy saw, picked and skipped, by tier, over
// a reporting window — the "loot census" log line.
type Census struct {
	every   time.Duration
	start   time.Time
	seen    map[uint32]Tier // unit → tier (first valuation this window)
	picked  [4]int
	skipped map[string]int // "tier:reason" → count (one per unit)
	skipU   map[uint32]bool
}

// NewCensus reports every d (a floor of 10s).
func NewCensus(d time.Duration, now time.Time) *Census {
	if d < 10*time.Second {
		d = 10 * time.Second
	}
	c := &Census{every: d}
	c.reset(now)
	return c
}

func (c *Census) reset(now time.Time) {
	c.start = now
	c.seen = map[uint32]Tier{}
	c.picked = [4]int{}
	c.skipped = map[string]int{}
	c.skipU = map[uint32]bool{}
}

// Seen notes a ground unit's tier (first sighting per window counts).
func (c *Census) Seen(unit uint32, t Tier) {
	if _, ok := c.seen[unit]; !ok {
		c.seen[unit] = t
	}
}

// Picked counts a unit that landed in the bag (or belt, or purse).
func (c *Census) Picked(t Tier) { c.picked[t]++ }

// Skipped counts one unit left behind, once per window, by reason class.
func (c *Census) Skipped(unit uint32, t Tier, reason string) {
	if c.skipU[unit] {
		return
	}
	c.skipU[unit] = true
	c.skipped[t.String()+":"+reason]++
}

// Line returns the census line when the window is over (and opens the next).
func (c *Census) Line(now time.Time) (string, bool) {
	if now.Sub(c.start) < c.every {
		return "", false
	}
	var seen [4]int
	for _, t := range c.seen {
		seen[t]++
	}
	keys := make([]string, 0, len(c.skipped))
	for k := range c.skipped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, c.skipped[k]))
	}
	skips := strings.Join(parts, ",")
	if skips == "" {
		skips = "none"
	}
	line := fmt.Sprintf("window=%s seen=S%d/A%d/B%d/C%d picked=S%d/A%d/B%d/C%d skipped=%s",
		now.Sub(c.start).Round(time.Second),
		seen[TierS], seen[TierA], seen[TierB], seen[TierC],
		c.picked[TierS], c.picked[TierA], c.picked[TierB], c.picked[TierC], skips)
	c.reset(now)
	return line, true
}

// ReasonClass folds a plan's free-text why into a short census bucket.
func ReasonClass(p Plan) string {
	w := p.Why
	switch {
	case p.Act != Skip:
		return p.Act.String()
	case strings.Contains(w, "urgent"):
		return "urgent"
	case strings.Contains(w, "bag full"):
		return "full"
	case strings.Contains(w, "gated"):
		return "gated"
	case strings.Contains(w, "belt full"):
		return "belt"
	case p.Verdict.Tier <= TierB:
		return "policy"
	}
	return "other"
}

// CatalogEntry is one item row as the owner reviews it (logs/loot_catalog.jsonl
// and the memory fact loot.catalog.<id>).
type CatalogEntry struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`               // d2go's (scrambled) table name
	VanillaID int    `json:"vanilla_id"`         // -1: a mod row past the vanilla table
	Code      string `json:"code,omitempty"`     // the vanilla code after the +15 shift
	Type      string `json:"type,omitempty"`     // the vanilla type code
	Kind      string `json:"kind"`               // what the policy thinks it is
	Quality   string `json:"quality"`            // first quality seen
	Tier      string `json:"tier"`               // the tier the policy gave it
	Why       string `json:"why"`                // why that tier
	Area      int    `json:"area"`               // where it was first seen
	Where     string `json:"where"`              // "ground" or "bag"
	First     string `json:"first"`              // RFC3339 first sighting
	TagHint   string `json:"tag_hint,omitempty"` // how to tag it in config/loot.yaml
}

// Catalog remembers which item rows (id+quality) were ever seen.
type Catalog struct {
	known map[string]bool
}

func NewCatalog() *Catalog { return &Catalog{known: map[string]bool{}} }

// Key is the catalog identity: the row and the quality it came in.
func CatalogKey(id, quality int) string { return fmt.Sprintf("%d.q%d", id, quality) }

// Has reports whether key was cataloged this session (or marked known).
func (c *Catalog) Has(key string) bool { return c.known[key] }

// Know marks a key as already cataloged (a memory fact from an earlier run).
func (c *Catalog) Know(key string) { c.known[key] = true }

// Note returns the entry for a first sighting (isNew=false after that).
func (c *Catalog) Note(it Item, v Verdict, area int, where string, now time.Time) (CatalogEntry, bool) {
	key := CatalogKey(it.ID, it.Quality)
	if c.known[key] {
		return CatalogEntry{}, false
	}
	c.known[key] = true
	e := CatalogEntry{ID: it.ID, Name: it.Name, VanillaID: v.Class.VanillaID, Code: v.Class.Code,
		Type: v.Class.Type, Kind: v.Class.Kind.String(), Quality: QualityName(it.Quality),
		Tier: v.Tier.String(), Why: v.Why, Area: area, Where: where, First: now.Format(time.RFC3339)}
	if v.Class.Kind == KindModUnknown || strings.Contains(v.Why, "table is wrong") {
		e.TagHint = fmt.Sprintf("ids: { %d: S }   # or A/B/C once you know what it is", it.ID)
	}
	return e, true
}
