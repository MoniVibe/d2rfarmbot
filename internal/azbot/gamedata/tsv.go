package gamedata

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// table is one excel .txt file: a header row and tab-separated data rows,
// addressed by column NAME (case-insensitive) — mod tables move columns, so
// nothing here indexes positionally.
type table struct {
	name string
	cols map[string]int
	rows [][]string
}

// row is one data row with header-name access.
type row struct {
	t *table
	f []string
}

// parseTable reads a tab-separated table. Tolerant of a UTF-8 BOM, CRLF line
// ends, blank lines and short rows (missing trailing fields read as "").
func parseTable(name string, r io.Reader) (*table, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)
	t := &table{name: name, cols: map[string]int{}}
	header := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if header {
			line = strings.TrimPrefix(line, "\uFEFF")
			if strings.TrimSpace(line) == "" {
				continue
			}
			for i, h := range strings.Split(line, "\t") {
				h = strings.ToLower(strings.TrimSpace(h))
				if _, dup := t.cols[h]; !dup && h != "" { // first occurrence wins
					t.cols[h] = i
				}
			}
			header = false
			continue
		}
		f := strings.Split(line, "\t")
		if allBlank(f) {
			continue // a blank line is not a row: the game never counts it
		}
		t.rows = append(t.rows, f)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("gamedata: reading %s: %w", name, err)
	}
	if header {
		return nil, fmt.Errorf("gamedata: %s: empty or missing header", name)
	}
	return t, nil
}

func allBlank(f []string) bool {
	for _, s := range f {
		if strings.TrimSpace(s) != "" {
			return false
		}
	}
	return true
}

// isExpansionRow is the classic/expansion divider ("Expansion" in the first
// column). The game skips it when it numbers rows: weapons.txt's Tomahawk sits
// on file row 197 and is item class 196; monstats' nihlathakboss sits on row
// 527 and is monster class 526 (its own *hcIdx comment agrees).
func isExpansionRow(f []string) bool {
	return len(f) > 0 && strings.EqualFold(strings.TrimSpace(f[0]), "Expansion")
}

// each visits every counted row with its game index (txtFileNo / class id):
// Expansion dividers are skipped and do not advance the index.
func (t *table) each(fn func(idx int, r row)) {
	idx := 0
	for _, f := range t.rows {
		if isExpansionRow(f) {
			continue
		}
		fn(idx, row{t: t, f: f})
		idx++
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// firstCol is the header name of column 0.
func (t *table) firstCol() string {
	for name, i := range t.cols {
		if i == 0 {
			return name
		}
	}
	return ""
}

// str is the trimmed field under col ("" when the column or field is absent).
func (r row) str(col string) string {
	i, ok := r.t.cols[strings.ToLower(col)]
	if !ok || i >= len(r.f) {
		return ""
	}
	return strings.TrimSpace(r.f[i])
}

// int reads an integer field; blank or malformed reads 0.
func (r row) int(col string) int {
	n, _ := r.intOK(col)
	return n
}

// intOK reads an integer field and whether it held one.
func (r row) intOK(col string) (int, bool) {
	s := r.str(col)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		if f, ferr := strconv.ParseFloat(s, 64); ferr == nil {
			return int(f), true
		}
		return 0, false
	}
	return n, true
}

// intOr reads an integer field, def when blank or malformed.
func (r row) intOr(col string, def int) int {
	if n, ok := r.intOK(col); ok {
		return n
	}
	return def
}

// bool: any non-zero integer is true.
func (r row) bool(col string) bool { return r.int(col) != 0 }

// openTable finds name in dir (case-insensitively: the game's own tree mixes
// cases) and parses it. os.ErrNotExist when absent.
func openTable(dir, name string) (*table, error) {
	p, err := findFile(dir, name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseTable(name, f)
}

func findFile(dir, name string) (string, error) {
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range ents {
		if !e.IsDir() && strings.EqualFold(e.Name(), name) {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("gamedata: %s: %w", p, os.ErrNotExist)
}
