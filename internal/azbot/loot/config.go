package loot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// Tier is an item's worth to her. Higher is better.
type Tier uint8

const (
	TierC Tier = iota // junk: never picked; sold if carried
	TierB             // sell-grade: skipped on the ground; sold if carried
	TierA             // take if room, or swap for worse carried stuff
	TierS             // always take; make room (drop junk, town trip) if needed
)

func (t Tier) String() string {
	switch t {
	case TierS:
		return "S"
	case TierA:
		return "A"
	case TierB:
		return "B"
	}
	return "C"
}

// ParseTier reads "S", "A", "B" or "C" (any case).
func ParseTier(s string) (Tier, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "S":
		return TierS, nil
	case "A":
		return TierA, nil
	case "B":
		return TierB, nil
	case "C":
		return TierC, nil
	}
	return TierC, fmt.Errorf("tier %q: want S, A, B or C", s)
}

// Defaults: the tier for each class of item when no explicit tag matches.
type Defaults struct {
	Unique, Set, Rare, Crafted Tier
	MagicJewelry, MagicGear    Tier
	PlainGear                  Tier // normal / superior / low-quality bases
	Rune, Gem, Jewel, Charm    Tier
	Gold, Potion, Quest, Ammo  Tier
	Scroll, Misc               Tier
	UnknownModItem             Tier // rows past the vanilla table: the mod's own items
	QualityContradiction       Tier // a row that cannot carry the quality it shows
}

// Space tunes the bag-full behaviour.
type Space struct {
	PickTierB    bool // take tier B when the bag is roomy and the march is not urgent
	SwapForA     bool // a tier A drop may displace a worse carried item (never while the march is urgent)
	SwapForS     bool // a tier S drop may displace a worse carried item
	TownTripForS bool // a tier S drop may send her to town (Fence) and back through the portal
	// SellRaresPct: once the bag is this full (percent of cells), identified
	// rare gear that is not an upgrade becomes merchandise for the Fence.
	SellRaresPct int
}

// NameRule tags items whose table name contains Sub (lower case).
type NameRule struct {
	Sub  string
	Tier Tier
}

// Config is the whole loot policy.
type Config struct {
	Defaults    Defaults
	IDs         map[int]Tier
	Codes       map[string]Tier
	Names       []NameRule // longest substring first
	Space       Space
	CensusEvery time.Duration
	Source      string // the file it came from, or "built-in defaults"
}

// Default is the policy shipped in code — config/loot.yaml mirrors it.
func Default() *Config {
	return &Config{
		Defaults: Defaults{
			// OWNER RULING (2026-09-24, watching relay R13): "for now pick up
			// only uniques and special items (orbs)" — the wide net looped on
			// piles. Special = quest items and the mod's own rows (orbs,
			// currencies). Widen here once pickups are proven reliable.
			Unique: TierS, Set: TierC, Rare: TierC, Crafted: TierC,
			MagicJewelry: TierC, MagicGear: TierC, PlainGear: TierC,
			Rune: TierC, Gem: TierC, Jewel: TierC, Charm: TierC,
			Gold: TierC, Potion: TierC, Quest: TierS, Ammo: TierC,
			Scroll: TierC, Misc: TierC,
			UnknownModItem: TierS, QualityContradiction: TierC,
		},
		IDs:   map[int]Tier{},
		Codes: map[string]Tier{},
		Space: Space{SwapForA: false, SwapForS: true, TownTripForS: true, SellRaresPct: 70},
		// A census every three minutes: often enough to read a run by, rare
		// enough not to drown the log.
		CensusEvery: 3 * time.Minute,
		Source:      "built-in defaults",
	}
}

// rawConfig is the YAML shape (strings, validated by Parse).
type rawConfig struct {
	Tiers       map[string]string `yaml:"tiers"`
	IDs         map[int]string    `yaml:"ids"`
	Codes       map[string]string `yaml:"codes"`
	Names       map[string]string `yaml:"names"`
	Space       *rawSpace         `yaml:"space"`
	CensusEvery string            `yaml:"census_every"`
}

type rawSpace struct {
	PickTierB    *bool `yaml:"pick_tier_b"`
	SwapForA     *bool `yaml:"swap_for_a"`
	SwapForS     *bool `yaml:"swap_for_s"`
	TownTripForS *bool `yaml:"town_trip_for_s"`
	SellRaresPct *int  `yaml:"sell_rares_pct"`
}

// defaultSlots maps the YAML tier keys onto Defaults fields.
func defaultSlots(d *Defaults) map[string]*Tier {
	return map[string]*Tier{
		"unique": &d.Unique, "set": &d.Set, "rare": &d.Rare, "crafted": &d.Crafted,
		"magic_jewelry": &d.MagicJewelry, "magic_gear": &d.MagicGear, "plain_gear": &d.PlainGear,
		"rune": &d.Rune, "gem": &d.Gem, "jewel": &d.Jewel, "charm": &d.Charm,
		"gold": &d.Gold, "potion": &d.Potion, "quest": &d.Quest, "ammo": &d.Ammo,
		"scroll": &d.Scroll, "misc": &d.Misc,
		"unknown_mod_item": &d.UnknownModItem, "quality_contradiction": &d.QualityContradiction,
	}
}

// Parse reads a loot.yaml document over the built-in defaults. Unknown keys
// and bad tiers are errors — a typo must not silently change what she loots.
func Parse(b []byte) (*Config, error) {
	c := Default()
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("loot config: %w", err)
	}
	slots := defaultSlots(&c.Defaults)
	for k, v := range raw.Tiers {
		slot, ok := slots[strings.ToLower(k)]
		if !ok {
			return nil, fmt.Errorf("loot config: tiers.%s is not a known class", k)
		}
		t, err := ParseTier(v)
		if err != nil {
			return nil, fmt.Errorf("loot config: tiers.%s: %w", k, err)
		}
		*slot = t
	}
	for id, v := range raw.IDs {
		t, err := ParseTier(v)
		if err != nil {
			return nil, fmt.Errorf("loot config: ids.%d: %w", id, err)
		}
		c.IDs[id] = t
	}
	for code, v := range raw.Codes {
		t, err := ParseTier(v)
		if err != nil {
			return nil, fmt.Errorf("loot config: codes.%s: %w", code, err)
		}
		c.Codes[strings.ToLower(code)] = t
	}
	for sub, v := range raw.Names {
		t, err := ParseTier(v)
		if err != nil {
			return nil, fmt.Errorf("loot config: names.%s: %w", sub, err)
		}
		if s := strings.ToLower(strings.TrimSpace(sub)); s != "" {
			c.Names = append(c.Names, NameRule{Sub: s, Tier: t})
		}
	}
	// Longest substring first (then alphabetical): the most specific tag wins
	// and the order never depends on map iteration.
	sort.Slice(c.Names, func(i, j int) bool {
		if len(c.Names[i].Sub) != len(c.Names[j].Sub) {
			return len(c.Names[i].Sub) > len(c.Names[j].Sub)
		}
		return c.Names[i].Sub < c.Names[j].Sub
	})
	if s := raw.Space; s != nil {
		set := func(dst *bool, src *bool) {
			if src != nil {
				*dst = *src
			}
		}
		set(&c.Space.PickTierB, s.PickTierB)
		set(&c.Space.SwapForA, s.SwapForA)
		set(&c.Space.SwapForS, s.SwapForS)
		set(&c.Space.TownTripForS, s.TownTripForS)
		if s.SellRaresPct != nil {
			if *s.SellRaresPct < 0 || *s.SellRaresPct > 100 {
				return nil, fmt.Errorf("loot config: space.sell_rares_pct %d: want 0..100", *s.SellRaresPct)
			}
			c.Space.SellRaresPct = *s.SellRaresPct
		}
	}
	if raw.CensusEvery != "" {
		d, err := time.ParseDuration(raw.CensusEvery)
		if err != nil || d < 10*time.Second {
			return nil, fmt.Errorf("loot config: census_every %q: want a duration ≥ 10s", raw.CensusEvery)
		}
		c.CensusEvery = d
	}
	c.Source = "yaml"
	return c, nil
}

// Load reads path. A missing file is not an error: the built-in defaults
// serve (ok=false tells the caller to say so).
func Load(path string) (c *Config, ok bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), false, nil
		}
		return Default(), false, err
	}
	c, err = Parse(b)
	if err != nil {
		return Default(), false, err
	}
	c.Source = path
	return c, true, nil
}

var active atomic.Pointer[Config]

// Active is the policy in force (the built-in defaults until SetActive).
func Active() *Config {
	if c := active.Load(); c != nil {
		return c
	}
	return Default()
}

// SetActive installs c as the policy in force.
func SetActive(c *Config) { active.Store(c) }
