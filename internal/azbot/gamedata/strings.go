package gamedata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Strings is the game's English string table: every strings/*.json file's
// {Key → enUS}. Keys are global across files; the first file in stringFiles
// order that defines a key wins (the item files first, so an item's namestr
// never resolves to a UI string of the same key).
type Strings struct {
	m     map[string]string
	per   map[string]map[string]string // file (lower-case) → its own keys
	Files []string                     // the files read, in precedence order
	Dups  int                          // keys defined again by a later file (ignored)
}

var stringFiles = []string{"item-names.json", "item-runes.json", "item-nameaffixes.json",
	"item-modifiers.json", "monsters.json", "levels.json", "skills.json", "ui.json"}

type stringEntry struct {
	ID   int    `json:"id"`
	Key  string `json:"Key"`
	EnUS string `json:"enUS"`
}

func newStrings() *Strings {
	return &Strings{m: map[string]string{}, per: map[string]map[string]string{}}
}

// loadStrings reads dir/*.json: the known files first (stringFiles order),
// then any others alphabetically.
func loadStrings(dir string) (*Strings, error) {
	s := newStrings()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return s, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			names = append(names, e.Name())
		}
	}
	rank := func(n string) int {
		for i, k := range stringFiles {
			if strings.EqualFold(k, n) {
				return i
			}
		}
		return len(stringFiles)
	}
	sort.SliceStable(names, func(i, j int) bool {
		ri, rj := rank(names[i]), rank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return s, err
		}
		if err := s.add(n, b); err != nil {
			return s, err
		}
	}
	return s, nil
}

func (s *Strings) add(name string, b []byte) error {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	var es []stringEntry
	if err := json.Unmarshal(b, &es); err != nil {
		return fmt.Errorf("gamedata: strings %s: %w", name, err)
	}
	own := map[string]string{}
	s.per[strings.ToLower(name)] = own
	for _, e := range es {
		if e.Key == "" {
			continue
		}
		if _, dup := own[e.Key]; !dup {
			own[e.Key] = e.EnUS
		}
		if _, dup := s.m[e.Key]; dup {
			s.Dups++
			continue
		}
		s.m[e.Key] = e.EnUS
	}
	s.Files = append(s.Files, name)
	return nil
}

// Raw is the enUS text for key exactly as the game stores it (color codes,
// newlines and all).
func (s *Strings) Raw(key string) (string, bool) {
	if s == nil || key == "" {
		return "", false
	}
	v, ok := s.m[key]
	return v, ok
}

// Name is the cleaned enUS text for key (see Clean), fallback when absent or blank.
func (s *Strings) Name(key, fallback string) string {
	if v, ok := s.Raw(key); ok {
		if c := Clean(v); c != "" {
			return c
		}
	}
	return fallback
}

// NameIn is Name, looking in file (e.g. "skills.json") first: a domain's own
// file wins over a same-named key elsewhere.
func (s *Strings) NameIn(file, key, fallback string) string {
	if s != nil && key != "" {
		if v, ok := s.per[strings.ToLower(file)][key]; ok {
			if c := Clean(v); c != "" {
				return c
			}
		}
	}
	return s.Name(key, fallback)
}

// Len is the number of keys.
func (s *Strings) Len() int {
	if s == nil {
		return 0
	}
	return len(s.m)
}

// Clean turns a display string into a plain name: color codes (ÿc + one
// character) and a leading gender tag ("[ms]") are removed, and only the
// first line is kept — D2R stacks later lines ABOVE the name, and the mod
// uses them for tags ("~Pick Up~") and rune numbers, not for the name itself.
func Clean(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 'ÿ' && i+1 < len(rs) && rs[i+1] == 'c' {
			i += 2 // skip ÿ, c and the color character
			continue
		}
		b.WriteRune(rs[i])
	}
	out := strings.TrimSpace(b.String())
	if len(out) >= 4 && out[0] == '[' && out[3] == ']' {
		out = strings.TrimSpace(out[4:])
	}
	return out
}
